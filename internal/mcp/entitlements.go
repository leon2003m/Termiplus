package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Entitlements decide which workspaces a caller may see and launch, based on
// the groups (roles) from the caller's verified identity. They are configured
// in the MCP server (MCP_ENTITLEMENTS_FILE), not in the gateway: the gateway
// trusts the MCP server to have checked them.
//
// A workspace listed in Workspaces needs at least one of its groups. A
// workspace that is not listed follows Default ("allow" or "deny"). A nil
// *Entitlements allows every workspace, which is the behaviour without a file.
type Entitlements struct {
	Default    string              `json:"default"`
	Workspaces map[string][]string `json:"workspaces"`
}

// ParseEntitlements reads the JSON entitlements file. Unknown fields, an
// unknown default, a missing default and empty group lists are rejected, so
// a typo cannot silently open a workspace to everyone.
func ParseEntitlements(data []byte) (*Entitlements, error) {
	var e Entitlements
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return nil, fmt.Errorf("entitlements: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("entitlements: trailing data after the JSON object")
		}
		return nil, fmt.Errorf("entitlements: trailing data: %w", err)
	}
	if e.Default != "allow" && e.Default != "deny" {
		return nil, fmt.Errorf("entitlements: default must be \"allow\" or \"deny\", got %q", e.Default)
	}
	for ws, groups := range e.Workspaces {
		if strings.TrimSpace(ws) == "" {
			return nil, fmt.Errorf("entitlements: empty workspace name")
		}
		if len(groups) == 0 {
			return nil, fmt.Errorf("entitlements: workspace %q lists no groups (remove it or name at least one group)", ws)
		}
		for _, g := range groups {
			if strings.TrimSpace(g) == "" || g != strings.TrimSpace(g) {
				return nil, fmt.Errorf("entitlements: workspace %q has an empty or padded group name", ws)
			}
		}
	}
	return &e, nil
}

// Allowed reports whether p may see and launch workspace ws.
func (e *Entitlements) Allowed(p Principal, ws string) bool {
	if e == nil {
		return true
	}
	groups, listed := e.Workspaces[ws]
	if !listed {
		return e.Default == "allow"
	}
	for _, g := range groups {
		if p.HasRole(g) {
			return true
		}
	}
	return false
}

// filter returns the workspaces p is entitled to.
func (e *Entitlements) filter(p Principal, all []Workspace) []Workspace {
	out := []Workspace{}
	for _, w := range all {
		if e.Allowed(p, w.Name) {
			out = append(out, w)
		}
	}
	return out
}

// marshal renders e as the file format; nil renders the allow-all default.
func (e *Entitlements) marshal() []byte {
	out := Entitlements{Default: "allow", Workspaces: map[string][]string{}}
	if e != nil {
		out.Default = e.Default
		for ws, g := range e.Workspaces {
			out.Workspaces[ws] = g
		}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return b
}
