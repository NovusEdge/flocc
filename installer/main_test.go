package main

import (
	"context"
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
	f := host.NewFake().
		On("chown -R root:linear-agents /tmp/la-setup", 0, "").
		On("chmod -R g+rX,o-rwx /tmp/la-setup", 0, "").
		On("sudo -iu linear-agents sh -c "+copyScript("e2e.toml"), 0, "")
	if err := handoff(context.Background(), f, "/tmp/la-setup", "/tmp/la-setup/e2e.toml", nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "/home/linear-agents") && !strings.HasPrefix(c, "interactive: sudo -iu linear-agents") {
			t.Errorf("root touched the service home: %s", c)
		}
	}
	if err := handoff(context.Background(), host.NewFake(), "/tmp/la-setup", "/x/a;rm -rf ~.toml", nil); err == nil {
		t.Error("a shell metacharacter in the answers file name must be refused")
	}
}
