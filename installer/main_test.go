package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
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
	got := handoffArgs("/tmp/la-setup/e2e.toml", true, "/home/linear-agents/.cache/la-setup")
	want := []string{"--phase", "service", "--answers", "/home/linear-agents/.cache/la-setup/e2e.toml", "--skip-interactive"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	if got := handoffArgs("", false, "/d"); !slices.Equal(got, []string{"--phase", "service"}) {
		t.Fatalf("got %v", got)
	}
}

func TestHandoffNeverWritesIntoTheServiceHomeAsRoot(t *testing.T) {
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "e2e.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := host.NewFake().
		On("chown -R root:linear-agents "+stage, 0, "").
		On("chmod -R g+rX,o-rwx "+stage, 0, "").
		On("sudo -iu linear-agents sh -c "+copyScript("e2e.toml"), 0, "")
	if err := handoff(context.Background(), f, stage, filepath.Join(stage, "e2e.toml"), nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if c == "chown -R root:linear-agents "+stage || c == "chmod -R g+rX,o-rwx "+stage {
			continue
		}
		if !strings.HasPrefix(c, "sudo -iu linear-agents") && !strings.HasPrefix(c, "interactive: sudo -iu linear-agents") {
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
	for _, d := range []string{"/usr/local/bin", "/tmp/la-setup/x", ""} {
		if checkStage(d) == nil {
			t.Errorf("%q: want refusal", d)
		}
	}
}
