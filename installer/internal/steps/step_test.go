package steps

import (
	"bytes"
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

func capture(color bool) (*ui.Scripted, *bytes.Buffer) {
	var b bytes.Buffer
	return &ui.Scripted{A: &answers.Answers{}, Out: &b, Color: color}, &b
}

func TestRunAllNumbersOnlyTheStepsThatRun(t *testing.T) {
	a, b, c := &fakeStep{name: "a", done: true}, &fakeStep{name: "b"}, &fakeStep{name: "c"}
	u, out := capture(false)
	if err := RunAll(context.Background(), newEnv(u), []Step{a, b, c}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"→ 1/2 b ", "✓ 1/2 b ", "→ 2/2 c ", "✓ 2/2 c "} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "/3") || strings.Contains(got, "→ 1/2 a") {
		t.Errorf("a done step was counted:\n%s", got)
	}
}

func TestRunAllCountsARedoneStep(t *testing.T) {
	a, b := &fakeStep{name: "a", done: true}, &fakeStep{name: "b", done: true}
	s, out := capture(false)
	u := redoUI{Scripted: s, redo: []string{"b"}}
	if err := RunAll(context.Background(), newEnv(u), []Step{a, b}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "→ 1/1 b ") {
		t.Fatalf("%s", out.String())
	}
}

func TestRunAllShowsTheNewCheckDetailAndNoEscapesWithoutATerminal(t *testing.T) {
	b := &fakeStep{name: "b"}
	u, out := capture(false)
	if err := RunAll(context.Background(), newEnv(u), []Step{b}); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\r\x1b") {
		t.Fatalf("%q", out.String())
	}
	if !strings.Contains(out.String(), "  ✓ 1/1 b              detail b (0s)\n") {
		t.Fatalf("%q", out.String())
	}
}

func TestRunAllAnimatesOnlyWhereTheOutputIsATerminal(t *testing.T) {
	u, out := capture(true)
	if err := RunAll(context.Background(), newEnv(u), []Step{&fakeStep{name: "b"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\r\x1b[K") || !strings.HasSuffix(out.String(), "\x1b[32m✓\x1b[0m 1/1 b              detail b (0s)\n") {
		t.Fatalf("%q", out.String())
	}
}

func TestRunAllClearsTheLiveLineWhenAStepFails(t *testing.T) {
	u, out := capture(true)
	err := RunAll(context.Background(), newEnv(u), []Step{&fakeStep{name: "a", applyErr: errors.New("boom")}})
	if err == nil || err.Error() != "a: boom" {
		t.Fatalf("err=%v", err)
	}
	if !strings.HasSuffix(out.String(), "\r\x1b[K") || strings.Contains(out.String(), "✓ 1/1") {
		t.Fatalf("%q", out.String())
	}
}

type notingStep struct{ fakeStep }

func (s *notingStep) Apply(ctx context.Context, e *Env) error {
	e.UI.Note("flocc doctor", "FAIL nothing")
	return s.fakeStep.Apply(ctx, e)
}

func TestRunAllNoteDuringAnAnimatedStepDoesNotShareALineWithTheFrame(t *testing.T) {
	u, out := capture(true)
	if err := RunAll(context.Background(), newEnv(u), []Step{&notingStep{fakeStep{name: "b"}}}); err != nil {
		t.Fatal(err)
	}
	for _, seg := range strings.Split(out.String(), "\r\x1b[K") {
		if strings.Contains(seg, "flocc doctor") && (strings.Contains(seg, "1/1") || strings.Contains(seg, "0:00")) {
			t.Fatalf("a note shares a line with a frame: %q", seg)
		}
	}
	if !strings.Contains(out.String(), "\r\x1b[K\nflocc doctor\n  FAIL nothing\n\r\x1b[K") {
		t.Fatalf("%q", out.String())
	}
}

func TestRunAllShowsAPendingStepWithADotAndGoesOn(t *testing.T) {
	a := &fakeStep{name: "a", applyErr: ErrPending}
	u, out := capture(false)
	if err := RunAll(context.Background(), newEnv(u), []Step{a}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "  • 1/1 a              detail a (0s)\n") {
		t.Fatalf("%q", out.String())
	}
}

func TestDetectMode(t *testing.T) {
	if DetectMode("systemd\n") != Systemd || DetectMode("daytona\n") != Loop || DetectMode("") != Loop {
		t.Fatal("DetectMode")
	}
}
