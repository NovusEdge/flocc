package dash

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
)

// bodyHeight is the inner height less the header, the status line and the key bar, which wraps.
func (m *Model) bodyHeight() int {
	iw, ih := theme.Inner(m.w, m.h)
	bar := strings.Count(m.th.KeyBar(m.bar(), iw), "\n") + 1
	return max(ih-2-bar, 1)
}

// chrome is the header, body, status line and key bar in the margin, with the toast over it. body
// is cut or padded to bodyH rows; with "?" on, the key panel replaces its bottom rows.
func (m *Model) chrome(body string, bodyH int) string {
	iw, _ := theme.Inner(m.w, m.h)
	lines := strings.Split(body, "\n")
	if len(lines) > bodyH {
		lines = lines[:bodyH]
	}
	for len(lines) < bodyH {
		lines = append(lines, "")
	}
	if m.showKeys {
		help := strings.Split(m.th.KeyHelp(m.fullKeys(), iw), "\n")
		help = help[:min(len(help), bodyH)]
		lines = append(lines[:bodyH-len(help)], help...)
	}
	out := append([]string{m.header(iw)}, lines...)
	out = append(out, m.statusLine(), m.th.KeyBar(m.bar(), iw))
	return m.th.Toast(theme.Screen(strings.Join(out, "\n"), m.w, m.h), m.toasts, m.w)
}

func (m *Model) header(iw int) string {
	left := m.th.Title.Render("flocc") + fmt.Sprintf(" %d hosts%s%d issues", len(m.hosts), theme.Sep, len(m.rows))
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
