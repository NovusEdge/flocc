package dash

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ship"
)

func TestHostIssueKeyNeverReachesTheTerminalRaw(t *testing.T) {
	const evil = "NF-9\x1b[2J"
	m, _ := newModel(t)
	f := canned()
	f.results[1].Status = report(1, claim(evil, "test", "running", 18, 1, 60))
	m.Update(f)
	key(m, "r")
	if strings.Contains(m.View().Content, "\x1b[2J") {
		t.Errorf("release question drew the raw key:\n%q", m.View().Content)
	}
	key(m, "n")
	key(m, "enter")
	if strings.Contains(m.View().Content, "\x1b[2J") {
		t.Errorf("the issue screen drew the raw key:\n%q", m.View().Content)
	}
	m.Update(execDone{what: "ship " + evil, err: errors.New("x")})
	if strings.Contains(m.View().Content, "\x1b[2J") {
		t.Errorf("the toast drew the raw name:\n%q", m.View().Content)
	}
}

func TestPagerNextWalksHitsOnTheLastPage(t *testing.T) {
	m := pagerOn(t, 200)
	key(m, "g")
	key(m, "/")
	typed(m, "line 19")
	key(m, "enter")
	if m.pager.hit != 18 {
		t.Fatalf("first hit %d, want 18", m.pager.hit)
	}
	for _, want := range []int{189, 190, 191, 192, 193, 194, 195, 196, 197, 198, 18} {
		key(m, "n")
		if m.pager.hit != want {
			t.Fatalf("n moved to %d, want %d", m.pager.hit, want)
		}
	}
}

func TestStackedHostScreenKeepsTheLogPaneAndItsBorder(t *testing.T) {
	for _, sz := range [][2]int{{80, 20}, {80, 24}, {99, 30}} {
		m := goldenModel(t, [2]int{140, 40})
		src(m).answer("doctor --json", ok(doctorJSON))
		cmd := openHost(t, m, "flocc-nimble")
		m.Update(sizeMsg(sz))
		settle(m, cmd, 3)
		m.Update(outputMsg{gen: m.outGen, title: "log", lines: []string{"one", "two", "three", "four"}})
		lines := strings.Split(text(m), "\n")
		at := -1
		for i, l := range lines {
			if strings.Contains(l, "╭─ log") {
				at = i
			}
		}
		if at < 0 {
			t.Fatalf("%v: no log pane:\n%s", sz, text(m))
		}
		end := -1
		for i := at; i < len(lines); i++ {
			if strings.Contains(lines[i], "╰") {
				end = i
				break
			}
		}
		if end < 0 || end-at-1 < 3 {
			t.Errorf("%v: log pane has %d rows, bottom border at %d:\n%s", sz, end-at-1, end, text(m))
		}
	}
}

func TestHostsPaneScrollsToTheSelectedHost(t *testing.T) {
	m, _ := newModel(t)
	f := canned()
	f.results = nil
	for _, n := range []string{"h1", "h2", "h3", "h4", "h5", "h6", "h7", "h8"} {
		f.results = append(f.results, Result{Host: ship.FleetHost{Name: n}, Status: report(1)})
	}
	m.Update(f)
	m.focus = hostsPane
	for range 7 {
		key(m, "down")
	}
	if !strings.Contains(text(m), "h8") {
		t.Errorf("the hosts pane did not follow the selection:\n%s", text(m))
	}
}

func TestSmallTerminalTakesOnlyQuit(t *testing.T) {
	m, ran := newModel(t)
	m.Update(canned())
	key(m, "r")
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	if m.ask != nil {
		t.Error("the question stayed open behind the too-small screen")
	}
	if cmd := key(m, "y"); cmd != nil || len(*ran) != 0 {
		t.Errorf("y acted: %v", *ran)
	}
	key(m, "r")
	if m.ask != nil {
		t.Error("r opened a question unseen")
	}
	key(m, "enter")
	if m.screen != fleetScreen {
		t.Error("enter opened a screen unseen")
	}
	if cmd := key(m, "q"); cmd == nil {
		t.Error("q did not quit")
	}
}

func TestEmptyOutputShowsNothingYet(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	src(m).answer("peek NF-240 -n *", ok(""))
	settle(m, openIssue(t, m, "NF-240"), 3)
	if len(m.out.lines) != 0 {
		t.Errorf("lines %q", m.out.lines)
	}
	if !strings.Contains(text(m), "nothing yet") {
		t.Errorf("no empty note:\n%s", text(m))
	}
}

func TestLogsForAnIssueWithNoLinesNamesIt(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	src(m).answer("peek NF-240 -n *", host.Result{Code: 3})
	src(m).answer("logs --issue NF-240 -n *", ok("\n"))
	settle(m, openIssue(t, m, "NF-240"), 3)
	if !strings.Contains(text(m), "no lines name NF-240 yet") {
		t.Errorf("frame:\n%s", text(m))
	}
}

func TestCountsArePluralised(t *testing.T) {
	m, _ := newModel(t)
	rep := report(1, claim("NF-402", "test", "running", 18, 1, 60))
	rep.SessionCap = 1
	m.Update(fetched{results: []Result{{Host: ship.FleetHost{Name: "flocc-2"}, Status: rep}}, at: t0})
	if got := text(m); !strings.Contains(got, "flocc 1 host · 1 issue") {
		t.Errorf("header:\n%s", got)
	}
	openHost(t, m, "flocc-2")
	if got := squeezed(m); !strings.Contains(got, "cap 1 session") || strings.Contains(got, "1 sessions") {
		t.Errorf("host facts:\n%s", got)
	}
}

func TestOpenHintGoesWithoutAURL(t *testing.T) {
	m, _ := newModel(t)
	m.Update(canned())
	openIssue(t, m, "NF-402")
	if strings.Contains(text(m), "open in Linear") {
		t.Errorf("hint shown without a URL:\n%s", text(m))
	}
}

func TestTabBarSaysPanes(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	if got := text(m); !strings.Contains(got, "tab panes") || strings.Contains(got, "hosts/issues") {
		t.Errorf("key bar:\n%s", got)
	}
}

func TestKeyPanelKeepsEveryPaneBorder(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		m := goldenModel(t, sz)
		key(m, "?")
		if top, bottom := strings.Count(text(m), "╭"), strings.Count(text(m), "╰"); top != bottom {
			t.Errorf("%v: %d top borders, %d bottom:\n%s", sz, top, bottom, text(m))
		}
	}
}
