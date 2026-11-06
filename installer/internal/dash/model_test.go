package dash

import (
	"context"
	"errors"
	"image/color"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
	"github.com/NovusEdge/flocc/installer/internal/ui/theme/themetest"
)

var t0 = time.Date(2026, 10, 22, 14, 10, 0, 0, time.UTC)

func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

func f64(v float64) *float64 { return &v }

func report(v int, claims ...ship.Claim) *ship.Report {
	r := &ship.Report{V: v, Mode: "live", SessionCap: 2, FiveHourMaxPct: 80, WeeklyMaxPct: 90, Claims: claims}
	r.LastTick = &struct {
		Exit *string  `json:"exit"`
		AgeS *float64 `json:"age_s"`
	}{AgeS: f64(120), Exit: new("0")}
	r.Usage = &struct {
		FiveHourPct float64 `json:"five_hour_pct"`
		WeeklyPct   float64 `json:"weekly_pct"`
	}{12, 31}
	r.Disk = &struct {
		FreeBytes  uint64 `json:"free_bytes"`
		TotalBytes uint64 `json:"total_bytes"`
	}{82 << 30, 200 << 30}
	r.ClaudeLogin = new("full")
	if v > 0 {
		r.Commit = "abc1234def5678"
	}
	return r
}

func claim(issue, phase, state string, pct float64, upd float64, act ...string) ship.Claim {
	c := ship.Claim{Issue: issue, Phase: phase, State: state, ContextPct: f64(pct),
		UpdatedAge: f64(upd), StartedAge: 3 * 86400, TmuxAlive: state == "running"}
	for i, a := range act {
		c.Activity = append(c.Activity, ship.ActivityLine{At: float64(t0.Add(time.Duration(i) * time.Minute).Unix()), Text: a})
	}
	return c
}

func canned() fetched {
	a := report(1, claim("NF-240", "fix", "blocked", 7, 86400, "ran just check: 2 failures", "asked in Linear: keep the old summary line?"),
		claim("NF-388", "review", "running", 41, 1))
	b := report(1, claim("NF-402", "test", "running", 18, 60))
	return fetched{
		results: []Result{
			{Host: ship.FleetHost{Name: "flocc-nimble"}, Status: a},
			{Host: ship.FleetHost{Name: "flocc-2"}, Status: b},
		},
		at: t0,
	}
}

// fakeSource answers Run from answers, keyed by the joined args, and records every call.
type fakeSource struct {
	mu      sync.Mutex
	answers map[string]host.Result
	calls   []string
}

func (*fakeSource) Discover(context.Context) ([]ship.FleetHost, []string) { return nil, nil }
func (*fakeSource) Status(context.Context, ship.FleetHost) (*ship.Report, error) {
	return nil, nil
}

func (f *fakeSource) Run(_ context.Context, h ship.FleetHost, args []string) (host.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := strings.Join(args, " ")
	f.calls = append(f.calls, h.Name+": "+k)
	if r, ok := f.answers[norm(k)]; ok {
		return r, nil
	}
	return host.Result{Code: 1, Stderr: "no scripted answer for " + k}, nil
}

var lastN = regexp.MustCompile(` -n (\d+)$`)

// norm makes a screen's row count irrelevant to a scripted answer, except the pager's 2000.
func norm(k string) string {
	if m := lastN.FindStringSubmatch(k); m != nil && m[1] != "2000" {
		return lastN.ReplaceAllString(k, " -n *")
	}
	return k
}

func (f *fakeSource) answer(args string, r host.Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.answers == nil {
		f.answers = map[string]host.Result{}
	}
	f.answers[norm(args)] = r
}

func (f *fakeSource) ran() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func src(m *Model) *fakeSource { return m.opts.Source.(*fakeSource) }

// newModel returns a model on a 100×30 terminal and the handoffs it has built.
func newModel(t *testing.T) (*Model, *[][]string) {
	t.Helper()
	m := New(context.Background(), Options{Source: &fakeSource{}, Self: "flocc"})
	m.th = theme.New(true)
	m.spin = m.th.Spinner()
	m.now = func() time.Time { return t0.Add(3 * time.Second) }
	var ran [][]string
	m.command = func(args ...string) *exec.Cmd {
		ran = append(ran, args)
		return exec.Command("true")
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m, &ran
}

func sizeMsg(sz [2]int) tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: sz[0], Height: sz[1]} }

func text(m *Model) string { return themetest.Strip(m.View().Content) }

func key(m *Model, s string) tea.Cmd {
	var k tea.KeyPressMsg
	switch s {
	case "down":
		k = tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		k = tea.KeyPressMsg{Code: tea.KeyUp}
	case "enter":
		k = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		k = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		k = tea.KeyPressMsg{Code: tea.KeyTab}
	case "backspace":
		k = tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "pgup":
		k = tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		k = tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "right":
		k = tea.KeyPressMsg{Code: tea.KeyRight}
	default:
		k = tea.KeyPressMsg{Code: rune(s[0]), Text: s}
	}
	_, cmd := m.Update(k)
	return cmd
}

// typed sends each rune of s as its own key.
func typed(m *Model, s string) {
	for _, r := range s {
		key(m, string(r))
	}
}

// settle feeds the messages cmd produces back into the model, a few levels deep, as the program
// loop would. A tea.Tick blocks for its duration, so settle skips commands slower than 50ms.
func settle(m *Model, cmd tea.Cmd, depth int) {
	if cmd == nil || depth == 0 {
		return
	}
	msg := run(cmd)
	switch msg := msg.(type) {
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

// run returns cmd's message, or nil when it takes longer than 50ms (a tick or a spinner).
func run(cmd tea.Cmd) tea.Msg {
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(50 * time.Millisecond):
		return nil
	}
}

func line(m *Model, sub string) string {
	for _, l := range strings.Split(text(m), "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func TestTooSmall(t *testing.T) {
	m, _ := newModel(t)
	m.Update(canned())
	m.Update(tea.WindowSizeMsg{Width: 59, Height: 19})
	if got := text(m); !strings.Contains(got, "terminal too small: 59×19 · needs 60×20") {
		t.Errorf("frame:\n%s", got)
	}
}

func TestBackgroundAnswerSwitchesPalette(t *testing.T) {
	m, _ := newModel(t)
	dark := m.View().Content
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	if m.View().Content == dark {
		t.Error("a light background drew the dark palette")
	}
}

func TestReleaseAsksOnTheStatusLine(t *testing.T) {
	m, ran := newModel(t)
	m.Update(canned())
	key(m, "r") // the dashboard opens with focus on the issues pane
	if l := line(m, "release NF-402? y/N"); l == "" {
		t.Fatalf("no question:\n%s", text(m))
	}
	key(m, "x")
	if l := line(m, "y/N"); l != "" {
		t.Fatalf("a key other than y left the question: %q", l)
	}
	key(m, "r")
	if l := line(m, "release NF-402? y/N"); l == "" {
		t.Fatalf("no question:\n%s", text(m))
	}
	key(m, "n")
	if len(*ran) != 0 || line(m, "y/N") != "" {
		t.Errorf("n released or left the question: %v", *ran)
	}
	key(m, "r")
	settle(m, key(m, "y"), 2)
	if len(*ran) != 1 || strings.Join((*ran)[0], " ") != "-H flocc-2 release NF-402" {
		t.Errorf("ran %v", *ran)
	}
}

func TestQuestionTakesTheKeyBeforeQuit(t *testing.T) {
	m, _ := newModel(t)
	m.Update(canned())
	key(m, "r")
	if cmd := key(m, "q"); cmd != nil {
		t.Error("q quit with a question open")
	}
	if line(m, "y/N") != "" {
		t.Error("q left the question open")
	}
}

func TestHandoffErrorIsAToast(t *testing.T) {
	m, _ := newModel(t)
	m.Update(execDone{what: "attach NF-402", err: errors.New("exit status 1")})
	if !strings.Contains(text(m), "attach NF-402: exit status 1") {
		t.Errorf("no toast:\n%s", text(m))
	}
	m.Update(theme.ToastExpired{Gen: 0})
	if !strings.Contains(text(m), "attach NF-402") {
		t.Error("a stale timer cleared the toast")
	}
	m.Update(theme.ToastExpired{Gen: 1})
	if strings.Contains(text(m), "attach NF-402") {
		t.Error("the current timer left the toast up")
	}
}

func TestQuestionMarkShowsEveryKey(t *testing.T) {
	m, _ := newModel(t)
	key(m, "?")
	for _, want := range []string{"keys", "attach", "release", "quit"} {
		if !strings.Contains(text(m), want) {
			t.Errorf("? lacks %q:\n%s", want, text(m))
		}
	}
}

func TestEveryFrameFitsTheTerminal(t *testing.T) {
	for _, sz := range [][2]int{{59, 19}, {80, 24}, {140, 40}} {
		m, _ := newModel(t)
		m.Update(canned())
		m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		lines := strings.Split(text(m), "\n")
		if len(lines) != sz[1] {
			t.Errorf("%v: %d lines", sz, len(lines))
		}
		for _, l := range lines {
			if lipgloss.Width(l) > sz[0] {
				t.Errorf("%v: %q too wide", sz, l)
			}
		}
	}
}

func TestSelectionFollowsItsRowAcrossAPoll(t *testing.T) {
	m, _ := newModel(t)
	m.Update(canned())
	key(m, "down")
	f := canned()
	f.at = t0.Add(time.Second)
	f.results[1].Status = report(1, claim("NF-100", "fix", "running", 1, 1), claim("NF-240", "fix", "blocked", 7, 840))
	m.Update(f)
	for _, l := range strings.Split(text(m), "\n") {
		if strings.Contains(l, "NF-240") && strings.Contains(l, "flocc-nimble") && !strings.Contains(l, "❯") {
			t.Errorf("selection moved off NF-240 on flocc-nimble:\n%s", text(m))
		}
	}
	if m.selKey != [2]string{"flocc-nimble", "NF-240"} {
		t.Errorf("selKey %v", m.selKey)
	}
}

func TestRowsSortByHostThenIssue(t *testing.T) {
	m, _ := newModel(t)
	m.Update(canned())
	got := text(m)
	if strings.Index(got, "NF-402") > strings.Index(got, "NF-240") || strings.Index(got, "NF-240") > strings.Index(got, "NF-388") {
		t.Errorf("rows are not sorted by host then issue:\n%s", got)
	}
}

func TestHandoffKeys(t *testing.T) {
	m, ran := newModel(t)
	m.Update(canned())
	if cmd := key(m, "a"); cmd == nil || len(*ran) != 1 || strings.Join((*ran)[0], " ") != "-H flocc-2 attach NF-402" {
		t.Errorf("ran %v", *ran)
	}
}

func TestHandoffReturnPollsAtOnce(t *testing.T) {
	m, _ := newModel(t)
	m.Update(canned())
	_, cmd := m.Update(execDone{what: "attach NF-402", err: errors.New("exit status 1")})
	if cmd == nil || !m.fetching {
		t.Error("no poll after the child exited")
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
