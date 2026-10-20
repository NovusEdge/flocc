package steps

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

const doctorCmd = "/home/flocc/.local/bin/flocc doctor"

type declineUI struct{ *ui.Scripted }

func (declineUI) Confirm(string, bool) (bool, error) { return false, nil }

func TestRunLoopModeStartsTmuxBeforeDoctor(t *testing.T) {
	f := host.NewFake().
		On("tmux -L flocc new-session -d -s flocc-loop sleep infinity", 0, "").
		On(doctorCmd, 0, "ok    tmux: server on socket flocc\n").
		On("tmux -L flocc respawn-pane -k -t =flocc-loop: "+loopCommand("/home/flocc"), 0, "")
	e := testEnv(t, f)
	e.Mode = Loop
	if err := (Run{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("tmux -L flocc respawn-pane -k -t =flocc-loop: " + loopCommand("/home/flocc")) {
		t.Fatal("never started ticking")
	}
	start, doc := -1, -1
	for i, c := range f.Calls {
		if strings.Contains(c, "new-session") {
			start = i
		}
		if c == doctorCmd {
			doc = i
		}
	}
	if start < 0 || doc < start {
		t.Fatalf("tmux must start before doctor: %v", f.Calls)
	}
}

func TestRunStopsOnDoctorFailLines(t *testing.T) {
	f := host.NewFake().
		On("tmux -L flocc new-session -d -s flocc-loop sleep infinity", 0, "").
		On(doctorCmd, 1, "FAIL  linear: 401\nFAIL  claude login: none\n")
	e := testEnv(t, f)
	e.Mode = Loop
	err := (Run{}).Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "2 FAIL lines") {
		t.Fatalf("err=%v", err)
	}
	if f.Ran("tmux -L flocc respawn-pane -k -t =flocc-loop: " + loopCommand("/home/flocc")) {
		t.Fatal("started ticking despite FAIL lines")
	}
}

func TestRunDeclineIsErrStopped(t *testing.T) {
	f := host.NewFake().
		On("tmux -L flocc new-session -d -s flocc-loop sleep infinity", 0, "").
		On(doctorCmd, 0, "ok    tmux: x\n")
	e := testEnv(t, f)
	e.Mode = Loop
	e.UI = declineUI{&ui.Scripted{A: &answers.Answers{}, Out: io.Discard}}
	if err := (Run{}).Apply(context.Background(), e); !errors.Is(err, ErrStopped) {
		t.Fatalf("err=%v", err)
	}
	if f.Ran("tmux -L flocc respawn-pane -k -t =flocc-loop: " + loopCommand("/home/flocc")) {
		t.Fatal("started ticking after decline")
	}
}

func TestRunRefusesWhenDoctorDoesNotRun(t *testing.T) {
	f := host.NewFake().
		On("tmux -L flocc new-session -d -s flocc-loop sleep infinity", 0, "")
	f.Responses[doctorCmd] = host.Result{Code: 2, Stderr: "Traceback (most recent call last):\n"}
	e := testEnv(t, f)
	e.Mode = Loop
	err := (Run{}).Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "did not run cleanly") {
		t.Fatalf("err=%v", err)
	}
	if f.Ran("tmux -L flocc respawn-pane -k -t =flocc-loop: " + loopCommand("/home/flocc")) {
		t.Fatal("started ticking on a broken doctor")
	}
}

func TestLoopCommandRestartsTheRunAndStaysCheckable(t *testing.T) {
	c := loopCommand("/home/flocc")
	if !strings.Contains(c, "while :;") || !strings.Contains(c, "flocc run") {
		t.Fatal(c)
	}
	if out, err := exec.Command("sh", "-n", "-c", c).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestRunLoopCheckLooksAtThePaneCommand(t *testing.T) {
	f := host.NewFake().On("tmux -N -L flocc list-panes -t =flocc-loop -F #{pane_start_command}", 0, "sleep infinity\n")
	e := testEnv(t, f)
	e.Mode = Loop
	if done, _, _ := (Run{}).Check(context.Background(), e); done {
		t.Fatal("an idle flocc-loop is not ticking")
	}
	f.On("tmux -N -L flocc list-panes -t =flocc-loop -F #{pane_start_command}", 0, loopCommand("/home/flocc")+"\n")
	if done, _, _ := (Run{}).Check(context.Background(), e); !done {
		t.Fatal("want done")
	}
}
