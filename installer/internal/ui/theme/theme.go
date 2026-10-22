// Package theme is the one look every desktop screen draws through: the
// palette, glyphs, pane, key bar, toast and screen margin. Technique and hex
// values follow stoat's internal/tui and internal/theme.
package theme

import (
	"image/color"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The unsuffixed hexes are the dark palette. The Light ones are the same hues
// darkened until each clears 4.5:1 against #FFFFFF.
const (
	AccentHex, UpHex, DownHex, WarnHex, ErrHex, DimHex = "#C98A5B", "#7FB069", "#868CAA", "#E0A458", "#D16969", "#8B949E"

	AccentLightHex, UpLightHex, DownLightHex, WarnLightHex, ErrLightHex, DimLightHex = "#7A3E12", "#386A20", "#4C5163", "#7A4E00", "#A12D2D", "#59636E"
)

const (
	Cursor   = "❯"
	UpMark   = "●"
	DownMark = "○"
	FailMark = "✗"
	DoneMark = "✓"
	Sep      = " · "
)

const (
	MarginX, MarginY = 2, 1
	MinW, MinH       = 60, 20
	StackBelow       = 100 // side-by-side panes stack below this many columns
	ToastLife        = 4 * time.Second
)

type Palette struct{ Accent, Up, Down, Warn, Err, Dim color.Color }

// For picks the palette that reads on the reported background; a terminal
// that cannot answer is treated as dark.
func For(isDark bool) Palette {
	pick := lipgloss.LightDark(isDark)
	c := lipgloss.Color
	return Palette{
		Accent: pick(c(AccentLightHex), c(AccentHex)),
		Up:     pick(c(UpLightHex), c(UpHex)),
		Down:   pick(c(DownLightHex), c(DownHex)),
		Warn:   pick(c(WarnLightHex), c(WarnHex)),
		Err:    pick(c(ErrLightHex), c(ErrHex)),
		Dim:    pick(c(DimLightHex), c(DimHex)),
	}
}

// Theme is a value: a model keeps one and replaces it when the background
// answer arrives.
type Theme struct {
	Color, Dark                      bool
	Accent, Up, Down, Warn, Err, Dim lipgloss.Style
	Bold, Title, Sel, Off            lipgloss.Style // Title and Sel: bold accent; Off: down + faint
}

func New(isDark bool) Theme {
	p := For(isDark)
	fg := func(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }
	return Theme{
		Color: true, Dark: isDark,
		Accent: fg(p.Accent), Up: fg(p.Up), Down: fg(p.Down), Warn: fg(p.Warn), Err: fg(p.Err), Dim: fg(p.Dim),
		Bold:  lipgloss.NewStyle().Bold(true),
		Title: fg(p.Accent).Bold(true),
		Sel:   fg(p.Accent).Bold(true),
		Off:   fg(p.Down).Faint(true),
	}
}

// Plain is the zero Theme: a zero lipgloss.Style renders its input as-is.
var Plain Theme

// FromEnv mirrors ui.colorOK's env rules without importing ui, which imports
// this package.
func FromEnv(getenv func(string) string, isDark bool) Theme {
	if getenv("NO_COLOR") != "" || getenv("TERM") == "dumb" {
		return Plain
	}
	return New(isDark)
}

func (t Theme) WithDark(isDark bool) Theme {
	if !t.Color {
		return t
	}
	return New(isDark)
}

func (t Theme) Spinner() spinner.Model {
	return spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(t.Accent))
}

func (t Theme) Row(sel bool, s string) string {
	if sel {
		return t.Sel.Render(Cursor + " " + s)
	}
	return "  " + s
}

// Inner is the area left inside the margin.
func Inner(w, h int) (iw, ih int) {
	return max(w-2*MarginX, 1), max(h-2*MarginY, 1)
}

// Screen puts body inside the margin, in exactly h lines.
func Screen(body string, w, h int) string {
	iw, ih := Inner(w, h)
	lines := make([]string, 0, h)
	for range MarginY {
		lines = append(lines, "")
	}
	src := strings.Split(body, "\n")
	for i := range ih {
		l := ""
		if i < len(src) && src[i] != "" {
			l = strings.Repeat(" ", MarginX) + ansi.Truncate(src[i], iw, "")
		}
		lines = append(lines, l)
	}
	for range MarginY {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

func (t Theme) TooSmall(w, h int) string {
	msg := "terminal too small: " + strconv.Itoa(w) + "×" + strconv.Itoa(h) + " · needs " + strconv.Itoa(MinW) + "×" + strconv.Itoa(MinH)
	lines := make([]string, max(h, 1))
	lines[(len(lines)-1)/2] = strings.Repeat(" ", max((w-lipgloss.Width(msg))/2, 0)) + t.Warn.Render(msg)
	return strings.Join(lines, "\n")
}
