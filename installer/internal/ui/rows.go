package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
)

var marks = map[State]struct{ glyph, code string }{
	Running: {"→", ""}, Done: {"✓", "32"}, Pending: {"•", "33"}, Skipped: {"·", ""}, Failed: {"✗", "31"},
}

// Mark is a state's glyph, in the terminal's own 16 colours when color is set.
func Mark(s State, color bool) string {
	m := marks[s]
	return Out{Color: color}.paint(m.code, m.glyph)
}

// ThemedMark is Mark drawn with th: running in Accent, done Up, pending Warn, skipped Dim, failed Err.
func ThemedMark(s State, th theme.Theme) string {
	style := map[State]lipgloss.Style{Running: th.Accent, Done: th.Up, Pending: th.Warn, Skipped: th.Dim, Failed: th.Err}[s]
	return style.Render(marks[s].glyph)
}

// RowText is an ended step as the plain and inline renderers print it: two spaces in, the mark,
// the name padded to nameWidth, the detail (or the error), continuation lines under the detail.
func RowText(e Event, nameWidth int, color bool) string {
	return rowText(e, nameWidth, Mark(e.State, color), same)
}

// ThemedRow is RowText drawn with th: the mark in its state's colour, the detail in Dim.
func ThemedRow(e Event, nameWidth int, th theme.Theme) string {
	return rowText(e, nameWidth, ThemedMark(e.State, th), dimmer(th))
}

// dimmer draws text in Dim and leaves an empty string empty, so a row without detail keeps no
// trailing escape codes for TrimRight to miss.
func dimmer(th theme.Theme) func(string) string {
	return func(s string) string {
		if s == "" {
			return s
		}
		return th.Dim.Render(s)
	}
}

func rowText(e Event, nameWidth int, mark string, detail func(string) string) string {
	text := e.Detail
	if e.State == Failed && e.Err != "" {
		text = e.Err
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	head := fmt.Sprintf("%s%s %-*s %s", indent, mark, nameWidth, e.Name, detail(lines[0]))
	out := []string{strings.TrimRight(head, " ")}
	pad := strings.Repeat(" ", len(indent)+2+nameWidth+1)
	for _, l := range lines[1:] {
		out = append(out, pad+detail(l))
	}
	return strings.Join(out, "\n")
}

// NoteText is a note as the plain and inline renderers print it: the title, then the body
// indented two.
func NoteText(title, body string) string {
	out := []string{title}
	for _, l := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if l != "" {
			l = indent + l
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// TableText is a table with each column padded to its widest cell, two spaces apart, indented
// two, under the title when it has one.
func TableText(t Table) string {
	widths := make([]int, len(t.Headers))
	for i, h := range t.Headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, r := range t.Rows {
		for i, c := range r {
			if i < len(widths) {
				widths[i] = max(widths[i], utf8.RuneCountInString(c))
			}
		}
	}
	var out []string
	if t.Title != "" {
		out = append(out, t.Title)
	}
	line := func(cells []string) {
		var b strings.Builder
		b.WriteString(indent)
		for i, c := range cells {
			if i < len(widths) {
				fmt.Fprintf(&b, "%-*s  ", widths[i], c)
			}
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	line(t.Headers)
	for _, r := range t.Rows {
		line(r)
	}
	return strings.Join(out, "\n")
}

// CloseText is the record at the end of a session: the to-dos, then the log file.
func CloseText(todos []string, log string) string {
	var b strings.Builder
	if len(todos) > 0 {
		b.WriteString("\nStill yours to do\n")
		for _, t := range todos {
			b.WriteString(indent + "• " + t + "\n")
		}
	}
	if log != "" {
		b.WriteString("\nLog: " + log + "\n")
	}
	return b.String()
}
