package dash

import (
	"strings"

	kb "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/NovusEdge/flocc/installer/internal/cardinal"
	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/ui"
	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
)

func (m *Model) cardinalKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "esc", "left", "h":
		m.screen = fleetScreen
	case "up", "k":
		m.cardSel = max(m.cardSel-1, 0)
	case "down", "j":
		m.cardSel = max(min(m.cardSel+1, len(m.cards)-1), 0)
	case "c":
		if len(m.cards) == 0 {
			return m, nil
		}
		return m, m.handoff("cardinal", "cardinal", "attach", "--user", m.cards[m.cardSel].Saved.User)
	}
	return m, nil
}

func (m *Model) cardinalBar() []kb.Binding {
	t := m.th
	return []kb.Binding{
		t.Key(len(m.cards) > 1, "↑↓", "select", "up", "down", "j", "k"),
		t.Key(len(m.cards) > 0, "c", "attach"),
		t.Key(true, "esc", "back", "esc", "left", "h"),
		t.Key(true, "?", "keys"),
	}
}

// cardinalView is one pane per saved install: its status table, or why there is none.
func (m *Model) cardinalView() string {
	iw, _ := theme.Inner(m.w, m.h)
	if len(m.cards) == 0 {
		return m.th.Pane("cardinal", "", m.th.Dim.Render("no cardinal installs"), iw, 0)
	}
	var panes []string
	for i, c := range m.cards {
		title := "cardinal"
		if len(m.cards) > 1 {
			title = saved.Sanitize(c.Saved.User + "@" + c.Saved.Machine)
			if i == m.cardSel {
				title = theme.Cursor + " " + title
			}
		}
		body := m.th.Err.Render(saved.Sanitize(c.Err))
		if c.Status != nil {
			t := cardinal.StatusTable(*c.Status)
			t.Title = ""
			lines := strings.Split(ui.TableText(t), "\n")
			for i, l := range lines {
				lines[i] = saved.Sanitize(l)
			}
			body = strings.Join(lines, "\n")
		}
		panes = append(panes, m.th.Pane(title, "", body, iw, 0))
	}
	return strings.Join(panes, "\n")
}
