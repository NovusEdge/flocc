package keys

import (
	"errors"
	"os"

	"charm.land/huh/v2"
	"golang.org/x/term"
)

// Huh asks on the terminal. A paste is read with echo off, so it never shows on screen or in the
// scrollback, and never reaches shell history.
type Huh struct {
	// Terminal reports whether stdin is a terminal; nil asks os.Stdin. Tests set it.
	Terminal func() bool
}

var errNoTerminal = errors.New("flocc keys reads keys with echo off; run it in a terminal")

// ready refuses a piped stdin before any prompt: huh's Confirm can return its default there with
// no error, which would answer "Check the same paste again?" yes with nobody at the keyboard.
func (h Huh) ready() error {
	isTerm := h.Terminal
	if isTerm == nil {
		isTerm = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
	}
	if !isTerm() {
		return errNoTerminal
	}
	return nil
}

func (h Huh) Secret(title, help string) (string, error) {
	if err := h.ready(); err != nil {
		return "", err
	}
	var v string
	err := huh.NewForm(huh.NewGroup(huh.NewInput().Title(title).Description(help).
		EchoMode(huh.EchoModeNone).Value(&v))).Run()
	return v, err
}

func (h Huh) Confirm(title string, def bool) (bool, error) {
	if err := h.ready(); err != nil {
		return false, err
	}
	v := def
	err := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title(title).Value(&v))).Run()
	return v, err
}
