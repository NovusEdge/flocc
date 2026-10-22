package dash

import (
	kb "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// execDone arrives when a handoff's child has exited; the dashboard polls again at once.
type execDone struct {
	what string
	err  error
}

func (m *Model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		return m, tea.Quit
	}
	if m.ask != nil {
		a := m.ask
		m.ask = nil
		if k == "y" {
			return m, a.yes()
		}
		return m, nil
	}
	switch k {
	case "?":
		m.showKeys = !m.showKeys
		return m, nil
	}
	return m.fleetKey(k)
}

func (m *Model) fleetKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "q":
		return m, tea.Quit
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "a", "r":
		if !m.issueActs() {
			return m, nil
		}
		r, _ := m.selected()
		if k == "a" {
			return m, m.handoff("attach "+r.claim.Issue, "-H", r.host, "attach", r.claim.Issue)
		}
		m.ask = &ask{text: "release " + r.claim.Issue + "? y/N", yes: func() tea.Cmd {
			return m.handoff("release "+r.claim.Issue, "-H", r.host, "release", r.claim.Issue)
		}}
	}
	return m, nil
}

// issueActs holds when a and r have an issue to act on.
func (m *Model) issueActs() bool {
	_, ok := m.selected()
	return ok
}

func (m *Model) move(by int) {
	m.sel = max(min(m.sel+by, len(m.rows)-1), 0)
	m.remember()
}

// bar is the current screen's short bindings; a key that does nothing here is dimmed, not hidden.
func (m *Model) bar() []kb.Binding {
	t := m.th
	return []kb.Binding{
		t.Key(true, "↑↓", "select", "up", "down"),
		t.Key(m.issueActs(), "a", "attach"),
		t.Key(m.issueActs(), "r", "release"),
		t.Key(true, "?", "keys"),
		t.Key(true, "q", "quit"),
	}
}

// fullKeys are the "?" groups.
func (m *Model) fullKeys() [][]kb.Binding {
	t := m.th
	return [][]kb.Binding{
		{t.Key(true, "↑↓", "select", "up", "down")},
		{t.Key(m.issueActs(), "a", "attach"), t.Key(m.issueActs(), "r", "release")},
		{t.Key(true, "?", "keys"), t.Key(true, "q", "quit")},
	}
}

// handoff gives the terminal to a child flocc and polls again when it returns. what names the
// child in the error toast.
func (m *Model) handoff(what string, args ...string) tea.Cmd {
	return tea.ExecProcess(m.command(args...), func(err error) tea.Msg { return execDone{what, err} })
}
