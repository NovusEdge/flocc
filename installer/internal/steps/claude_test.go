package steps

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/plugins"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

const claudeBin = "/home/flocc/.local/bin/claude"

func claudeEnv(t *testing.T, f *host.Fake, l plugins.List) *Env {
	e := testEnv(t, f)
	e.Stage = t.TempDir()
	if err := l.Write(filepath.Join(e.Stage, "plugins.json")); err != nil {
		t.Fatal(err)
	}
	return e
}

const pushOn = `{"inputNeededNotifEnabled":true,"agentPushNotifEnabled":false}`

func writeSettings(t *testing.T, e *Env, body string) {
	t.Helper()
	os.MkdirAll(e.HomePath(".claude"), 0o755)
	if err := os.WriteFile(e.HomePath(".claude/settings.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeInstalled(t *testing.T, e *Env, json string) {
	os.MkdirAll(e.HomePath(".claude/plugins"), 0o755)
	os.WriteFile(e.HomePath(".claude/plugins/installed_plugins.json"), []byte(json), 0o644)
}

func TestMinimumClaudeIsTheGateVersion(t *testing.T) {
	if minClaude != [3]int{2, 1, 295} {
		t.Fatalf("minClaude %v", minClaude)
	}
}

func TestClaudeCheck(t *testing.T) {
	f := host.NewFake().On(claudeBin+" --version", 0, "2.1.295 (Claude Code)\n")
	e := claudeEnv(t, f, plugins.List{Plugins: []string{"curt@curt"}})
	writeSettings(t, e, pushOn)
	if done, detail, _ := (Claude{}).Check(context.Background(), e); done || detail != "plugins missing: curt@curt" {
		t.Fatalf("detail=%q", detail)
	}
	writeInstalled(t, e, `{"version":2,"plugins":{"curt@curt":[{"scope":"user"}]}}`)
	if done, detail, _ := (Claude{}).Check(context.Background(), e); !done || detail != "2.1.295 (Claude Code), 1 plugin" {
		t.Fatalf("done=%v detail=%q", done, detail)
	}
}

func TestClaudeApplyNeverLogsIn(t *testing.T) {
	f := host.NewFake().
		On(claudeBin+" --version", 0, "2.1.295\n").
		On(claudeBin+" auth status", 1, `{"loggedIn":false}`).
		On(claudeBin+" plugin marketplace add o/curt", 0, "").
		On(claudeBin+" plugin install curt@curt", 0, "")
	e := claudeEnv(t, f, plugins.List{Marketplaces: []plugins.Marketplace{{Name: "curt", GitHub: "o/curt"}}, Plugins: []string{"curt@curt"}})
	if err := (Claude{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "interactive:") || c == "sh -c curl -fsSL https://claude.ai/install.sh | bash" {
			t.Fatalf("unexpected %s", c)
		}
	}
	if !f.Ran(claudeBin + " plugin install curt@curt") {
		t.Fatalf("calls: %v", f.Calls)
	}
}

func TestClaudeLoginProbesInEveryMode(t *testing.T) {
	for _, c := range []struct {
		code       int
		out, login string
	}{
		{0, `{"loggedIn":true,"authMethod":"claude.ai"}`, "full"},
		{0, `{"loggedIn":true,"authMethod":"oauth_token"}`, "token-only (oauth_token)"},
		{1, `{"loggedIn":false}`, "none"},
		{0, `not json`, "none"},
	} {
		for _, skip := range []bool{false, true} {
			f := host.NewFake().On(claudeBin+" auth status", c.code, c.out)
			e := testEnv(t, f)
			e.SkipInteractive = skip
			if got := claudeLogin(context.Background(), e); got != c.login {
				t.Errorf("%s skip=%v: %q", c.out, skip, got)
			}
			done, detail, _ := (ClaudeLogin{}).Check(context.Background(), e)
			if done != (c.login == "full") || !done && !strings.HasPrefix(detail, c.login+"; ") {
				t.Errorf("%s skip=%v: done=%v detail=%q", c.out, skip, done, detail)
			}
		}
	}
}

func TestClaudeLoginApplyLeavesAMissingLoginPendingAndStartsNothing(t *testing.T) {
	f := host.NewFake().On(claudeBin+" auth status", 1, `{"loggedIn":false}`)
	if err := (ClaudeLogin{}).Apply(context.Background(), testEnv(t, f)); !errors.Is(err, ErrPending) ||
		!slices.Equal(f.Calls, []string{claudeBin + " auth status"}) {
		t.Fatalf("err=%v calls=%v", err, f.Calls)
	}
}

// --interactive offers every finished step for redo; redoing a full login must not keep the
// Run step from starting the timer.
func TestRedoingAFullLoginLeavesNothingPending(t *testing.T) {
	f := host.NewFake().On(claudeBin+" auth status", 0, `{"loggedIn":true,"authMethod":"claude.ai"}`)
	e := testEnv(t, f)
	e.UI = redoUI{Scripted: &ui.Scripted{A: &answers.Answers{}, Out: io.Discard}, redo: []string{"Claude login"}}
	if err := RunAll(context.Background(), e, []Step{ClaudeLogin{}}); err != nil || len(e.Pending) != 0 {
		t.Fatalf("err=%v pending=%v", err, e.Pending)
	}
}

func TestClaudeArchivedMarketplaceIsExtractedRegisteredAndTracked(t *testing.T) {
	dir := "/home/flocc/marketplaces/NovusEdge"
	f := host.NewFake().
		On(claudeBin+" --version", 0, "2.1.295\n").
		On(claudeBin+" auth status", 0, `{"loggedIn":true,"authMethod":"claude.ai"}`)
	l := plugins.List{Marketplaces: []plugins.Marketplace{{Name: "NovusEdge", Archive: "mp-NovusEdge.tar", Commit: "c1"}},
		Plugins: []string{"docket@NovusEdge"}}
	e := claudeEnv(t, f, l)
	f.On("tar -xf "+filepath.Join(e.Stage, "mp-NovusEdge.tar")+" -C "+dir, 0, "").
		On(claudeBin+" plugin marketplace add "+dir, 0, "").
		On(claudeBin+" plugin install docket@NovusEdge", 0, "")
	if err := (Claude{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran(claudeBin + " plugin marketplace add " + dir) {
		t.Fatalf("calls: %v", f.Calls)
	}
	writeInstalled(t, e, `{"plugins":{"docket@NovusEdge":[{"scope":"user"}]}}`)
	if done, _, _ := (Claude{}).Check(context.Background(), e); !done {
		t.Fatal("want done after extracting c1")
	}
	l.Marketplaces[0].Commit = "c2"
	l.Write(filepath.Join(e.Stage, "plugins.json"))
	if done, detail, _ := (Claude{}).Check(context.Background(), e); done || detail != "marketplace NovusEdge out of date" {
		t.Fatalf("detail=%q", detail)
	}
}

func TestMergePushSettings(t *testing.T) {
	for name, in := range map[string]string{
		"missing":  "",
		"blank":    " \n",
		"null":     "null",
		"wrong":    `{"inputNeededNotifEnabled":false,"agentPushNotifEnabled":true,"theme":"dark","hooks":{"a":[1]}}`,
		"preserve": `{"theme":"dark","hooks":{"a":[1]}}`,
	} {
		out, err := mergePushSettings([]byte(in))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if m["inputNeededNotifEnabled"] != true || m["agentPushNotifEnabled"] != false {
			t.Fatalf("%s: %s", name, out)
		}
		if name != "missing" && name != "blank" && name != "null" && (m["theme"] != "dark" || m["hooks"] == nil) {
			t.Fatalf("%s lost keys: %s", name, out)
		}
		if !strings.Contains(string(out), "\n  \"") {
			t.Fatalf("%s not 2-space indented: %s", name, out)
		}
	}
}

func TestClaudeApplyLeavesCorrectSettingsUntouched(t *testing.T) {
	f := host.NewFake().
		On(claudeBin+" --version", 0, "2.1.295\n").
		On(claudeBin+" auth status", 0, `{"loggedIn":true,"authMethod":"claude.ai"}`)
	e := claudeEnv(t, f, plugins.List{})
	const body = `{"z":"a<b","inputNeededNotifEnabled":true,"agentPushNotifEnabled":false}`
	writeSettings(t, e, body)
	if err := (Claude{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(e.HomePath(".claude/settings.json")); string(b) != body {
		t.Fatalf("rewrote: %s", b)
	}
}

func TestClaudeApplyWritesPushSettings(t *testing.T) {
	f := host.NewFake().
		On(claudeBin+" --version", 0, "2.1.295\n").
		On(claudeBin+" auth status", 0, `{"loggedIn":true,"authMethod":"claude.ai"}`)
	e := claudeEnv(t, f, plugins.List{})
	if done, detail, _ := (Claude{}).Check(context.Background(), e); done || detail != "push settings not applied" {
		t.Fatalf("detail=%q", detail)
	}
	writeSettings(t, e, `{"theme":"dark"}`)
	if err := (Claude{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if done, detail, _ := (Claude{}).Check(context.Background(), e); !done {
		t.Fatalf("detail=%q", detail)
	}
	st, err := os.Stat(e.HomePath(".claude/settings.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stat: %v %v", st, err)
	}
	b, _ := os.ReadFile(e.HomePath(".claude/settings.json"))
	if !strings.Contains(string(b), `"theme": "dark"`) {
		t.Fatalf("lost theme: %s", b)
	}
}

func TestClaudeApplyRejectsInvalidSettingsWithoutOverwriting(t *testing.T) {
	f := host.NewFake().
		On(claudeBin+" --version", 0, "2.1.295\n").
		On(claudeBin+" auth status", 0, `{"loggedIn":true,"authMethod":"claude.ai"}`)
	e := claudeEnv(t, f, plugins.List{})
	writeSettings(t, e, `{oops`)
	err := (Claude{}).Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "/home/flocc/.claude/settings.json") {
		t.Fatalf("err=%v", err)
	}
	if b, _ := os.ReadFile(e.HomePath(".claude/settings.json")); string(b) != `{oops` {
		t.Fatalf("overwrote: %s", b)
	}
}
