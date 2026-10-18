package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// The audit log records every tool call that can change something (sessions,
// entitlements, network policy, group membership), whether it ran, failed or
// was refused, from chat and from the management console alike. Read-only
// calls and calls still waiting for a human confirmation are not recorded.
// It is a JSON-lines file next to the other MCP state; when it grows past its
// size limit it is moved to <file>.1 (replacing the previous one), so at most
// two files' worth of history is kept. Admins read it with get_audit_log.

const (
	auditMaxArgs     = 4 << 10 // bytes of JSON arguments kept per entry
	auditDefaultMax  = 5 << 20
	auditDefaultRead = 50
	auditMaxRead     = 500
)

// AuditEntry is one recorded tool call.
type AuditEntry struct {
	Time    time.Time      `json:"time"`
	Actor   string         `json:"actor"`             // the user id the call acted as
	Subject string         `json:"subject,omitempty"` // OIDC subject, when known
	Tool    string         `json:"tool"`
	Args    map[string]any `json:"args,omitempty"`
	Outcome string         `json:"outcome"` // "ok", "error" or "denied"
	Error   string         `json:"error,omitempty"`
}

// AuditLog appends AuditEntries to a file.
type AuditLog struct {
	path     string
	maxBytes int64

	mu sync.Mutex
	f  *os.File
}

// OpenAuditLog opens (or creates) path for appending. maxBytes <= 0 selects
// the default of 5 MiB per file.
func OpenAuditLog(path string, maxBytes int64) (*AuditLog, error) {
	if maxBytes <= 0 {
		maxBytes = auditDefaultMax
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit log: %w", err)
	}
	return &AuditLog{path: path, maxBytes: maxBytes, f: f}, nil
}

// Append writes e as one line and syncs it to disk.
func (a *AuditLog) Append(e AuditEntry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	if st, err := a.f.Stat(); err == nil && st.Size() > 0 && st.Size()+int64(len(line)) > a.maxBytes {
		if err := a.rotate(); err != nil {
			return err
		}
	}
	if _, err := a.f.Write(line); err != nil {
		return err
	}
	return a.f.Sync()
}

func (a *AuditLog) rotate() error {
	if err := a.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(a.path, a.path+".1"); err != nil {
		return err
	}
	f, err := os.OpenFile(a.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	a.f = f
	return nil
}

// AuditFilter selects entries for Read. Empty fields match everything.
type AuditFilter struct {
	Actor string
	Tool  string
	Since time.Time
	Limit int
}

// Read returns the newest entries matching f, newest first.
func (a *AuditLog) Read(f AuditFilter) ([]AuditEntry, error) {
	if f.Limit <= 0 {
		f.Limit = auditDefaultRead
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var all []AuditEntry
	for _, p := range []string{a.path + ".1", a.path} {
		entries, err := readAuditFile(p)
		if err != nil {
			return nil, err
		}
		all = append(all, entries...)
	}
	out := []AuditEntry{}
	for i := len(all) - 1; i >= 0 && len(out) < f.Limit; i-- {
		e := all[i]
		if (f.Actor == "" || e.Actor == f.Actor) && (f.Tool == "" || e.Tool == f.Tool) && !e.Time.Before(f.Since) {
			out = append(out, e)
		}
	}
	return out, nil
}

func readAuditFile(path string) ([]AuditEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []AuditEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e AuditEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e) // a torn last line (crash mid-write) is skipped
		}
	}
	return out, sc.Err()
}

// auditArgs drops control arguments (leading "_", e.g. _confirmed) and
// shortens oversized ones so one entry stays small.
func auditArgs(args map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range args {
		if !strings.HasPrefix(k, "_") {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	if js, err := json.Marshal(out); err != nil || len(js) > auditMaxArgs {
		s := string(js)
		if len(s) > auditMaxArgs {
			s = s[:auditMaxArgs]
		}
		return map[string]any{"truncated": s}
	}
	return out
}

// readActions are the read-only actions of tools that can also write.
var readActions = map[string]string{"manage_user": "show", "manage_snapshots": "list", "share_desktop": "list", "manage_departed": "list"}

// audited reports whether a call to t with args can change something.
func audited(t *tool, args map[string]any) bool {
	if t.readOnly {
		return false
	}
	a, ok := readActions[t.name]
	return !ok || argString(args, "action") != a
}

func (s *Server) audit(p Principal, t *tool, args map[string]any, outcome string, err error) {
	if s.auditLog == nil || !audited(t, args) {
		return
	}
	e := AuditEntry{Time: time.Now().UTC(), Actor: p.UserID, Subject: p.Subject, Tool: t.name, Args: auditArgs(args), Outcome: outcome}
	if err != nil {
		e.Error = err.Error()
	}
	if werr := s.auditLog.Append(e); werr != nil {
		log.Printf("audit: could not record %s by %s: %v", t.name, p.UserID, werr)
	}
}

// WithAuditLog records state-changing tool calls in a and enables
// get_audit_log.
func (s *Server) WithAuditLog(a *AuditLog) *Server {
	s.auditLog = a
	return s
}

func (s *Server) getAuditLog(args map[string]any) (any, error) {
	if s.auditLog == nil {
		return nil, errors.New("the audit log is not configured on this server (no MCP_AUDIT_FILE)")
	}
	f := AuditFilter{Actor: argString(args, "actor"), Tool: argString(args, "tool"), Limit: auditDefaultRead}
	if n, ok := args["limit"].(float64); ok {
		if n < 1 || n > auditMaxRead {
			return nil, fmt.Errorf("limit must be between 1 and %d", auditMaxRead)
		}
		f.Limit = int(n)
	}
	if since := argString(args, "since"); since != "" {
		t, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return nil, errors.New(`since must be an RFC 3339 time, e.g. "2026-10-16T00:00:00Z"`)
		}
		f.Since = t
	}
	entries, err := s.auditLog.Read(f)
	if err != nil {
		return nil, err
	}
	return map[string]any{"entries": entries}, nil
}
