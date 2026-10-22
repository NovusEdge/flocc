package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
)

func TestThemedRowDrawsWithTheTheme(t *testing.T) {
	th := theme.New(true)
	e := Event{Kind: KStep, Name: "x", State: Done, Detail: "valid"}
	got := ThemedRow(e, 14, th)
	for _, want := range []string{th.Up.Render("✓"), th.Dim.Render("valid")} {
		if !strings.Contains(got, want) {
			t.Errorf("ThemedRow %q lacks %q", got, want)
		}
	}
	for st, style := range map[State]lipgloss.Style{Failed: th.Err, Pending: th.Warn, Skipped: th.Dim, Running: th.Accent} {
		if g := ThemedMark(st, th); !strings.Contains(g, style.Render(marks[st].glyph)) {
			t.Errorf("ThemedMark(%v) = %q", st, g)
		}
	}
	multi := ThemedRow(Event{Kind: KStep, Name: "k", State: Pending, Detail: "a\nb"}, 6, th)
	if !strings.Contains(multi, th.Dim.Render("a")) || !strings.Contains(multi, th.Dim.Render("b")) {
		t.Errorf("detail lines not all dim: %q", multi)
	}
}

func TestThemedRowPlainEqualsRowText(t *testing.T) {
	for _, e := range []Event{
		{Kind: KStep, Name: "Linear key", State: Done, Detail: "valid"},
		{Kind: KStep, Name: "x", State: Failed, Err: "boom"},
		{Kind: KStep, Name: "k", State: Pending, Detail: "a\nb"},
		{Kind: KStep, Name: "x", State: Done},
	} {
		if got, want := ThemedRow(e, 14, theme.Plain), RowText(e, 14, false); got != want {
			t.Errorf("ThemedRow(Plain) = %q, RowText = %q", got, want)
		}
	}
}

func TestThemedCloseTextDrawsTodosInWarn(t *testing.T) {
	th := theme.New(true)
	got := ThemedCloseText([]string{"run flocc ship"}, "/tmp/x.log", th)
	if want := th.Warn.Render("• run flocc ship"); !strings.Contains(got, want) {
		t.Errorf("ThemedCloseText %q lacks %q", got, want)
	}
	if g, want := ThemedCloseText([]string{"a"}, "l", theme.Plain), CloseText([]string{"a"}, "l"); g != want {
		t.Errorf("ThemedCloseText(Plain) = %q, CloseText = %q", g, want)
	}
}

func TestRowText(t *testing.T) {
	cases := []struct {
		e     Event
		color bool
		want  string
	}{
		{Event{Kind: KStep, Name: "Linear key", State: Done, Detail: "valid"}, false, "  ✓ Linear key     valid"},
		{Event{Kind: KStep, Name: "la-nimble", State: Pending, Detail: "1 left"}, false, "  • la-nimble      1 left"},
		{Event{Kind: KStep, Name: "x", State: Skipped, Detail: "kept"}, false, "  · x              kept"},
		{Event{Kind: KStep, Name: "x", State: Failed, Err: "boom"}, false, "  ✗ x              boom"},
		{Event{Kind: KStep, Name: "x", State: Done}, true, "  \x1b[32m✓\x1b[0m x"},
	}
	for _, c := range cases {
		if got := RowText(c.e, 14, c.color); got != c.want {
			t.Errorf("RowText(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
}

func TestRowTextMultiLineDetail(t *testing.T) {
	got := RowText(Event{Kind: KStep, Name: "keys", State: Pending, Detail: "a\nb"}, 6, false)
	if want := "  • keys   a\n           b"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
