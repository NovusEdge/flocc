package ship

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Resolve is resolveTarget with SSH connection sharing.
func Resolve(ctx context.Context, target string) (Dest, error) {
	mux, err := SSHMux()
	if err != nil {
		return Dest{}, err
	}
	home, _ := os.UserHomeDir()
	d, err := resolveTarget(ctx, target, filepath.Join(home, ".config/daytona/config.json"), &http.Client{Timeout: 30 * time.Second}, realWaiter(os.Stderr))
	if err != nil {
		return d, err
	}
	d.Opts = append(d.Opts, mux...)
	return d, nil
}

// SSHMux makes every ssh to one host in the next ten minutes reuse the first connection, so one
// Tailscale check-mode approval covers a whole run. %C is a hash of the connection, which keeps
// the socket path inside the 104 bytes macOS allows.
func SSHMux() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".ssh")
	// ssh refuses a ControlPath longer than sun_path (104 on macOS), and the master adds a 17-byte
	// temporary suffix; %C expands to 40 hex digits. Past that every ssh would fail, so go without.
	if len(dir)+len("/flocc-")+40+17 >= 104 {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + filepath.Join(dir, "flocc-%C"), "-o", "ControlPersist=10m"}, nil
}

type Dest struct {
	SSH   string
	Token string // a Daytona SSH token inside SSH, redacted from logs
	Opts  []string
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
		return errors.New("Daytona refused the stored login; run daytona list to refresh it, then retry")
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
	all, err := a.sandboxes(ctx, client)
	if err != nil {
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

// maxSandboxPages bounds the walk if the API keeps returning new cursors without advancing.
const maxSandboxPages = 50

// sandboxes returns what it collected alongside any error, so a caller can still show a partial
// list. The cursor parameter name was never seen working against a second page; a page with no
// new IDs is how a wrong one would show up.
func (a daytonaAuth) sandboxes(ctx context.Context, client *http.Client) ([]Sandbox, error) {
	var all []Sandbox
	seen := map[string]bool{}
	cursor := ""
	for range maxSandboxPages {
		path := "/sandbox"
		if cursor != "" {
			path += "?cursor=" + url.QueryEscape(cursor)
		}
		var page struct {
			Items      []Sandbox `json:"items"`
			NextCursor *string   `json:"nextCursor"`
		}
		if err := a.do(ctx, client, "GET", path, &page); err != nil {
			return all, err
		}
		fresh := 0
		for _, s := range page.Items {
			if !seen[s.ID] {
				seen[s.ID] = true
				all = append(all, s)
				fresh++
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return all, nil
		}
		if fresh == 0 {
			return all, fmt.Errorf("the sandbox list did not advance; showing the first %d", len(all))
		}
		cursor = *page.NextCursor
	}
	return all, fmt.Errorf("the sandbox list ran past %d pages; showing the first %d", maxSandboxPages, len(all))
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
