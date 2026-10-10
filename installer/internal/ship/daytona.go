package ship

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Dest struct {
	SSH   string
	Token string // a Daytona SSH token inside SSH, redacted from logs
	Opts  []string
}

// redact keeps a credential out of the command echo on the desktop terminal.
type redact struct {
	w      io.Writer
	secret string
}

func (r redact) Write(p []byte) (int, error) {
	if r.secret != "" {
		if _, err := r.w.Write(bytes.ReplaceAll(p, []byte(r.secret), []byte("<token>"))); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return r.w.Write(p)
}

type daytonaAuth struct{ url, bearer, org string }

func loadDaytona(path string) (daytonaAuth, error) {
	var c struct {
		ActiveProfile string `json:"activeProfile"`
		Profiles      []struct {
			ID                   string `json:"id"`
			ActiveOrganizationID string `json:"activeOrganizationId"`
			API                  struct {
				URL   string          `json:"url"`
				Key   string          `json:"key"`
				Token json.RawMessage `json:"token"`
			} `json:"api"`
		} `json:"profiles"`
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return daytonaAuth{}, fmt.Errorf("%w; log the Daytona CLI in with daytona login", err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return daytonaAuth{}, err
	}
	for _, p := range c.Profiles {
		if p.ID != c.ActiveProfile {
			continue
		}
		// A browser login stores a token object; the API key alone is refused for SSH access.
		var tok struct {
			AccessToken string `json:"accessToken"`
		}
		bearer := p.API.Key
		if json.Unmarshal(p.API.Token, &tok) == nil && tok.AccessToken != "" {
			bearer = tok.AccessToken
		}
		return daytonaAuth{url: p.API.URL, bearer: bearer, org: p.ActiveOrganizationID}, nil
	}
	return daytonaAuth{}, errors.New("no active Daytona profile; run daytona login")
}

func (a daytonaAuth) do(ctx context.Context, client *http.Client, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, a.url+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.bearer)
	req.Header.Set("X-Daytona-Organization-ID", a.org)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		// The CLI refreshes its stored access token; this reader does not.
		return errors.New("Daytona refused the stored login; run daytona list to refresh it, then ship again")
	}
	if resp.StatusCode >= 300 {
		return &apiError{fmt.Sprintf("Daytona API %s %s: %s", method, path, resp.Status), resp.StatusCode}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type apiError struct {
	msg  string
	code int
}

func (e *apiError) Error() string { return e.msg }

// sandboxID looks a sandbox up by name, falling back to the list if the API wants an id.
func (a daytonaAuth) sandboxID(ctx context.Context, client *http.Client, name string) (string, error) {
	var sb struct{ ID string }
	err := a.do(ctx, client, "GET", "/sandbox/"+url.PathEscape(name), &sb)
	var api *apiError
	if !errors.As(err, &api) || api.code != http.StatusNotFound {
		return sb.ID, err
	}
	var all []struct{ ID, Name string }
	if err := a.do(ctx, client, "GET", "/sandbox", &all); err != nil {
		return "", err
	}
	for _, s := range all {
		if s.Name == name {
			return s.ID, nil
		}
	}
	return "", fmt.Errorf("no Daytona sandbox named %q in this organization", name)
}

type Sandbox struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	State  string `json:"state"`
	CPU    int    `json:"cpu"`
	Memory int    `json:"memory"` // GiB
	Disk   int    `json:"disk"`   // GiB
}

func (a daytonaAuth) sandboxes(ctx context.Context, client *http.Client) ([]Sandbox, error) {
	var all []Sandbox
	err := a.do(ctx, client, "GET", "/sandbox", &all)
	return all, err
}

const (
	waitEvery   = 5 * time.Second
	waitTimeout = 5 * time.Minute
)

// waiter is injected so tests run the wait without sleeping.
type waiter struct {
	sleep func(ctx context.Context, d time.Duration) error
	now   func() time.Time
	log   io.Writer
}

func realWaiter(log io.Writer) waiter {
	return waiter{
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
		now: time.Now,
		log: log,
	}
}

// waitStarted blocks until the sandbox is started: the SSH gateway drops connections to a sandbox still in CREATING.
func (a daytonaAuth) waitStarted(ctx context.Context, client *http.Client, id, name string, w waiter) error {
	deadline := w.now().Add(waitTimeout)
	last := ""
	for {
		var sb struct{ State string }
		if err := a.do(ctx, client, "GET", "/sandbox/"+url.PathEscape(id), &sb); err != nil {
			return err
		}
		state := strings.ToLower(sb.State)
		switch {
		case state == "":
			return fmt.Errorf("Daytona sandbox %s: the API response has no state field; the API shape may have changed", name)
		case state == "started":
			return nil
		case state == "stopped" || state == "archived":
			return fmt.Errorf("Daytona sandbox %s is %s; start it with daytona start %s", name, state, name)
		case state == "destroying":
			return fmt.Errorf("Daytona sandbox %s is being deleted; create it again with daytona create", name)
		case state == "destroyed":
			return fmt.Errorf("Daytona sandbox %s was deleted; create it again with daytona create", name)
		case state == "error" || strings.HasSuffix(state, "failed"):
			return fmt.Errorf("Daytona sandbox %s is in state %s", name, state)
		}
		if state != last {
			fmt.Fprintf(w.log, "ship: waiting for Daytona sandbox %s (state %s)\n", name, state)
			last = state
		}
		if !w.now().Add(waitEvery).Before(deadline) {
			return fmt.Errorf("Daytona sandbox %s did not start within %s (last state %s)", name, waitTimeout, state)
		}
		if err := w.sleep(ctx, waitEvery); err != nil {
			return err
		}
	}
}

func resolveTarget(ctx context.Context, target, daytonaConfig string, client *http.Client, w waiter) (Dest, error) {
	name, ok := strings.CutPrefix(target, "daytona:")
	if !ok {
		return Dest{SSH: target}, nil
	}
	auth, err := loadDaytona(daytonaConfig)
	if err != nil {
		return Dest{}, err
	}
	id, err := auth.sandboxID(ctx, client, name)
	if err != nil {
		return Dest{}, err
	}
	return auth.sshDest(ctx, client, id, name, w)
}

// sshDest waits for the sandbox to start, then mints a 60-minute SSH token for it.
func (auth daytonaAuth) sshDest(ctx context.Context, client *http.Client, id, name string, w waiter) (Dest, error) {
	if err := auth.waitStarted(ctx, client, id, name, w); err != nil {
		return Dest{}, err
	}
	var access struct {
		Token      string `json:"token"`
		SSHCommand string `json:"sshCommand"`
	}
	if err := auth.do(ctx, client, "POST", "/sandbox/"+id+"/ssh-access?expiresInMinutes=60", &access); err != nil {
		return Dest{}, err
	}
	host := "ssh.app.daytona.io"
	if f := strings.Fields(access.SSHCommand); len(f) > 0 {
		if _, h, ok := strings.Cut(f[len(f)-1], "@"); ok {
			host = h
		}
	}
	return Dest{SSH: access.Token + "@" + host, Token: access.Token, Opts: []string{"-o", "StrictHostKeyChecking=accept-new"}}, nil
}
