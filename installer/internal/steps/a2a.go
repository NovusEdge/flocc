package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/NovusEdge/flocc/deploy"
)

// A2A writes the host's a2a.toml and runs flocc-a2a.service. The server reads the file on every
// request, but a new checkout needs a restart, so the step compares the HEAD the server started on.
type A2A struct{}

const a2aFile = ".config/flocc/a2a.toml"

func (A2A) Name() string { return "A2A" }

func (A2A) want(e *Env) ([]byte, error) {
	if e.Answers == nil || e.Answers.Secrets == nil || e.Answers.Secrets.A2A == nil {
		return nil, nil
	}
	return toml.Marshal(*e.Answers.Secrets.A2A)
}

func a2aUnitPath(e *Env) string {
	return e.HomePath(filepath.Join(".config/systemd/user", deploy.A2AUnit))
}

func (a A2A) Check(ctx context.Context, e *Env) (bool, string, error) {
	want, err := a.want(e)
	if err != nil || want == nil {
		return err == nil, "not shipped from a desktop with A2A", err
	}
	if got, _ := os.ReadFile(e.HomePath(a2aFile)); !bytes.Equal(got, want) {
		return false, "a2a.toml differs from the desktop's", nil
	}
	if e.Mode != Systemd {
		return true, "loop mode: no A2A service", nil
	}
	unit, err := deploy.Files.ReadFile(deploy.A2AUnit)
	if err != nil {
		return false, "", err
	}
	if got, _ := os.ReadFile(a2aUnitPath(e)); !bytes.Equal(got, unit) {
		return false, deploy.A2AUnit + " not installed or out of date", nil
	}
	if r, err := e.Run.Run(ctx, e.userctl("is-active", "--quiet", deploy.A2AUnit)); err != nil || r.Code != 0 {
		return false, "flocc-a2a not running", nil
	}
	if h := servedHead(e); h == "" || h != e.Output(ctx, "git", "-C", e.Real(repoRel), "rev-parse", "HEAD") {
		return false, "serving an older flocc", nil
	}
	return true, "serving on port 7443", nil
}

func (a A2A) Apply(ctx context.Context, e *Env) error {
	want, err := a.want(e)
	if err != nil || want == nil {
		return err
	}
	if err := os.MkdirAll(e.HomePath(".config/flocc"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(e.HomePath(a2aFile), want, 0o600); err != nil {
		return err
	}
	if e.Mode != Systemd {
		return nil
	}
	unit, err := deploy.Files.ReadFile(deploy.A2AUnit)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a2aUnitPath(e)), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(a2aUnitPath(e), unit, 0o644); err != nil {
		return err
	}
	for _, c := range [][]string{{"daemon-reload"}, {"enable", deploy.A2AUnit}, {"restart", deploy.A2AUnit}} {
		if _, err := e.MustCmd(ctx, e.userctl(c...)); err != nil {
			return err
		}
	}
	// The server writes a2a-serve.json about a second after systemd reports it started, and the
	// runner checks right after Apply.
	head := e.Output(ctx, "git", "-C", e.Real(repoRel), "rev-parse", "HEAD")
	for range 20 {
		if servedHead(e) == head {
			return nil
		}
		if err := waitFor(ctx, e, time.Second); err != nil {
			return err
		}
	}
	return nil
}

func servedHead(e *Env) string {
	var served struct {
		Head string `json:"head"`
	}
	b, _ := os.ReadFile(e.HomePath(".local/state/flocc/a2a-serve.json"))
	_ = json.Unmarshal(b, &served)
	return served.Head
}
