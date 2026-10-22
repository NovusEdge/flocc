package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

func TestCreateRootIsARootPhaseWithoutAUser(t *testing.T) {
	if _, ok := cardinalPhase([]string{"--phase", "create-root"}); !ok {
		t.Fatal("create-root is not routed to runCardinal")
	}
	if checkCardinalPhase("create-root", 0) != nil || checkCardinalPhase("create-root", 1000) == nil {
		t.Fatal("create-root is not a root phase")
	}
	// Refused before anything runs (not root, or no SUDO_UID), and never as a bad --user.
	var errb strings.Builder
	if code := runCardinal([]string{"--phase", "create-root"}, nil, io.Discard, &errb); code != 2 || strings.Contains(errb.String(), "user name") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

// endedRunner fails every command once ctx has ended, as host.Exec does.
type endedRunner struct{}

func (endedRunner) Run(ctx context.Context, _ host.Cmd) (host.Result, error) {
	return host.Result{}, ctx.Err()
}
func (endedRunner) Interactive(ctx context.Context, _ host.Cmd) error { return ctx.Err() }

func TestCreateRootReportAfterInterruptHoldsNoSteps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env := &steps.Env{Run: endedRunner{}, Sleep: func(time.Duration) {}, Answers: &answers.Answers{},
		UI: &ui.Scripted{A: &answers.Answers{}, Out: io.Discard}}
	r, err := createRootReport(ctx, env)
	if !errors.Is(err, context.Canceled) || !strings.HasPrefix(err.Error(), "interrupted: ") || len(r.Steps) != 0 {
		t.Fatalf("report %+v, err %v", r, err)
	}
}

func TestCardinalPhaseIsFoundBeforeParsing(t *testing.T) {
	for args, want := range map[string]bool{
		"--phase cardinal-status --quick": true,
		"--phase=cardinal-root --user c":  true,
		"-phase cardinal":                 true,
		"--phase service --answers -":     false,
		"--answers -":                     false,
		"--phase cardinal-bogus":          false,
	} {
		if _, ok := cardinalPhase(strings.Fields(args)); ok != want {
			t.Errorf("%q: %v", args, ok)
		}
	}
}

func TestCheckCardinalPhase(t *testing.T) {
	for _, c := range []struct {
		phase string
		euid  int
		ok    bool
	}{
		{"cardinal-root", 0, true}, {"cardinal-root", 1000, false},
		{"cardinal-host", 0, true}, {"cardinal-purge", 1002, false},
		{"cardinal", 1002, true}, {"cardinal", 0, false},
		{"cardinal-status", 1002, true}, {"cardinal-prestart", 0, false},
	} {
		if err := checkCardinalPhase(c.phase, c.euid); (err == nil) != c.ok {
			t.Errorf("%s as %d: %v", c.phase, c.euid, err)
		}
	}
}

func TestRunCardinalRefusesABadUserBeforeTheStage(t *testing.T) {
	var errb strings.Builder
	if code := runCardinal([]string{"--phase", "cardinal-root", "--user", "-rf"}, nil, io.Discard, &errb); code != 2 || !strings.Contains(errb.String(), "not a user name") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}
