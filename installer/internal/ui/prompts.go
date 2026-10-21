package ui

import "charm.land/huh/v2"

// Prompts adapts a Session to the prompt shapes keys and cli already use.
type Prompts struct{ S Session }

func (p Prompts) Secret(title, help string) (string, error) {
	return p.S.Secret(Question{Title: title, Help: help})
}

func (p Prompts) Confirm(title string, def bool) (bool, error) {
	d := "no"
	if def {
		d = "yes"
	}
	return p.S.Confirm(Question{Title: title, Default: d})
}

func (p Prompts) Ask(title, help string) (string, error) {
	return p.S.Ask(Question{Title: title, Help: help})
}

func (p Prompts) Choose(title string, options []string) (int, error) {
	return p.S.Choose(Question{Title: title}, options)
}

func (p Prompts) Note(title, body string) { p.S.Note(title, body) }

func (p Prompts) Form(f *huh.Form) error { return p.S.Form(f) }
