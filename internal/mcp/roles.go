package mcp

import (
	"fmt"
	"strings"
)

// RoleMap maps an organisation's own groups or roles onto termiplus's two
// roles (RolePersist, RoleAdmin), so an existing Keycloak realm does not need
// termiplus-specific groups (MCP_PERSIST_ROLES, MCP_ADMIN_ROLES).
//
// With a RoleMap, group names from the sign-in proxy are normalised before
// any matching (roles and entitlements): one leading "role:" (oauth2-proxy's
// keycloak-oidc provider adds it to realm roles) and then one leading "/"
// (Keycloak's "Full group path") are stripped. A principal then has a
// termiplus role exactly when one of its normalised groups is listed for it;
// a group that is literally called termiplus-persist or termiplus-admin only
// counts if it is listed too. Without a RoleMap (nil) group names are used
// exactly as forwarded, which is the behaviour before RoleMap existed.
type RoleMap struct {
	Persist []string
	Admin   []string
}

// NewRoleMap builds a RoleMap; an empty list keeps that role's default name.
// Listed names are normalised like forwarded groups, so "/ki-admin" and
// "ki-admin" mean the same.
func NewRoleMap(persist, admin []string) (*RoleMap, error) {
	clean := func(env string, names []string, def string) ([]string, error) {
		if len(names) == 0 {
			return []string{def}, nil
		}
		out := make([]string, 0, len(names))
		for _, n := range names {
			g := normalizeGroup(n)
			if g == "" || g != strings.TrimSpace(g) || strings.ContainsAny(g, ",\x00\r\n") {
				return nil, fmt.Errorf("%s: invalid group name %q", env, n)
			}
			out = append(out, g)
		}
		return out, nil
	}
	p, err := clean("MCP_PERSIST_ROLES", persist, RolePersist)
	if err != nil {
		return nil, err
	}
	a, err := clean("MCP_ADMIN_ROLES", admin, RoleAdmin)
	if err != nil {
		return nil, err
	}
	return &RoleMap{Persist: p, Admin: a}, nil
}

// normalizeGroup strips one "role:" prefix and then one leading "/".
func normalizeGroup(g string) string {
	return strings.TrimPrefix(strings.TrimPrefix(g, "role:"), "/")
}

// apply turns the forwarded groups into the principal's roles: the normalised
// groups (deduplicated, for entitlements) plus RolePersist / RoleAdmin when a
// listed group is among them. A nil RoleMap returns groups unchanged.
func (m *RoleMap) apply(groups []string) []string {
	if m == nil {
		return groups
	}
	have := map[string]bool{}
	out := []string{}
	for _, g := range groups {
		g = normalizeGroup(g)
		if g == "" || have[g] {
			continue
		}
		have[g] = true
		if g != RolePersist && g != RoleAdmin {
			out = append(out, g)
		}
	}
	for _, r := range []struct {
		role   string
		groups []string
	}{{RolePersist, m.Persist}, {RoleAdmin, m.Admin}} {
		for _, g := range r.groups {
			if have[g] {
				out = append(out, r.role)
				break
			}
		}
	}
	return out
}

// isAdminGroup reports whether a directory group grants RoleAdmin.
func (m *RoleMap) isAdminGroup(name string) bool {
	if m == nil {
		return name == RoleAdmin
	}
	for _, g := range m.Admin {
		if g == normalizeGroup(name) {
			return true
		}
	}
	return false
}

// persistGroups names the groups that grant RolePersist, for error messages.
func (m *RoleMap) persistGroups() string {
	if m == nil {
		return RolePersist
	}
	return strings.Join(m.Persist, " or ")
}

// String describes the mapping for the startup log.
func (m *RoleMap) String() string {
	if m == nil {
		return "roles from groups " + RolePersist + " / " + RoleAdmin + " (exact names)"
	}
	return fmt.Sprintf("persist role from groups %s, admin role from groups %s (normalised names)",
		strings.Join(m.Persist, ","), strings.Join(m.Admin, ","))
}
