package ui

import (
	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"
	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
)

// Theme is the one look of every form flocc builds: the shared palette's accent. huh's
// ThemeBase16 puts magenta on the focused button and the cursor, which many terminal themes
// render purple, so those take the accent here.
func Theme(isDark bool) *huh.Styles {
	accent := theme.For(isDark).Accent
	onAccent := lipgloss.Black
	if !isDark {
		onAccent = lipgloss.White
	}
	t := huh.ThemeBase16(isDark)
	f := &t.Focused
	f.Base = f.Base.BorderForeground(accent)
	f.Card = f.Base
	f.Title = f.Title.UnsetForeground().Bold(true)
	f.NoteTitle = f.NoteTitle.UnsetForeground().Bold(true)
	for _, s := range []*lipgloss.Style{&f.SelectSelector, &f.NextIndicator, &f.PrevIndicator, &f.MultiSelectSelector, &f.TextInput.Prompt} {
		*s = s.Foreground(accent)
	}
	f.FocusedButton = f.FocusedButton.Foreground(onAccent).Background(accent)
	f.TextInput.Cursor = f.TextInput.Cursor.Foreground(accent)

	// ThemeBase16 copied Focused into Blurred before the changes above, so the blurred field
	// still has the magenta button and cursor.
	t.Blurred.FocusedButton = f.FocusedButton
	t.Blurred.TextInput.Cursor = f.TextInput.Cursor
	t.Group.Title = f.Title
	return t
}

// themeIn is Theme for the background *dark reports, read on every draw. huh never asks the
// terminal, and its fields start with hasDarkBg=false, so a form left to its own flag would draw
// the light colours on a dark terminal. The flag is shared with the model because a field keeps
// the first theme it is given: Form.WithTheme on an open form changes nothing.
func themeIn(dark *bool) huh.ThemeFunc {
	return func(bool) *huh.Styles { return Theme(*dark) }
}

// withDark switches th to the palette for the reported background. A Plain theme stays plain but
// still records the background, which the forms it opens read.
func withDark(th theme.Theme, dark bool) theme.Theme {
	th = th.WithDark(dark)
	th.Dark = dark
	return th
}

// Form is huh.NewForm in flocc's theme, for a dark terminal.
func Form(groups ...*huh.Group) *huh.Form { return formIn(new(true), groups...) }

func formIn(dark *bool, groups ...*huh.Group) *huh.Form {
	return huh.NewForm(groups...).WithTheme(themeIn(dark))
}

// SpinnerTheme gives the spinner the same palette as Theme.
func SpinnerTheme(isDark bool) *spinner.Styles {
	return &spinner.Styles{
		Spinner: lipgloss.NewStyle().Foreground(theme.For(isDark).Accent),
		Title:   lipgloss.NewStyle(),
	}
}
