package steps

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

var (
	authFull = host.Result{Stdout: `{"loggedIn":true,"authMethod":"claude.ai"}`}
	authNone = host.Result{Stdout: `{"loggedIn":false}`, Code: 1}
)

type fakeLock struct {
	busy, takes, releases int
	held                  bool
}

func (l *fakeLock) lock(string) (func(), error) {
	if l.busy > 0 {
		l.busy--
		return nil, ErrLocked
	}
	if l.held {
		panic("tick lock taken twice")
	}
	l.takes++
	l.held = true
	return func() { l.held = false; l.releases++ }, nil
}

// loginFake answers auth status in the given order (the last repeats) and the Run step.
func loginFake(auth ...host.Result) *host.Fake {
	return systemdRun(host.NewFake(), 0, "ok    tmux: server on socket flocc\n").
		OnSeq(agentClaudeBin+" auth status", auth...).
		On(floccBin+" status --json", 0, statusJSON())
}

// noteConsent records the consent run in f.Calls instead of starting claude.
func noteConsent(f *host.Fake) func(context.Context, host.Cmd) error {
	return func(_ context.Context, c host.Cmd) error {
		f.Calls = append(f.Calls, "consent: "+c.String()+" in "+c.Dir)
		return nil
	}
}

const consentCall = "consent: " + agentClaudeBin + " remote-control in /home/flocc-agent/worktrees"

func TestLoginWithAFullLoginOnlyAnswersTheConsent(t *testing.T) {
	f := loginFake(authFull)
	lk := &fakeLock{}
	if err := Login(context.Background(), testEnv(t, f), LoginOptions{Lock: lk.lock, Consent: noteConsent(f)}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "auth login") {
			t.Fatalf("logged in again: %v", f.Calls)
		}
	}
	if !f.Ran(consentCall) || !f.Ran("systemctl --user enable --now flocc.timer") {
		t.Fatalf("calls: %v", f.Calls)
	}
	if lk.held || lk.takes != 1 || lk.releases != 1 {
		t.Fatalf("lock %+v", lk)
	}
}

func TestLoginSignsInThenAnswersTheConsent(t *testing.T) {
	f := loginFake(authNone, authFull)
	lk := &fakeLock{}
	err := Login(context.Background(), testEnv(t, f), LoginOptions{Email: "o@example.test", Lock: lk.lock, Consent: noteConsent(f)})
	if err != nil {
		t.Fatal(err)
	}
	login := slices.Index(f.Calls, "interactive: "+agentClaudeBin+" auth login --claudeai --email o@example.test")
	consent := slices.Index(f.Calls, consentCall)
	if login < 0 || consent < login || !f.Ran("systemctl --user enable --now flocc.timer") {
		t.Fatalf("calls: %v", f.Calls)
	}
}

func TestLoginStopsWhenTheSignInDidNotTake(t *testing.T) {
	f := loginFake(authNone)
	err := Login(context.Background(), testEnv(t, f), LoginOptions{Lock: (&fakeLock{}).lock, Consent: noteConsent(f)})
	if err == nil || !strings.Contains(err.Error(), "reports none") || f.Ran("systemctl --user enable --now flocc.timer") {
		t.Fatalf("err=%v calls=%v", err, f.Calls)
	}
	// Before the consent: a login that did not take must fail as that, not as a consent error.
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "consent:") {
			t.Fatalf("ran the consent without a login: %v", f.Calls)
		}
	}
}

func TestLoginWaitsForARunningTick(t *testing.T) {
	f := loginFake(authFull)
	e := testEnv(t, f)
	var slept []time.Duration
	e.Sleep = func(d time.Duration) { slept = append(slept, d) }
	lk := &fakeLock{busy: 2}
	if err := Login(context.Background(), e, LoginOptions{Lock: lk.lock, Consent: noteConsent(f)}); err != nil {
		t.Fatal(err)
	}
	if lk.takes != 1 || len(slept) != 2 {
		t.Fatalf("takes %d slept %v", lk.takes, slept)
	}
}

func TestLoginStopsWhenTheOwnerWillNotWaitForSessions(t *testing.T) {
	f := loginFake(authNone).On(floccBin+" status --json", 0, statusJSON("NF-1"))
	e := testEnv(t, f)
	e.UI = declineUI{&ui.Scripted{A: &answers.Answers{}, Out: io.Discard}}
	lk := &fakeLock{}
	if err := Login(context.Background(), e, LoginOptions{Lock: lk.lock, Consent: noteConsent(f)}); !errors.Is(err, ErrStopped) {
		t.Fatalf("err=%v", err)
	}
	if lk.held || lk.takes != 1 {
		t.Fatalf("lock %+v", lk)
	}
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "interactive:") || strings.HasPrefix(c, "consent:") {
			t.Fatalf("changed something: %v", f.Calls)
		}
	}
}

func TestLoginWaitsForSessionsWithTheLockLetGo(t *testing.T) {
	f := loginFake(authFull).OnSeq(floccBin+" status --json",
		host.Result{Stdout: statusJSON("NF-1")}, host.Result{Stdout: statusJSON("NF-1")}, host.Result{Stdout: statusJSON()})
	e := testEnv(t, f)
	var slept []time.Duration
	e.Sleep = func(d time.Duration) { slept = append(slept, d) }
	lk := &fakeLock{}
	if err := Login(context.Background(), e, LoginOptions{Lock: lk.lock, Consent: noteConsent(f)}); err != nil {
		t.Fatal(err)
	}
	if lk.takes != 2 || lk.releases != 2 || !slices.Equal(slept, []time.Duration{pollSessions}) {
		t.Fatalf("lock %+v slept %v", lk, slept)
	}
}

func TestLoginShowsWhichSessionsAreStillRunningOnEachLaterPoll(t *testing.T) {
	// Two looks per poll, before and under the lock.
	f := loginFake(authFull).OnSeq(floccBin+" status --json", host.Result{Stdout: statusJSON("NF-1")}, host.Result{Stdout: statusJSON("NF-1")},
		host.Result{Stdout: statusJSON("NF-1")}, host.Result{Stdout: statusJSON("NF-1")}, host.Result{Stdout: statusJSON()})
	e := testEnv(t, f)
	var out bytes.Buffer
	e.UI = &ui.Scripted{A: &answers.Answers{}, Out: &out}
	var slept []time.Duration
	e.Sleep = func(d time.Duration) { slept = append(slept, d) }
	if err := Login(context.Background(), e, LoginOptions{Lock: (&fakeLock{}).lock, Consent: noteConsent(f)}); err != nil {
		t.Fatal(err)
	}
	// The first poll's list is in the question; later polls say it, so the owner sees whether it shrinks.
	if !slices.Equal(slept, []time.Duration{pollSessions, pollSessions}) || strings.Count(out.String(), "NF-1 impl") != 1 {
		t.Fatalf("slept %v output %q", slept, out.String())
	}
}

func TestLoginRefusesAnUnreadableLedgerAndAHostThatNamesNoTickLock(t *testing.T) {
	for _, c := range []struct{ status, want string }{
		{`{"tick_lock":"/x/ledger.lock","claims":[],"ledger_error":"database is locked"}`, "database is locked"},
		{`{"claims":[]}`, "ship 0.4.0"},
	} {
		f := loginFake(authFull).On(floccBin+" status --json", 0, c.status)
		lk := &fakeLock{}
		err := Login(context.Background(), testEnv(t, f), LoginOptions{Lock: lk.lock, Consent: noteConsent(f)})
		if err == nil || !strings.Contains(err.Error(), c.want) || lk.takes != 0 || f.Ran(consentCall) {
			t.Errorf("%s: err=%v takes=%d calls=%v", c.status, err, lk.takes, f.Calls)
		}
	}
}

func TestLoginStopsWaitingWhenTheContextIsCancelled(t *testing.T) {
	f := loginFake(authNone).On(floccBin+" status --json", 0, statusJSON("NF-1"))
	e := testEnv(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	defer close(release)
	e.Sleep = func(time.Duration) { cancel(); <-release }
	done := make(chan error, 1)
	go func() { done <- Login(ctx, e, LoginOptions{Lock: (&fakeLock{}).lock, Consent: noteConsent(f)}) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Login kept sleeping after Ctrl-C")
	}
}

func TestFreshHostShipLeavesTheLoginPendingAndLoginStartsTheTimer(t *testing.T) {
	f := systemdRun(host.NewFake(), 1, "ok    tmux: server on socket flocc\n"+loginFailLine).
		On(agentClaudeBin+" auth status", 1, `{"loggedIn":false}`).
		On(floccBin+" --version", 0, "flocc 0.4.0\n").
		On(floccBin+" status --json", 0, statusJSON())
	e := testEnv(t, f)
	e.SkipInteractive = true
	if err := RunAll(context.Background(), e, []Step{ClaudeLogin{}, Run{}}); err != nil {
		t.Fatal(err)
	}
	s := Summarize(context.Background(), e)
	if s.Failing() || s.Login != "none" || !s.LoginPending || s.Timer ||
		f.Ran("systemctl --user enable --now flocc.timer") {
		t.Fatalf("summary %+v calls %v", s, f.Calls)
	}
	f.On(agentClaudeBin+" auth status", 0, authFull.Stdout).On(doctorCmd, 0, "ok    tmux: server on socket flocc\n")
	e.Pending = nil
	if err := Login(context.Background(), e, LoginOptions{Lock: (&fakeLock{}).lock, Consent: noteConsent(f)}); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("systemctl --user enable --now flocc.timer") {
		t.Fatalf("timer not started: %v", f.Calls)
	}
}

func TestFlockPathExcludesASecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "ledger.lock")
	unlock, err := flockPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flockPath(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second take: %v", err)
	}
	unlock()
	again, err := flockPath(path)
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func consentScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// The consent runs claude under util-linux script, which every Linux host has.
func needScript(t *testing.T) {
	t.Helper()
	if out, err := exec.Command("script", "--version").Output(); err != nil || !strings.Contains(string(out), "util-linux") {
		t.Skip("util-linux script is not installed")
	}
}

var quickWaits = consentWaits{connect: 3 * time.Second, stop: 2 * time.Second}

func TestAnswerConsentAnswersAPromptSplitAcrossWritesAndWaitsForTheConnectedLine(t *testing.T) {
	needScript(t)
	got := filepath.Join(t.TempDir(), "answer")
	bin := consentScript(t, `printf 'Enable Remote '; sleep 0.2; printf 'Control? (y/n) '
read a
echo "$a" > "$1"
echo '·✔︎· Connected · worktrees · HEAD'
exec sleep 30
`)
	start := time.Now()
	if err := answerConsent(context.Background(), host.Cmd{Name: bin, Args: []string{got}}, io.Discard, quickWaits); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got); string(b) != "y\n" {
		t.Fatalf("answer %q", b)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("remote-control was not stopped")
	}
}

// The dialog's own text can match the connected line; only output after the y proves consent.
func TestAnswerConsentAnswersADialogThatLooksConnected(t *testing.T) {
	needScript(t)
	got := filepath.Join(t.TempDir(), "answer")
	bin := consentScript(t, `printf 'Enable Remote Control? Sessions show at https://claude.ai/code/sessions (y/n) '
read a
echo "$a" > "$1"
sleep 0.2
echo '·✔︎· Connected · worktrees · HEAD'
exec sleep 30
`)
	if err := answerConsent(context.Background(), host.Cmd{Name: bin, Args: []string{got}}, io.Discard, quickWaits); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got); string(b) != "y\n" {
		t.Fatalf("answer %q", b)
	}
}

func TestAnswerConsentFailsWhenADialogThatLooksConnectedGoesUnanswered(t *testing.T) {
	needScript(t)
	bin := consentScript(t, "printf 'Enable Remote Control? Sessions show at https://claude.ai/code/sessions (y/n) '\nexit 1\n")
	if err := answerConsent(context.Background(), host.Cmd{Name: bin}, io.Discard, quickWaits); err == nil ||
		!strings.Contains(err.Error(), "exited before it connected") {
		t.Fatalf("err=%v", err)
	}
}

// The output is all copied before Wait returns, so a connected line printed before the exit counts.
func TestAnswerConsentTakesAConnectedLineFromAProcessThatExits(t *testing.T) {
	needScript(t)
	bin := consentScript(t, "echo '·✔︎· Connected · worktrees · HEAD'\nexit 0\n")
	if err := answerConsent(context.Background(), host.Cmd{Name: bin}, io.Discard, quickWaits); err != nil {
		t.Fatal(err)
	}
}

func TestAnswerConsentTakesAConnectedLineWithoutAPromptAsConsentGiven(t *testing.T) {
	needScript(t)
	got := filepath.Join(t.TempDir(), "answer")
	bin := consentScript(t, "echo '·✔︎· Connected · worktrees · HEAD'\nexec sleep 30\n")
	if err := answerConsent(context.Background(), host.Cmd{Name: bin, Args: []string{got}}, io.Discard, quickWaits); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(got); err == nil {
		t.Fatal("answered a prompt that never appeared")
	}
}

// Silence is not consent: another dialog, such as the folder trust, or a slow start look the same.
func TestAnswerConsentFailsWithTheOutputWhenNothingConnects(t *testing.T) {
	needScript(t)
	bin := consentScript(t, "echo 'Do you trust the files in this folder? (y/n)'\nexec sleep 30\n")
	w := quickWaits
	w.connect = 500 * time.Millisecond
	start := time.Now()
	err := answerConsent(context.Background(), host.Cmd{Name: bin}, io.Discard, w)
	if err == nil || !strings.Contains(err.Error(), "no connected line") || !strings.Contains(err.Error(), "trust the files") ||
		!strings.Contains(err.Error(), "by hand") {
		t.Fatalf("err=%v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("remote-control was not stopped")
	}
}

func TestAnswerConsentReportsAnEarlyExit(t *testing.T) {
	needScript(t)
	bin := consentScript(t, "echo 'Error: not logged in'\nexit 1\n")
	err := answerConsent(context.Background(), host.Cmd{Name: bin}, io.Discard, quickWaits)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("err=%v", err)
	}
}

func TestConsentMatchersReadPastEscapeCodes(t *testing.T) {
	w := newConsentWatch(io.Discard)
	// Ink moves the cursor right instead of printing a space.
	w.Write([]byte("\x1b[1mEnable\x1b[1CRemote\x1b[0m Control\x1b[22m? (y/n) "))
	select {
	case <-w.prompt:
	default:
		t.Fatal("prompt not seen")
	}
	select {
	case <-w.connected:
		t.Fatal("the prompt is not a connected line")
	default:
	}
}

// Lines from a real run on flocc-nimble (Claude Code 2.1.304), escape codes removed.
func TestConnectedLineMatchesOnlyTheStatusLine(t *testing.T) {
	for _, line := range []string{
		"The session keeps running on this machine. Use your other devices as a remote",
		"Enable Remote Control? (y/n) y",
		"Remote Control connected",
		"Ready when you are",
		"Sessions show at https://claude.ai/code/sessions",
		"Capacity: 0/32 · New sessions will be created in the current directory",
	} {
		if connectedLine.MatchString(line) {
			t.Errorf("matched %q", line)
		}
	}
	if !connectedLine.MatchString("·✔︎· Connected · worktrees · HEAD") {
		t.Error("missed the status line")
	}
}
