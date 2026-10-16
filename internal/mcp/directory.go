package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Directory is the user directory behind the OIDC provider, as far as the
// admin tools manage_user and manage_group need it: read users and groups and
// change group membership. It never creates users or touches credentials.
type Directory interface {
	Groups(ctx context.Context) ([]DirGroup, error)
	Members(ctx context.Context, groupID string) ([]DirUser, error)
	// FindUser returns the user with exactly this username, or nil.
	FindUser(ctx context.Context, username string) (*DirUser, error)
	UserGroups(ctx context.Context, userID string) ([]DirGroup, error)
	AddToGroup(ctx context.Context, userID, groupID string) error
	RemoveFromGroup(ctx context.Context, userID, groupID string) error
}

type DirGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type DirUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
	Enabled  bool   `json:"enabled"`
}

// KeycloakDirectory implements Directory with Keycloak's admin REST API. It
// signs in as a confidential client's service account (client credentials
// grant, handled by Keycloak) and needs the realm-management roles
// view-users, query-groups and manage-users on that service account.
type KeycloakDirectory struct {
	base, realm, clientID, secret string
	http                          *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewKeycloakDirectory talks to Keycloak at base (e.g. http://keycloak:8080),
// realm realm, as the service account of clientID.
func NewKeycloakDirectory(base, realm, clientID, secret string) (*KeycloakDirectory, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("keycloak admin URL %q must be http(s)://host[:port]", base)
	}
	if realm == "" || clientID == "" || secret == "" {
		return nil, errors.New("keycloak directory needs a realm, a client id and a client secret")
	}
	return &KeycloakDirectory{
		base: strings.TrimRight(base, "/"), realm: realm, clientID: clientID, secret: secret,
		http: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// accessToken returns a cached service-account token, fetching a new one from
// Keycloak's token endpoint shortly before it expires.
func (k *KeycloakDirectory) accessToken(ctx context.Context) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.token != "" && time.Now().Before(k.expires) {
		return k.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		k.base+"/realms/"+url.PathEscape(k.realm)+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(k.clientID), url.QueryEscape(k.secret))
	resp, err := k.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("keycloak token: status %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil || tok.AccessToken == "" {
		return "", errors.New("keycloak token: no access token in the response")
	}
	life := time.Duration(tok.ExpiresIn) * time.Second
	if life > time.Minute {
		life -= 30 * time.Second
	} else {
		life /= 2
	}
	k.token, k.expires = tok.AccessToken, time.Now().Add(life)
	return k.token, nil
}

// admin calls the realm's admin API. path is relative to
// /admin/realms/<realm>/ and every id in it must be escaped by the caller.
func (k *KeycloakDirectory) admin(ctx context.Context, method, path string, q url.Values, out any) error {
	tok, err := k.accessToken(ctx)
	if err != nil {
		return err
	}
	u := k.base + "/admin/realms/" + url.PathEscape(k.realm) + "/" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var body io.Reader
	if method == http.MethodPut {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := k.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak admin API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		k.mu.Lock()
		k.token = "" // fetch a fresh token next time
		k.mu.Unlock()
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("keycloak admin API %s %s: status %d", method, strings.SplitN(path, "/", 2)[0], resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

func (k *KeycloakDirectory) Groups(ctx context.Context) ([]DirGroup, error) {
	var gs []DirGroup
	err := k.admin(ctx, http.MethodGet, "groups", url.Values{"briefRepresentation": {"true"}, "max": {"1000"}}, &gs)
	return gs, err
}

func (k *KeycloakDirectory) Members(ctx context.Context, groupID string) ([]DirUser, error) {
	var us []DirUser
	err := k.admin(ctx, http.MethodGet, "groups/"+url.PathEscape(groupID)+"/members",
		url.Values{"briefRepresentation": {"true"}, "max": {"1000"}}, &us)
	return us, err
}

func (k *KeycloakDirectory) FindUser(ctx context.Context, username string) (*DirUser, error) {
	var us []DirUser
	if err := k.admin(ctx, http.MethodGet, "users",
		url.Values{"username": {username}, "exact": {"true"}, "briefRepresentation": {"true"}}, &us); err != nil {
		return nil, err
	}
	for i := range us {
		// Keycloak stores usernames in lower case; exact=true already matches
		// case-insensitively, so compare the same way.
		if strings.EqualFold(us[i].Username, username) {
			return &us[i], nil
		}
	}
	return nil, nil
}

func (k *KeycloakDirectory) UserGroups(ctx context.Context, userID string) ([]DirGroup, error) {
	var gs []DirGroup
	err := k.admin(ctx, http.MethodGet, "users/"+url.PathEscape(userID)+"/groups",
		url.Values{"briefRepresentation": {"true"}, "max": {"1000"}}, &gs)
	return gs, err
}

func (k *KeycloakDirectory) AddToGroup(ctx context.Context, userID, groupID string) error {
	return k.admin(ctx, http.MethodPut, "users/"+url.PathEscape(userID)+"/groups/"+url.PathEscape(groupID), nil, nil)
}

func (k *KeycloakDirectory) RemoveFromGroup(ctx context.Context, userID, groupID string) error {
	return k.admin(ctx, http.MethodDelete, "users/"+url.PathEscape(userID)+"/groups/"+url.PathEscape(groupID), nil, nil)
}
