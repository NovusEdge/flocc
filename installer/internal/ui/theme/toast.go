package theme

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Toasts holds at most one message. Every Show bumps the generation, so the
// timer of a replaced toast cannot clear the newer one early. It is hand-rolled
// because bubbleup re-ticks on every message it sees while an alert is live,
// which multiplies pending ticks with each unrelated message.
type Toasts struct {
	text string
	err  bool
	gen  int
}

type ToastExpired struct{ Gen int }

// Show returns the Cmd that takes the toast down; the caller must return it.
func (ts *Toasts) Show(text string, isErr bool) tea.Cmd {
	if text == "" {
		return nil
	}
	ts.gen++
	ts.text, ts.err = text, isErr
	gen := ts.gen
	return tea.Tick(ToastLife, func(time.Time) tea.Msg { return ToastExpired{Gen: gen} })
}

func (ts *Toasts) Expire(msg ToastExpired) {
	if msg.Gen == ts.gen {
		ts.text = ""
	}
}

func (ts Toasts) Text() string { return ts.text }

// Toast overlays the box on screen, replacing what is under it. The compositor
// trims trailing spaces, so a line's rendered width can shrink.
func (t Theme) Toast(screen string, ts Toasts, w int) string {
	if ts.text == "" {
		return screen
	}
	c := t.Accent
	if ts.err {
		c = t.Err
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(c.GetForeground()).
		Foreground(c.GetForeground()).
		Padding(0, 1).
		Width(max(min(34, w-2*MarginX), 1)).
		Render(ts.text)
	x := max(w-lipgloss.Width(box)-MarginX, 0)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(screen),
		lipgloss.NewLayer(box).X(x).Y(MarginY).Z(1),
	).Render()
}
