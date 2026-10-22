package theme

import (
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
)

// Key pre-renders its help text so the bar can dim one key without help.Model
// styling them all alike. The binding stays enabled even when off: help skips
// disabled bindings, which would make the bar jump as the selection moves.
func (t Theme) Key(on bool, label, desc string, keys ...string) key.Binding {
	ks, ds := t.Accent, t.Dim
	if !on {
		ks, ds = t.Off, t.Off
	}
	if len(keys) == 0 {
		// A binding with no keys counts as disabled, and help skips those.
		keys = []string{label}
	}
	return key.NewBinding(key.WithKeys(keys...), key.WithHelp(ks.Render(label), ds.Render(desc)))
}

func (t Theme) helpModel() help.Model {
	h := help.New()
	blank := lipgloss.NewStyle()
	h.Styles = help.Styles{
		Ellipsis: t.Dim, ShortKey: blank, ShortDesc: blank, ShortSeparator: t.Dim,
		FullKey: blank, FullDesc: blank, FullSeparator: t.Dim,
	}
	h.ShortSeparator = Sep
	return h
}

// KeyBar packs bindings into lines no wider than width. help.Model drops what
// does not fit, and a cut-off key is one nobody can discover.
func (t Theme) KeyBar(bindings []key.Binding, width int) string {
	h := t.helpModel()
	var lines []string
	var row []key.Binding
	for _, b := range bindings {
		if len(row) > 0 && lipgloss.Width(h.ShortHelpView(append(row[:len(row):len(row)], b))) > width {
			lines = append(lines, h.ShortHelpView(row))
			row = nil
		}
		row = append(row, b)
	}
	if len(row) > 0 {
		lines = append(lines, h.ShortHelpView(row))
	}
	return strings.Join(lines, "\n")
}

// KeyHelp is the "?" panel; whole columns wrap to the next row.
func (t Theme) KeyHelp(groups [][]key.Binding, width int) string {
	h := t.helpModel()
	inner := max(width-4, 1)
	var lines []string
	var row [][]key.Binding
	for _, g := range groups {
		if len(row) > 0 && lipgloss.Width(h.FullHelpView(append(row[:len(row):len(row)], g))) > inner {
			lines = append(lines, h.FullHelpView(row))
			row = nil
		}
		row = append(row, g)
	}
	if len(row) > 0 {
		lines = append(lines, h.FullHelpView(row))
	}
	return t.Pane("keys", "", strings.Join(lines, "\n"), width, 0)
}
