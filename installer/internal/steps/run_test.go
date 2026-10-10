package steps

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/NovusEdge/linear-agents/installer/internal/answers"
	"github.com/NovusEdge/linear-agents/installer/internal/host"
	"github.com/NovusEdge/linear-agents/installer/internal/ui"
)

const doctorCmd = "/home/linear-agents/.local/bin/linear-agents doctor"

type declineUI struct{ *ui.Scripted }

func (declineUI) Confirm(string, bool) (bool, error) { return false, nil }

func TestRunLoopModeStartsTmuxBeforeDoctor(t *testing.T) {
	f := host.NewFake().
		On("tmux -L linear-agents new-session -d -s la-loop sleep infinity", 0, "").
		On(doctorCmd, 0, "ok    tmux: server on socket linear-agents\n").
		On("tmux -L linear-agents respawn-pane -k -t =la-loop "+loopCommand("/home/linear-agents"), 0, "")
	e := testEnv(t, f)
	e.Mode = Loop
	if err := (Run{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("tmux -L linear-agents respawn-pane -k -t =la-loop " + loopCommand("/home/linear-agents")) {
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
		On("tmux -L linear-agents new-session -d -s la-loop sleep infinity", 0, "").
		On(doctorCmd, 1, "FAIL  linear: 401\nFAIL  claude login: none\n")
	e := testEnv(t, f)
	e.Mode = Loop
	err := (Run{}).Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "2 FAIL lines") {
		t.Fatalf("err=%v", err)
	}
	if f.Ran("tmux -L linear-agents respawn-pane -k -t =la-loop " + loopCommand("/home/linear-agents")) {
		t.Fatal("started ticking despite FAIL lines")
	}
}

func TestRunDeclineIsErrStopped(t *testing.T) {
	f := host.NewFake().
		On("tmux -L linear-agents new-session -d -s la-loop sleep infinity", 0, "").
		On(doctorCmd, 0, "ok    tmux: x\n")
	e := testEnv(t, f)
	e.Mode = Loop
	e.UI = declineUI{&ui.Scripted{A: &answers.Answers{}, Out: io.Discard}}
	if err := (Run{}).Apply(context.Background(), e); !errors.Is(err, ErrStopped) {
		t.Fatalf("err=%v", err)
	}
	if f.Ran("tmux -L linear-agents respawn-pane -k -t =la-loop " + loopCommand("/home/linear-agents")) {
		t.Fatal("started ticking after decline")
	}
}

func TestRunRefusesWhenDoctorDoesNotRun(t *testing.T) {
	f := host.NewFake().
		On("tmux -L linear-agents new-session -d -s la-loop sleep infinity", 0, "")
	f.Responses[doctorCmd] = host.Result{Code: 2, Stderr: "Traceback (most recent call last):\n"}
	e := testEnv(t, f)
	e.Mode = Loop
	err := (Run{}).Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "did not run cleanly") {
		t.Fatalf("err=%v", err)
	}
	if f.Ran("tmux -L linear-agents respawn-pane -k -t =la-loop " + loopCommand("/home/linear-agents")) {
		t.Fatal("started ticking on a broken doctor")
	}
}

func TestRunLoopCheckLooksAtThePaneCommand(t *testing.T) {
	f := host.NewFake().On("tmux -N -L linear-agents list-panes -t =la-loop -F #{pane_start_command}", 0, "sleep infinity\n")
	e := testEnv(t, f)
	e.Mode = Loop
	if done, _, _ := (Run{}).Check(context.Background(), e); done {
		t.Fatal("an idle la-loop is not ticking")
	}
	f.On("tmux -N -L linear-agents list-panes -t =la-loop -F #{pane_start_command}", 0, loopCommand("/home/linear-agents")+"\n")
	if done, _, _ := (Run{}).Check(context.Background(), e); !done {
		t.Fatal("want done")
	}
}
