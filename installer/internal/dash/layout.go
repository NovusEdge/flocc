package dash

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
)

// keyPanel is the "?" panel's rows, none while it is closed.
func (m *Model) keyPanel(iw int) []string {
	if !m.showKeys {
		return nil
	}
	return strings.Split(m.th.KeyHelp(m.fullKeys(), iw), "\n")
}

// bodyHeight is the inner height less the header, the status line, the key bar (which wraps) and
// the "?" panel, so every pane of the body keeps its border while the panel is open.
func (m *Model) bodyHeight() int {
	iw, ih := theme.Inner(m.w, m.h)
	bar := strings.Count(m.th.KeyBar(m.bar(), iw), "\n") + 1
	return max(ih-2-bar-len(m.keyPanel(iw)), 1)
}

// chrome is the header, body, status line and key bar in the margin, with the toast over it. body
// is cut or padded to bodyH rows, and the "?" panel sits under it.
func (m *Model) chrome(body string, bodyH int) string {
	iw, _ := theme.Inner(m.w, m.h)
	lines := strings.Split(body, "\n")
	if len(lines) > bodyH {
		lines = lines[:bodyH]
	}
	for len(lines) < bodyH {
		lines = append(lines, "")
	}
	lines = append(lines, m.keyPanel(iw)...)
	out := append([]string{m.header(iw)}, lines...)
	out = append(out, m.statusLine(), m.th.KeyBar(m.bar(), iw))
	return m.th.Toast(theme.Screen(strings.Join(out, "\n"), m.w, m.h), m.toasts, m.w)
}

func (m *Model) header(iw int) string {
	left := m.th.Title.Render("flocc") + " " + steps.Plural(len(m.hosts), "host") + theme.Sep + steps.Plural(len(m.rows), "issue")
	right := m.th.Dim.Render("loading")
	switch {
	case m.fetching:
		right = m.spin.View() + m.th.Dim.Render(" polling")
	case m.loaded:
		right = m.th.Dim.Render("refreshed " + ship.Age(max(m.now().Sub(m.fetchedAt).Seconds(), 0)) + " ago")
	}
	return left + strings.Repeat(" ", max(iw-lipgloss.Width(left)-lipgloss.Width(right), 1)) + right
}

// statusLine is the one row above the key bar that nothing else moves: the open question, else the
// first note.
func (m *Model) statusLine() string {
	switch {
	case m.ask != nil:
		return m.th.Warn.Render(m.ask.text)
	case m.editing == filterEdit:
		return "/" + m.filter + "▏"
	case m.editing == searchEdit:
		return "/" + m.pager.search + "▏"
	case m.screen == pagerScreen && m.pager.note != "":
		return m.th.Dim.Render(m.pager.note)
	case m.titleNote != "":
		return m.th.Dim.Render(m.titleNote)
	case len(m.notes) > 0:
		return m.th.Dim.Render(saved.Sanitize(m.notes[0]))
	}
	return ""
}
