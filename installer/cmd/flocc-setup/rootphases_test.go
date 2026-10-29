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
	if _, ok := rootPhase([]string{"--phase", "create-root"}); !ok {
		t.Fatal("create-root is not routed to runRootPhase")
	}
	// Refused before anything runs (not root, or no SUDO_UID), and never as a bad --user.
	var errb strings.Builder
	if code := runRootPhase([]string{"--phase", "create-root"}, io.Discard, &errb); code != 2 || strings.Contains(errb.String(), "user name") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func TestOldUserRemoveIsARootPhase(t *testing.T) {
	if p, ok := rootPhase([]string{"--phase", "old-user-remove", "--user", "cardinal"}); !ok || p != "old-user-remove" {
		t.Fatalf("got %q %v", p, ok)
	}
	if _, ok := rootPhase([]string{"--phase", "cardinal-purge"}); ok {
		t.Fatal("cardinal-purge is gone")
	}
}

func TestOldUserRemoveRefusesAUserNotOnTheList(t *testing.T) {
	var errb strings.Builder
	if code := runRootPhase([]string{"--phase", "old-user-remove", "--user", "root"}, io.Discard, &errb); code != 2 || !strings.Contains(errb.String(), "earlier flocc") {
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
