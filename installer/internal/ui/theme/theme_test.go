package theme

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/flocc/installer/internal/ui/theme/themetest"
)

func lum(hex string) float64 {
	ch := func(i int) float64 {
		v, _ := strconv.ParseUint(hex[i:i+2], 16, 8)
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(1) + 0.7152*ch(3) + 0.0722*ch(5)
}

func TestLightColoursReadOnWhite(t *testing.T) {
	for _, h := range []string{AccentLightHex, UpLightHex, DownLightHex, WarnLightHex, ErrLightHex, DimLightHex} {
		if c := 1.05 / (lum(h) + 0.05); c < 4.5 {
			t.Errorf("%s is %.2f:1 on white", h, c)
		}
	}
}

func TestPalettesDiffer(t *testing.T) {
	if For(true) == For(false) {
		t.Error("dark and light palettes are the same")
	}
}

func TestPlainAndNoColorDrawNoEscapes(t *testing.T) {
	env := func(k string) string { return map[string]string{"NO_COLOR": "1"}[k] }
	dumb := func(k string) string { return map[string]string{"TERM": "dumb"}[k] }
	for name, th := range map[string]Theme{"plain": Plain, "NO_COLOR": FromEnv(env, true), "dumb": FromEnv(dumb, true), "plain light": Plain.WithDark(false)} {
		out := th.Pane("hosts", "hint", th.Up.Render(UpMark)+" x", 30, 4) + th.KeyBar([]key.Binding{th.Key(true, "a", "attach"), th.Key(false, "r", "release")}, 40) + th.TooSmall(59, 19)
		if strings.Contains(out, "\x1b") {
			t.Errorf("%s drew an escape: %q", name, out)
		}
	}
	if !strings.Contains(New(true).Up.Render("x"), "\x1b") {
		t.Error("the coloured theme drew no colour")
	}
}

func TestPaneTitleAndHintInTopBorder(t *testing.T) {
	got := themetest.Strip(New(true).Pane("issues", "1 of 2 · / filter", "row", 40, 4))
	lines := strings.Split(got, "\n")
	if len(lines) != 4 {
		t.Fatalf("%d lines:\n%s", len(lines), got)
	}
	// 10 columns of "╭─ issues ", 21 of " 1 of 2 · / filter ─╮", the rest is fill.
	if want := "╭─ issues " + strings.Repeat("─", 40-31) + " 1 of 2 · / filter ─╮"; lines[0] != want {
		t.Errorf("top %q, want %q", lines[0], want)
	}
	if lines[1] != "│ row"+strings.Repeat(" ", 33)+" │" {
		t.Errorf("body %q", lines[1])
	}
	for _, l := range lines {
		if w := lipgloss.Width(l); w != 40 {
			t.Errorf("line %q is %d wide", l, w)
		}
	}
}

func TestPaneDropsHintThenCutsTitle(t *testing.T) {
	got := themetest.Strip(New(true).Pane("a long pane title", "a long hint", "", 16, 2))
	top := strings.Split(got, "\n")[0]
	if strings.Contains(top, "hint") || lipgloss.Width(top) != 16 {
		t.Errorf("top %q", top)
	}
}

func TestPaneSizesToBodyAndCutsLongLines(t *testing.T) {
	got := themetest.Strip(New(true).Pane("t", "", "a\tb\n"+strings.Repeat("x", 100), 20, 0))
	lines := strings.Split(got, "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[1], "│ a    b") || !strings.Contains(lines[2], "…") {
		t.Errorf("pane:\n%s", got)
	}
	for _, l := range lines {
		if w := lipgloss.Width(l); w != 20 {
			t.Errorf("line %q is %d wide", l, w)
		}
	}
}

func TestKeyBarWrapsAndDims(t *testing.T) {
	th := New(true)
	var bs []key.Binding
	for _, k := range []string{"a", "b", "c", "d", "e", "f"} {
		bs = append(bs, th.Key(k != "c", k, "does "+k))
	}
	bar := th.KeyBar(bs, 30)
	plain := themetest.Strip(bar)
	if strings.Count(plain, "\n") == 0 || !strings.Contains(plain, "f does f") {
		t.Errorf("bar did not wrap or lost a key:\n%s", plain)
	}
	for _, l := range strings.Split(plain, "\n") {
		if lipgloss.Width(l) > 30 {
			t.Errorf("line %q over 30", l)
		}
	}
	if !strings.Contains(bar, th.Off.Render("c")) {
		t.Error("the off key is not dimmed")
	}
}

func TestKeyHelpIsAPaneTitledKeys(t *testing.T) {
	th := New(true)
	got := themetest.Strip(th.KeyHelp([][]key.Binding{{th.Key(true, "a", "attach")}, {th.Key(true, "q", "quit")}}, 40))
	if !strings.Contains(got, " keys ") || !strings.Contains(got, "attach") || !strings.Contains(got, "quit") {
		t.Errorf("help:\n%s", got)
	}
}

func TestToastGenerations(t *testing.T) {
	var ts Toasts
	ts.Show("first", true)
	old := ToastExpired{Gen: 1}
	ts.Show("second", true)
	ts.Expire(old)
	if ts.Text() != "second" {
		t.Errorf("an older timer cleared the newer toast: %q", ts.Text())
	}
	ts.Expire(ToastExpired{Gen: 2})
	if ts.Text() != "" {
		t.Errorf("toast stayed: %q", ts.Text())
	}
	if ts.Show("", false) != nil {
		t.Error("empty text returned a cmd")
	}
}

func TestScreenAndTooSmall(t *testing.T) {
	got := themetest.Strip(Screen("a\nb", 20, 6))
	if want := "\n  a\n  b\n\n\n"; got != want {
		t.Errorf("screen %q, want %q", got, want)
	}
	small := themetest.Strip(New(true).TooSmall(59, 19))
	if strings.Count(small, "\n") != 18 || !strings.Contains(small, "terminal too small: 59×19 · needs 60×20") {
		t.Errorf("too small:\n%s", small)
	}
}

func TestToastSitsTopRight(t *testing.T) {
	var ts Toasts
	ts.Show("boom", true)
	screen := strings.Repeat(strings.Repeat(".", 80)+"\n", 23) + strings.Repeat(".", 80)
	lines := strings.Split(themetest.Strip(New(true).Toast(screen, ts, 80)), "\n")
	if len(lines) != 24 || !strings.Contains(lines[2], "boom") || !strings.HasSuffix(lines[2], "│..") {
		t.Errorf("toast placement:\n%s", strings.Join(lines[:5], "\n"))
	}
}

func TestSpinnerAndRow(t *testing.T) {
	th := New(true)
	if f := themetest.Strip(th.Spinner().View()); f != "⠋" {
		t.Errorf("first spinner frame %q", f)
	}
	if themetest.Strip(th.Row(true, "x")) != Cursor+" x" || th.Row(false, "x") != "  x" {
		t.Error("row prefixes")
	}
}
