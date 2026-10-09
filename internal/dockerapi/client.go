// Package dockerapi is a minimal client for the few Docker Engine API calls
// the gateway needs. It talks to the socket proxy, never to the socket.
package dockerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

var ErrNotFound = errors.New("not found")

type Client struct {
	Base  string // e.g. http://sockproxy:2375
	Token string
	HTTP  *http.Client
}

func New(base, token string) *Client {
	return &Client{Base: base, Token: token, HTTP: &http.Client{Timeout: 90 * time.Second}}
}

type Container struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Running bool   `json:"Running"`
		Status  string `json:"Status"`
	} `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

type ListItem struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, in, out any) error {
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("X-Proxy-Token", c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusNotModified: // already started / stopped
		return nil
	case resp.StatusCode >= 300:
		return fmt.Errorf("docker %s %s: %d %s", method, path, resp.StatusCode, bytes.TrimSpace(data))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *Client) Inspect(ctx context.Context, name string) (*Container, error) {
	var ct Container
	if err := c.do(ctx, http.MethodGet, "/containers/"+name+"/json", nil, nil, &ct); err != nil {
		return nil, err
	}
	return &ct, nil
}

func (c *Client) List(ctx context.Context) ([]ListItem, error) {
	var out []ListItem
	err := c.do(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"1"}}, nil, &out)
	return out, err
}

func (c *Client) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	return c.do(ctx, http.MethodPost, "/volumes/create", nil, map[string]any{"Name": name, "Labels": labels}, nil)
}

func (c *Client) Create(ctx context.Context, name string, spec any) error {
	return c.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, spec, nil)
}

func (c *Client) Start(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+name+"/start", nil, nil, nil)
}

func (c *Client) Stop(ctx context.Context, name string, timeoutSec int) error {
	return c.do(ctx, http.MethodPost, "/containers/"+name+"/stop", url.Values{"t": {fmt.Sprint(timeoutSec)}}, nil, nil)
}

func (c *Client) Remove(ctx context.Context, name string) error {
	err := c.do(ctx, http.MethodDelete, "/containers/"+name, url.Values{"force": {"1"}}, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}
