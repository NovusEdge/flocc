package steps

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NovusEdge/linear-agents/deploy"
	"github.com/NovusEdge/linear-agents/installer/internal/answers"
	"github.com/NovusEdge/linear-agents/installer/internal/host"
	"github.com/NovusEdge/linear-agents/installer/internal/ui"
)

var update = flag.Bool("update", false, "rewrite golden files")

func goldenAnswers() *answers.Answers {
	return &answers.Answers{TeamKey: "NF", DigestIssue: "NF-186", NtfyTopic: "la-test",
		Repos: []answers.Repo{{Name: "nimblefox-template", URL: "https://github.com/nimble-fox-ai/nimblefox-template",
			BaseBranch: "main", Check: "mise exec -- just check", Draft: false}}}
}

func TestRenderConfigMatchesGolden(t *testing.T) {
	example, _ := deploy.Files.ReadFile("config.example.toml")
	got, err := RenderConfig(example, goldenAnswers())
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

func TestRenderConfigSetsSkipUnmappedOnlyWhenReposAreExcluded(t *testing.T) {
	example, _ := deploy.Files.ReadFile("config.example.toml")
	plain, err := RenderConfig(example, goldenAnswers())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "skip_unmapped") {
		t.Errorf("skip_unmapped rendered with nothing excluded:\n%s", plain)
	}
	a := goldenAnswers()
	a.ExcludedRepos = []string{"https://github.com/nimble-fox-ai/other"}
	got, err := RenderConfig(example, a)
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

func TestDoctorRunsWithTheServicePath(t *testing.T) {
	var got []string
	r := envRecorder{Fake: host.NewFake(), env: &got}
	e := testEnv(t, r.Fake)
	e.Run = r
	doctor(context.Background(), e)
	if len(got) != 1 || !strings.HasPrefix(got[0], "PATH=/home/linear-agents/.local/bin:") {
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
	bin := "/home/linear-agents/.local/bin/linear-agents doctor"
	f := host.NewFake().On(bin, 1, "ok    config: /x\nFAIL  repo r: /home/linear-agents/src/r is not a git clone\nFAIL  claude login: none\n")
	done, detail, _ := (Config{}).Check(context.Background(), testEnv(t, f))
	if done || detail != "repo r: /home/linear-agents/src/r is not a git clone" {
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
