package ui

import (
	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"
	"charm.land/lipgloss/v2"
)

// Theme is the one look of every form flocc builds: the terminal's own 16 colours, so the forms
// follow the owner's terminal theme. huh's ThemeBase16 puts magenta on the focused button and the
// cursor, which many terminal themes render purple, so those are cyan here.
func Theme(isDark bool) *huh.Styles {
	t := huh.ThemeBase16(isDark)
	f := &t.Focused
	f.Base = f.Base.BorderForeground(lipgloss.Cyan)
	f.Card = f.Base
	f.Title = f.Title.UnsetForeground().Bold(true)
	f.NoteTitle = f.NoteTitle.UnsetForeground().Bold(true)
	for _, s := range []*lipgloss.Style{&f.SelectSelector, &f.NextIndicator, &f.PrevIndicator, &f.MultiSelectSelector, &f.TextInput.Prompt} {
		*s = s.Foreground(lipgloss.Cyan)
	}
	f.FocusedButton = f.FocusedButton.Foreground(lipgloss.Black).Background(lipgloss.Cyan)
	f.TextInput.Cursor = f.TextInput.Cursor.Foreground(lipgloss.Cyan)

	// ThemeBase16 copied Focused into Blurred before the changes above, so the blurred field
	// still has the magenta button and cursor.
	t.Blurred.FocusedButton = f.FocusedButton
	t.Blurred.TextInput.Cursor = f.TextInput.Cursor
	t.Group.Title = f.Title
	return t
}

// Form is huh.NewForm in flocc's theme.
func Form(groups ...*huh.Group) *huh.Form {
	return huh.NewForm(groups...).WithTheme(huh.ThemeFunc(Theme))
}

// SpinnerTheme gives the spinner the same palette as Theme.
func SpinnerTheme(bool) *spinner.Styles {
	return &spinner.Styles{
		Spinner: lipgloss.NewStyle().Foreground(lipgloss.Cyan),
		Title:   lipgloss.NewStyle(),
	}
}
