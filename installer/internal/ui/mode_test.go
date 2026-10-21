package ui

import (
	"slices"
	"testing"
)

func TestModeFor(t *testing.T) {
	cases := []struct {
		name     string
		flag     Mode
		getenv   func(string) string
		terminal bool
		def      Mode
		want     Mode
	}{
		{"pipe beats flag", ModeInline, env(), false, ModeFull, ModePlain},
		{"NO_COLOR", "", env("NO_COLOR", "1"), true, ModeFull, ModePlain},
		{"TERM=dumb", ModeInline, env("TERM", "dumb"), true, ModeFull, ModePlain},
		{"flag", ModeInline, env(), true, ModeFull, ModeInline},
		{"env", "", env("FLOCC_UI", "plain"), true, ModeFull, ModePlain},
		{"flag beats env", ModeInline, env("FLOCC_UI", "plain"), true, ModeFull, ModeInline},
		{"default full stays full", "", env(), true, ModeFull, ModeFull},
		{"flag full", ModeFull, env(), true, ModeInline, ModeFull},
		{"bad env takes default", "", env("FLOCC_UI", "fancy"), true, ModeInline, ModeInline},
	}
	for _, c := range cases {
		if got := ModeFor(c.flag, c.getenv, c.terminal, c.def); got != c.want {
			t.Errorf("%s: ModeFor = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStripModeFlags(t *testing.T) {
	mode, rest := StripModeFlags([]string{"host", "run", "-H", "x", "--inline", "--", "ls", "--plain"})
	if mode != ModeInline {
		t.Errorf("mode = %q, want inline", mode)
	}
	if want := []string{"host", "run", "-H", "x", "--", "ls", "--plain"}; !slices.Equal(rest, want) {
		t.Errorf("rest = %q, want %q", rest, want)
	}
	if mode, rest := StripModeFlags([]string{"--plain", "a", "--full"}); mode != ModeFull || !slices.Equal(rest, []string{"a"}) {
		t.Errorf("last flag wins: %q %q", mode, rest)
	}
	if mode, _ := StripModeFlags([]string{"a"}); mode != "" {
		t.Errorf("no flag gave mode %q", mode)
	}
}
