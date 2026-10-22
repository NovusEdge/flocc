package dash

import (
	"fmt"
	"slices"
	"strings"

	kb "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
)

// pagerState is the full-screen output. The viewport is driven by its methods alone: its default
// key map binds f, space, b, d, u, h and l, which would page as well as follow, and make typing a
// search scroll.
type pagerState struct {
	vp     viewport.Model
	from   screen // the screen esc returns to
	lines  []string
	follow bool
	loaded bool // the 2000-line fetch has answered once
	search string
	note   string
}

func (m *Model) openPager() tea.Cmd {
	m.pager = pagerState{from: m.screen, vp: viewport.New()}
	m.screen = pagerScreen
	m.outGen++
	m.sizePager()
	m.pager.setLines(m.out.lines)
	m.pager.vp.GotoBottom()
	return m.outputFor(m.pager.from, pagerLines)
}

// sizePager fits the viewport inside the pane.
func (m *Model) sizePager() {
	if m.screen != pagerScreen {
		return
	}
	iw, _ := theme.Inner(m.w, m.h)
	atBottom := m.pager.vp.AtBottom()
	m.pager.vp.SetWidth(max(iw-4, 1))
	m.pager.vp.SetHeight(max(m.bodyHeight()-2, 1))
	if atBottom {
		m.pager.vp.GotoBottom()
	}
}

func (p *pagerState) setLines(lines []string) {
	p.lines = lines
	p.vp.SetContentLines(slices.Clone(lines))
}

// load takes a fetch. The first one lands at the bottom, as does each while following.
func (p *pagerState) load(msg outputMsg) {
	lines := msg.lines
	if len(lines) == 0 && msg.note != "" {
		lines = []string{msg.note}
	}
	p.setLines(lines)
	if !p.loaded || p.follow {
		p.vp.GotoBottom()
	}
	p.loaded = true
}

// find moves to the next line holding the search text, wrapping at the end; from is the line at
// the top of the view, or the one after it for n.
func (p *pagerState) find(after bool) {
	if p.search == "" || len(p.lines) == 0 {
		return
	}
	needle, start := strings.ToLower(p.search), p.vp.YOffset()
	if after {
		start++
	}
	for i := range p.lines {
		j := (start + i) % len(p.lines)
		if strings.Contains(strings.ToLower(p.lines[j]), needle) {
			p.vp.SetYOffset(j)
			return
		}
	}
	p.note = "no match: " + saved.Sanitize(p.search)
}

func (m *Model) pagerKey(k string) (tea.Model, tea.Cmd) {
	p := &m.pager
	p.note = ""
	switch k {
	case "j", "down":
		p.vp.ScrollDown(1)
	case "k", "up":
		p.vp.ScrollUp(1)
	case "pgdown":
		p.vp.PageDown()
	case "pgup":
		p.vp.PageUp()
	case "g":
		p.vp.GotoTop()
	case "G":
		p.vp.GotoBottom()
	case "/":
		p.search, m.editing = "", searchEdit
	case "n":
		p.find(true)
	case "f":
		p.follow = !p.follow
		if p.follow {
			p.vp.GotoBottom()
			m.outGen++
			return m, m.outputFor(p.from, pagerLines)
		}
	case "esc":
		m.leave(p.from)
		return m, m.outputFor(p.from, m.outRows())
	}
	return m, nil
}

func (m *Model) pagerBar() []kb.Binding {
	t := m.th
	return []kb.Binding{
		t.Key(true, "↑↓", "scroll", "up", "down", "j", "k"),
		t.Key(true, "pgup/pgdn", "page", "pgup", "pgdown"),
		t.Key(true, "g/G", "top/bottom", "g", "G"),
		t.Key(true, "/", "search"),
		t.Key(m.pager.search != "", "n", "next"),
		t.Key(true, "f", "follow"),
		t.Key(true, "esc", "back"),
		t.Key(true, "?", "keys"),
	}
}

func (m *Model) pagerView() string {
	iw, _ := theme.Inner(m.w, m.h)
	what := m.out.title
	if what == "" {
		what = "session"
	}
	title := saved.Sanitize(m.cur[1]) + " " + what
	if m.pager.from == hostScreen {
		title = saved.Sanitize(m.cur[0]) + " log"
	}
	hint := fmt.Sprintf("%.0f%%", m.pager.vp.ScrollPercent()*100)
	if m.pager.follow {
		hint = "following"
	}
	return m.th.Pane(title, hint, m.pager.vp.View(), iw, m.bodyHeight())
}
