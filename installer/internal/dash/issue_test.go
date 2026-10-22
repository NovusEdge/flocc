package dash

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ui/theme/themetest"
)

// openIssue selects issue on the fleet screen and opens it, returning the output fetch.
func openIssue(t *testing.T, m *Model, issue string) tea.Cmd {
	t.Helper()
	at := slices.IndexFunc(m.visible(), func(r row) bool { return r.claim.Issue == issue })
	for ; m.sel < at; key(m, "down") {
	}
	for ; m.sel > at && at >= 0; key(m, "up") {
	}
	if r, _ := m.selected(); r.claim.Issue != issue {
		t.Fatalf("cannot select %s", issue)
	}
	cmd := key(m, "enter")
	if m.screen != issueScreen {
		t.Fatalf("enter opened screen %v", m.screen)
	}
	return cmd
}

func peekArgs(m *Model, issue string) string { return fmt.Sprintf("peek %s -n %d", issue, m.outRows()) }

func ok(out string) host.Result { return host.Result{Stdout: out} }

func TestIssueScreenShowsPhasesAndFacts(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	openIssue(t, m, "NF-240")
	for _, want := range []string{
		"NF-240 · Fix the poller tick",
		"phase    test ✓ → impl ✓ → review ✓ → fix ✗",
		"state    blocked 24h",
		"session  none · context 7% · launches 0/2",
		"host     flocc-nimble · started 3d ago · updated 24h ago",
		"o open in Linear", "L full screen",
	} {
		if !strings.Contains(text(m), want) {
			t.Errorf("lacks %q:\n%s", want, text(m))
		}
	}
	key(m, "esc")
	if m.screen != fleetScreen {
		t.Error("esc did not go back")
	}
}

func TestPhaseLineForOtherPhases(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	openIssue(t, m, "NF-388") // review, running
	if l := line(m, "phase"); !strings.Contains(l, "test ✓ → impl ✓ → review ⠋ → fix") {
		t.Errorf("phase line %q", l)
	}
	key(m, "esc")
	f := canned()
	f.at = t0.Add(time.Second)
	f.results[0].Status = report(1, claim("NF-240", "triage", "running", 7, 0, 1))
	m.Update(f)
	openIssue(t, m, "NF-240")
	if l := line(m, "phase"); !strings.Contains(l, "triage ⠋") || strings.Contains(l, "→") {
		t.Errorf("a phase outside the list: %q", l)
	}
}

func TestIssueOutputPrefersPeek(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	src(m).answer("peek NF-240 -n 23", ok("first line\nsecond line\n"))
	cmd := openIssue(t, m, "NF-240")
	if m.outRows() < 5 {
		t.Fatalf("rows: %s", peekArgs(m, "NF-240"))
	}
	settle(m, cmd, 3)
	for _, want := range []string{"─ session ─", "first line", "second line"} {
		if !strings.Contains(text(m), want) {
			t.Errorf("lacks %q:\n%s", want, text(m))
		}
	}
}

func TestIssueOutputFallsBackToTheLog(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	cmd := openIssue(t, m, "NF-240")
	src(m).answer(peekArgs(m, "NF-240"), host.Result{Code: 3})
	src(m).answer(fmt.Sprintf("logs --issue NF-240 -n %d", m.outRows()), ok("log one\nlog two\n"))
	settle(m, cmd, 3)
	for _, want := range []string{"─ log ─", "log one", "log two"} {
		if !strings.Contains(text(m), want) {
			t.Errorf("lacks %q:\n%s", want, text(m))
		}
	}
}

func TestOldHostSaysShip(t *testing.T) {
	for _, res := range []host.Result{
		{Code: 2, Stderr: "flocc: error: argument command: invalid choice: 'peek'"},
		{Code: 2, Stderr: "flocc: error: unrecognized arguments: --issue"},
	} {
		m := goldenModel(t, [2]int{140, 40})
		peek := host.Result{Code: 2, Stderr: "invalid choice: 'peek'"}
		if strings.Contains(res.Stderr, "--issue") {
			peek = host.Result{Code: 3}
			src(m).answer("logs --issue NF-240 -n 23", res)
		}
		src(m).answer("peek NF-240 -n 23", peek)
		settle(m, openIssue(t, m, "NF-240"), 3)
		if !strings.Contains(text(m), "ship to see session output") {
			t.Errorf("%q:\n%s", res.Stderr, text(m))
		}
	}
	if oldHost(host.Result{Code: 2, Stderr: "usage: flocc"}) || oldHost(host.Result{Code: 1, Stderr: "invalid choice"}) {
		t.Error("oldHost holds for something else")
	}
}

func TestAnOtherFailureShowsItsFirstLine(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	src(m).answer("peek NF-240 -n 23", host.Result{Code: 1, Stderr: "boom \x1b[2J\nsecond"})
	settle(m, openIssue(t, m, "NF-240"), 3)
	if !strings.Contains(text(m), "boom") || strings.Contains(text(m), "second") || strings.Contains(m.View().Content, "\x1b[2J") {
		t.Errorf("\n%s", text(m))
	}
}

func TestStaleOutputIsDropped(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	src(m).answer("peek NF-240 -n 23", ok("from NF-240\n"))
	src(m).answer("peek NF-402 -n 23", ok("from NF-402\n"))
	old := openIssue(t, m, "NF-240")
	gen := m.outGen
	key(m, "esc")
	cur := openIssue(t, m, "NF-402")
	m.Update(old())
	if strings.Contains(text(m), "from NF-240") {
		t.Fatalf("the first screen's answer drew on the second:\n%s", text(m))
	}
	if !strings.Contains(text(m), "loading") {
		t.Errorf("not loading:\n%s", text(m))
	}
	settle(m, cur, 3)
	if !strings.Contains(text(m), "from NF-402") {
		t.Errorf("no output:\n%s", text(m))
	}
	before := len(src(m).ran())
	if _, cmd := m.Update(outputTick{gen: gen}); cmd != nil {
		t.Error("an old tick started a fetch")
	}
	if len(src(m).ran()) != before {
		t.Error("an old tick ran a command")
	}
}

func TestOutputRefreshesEveryTwoSeconds(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	src(m).answer("peek NF-240 -n 23", ok("one\n"))
	openIssue(t, m, "NF-240")
	_, cmd := m.Update(outputMsg{gen: m.outGen, title: "session", lines: []string{"one"}})
	if cmd == nil {
		t.Fatal("no next tick")
	}
	before := len(src(m).ran())
	_, cmd = m.Update(outputTick{gen: m.outGen})
	if cmd == nil {
		t.Fatal("the tick started nothing")
	}
	settle(m, cmd, 2)
	if len(src(m).ran()) != before+1 {
		t.Errorf("calls %v", src(m).ran())
	}
	if _, cmd := m.Update(outputMsg{gen: m.outGen - 1, lines: []string{"stale"}}); cmd != nil || strings.Contains(text(m), "stale") {
		t.Error("an old answer was kept")
	}
}

func TestIssueGoneFromThePoll(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	openIssue(t, m, "NF-240")
	f := canned()
	f.at = t0.Add(time.Second)
	f.results[0].Status = report(1)
	m.Update(f)
	if !strings.Contains(text(m), "NF-240 is no longer claimed") {
		t.Fatalf("\n%s", text(m))
	}
	if !strings.Contains(m.View().Content, m.th.Off.Render("a")+" "+m.th.Off.Render("attach")) ||
		!strings.Contains(m.View().Content, m.th.Off.Render("r")+" "+m.th.Off.Render("release")) {
		t.Error("a and r are not dimmed")
	}
	if cmd := key(m, "a"); cmd != nil {
		t.Error("a attached to a released issue")
	}
	if key(m, "r"); m.ask != nil {
		t.Error("r asked about a released issue")
	}
	f.at = t0.Add(2 * time.Second)
	f.results = nil
	m.Update(f)
	if text(m) == "" {
		t.Error("blank frame")
	}
}

func TestIssueKeys(t *testing.T) {
	var opened []string
	m, ran := newModel(t)
	m.opts.Open = func(u string) { opened = append(opened, u) }
	m.titles = fixtureTitles
	m.Update(canned())
	openIssue(t, m, "NF-402")
	if cmd := key(m, "a"); cmd == nil || len(*ran) != 1 || strings.Join((*ran)[0], " ") != "-H flocc-2 attach NF-402" {
		t.Errorf("ran %v", *ran)
	}
	key(m, "r")
	if !strings.Contains(text(m), "release NF-402? y/N") {
		t.Errorf("no question:\n%s", text(m))
	}
	key(m, "n")
	settle(m, key(m, "o"), 2)
	if len(opened) != 1 || opened[0] != "https://linear.app/x/issue/NF-402" {
		t.Errorf("opened %v", opened)
	}
	if key(m, "q"); m.screen != issueScreen {
		t.Error("q left the issue screen")
	}
	key(m, "h")
	if m.screen != fleetScreen {
		t.Error("h did not go back")
	}
	openIssue(t, m, "NF-402")
	key(m, "left")
}

func TestOpenDimsWithoutAURL(t *testing.T) {
	m, _ := newModel(t)
	m.Update(canned())
	openIssue(t, m, "NF-402")
	if !strings.Contains(m.View().Content, m.th.Off.Render("o")+" "+m.th.Off.Render("open")) {
		t.Error("o open is not dimmed without a URL")
	}
	if cmd := key(m, "o"); cmd != nil {
		t.Error("o did something without a URL")
	}
}

func TestResizeKeepsTheScreen(t *testing.T) {
	sizes := [][2]int{{140, 40}, {80, 24}, {59, 19}, {140, 40}}
	for _, name := range []string{"issue", "pager"} {
		m := goldenModel(t, sizes[0])
		src(m).answer("peek NF-240 -n 23", ok("one\ntwo\n"))
		src(m).answer("peek NF-240 -n 2000", ok(strings.Repeat("a line\n", 50)))
		settle(m, openIssue(t, m, "NF-240"), 3)
		if name == "pager" {
			settle(m, key(m, "L"), 3)
		}
		first := text(m)
		for _, sz := range sizes {
			m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			got := text(m)
			if n := len(strings.Split(got, "\n")); n != sz[1] {
				t.Errorf("%s %v: %d lines", name, sz, n)
			}
			for _, l := range strings.Split(got, "\n") {
				if lipgloss.Width(l) > sz[0] {
					t.Errorf("%s %v: %q too wide", name, sz, l)
				}
			}
			if sz[0] == 59 && !strings.Contains(got, "terminal too small: 59×19") {
				t.Errorf("%s: no too-small text", name)
			}
		}
		if text(m) != first {
			t.Errorf("%s changed over a resize:\n%s\n---\n%s", name, first, text(m))
		}
	}
}

func TestHostileOutputIsSanitised(t *testing.T) {
	m := goldenModel(t, [2]int{80, 24})
	src(m).answer(peekArgs(m, "NF-240"), ok("\x1b[2Jfine\n"+strings.Repeat("y", 300)+"\n"))
	settle(m, openIssue(t, m, "NF-240"), 3)
	if strings.Contains(m.View().Content, "\x1b[2J") {
		t.Error("an escape reached the frame")
	}
	for _, l := range strings.Split(text(m), "\n") {
		if lipgloss.Width(l) > 80 {
			t.Errorf("%q too wide", l)
		}
	}
}

func TestIssueGolden(t *testing.T) {
	for _, sz := range [][2]int{{59, 19}, {80, 24}, {140, 40}} {
		m := goldenModel(t, [2]int{140, 40})
		openIssue(t, m, "NF-240")
		m.Update(sizeMsg(sz))
		m.Update(outputMsg{gen: m.outGen, title: "session", lines: []string{
			"\u276f /review", "reading the diff of src/flocc/poller.py", "ran just check: 2 failures",
			"waiting for the owner to answer in Linear",
		}})
		themetest.Golden(t, fmt.Sprintf("issue-%dx%d", sz[0], sz[1]), text(m))
	}
}
