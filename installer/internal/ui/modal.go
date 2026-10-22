package ui

import (
	"regexp"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
)

const modalWidth = 64

var sgrRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// overlay draws box centred over frame, which is dimmed first. The frame loses its colours: an
// SGR reset inside a coloured cell would end the faint attribute mid-row.
func overlay(frame, box string, w, h int) string {
	dimmed := lipgloss.NewStyle().Faint(true).Render(sgrRE.ReplaceAllString(frame, ""))
	bw, bh := lipgloss.Size(box)
	top := lipgloss.NewLayer(dimmed)
	over := lipgloss.NewLayer(box).X(max((w-bw)/2, 0)).Y(max((h-bh)/2, 0)).Z(1)
	return lipgloss.NewCompositor(top, over).Render()
}

// modalBoxIn is the open form in a rounded box with the border in th's accent. huh draws the
// question's title and facts itself.
func modalBoxIn(th theme.Theme, form string, w int) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(th.Accent.GetForeground()).
		Padding(0, 1).
		Width(min(modalWidth, max(w-8, 1))).
		Render(form)
}

// advance gives msg to the open form f and, once the form has ended, the answer it produced.
func advance(f *huh.Form, result func() any, msg tea.Msg) (cmd tea.Cmd, reply promptReply, ended bool) {
	_, cmd = f.Update(msg)
	switch f.State {
	case huh.StateCompleted:
		return cmd, promptReply{v: result()}, true
	case huh.StateAborted:
		return cmd, promptReply{err: huh.ErrUserAborted}, true
	}
	return cmd, promptReply{}, false
}

// stopForm asks whether to cancel the running step.
func stopForm(dark *bool, title string) (*huh.Form, func() any) {
	var stop bool
	f := huh.NewConfirm().
		Title("Stop " + title + "?").
		Description("The running step is cancelled.").
		Affirmative("Stop").
		Negative("Keep going").
		Value(&stop)
	return formIn(dark, huh.NewGroup(f)), func() any { return stop }
}
