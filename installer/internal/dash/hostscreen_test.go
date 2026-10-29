package dash

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ui/theme/themetest"
)

const doctorJSON = `{"v":1,"checks":[` +
	`{"name":"config","ok":true,"detail":"/etc/flocc/config.toml"},` +
	`{"name":"ntfy","ok":true,"warn":true,"detail":"placeholder"},` +
	`{"name":"linear","ok":false,"detail":"401"}]}`

// openHost focuses the hosts pane and opens name, returning the fetches it starts.
func openHost(t *testing.T, m *Model, name string) tea.Cmd {
	t.Helper()
	m.focus = hostsPane
	m.hostSel = slices.IndexFunc(m.hosts, func(h hostState) bool { return h.host.Name == name })
	cmd := key(m, "enter")
	if m.screen != hostScreen {
		t.Fatalf("enter opened screen %v", m.screen)
	}
	return cmd
}

// squeezed collapses runs of spaces, so a test need not know a column's width.
func squeezed(m *Model) string { return strings.Join(strings.Fields(text(m)), " ") }

func TestHostScreenFacts(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	openHost(t, m, "flocc-nimble")
	for _, want := range []string{
		"─ flocc-nimble ─",
		"version status v1", "mode live", "last tick 2m ago, ok", "usage 5h 12% of 80% · wk 31% of 90%",
		"disk 82.0 GiB free of 200 GiB", "claude full", "tokens -", "cap 2 sessions",
		"─ doctor ─", "─ log ─", "l full screen",
	} {
		if !strings.Contains(squeezed(m), want) {
			t.Errorf("lacks %q:\n%s", want, text(m))
		}
	}
	key(m, "esc")
	if m.screen != fleetScreen {
		t.Error("esc did not go back")
	}
}

func TestHostScreenBehindAndUnreported(t *testing.T) {
	m, _ := newModel(t)
	f := canned()
	f.results[0].Status = report(0)
	m.Update(f)
	openHost(t, m, "flocc-nimble")
	if !strings.Contains(squeezed(m), "version behind · ship to update") {
		t.Errorf("\n%s", text(m))
	}
	key(m, "esc")
	f.at = t0.Add(1)
	f.results[0] = Result{Host: f.results[0].Host, Err: fmt.Errorf("exit 255: refused")}
	m.Update(f)
	openHost(t, m, "flocc-nimble")
	if !strings.Contains(text(m), "refused") {
		t.Errorf("no error:\n%s", text(m))
	}
}

func TestDoctorRowsAndRerun(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	src(m).answer("doctor --json", host.Result{Code: 1, Stdout: doctorJSON})
	cmd := openHost(t, m, "flocc-nimble")
	if !strings.Contains(text(m), "running") {
		t.Errorf("doctor not running:\n%s", text(m))
	}
	settle(m, cmd, 3)
	got := squeezed(m)
	for _, want := range []string{"✓ config /etc/flocc/config.toml", "• ntfy placeholder", "✗ linear 401", "d run again"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, text(m))
		}
	}
	if strings.Contains(got, "running") && !strings.Contains(got, "d run again") {
		t.Error("still running after the answer")
	}
	src(m).answer("doctor --json", host.Result{Stdout: `{"v":1,"checks":[{"name":"second","ok":true,"detail":"run"}]}`})
	again := key(m, "d")
	if !strings.Contains(text(m), "running") {
		t.Errorf("no running hint:\n%s", text(m))
	}
	m.Update(doctorMsg{gen: m.docGen - 1, checks: nil, note: "stale"})
	if strings.Contains(text(m), "stale") {
		t.Error("an older doctor answer was kept")
	}
	settle(m, again, 3)
	if !strings.Contains(squeezed(m), "✓ second run") || strings.Contains(squeezed(m), "linear") {
		t.Errorf("rerun:\n%s", text(m))
	}
}

func TestDoctorOnAnOldHost(t *testing.T) {
	for name, res := range map[string]host.Result{
		"old":     {Code: 2, Stderr: "invalid choice: 'doctor --json'"},
		"v0":      {Stdout: `{"checks":[]}`},
		"garbled": {Stdout: "not json"},
	} {
		m := goldenModel(t, [2]int{140, 40})
		src(m).answer("doctor --json", res)
		settle(m, openHost(t, m, "flocc-nimble"), 3)
		if !strings.Contains(text(m), "ship to see doctor checks") {
			t.Errorf("%s:\n%s", name, text(m))
		}
	}
}

func TestHostileDoctorDetail(t *testing.T) {
	hostile := "\x1b[2J" + strings.Repeat("x", 300)
	for _, sz := range [][2]int{{80, 24}, {140, 40}} {
		m := goldenModel(t, sz)
		src(m).answer("doctor --json", ok(`{"v":1,"checks":[{"name":"bad\u001b[2J","ok":false,"detail":"`+strings.ReplaceAll(hostile, "\x1b", `\u001b`)+`"}]}`))
		settle(m, openHost(t, m, "flocc-nimble"), 3)
		if strings.Contains(m.View().Content, "\x1b[2J") {
			t.Errorf("%v: an escape reached the frame", sz)
		}
		for _, l := range strings.Split(text(m), "\n") {
			if lipgloss.Width(l) > sz[0] {
				t.Errorf("%v: %q too wide", sz, l)
			}
		}
	}
}

func TestServiceLogTail(t *testing.T) {
	m := goldenModel(t, [2]int{140, 40})
	src(m).answer("logs -n 1", ok("tick one\ntick two\n"))
	cmd := openHost(t, m, "flocc-nimble")
	settle(m, cmd, 3)
	if !strings.Contains(text(m), "tick two") {
		t.Fatalf("no log:\n%s", text(m))
	}
	_, next := m.Update(outputMsg{gen: m.outGen, title: "log", lines: []string{"tick three"}})
	if next == nil {
		t.Error("the log did not schedule a refresh")
	}
	before := len(src(m).ran())
	_, cmd = m.Update(outputTick{gen: m.outGen})
	settle(m, cmd, 2)
	if got := src(m).ran(); len(got) != before+1 || !strings.Contains(got[len(got)-1], "logs -n ") {
		t.Errorf("calls %v", got)
	}
	src(m).answer("logs -n 2000", ok(numbered(100)))
	settle(m, key(m, "l"), 3)
	if m.screen != pagerScreen || !strings.Contains(text(m), "flocc-nimble log") || !strings.Contains(text(m), "line 100") {
		t.Errorf("pager:\n%s", text(m))
	}
	key(m, "esc")
	if m.screen != hostScreen {
		t.Error("esc did not return to the host screen")
	}
}

func TestShipDimmedOutsideTheCheckout(t *testing.T) {
	m, ran := newModel(t)
	m.opts.ShipBlock = "cd into the flocc checkout to ship"
	m.Update(canned())
	openHost(t, m, "flocc-nimble")
	off := m.th.Off.Render("s") + " " + m.th.Off.Render("ship (cd into the flocc checkout to ship)")
	if !strings.Contains(m.View().Content, off) {
		t.Errorf("s ship is not dimmed with its reason:\n%s", text(m))
	}
	if cmd := key(m, "s"); cmd != nil || len(*ran) != 0 {
		t.Errorf("s ran %v", *ran)
	}

	m, ran = newModel(t)
	m.Update(canned())
	openHost(t, m, "flocc-nimble")
	if !strings.Contains(text(m), "s ship") || strings.Contains(m.View().Content, m.th.Off.Render("s")) {
		t.Errorf("s ship is dimmed:\n%s", text(m))
	}
	if cmd := key(m, "s"); cmd == nil || len(*ran) != 1 || strings.Join((*ran)[0], " ") != "ship -H flocc-nimble" {
		t.Errorf("ran %v", *ran)
	}
}

func TestHostGolden(t *testing.T) {
	for _, sz := range [][2]int{{59, 19}, {80, 24}, {140, 40}} {
		m := goldenModel(t, [2]int{140, 40}) // keys do nothing below the minimum size, so resize after opening
		src(m).answer("doctor --json", ok(doctorJSON))
		cmd := openHost(t, m, "flocc-nimble")
		m.Update(sizeMsg(sz))
		m.Update(outputMsg{gen: m.outGen, title: "log", lines: []string{
			"14:08 tick start", "14:08 NF-240 blocked: asked in Linear", "14:09 tick ok",
		}})
		settle(m, cmd, 3)
		m.Update(outputMsg{gen: m.outGen, title: "log", lines: []string{
			"14:08 tick start", "14:08 NF-240 blocked: asked in Linear", "14:09 tick ok",
		}})
		themetest.Golden(t, fmt.Sprintf("host-%dx%d", sz[0], sz[1]), text(m))
	}
}
