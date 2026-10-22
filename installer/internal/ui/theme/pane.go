package theme

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Pane draws a rounded box exactly w wide with title and hint set into the top
// border. Lipgloss has no border titles, so the border is built by hand. With
// h <= 0 the pane is as tall as body.
func (t Theme) Pane(title, hint, body string, w, h int) string {
	w = max(w, 4)
	bd := t.Accent
	inner := w - 4

	titleSeg, hintSeg := "", ""
	if title != "" {
		titleSeg = " " + title + " "
	}
	if hint != "" {
		hintSeg = " " + hint + " "
	}
	// "╭─" + title + fill + hint + "─╮"
	room := w - 4
	if lipgloss.Width(titleSeg)+lipgloss.Width(hintSeg) > room {
		hintSeg = ""
	}
	if lipgloss.Width(titleSeg) > room {
		titleSeg = " " + ansi.Truncate(title, max(room-2, 0), "…") + " "
	}
	fill := max(room-lipgloss.Width(titleSeg)-lipgloss.Width(hintSeg), 0)
	top := bd.Render("╭─") + t.Title.Render(titleSeg) + bd.Render(strings.Repeat("─", fill)) + t.Dim.Render(hintSeg) + bd.Render("─╮")

	lines := strings.Split(strings.ReplaceAll(body, "\t", "    "), "\n")
	if h > 0 {
		rows := max(h-2, 0)
		lines = lines[:min(len(lines), rows)]
		for len(lines) < rows {
			lines = append(lines, "")
		}
	}
	out := make([]string, 0, len(lines)+2)
	out = append(out, top)
	for _, l := range lines {
		l = ansi.Truncate(l, inner, "…")
		out = append(out, bd.Render("│")+" "+l+strings.Repeat(" ", max(inner-lipgloss.Width(l), 0))+" "+bd.Render("│"))
	}
	out = append(out, bd.Render("╰"+strings.Repeat("─", w-2)+"╯"))
	return strings.Join(out, "\n")
}
