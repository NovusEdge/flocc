// Package tailnet talks to the Tailscale API with an OAuth client and checks the tailnet policy.
// It never writes the policy and never puts the client secret, token or a minted key in an error.
package tailnet

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
	"time"
)

// Tag is the tag every flocc host joins the tailnet with.
const Tag = "tag:flocc-host"

type Client struct {
	HTTP         *http.Client
	Base         string // https://api.tailscale.com outside tests
	ID, Secret   string
	token        string
	tokenExpires time.Time
}

var ErrTagNotPermitted = errors.New("the tailnet policy does not let this client use the tag")

// ErrRefused marks a 4xx reply: the client or its scopes are wrong, not Tailscale being down.
var ErrRefused = errors.New("refused")

// The margin keeps a token from expiring between the cache check and the request using it.
const tokenMargin = time.Minute

func (c *Client) do(ctx context.Context, req *http.Request, secrets ...string) ([]byte, int, error) {
	req = req.WithContext(ctx)
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		// url.Error carries the URL, which holds no credential; the request body is not included.
		return nil, 0, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, errors.Unwrap(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// scrub removes credentials a server might echo back before the text goes into an error.
func (c *Client) scrub(s string, extra ...string) string {
	for _, secret := range append([]string{c.Secret, c.token}, extra...) {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return strings.TrimSpace(s)
}

func (c *Client) statusErr(what string, status int, body []byte, tag string) error {
	text := string(body)
	if tag != "" && status >= 400 && status < 500 && strings.Contains(text, tag) {
		return ErrTagNotPermitted
	}
	err := fmt.Errorf("tailscale %s: HTTP %d: %s", what, status, c.scrub(text))
	if status >= 400 && status < 500 {
		return fmt.Errorf("%w (%w)", err, ErrRefused)
	}
	return err
}

func (c *Client) Token(ctx context.Context) (string, error) {
	if c.token != "" && time.Now().Add(tokenMargin).Before(c.tokenExpires) {
		return c.token, nil
	}
	form := url.Values{"client_id": {c.ID}, "client_secret": {c.Secret}, "grant_type": {"client_credentials"}}
	req, err := http.NewRequest("POST", c.Base+"/api/v2/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, status, err := c.do(ctx, req)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", c.statusErr("token", status, body, "")
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		return "", errors.New("tailscale token: the reply has no access_token")
	}
	c.token = out.AccessToken
	c.tokenExpires = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	return c.token, nil
}

func (c *Client) authed(ctx context.Context, method, path string, payload any) ([]byte, int, error) {
	tok, err := c.Token(ctx)
	if err != nil {
		return nil, 0, err
	}
	var r io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.Base+path, r)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(ctx, req)
}

// TaggedKey mints a one-off, preauthorized, non-ephemeral auth key for a node that joins as tag.
func (c *Client) TaggedKey(ctx context.Context, tag string, ttl time.Duration) (string, error) {
	create := map[string]any{"reusable": false, "ephemeral": false, "preauthorized": true, "tags": []string{tag}}
	payload := map[string]any{
		"capabilities":  map[string]any{"devices": map[string]any{"create": create}},
		"expirySeconds": int(ttl.Seconds()),
	}
	body, status, err := c.authed(ctx, "POST", "/api/v2/tailnet/-/keys", payload)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", c.statusErr("key", status, body, tag)
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Key == "" {
		return "", errors.New("tailscale key: the reply has no key")
	}
	return out.Key, nil
}

func (c *Client) SetTags(ctx context.Context, deviceID string, tags []string) error {
	body, status, err := c.authed(ctx, "POST", "/api/v2/device/"+url.PathEscape(deviceID)+"/tags", map[string]any{"tags": tags})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		tag := ""
		if len(tags) > 0 {
			tag = tags[0]
		}
		return c.statusErr("tags", status, body, tag)
	}
	return nil
}

// Policy returns the policy file as JSON.
func (c *Client) Policy(ctx context.Context) ([]byte, error) {
	body, status, err := c.authed(ctx, "GET", "/api/v2/tailnet/-/acl", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, c.statusErr("policy", status, body, "")
	}
	return body, nil
}
