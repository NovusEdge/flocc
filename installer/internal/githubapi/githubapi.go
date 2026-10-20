// Package githubapi is the part of GitHub's REST API the desktop uses: token checks that change
// nothing, and repo discovery.
package githubapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const DefaultBase = "https://api.github.com"

var (
	ErrBadCredentials = errors.New("GitHub rejected the token: it expired, was revoked or is mistyped")
	ErrNotVisible     = errors.New("not visible to this token")
)

const (
	maxBody  = 1 << 20
	maxPages = 50
)

type Client struct {
	Base, Token string
	HTTP        *http.Client
}

func New(token string) *Client {
	return &Client{Base: DefaultBase, Token: token, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

type Repo struct {
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, []byte, error) {
	var in io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		in = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, in)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, nil, err
	}
	if len(b) > maxBody {
		return nil, nil, fmt.Errorf("GitHub %s %s: response larger than %d bytes", method, req.URL.Path, maxBody)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return resp, b, ErrBadCredentials
	}
	return resp, b, nil
}

// lacksPermission is true for the 403 GitHub sends when the token's grants do not cover the call.
// Rate limits, archived repos and SSO enforcement are 403 too, with other messages, and say
// nothing about the token's permissions.
func lacksPermission(resp *http.Response, body []byte) bool {
	var e struct{ Message string }
	json.Unmarshal(body, &e)
	return resp.StatusCode == http.StatusForbidden && strings.HasPrefix(e.Message, "Resource not accessible")
}

func apiError(resp *http.Response, body []byte) error {
	var e struct{ Message string }
	json.Unmarshal(body, &e)
	return fmt.Errorf("GitHub %s %s: %d %s", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, e.Message)
}

// Expiry is when the token expires; the zero time for a token that never does.
func (c *Client) Expiry(ctx context.Context) (time.Time, error) {
	resp, b, err := c.do(ctx, "GET", "/user", nil)
	if err != nil {
		return time.Time{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, apiError(resp, b)
	}
	h := resp.Header.Get("GitHub-Authentication-Token-Expiration")
	if h == "" {
		return time.Time{}, nil
	}
	return time.Parse("2006-01-02 15:04:05 MST", h)
}

func (c *Client) Repo(ctx context.Context, slug string) (Repo, error) {
	resp, b, err := c.do(ctx, "GET", "/repos/"+slug, nil)
	if err != nil {
		return Repo{}, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		var r Repo
		return r, json.Unmarshal(b, &r)
	case http.StatusNotFound:
		return Repo{}, fmt.Errorf("%s: %w", slug, ErrNotVisible)
	}
	return Repo{}, apiError(resp, b)
}

// CanWriteContents reports whether the token may push to slug, asked without changing
// anything. A ref at the all-zero SHA can never be created, and GitHub checks permission
// before the object, so 422 means the write itself was allowed. A refusal is 403, or 404 when
// the token can read the repo but not write it, the same answer as for a repo it cannot see.
func (c *Client) CanWriteContents(ctx context.Context, slug string) (bool, error) {
	resp, b, err := c.do(ctx, "POST", "/repos/"+slug+"/git/refs",
		map[string]string{"ref": "refs/heads/flocc-doctor-probe", "sha": strings.Repeat("0", 40)})
	if err != nil {
		return false, err
	}
	switch {
	case lacksPermission(resp, b):
		return false, nil
	case resp.StatusCode == http.StatusUnprocessableEntity:
		return true, nil
	case resp.StatusCode == http.StatusNotFound:
		_, err := c.Repo(ctx, slug)
		return false, err
	case resp.StatusCode < 300:
		return false, fmt.Errorf("GitHub accepted a ref at the zero SHA in %s", slug)
	}
	return false, apiError(resp, b)
}

// probeHead is a branch no repo has, so the pull request probe can never open one.
const probeHead = "flocc-probe-no-such-branch"

// CanOpenPulls asks for a pull request from probeHead. GitHub checks the caller's permission
// before it validates the head, so 422 means opening one is allowed and 403 means it is not.
func (c *Client) CanOpenPulls(ctx context.Context, slug, base string) (bool, error) {
	resp, b, err := c.do(ctx, "POST", "/repos/"+slug+"/pulls",
		map[string]string{"title": "flocc permission probe", "head": probeHead, "base": base})
	if err != nil {
		return false, err
	}
	switch {
	case resp.StatusCode == http.StatusUnprocessableEntity:
		return true, nil
	case lacksPermission(resp, b):
		return false, nil
	case resp.StatusCode == http.StatusNotFound:
		_, err := c.Repo(ctx, slug)
		return false, err
	case resp.StatusCode < 300:
		return false, fmt.Errorf("GitHub opened a pull request in %s from %s; close it", slug, probeHead)
	}
	return false, apiError(resp, b)
}

var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// ListRepos returns every page of /user/repos concatenated, the form steps.ParseRepos reads.
// Only the path and query of each next link are kept, so every page comes from c.Base.
func (c *Client) ListRepos(ctx context.Context) ([]byte, error) {
	path := "/user/repos?per_page=100&affiliation=owner,collaborator,organization_member"
	var all []byte
	seen := map[string]bool{}
	for page := 0; path != ""; page++ {
		if page == maxPages || seen[path] {
			return nil, fmt.Errorf("GitHub /user/repos: more than %d pages or a page link that repeats", maxPages)
		}
		seen[path] = true
		resp, b, err := c.do(ctx, "GET", path, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, apiError(resp, b)
		}
		all = append(all, b...)
		path = ""
		if m := nextLink.FindStringSubmatch(resp.Header.Get("Link")); m != nil {
			if u, err := url.Parse(m[1]); err == nil {
				path = u.RequestURI()
			}
		}
	}
	return all, nil
}
