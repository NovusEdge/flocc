package ui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
)

// Out writes the desktop's status lines: indented two spaces under the command's first line, in
// the terminal's own 16 ANSI colours when Color is set. The zero Color is plain text, which is
// what logs, pipes and the host's flocc-summary line get.
type Out struct {
	W     io.Writer
	Color bool
}

// NewOut colours when w is a terminal, NO_COLOR is unset and TERM is not dumb.
func NewOut(w io.Writer) Out { return Out{W: w, Color: ColorFor(w)} }

func ColorFor(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && colorOK(term.IsTerminal(int(f.Fd())), os.Getenv)
}

// colorOK is a terminal whose environment allows colour; theme.FromEnv holds the env rule
// (no-color.org: any non-empty NO_COLOR, and TERM=dumb, turn colour off).
func colorOK(isTerminal bool, getenv func(string) string) bool {
	return isTerminal && theme.FromEnv(getenv, true).Color
}

func (o Out) paint(code, s string) string {
	if !o.Color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (o Out) OK(s string) string   { return o.paint("32", s) }
func (o Out) Fail(s string) string { return o.paint("31", s) }
func (o Out) Warn(s string) string { return o.paint("33", s) }
func (o Out) Bold(s string) string { return o.paint("1", s) }

const indent = "  "

func same(s string) string { return s }

// Line writes a status line two spaces in; each line of a multi-line text is indented.
func (o Out) Line(format string, a ...any) { o.write(indent, same, fmt.Sprintf(format, a...)) }

// SplitNext cuts the next step off an error text: what follows the last "; run ", as in
// "Linear rejected the key; run flocc keys rotate linear".
func SplitNext(s string) (what, next string) {
	if i := strings.LastIndex(s, "; run "); i >= 0 && !strings.Contains(s[i:], "\n") {
		return s[:i], s[i+2:]
	}
	return s, ""
}

// write paints each line on its own, so the colour never runs across the indent.
func (o Out) write(prefix string, paint func(string) string, text string) {
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if l != "" {
			l = prefix + paint(l)
		}
		fmt.Fprintln(o.W, l)
	}
}
