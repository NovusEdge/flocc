package ui

import "io"

type Mode string

const (
	ModePlain  Mode = "plain"
	ModeInline Mode = "inline"
	ModeFull   Mode = "full"
)

// ModeEnv is the variable that picks the renderer when no flag does.
const ModeEnv = "FLOCC_UI"

func (m Mode) valid() bool { return m == ModePlain || m == ModeInline || m == ModeFull }

// ModeFor picks the renderer: a non-terminal out, NO_COLOR or TERM=dumb is always plain; else the
// flag, else FLOCC_UI, else def.
func ModeFor(flag Mode, getenv func(string) string, isTerminal bool, def Mode) Mode {
	if !colorOK(isTerminal, getenv) {
		return ModePlain
	}
	m := flag
	if !m.valid() {
		m = Mode(getenv(ModeEnv))
	}
	if !m.valid() {
		m = def
	}
	return m
}

// Open starts a session on out/in in mode m. title names the command in the full-screen header
// and stop question (flocc setup, host create, ...); the others ignore it.
func Open(m Mode, out io.Writer, in io.Reader, title ...string) Session {
	switch m {
	case ModePlain:
		return New(NewPlain(out, in, false))
	case ModeFull:
		return New(NewFull(out, in, append(title, "flocc")[0]))
	}
	return New(NewInline(out, in))
}

// StripModeFlags removes --plain, --inline and --full from args, returning the last one given.
// Everything after "--" belongs to the command being run and stays.
func StripModeFlags(args []string) (Mode, []string) {
	var mode Mode
	rest := make([]string, 0, len(args))
	for i, a := range args {
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		switch a {
		case "--plain":
			mode = ModePlain
		case "--inline":
			mode = ModeInline
		case "--full":
			mode = ModeFull
		default:
			rest = append(rest, a)
		}
	}
	return mode, rest
}
