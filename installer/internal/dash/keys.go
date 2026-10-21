package dash

import (
	tea "charm.land/bubbletea/v2"

	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// execDone arrives when a handoff's child has exited; the dashboard polls again at once.
type execDone struct {
	what string
	err  error
}

func (m *Model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.notice = ""
	k := msg.String()
	switch k {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.showKeys = !m.showKeys
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "c":
		return m, m.handoff("cardinal", "cardinal", "attach")
	case "a", "l", "r":
		r, ok := m.selected()
		if !ok {
			return m, nil
		}
		switch k {
		case "a":
			return m, m.handoff("attach "+r.claim.Issue, "-H", r.host, "attach", r.claim.Issue)
		case "l":
			return m, m.handoff("logs "+r.claim.Issue, "-H", r.host, "logs", r.claim.Issue)
		}
		m.confirm, m.confirmN = ui.NewConfirm("Release "+r.claim.Issue+"?", "Its session ends and it goes back to Todo.", "Release", "Keep"), r
		return m, m.confirm.Init()
	}
	return m, nil
}

func (m *Model) move(by int) {
	m.sel = max(min(m.sel+by, len(m.rows)-1), 0)
	m.remember()
}

// handoff gives the terminal to a child flocc and polls again when it returns. what names the
// child in the error line.
func (m *Model) handoff(what string, args ...string) tea.Cmd {
	return tea.ExecProcess(m.command(args...), func(err error) tea.Msg { return execDone{what, err} })
}
