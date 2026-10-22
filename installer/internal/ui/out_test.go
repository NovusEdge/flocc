package ui

import (
	"bytes"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
)

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func TestColorOnlyOnATerminalWithoutNoColorOrDumbTerm(t *testing.T) {
	for _, c := range []struct {
		name     string
		terminal bool
		env      func(string) string
		want     bool
	}{
		{"terminal", true, env("TERM", "xterm-256color"), true},
		{"not a terminal", false, env("TERM", "xterm-256color"), false},
		{"NO_COLOR", true, env("TERM", "xterm", "NO_COLOR", "1"), false},
		{"dumb", true, env("TERM", "dumb"), false},
	} {
		if got := colorOK(c.terminal, c.env); got != c.want {
			t.Errorf("%s: colorOK = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAWriterThatIsNotAFileGetsNoColor(t *testing.T) {
	if NewOut(&bytes.Buffer{}).Color {
		t.Fatal("a buffer is not a terminal")
	}
}

func TestPlainOutIndentsAndEmitsNoEscapes(t *testing.T) {
	var b bytes.Buffer
	o := Out{W: &b}
	o.Line("%s    Linear key: Ada", o.OK("ok"))
	o.Line("Created NF-1,\npin it")
	want := "  ok    Linear key: Ada\n  Created NF-1,\n  pin it\n"
	if b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
}

func TestColoredOutUsesTheSixteenANSIColors(t *testing.T) {
	var b bytes.Buffer
	o := Out{W: &b, Color: true}
	o.Line("%s    k", o.OK("ok"))
	o.Line("%s    bad", o.Fail("FAIL"))
	want := "  \x1b[32mok\x1b[0m    k\n" +
		"  \x1b[31mFAIL\x1b[0m    bad\n"
	if b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
}

func TestSplitNext(t *testing.T) {
	what, next := SplitNext("Linear key: Linear rejected the key; run flocc keys rotate linear")
	if what != "Linear key: Linear rejected the key" || next != "run flocc keys rotate linear" {
		t.Fatalf("%q %q", what, next)
	}
	// A run inside a longer message is not the next step: only the last line's can be.
	what, next = SplitNext("a; run b\nmore")
	if what != "a; run b\nmore" || next != "" {
		t.Fatalf("%q %q", what, next)
	}
}

func TestScriptedStatusIsIndentedAndColoredOnlyWhenAsked(t *testing.T) {
	steps := []StepStatus{{Name: "Packages", Detail: "installed", Done: true}, {Name: "User", Detail: "missing"}}
	var plain, color bytes.Buffer
	(&Scripted{Out: &plain}).Status(steps)
	(&Scripted{Out: &color, Color: true}).Status(steps)
	if want := "  ✓ Packages       installed\n  · User           missing\n"; plain.String() != want {
		t.Fatalf("got %q", plain.String())
	}
	if !strings.Contains(color.String(), "\x1b[32m✓\x1b[0m") {
		t.Fatalf("no green mark: %q", color.String())
	}
}

func TestFormThemeTakesTheSharedAccent(t *testing.T) {
	for _, dark := range []bool{true, false} {
		th := Theme(dark)
		want := theme.For(dark).Accent
		for name, s := range map[string]lipgloss.Style{
			"focused button": th.Focused.FocusedButton,
			"blurred button": th.Blurred.FocusedButton,
		} {
			if s.GetBackground() != want {
				t.Errorf("dark=%v %s: background %v, want the accent %v", dark, name, s.GetBackground(), want)
			}
		}
		if th.Focused.TextInput.Cursor.GetForeground() != want || th.Focused.Base.GetBorderTopForeground() != want {
			t.Errorf("dark=%v: cursor or border is not the accent", dark)
		}
		if got := SpinnerTheme(dark).Spinner.GetForeground(); got != want {
			t.Errorf("dark=%v: spinner %v, want %v", dark, got, want)
		}
	}
}
