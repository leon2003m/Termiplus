package sockproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"termiplus/internal/naming"
)

// Rules is the complete policy the proxy enforces. Anything not expressible
// here cannot be requested through the proxy.
type Rules struct {
	Image string // exact image reference, ideally pinned by digest
	// ExtraImages are further exact image references terminals may use (one
	// per policy that names its own "image"). Image is always allowed.
	ExtraImages []string
	Runtimes    map[string]bool // allowed OCI runtimes, e.g. runsc, sysbox-runc
	Network     string          // the only network terminals may join
	MaxMemory   int64           // bytes
	MaxNanoCPUs int64
	MaxPids     int64
	// AllowedDNS is the single resolver address a terminal may be pinned to
	// (the internal forwarder). Empty means HostConfig.Dns is refused, as
	// before. Any other value, more than one entry, DnsSearch or DnsOptions
	// are always refused.
	AllowedDNS string
	// HomeVolumeSize, when set (e.g. "20G", see ParseVolumeSize), is the disk
	// quota the proxy puts on every home volume it creates. Docker enforces it
	// with project quotas, which needs the volume directory on XFS mounted
	// with prjquota; without that support Docker refuses to create the
	// volume, so the limit can never be silently skipped. Empty: no limit.
	HomeVolumeSize string
	// RootfsSize, when set (same format), caps the writable container layer
	// of every terminal (HostConfig.StorageOpt size): everything outside the
	// home volume, and all of an ephemeral sandbox. Docker supports it with
	// overlay2 on XFS mounted with pquota (and btrfs/zfs storage drivers);
	// elsewhere it refuses to create the container. Empty: no limit.
	RootfsSize string
	// AllowHomeRemove permits DELETE /volumes/<home volume> (no force; Docker
	// refuses volumes still used by a container). Off by default: homes are
	// then never deleted through the proxy. The gateway needs it only to
	// archive the homes of departed users.
	AllowHomeRemove bool
}

// ImageAllowed reports whether ref is one of the exact image references
// terminals may run.
func (r *Rules) ImageAllowed(ref string) bool {
	if ref == "" {
		return false
	}
	if ref == r.Image {
		return true
	}
	for _, x := range r.ExtraImages {
		if ref == x {
			return true
		}
	}
	return false
}

var imageRefPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:@-]{0,254}$`)

// ParseImageRef checks an image reference given in the configuration.
func ParseImageRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if !imageRefPattern.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.HasSuffix(ref, "/") {
		return "", fmt.Errorf("image reference %q not allowed", ref)
	}
	return ref, nil
}

var volumeSizePattern = regexp.MustCompile(`^([1-9][0-9]{0,5})([MGT])$`)

// ParseVolumeSize checks a home volume quota such as "20G" (binary units M,
// G or T; at least 64M) and returns it in the form handed to Docker.
func ParseVolumeSize(v string) (string, error) {
	v = strings.ToUpper(strings.TrimSpace(v))
	m := volumeSizePattern.FindStringSubmatch(v)
	if m == nil {
		return "", fmt.Errorf("volume size %q must look like 512M, 20G or 1T", v)
	}
	if n, _ := strconv.Atoi(m[1]); m[2] == "M" && n < 64 {
		return "", fmt.Errorf("volume size %q is below 64M", v)
	}
	return v, nil
}

const maxBody = 64 << 10

var envPattern = regexp.MustCompile(`^OPEN_TERMINAL_[A-Z0-9_]{1,64}=[^\x00\r\n]{0,1000}$`)

// --- container create -------------------------------------------------------

// The structs below list every field a client may send. Decoding uses
// DisallowUnknownFields, so Privileged, Binds, CapAdd, Devices, SecurityOpt,
// PidMode, VolumeOptions and everything else are rejected by construction.

type createMount struct {
	Type     string `json:"Type"`
	Source   string `json:"Source"`
	Target   string `json:"Target"`
	ReadOnly bool   `json:"ReadOnly,omitempty"`
}

type createHostConfig struct {
	Runtime     string        `json:"Runtime"`
	NetworkMode string        `json:"NetworkMode"`
	Memory      int64         `json:"Memory"`
	MemorySwap  int64         `json:"MemorySwap,omitempty"`
	NanoCpus    int64         `json:"NanoCpus"`
	PidsLimit   *int64        `json:"PidsLimit"`
	Mounts      []createMount `json:"Mounts"`
	// Dns, when present, must be exactly [Rules.AllowedDNS]. DnsSearch and
	// DnsOptions are intentionally absent, so DisallowUnknownFields refuses them.
	Dns []string `json:"Dns,omitempty"`
}

type createRequest struct {
	Image      string            `json:"Image"`
	Env        []string          `json:"Env,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
	HostConfig createHostConfig  `json:"HostConfig"`
}

// ValidateCreate checks a container name and create body and returns the
// body to forward. The forwarded body is rebuilt from validated values only.
func (r *Rules) ValidateCreate(name string, body io.Reader) ([]byte, error) {
	ephemeral := naming.EphemeralName.MatchString(name)
	helper := naming.ArchiveName.MatchString(name)
	if !ephemeral && !helper && !naming.ContainerName.MatchString(name) {
		return nil, fmt.Errorf("container name %q not allowed", name)
	}
	userHash := naming.UserHash(name)

	var req createRequest
	if err := strictDecode(body, &req); err != nil {
		return nil, err
	}

	if !r.ImageAllowed(req.Image) {
		return nil, fmt.Errorf("image %q not allowed", req.Image)
	}
	hc := req.HostConfig
	if !r.Runtimes[hc.Runtime] {
		return nil, fmt.Errorf("runtime %q not allowed", hc.Runtime)
	}
	if hc.NetworkMode != r.Network {
		return nil, fmt.Errorf("network %q not allowed", hc.NetworkMode)
	}
	if hc.Memory <= 0 || hc.Memory > r.MaxMemory {
		return nil, fmt.Errorf("memory limit missing or above %d", r.MaxMemory)
	}
	if hc.NanoCpus <= 0 || hc.NanoCpus > r.MaxNanoCPUs {
		return nil, fmt.Errorf("cpu limit missing or above %d", r.MaxNanoCPUs)
	}
	if hc.PidsLimit == nil || *hc.PidsLimit <= 0 || *hc.PidsLimit > r.MaxPids {
		return nil, fmt.Errorf("pids limit missing or above %d", r.MaxPids)
	}
	// A terminal may be pinned to exactly the configured forwarder and nothing
	// else. This is the only place a resolver address can enter a container.
	if len(hc.Dns) > 0 {
		if r.AllowedDNS == "" {
			return nil, fmt.Errorf("dns not allowed")
		}
		if len(hc.Dns) != 1 || hc.Dns[0] != r.AllowedDNS {
			return nil, fmt.Errorf("dns must be exactly [%q]", r.AllowedDNS)
		}
	}
	var mounts []map[string]any
	if ephemeral {
		// Ephemeral sandboxes get no storage at all: everything lives in the
		// container layer and disappears with the container.
		if len(hc.Mounts) != 0 {
			return nil, fmt.Errorf("ephemeral containers may not mount anything")
		}
	} else {
		if len(hc.Mounts) != 1 {
			return nil, fmt.Errorf("exactly one mount required")
		}
		mt := hc.Mounts[0]
		if mt.Type != "volume" || mt.Target != naming.HomeDir || mt.ReadOnly {
			return nil, fmt.Errorf("mount must be a writable volume at %s", naming.HomeDir)
		}
		// The volume must belong to the same user as the container. Policies of
		// one user may share a home volume (e.g. terminal and desktop).
		if !naming.VolumeName.MatchString(mt.Source) || naming.UserHash(mt.Source) != userHash {
			return nil, fmt.Errorf("volume %q does not belong to %q", mt.Source, name)
		}
		// An archive helper mounts exactly the home it is named after.
		if helper && naming.ArchiveFor(mt.Source) != name {
			return nil, fmt.Errorf("archive helper %q must mount its own home volume", name)
		}
		mounts = []map[string]any{{"Type": "volume", "Source": mt.Source, "Target": naming.HomeDir}}
	}
	if len(req.Env) > 32 {
		return nil, fmt.Errorf("too many env entries")
	}
	for _, e := range req.Env {
		if !envPattern.MatchString(e) {
			return nil, fmt.Errorf("env entry not allowed: %q", envKey(e))
		}
	}
	labels, err := sanitizeLabels(req.Labels)
	if err != nil {
		return nil, err
	}

	pids := *hc.PidsLimit
	var dns []string
	if len(hc.Dns) == 1 {
		dns = []string{hc.Dns[0]}
	}
	out := map[string]any{
		"Image":    req.Image,
		"Env":      req.Env,
		"Labels":   labels,
		"Hostname": "terminal",
		"HostConfig": map[string]any{
			"Runtime":        hc.Runtime,
			"NetworkMode":    r.Network,
			"Memory":         hc.Memory,
			"MemorySwap":     hc.Memory, // no swap beyond the memory limit
			"NanoCpus":       hc.NanoCpus,
			"PidsLimit":      pids,
			"Privileged":     false,
			"ReadonlyRootfs": false,
			"Mounts":         mounts,
			"Dns":            dns,
			// Ephemeral containers are deleted by Docker as soon as they stop.
			"AutoRemove":    ephemeral,
			"RestartPolicy": map[string]any{"Name": "no"},
			"LogConfig": map[string]any{
				"Type":   "json-file",
				"Config": map[string]string{"max-size": "10m", "max-file": "2"},
			},
		},
	}
	if r.RootfsSize != "" {
		out["HostConfig"].(map[string]any)["StorageOpt"] = map[string]string{"size": r.RootfsSize}
	}
	return json.Marshal(out)
}

// --- volume create ----------------------------------------------------------

type volumeRequest struct {
	Name   string            `json:"Name"`
	Driver string            `json:"Driver,omitempty"`
	Labels map[string]string `json:"Labels,omitempty"`
}

// ValidateVolume rejects driver options (a local volume with o=bind is a
// host mount in disguise) and anything not named like a terminal volume. The
// only driver option ever forwarded is the proxy's own size quota.
func (r *Rules) ValidateVolume(body io.Reader) ([]byte, error) {
	var req volumeRequest
	if err := strictDecode(body, &req); err != nil {
		return nil, err
	}
	if !naming.VolumeName.MatchString(req.Name) {
		return nil, fmt.Errorf("volume name %q not allowed", req.Name)
	}
	if req.Driver != "" && req.Driver != "local" {
		return nil, fmt.Errorf("volume driver %q not allowed", req.Driver)
	}
	labels, err := sanitizeLabels(req.Labels)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"Name":   req.Name,
		"Driver": "local",
		"Labels": labels,
	}
	if r.HomeVolumeSize != "" {
		out["DriverOpts"] = map[string]string{"size": r.HomeVolumeSize}
	}
	return json.Marshal(out)
}

// --- helpers ----------------------------------------------------------------

func strictDecode(body io.Reader, v any) error {
	data, err := io.ReadAll(io.LimitReader(body, maxBody+1))
	if err != nil {
		return err
	}
	if len(data) > maxBody {
		return fmt.Errorf("body too large")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("body rejected: %w", err)
	}
	if dec.More() {
		return fmt.Errorf("trailing data in body")
	}
	return nil
}

// Duplicate JSON keys are a classic parser-differential trick; Go keeps the
// last value and the proxy forwards a rebuilt body, so the daemon only ever
// sees what was validated.

func sanitizeLabels(in map[string]string) (map[string]string, error) {
	out := map[string]string{naming.ManagedLabel: naming.ManagedValue}
	for k, v := range in {
		switch k {
		case naming.ManagedLabel:
			continue // always forced
		case naming.LabelUser, naming.LabelPolicy, naming.LabelConfig, naming.LabelStorage:
			if !naming.LabelValue.MatchString(v) {
				return nil, fmt.Errorf("label %s has invalid value", k)
			}
			out[k] = v
		default:
			return nil, fmt.Errorf("label %q not allowed", k)
		}
	}
	return out, nil
}

func envKey(e string) string {
	if i := strings.IndexByte(e, '='); i >= 0 {
		return e[:i]
	}
	return e
}
