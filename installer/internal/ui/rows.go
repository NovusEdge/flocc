package ui

import (
	"fmt"
	"strings"
)

var marks = map[State]struct{ glyph, code string }{
	Running: {"→", ""}, Done: {"✓", "32"}, Pending: {"•", "33"}, Skipped: {"·", ""}, Failed: {"✗", "31"},
}

// Mark is a state's glyph, in the terminal's own 16 colours when color is set.
func Mark(s State, color bool) string {
	m := marks[s]
	return Out{Color: color}.paint(m.code, m.glyph)
}

// RowText is an ended step as the plain and inline renderers print it: two spaces in, the mark,
// the name padded to nameWidth, the detail (or the error), continuation lines under the detail.
func RowText(e Event, nameWidth int, color bool) string {
	detail := e.Detail
	if e.State == Failed && e.Err != "" {
		detail = e.Err
	}
	lines := strings.Split(strings.TrimRight(detail, "\n"), "\n")
	head := fmt.Sprintf("%s%s %-*s %s", indent, Mark(e.State, color), nameWidth, e.Name, lines[0])
	out := []string{strings.TrimRight(head, " ")}
	pad := strings.Repeat(" ", len(indent)+2+nameWidth+1)
	for _, l := range lines[1:] {
		out = append(out, pad+l)
	}
	return strings.Join(out, "\n")
}
