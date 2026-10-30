package dash

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	kb "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/NovusEdge/flocc/installer/internal/safe"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
)

// factsH is the facts pane of the issue screen: four rows and the border.
const factsH = 6

// phases is the order the host runs them (src/flocc/supervisor.py), not gate.py's BUDGETS order.
var phases = []string{"test", "impl", "review", "fix"}

// open moves into the focused row's detail screen.
func (m *Model) open() tea.Cmd {
	switch m.focus {
	case issuesPane:
		if r, ok := m.selected(); ok {
			m.cur = [2]string{r.host, r.claim.Issue}
			return m.enter(issueScreen)
		}
	case hostsPane:
		return m.openHost()
	}
	return nil
}

// curRow is the open issue's claim in the latest poll; ok is false once it was released.
func (m *Model) curRow() (row, bool) {
	for _, r := range m.rows {
		if [2]string{r.host, r.claim.Issue} == m.cur {
			return r, true
		}
	}
	return row{}, false
}

// outRows is how many lines the open screen's output pane shows.
func (m *Model) outRows() int {
	if m.screen == hostScreen {
		_, topH, _, _ := m.hostSplit()
		return max(m.bodyHeight()-topH-2, 1)
	}
	return max(m.bodyHeight()-factsH-2, 1)
}

func (m *Model) issueKey(k string) (tea.Model, tea.Cmd) {
	r, live := m.curRow()
	switch k {
	case "esc", "left", "h":
		m.leave(fleetScreen)
	case "a":
		if live {
			return m, m.handoff("attach "+r.claim.Issue, "-H", r.host, "attach", r.claim.Issue)
		}
	case "r":
		if live {
			m.askRelease(r)
		}
	case "o":
		if url, open := m.titles[m.cur[1]].URL, m.opts.Open; url != "" && open != nil {
			return m, func() tea.Msg { open(url); return nil }
		}
	case "L":
		return m, m.openPager()
	}
	return m, nil
}

func (m *Model) issueBar() []kb.Binding {
	t := m.th
	_, live := m.curRow()
	return []kb.Binding{
		t.Key(live, "a", "attach"),
		t.Key(live, "r", "release"),
		t.Key(m.titles[m.cur[1]].URL != "" && m.opts.Open != nil, "o", "open"),
		t.Key(true, "L", "pager"),
		t.Key(true, "esc", "back", "esc", "left", "h"),
		t.Key(true, "?", "keys"),
	}
}

func (m *Model) issueView() string {
	iw, _ := theme.Inner(m.w, m.h)
	key := safe.Sanitize(m.cur[1])
	title := key
	if t := m.titles[m.cur[1]].Title; t != "" {
		title += theme.Sep + t
	}
	rows := []string{m.th.Warn.Render(key + " is no longer claimed")}
	if r, ok := m.curRow(); ok {
		rows = m.issueFacts(r)
	}
	hint := ""
	if m.titles[m.cur[1]].URL != "" {
		hint = "o open in Linear"
	}
	facts := m.th.Pane(title, hint, strings.Join(rows, "\n"), iw, factsH)
	return facts + "\n" + m.outputPane("session", "L full screen", iw, m.bodyHeight()-factsH)
}

// outputPane is the output of the open screen, its last lines that fit, in a pane h rows tall.
// title is what the pane is called until a fetch names its source.
func (m *Model) outputPane(title, hint string, w, h int) string {
	if m.out.title != "" && m.screen == issueScreen {
		title = m.out.title
	}
	lines := m.out.lines
	if len(lines) == 0 {
		note, st := m.out.note, m.th.Dim
		switch {
		case note == "":
			note = cmp.Or(m.out.empty, "nothing yet")
		case note != loading && note != shipToSee:
			st = m.th.Warn
		}
		lines = []string{st.Render(note)}
	}
	return m.th.Pane(title, hint, strings.Join(lines[max(len(lines)-max(h-2, 0), 0):], "\n"), w, h)
}

func (m *Model) issueFacts(r row) []string {
	c := r.claim
	state, updated := safe.Sanitize(c.State), ""
	if c.UpdatedAge != nil {
		state += " " + ship.Age(*c.UpdatedAge)
		updated = theme.Sep + "updated " + ship.Age(*c.UpdatedAge) + " ago"
	}
	session, ctx := "none", "-"
	if c.TmuxAlive {
		session = "live"
	}
	if c.ContextPct != nil {
		ctx = fmt.Sprintf("%.0f%%", *c.ContextPct)
	}
	where := safe.Sanitize(r.host) + theme.Sep + "started " + ship.Age(c.StartedAge) + " ago" + updated
	if r.stale {
		where += m.th.Dim.Render(" (stale)")
	}
	return []string{
		m.label(7, "phase", m.phaseLine(c)),
		m.label(7, "state", state),
		m.label(7, "session", fmt.Sprintf("%s%scontext %s", session, theme.Sep, ctx)),
		m.label(7, "host", where),
	}
}

func (m *Model) label(w int, l, v string) string { return m.th.Dim.Render(pad(l, w)) + "  " + v }

func (m *Model) phaseLine(c ship.Claim) string {
	glyph := stateGlyph(m.th, m.spin.View(), c.State)
	i := slices.Index(phases, c.Phase)
	if i < 0 {
		return safe.Sanitize(c.Phase) + " " + glyph
	}
	parts := make([]string, len(phases))
	for j, p := range phases {
		switch {
		case j < i:
			parts[j] = p + " " + m.th.Up.Render(theme.DoneMark)
		case j == i:
			parts[j] = p + " " + glyph
		default:
			parts[j] = m.th.Dim.Render(p)
		}
	}
	return strings.Join(parts, " → ")
}
