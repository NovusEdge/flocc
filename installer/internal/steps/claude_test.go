package steps

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
	"github.com/NovusEdge/linear-agents/installer/internal/plugins"
)

const claudeBin = "/home/linear-agents/.local/bin/claude"

func claudeEnv(t *testing.T, f *host.Fake, l plugins.List) *Env {
	e := testEnv(t, f)
	e.Stage = t.TempDir()
	if err := l.Write(filepath.Join(e.Stage, "plugins.json")); err != nil {
		t.Fatal(err)
	}
	return e
}

func writeInstalled(t *testing.T, e *Env, json string) {
	os.MkdirAll(e.HomePath(".claude/plugins"), 0o755)
	os.WriteFile(e.HomePath(".claude/plugins/installed_plugins.json"), []byte(json), 0o644)
}

func TestClaudeCheck(t *testing.T) {
	f := host.NewFake().
		On(claudeBin+" --version", 0, "2.1.295 (Claude Code)\n").
		On(claudeBin+" auth status", 0, `{"loggedIn":true,"authMethod":"claude.ai"}`)
	e := claudeEnv(t, f, plugins.List{Plugins: []string{"curt@curt"}})
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
	dir := "/home/linear-agents/agents/marketplaces/NovusEdge"
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
