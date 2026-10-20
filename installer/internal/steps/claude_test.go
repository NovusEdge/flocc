package steps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/plugins"
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
	f := host.NewFake().
		On(claudeBin+" --version", 0, "2.1.295 (Claude Code)\n").
		On(claudeBin+" auth status", 0, `{"loggedIn":true,"authMethod":"claude.ai"}`)
	e := claudeEnv(t, f, plugins.List{Plugins: []string{"curt@curt"}})
	writeSettings(t, e, pushOn)
	if done, detail, _ := (Claude{}).Check(context.Background(), e); done || detail != "plugins missing: curt@curt" {
		t.Fatalf("detail=%q", detail)
	}
	writeInstalled(t, e, `{"version":2,"plugins":{"curt@curt":[{"scope":"user"}]}}`)
	if done, _, _ := (Claude{}).Check(context.Background(), e); !done {
		t.Fatal("want done")
	}
	f.On(claudeBin+" auth status", 0, `{"loggedIn":true,"authMethod":"apiKey"}`)
	if done, detail, _ := (Claude{}).Check(context.Background(), e); done || detail != "not logged in to a claude.ai subscription" {
		t.Fatalf("detail=%q", detail)
	}
}

func TestClaudeApplyAfterAbortedLoginSkipsReinstall(t *testing.T) {
	f := host.NewFake().
		On(claudeBin+" --version", 0, "2.1.295\n").
		OnSeq(claudeBin+" auth status",
			host.Result{Stdout: `{"loggedIn":false,"authMethod":"none"}`},
			host.Result{Stdout: `{"loggedIn":true,"authMethod":"claude.ai"}`}).
		On(claudeBin+" plugin marketplace add o/curt", 0, "").
		On(claudeBin+" plugin install curt@curt", 0, "")
	e := claudeEnv(t, f, plugins.List{Marketplaces: []plugins.Marketplace{{Name: "curt", GitHub: "o/curt"}}, Plugins: []string{"curt@curt"}})
	if err := (Claude{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if f.Ran("sh -c curl -fsSL https://claude.ai/install.sh | bash") {
		t.Fatal("reinstalled a current claude")
	}
	if !f.Ran("interactive: "+claudeBin) || !f.Ran(claudeBin+" plugin install curt@curt") {
		t.Fatalf("calls: %v", f.Calls)
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
