package dash

import (
	"slices"

	kb "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// execDone arrives when a handoff's child has exited; the dashboard polls again at once.
type execDone struct {
	what string
	err  error
}

type editKind int

const (
	notEditing editKind = iota
	filterEdit
	searchEdit // the pager's
)

// key sends a key to the first of: the open question, the text being typed, the screen.
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
	if m.editing != notEditing {
		return m, m.edit(msg)
	}
	if k == "?" {
		m.showKeys = !m.showKeys
		return m, nil
	}
	switch m.screen {
	case issueScreen:
		return m.issueKey(k)
	case pagerScreen:
		return m.pagerKey(k)
	}
	return m.fleetKey(k)
}

// edit takes a key into the text being typed: enter keeps it, esc drops it.
func (m *Model) edit(msg tea.KeyPressMsg) tea.Cmd {
	field := &m.filter
	if m.editing == searchEdit {
		field = &m.pager.search
	}
	switch msg.String() {
	case "enter":
		if m.editing == searchEdit {
			m.pager.find(false)
		}
		m.editing = notEditing
	case "esc":
		*field, m.editing = "", notEditing
	case "backspace":
		if r := []rune(*field); len(r) > 0 {
			*field = string(r[:len(r)-1])
		}
	default:
		*field += msg.Text
	}
	m.reselect()
	return nil
}

func (m *Model) fleetKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "q":
		return m, tea.Quit
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "tab":
		m.cycle()
	case "/":
		m.editing = filterEdit
	case "esc":
		m.filter = ""
		m.reselect()
	case "right", "l", "enter":
		return m, m.open()
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
	return ok && m.focus == issuesPane
}

// opens holds when the focused pane has a row to open.
func (m *Model) opens() bool {
	switch m.focus {
	case hostsPane:
		return len(m.hosts) > 0
	case cardinalPane:
		return len(m.cards) > 0
	}
	_, ok := m.selected()
	return ok
}

// panes are the focus order tab cycles; the cardinal pane is there only with an install.
func (m *Model) panes() []focus {
	if len(m.cards) == 0 {
		return []focus{hostsPane, issuesPane}
	}
	return []focus{hostsPane, cardinalPane, issuesPane}
}

func (m *Model) cycle() {
	p := m.panes()
	m.focus = p[(slices.Index(p, m.focus)+1)%len(p)]
}

func (m *Model) move(by int) {
	clamp := func(i, n int) int { return max(min(i+by, n-1), 0) }
	switch m.focus {
	case hostsPane:
		m.hostSel = clamp(m.hostSel, len(m.hosts))
	case cardinalPane:
		m.cardSel = clamp(m.cardSel, len(m.cards))
	default:
		m.sel = clamp(m.sel, len(m.visible()))
		m.remember()
	}
}

// bar is the current screen's short bindings; a key that does nothing here is dimmed, not hidden.
func (m *Model) bar() []kb.Binding {
	switch m.screen {
	case issueScreen:
		return m.issueBar()
	case pagerScreen:
		return m.pagerBar()
	}
	t := m.th
	return []kb.Binding{
		t.Key(true, "↑↓", "select", "up", "down"),
		t.Key(true, "tab", "hosts/issues"),
		t.Key(m.opens(), "→", "open", "right"),
		t.Key(m.issueActs(), "a", "attach"),
		t.Key(m.issueActs(), "r", "release"),
		t.Key(true, "/", "filter"),
		t.Key(true, "?", "keys"),
		t.Key(true, "q", "quit"),
	}
}

// fullKeys are the "?" groups.
func (m *Model) fullKeys() [][]kb.Binding {
	if m.screen != fleetScreen {
		return [][]kb.Binding{m.bar()}
	}
	t := m.th
	return [][]kb.Binding{
		{t.Key(true, "↑↓", "select", "up", "down"), t.Key(true, "tab", "hosts/issues"), t.Key(m.opens(), "→", "open", "right")},
		{t.Key(m.issueActs(), "a", "attach"), t.Key(m.issueActs(), "r", "release"), t.Key(true, "/", "filter")},
		{t.Key(true, "esc", "clear filter"), t.Key(true, "?", "keys"), t.Key(true, "q", "quit")},
	}
}

// handoff gives the terminal to a child flocc and polls again when it returns. what names the
// child in the error toast.
func (m *Model) handoff(what string, args ...string) tea.Cmd {
	return tea.ExecProcess(m.command(args...), func(err error) tea.Msg { return execDone{what, err} })
}
