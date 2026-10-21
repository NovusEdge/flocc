package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/plugins"
)

// The gate hook's onFailure setting needs this version.
var minClaude = [3]int{2, 1, 295}

const settingsRel = ".claude/settings.json"

// pushSettings are the /config toggles "Push when actions required" and "Push when Claude decides".
var pushSettings = map[string]bool{"inputNeededNotifEnabled": true, "agentPushNotifEnabled": false}

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

// claudeLogin is "full" for a claude.ai login, "token-only (<method>)" for any other, or "none".
// Only a full login can open Remote Control sessions.
func claudeLogin(ctx context.Context, e *Env) string {
	var s struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	if json.Unmarshal([]byte(e.Output(ctx, e.claude(), "auth", "status")), &s) != nil || !s.LoggedIn {
		return "none"
	}
	if s.AuthMethod == "claude.ai" {
		return "full"
	}
	return "token-only (" + s.AuthMethod + ")"
}

// ClaudeLogin reports the login and never does it: it needs a browser and the owner, so ship
// leaves it to flocc login.
type ClaudeLogin struct{}

func (ClaudeLogin) Name() string { return "Claude login" }

func (ClaudeLogin) Check(ctx context.Context, e *Env) (bool, string, error) {
	if l := claudeLogin(ctx, e); l != "full" {
		return false, l + "; flocc login -H <host> does it after ship", nil
	}
	return true, "claude.ai subscription", nil
}

// Apply is reached for a full login only when --interactive offers the step for redo; pending
// then would keep the Run step from starting the timer.
func (ClaudeLogin) Apply(ctx context.Context, e *Env) error {
	if claudeLogin(ctx, e) == "full" {
		return nil
	}
	return ErrPending
}

// mergePushSettings returns existing settings JSON (empty means none) with the push keys set
// and every other key kept.
func mergePushSettings(existing []byte, want map[string]bool) ([]byte, error) {
	m := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &m); err != nil {
			return nil, err
		}
	}
	if m == nil { // the file held a literal null
		m = map[string]json.RawMessage{}
	}
	for k, v := range want {
		b, _ := json.Marshal(v)
		m[k] = b
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(m)
	return buf.Bytes(), err
}

func pushSettingsApplied(e *Env, want map[string]bool) bool {
	b, _ := os.ReadFile(e.HomePath(settingsRel))
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	for k, v := range want {
		if m[k] != v {
			return false
		}
	}
	return true
}

func applyPushSettings(e *Env, want map[string]bool) error {
	if pushSettingsApplied(e, want) {
		return nil
	}
	path := e.HomePath(settingsRel)
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	out, err := mergePushSettings(b, want)
	if err != nil {
		return fmt.Errorf("%s: %w", e.Real(settingsRel), err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// CreateTemp makes the file 0600, which also tightens a looser existing settings.json.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
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

const marketplacesRel = "marketplaces"

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
	return os.WriteFile(e.Path(filepath.Join(dir, ".flocc-commit")), []byte(commit), 0o644)
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
		return false, "claude " + v + "; needs 2.1.295", nil
	}
	if !pushSettingsApplied(e, pushSettings) {
		return false, "push settings not applied", nil
	}
	if m := missingPlugins(e, l); len(m) > 0 {
		return false, "plugins missing: " + strings.Join(m, ", "), nil
	}
	for _, m := range l.Marketplaces {
		if m.Archive == "" {
			continue
		}
		if b, _ := os.ReadFile(e.HomePath(filepath.Join(marketplacesRel, m.Name, ".flocc-commit"))); string(b) != m.Commit {
			return false, "marketplace " + m.Name + " out of date", nil
		}
	}
	_, v := claudeVersion(ctx, e)
	return true, v + ", " + plural(len(l.Plugins), "plugin"), nil
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
	if err := applyPushSettings(e, pushSettings); err != nil {
		return err
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
