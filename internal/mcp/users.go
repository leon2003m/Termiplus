package mcp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
)

// The admin tools manage_user and manage_group change who is in which
// termiplus group (and so who gets which role and workspace). They only touch
// top-level groups whose name starts with the configured prefix, never create
// users and never touch credentials; the directory (Keycloak) stays the
// source of truth and changes apply with the user's next token.

const defaultGroupPrefix = "termiplus-"

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@+-]{0,254}$`)

// WithDirectory enables manage_user and manage_group on top of dir, limited to
// top-level groups named prefix* (default "termiplus-").
func (s *Server) WithDirectory(dir Directory, prefix string) *Server {
	if prefix == "" {
		prefix = defaultGroupPrefix
	}
	s.dir, s.groupPrefix = dir, prefix
	return s
}

var errNoDirectory = errors.New("user management is not configured on this server (no Keycloak admin client)")

// managedGroups returns the groups the tools may manage, by name.
func (s *Server) managedGroups(ctx context.Context) (map[string]DirGroup, error) {
	all, err := s.dir.Groups(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]DirGroup{}
	for _, g := range all {
		if strings.HasPrefix(g.Name, s.groupPrefix) && g.Path == "/"+g.Name {
			out[g.Name] = g
		}
	}
	return out, nil
}

func (s *Server) managedGroup(ctx context.Context, name string) (DirGroup, error) {
	if !strings.HasPrefix(name, s.groupPrefix) {
		return DirGroup{}, fmt.Errorf("group %q is not managed by termiplus (only %s* groups)", name, s.groupPrefix)
	}
	groups, err := s.managedGroups(ctx)
	if err != nil {
		return DirGroup{}, err
	}
	g, ok := groups[name]
	if !ok {
		return DirGroup{}, fmt.Errorf("no group %q in the directory", name)
	}
	return g, nil
}

func (s *Server) findUser(ctx context.Context, username string) (*DirUser, error) {
	if !usernamePattern.MatchString(username) {
		return nil, fmt.Errorf("invalid username %q", username)
	}
	u, err := s.dir.FindUser(ctx, username)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, fmt.Errorf("no user %q in the directory (users are created in the identity provider)", username)
	}
	return u, nil
}

func (s *Server) userManagedGroups(ctx context.Context, userID string) ([]string, error) {
	gs, err := s.dir.UserGroups(ctx, userID)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, g := range gs {
		if strings.HasPrefix(g.Name, s.groupPrefix) && g.Path == "/"+g.Name {
			names = append(names, g.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (s *Server) manageGroup(ctx context.Context, args map[string]any) (any, error) {
	if s.dir == nil {
		return nil, errNoDirectory
	}
	switch argString(args, "action") {
	case "list":
		groups, err := s.managedGroups(ctx)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(groups))
		for n := range groups {
			names = append(names, n)
		}
		sort.Strings(names)
		return map[string]any{"groups": names}, nil
	case "members":
		g, err := s.managedGroup(ctx, argString(args, "group"))
		if err != nil {
			return nil, err
		}
		members, err := s.dir.Members(ctx, g.ID)
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0, len(members))
		for _, m := range members {
			out = append(out, map[string]any{"username": m.Username, "email": m.Email, "enabled": m.Enabled})
		}
		sort.Slice(out, func(i, j int) bool { return out[i]["username"].(string) < out[j]["username"].(string) })
		return map[string]any{"group": g.Name, "members": out}, nil
	}
	return nil, errors.New(`action must be "list" or "members"`)
}

func (s *Server) manageUser(ctx context.Context, p Principal, args map[string]any) (any, error) {
	if s.dir == nil {
		return nil, errNoDirectory
	}
	action := argString(args, "action")
	u, err := s.findUser(ctx, argString(args, "username"))
	if err != nil {
		return nil, err
	}
	if action == "show" {
		groups, err := s.userManagedGroups(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"username": u.Username, "email": u.Email, "enabled": u.Enabled, "groups": groups}, nil
	}

	g, err := s.managedGroup(ctx, argString(args, "group"))
	if err != nil {
		return nil, err
	}
	current, err := s.userManagedGroups(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	member := false
	for _, n := range current {
		member = member || n == g.Name
	}
	switch action {
	case "add_to_group":
		if member {
			return map[string]any{"username": u.Username, "group": g.Name, "changed": false}, nil
		}
		if err := s.dir.AddToGroup(ctx, u.ID, g.ID); err != nil {
			return nil, err
		}
	case "remove_from_group":
		if !member {
			return map[string]any{"username": u.Username, "group": g.Name, "changed": false}, nil
		}
		if g.Name == RoleAdmin {
			// Never leave the deployment without an enabled admin.
			members, err := s.dir.Members(ctx, g.ID)
			if err != nil {
				return nil, err
			}
			others := 0
			for _, m := range members {
				if m.ID != u.ID && m.Enabled {
					others++
				}
			}
			if others == 0 {
				return nil, fmt.Errorf("refusing to remove the last enabled member of %s", g.Name)
			}
		}
		if err := s.dir.RemoveFromGroup(ctx, u.ID, g.ID); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New(`action must be "show", "add_to_group" or "remove_from_group"`)
	}
	logAdmin(p, action, u.Username, g.Name)
	return map[string]any{
		"username": u.Username, "group": g.Name, "changed": true,
		"note": "takes effect when the user's sign-in token is next refreshed (at the latest on their next login)",
	}, nil
}

func logAdmin(p Principal, action, username, group string) {
	log.Printf("admin %q (subject %q): %q %q %q", p.UserID, p.Subject, action, username, group)
}

// manageUserGate: admins only; changes need a human confirmation, reads do not.
func manageUserGate(p Principal, args map[string]any) (bool, error) {
	if !p.HasRole(RoleAdmin) {
		return false, fmt.Errorf("user management requires the %s role", RoleAdmin)
	}
	switch argString(args, "action") {
	case "show":
		return false, nil
	case "add_to_group", "remove_from_group":
		return true, nil
	}
	return false, errors.New(`action must be "show", "add_to_group" or "remove_from_group"`)
}
