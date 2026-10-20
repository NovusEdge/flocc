package keys

import "charm.land/huh/v2"

// Huh asks on the terminal. A paste is read with echo off, so it never shows on screen or in the
// scrollback, and never reaches shell history.
type Huh struct{}

func (Huh) Secret(title, help string) (string, error) {
	var v string
	err := huh.NewForm(huh.NewGroup(huh.NewInput().Title(title).Description(help).
		EchoMode(huh.EchoModeNone).Value(&v))).Run()
	return v, err
}

func (Huh) Confirm(title string, def bool) (bool, error) {
	v := def
	err := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title(title).Value(&v))).Run()
	return v, err
}
