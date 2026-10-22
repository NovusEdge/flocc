package dash

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
)

type titlesMsg struct {
	keys []string
	got  map[string]Title
	err  error
}

// askTitles asks Options.Titles for the keys no earlier poll asked about, in one call.
func (m *Model) askTitles() tea.Cmd {
	if m.opts.Titles == nil {
		return nil
	}
	var keys []string
	for _, r := range m.rows {
		k := r.claim.Issue
		if _, known := m.titles[k]; !known && !m.asked[k] && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	slices.Sort(keys)
	for _, k := range keys {
		m.asked[k] = true
	}
	ask, ctx := m.opts.Titles, m.ctx
	return func() tea.Msg {
		got, err := ask(ctx, keys)
		return titlesMsg{keys, got, err}
	}
}

// gotTitles caches every key asked, a miss as a zero Title so it is not asked for again. An error
// caches nothing: the next poll asks again.
func (m *Model) gotTitles(msg titlesMsg) {
	for _, k := range msg.keys {
		delete(m.asked, k)
	}
	if msg.err != nil {
		m.titleNote = "titles unavailable: " + saved.Sanitize(msg.err.Error())
		return
	}
	m.titleNote = ""
	for _, k := range msg.keys {
		t := msg.got[k]
		m.titles[k] = Title{Title: saved.Sanitize(t.Title), URL: saved.Sanitize(t.URL)}
	}
	m.reselect()
}
