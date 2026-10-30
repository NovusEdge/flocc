package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

func TestCheckPhase(t *testing.T) {
	if checkPhase("root", 0) != nil || checkPhase("service", 1001) != nil || checkPhase("login", 1001) != nil {
		t.Fatal("valid phases refused")
	}
	for _, c := range []struct {
		phase string
		euid  int
	}{{"root", 1000}, {"service", 0}, {"login", 0}, {"other", 0}} {
		if checkPhase(c.phase, c.euid) == nil {
			t.Errorf("%s as %d: want refusal", c.phase, c.euid)
		}
	}
}

func TestHandoffArgs(t *testing.T) {
	if got := handoffArgs("service", true); !slices.Equal(got, []string{"--phase", "service", "--answers", "-"}) {
		t.Fatalf("got %v", got)
	}
	if got := handoffArgs("service", false); !slices.Equal(got, []string{"--phase", "service"}) {
		t.Fatalf("got %v", got)
	}
}

func TestAgentPhaseRunsAsFloccAgent(t *testing.T) {
	if err := checkPhase("agent", 0); err == nil {
		t.Fatal("agent phase allowed as root")
	}
	if err := checkPhase("agent", 1001); err != nil {
		t.Fatal(err)
	}
}

func TestRootHandsOffToTheAgentThenTheService(t *testing.T) {
	f := host.NewFake().OnName("chown", 0, "").OnName("chmod", 0, "").OnName("sudo", 0, "")
	stage := fillStage(t, "flocc-setup")
	for _, h := range []struct{ user, phase string }{{"flocc-agent", "agent"}, {"flocc", "service"}} {
		if err := handoff(context.Background(), f, stage, h.user, handoffArgs(h.phase, true), []byte("doc")); err != nil {
			t.Fatal(err)
		}
	}
	var order []string
	for _, c := range f.Interactives {
		order = append(order, c.String())
	}
	want := []string{
		"sudo -iu flocc-agent /home/flocc-agent/.cache/flocc-setup/flocc-setup --phase agent --answers -",
		"sudo -iu flocc /home/flocc/.cache/flocc-setup/flocc-setup --phase service --answers -",
	}
	if !slices.Equal(order, want) {
		t.Fatalf("%v", order)
	}
}

func fillStage(t *testing.T, names ...string) string {
	t.Helper()
	stage := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(stage, n), []byte(n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return stage
}

func TestStageNamesKeepsOnlyKnownSafeFiles(t *testing.T) {
	stage := fillStage(t, "flocc-setup", "flocc.bundle", "plugins.json", "identity.json", "mp-X.tar", "e2e.toml",
		"setup.log", "-bad.tar", "a b.tar")
	got, err := stageNames(stage)
	want := []string{"flocc-setup", "flocc.bundle", "identity.json", "mp-X.tar", "plugins.json"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := stageNames(fillStage(t, "plugins.json")); err == nil {
		t.Fatal("a stage without flocc-setup must be refused")
	}
}

// sudo -i passes the script through the login shell, which expands $ before the inner sh runs.
func TestCopyScriptHasNoExpansionAndCopiesTheStage(t *testing.T) {
	stage := fillStage(t, "flocc-setup", "flocc.bundle", "plugins.json", "mp-X.tar", "e2e.toml")
	names, err := stageNames(stage)
	if err != nil {
		t.Fatal(err)
	}
	script := copyScript(stage, names)
	if strings.ContainsAny(script, "$`*?[\"'\\") {
		t.Fatalf("shell expansion in %q", script)
	}
	home := t.TempDir()
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, n := range names {
		if b, err := os.ReadFile(filepath.Join(home, ".cache/flocc-setup", n)); err != nil || string(b) != n {
			t.Errorf("%s: %q %v", n, b, err)
		}
	}
}

func TestSavedAnswersLiveOutsideTheStage(t *testing.T) {
	got := savedAnswersPath("/home/flocc")
	if got != "/home/flocc/.config/flocc/answers.toml" || strings.HasPrefix(got, stageFor(steps.ServiceUser)) {
		t.Fatal(got)
	}
}

func TestRemoveStagedAnswersDeletesLeftoverTomlOnly(t *testing.T) {
	stage := fillStage(t, "flocc-setup", "e2e.toml", "answers.toml", "setup.log")
	if err := removeStagedAnswers(stage); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"e2e.toml", "answers.toml"} {
		if _, err := os.Stat(filepath.Join(stage, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: %v", gone, err)
		}
	}
	for _, kept := range []string{"flocc-setup", "setup.log"} {
		if _, err := os.Stat(filepath.Join(stage, kept)); err != nil {
			t.Errorf("%s: %v", kept, err)
		}
	}
	if err := removeStagedAnswers(stage); err != nil {
		t.Fatalf("an empty stage is not an error: %v", err)
	}
}

func handoffFake(stage string) *host.Fake {
	return host.NewFake().
		On("chown -R root:flocc "+stage, 0, "").
		On("chmod -R g+rX,o-rwx "+stage, 0, "").
		On("sudo -iu flocc sh -c "+copyScript(rootStage, []string{"flocc-setup", "flocc.bundle"}), 0, "")
}

func TestHandoffNeverWritesIntoTheServiceHomeAsRoot(t *testing.T) {
	stage := fillStage(t, "flocc-setup", "flocc.bundle")
	f := handoffFake(stage)
	if err := handoff(context.Background(), f, stage, steps.ServiceUser, handoffArgs("service", false), nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if c == "chown -R root:flocc "+stage || c == "chmod -R g+rX,o-rwx "+stage {
			continue
		}
		if !strings.HasPrefix(c, "sudo -iu flocc") && !strings.HasPrefix(c, "interactive: sudo -iu flocc") {
			t.Errorf("root ran something besides the stage chown/chmod and the service user's own commands: %s", c)
		}
	}
}

func TestHandoffPipesTheAnswersToTheServicePhase(t *testing.T) {
	stage := fillStage(t, "flocc-setup", "flocc.bundle")
	f := handoffFake(stage)
	doc := "[secrets]\nlinear_key = \"lin_api_s3cret\"\n"
	if err := handoff(context.Background(), f, stage, steps.ServiceUser, handoffArgs("service", true), []byte(doc)); err != nil {
		t.Fatal(err)
	}
	call := "sudo -iu flocc /home/flocc/.cache/flocc-setup/flocc-setup --phase service --answers -"
	if !f.Ran("interactive: "+call) || f.Stdins[call] != doc {
		t.Fatalf("calls %v stdin %q", f.Calls, f.Stdins[call])
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "s3cret") {
			t.Errorf("secret in argv: %s", c)
		}
	}
}

func TestReadAnswersKeepsTheBytesAndLeavesSecretsOutOfErrors(t *testing.T) {
	if _, _, err := readAnswers(strings.NewReader("[secrets]\nlinear_key = \"lin_api_s3cret\n")); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("%v", err)
	}
	doc := "[secrets]\nlinear_key = \"lin_api_s3cret\"\n"
	raw, a, err := readAnswers(strings.NewReader(doc))
	if err != nil || string(raw) != doc || !slices.Equal(a.Secrets.Values(), []string{"lin_api_s3cret"}) {
		t.Fatalf("%q %+v %v", raw, a, err)
	}
}

// recorder succeeds at every command, so a step runs to its end, and keeps each command whole,
// environment included.
type recorder struct{ cmds []host.Cmd }

func (r *recorder) Run(_ context.Context, c host.Cmd) (host.Result, error) {
	r.cmds = append(r.cmds, c)
	if strings.HasSuffix(c.Name, "/claude") && slices.Equal(c.Args, []string{"auth", "status"}) {
		return host.Result{Stdout: `{"loggedIn":false}`, Code: 1}, nil
	}
	return host.Result{}, nil
}

func (r *recorder) Interactive(ctx context.Context, c host.Cmd) error {
	_, err := r.Run(ctx, c)
	return err
}

const secretsDoc = `team_key = "NF"
digest_issue = "NF-1"

[[repos]]
name = "r"
url = "https://github.com/o/r"
base_branch = "main"

[secrets]
linear_key = "lin_api_s3cretA"
agent_token = "github_pat_s3cretB"
publish_token = "github_pat_s3cretC"
`

// Redaction is the second guard; this proves the first: no step puts a secret in argv or output,
// and the agent token reaches a command only as GH_TOKEN in the environment.
func TestServiceStepsKeepSecretsOutOfArgvAndOutput(t *testing.T) {
	// Scripted answers skip repo discovery; --interactive's discovery runs gh with the agent token.
	for _, skip := range []bool{true, false} {
		_, in, err := readAnswers(strings.NewReader(secretsDoc))
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		rec, root := &recorder{}, t.TempDir()
		e := &steps.Env{Run: rec, UI: &ui.Scripted{A: in, Out: &out}, Root: root, Home: "/home/flocc", Answers: in,
			AnswersPath: filepath.Join(root, "answers.toml"), SkipInteractive: skip, Sleep: func(time.Duration) {}}
		for _, s := range []steps.Step{steps.Secrets{}, steps.Config{}, steps.ClaudeLogin{}} {
			if err := s.Apply(context.Background(), e); err != nil && !errors.Is(err, steps.ErrPending) {
				t.Fatalf("skip=%v %s: %v", skip, s.Name(), err)
			}
		}
		var sawToken bool
		for _, s := range in.Secrets.Values() {
			for _, c := range rec.cmds {
				if strings.Contains(c.String(), s) {
					t.Errorf("skip=%v: secret in argv: %s", skip, c)
				}
				for _, env := range c.Env {
					switch {
					case env == "GH_TOKEN="+in.Secrets.AgentToken:
						sawToken = true
					case strings.Contains(env, s):
						t.Errorf("skip=%v: secret in the environment: %s", skip, c.Name)
					}
				}
			}
			if strings.Contains(out.String(), s) {
				t.Errorf("skip=%v: secret on stdout: %s", skip, out.String())
			}
		}
		if sawToken == skip {
			t.Errorf("skip=%v: GH_TOKEN seen=%v; discovery should run exactly when answers are not scripted", skip, sawToken)
		}
		key := filepath.Join(root, "/home/flocc/.config/flocc/linear.key")
		if b, err := os.ReadFile(key); err != nil || string(b) != "lin_api_s3cretA\n" {
			t.Fatalf("%q %v", b, err)
		}
		if st, _ := os.Stat(key); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %o", st.Mode().Perm())
		}
	}
}

const loginFailLine = "FAIL  claude login: none; on the desktop, run flocc login -H <this host>\n"

func finishEnv(t *testing.T, doctorOut string) *steps.Env {
	t.Helper()
	f := host.NewFake().
		On("/home/flocc/.local/bin/flocc doctor", 1, doctorOut).
		On("/home/flocc/.local/bin/flocc --version", 0, "flocc 0.4.0\n").
		On("/home/flocc/.local/bin/claude auth status", 1, `{"loggedIn":false}`).
		On("/home/flocc/.local/bin/flocc status --json", 0, `{"tick_lock":"/x/ledger.lock","claims":[]}`)
	return &steps.Env{Run: f, UI: &ui.Scripted{A: &answers.Answers{}, Out: io.Discard}, Root: t.TempDir(), Home: "/home/flocc",
		Mode: steps.Systemd, Answers: &answers.Answers{}, Sleep: func(time.Duration) {}}
}

// The desktop exits with the host's status, so these three decide what flocc ship reports.
func TestFinishExitCodesAndSummaryLine(t *testing.T) {
	for _, c := range []struct {
		name    string
		err     error
		doctor  string
		code    int
		summary bool
	}{
		{"only the Claude login is pending", nil, loginFailLine, 0, true},
		{"doctor reports another FAIL", &steps.DoctorFailed{Fails: []string{"FAIL  linear: 401"}}, loginFailLine + "FAIL  linear: 401\n", 1, true},
		{"the owner declined to go on", steps.ErrStopped, "", 0, false},
		{"a step failed", errors.New("Claude: boom"), "", 1, false},
	} {
		var out, errOut bytes.Buffer
		got := finish(context.Background(), finishEnv(t, c.doctor), c.err, true, &out, &errOut, "/tmp/setup.log")
		var line string
		for _, l := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(l, steps.SummaryMarker) {
				line = l
			}
		}
		if got != c.code || (line != "") != c.summary {
			t.Errorf("%s: exit %d, summary line %q\nstdout %q\nstderr %q", c.name, got, line, out.String(), errOut.String())
			continue
		}
		if line != "" {
			if !strings.Contains(out.String(), "\n"+steps.SummaryMarker) {
				t.Errorf("%s: the summary line does not start a line of its own: %q", c.name, out.String())
			}
			if s, ok := steps.ParseSummaryLine(line); !ok || !s.LoginPending || s.Failing() != (c.code == 1) {
				t.Errorf("%s: %+v %v", c.name, s, ok)
			}
		}
	}
}

// The desktop prints the line to its own stdout, past the host's redacting writers, so finish
// redacts it: a doctor FAIL line and the version both carry raw host output. The second secret
// is only on disk, as the interactive form leaves it.
func TestFinishRedactsSecretsFromTheSummaryLine(t *testing.T) {
	const scripted, onDisk = "github_pat_s3cretB", "lin_api_s3cretA"
	f := host.NewFake().
		On("/home/flocc/.local/bin/flocc doctor", 1, "FAIL  github: bad token "+scripted+"\nFAIL  linear: 401 "+onDisk+"\n").
		On("/home/flocc/.local/bin/flocc --version", 0, "flocc 0.4.0 "+scripted+"\n").
		On("/home/flocc/.local/bin/claude auth status", 0, `{"loggedIn":true}`).
		On("/home/flocc/.local/bin/flocc status --json", 0, `{"tick_lock":"/x/ledger.lock","claims":[]}`)
	root := t.TempDir()
	key := filepath.Join(root, "home/flocc/.config/flocc/linear.key")
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte(onDisk+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &answers.Answers{Secrets: &answers.Secrets{AgentToken: scripted}}
	env := &steps.Env{Run: f, UI: &ui.Scripted{A: a, Out: io.Discard}, Root: root, Home: "/home/flocc",
		Mode: steps.Systemd, Answers: a, Sleep: func(time.Duration) {}}
	var out, errOut bytes.Buffer
	err := &steps.DoctorFailed{Fails: []string{"FAIL  github: bad token " + scripted}}
	if code := finish(context.Background(), env, err, true, &out, &errOut, "/tmp/setup.log"); code != 1 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "\n"+steps.SummaryMarker) {
		t.Fatalf("no summary line: %q", out.String())
	}
	for _, s := range []string{scripted, onDisk} {
		if strings.Contains(out.String(), s) {
			t.Errorf("secret %q in the summary: %q", s, out.String())
		}
	}
	if !strings.Contains(out.String(), "***") {
		t.Errorf("nothing was redacted; the planted secrets never reached the summary: %q", out.String())
	}
}

func TestRunRefusesAnAnswersFile(t *testing.T) {
	if code := run([]string{"--answers", "a.toml"}); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestCheckStage(t *testing.T) {
	if checkStage(rootStage) != nil {
		t.Fatal("the root stage was refused")
	}
	for _, d := range []string{"/usr/local/bin", "/tmp/flocc-setup/x", ""} {
		if checkStage(d) == nil {
			t.Errorf("%q: want refusal", d)
		}
	}
}

type fakeStep struct{ name string }

func (f fakeStep) Name() string { return f.name }
func (f fakeStep) Check(context.Context, *steps.Env) (bool, string, error) {
	return false, "to do", nil
}
func (f fakeStep) Apply(context.Context, *steps.Env) error { return nil }

func TestEventsRunDrawsTheStepsAndSendsTheSummaryAsData(t *testing.T) {
	var out, errOut bytes.Buffer
	env := finishEnv(t, loginFailLine)
	env.UI = &ui.Events{Scripted: ui.Scripted{A: env.Answers, Out: &out}, W: &out}
	if err := steps.RunAll(context.Background(), env, []steps.Step{fakeStep{"One"}, fakeStep{"Two"}}); err == nil {
		t.Fatal("the fake step's check never passes; want the applied-but-still-failing error")
	}
	if code := finish(context.Background(), env, nil, true, &out, &errOut, "/tmp/setup.log"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), steps.SummaryMarker) {
		t.Fatalf("escape or marker line in %q", out.String())
	}
	rec := &ui.Recorder{}
	r := ui.Replay(ui.New(rec), "", "")
	r.Write(out.Bytes())
	r.Close()
	var names []string
	for _, row := range rec.Rows() {
		names = append(names, row.Name)
	}
	if !slices.Equal(names, []string{"One"}) {
		t.Errorf("rows %q", names)
	}
	data, ok := r.Data("summary")
	if !ok {
		t.Fatalf("no summary data in %q", out.String())
	}
	if s, ok := steps.ParseSummaryLine(steps.SummaryMarker + data); !ok || !s.LoginPending {
		t.Errorf("summary %q", data)
	}
}
