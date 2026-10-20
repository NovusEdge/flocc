package steps

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const floccBin = "/home/flocc/.local/bin/flocc"

// fixedNow makes the days left on a token the 100 that doctorReport shows.
func fixedNow(t *testing.T) {
	t.Helper()
	old := now
	now = func() time.Time { return time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = old })
}

var jan31 = time.Date(2027, 1, 31, 0, 0, 0, 0, time.UTC)

const doctorReport = `ok    config: /home/flocc/.config/flocc/config.toml
ok    github agent token: /home/flocc/.config/flocc/gh-agent.token
ok    github agent token: expires 2027-01-31 (100 days)
ok    github agent token: read-only on o/r
ok    github publish token: /home/flocc/.config/flocc/gh-publish.token
ok    github publish token: never expires
ok    github publish token: write on o/r
` + loginFailLine + `ok    tmux: server on socket flocc
9 checks, 1 failed
`

// statusJSON is flocc status --json with one live implementer session per key.
func statusJSON(live ...string) string {
	var claims []string
	for _, k := range live {
		claims = append(claims, `{"issue":"`+k+`","phase":"impl","tmux_alive":true}`)
	}
	return `{"tick_lock":"/home/flocc/.local/state/flocc/ledger.lock","claims":[` + strings.Join(claims, ",") + `]}`
}

func summaryFake(doctorCode int, doctorOut string) *host.Fake {
	return host.NewFake().
		On(doctorCmd, doctorCode, doctorOut).
		On(floccBin+" --version", 0, "flocc 0.4.0\n").
		On(claudeBin+" auth status", 1, `{"loggedIn":false}`).
		On(floccBin+" status --json", 0, statusJSON())
}

func row(k, v string) string { return fmt.Sprintf("  %-14s %s\n", k, v) }

func TestSummaryOfAFreshHostWithTheLoginPending(t *testing.T) {
	fixedNow(t)
	s := Summarize(context.Background(), testEnv(t, summaryFake(1, doctorReport)))
	if s.Failing() {
		t.Fatalf("a pending login alone must not fail ship: %+v", s)
	}
	if s.Version != "flocc 0.4.0" || s.Login != "none" || !s.LoginPending || s.Timer || len(s.Sessions) != 0 {
		t.Fatalf("%+v", s)
	}
	if got, ok := s.TokenExpiry["agent token"]; !ok || !got.Equal(jan31) {
		t.Fatalf("agent token expiry %v %v", got, ok)
	}
	if got, ok := s.TokenExpiry["publish token"]; !ok || !got.IsZero() {
		t.Fatalf("publish token never expires: %v %v", got, ok)
	}
	var b strings.Builder
	s.Target = "root@h"
	s.Print(&b)
	want := "flocc 0.4.0\n" +
		row("claude login", "none; on the desktop run: flocc login -H root@h") +
		row("agent token", "expires 2027-01-31 (100 days)") +
		row("publish token", "never expires") +
		row("timer", "off until the Claude login") +
		row("sessions", "none")
	if b.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", b.String(), want)
	}
}

func TestSummaryFailsOnAnyOtherFailLine(t *testing.T) {
	fixedNow(t)
	s := Summarize(context.Background(), testEnv(t, summaryFake(1, doctorReport+"FAIL  linear: 401\n")))
	if !s.Failing() || !slices.Equal(s.Failed, []string{"FAIL  linear: 401"}) {
		t.Fatalf("%+v", s)
	}
	var b strings.Builder
	s.Print(&b)
	if !strings.HasSuffix(b.String(), "  FAIL  linear: 401\n") {
		t.Fatalf("%q", b.String())
	}
}

// The Run step keeps the timer off on any login FAIL line unless the login is pending, so the
// summary must fail too.
func TestSummaryKeepsTheLoginFailLineWhenTheLoginIsNotPending(t *testing.T) {
	fixedNow(t)
	f := summaryFake(1, doctorReport)
	f.Responses[claudeBin+" auth status"] = authFull
	s := Summarize(context.Background(), testEnv(t, f))
	if s.LoginPending || !s.Failing() || !slices.Equal(s.Failed, []string{strings.TrimSuffix(loginFailLine, "\n")}) {
		t.Fatalf("%+v", s)
	}
}

func TestSummaryOfAnUpgradedHostWhoseLoginLapsed(t *testing.T) {
	fixedNow(t)
	f := summaryFake(1, doctorReport).
		On("systemctl --user is-enabled --quiet flocc.timer", 0, "").
		On(floccBin+" status --json", 0, statusJSON("NF-1"))
	s := Summarize(context.Background(), testEnv(t, f))
	var b strings.Builder
	s.Target = "root@h"
	s.Print(&b)
	out := b.String()
	if s.Failing() || !s.Timer || !s.LoginPending || !strings.Contains(out, row("timer", "on")) ||
		!strings.Contains(out, row("sessions", "NF-1 impl")) || !strings.Contains(out, "flocc login -H root@h") {
		t.Fatalf("%q", out)
	}
}

func TestSummaryLineRoundTripsAndPrintsNothingWhenEmpty(t *testing.T) {
	in := Summary{Version: "flocc 0.4.0", Login: "token-only (oauth_token)", LoginPending: true,
		TokenExpiry: map[string]time.Time{"agent token": jan31, "publish token": {}},
		Failed:      []string{"FAIL  linear: 401"}, Sessions: []string{"NF-1 impl"}, Target: "never sent"}
	line := in.Line()
	if !strings.HasPrefix(line, SummaryMarker) || strings.ContainsAny(line, "\r\n") {
		t.Fatalf("%q", line)
	}
	out, ok := ParseSummaryLine(line)
	if !ok || out.Login != in.Login || !out.LoginPending || !out.TokenExpiry["agent token"].Equal(jan31) ||
		!slices.Equal(out.Failed, in.Failed) || !slices.Equal(out.Sessions, in.Sessions) || out.Target != "" {
		t.Fatalf("%+v %v", out, ok)
	}
	if _, ok := ParseSummaryLine("flocc-summary {not json"); ok {
		t.Fatal("a corrupt line must not parse")
	}
	var b strings.Builder
	Summary{}.Print(&b)
	if b.Len() != 0 {
		t.Fatalf("%q", b.String())
	}
}

func TestRedactKeepsSecretsOutOfTheLine(t *testing.T) {
	in := Summary{
		Version:     "flocc 0.4.0 tok-version",
		Login:       "token-only (tok-login)",
		Failed:      []string{"FAIL  linear: 401 tok-failed", "FAIL  b: tok-failed-long-secret"},
		Sessions:    []string{"NF-1 tok-session"},
		SessionsErr: "flocc status failed: tok-err",
	}
	secrets := []string{"", "tok-version", "tok-login", "tok-failed", "tok-failed-long-secret", "tok-session", "tok-err"}
	out := in.Redact(secrets...)
	line := out.Line()
	for _, x := range secrets[1:] {
		if strings.Contains(line, x) {
			t.Errorf("%q leaked: %s", x, line)
		}
	}
	if strings.Contains(line, "long-secret") || strings.Contains(line, "-long") {
		t.Errorf("the shorter secret left the longer one's tail: %s", line)
	}
	if out.Failed[1] != "FAIL  b: ***" || out.Version != "flocc 0.4.0 ***" {
		t.Errorf("%+v", out)
	}
	if in.Failed[0] != "FAIL  linear: 401 tok-failed" || !strings.Contains(in.Line(), "tok-version") {
		t.Errorf("Redact changed its receiver: %+v", in)
	}
	if got := in.Redact().Line(); got != in.Line() {
		t.Errorf("no secrets must change nothing: %s", got)
	}
}

func TestSummaryReusesTheRunStepsDoctor(t *testing.T) {
	f := summaryFake(0, "")
	delete(f.Responses, doctorCmd)
	e := testEnv(t, f)
	e.lastDoctor = &doctorResult{out: "ok    tmux: server on socket flocc\n", ok: true}
	Summarize(context.Background(), e)
	if f.Ran(doctorCmd) {
		t.Fatal("doctor ran a second time")
	}
}

func TestSummaryWhenDoctorDoesNotRun(t *testing.T) {
	f := summaryFake(0, "")
	f.Responses[doctorCmd] = host.Result{Code: 2, Stderr: "Traceback (most recent call last):\n"}
	s := Summarize(context.Background(), testEnv(t, f))
	if !s.Failing() || !strings.Contains(s.Failed[0], "did not run cleanly") {
		t.Fatalf("%+v", s)
	}
}

func TestSummaryShowsAStatusFailure(t *testing.T) {
	fixedNow(t)
	f := summaryFake(1, doctorReport)
	f.Responses[floccBin+" status --json"] = host.Result{Code: 1}
	var b strings.Builder
	Summarize(context.Background(), testEnv(t, f)).Print(&b)
	if !strings.Contains(b.String(), row("sessions", "unknown: flocc status failed")) ||
		!strings.Contains(b.String(), "flocc login -H <host>") {
		t.Fatalf("%q", b.String())
	}
}
