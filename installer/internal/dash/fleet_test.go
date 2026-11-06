package dash

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/ui/theme/themetest"
)

var fixtureTitles = map[string]Title{
	"NF-240": {"Fix the poller tick", "https://linear.app/x/issue/NF-240"},
	"NF-388": {"Retry a failed publish", "https://linear.app/x/issue/NF-388"},
	"NF-402": {"Trim the activity log", "https://linear.app/x/issue/NF-402"},
}

// goldenModel is the canned fleet with a title for every key.
func goldenModel(t *testing.T, sz [2]int) *Model {
	t.Helper()
	m, _ := newModel(t)
	m.titles = maps.Clone(fixtureTitles)
	m.Update(canned())
	m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
	return m
}

func TestTabCyclesHostsAndIssues(t *testing.T) {
	m, _ := newModel(t)
	m.Update(canned())
	for _, want := range []focus{hostsPane, issuesPane, hostsPane} {
		key(m, "tab")
		if m.focus != want {
			t.Fatalf("focus %v, want %v", m.focus, want)
		}
	}
}

func TestQuestionMarkListsTabAndFilter(t *testing.T) {
	m, _ := newModel(t)
	key(m, "?")
	for _, want := range []string{"tab", "filter", "open"} {
		if !strings.Contains(text(m), want) {
			t.Errorf("? lacks %q:\n%s", want, text(m))
		}
	}
}

func TestArrowsMoveOnlyTheFocusedPane(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	key(m, "down")
	if m.sel != 1 || m.hostSel != 0 {
		t.Fatalf("issues focus: sel %d host %d", m.sel, m.hostSel)
	}
	key(m, "tab")
	key(m, "down")
	key(m, "down")
	if m.hostSel != 1 || m.sel != 1 {
		t.Fatalf("hosts focus: sel %d host %d (two hosts, so down twice stops at 1)", m.sel, m.hostSel)
	}
}

func TestFilterMatchesKeyTitleHostOrState(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	key(m, "/")
	typed(m, "blo")
	key(m, "enter")
	if l := line(m, "1 of 1 · filter: blo"); l == "" {
		t.Fatalf("hint:\n%s", text(m))
	}
	got := text(m)
	if !strings.Contains(got, "NF-240") || strings.Contains(got, "NF-388") || strings.Contains(got, "NF-402") {
		t.Errorf("filter blo:\n%s", got)
	}
	key(m, "esc")
	if !strings.Contains(text(m), "NF-388") || !strings.Contains(text(m), "2 of 3 · / filter") {
		t.Errorf("esc did not clear:\n%s", text(m))
	}
	for filter, want := range map[string]string{"retry": "NF-388", "flocc-2": "NF-402", "running": "NF-388", "nf-240": "NF-240"} {
		key(m, "/")
		typed(m, filter)
		key(m, "enter")
		if !strings.Contains(text(m), want) {
			t.Errorf("filter %q lacks %s:\n%s", filter, want, text(m))
		}
		key(m, "esc")
	}
	key(m, "/")
	typed(m, "zzz")
	key(m, "enter")
	if !strings.Contains(text(m), "no issue matches, esc clears") {
		t.Errorf("empty filter:\n%s", text(m))
	}
	if _, ok := m.selected(); ok {
		t.Error("a selection with no visible row")
	}
	if cmd := key(m, "a"); cmd != nil {
		t.Error("a acted on no row")
	}
}

func TestFilterTypingTakesQuitAndTheOtherKeys(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	key(m, "/")
	if cmd := key(m, "q"); cmd != nil || m.filter != "q" {
		t.Fatalf("q was not text: filter %q cmd %v", m.filter, cmd)
	}
	key(m, "backspace")
	typed(m, "a")
	typed(m, "r")
	if m.filter != "ar" || m.ask != nil {
		t.Errorf("filter %q", m.filter)
	}
	if !strings.Contains(text(m), "/ar") {
		t.Errorf("typing not on the status line:\n%s", text(m))
	}
}

func TestNoHostsYet(t *testing.T) {
	m, _ := newModel(t)
	m.Update(fetched{at: t0})
	got := text(m)
	for _, want := range []string{"0 hosts · 0 issues", "no hosts yet, run flocc setup", "no claimed issues"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
}

func TestAHostWithNoClaims(t *testing.T) {
	m, _ := newModel(t)
	f := canned()
	f.results = f.results[:1]
	f.results[0].Status = report(1)
	m.Update(f)
	if !strings.Contains(text(m), "no claimed issues") || !strings.Contains(line(m, "tick"), "0/2") {
		t.Errorf("%s", text(m))
	}
}

func TestFailedHostKeepsItsReportDim(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	f := canned()
	f.at = t0.Add(time.Second)
	f.results[0] = Result{Host: ship.FleetHost{Name: "flocc-nimble"}, Err: errors.New("exit 255: Connection refused")}
	m.Update(f)
	if l := line(m, "flocc-nimble"); !strings.Contains(l, "Connection refused") || !strings.Contains(l, "○") {
		t.Errorf("host row %q", l)
	}
	dim := strings.SplitN(m.th.Dim.Render("x"), "x", 2)[0]
	rawLine := func(sub string) string {
		for _, l := range strings.Split(m.View().Content, "\n") {
			if strings.Contains(themetest.Strip(l), sub) && strings.Contains(themetest.Strip(l), "ctx") {
				return l
			}
		}
		return ""
	}
	if l := rawLine("NF-388"); l == "" || !strings.Contains(l, dim) {
		t.Errorf("a stale row is not dim: %q", l)
	}
	if l := rawLine("NF-402"); l == "" || strings.Contains(l, dim) {
		t.Errorf("a fresh row is dim: %q", l)
	}
}

func TestAHostWithoutAVersionIsBehind(t *testing.T) {
	m, _ := newModel(t)
	f := canned()
	f.results[0].Status = report(0)
	m.Update(f)
	if l := line(m, "flocc-nimble"); !strings.Contains(l, "behind · ship to update") {
		t.Errorf("host row %q", l)
	}
}

func TestAHostShowsItsCommitAndIsBehindWhenItDiffersFromTheBinary(t *testing.T) {
	old := ship.SelfBuild
	defer func() { ship.SelfBuild = old }()
	m, _ := newModel(t)

	ship.SelfBuild = func() string { return "abc1234def5678" }
	m.Update(canned())
	if l := line(m, "flocc-nimble"); !strings.Contains(l, "abc1234") || strings.Contains(l, "behind") {
		t.Errorf("host on the binary's commit %q", l)
	}

	ship.SelfBuild = func() string { return "fff0000aaaa" }
	f := canned()
	f.at = t0.Add(1)
	m.Update(f)
	if l := line(m, "flocc-nimble"); !strings.Contains(l, "abc1234 not fff0000 · behind · ship to update") {
		t.Errorf("host on another commit %q", l)
	}

	ship.SelfBuild = func() string { return "" }
	f.at = t0.Add(2)
	m.Update(f)
	if l := line(m, "flocc-nimble"); !strings.Contains(l, "abc1234") || strings.Contains(l, "behind") {
		t.Errorf("host judged by an unstamped build %q", l)
	}
}

func TestAHostThatIsNotStarted(t *testing.T) {
	m, _ := newModel(t)
	f := canned()
	f.results[0] = Result{Host: ship.FleetHost{Name: "flocc-nimble", State: "stopped"}, Err: errors.New("stopped")}
	m.Update(f)
	if l := line(m, "flocc-nimble"); !strings.Contains(l, "○") || !strings.Contains(l, "stopped") {
		t.Errorf("host row %q", l)
	}
}

func TestTitlesAskedOncePerKey(t *testing.T) {
	var (
		mu    sync.Mutex
		asked [][]string
		err   error
	)
	m, _ := newModel(t)
	m.opts.Titles = func(_ context.Context, keys []string) (map[string]Title, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, keys)
		return map[string]Title{"NF-240": {Title: "Fix the poller tick"}}, err
	}
	_, cmd := m.Update(canned())
	settle(m, cmd, 3)
	if fmt.Sprint(asked) != "[[NF-240 NF-388 NF-402]]" {
		t.Fatalf("asked %v", asked)
	}
	if !strings.Contains(text(m), "Fix the poller tick") {
		t.Errorf("no title:\n%s", text(m))
	}
	f := canned()
	f.at = t0.Add(time.Second)
	_, cmd = m.Update(f)
	settle(m, cmd, 3)
	if len(asked) != 1 {
		t.Errorf("asked again: %v", asked)
	}

	m, _ = newModel(t)
	err = errors.New("no Linear key stored; run flocc keys")
	m.opts.Titles = func(context.Context, []string) (map[string]Title, error) { return nil, err }
	_, cmd = m.Update(canned())
	settle(m, cmd, 3)
	if !strings.Contains(text(m), "titles unavailable: no Linear key stored; run flocc keys") {
		t.Errorf("no note:\n%s", text(m))
	}
	if len(m.titles) != 0 {
		t.Errorf("an error cached %v", m.titles)
	}

	m, _ = newModel(t)
	_, cmd = m.Update(canned())
	if cmd != nil {
		t.Error("nil Titles asked")
	}
	if strings.Contains(text(m), "titles unavailable") {
		t.Error("nil Titles left a note")
	}
}

func TestAttachAndReleaseDimOffTheIssuesPane(t *testing.T) {
	m, ran := newModel(t)
	m.Update(canned())
	key(m, "tab")
	if cmd := key(m, "a"); cmd != nil || len(*ran) != 0 {
		t.Errorf("a ran %v", *ran)
	}
	if key(m, "r"); m.ask != nil {
		t.Error("r asked with the hosts pane focused")
	}
	if !strings.Contains(m.View().Content, m.th.Off.Render("a")+" "+m.th.Off.Render("attach")) {
		t.Errorf("a attach is not dimmed:\n%q", m.View().Content)
	}
	key(m, "tab")
	if strings.Contains(m.View().Content, m.th.Off.Render("a")+" "+m.th.Off.Render("attach")) {
		t.Error("a attach is dimmed on the issues pane")
	}
}

func TestHostileStringsAreSanitised(t *testing.T) {
	hostile := "\x1b[2J" + strings.Repeat("x", 300)
	f := canned()
	f.results[1].Status = report(1, claim("NF-402", "test", "running", 18, 60, hostile))
	f.results[0].Host.Name = "flocc\x1b[2J-nimble"
	for _, sz := range [][2]int{{80, 24}, {140, 40}} {
		m, _ := newModel(t)
		m.opts.Titles = func(context.Context, []string) (map[string]Title, error) {
			return map[string]Title{"NF-402": {Title: hostile, URL: "https://x/\x1b[2J"}}, nil
		}
		m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		_, cmd := m.Update(f)
		settle(m, cmd, 3)
		if got := m.View().Content; strings.Contains(got, "\x1b[2J") {
			t.Errorf("%v: an escape reached the frame", sz)
		}
		if !strings.Contains(text(m), "xxxx") {
			t.Errorf("%v: the hostile title never drew:\n%s", sz, text(m))
		}
		for _, l := range strings.Split(text(m), "\n") {
			if lipgloss.Width(l) > sz[0] {
				t.Errorf("%v: %q too wide", sz, l)
			}
		}
	}
}

func TestFleetGolden(t *testing.T) {
	for _, sz := range [][2]int{{59, 19}, {80, 24}, {140, 40}} {
		m := goldenModel(t, sz)
		themetest.Golden(t, fmt.Sprintf("fleet-%dx%d", sz[0], sz[1]), text(m))
	}
}
