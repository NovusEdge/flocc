package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

func TestCheckPhase(t *testing.T) {
	if checkPhase("root", 0) != nil || checkPhase("service", 1001) != nil {
		t.Fatal("valid phases refused")
	}
	for _, c := range []struct {
		phase string
		euid  int
	}{{"root", 1000}, {"service", 0}, {"other", 0}} {
		if checkPhase(c.phase, c.euid) == nil {
			t.Errorf("%s as %d: want refusal", c.phase, c.euid)
		}
	}
}

func TestHandoffArgs(t *testing.T) {
	got := handoffArgs("/tmp/flocc-setup/e2e.toml", true, "/home/flocc/.cache/flocc-setup")
	want := []string{"--phase", "service", "--answers", "/home/flocc/.cache/flocc-setup/e2e.toml", "--skip-interactive"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	if got := handoffArgs("", false, "/d"); !slices.Equal(got, []string{"--phase", "service"}) {
		t.Fatalf("got %v", got)
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
		"setup.log", "other.toml", "-bad.tar", "a b.tar")
	got, err := stageNames(stage, "/x/e2e.toml")
	want := []string{"e2e.toml", "flocc-setup", "flocc.bundle", "identity.json", "mp-X.tar", "plugins.json"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := stageNames(fillStage(t, "plugins.json"), ""); err == nil {
		t.Fatal("a stage without flocc-setup must be refused")
	}
}

// sudo -i passes the script through the login shell, which expands $ before the inner sh runs.
func TestCopyScriptHasNoExpansionAndCopiesTheStage(t *testing.T) {
	stage := fillStage(t, "flocc-setup", "flocc.bundle", "plugins.json", "mp-X.tar", "e2e.toml")
	names, err := stageNames(stage, "e2e.toml")
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
	if got != "/home/flocc/.config/flocc/answers.toml" || strings.HasPrefix(got, serviceStage) {
		t.Fatal(got)
	}
}

func TestHandoffRemovesAStagedAnswersToml(t *testing.T) {
	stage := fillStage(t, "flocc-setup", "answers.toml")
	f := host.NewFake().
		On("chown -R root:flocc "+stage, 0, "").
		On("chmod -R g+rX,o-rwx "+stage, 0, "").
		On("sudo -iu flocc sh -c "+copyScript(rootStage, []string{"answers.toml", "flocc-setup"}), 0, "")
	if err := handoff(context.Background(), f, stage, "answers.toml", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stage, "answers.toml")); err == nil {
		t.Fatal("a staged answers.toml, which carries tokens, was kept")
	}
}

func TestHandoffRemovesTheStagedAnswersOnlyOnSuccess(t *testing.T) {
	stage := fillStage(t, "flocc-setup", "e2e.toml")
	f := host.NewFake().
		On("chown -R root:flocc "+stage, 0, "").
		On("chmod -R g+rX,o-rwx "+stage, 0, "")
	f.Responses["sudo -iu flocc sh -c "+copyScript(rootStage, []string{"e2e.toml", "flocc-setup"})] = host.Result{Code: 1}
	if err := handoff(context.Background(), f, stage, "e2e.toml", nil); err == nil {
		t.Fatal("want the copy failure")
	}
	if _, err := os.Stat(filepath.Join(stage, "e2e.toml")); err != nil {
		t.Fatal("answers removed after a failed handoff")
	}
	f.On("sudo -iu flocc sh -c "+copyScript(rootStage, []string{"e2e.toml", "flocc-setup"}), 0, "")
	if err := handoff(context.Background(), f, stage, "e2e.toml", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stage, "e2e.toml")); err == nil {
		t.Fatal("answers kept after a successful handoff")
	}
}

func TestHandoffNeverWritesIntoTheServiceHomeAsRoot(t *testing.T) {
	stage := fillStage(t, "flocc-setup", "e2e.toml")
	f := host.NewFake().
		On("chown -R root:flocc "+stage, 0, "").
		On("chmod -R g+rX,o-rwx "+stage, 0, "").
		On("sudo -iu flocc sh -c "+copyScript(rootStage, []string{"e2e.toml", "flocc-setup"}), 0, "")
	if err := handoff(context.Background(), f, stage, filepath.Join(stage, "e2e.toml"), nil); err != nil {
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
	for _, name := range []string{"a;rm -rf ~.toml", "-t.toml", "..", "missing.toml"} {
		if err := handoff(context.Background(), host.NewFake(), stage, filepath.Join(stage, name), nil); err == nil {
			t.Errorf("answers file %q must be refused", name)
		}
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
