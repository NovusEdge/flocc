package steps

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

type fakeStep struct {
	name        string
	done        bool
	applied     int
	applyErr    error
	staysUndone bool
}

func (s *fakeStep) Name() string { return s.name }
func (s *fakeStep) Check(context.Context, *Env) (bool, string, error) {
	return s.done, "detail " + s.name, nil
}
func (s *fakeStep) Apply(context.Context, *Env) error {
	s.applied++
	if s.applyErr != nil {
		return s.applyErr
	}
	s.done = !s.staysUndone
	return nil
}

type redoUI struct {
	*ui.Scripted
	redo []string
}

func (r redoUI) PickRedo([]string) ([]string, error) { return r.redo, nil }

func newEnv(u ui.UI) *Env {
	return &Env{Run: host.NewFake(), UI: u, Answers: &answers.Answers{}}
}

func scripted() ui.UI { return &ui.Scripted{A: &answers.Answers{}, Out: io.Discard} }

func TestRunAllSkipsDoneStepsAndAppliesTheRest(t *testing.T) {
	a, b := &fakeStep{name: "a", done: true}, &fakeStep{name: "b"}
	if err := RunAll(context.Background(), newEnv(scripted()), []Step{a, b}); err != nil {
		t.Fatal(err)
	}
	if a.applied != 0 || b.applied != 1 {
		t.Fatalf("applied a=%d b=%d", a.applied, b.applied)
	}
}

func TestRunAllRedoesPickedSteps(t *testing.T) {
	a := &fakeStep{name: "a", done: true}
	u := redoUI{Scripted: &ui.Scripted{A: &answers.Answers{}, Out: io.Discard}, redo: []string{"a"}}
	if err := RunAll(context.Background(), newEnv(u), []Step{a}); err != nil || a.applied != 1 {
		t.Fatalf("applied=%d err=%v", a.applied, err)
	}
}

func TestRunAllStopsAtFirstFailureAndNamesTheStep(t *testing.T) {
	a, b := &fakeStep{name: "a", applyErr: errors.New("boom")}, &fakeStep{name: "b"}
	err := RunAll(context.Background(), newEnv(scripted()), []Step{a, b})
	if err == nil || !strings.HasPrefix(err.Error(), "a: boom") || b.applied != 0 {
		t.Fatalf("err=%v b.applied=%d", err, b.applied)
	}
}

func TestRunAllFailsWhenCheckStillFailsAfterApply(t *testing.T) {
	a := &fakeStep{name: "a", staysUndone: true}
	err := RunAll(context.Background(), newEnv(scripted()), []Step{a})
	if err == nil || !strings.Contains(err.Error(), "check still fails: detail a") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunAllPassesErrStoppedThrough(t *testing.T) {
	a := &fakeStep{name: "a", applyErr: ErrStopped}
	if err := RunAll(context.Background(), newEnv(scripted()), []Step{a}); !errors.Is(err, ErrStopped) {
		t.Fatalf("err=%v", err)
	}
}

func TestRunAllRecordsPendingStepsAndGoesOn(t *testing.T) {
	a, b := &fakeStep{name: "a", applyErr: ErrPending}, &fakeStep{name: "b"}
	e := newEnv(scripted())
	if err := RunAll(context.Background(), e, []Step{a, b}); err != nil || b.applied != 1 || !slices.Equal(e.Pending, []string{"a"}) {
		t.Fatalf("err=%v b.applied=%d pending=%v", err, b.applied, e.Pending)
	}
}

func TestDetectMode(t *testing.T) {
	if DetectMode("systemd\n") != Systemd || DetectMode("daytona\n") != Loop || DetectMode("") != Loop {
		t.Fatal("DetectMode")
	}
}
