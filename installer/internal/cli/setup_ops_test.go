package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/rootpass"
	"github.com/NovusEdge/flocc/installer/internal/steps"
)

// flocc-setup's rootPhase and steps.ValidRootStage accept exactly this line; a drift in the phase
// name or the stage prefix on either side fails only on the machine, after the sudo password.
func TestRemoveOldRunsTheRemovePhaseFromTheRemoveStage(t *testing.T) {
	f := host.NewFake()
	d, _, _ := testDeps(t)
	d.run, d.tty = f, true
	d.repoRoot = func() (string, error) { return "/repo", nil }
	m := "box"
	st := "/tmp/flocc-remove.Ab12Cd"
	f.On("ssh -O check "+m, 0, "")
	f.On(rootpass.SSH(nil, false, m, "mktemp -d /tmp/flocc-remove.XXXXXX").String(), 0, st+"\n")
	f.OnName("ssh", 0, "x86_64\n").OnName("go", 0, "").OnName("scp", 0, "")
	if err := removeOld(context.Background(), d, nil, m, []string{earlierUser}); err != nil {
		t.Fatalf("%v; %v", err, f.Calls)
	}
	want := "interactive: " + rootpass.SSH(nil, true, m,
		"chmod 755 "+st+"/flocc-setup && sudo "+st+"/flocc-setup --phase old-user-remove --user "+earlierUser+" && rm -rf "+st).String()
	if !f.Ran(want) {
		t.Fatalf("want %q in %v", want, f.Calls)
	}
}

func TestRemoveOldNeedsATerminalBeforeAnySSH(t *testing.T) {
	f := host.NewFake()
	d, _, _ := testDeps(t)
	d.run, d.tty = f, false
	d.repoRoot = func() (string, error) { return "", errors.New("unused") }
	err := removeOld(context.Background(), d, nil, "box", []string{earlierUser})
	if err == nil || !strings.Contains(err.Error(), "run this in a terminal") || len(f.Calls) != 0 {
		t.Fatalf("err %v calls %v", err, f.Calls)
	}
}

var earlierUser = steps.OldUsers[0]
