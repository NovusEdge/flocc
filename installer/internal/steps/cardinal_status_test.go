package steps

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

func TestShouldRenew(t *testing.T) {
	for _, c := range []struct {
		active     bool
		age, quiet time.Duration
		want       bool
	}{
		{true, 21 * time.Hour, 6 * time.Minute, true},
		{false, 21 * time.Hour, 6 * time.Minute, false},
		{true, 19 * time.Hour, 6 * time.Minute, false},
		{true, 21 * time.Hour, 4 * time.Minute, false},
	} {
		if got := ShouldRenew(c.active, c.age, c.quiet, RenewAge); got != c.want {
			t.Errorf("%+v: got %v", c, got)
		}
	}
}

func f64(v float64) *float64 { return &v }

func TestCardinalStatusFailures(t *testing.T) {
	ok := CardinalStatus{Unit: "active", Login: "full", SessionAgeS: f64(3600), LastTurnAgeS: f64(60), Hosts: map[string]bool{"flocc-nimble": true}}
	if f := ok.Failures(); len(f) != 0 {
		t.Fatalf("healthy status fails: %v", f)
	}
	for name, c := range map[string]struct {
		mut  func(*CardinalStatus)
		want string
	}{
		"unit":        {func(s *CardinalStatus) { s.Unit = "failed" }, "cardinal.service is failed: flocc cardinal restart"},
		"login":       {func(s *CardinalStatus) { s.Login = "none" }, "login is none: flocc cardinal login"},
		"stale turn":  {func(s *CardinalStatus) { s.LastTurnAgeS = f64(46 * 60) }, "no turn for 46m0s"},
		"no turn":     {func(s *CardinalStatus) { s.LastTurnAgeS = nil }, "no turn since the session started"},
		"old session": {func(s *CardinalStatus) { s.SessionAgeS = f64(6.5 * 86400) }, "6d old"},
		"host":        {func(s *CardinalStatus) { s.Hosts["flocc-nimble"] = false }, "flocc-nimble unreachable: flocc cardinal setup"},
	} {
		s := ok
		s.Hosts = map[string]bool{"flocc-nimble": true}
		c.mut(&s)
		f := s.Failures()
		if len(f) != 1 || !strings.Contains(f[0], c.want) {
			t.Errorf("%s: %v", name, f)
		}
	}
}

func TestQuickStatusSkipsLinearAndHosts(t *testing.T) {
	f := host.NewFake()
	e := userEnv(t, f)
	s := CollectCardinalStatus(context.Background(), e, "cardinal", true, time.Now())
	for _, c := range f.Calls {
		if strings.Contains(c, "mcp list") || strings.HasPrefix(c, "ssh ") {
			t.Fatalf("quick status ran %q", c)
		}
	}
	if !s.Quick || s.Schema != 1 || s.Unit != "unknown" {
		t.Fatalf("%+v", s)
	}
}

func TestCardinalRenewRestartsOnlyAnOldQuietSession(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	for _, c := range []struct {
		started   time.Time
		turnAgo   time.Duration
		restarted bool
	}{
		{now.Add(-21 * time.Hour), 10 * time.Minute, true},
		{now.Add(-21 * time.Hour), time.Minute, false},
		{now.Add(-2 * time.Hour), 10 * time.Minute, false},
	} {
		f := host.NewFake().
			On(userctlPrefix+"is-active --quiet cardinal.service", 0, "").
			On(userctlPrefix+"show cardinal.service -p ActiveEnterTimestamp --value --timestamp=unix", 0, "@"+strconv.FormatInt(c.started.Unix(), 10)+"\n").
			On(userctlPrefix+"restart cardinal.service", 0, "")
		e := userEnv(t, f)
		p := e.HomePath(".claude/projects/-home-cardinal/s.jsonl")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("{}"), 0o600)
		os.Chtimes(p, now.Add(-c.turnAgo), now.Add(-c.turnAgo))
		got, err := CardinalRenew(context.Background(), e, RenewAge, now)
		if err != nil || got != c.restarted || f.Ran(userctlPrefix+"restart cardinal.service") != c.restarted {
			t.Errorf("%+v: restarted=%v err=%v", c, got, err)
		}
	}
}

func TestCardinalPaneDropsLinesWithACode(t *testing.T) {
	f := host.NewFake().On("tmux -L cardinal capture-pane -p -t =cardinal -S -15", 0, "ok line\nhttp://localhost:3000/callback?code=SECRET&state=x\nlast\n")
	if p := CardinalPane(context.Background(), userEnv(t, f), "cardinal"); strings.Contains(p, "SECRET") || !strings.Contains(p, "last") {
		t.Fatalf("pane %q", p)
	}
}
