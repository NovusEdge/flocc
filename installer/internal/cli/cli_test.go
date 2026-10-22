package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/ui"
)

func testDeps(t *testing.T) (*deps, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errb bytes.Buffer
	return &deps{out: &out, err: &errb, home: t.TempDir(), getenv: func(string) string { return "" },
		ui: ui.New(&ui.Recorder{})}, &out, &errb
}

func TestBareFloccPrintsHelpAndFails(t *testing.T) {
	d, out, errb := testDeps(t)
	if code := run(context.Background(), d, nil); code != 2 || out.Len() != 0 || !strings.Contains(errb.String(), "Desktop commands:") {
		t.Fatalf("%d %q %q", code, out, errb)
	}
}

// dashDeps is a terminal on both ends with a recording dashboard.
func dashDeps(t *testing.T, names ...string) (*deps, *int) {
	t.Helper()
	d, _, _, _ := hostDeps(t, names...)
	d.tty, d.outTTY = true, func() bool { return true }
	opened := 0
	d.dash = func(context.Context) int { opened++; return 0 }
	return d, &opened
}

func TestBareFloccOnATerminalOpensTheDashboard(t *testing.T) {
	d, opened := dashDeps(t, "a")
	if code := run(context.Background(), d, nil); code != 0 || *opened != 1 {
		t.Fatalf("exit %d, opened %d", code, *opened)
	}
}

func TestBareFloccStaysPlainWhenAskedOrPiped(t *testing.T) {
	for name, set := range map[string]func(*deps){
		"flag":      func(d *deps) { d.mode = ui.ModePlain },
		"piped out": func(d *deps) { d.outTTY = func() bool { return false } },
		"piped in":  func(d *deps) { d.tty = false },
	} {
		d, opened := dashDeps(t, "a")
		set(d)
		errb := d.err.(*bytes.Buffer)
		if code := run(context.Background(), d, nil); code != 2 || *opened != 0 || !strings.Contains(errb.String(), "Desktop commands:") {
			t.Errorf("%s: exit %d, opened %d, %q", name, code, *opened, errb)
		}
	}
}

func TestBareFloccWithoutHostsSaysSo(t *testing.T) {
	d, opened := dashDeps(t)
	errb := d.err.(*bytes.Buffer)
	if code := run(context.Background(), d, nil); code != 2 || *opened != 0 ||
		!strings.Contains(errb.String(), "Desktop commands:") || !strings.Contains(errb.String(), "No hosts yet; run flocc setup.") {
		t.Fatalf("exit %d, opened %d, %q", code, *opened, errb)
	}
}

func TestStatusWatchOpensTheDashboardOnATerminalAndPrintsOnceOtherwise(t *testing.T) {
	d, opened := dashDeps(t, "a")
	if code := run(context.Background(), d, []string{"status", "--watch"}); code != 0 || *opened != 1 {
		t.Fatalf("terminal: exit %d, opened %d", code, *opened)
	}
	d, opened = dashDeps(t, "a")
	d.outTTY = func() bool { return false }
	var got []string
	d.fleet = func(_ context.Context, _ ui.Session, args []string) int { got = args; return 0 }
	if code := run(context.Background(), d, []string{"status", "-H", "all", "--watch", "--interval", "9s"}); code != 0 || *opened != 0 || len(got) != 0 {
		t.Fatalf("piped: exit %d, opened %d, fleet args %q", code, *opened, got)
	}
}

func TestAPanicInACommandClosesTheSessionBeforeItGoesOn(t *testing.T) {
	d, f, _, _ := hostDeps(t, "a")
	d.fleet = func(context.Context, ui.Session, []string) int { panic("boom") }
	defer func() {
		if recover() != "boom" {
			t.Error("the panic was swallowed or changed")
		}
		if !f.rec.Closed {
			t.Error("the session was left open: the terminal stays in the program's mode")
		}
	}()
	run(context.Background(), d, []string{"-H", "all", "status", "--json"})
}

// A forwarded command's stdout is the remote command's, so flocc logs NF-1 | grep gets no rows of ours.
func TestAHandoffOnlyCommandDrawsItsRowsOnStderr(t *testing.T) {
	d, out, errb := testDeps(t)
	d.ui = nil
	openSession(context.Background(), d, "a", []string{"logs", "NF-1"})
	d.ui.Step("Find NF-1").Done("flocc-a")
	d.ui.Close()
	if out.Len() != 0 || !strings.Contains(errb.String(), "Find NF-1") {
		t.Fatalf("stdout %q stderr %q", out, errb)
	}
}

func TestHelpGoesToStdout(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		d, out, _ := testDeps(t)
		if code := run(context.Background(), d, []string{arg}); code != 0 || !strings.Contains(out.String(), "Desktop commands:") {
			t.Fatalf("%s: %d %q", arg, code, out)
		}
	}
}

func TestSessionModeDefaultsToFullScreenForLongCommands(t *testing.T) {
	for _, c := range []struct {
		args []string
		want ui.Mode
	}{
		{[]string{"setup"}, ui.ModeFull},
		{[]string{"host", "create", "x", "--on", "m"}, ui.ModeFull},
		{[]string{"ship"}, ui.ModeFull},
		{[]string{"login"}, ui.ModeFull},
		{[]string{"cardinal", "setup"}, ui.ModeFull},
		{[]string{"cardinal", "login"}, ui.ModeFull},
		{[]string{"keys"}, ui.ModeInline},
		{[]string{"host", "add", "x"}, ui.ModeInline},
		{[]string{"cardinal", "status"}, ui.ModeInline},
		{[]string{"status"}, ui.ModePlain},
		{[]string{"host", "run", "x"}, ui.ModePlain},
		{[]string{"cardinal", "attach"}, ui.ModePlain},
	} {
		d, _, _ := testDeps(t)
		d.tty, d.outTTY = true, func() bool { return true }
		name, rest, _ := splitHost(c.args)
		if got := sessionMode(d, name, rest); got != c.want {
			t.Errorf("%v: %q, want %q", c.args, got, c.want)
		}
	}
}

func TestSessionModeKeepsFullScreenOffAPipeOrAFlag(t *testing.T) {
	for name, set := range map[string]func(*deps){
		"piped out": func(d *deps) { d.outTTY = func() bool { return false } },
		"piped in":  func(d *deps) { d.tty = false },
		"--plain":   func(d *deps) { d.mode = ui.ModePlain },
		"--inline":  func(d *deps) { d.mode = ui.ModeInline },
	} {
		d, _, _ := testDeps(t)
		d.tty, d.outTTY = true, func() bool { return true }
		set(d)
		if got := sessionMode(d, "", []string{"setup"}); got == ui.ModeFull {
			t.Errorf("%s: full screen", name)
		}
	}
}
