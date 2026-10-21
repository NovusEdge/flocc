package ui

import (
	"regexp"
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plainView(m fullModel) string { return ansiRE.ReplaceAllString(m.View().Content, "") }

func updateFull(m fullModel, msg tea.Msg) (fullModel, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(fullModel), cmd
}

func sized(w, h int) fullModel {
	m := newFullModel(fixedNow, "dev")
	m, _ = updateFull(m, tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func feed(m fullModel, evs ...Event) fullModel {
	for _, e := range evs {
		m, _ = updateFull(m, drawMsg{e})
	}
	return m
}

func keysAndHosts(m fullModel) fullModel {
	return feed(m,
		Event{Kind: KGroup, Name: "Keys"},
		Event{Kind: KStep, ID: "s1", Name: "ssh key", State: Running},
		Event{Kind: KStep, ID: "s1", Name: "ssh key", State: Done, Detail: "ed25519"},
		Event{Kind: KStep, ID: "s2", Name: "age key", State: Running},
		Event{Kind: KStep, ID: "s2", Name: "age key", State: Done, Detail: "made"},
		Event{Kind: KGroup, Name: "Hosts"},
		Event{Kind: KStep, ID: "s3", Name: "flocc-1", State: Running, Detail: "booting"},
	)
}

func TestFullFrame(t *testing.T) {
	m := keysAndHosts(sized(100, 30))
	if !m.View().AltScreen {
		t.Error("view does not ask for the alt screen")
	}
	v := plainView(m)
	for _, want := range []string{"✓ Keys", "2/2", "flocc-1", "booting"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	glyph := spinner.MiniDot.Frames[0]
	if !strings.Contains(v, glyph+" Hosts") {
		t.Errorf("no spinner before Hosts:\n%s", v)
	}
	if !strings.Contains(v, "Hosts\n") && !strings.Contains(v, "Hosts ") {
		t.Errorf("no pane title:\n%s", v)
	}
	if got := strings.Count(v, "\n") + 1; got != 30 {
		t.Errorf("view is %d lines, want 30", got)
	}
	for _, l := range strings.Split(v, "\n") {
		if w := lipgloss.Width(l); w > 100 {
			t.Errorf("line %q is %d wide", l, w)
		}
	}
}

func TestFullPlanRailAndTodos(t *testing.T) {
	m := sized(100, 30)
	m = feed(m,
		Event{Kind: KPlan, Text: "Keys\nHosts\nDone"},
		Event{Kind: KGroup, Name: "Keys"},
		Event{Kind: KTodo, Text: "open port 22"},
		Event{Kind: KTodo, Text: "add dns"},
	)
	v := plainView(m)
	for _, want := range []string{"· Hosts", "· Done", "2 left for you"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
}

func TestFullFailedAndPendingMarks(t *testing.T) {
	m := sized(100, 30)
	m = feed(m,
		Event{Kind: KGroup, Name: "A"},
		Event{Kind: KStep, ID: "a", Name: "x", State: Running},
		Event{Kind: KStep, ID: "a", Name: "x", State: Failed, Err: "boom"},
		Event{Kind: KGroup, Name: "B"},
		Event{Kind: KStep, ID: "b", Name: "y", State: Pending, Detail: "later"},
	)
	v := plainView(m)
	if !strings.Contains(v, "✗ A") || !strings.Contains(v, "• B") {
		t.Errorf("marks wrong:\n%s", v)
	}
}

func TestFullLogViewToggleAndScroll(t *testing.T) {
	m := keysAndHosts(sized(100, 30))
	for i := range 60 {
		m = feed(m, Event{Kind: KLine, ID: "s3", Text: "line " + string(rune('A'+i%26)) + strings.Repeat("x", i/26)})
	}
	if strings.Contains(plainView(m), "log of") {
		t.Fatal("log view open at start")
	}
	m, _ = updateFull(m, tea.KeyPressMsg{Code: 'l', Text: "l"})
	if !m.logView {
		t.Fatal("l did not open the log view")
	}
	tail := plainView(m)
	if !strings.Contains(tail, "flocc-1") || strings.Contains(tail, "line A\n") {
		t.Errorf("log view should follow the tail:\n%s", tail)
	}
	for range 5 {
		m, _ = updateFull(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	}
	if m.scroll == 0 {
		t.Error("PgUp did not scroll")
	}
	m, _ = updateFull(m, tea.KeyPressMsg{Code: 'l', Text: "l"})
	if m.logView {
		t.Error("l did not close the log view")
	}
}

func TestFullPaneShowsOnlyLastSixLines(t *testing.T) {
	m := keysAndHosts(sized(100, 30))
	for _, l := range []string{"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8"} {
		m = feed(m, Event{Kind: KLine, ID: "s3", Text: l})
	}
	v := plainView(m)
	if strings.Contains(v, "l2") || !strings.Contains(v, "l3") || !strings.Contains(v, "l8") {
		t.Errorf("pane tail wrong:\n%s", v)
	}
}

func TestFullSmallTerminalHandsOver(t *testing.T) {
	if m := sized(70, 20); !m.handover {
		t.Error("70x20 did not set handover")
	}
	if m := sized(80, 19); !m.handover {
		t.Error("80x19 did not set handover")
	}
	if m := sized(80, 20); m.handover {
		t.Error("80x20 set handover")
	}
}

func TestFullKeepsEveryEvent(t *testing.T) {
	m := keysAndHosts(sized(100, 30))
	if len(m.events) != 7 {
		t.Errorf("kept %d events, want 7", len(m.events))
	}
}

func TestFullQuitInterrupts(t *testing.T) {
	m := sized(100, 30)
	called := 0
	m.interrupt = func() { called++ }
	m, _ = updateFull(m, tea.KeyPressMsg{Code: 'q', Text: "q"})
	m, _ = updateFull(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if called != 2 || !m.interrupted {
		t.Errorf("interrupt called %d times, interrupted = %v", called, m.interrupted)
	}
}

func TestFullBarFollowsPlan(t *testing.T) {
	m := sized(100, 30)
	m = feed(m,
		Event{Kind: KPlan, Text: "A\nB\nC\nD"},
		Event{Kind: KGroup, Name: "A"},
		Event{Kind: KStep, ID: "a", Name: "x", State: Done},
	)
	if got := m.fraction(); got != 0.25 {
		t.Errorf("fraction = %v, want 0.25", got)
	}
}

func TestFullCloseQuits(t *testing.T) {
	m := sized(100, 30)
	_, cmd := updateFull(m, closeMsg{})
	if cmd == nil {
		t.Fatal("no quit command")
	}
}

func TestFullPromptOpensForm(t *testing.T) {
	m := sized(100, 30)
	reply := make(chan promptReply, 1)
	m, _ = updateFull(m, promptMsg{req: PromptReq{Kind: PConfirm, Q: Question{Title: "Adopt?"}}, reply: reply})
	if m.form == nil || !strings.Contains(plainView(m), "Adopt?") {
		t.Errorf("form not shown:\n%s", plainView(m))
	}
}
