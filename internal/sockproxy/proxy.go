// Package sockproxy is a deny-by-default filter in front of the Docker
// socket. It only allows what the gateway needs to run terminal containers:
// create/start/stop/remove/inspect labelled containers, create volumes and
// read the ID of the one allowed terminal image.
package sockproxy

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"termiplus/internal/naming"
)

type Proxy struct {
	Rules  Rules
	Token  string // optional shared secret with the gateway (X-Proxy-Token)
	daemon *http.Client
}

func New(rules Rules, socketPath, token string) *Proxy {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
		MaxIdleConns:    16,
		IdleConnTimeout: 30 * time.Second,
	}
	return &Proxy{Rules: rules, Token: token, daemon: &http.Client{Transport: tr, Timeout: 120 * time.Second}}
}

var (
	versionPrefix = regexp.MustCompile(`^/v[0-9]{1,2}\.[0-9]{1,3}(/.*)$`)
	fullID        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	imageID       = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	digits        = regexp.MustCompile(`^[0-9]{1,3}$`)
)

type denial struct {
	status int
	msg    string
}

func (d *denial) Error() string { return d.msg }

func deny(format string, a ...any) error {
	return &denial{status: http.StatusForbidden, msg: fmt.Sprintf(format, a...)}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := p.handle(w, r); err != nil {
		status := http.StatusBadGateway
		if d, ok := err.(*denial); ok {
			status = d.status
		}
		log.Printf("DENY %s %s: %v", r.Method, r.URL.RequestURI(), err)
		writeJSON(w, status, map[string]string{"message": "termiplus-sockproxy: " + err.Error()})
	}
}

func (p *Proxy) handle(w http.ResponseWriter, r *http.Request) error {
	// Liveness for the container healthcheck: no token, no query, no body,
	// and it only reveals whether the daemon answers a ping.
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" && r.URL.RawQuery == "" {
		return p.healthz(w, r)
	}
	if p.Token != "" {
		vals := r.Header.Values("X-Proxy-Token")
		if len(vals) != 1 || subtle.ConstantTimeCompare([]byte(vals[0]), []byte(p.Token)) != 1 {
			return &denial{status: http.StatusUnauthorized, msg: "missing or wrong proxy token"}
		}
	}
	if r.Header.Get("Upgrade") != "" {
		return deny("connection upgrades not allowed")
	}

	p2, err := normalizePath(r.URL)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimPrefix(p2, "/"), "/")
	q := r.URL.Query()

	switch {
	// ping / version: harmless, used for client negotiation
	case (r.Method == http.MethodGet || r.Method == http.MethodHead) && (p2 == "/_ping" || p2 == "/version"):
		if len(q) != 0 {
			return deny("no query allowed")
		}
		return p.forward(w, r, r.Method, p2, nil, nil)

	// list: always restricted to managed containers
	case r.Method == http.MethodGet && p2 == "/containers/json":
		for k := range q {
			if k != "all" {
				return deny("query %q not allowed", k)
			}
		}
		out := url.Values{}
		if v := q.Get("all"); v == "1" || v == "true" {
			out.Set("all", "1")
		}
		f, _ := json.Marshal(map[string][]string{"label": {naming.ManagedLabel + "=" + naming.ManagedValue}})
		out.Set("filters", string(f))
		return p.forward(w, r, http.MethodGet, p2, out, nil)

	case r.Method == http.MethodPost && p2 == "/containers/create":
		if len(q) != 1 || len(q["name"]) != 1 {
			return deny("exactly one query parameter (name) allowed")
		}
		name := q.Get("name")
		body, err := p.Rules.ValidateCreate(name, r.Body)
		if err != nil {
			return deny("%v", err)
		}
		return p.forward(w, r, http.MethodPost, p2, url.Values{"name": {name}}, body)

	case r.Method == http.MethodPost && p2 == "/volumes/create":
		if len(q) != 0 {
			return deny("no query allowed")
		}
		body, err := p.Rules.ValidateVolume(r.Body)
		if err != nil {
			return deny("%v", err)
		}
		return p.forward(w, r, http.MethodPost, p2, nil, body)

	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "volumes":
		if !naming.VolumeName.MatchString(parts[1]) || len(q) != 0 {
			return deny("volume %q not allowed", parts[1])
		}
		return p.forward(w, r, http.MethodGet, p2, nil, nil)

	// image ID of the allowed terminal image, and nothing else about it: the
	// gateway compares it with a stopped container's image to recreate
	// workspaces after an image update.
	case r.Method == http.MethodGet && p.Rules.Image != "" && p2 == "/images/"+p.Rules.Image+"/json":
		if len(q) != 0 {
			return deny("no query allowed")
		}
		return p.imageID(w, r, p2)

	case len(parts) >= 2 && parts[0] == "containers":
		return p.containerOp(w, r, parts, q)
	}
	return deny("endpoint not allowed")
}

// containerOp handles operations on one existing container. The reference is
// resolved by the daemon first and the resolved container must carry the
// managed label and a terminal name; the request is then forwarded by full ID
// so prefix or name tricks cannot redirect it to another container.
func (p *Proxy) containerOp(w http.ResponseWriter, r *http.Request, parts []string, q url.Values) error {
	ref := parts[1]
	if !naming.IsManagedContainer(ref) && !fullID.MatchString(ref) {
		return deny("container reference %q not allowed", ref)
	}
	var method, action string
	out := url.Values{}
	switch {
	case len(parts) == 3 && parts[2] == "json" && r.Method == http.MethodGet:
		method, action = http.MethodGet, "json"
		if len(q) != 0 {
			return deny("no query allowed")
		}
	case len(parts) == 3 && parts[2] == "start" && r.Method == http.MethodPost:
		method, action = http.MethodPost, "start"
		if len(q) != 0 {
			return deny("no query allowed")
		}
	case len(parts) == 3 && parts[2] == "stop" && r.Method == http.MethodPost:
		method, action = http.MethodPost, "stop"
		for k := range q {
			if k != "t" || !digits.MatchString(q.Get("t")) {
				return deny("query %q not allowed", k)
			}
			out.Set("t", q.Get("t"))
		}
	case len(parts) == 2 && r.Method == http.MethodDelete:
		method, action = http.MethodDelete, ""
		for k := range q {
			// "v" (remove volumes) is deliberately not allowed.
			if k != "force" || (q.Get("force") != "1" && q.Get("force") != "true") {
				return deny("query %q not allowed", k)
			}
			out.Set("force", "1")
		}
	default:
		return deny("container operation not allowed")
	}

	id, status, err := p.resolveOwned(r.Context(), ref)
	if err != nil {
		if status == http.StatusNotFound {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + ref})
			return nil
		}
		return err
	}
	target := "/containers/" + id
	if action != "" {
		target += "/" + action
	}
	return p.forward(w, r, method, target, out, nil)
}

// imageID inspects the allowed image and answers with its ID only, so labels,
// environment and history of the image never pass the proxy.
func (p *Proxy) imageID(w http.ResponseWriter, r *http.Request, p2 string) error {
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://docker"+p2, nil)
	resp, err := p.daemon.Do(req)
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such image: " + p.Rules.Image})
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon image inspect returned %d", resp.StatusCode)
	}
	var info struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&info); err != nil {
		return fmt.Errorf("decode image inspect: %w", err)
	}
	if !imageID.MatchString(info.ID) {
		return fmt.Errorf("unexpected image id from daemon")
	}
	log.Printf("ALLOW GET %s -> 200 (id only)", p2)
	writeJSON(w, http.StatusOK, map[string]string{"Id": info.ID})
	return nil
}

func (p *Proxy) healthz(w http.ResponseWriter, r *http.Request) error {
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://docker/_ping", nil)
	resp, err := p.daemon.Do(req)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "docker unreachable"})
		return nil
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "docker unhealthy"})
		return nil
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	return nil
}

func (p *Proxy) resolveOwned(ctx context.Context, ref string) (string, int, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/"+url.PathEscape(ref)+"/json", nil)
	resp, err := p.daemon.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("daemon: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", http.StatusNotFound, fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("daemon inspect returned %d", resp.StatusCode)
	}
	var info struct {
		ID     string `json:"Id"`
		Name   string `json:"Name"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&info); err != nil {
		return "", 0, fmt.Errorf("decode inspect: %w", err)
	}
	if info.Config.Labels[naming.ManagedLabel] != naming.ManagedValue ||
		!naming.IsManagedContainer(strings.TrimPrefix(info.Name, "/")) ||
		!fullID.MatchString(info.ID) {
		return "", 0, deny("container %q is not managed by termiplus", ref)
	}
	return info.ID, http.StatusOK, nil
}

func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, method, p2 string, q url.Values, body []byte) error {
	u := "http://docker" + p2
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.daemon.Do(req)
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	defer resp.Body.Close()
	log.Printf("ALLOW %s %s -> %d", method, p2, resp.StatusCode)
	for _, h := range []string{"Content-Type", "Api-Version", "Docker-Experimental", "Ostype"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		log.Printf("copy response: %v", err)
	}
	return nil
}

// normalizePath rejects encoded paths, dot segments and duplicate slashes and
// strips an optional API version prefix.
func normalizePath(u *url.URL) (string, error) {
	if u.RawPath != "" || strings.Contains(u.RawQuery, "%00") {
		return "", deny("encoded path not allowed")
	}
	p := u.Path
	if p == "" || p[0] != '/' || strings.Contains(p, "//") || strings.Contains(p, "..") || strings.ContainsAny(p, "\\%;") {
		return "", deny("malformed path")
	}
	if m := versionPrefix.FindStringSubmatch(p); m != nil {
		p = m[1]
	}
	if path.Clean(p) != p {
		return "", deny("non-canonical path")
	}
	return p, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
