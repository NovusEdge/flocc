package keys

import (
	"errors"
	"os"

	"charm.land/huh/v2"
	"golang.org/x/term"

	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// Huh asks on the terminal. A paste is read with echo off, so it never shows on screen or in the
// scrollback, and never reaches shell history.
type Huh struct {
	// Terminal reports whether stdin is a terminal; nil asks os.Stdin. Tests set it.
	Terminal func() bool
}

var errNoTerminal = errors.New("flocc keys reads keys with echo off; run it in a terminal")

// IsTerminal is false on a piped stdin. huh answers there with a default and no error, such as
// yes to "Check the same paste again?" or the first team of a list, with nobody at the keyboard,
// so every prompt checks this first.
func (h Huh) IsTerminal() bool {
	if h.Terminal != nil {
		return h.Terminal()
	}
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func (h Huh) Secret(title, help string) (string, error) {
	if !h.IsTerminal() {
		return "", errNoTerminal
	}
	var v string
	err := ui.Form(huh.NewGroup(huh.NewInput().Title(title).Description(help).
		EchoMode(huh.EchoModeNone).Value(&v))).Run()
	return v, err
}

func (h Huh) Confirm(title string, def bool) (bool, error) {
	if !h.IsTerminal() {
		return false, errNoTerminal
	}
	v := def
	err := ui.Form(huh.NewGroup(huh.NewConfirm().Title(title).Value(&v))).Run()
	return v, err
}
