package steps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
	"github.com/NovusEdge/linear-agents/installer/internal/plugins"
)

// The --name and --remote-control flags the launcher passes need this version.
var minClaude = [3]int{2, 1, 293}

const loginNote = `Claude Code opens next, in ~/agents/worktrees.
1. Type /login and finish it in the browser with the subscription account.
2. In /config, turn on "Push when actions required" and turn off "Push when Claude decides".
3. Accept "Enable Remote Control" if asked.
4. Leave with /exit.`

type Claude struct{}

func (Claude) Name() string { return "Claude" }

func (e *Env) claude() string { return e.Real(".local/bin/claude") }

func claudeVersion(ctx context.Context, e *Env) (bool, string) {
	v := e.Output(ctx, e.claude(), "--version")
	if v == "" {
		return false, "not installed"
	}
	return host.AtLeast(v, minClaude), v
}

func loggedIn(ctx context.Context, e *Env) bool {
	if e.SkipInteractive {
		return true
	}
	var s struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	return json.Unmarshal([]byte(e.Output(ctx, e.claude(), "auth", "status")), &s) == nil &&
		s.LoggedIn && s.AuthMethod == "claude.ai"
}

func missingPlugins(e *Env, l plugins.List) []string {
	var installed struct {
		Plugins map[string][]struct{ Scope string } `json:"plugins"`
	}
	b, _ := os.ReadFile(e.HomePath(".claude/plugins/installed_plugins.json"))
	json.Unmarshal(b, &installed)
	var missing []string
	for _, p := range l.Plugins {
		user := false
		for _, inst := range installed.Plugins[p] {
			user = user || inst.Scope == "user"
		}
		if !user {
			missing = append(missing, p)
		}
	}
	return missing
}

const marketplacesRel = "agents/marketplaces"

// extractArchive replaces dir (a real path) with the archive's tree and records its commit.
func extractArchive(ctx context.Context, e *Env, archive, dir, commit string) error {
	if err := os.RemoveAll(e.Path(dir)); err != nil {
		return err
	}
	if err := os.MkdirAll(e.Path(dir), 0o755); err != nil {
		return err
	}
	if _, err := e.Must(ctx, "tar", "-xf", archive, "-C", dir); err != nil {
		return err
	}
	return os.WriteFile(e.Path(filepath.Join(dir, ".la-commit")), []byte(commit), 0o644)
}

func knownMarketplaces(e *Env) map[string]json.RawMessage {
	known := map[string]json.RawMessage{}
	b, _ := os.ReadFile(e.HomePath(".claude/plugins/known_marketplaces.json"))
	json.Unmarshal(b, &known)
	return known
}

func (Claude) Check(ctx context.Context, e *Env) (bool, string, error) {
	l, err := plugins.Load(filepath.Join(e.Stage, "plugins.json"))
	if err != nil {
		return false, "", err
	}
	if ok, v := claudeVersion(ctx, e); !ok {
		return false, "claude " + v + "; needs 2.1.293", nil
	}
	if !loggedIn(ctx, e) {
		return false, "not logged in to a claude.ai subscription", nil
	}
	if m := missingPlugins(e, l); len(m) > 0 {
		return false, "plugins missing: " + strings.Join(m, ", "), nil
	}
	for _, m := range l.Marketplaces {
		if m.Archive == "" {
			continue
		}
		if b, _ := os.ReadFile(e.HomePath(filepath.Join(marketplacesRel, m.Name, ".la-commit"))); string(b) != m.Commit {
			return false, "marketplace " + m.Name + " out of date", nil
		}
	}
	_, v := claudeVersion(ctx, e)
	return true, v + ", logged in, " + plural(len(l.Plugins), "plugin"), nil
}

func (Claude) Apply(ctx context.Context, e *Env) error {
	l, err := plugins.Load(filepath.Join(e.Stage, "plugins.json"))
	if err != nil {
		return err
	}
	if ok, _ := claudeVersion(ctx, e); !ok {
		if err := e.UI.Spin("Installing Claude Code", func() error {
			_, err := e.Must(ctx, "sh", "-c", "curl -fsSL https://claude.ai/install.sh | bash")
			return err
		}); err != nil {
			return err
		}
	}
	if !loggedIn(ctx, e) {
		e.UI.Note("Claude login", loginNote)
		if err := os.MkdirAll(e.HomePath(worktreesRel), 0o755); err != nil {
			return err
		}
		if err := e.Run.Interactive(ctx, host.Cmd{Name: e.claude(), Dir: e.Real(worktreesRel)}); err != nil {
			return err
		}
		if !loggedIn(ctx, e) {
			return errors.New("still not logged in to a claude.ai subscription (claude auth status)")
		}
	}
	known := knownMarketplaces(e)
	for _, m := range l.Marketplaces {
		source := m.GitHub
		if m.Archive != "" {
			// Replaced on every run: a directory marketplace loads its plugins in place.
			source = e.Real(filepath.Join(marketplacesRel, m.Name))
			if err := extractArchive(ctx, e, filepath.Join(e.Stage, m.Archive), source, m.Commit); err != nil {
				return err
			}
		}
		if _, ok := known[m.Name]; ok {
			continue
		}
		if _, err := e.Must(ctx, e.claude(), "plugin", "marketplace", "add", source); err != nil {
			return err
		}
	}
	for _, p := range missingPlugins(e, l) {
		if _, err := e.Must(ctx, e.claude(), "plugin", "install", p); err != nil {
			return err
		}
	}
	return nil
}
