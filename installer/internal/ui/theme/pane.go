package theme

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Pane draws a rounded box exactly w wide with title and hint set into the top
// border. Lipgloss has no border titles, so the border is built by hand. With
// h <= 0 the pane is as tall as body; h of 1 is drawn as 2, the least a
// bordered pane can be. Widths below 2 are drawn as 2. Below 4 columns there
// is no room for the side padding, the title or the hint.
func (t Theme) Pane(title, hint, body string, w, h int) string {
	w = max(w, 2)
	bd := t.Accent
	pad := min(1, (w-2)/2)
	inner := w - 2 - 2*pad

	var top string
	if pad == 0 {
		top = bd.Render("╭" + strings.Repeat("─", w-2) + "╮")
	} else {
		oneLine := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")
		title, hint = oneLine.Replace(title), oneLine.Replace(hint)
		titleSeg, hintSeg := "", ""
		if title != "" && inner >= 3 {
			titleSeg = " " + title + " "
		}
		if hint != "" {
			hintSeg = " " + hint + " "
		}
		if lipgloss.Width(titleSeg)+lipgloss.Width(hintSeg) > inner {
			hintSeg = ""
		}
		if lipgloss.Width(titleSeg) > inner {
			titleSeg = " " + ansi.Truncate(title, inner-2, "…") + " "
		}
		if lipgloss.Width(hintSeg) > inner {
			hintSeg = ""
		}
		fill := max(inner-lipgloss.Width(titleSeg)-lipgloss.Width(hintSeg), 0)
		top = bd.Render("╭─") + t.Title.Render(titleSeg) + bd.Render(strings.Repeat("─", fill)) + t.Dim.Render(hintSeg) + bd.Render("─╮")
	}

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
		sp := strings.Repeat(" ", pad)
		out = append(out, bd.Render("│")+sp+l+strings.Repeat(" ", max(inner-lipgloss.Width(l), 0))+sp+bd.Render("│"))
	}
	out = append(out, bd.Render("╰"+strings.Repeat("─", w-2)+"╯"))
	return strings.Join(out, "\n")
}
