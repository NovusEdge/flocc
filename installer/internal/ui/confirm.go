package ui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
)

// Confirm is a yes/no question drawn as a modal over a caller's frame, for Bubble Tea programs
// that are not the session's own, such as the dashboard.
type Confirm struct {
	form *huh.Form
	yes  bool
}

// NewConfirm asks title, with facts under it; the default answer is no.
func NewConfirm(title, facts, yes, no string) *Confirm {
	c := &Confirm{}
	c.form = Form(huh.NewGroup(huh.NewConfirm().
		Title(title).
		Description(facts).
		Affirmative(yes).
		Negative(no).
		Value(&c.yes)))
	return c
}

func (c *Confirm) Init() tea.Cmd { return c.form.Init() }

// Update feeds msg to the form. done is true once the question has been answered or abandoned;
// yes is true only for an explicit yes.
func (c *Confirm) Update(msg tea.Msg) (cmd tea.Cmd, done, yes bool) {
	cmd, reply, ended := advance(c.form, func() any { return c.yes }, msg)
	if !ended {
		return cmd, false, false
	}
	ans, _ := reply.v.(bool)
	return cmd, true, ans
}

// Over draws the question centred on frame, which is dimmed.
func (c *Confirm) Over(frame string, w, h int) string {
	return overlay(frame, modalBox(c.form.View(), w), w, h)
}
