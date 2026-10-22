package dash

import (
	"fmt"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/ui/theme/themetest"
)

func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

// pagerOn opens NF-240's pager over n lines and lets its fetch land.
func pagerOn(t *testing.T, n int) *Model {
	t.Helper()
	m := goldenModel(t, [2]int{140, 40})
	src(m).answer("peek NF-240 -n 2000", ok(numbered(n)))
	settle(m, openIssue(t, m, "NF-240"), 3)
	settle(m, key(m, "L"), 3)
	if m.screen != pagerScreen {
		t.Fatalf("L opened screen %v", m.screen)
	}
	return m
}

func TestPagerKeys(t *testing.T) {
	m := pagerOn(t, 200)
	if !strings.Contains(text(m), "line 200") || strings.Contains(text(m), "line 1\n") {
		t.Fatalf("the pager did not open at the bottom:\n%s", text(m))
	}
	if !strings.Contains(text(m), "NF-240 session") {
		t.Errorf("no title:\n%s", text(m))
	}
	top := m.pager.vp.YOffset()
	key(m, "k")
	if m.pager.vp.YOffset() != top-1 {
		t.Errorf("k: offset %d, was %d", m.pager.vp.YOffset(), top)
	}
	key(m, "g")
	if m.pager.vp.YOffset() != 0 || !strings.Contains(text(m), "line 1 ") && !strings.Contains(text(m), "line 1\n") {
		t.Errorf("g: offset %d", m.pager.vp.YOffset())
	}
	key(m, "G")
	if !m.pager.vp.AtBottom() {
		t.Error("G did not reach the bottom")
	}
	key(m, "pgup")
	page := top - m.pager.vp.YOffset()
	if page < 10 {
		t.Errorf("pgup moved %d", page)
	}
	key(m, "pgdown")
	if !m.pager.vp.AtBottom() {
		t.Error("pgdown did not return")
	}

	key(m, "g")
	key(m, "/")
	typed(m, "line 42")
	if !strings.Contains(text(m), "/line 42") {
		t.Errorf("search not on the status line:\n%s", text(m))
	}
	key(m, "enter")
	if !strings.Contains(text(m), "line 42 ") && !strings.Contains(strings.ReplaceAll(text(m), "│", " "), "line 42  ") {
		t.Errorf("line 42 not in view:\n%s", text(m))
	}
	if m.pager.vp.YOffset() != 41 {
		t.Errorf("line 42 is at offset 41, got %d", m.pager.vp.YOffset())
	}
	key(m, "g")
	key(m, "/")
	typed(m, "line 15")
	key(m, "enter")
	if m.pager.vp.YOffset() != 14 {
		t.Errorf("line 15 is at offset 14, got %d", m.pager.vp.YOffset())
	}
	key(m, "n")
	if m.pager.vp.YOffset() != 149 {
		t.Errorf("n should reach line 150, got offset %d", m.pager.vp.YOffset())
	}
	key(m, "/")
	typed(m, "no such text")
	key(m, "enter")
	if !strings.Contains(text(m), "no match: no such text") {
		t.Errorf("no note:\n%s", text(m))
	}
}

func TestPagerSearchTypingIsText(t *testing.T) {
	m := pagerOn(t, 50)
	key(m, "/")
	typed(m, "fgjk")
	if m.pager.search != "fgjk" || m.pager.follow {
		t.Errorf("search %q follow %v", m.pager.search, m.pager.follow)
	}
	key(m, "esc")
	if m.screen != pagerScreen || m.pager.search != "" {
		t.Errorf("esc while typing: screen %v search %q", m.screen, m.pager.search)
	}
}

func TestPagerFollow(t *testing.T) {
	m := pagerOn(t, 200)
	key(m, "g")
	cmd := key(m, "f")
	if !m.pager.follow || !strings.Contains(text(m), "following") {
		t.Fatalf("f did not follow:\n%s", text(m))
	}
	if cmd == nil {
		t.Fatal("following fetched nothing")
	}
	_, next := m.Update(outputMsg{gen: m.outGen, title: "session", lines: strings.Split(strings.TrimSpace(numbered(220)), "\n")})
	if next == nil {
		t.Error("following did not tick again")
	}
	if !m.pager.vp.AtBottom() || !strings.Contains(text(m), "line 220") {
		t.Errorf("the bottom is not in view:\n%s", text(m))
	}
	key(m, "f")
	if m.pager.follow {
		t.Error("f again did not stop following")
	}
	if _, next := m.Update(outputMsg{gen: m.outGen, lines: []string{"x"}}); next != nil {
		t.Error("a pager not following ticked")
	}
}

func TestPagerEscRestartsTheIssueOutput(t *testing.T) {
	m := pagerOn(t, 20)
	gen := m.outGen
	cmd := key(m, "esc")
	if m.screen != issueScreen || m.outGen == gen || cmd == nil {
		t.Fatalf("screen %v gen %d->%d cmd %v", m.screen, gen, m.outGen, cmd)
	}
	if _, c := m.Update(outputTick{gen: gen}); c != nil {
		t.Error("a pager tick survived the exit")
	}
}

func TestPagerGolden(t *testing.T) {
	for _, sz := range [][2]int{{59, 19}, {80, 24}, {140, 40}} {
		m := pagerOn(t, 200)
		m.Update(sizeMsg(sz))
		themetest.Golden(t, fmt.Sprintf("pager-%dx%d", sz[0], sz[1]), text(m))
	}
}
