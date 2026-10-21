package dash

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/NovusEdge/flocc/installer/internal/ship"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

var t0 = time.Date(2026, 10, 22, 14, 10, 0, 0, time.UTC)

func f64(v float64) *float64 { return &v }

func report(v int, claims ...ship.Claim) *ship.Report {
	r := &ship.Report{V: v, SessionCap: 2, Claims: claims}
	r.LastTick = &struct {
		Exit *string  `json:"exit"`
		AgeS *float64 `json:"age_s"`
	}{AgeS: f64(120)}
	r.Usage = &struct {
		FiveHourPct float64 `json:"five_hour_pct"`
		WeeklyPct   float64 `json:"weekly_pct"`
	}{12, 31}
	r.Disk = &struct {
		FreeBytes  uint64 `json:"free_bytes"`
		TotalBytes uint64 `json:"total_bytes"`
	}{82 << 30, 200 << 30}
	return r
}

func claim(issue, phase, state string, pct float64, launches int, upd float64, act ...string) ship.Claim {
	c := ship.Claim{Issue: issue, Phase: phase, State: state, ContextPct: f64(pct), Launches: launches, LaunchLimit: 2, UpdatedAge: f64(upd)}
	for i, a := range act {
		c.Activity = append(c.Activity, ship.ActivityLine{At: float64(t0.Add(time.Duration(i) * time.Minute).Unix()), Text: a})
	}
	return c
}

func canned() fetched {
	a := report(1, claim("NF-240", "fix", "blocked", 7, 0, 840, "ran just check: 2 failures", "asked in Linear: keep the old summary line?"),
		claim("NF-388", "review", "running", 41, 1, 1))
	b := report(1, claim("NF-402", "test", "running", 18, 1, 60))
	return fetched{
		results: []Result{
			{Host: ship.FleetHost{Name: "flocc-nimble"}, Status: a},
			{Host: ship.FleetHost{Name: "flocc-2"}, Status: b},
		},
		at: t0,
	}
}

type fakeSource struct{}

func (fakeSource) Discover(context.Context) ([]ship.FleetHost, []string) { return nil, nil }
func (fakeSource) Status(context.Context, ship.FleetHost) (*ship.Report, error) {
	return nil, nil
}
func (fakeSource) Cardinals(context.Context) ([]ship.CardinalRow, []string) { return nil, nil }

func newModel(t *testing.T) (*Model, *[][]string) {
	t.Helper()
	m := New(context.Background(), Options{Source: fakeSource{}, Self: "flocc"})
	m.now = func() time.Time { return t0.Add(3 * time.Second) }
	var ran [][]string
	m.command = func(args ...string) *exec.Cmd {
		ran = append(ran, args)
		return exec.Command("true")
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m, &ran
}

func text(m *Model) string { return ansiRE.ReplaceAllString(m.View().Content, "") }

func key(m *Model, s string) tea.Cmd {
	var k tea.KeyPressMsg
	switch s {
	case "down":
		k = tea.KeyPressMsg{Code: tea.KeyDown}
	case "enter":
		k = tea.KeyPressMsg{Code: tea.KeyEnter}
	default:
		k = tea.KeyPressMsg{Code: rune(s[0]), Text: s}
	}
	_, cmd := m.Update(k)
	if m.confirm != nil {
		settle(m, cmd, 4)
	}
	return cmd
}

// settle feeds the messages cmd produces back into the model, a few levels deep, as the program
// loop would.
func settle(m *Model, cmd tea.Cmd, depth int) {
	if cmd == nil || depth == 0 {
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			settle(m, c, depth-1)
		}
	default:
		_, next := m.Update(msg)
		settle(m, next, depth-1)
	}
}

func hhmm(i int) string { return t0.Add(time.Duration(i) * time.Minute).Local().Format("15:04") }

func line(m *Model, sub string) string {
	for _, l := range strings.Split(text(m), "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func TestHeaderStripAndTable(t *testing.T) {
	m, _ := newModel(t)
	m.Update(canned())
	got := text(m)
	for _, want := range []string{
		"flocc  2 hosts · 3 issues", "refreshed 3s ago",
		"flocc-2", "v1", "tick 2m ago", "5h 12%  wk 31%", "disk 82 GiB", "cap 2",
		"ISSUE", "LAUNCHES", "UPDATED",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "NF-402") > strings.Index(got, "NF-240") || strings.Index(got, "NF-240") > strings.Index(got, "NF-388") {
		t.Errorf("rows are not sorted by host then issue:\n%s", got)
	}
	if l := line(m, "NF-388"); !strings.Contains(l, "41%") || !strings.Contains(l, "1/2") || !strings.Contains(l, "now") {
		t.Errorf("NF-388 row %q", l)
	}
}

func TestSelectionAndActivity(t *testing.T) {
	m, _ := newModel(t)
	m.Update(canned())
	if l := line(m, "NF-402"); !strings.Contains(l, "▸") {
		t.Fatalf("first row is not selected:\n%s", text(m))
	}
	if strings.Contains(line(m, "NF-240"), "▸") {
		t.Error("two rows selected")
	}
	key(m, "down")
	if !strings.Contains(line(m, "NF-240"), "▸") || strings.Contains(line(m, "NF-402"), "▸") {
		t.Fatalf("selection did not move:\n%s", text(m))
	}
	got := text(m)
	for _, want := range []string{hhmm(0) + " ran just check: 2 failures", hhmm(1) + " asked in Linear"} {
		if !strings.Contains(got, want) {
			t.Errorf("activity lacks %q:\n%s", want, got)
		}
	}
	key(m, "down")
	if !strings.Contains(text(m), "no activity reported") {
		t.Errorf("empty activity not said:\n%s", text(m))
	}
}

func TestSelectionFollowsItsRowAcrossAPoll(t *testing.T) {
	m, _ := newModel(t)
	m.Update(canned())
	key(m, "down")
	f := canned()
	f.at = t0.Add(time.Second)
	f.results[1].Status = report(1, claim("NF-100", "fix", "running", 1, 0, 1), claim("NF-240", "fix", "blocked", 7, 0, 840))
	m.Update(f)
	if !strings.Contains(line(m, "NF-240    flocc-nimble"), "▸") {
		t.Errorf("selection moved off NF-240:\n%s", text(m))
	}
}

func TestAFailedHostKeepsItsRowsStale(t *testing.T) {
	m, _ := newModel(t)
	m.Update(canned())
	f := canned()
	f.at = t0.Add(time.Second)
	f.results[0] = Result{Host: ship.FleetHost{Name: "flocc-nimble"}, Err: errors.New("exit 255: Connection refused")}
	m.Update(f)
	got := text(m)
	if l := line(m, "flocc-nimble"); !strings.Contains(l, "Connection refused") || !strings.Contains(l, "(stale)") {
		t.Errorf("strip line %q", l)
	}
	if l := line(m, "NF-388"); !strings.Contains(l, "(stale)") {
		t.Errorf("stale row %q\n%s", l, got)
	}
	if l := line(m, "NF-402"); strings.Contains(l, "stale") {
		t.Errorf("fresh row marked stale: %q", l)
	}
}

func TestAHostWithoutAVersionIsBehind(t *testing.T) {
	m, _ := newModel(t)
	f := canned()
	f.results[0].Status = report(0)
	m.Update(f)
	if l := line(m, "flocc-nimble"); !strings.Contains(l, "behind, ship to update") || !strings.Contains(l, "tick 2m ago") {
		t.Errorf("strip line %q", l)
	}
}

func TestNoHostsYet(t *testing.T) {
	m, _ := newModel(t)
	m.Update(fetched{at: t0})
	got := text(m)
	if !strings.Contains(got, "0 hosts · 0 issues") || !strings.Contains(got, "no claimed issues") {
		t.Errorf("%s", got)
	}
}

func TestReleaseAsksFirst(t *testing.T) {
	m, ran := newModel(t)
	m.Update(canned())
	key(m, "r")
	got := text(m)
	for _, want := range []string{"Release NF-402?", "Its session ends and it goes back to Todo."} {
		if !strings.Contains(got, want) {
			t.Fatalf("modal lacks %q:\n%s", want, got)
		}
	}
	if len(*ran) != 0 {
		t.Fatalf("released before the answer: %v", *ran)
	}
	key(m, "enter")
	if m.confirm != nil || len(*ran) != 0 {
		t.Fatalf("the default answer released: %v", *ran)
	}
	key(m, "r")
	key(m, "y")
	key(m, "enter")
	if len(*ran) != 1 || strings.Join((*ran)[0], " ") != "-H flocc-2 release NF-402" {
		t.Fatalf("ran %v", *ran)
	}
}

func TestHandoffKeys(t *testing.T) {
	for _, c := range []struct{ key, want string }{
		{"a", "-H flocc-2 attach NF-402"},
		{"l", "-H flocc-2 logs NF-402"},
		{"c", "cardinal attach"},
	} {
		m, ran := newModel(t)
		m.Update(canned())
		if cmd := key(m, c.key); cmd == nil || len(*ran) != 1 || strings.Join((*ran)[0], " ") != c.want {
			t.Errorf("%s: ran %v", c.key, *ran)
		}
	}
}

func TestHandoffReturnPollsAtOnce(t *testing.T) {
	m, _ := newModel(t)
	m.Update(canned())
	_, cmd := m.Update(execDone{what: "attach NF-402", err: errors.New("exit status 1")})
	if cmd == nil || !m.fetching {
		t.Error("no poll after the child exited")
	}
	if !strings.Contains(text(m), "attach NF-402: exit status 1") {
		t.Errorf("child failure not shown:\n%s", text(m))
	}
}

func TestQuit(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{{Code: 'q', Text: "q"}, {Code: 'c', Mod: tea.ModCtrl}} {
		m, _ := newModel(t)
		_, cmd := m.Update(k)
		if cmd == nil {
			t.Fatalf("%v did not quit", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%v: not a quit", k)
		}
	}
}

func TestPollsEveryIntervalNotWhileInFlight(t *testing.T) {
	m, _ := newModel(t)
	now := t0
	m.now = func() time.Time { return now }
	m.startFetch()
	m.Update(canned())
	now = t0.Add(9 * time.Second)
	m.Update(tick(now))
	if m.fetching {
		t.Error("polled before the interval")
	}
	now = t0.Add(10 * time.Second)
	m.Update(tick(now))
	if !m.fetching {
		t.Error("no poll at the interval")
	}
	started := m.startedAt
	now = t0.Add(12 * time.Second)
	m.Update(tick(now))
	if m.startedAt != started {
		t.Error("second poll started while one was in flight")
	}
}
