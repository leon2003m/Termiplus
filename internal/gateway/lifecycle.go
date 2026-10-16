package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"termiplus/internal/dockerapi"
	"termiplus/internal/naming"
)

// Policy describes one kind of terminal environment. Users reach a policy via
// the URL prefix /p/<name>; Open WebUI group access decides who sees which.
type Policy struct {
	Runtime  string  `json:"runtime"`
	MemoryMB int64   `json:"memory_mb"`
	CPUs     float64 `json:"cpus"`
	Pids     int64   `json:"pids"`
	// Home groups policies that share one home volume per user. Defaults to
	// the policy name. Give terminal and desktop the same value to share files.
	// Ignored for ephemeral policies.
	Home string `json:"home,omitempty"`
	// Storage is "persistent" (default): one container per user with a home
	// volume that survives restarts. "ephemeral": a fresh container per chat
	// with no volume, deleted when idle.
	Storage string `json:"storage,omitempty"`
	// IdleTimeout overrides the global idle timeout, e.g. "10m".
	IdleTimeout string `json:"idle_timeout,omitempty"`
	// MaxPerUser caps concurrent ephemeral sandboxes per user (default 3).
	// When reached, the user's least recently used idle sandbox is deleted.
	MaxPerUser int `json:"max_per_user,omitempty"`
	// Desktop starts the image's XFCE desktop (KasmVNC on desktopPort) next to
	// Open Terminal, protected by a per-container password derived from
	// KEY_SECRET, and makes it reachable under /stream/<container>/. Needs the
	// policy's image (TERMINAL_IMAGE or Image) to be the desktop image
	// (image/desktop). Experimental.
	Desktop bool `json:"desktop,omitempty"`
	// Image runs this policy on another image than TERMINAL_IMAGE (e.g. a
	// data-science or Node image). The socket proxy must list it in
	// EXTRA_IMAGES. It must be built on the termiplus terminal image (Open
	// Terminal on the same port, the same start script).
	Image string `json:"image,omitempty"`

	idle time.Duration
}

func (p Policy) ephemeral() bool { return p.Storage == storageEphemeral }

const (
	storagePersistent = "persistent"
	storageEphemeral  = "ephemeral"
)

type instance struct {
	name      string
	userHash  string
	userID    string // owner, from the request that created it or its label
	ephemeral bool
	idle      time.Duration
	addr      string // host:port reachable from the gateway
	key       string // per-container Open Terminal API key
	ready     bool
	lastUsed  time.Time
	conns     int // open WebSocket sessions (terminal and desktop stream)
	desktop   bool
	// controlSession is the session id of an ephemeral sandbox created through
	// the control API, so it can be listed with a usable handle. In memory only
	// (ephemerals are throwaway); never set for Open WebUI chat sandboxes.
	controlSession string
}

type manager struct {
	docker       *dockerapi.Client
	image        string
	network      string
	port         int
	keySecret    []byte
	startWait    time.Duration
	idle         time.Duration
	maxRunning   int
	policies     map[string]Policy
	addrFor      func(name string, port int) string // overridable in tests
	forwarderDNS string                             // resolver to pin terminals to, or ""

	stats counters

	mu        sync.Mutex
	instances map[string]*instance
	locks     map[string]*sync.Mutex
}

func (m *manager) lockFor(name string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[name]
	if !ok {
		l = &sync.Mutex{}
		m.locks[name] = l
	}
	return l
}

// desktopPassword derives the per-container KasmVNC password the same way, in
// a separate domain (container names never contain a NUL byte).
func (m *manager) desktopPassword(name string) string {
	mac := hmac.New(sha256.New, m.keySecret)
	mac.Write([]byte("desktop\x00" + name))
	return hex.EncodeToString(mac.Sum(nil))
}

// desktopViewPassword is the password of KasmVNC's view-only user "viewer"
// (shared desktops), in its own domain.
func (m *manager) desktopViewPassword(name string) string {
	mac := hmac.New(sha256.New, m.keySecret)
	mac.Write([]byte("desktop-view\x00" + name))
	return hex.EncodeToString(mac.Sum(nil))
}

// containerKey derives the per-container API key. Nothing to store, and
// rotating KEY_SECRET changes the config hash so containers get recreated.
func (m *manager) containerKey(name string) string {
	mac := hmac.New(sha256.New, m.keySecret)
	mac.Write([]byte(name))
	return hex.EncodeToString(mac.Sum(nil))
}

// imageFor returns the image a policy's containers run.
func (m *manager) imageFor(pol Policy) string {
	if pol.Image != "" {
		return pol.Image
	}
	return m.image
}

func (m *manager) configHash(pol Policy, policyName string) string {
	secretFP := sha256.Sum256(m.keySecret)
	// Policies without their own image hash exactly as before, so existing
	// containers are not recreated by this field's introduction.
	b, _ := json.Marshal([]any{m.imageFor(pol), m.network, m.port, pol, policyName, hex.EncodeToString(secretFP[:4]), m.forwarderDNS})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// ensure returns a ready instance for (user, policy) or, for ephemeral
// policies, for (user, session, policy), creating or starting it when needed.
func (m *manager) ensure(ctx context.Context, userID, sessionID, policyName string, pol Policy) (*instance, error) {
	name := naming.Container(userID, policyName)
	if pol.ephemeral() {
		name = naming.Ephemeral(userID, sessionID, policyName)
	}
	l := m.lockFor(name)
	l.Lock()
	defer l.Unlock()

	m.mu.Lock()
	inst := m.instances[name]
	if inst != nil && inst.ready {
		fresh := time.Since(inst.lastUsed) < probeAfter
		m.mu.Unlock()
		// A container unused for a while may have been stopped or removed
		// outside the gateway; check it is still there before routing to it.
		if fresh || m.probe(ctx, inst) {
			m.touch(inst, 0)
			return inst, nil
		}
		log.Printf("%s no longer answers, re-checking container", name)
		m.invalidate(name)
	} else {
		m.mu.Unlock()
	}

	want := m.configHash(pol, policyName)
	ct, err := m.docker.Inspect(ctx, name)
	switch {
	case errors.Is(err, dockerapi.ErrNotFound):
		ct = nil
	case err != nil:
		return nil, err
	}

	if ct != nil {
		if ct.Config.Labels[naming.LabelUser] != userID {
			// 64-bit hash collision or tampering: refuse rather than mix users.
			return nil, fmt.Errorf("container %s belongs to another user", name)
		}
		if ct.Config.Labels[naming.LabelConfig] != want || (pol.ephemeral() && !ct.State.Running) {
			log.Printf("recreating %s: config changed or stopped sandbox", name)
			if err := m.docker.Remove(ctx, name); err != nil {
				return nil, err
			}
			ct = nil
		} else if !pol.ephemeral() && !ct.State.Running && m.imageOutdated(ctx, ct, m.imageFor(pol)) {
			// Same config but the image tag now points to a newer image. Only a
			// stopped workspace is recreated; its home volume is kept.
			log.Printf("recreating %s: stopped and image updated", name)
			m.stats.imageRecreates.Add(1)
			if err := m.docker.Remove(ctx, name); err != nil {
				return nil, err
			}
			ct = nil
		}
	}

	if ct == nil || !ct.State.Running {
		if pol.ephemeral() {
			if err := m.makeRoomForEphemeral(ctx, naming.UserHash(name), pol, name); err != nil {
				return nil, err
			}
		}
		if err := m.checkCapacity(ctx); err != nil {
			return nil, err
		}
	}

	if ct == nil {
		if err := m.create(ctx, name, userID, policyName, pol, want); err != nil {
			return nil, err
		}
	}
	if ct == nil || !ct.State.Running {
		if err := m.docker.Start(ctx, name); err != nil {
			return nil, err
		}
		log.Printf("started %s (policy %s)", name, policyName)
		m.stats.starts.Add(1)
	}

	inst = &instance{
		name:      name,
		userHash:  naming.UserHash(name),
		userID:    userID,
		ephemeral: pol.ephemeral(),
		idle:      m.idleFor(pol),
		addr:      m.addrFor(name, m.port),
		key:       m.containerKey(name),
		desktop:   pol.Desktop,
	}
	if pol.ephemeral() && strings.HasPrefix(sessionID, controlSessionPrefix) {
		inst.controlSession = sessionID
	}
	if err := m.waitReady(ctx, inst); err != nil {
		return nil, err
	}
	inst.ready = true
	inst.lastUsed = time.Now()

	m.mu.Lock()
	if old := m.instances[name]; old != nil {
		inst.conns = old.conns
	}
	m.instances[name] = inst
	m.mu.Unlock()
	return inst, nil
}

// imageOutdated reports whether ct was created from another image than the
// one ref points to now. When that cannot be determined (an older socket
// proxy denies the lookup, the image is missing), the container is kept.
func (m *manager) imageOutdated(ctx context.Context, ct *dockerapi.Container, ref string) bool {
	if ct.Image == "" {
		return false
	}
	id, err := m.docker.ImageID(ctx, ref)
	if err != nil {
		log.Printf("image check for %s skipped: %v", strings.TrimPrefix(ct.Name, "/"), err)
		return false
	}
	return id != ct.Image
}

func (m *manager) checkCapacity(ctx context.Context) error {
	if m.maxRunning <= 0 {
		return nil
	}
	list, err := m.docker.List(ctx)
	if err != nil {
		return err
	}
	running := 0
	for _, c := range list {
		if c.State == "running" {
			running++
		}
	}
	if running >= m.maxRunning {
		return errCapacity
	}
	return nil
}

var (
	errCapacity  = errors.New("too many running terminals, try again later")
	errUserLimit = errors.New("too many temporary sandboxes in use, close one first")
)

func (m *manager) idleFor(pol Policy) time.Duration {
	if pol.idle > 0 {
		return pol.idle
	}
	return m.idle
}

// makeRoomForEphemeral enforces the per-user cap on ephemeral sandboxes. If
// the cap is reached, the user's least recently used sandbox without an open
// terminal session is deleted; if every sandbox is in use, it refuses.
func (m *manager) makeRoomForEphemeral(ctx context.Context, userHash string, pol Policy, self string) error {
	max := pol.MaxPerUser
	if max <= 0 {
		max = 3
	}
	list, err := m.docker.List(ctx)
	if err != nil {
		return err
	}
	var mine []string
	for _, c := range list {
		if len(c.Names) == 0 {
			continue
		}
		n := strings.TrimPrefix(c.Names[0], "/")
		if n != self && naming.EphemeralName.MatchString(n) && naming.UserHash(n) == userHash {
			mine = append(mine, n)
		}
	}
	for len(mine) >= max {
		victim := ""
		var oldest time.Time
		m.mu.Lock()
		for _, n := range mine {
			inst := m.instances[n]
			if inst != nil && inst.conns > 0 {
				continue
			}
			var used time.Time // unknown to this gateway: treat as oldest
			if inst != nil {
				used = inst.lastUsed
			}
			if victim == "" || used.Before(oldest) {
				victim, oldest = n, used
			}
		}
		if victim != "" {
			delete(m.instances, victim)
		}
		m.mu.Unlock()
		if victim == "" {
			return errUserLimit
		}
		if err := m.docker.Remove(ctx, victim); err != nil {
			return err
		}
		log.Printf("deleted %s to make room for a new sandbox", victim)
		m.stats.sandboxesDeleted.Add(1)
		for i, n := range mine {
			if n == victim {
				mine = append(mine[:i], mine[i+1:]...)
				break
			}
		}
	}
	return nil
}

func (m *manager) create(ctx context.Context, name, userID, policyName string, pol Policy, cfg string) error {
	labels := map[string]string{naming.LabelUser: userID, naming.LabelPolicy: policyName}
	mounts := []map[string]any{}
	if pol.ephemeral() {
		labels[naming.LabelStorage] = storageEphemeral
	} else {
		home := pol.Home
		if home == "" {
			home = policyName
		}
		volume := naming.Volume(userID, home)
		// Defense in depth, mirroring the container ownership check above: if a
		// volume of this name already exists, confirm it belongs to this user
		// before reusing it. Guards against a 64-bit user-hash collision handing
		// one user another's persistent home.
		if existing, err := m.docker.InspectVolume(ctx, volume); err == nil {
			if existing.Labels[naming.LabelUser] != userID {
				return fmt.Errorf("volume %s belongs to another user", volume)
			}
		} else if !errors.Is(err, dockerapi.ErrNotFound) {
			return fmt.Errorf("inspect volume: %w", err)
		}
		if err := m.docker.CreateVolume(ctx, volume, labels); err != nil {
			return fmt.Errorf("create volume: %w", err)
		}
		labels[naming.LabelStorage] = storagePersistent
		mounts = append(mounts, map[string]any{"Type": "volume", "Source": volume, "Target": naming.HomeDir})
	}
	labels[naming.LabelConfig] = cfg
	hostConfig := map[string]any{
		"Runtime":     pol.Runtime,
		"NetworkMode": m.network,
		"Memory":      pol.MemoryMB << 20,
		"NanoCpus":    int64(pol.CPUs * 1e9),
		"PidsLimit":   pol.Pids,
		"Mounts":      mounts,
	}
	// Pin the terminal to the internal forwarder when one is configured. The
	// socket proxy independently accepts only this one address, so a tampered
	// gateway cannot point a terminal at any other resolver.
	env := []string{"OPEN_TERMINAL_API_KEY=" + m.containerKey(name)}
	if m.forwarderDNS != "" {
		hostConfig["Dns"] = []string{m.forwarderDNS}
		// Docker still writes its embedded resolver into resolv.conf, which
		// gVisor cannot reach (#3); the image's start script uses this instead.
		env = append(env, "OPEN_TERMINAL_RESOLVER="+m.forwarderDNS)
	}
	if pol.Desktop {
		env = append(env, "OPEN_TERMINAL_DESKTOP_PASSWORD="+m.desktopPassword(name),
			"OPEN_TERMINAL_DESKTOP_VIEW_PASSWORD="+m.desktopViewPassword(name))
	}
	spec := map[string]any{
		"Image":      m.imageFor(pol),
		"Env":        env,
		"Labels":     labels,
		"HostConfig": hostConfig,
	}
	if err := m.docker.Create(ctx, name, spec); err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	log.Printf("created %s (policy %s, runtime %s, storage %s)", name, policyName, pol.Runtime, labels[naming.LabelStorage])
	return nil
}

// probeAfter is how long an instance may be idle before the next request
// first checks that its container still answers.
const probeAfter = 10 * time.Second

func (m *manager) probe(ctx context.Context, inst *instance) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+inst.addr+"/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (m *manager) waitReady(ctx context.Context, inst *instance) error {
	ctx, cancel := context.WithTimeout(ctx, m.startWait)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+inst.addr+"/health", nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s did not become ready: %w", inst.name, ctx.Err())
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (m *manager) invalidate(name string) {
	m.mu.Lock()
	if inst := m.instances[name]; inst != nil {
		inst.ready = false
	}
	m.mu.Unlock()
}

func (m *manager) touch(inst *instance, connDelta int) {
	m.mu.Lock()
	inst.lastUsed = time.Now()
	inst.conns += connDelta
	m.mu.Unlock()
}

// adopt registers already running managed containers after a gateway restart
// so the reaper can stop them once idle.
func (m *manager) adopt(ctx context.Context) {
	list, err := m.docker.List(ctx)
	if err != nil {
		log.Printf("adopt: %v", err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range list {
		if c.State != "running" || len(c.Names) == 0 {
			continue
		}
		name := strings.TrimPrefix(c.Names[0], "/")
		if !naming.IsManagedContainer(name) {
			continue
		}
		if _, ok := m.instances[name]; !ok {
			pol := m.policies[c.Labels[naming.LabelPolicy]]
			m.instances[name] = &instance{
				name:      name,
				userHash:  naming.UserHash(name),
				userID:    c.Labels[naming.LabelUser],
				ephemeral: naming.EphemeralName.MatchString(name),
				idle:      m.idleFor(pol),
				lastUsed:  time.Now(),
				desktop:   pol.Desktop,
			}
		}
	}
}

// reap stops persistent containers and deletes ephemeral ones once they have
// been idle longer than their idle timeout. Persistent volumes stay, so the
// next request simply starts the container again.
func (m *manager) reap(ctx context.Context) {
	now := time.Now()
	var victims []string
	m.mu.Lock()
	for name, inst := range m.instances {
		if inst.conns == 0 && now.Sub(inst.lastUsed) > inst.idle {
			victims = append(victims, name)
		}
	}
	m.mu.Unlock()
	for _, name := range victims {
		l := m.lockFor(name)
		l.Lock()
		m.mu.Lock()
		inst := m.instances[name]
		still := inst != nil && inst.conns == 0 && time.Since(inst.lastUsed) > inst.idle
		if still {
			delete(m.instances, name)
		}
		m.mu.Unlock()
		switch {
		case still && inst.ephemeral:
			if err := m.docker.Remove(ctx, name); err != nil {
				log.Printf("remove %s: %v", name, err)
			} else {
				log.Printf("deleted idle sandbox %s", name)
				m.stats.sandboxesDeleted.Add(1)
			}
		case still:
			if err := m.docker.Stop(ctx, name, 10); err != nil && !errors.Is(err, dockerapi.ErrNotFound) {
				log.Printf("stop %s: %v", name, err)
			} else {
				log.Printf("stopped idle %s", name)
				m.stats.idleStops.Add(1)
			}
		}
		l.Unlock()
	}
}

func (m *manager) runReaper(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.reap(ctx)
		}
	}
}
