package steps

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/deploy"
	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ui"
	"github.com/pelletier/go-toml/v2"
)

var update = flag.Bool("update", false, "rewrite golden files")

func goldenAnswers() *answers.Answers {
	return &answers.Answers{TeamKey: "NF", DigestIssue: "NF-186",
		Repos: []answers.Repo{{Name: "nimblefox-template", URL: "https://github.com/nimble-fox-ai/nimblefox-template",
			BaseBranch: "main", Check: "mise exec -- just check", Draft: false}}}
}

func TestRenderConfigMatchesGolden(t *testing.T) {
	example, _ := deploy.Files.ReadFile("config.example.toml")
	got, err := RenderConfig(example, nil, goldenAnswers())
	if err != nil {
		t.Fatal(err)
	}
	const golden = "testdata/config.golden.toml"
	if *update {
		os.WriteFile(golden, got, 0o644)
	}
	want, _ := os.ReadFile(golden)
	if string(got) != string(want) {
		t.Fatalf("render differs from %s; run go test ./installer/internal/steps -update\n%s", golden, got)
	}
	for _, s := range []string{"mcp_servers", "backend", "sentry"} {
		if strings.Contains(string(got), s) {
			t.Errorf("example placeholder %q kept", s)
		}
	}
}

func TestRenderConfigSetsSkipUnmappedOnlyForASharedTeam(t *testing.T) {
	example, _ := deploy.Files.ReadFile("config.example.toml")
	plain, err := RenderConfig(example, nil, goldenAnswers())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "skip_unmapped") {
		t.Errorf("skip_unmapped rendered for a single host:\n%s", plain)
	}
	// Auto-excluded empty repos and repos the owner just does not want land here too.
	a := goldenAnswers()
	a.ExcludedRepos = []string{"https://github.com/nimble-fox-ai/empty"}
	excluded, err := RenderConfig(example, nil, a)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(excluded), "skip_unmapped") {
		t.Errorf("excluding a repo alone turned skip_unmapped on:\n%s", excluded)
	}
	a.SharedTeam = true
	got, err := RenderConfig(example, nil, a)
	if err != nil {
		t.Fatal(err)
	}
	const golden = "testdata/config.skip.golden.toml"
	if *update {
		os.WriteFile(golden, got, 0o644)
	}
	want, _ := os.ReadFile(golden)
	if string(got) != string(want) {
		t.Fatalf("render differs from %s; run go test ./installer/internal/steps -update\n%s", golden, got)
	}
}

func TestRenderConfigKeepsKeysTheWizardDoesNotAsk(t *testing.T) {
	example, _ := deploy.Files.ReadFile("config.example.toml")
	existing := []byte(`team_key = "OLD"
setup_timeout_s = 1800
shared_uv_cache = true

[repos.nimblefox-template]
path = "~/src/nimblefox-template"
base_branch = "dev"
commit_trailer = false

[repos.dropped]
path = "~/src/dropped"

[mcp_servers.sentry]
type = "http"
url = "https://mcp.sentry.dev/mcp"
`)
	got, err := RenderConfig(example, existing, goldenAnswers())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := toml.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	if m["team_key"] != "NF" || m["setup_timeout_s"] != int64(1800) || m["shared_uv_cache"] != true {
		t.Errorf("top-level keys: team_key %v, setup_timeout_s %v, shared_uv_cache %v", m["team_key"], m["setup_timeout_s"], m["shared_uv_cache"])
	}
	if _, ok := m["mcp_servers"].(map[string]any)["sentry"]; !ok {
		t.Errorf("the host's mcp_servers were dropped:\n%s", got)
	}
	repos := m["repos"].(map[string]any)
	if _, ok := repos["dropped"]; ok {
		t.Errorf("a repo the answers no longer map was kept:\n%s", got)
	}
	repo := repos["nimblefox-template"].(map[string]any)
	if repo["base_branch"] != "main" || repo["commit_trailer"] != false {
		t.Errorf("repo: base_branch %v, commit_trailer %v", repo["base_branch"], repo["commit_trailer"])
	}
}

func TestRenderConfigRefusesAnUnreadableConfig(t *testing.T) {
	example, _ := deploy.Files.ReadFile("config.example.toml")
	if _, err := RenderConfig(example, []byte("team_key = "), goldenAnswers()); err == nil {
		t.Fatal("a broken config.toml was overwritten")
	}
}

func TestDoctorRunsWithTheServicePath(t *testing.T) {
	var got []string
	r := envRecorder{Fake: host.NewFake(), env: &got}
	e := testEnv(t, r.Fake)
	e.Run = r
	doctor(context.Background(), e)
	if len(got) != 1 || !strings.HasPrefix(got[0], "PATH=/home/flocc/.local/bin:") {
		t.Fatalf("env %v", got)
	}
}

type envRecorder struct {
	*host.Fake
	env *[]string
}

func (r envRecorder) Run(ctx context.Context, c host.Cmd) (host.Result, error) {
	*r.env = c.Env
	return r.Fake.Run(ctx, c)
}

func TestConfigCheckUsesDoctorConfigAndRepoLines(t *testing.T) {
	bin := "/home/flocc/.local/bin/flocc doctor"
	f := host.NewFake().On(bin, 1, "ok    config: /x\nFAIL  repo r: /home/flocc/src/r is not a git clone\nFAIL  claude login: none\n")
	done, detail, _ := (Config{}).Check(context.Background(), testEnv(t, f))
	if done || detail != "repo r: /home/flocc/src/r is not a git clone" {
		t.Fatalf("detail=%q", detail)
	}
	f.On(bin, 1, "ok    config: /x\nFAIL  claude login: none\n")
	if done, _, _ := (Config{}).Check(context.Background(), testEnv(t, f)); !done {
		t.Fatal("non-repo FAIL lines belong to other steps")
	}
	f.On(bin, 1, "ok    config: /x\nFAIL  repo r: cannot fetch origin\n")
	if done, _, _ := (Config{}).Check(context.Background(), testEnv(t, f)); !done {
		t.Fatal("a fetch failure is transient, not a missing config")
	}
	f.On(bin, 1, "FAIL  config: digest_issue is required\n")
	if done, _, _ := (Config{}).Check(context.Background(), testEnv(t, f)); done {
		t.Fatal("config FAIL must not be done")
	}
}

func TestConfigApplyKeepsAnswersWhenValidationFails(t *testing.T) {
	e := testEnv(t, host.NewFake())
	e.AnswersPath = filepath.Join(t.TempDir(), "answers.toml")
	e.Answers.DigestIssue = "NF-1"
	e.Answers.Repos = []answers.Repo{{Name: "r", URL: "not-a-url"}}
	e.UI = &ui.Scripted{A: e.Answers, Out: io.Discard}
	if err := (Config{}).Apply(context.Background(), e); err == nil {
		t.Fatal("want a validation error")
	}
	saved, err := answers.Load(e.AnswersPath)
	if err != nil || saved.DigestIssue != "NF-1" || len(saved.Repos) != 1 {
		t.Fatalf("%+v %v", saved, err)
	}
}

// gitOK accepts git, whose arguments hold the test's temp root.
type gitOK struct{ *host.Fake }

func (g gitOK) Run(ctx context.Context, c host.Cmd) (host.Result, error) {
	if c.Name == "git" {
		g.Fake.Calls = append(g.Fake.Calls, c.String())
		return host.Result{}, nil
	}
	return g.Fake.Run(ctx, c)
}
