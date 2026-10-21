package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// drain feeds the messages cmd produces back into the model, a few levels deep.
func drain(m fullModel, cmd tea.Cmd, depth int) fullModel {
	if cmd == nil || depth == 0 {
		return m
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			m = drain(m, c, depth-1)
		}
	default:
		var next tea.Cmd
		m, next = updateFull(m, msg)
		m = drain(m, next, depth-1)
	}
	return m
}

func press(m fullModel, msg tea.KeyPressMsg) fullModel {
	m, cmd := updateFull(m, msg)
	return drain(m, cmd, 4)
}

func TestModalPromptOverDimmedFrame(t *testing.T) {
	m := keysAndHosts(sized(100, 30))
	reply := make(chan promptReply, 1)
	m, _ = updateFull(m, promptMsg{req: PromptReq{Kind: PConfirm, Q: Question{Title: "Adopt flocc-1?", Facts: []string{"it is running"}}}, reply: reply})
	raw := m.View().Content
	lines := strings.Split(raw, "\n")
	if len(lines) != 30 {
		t.Fatalf("view is %d lines, want 30", len(lines))
	}
	box := -1
	for i, l := range lines {
		if strings.Contains(l, "╭") {
			box = i
			if !strings.Contains(raw, "Adopt flocc-1?") {
				t.Errorf("box lacks the title:\n%s", plainView(m))
			}
		}
	}
	if box < 8 || box > 20 {
		t.Errorf("box starts at row %d, want it centred", box)
	}
	for _, i := range []int{0, 1, 29} {
		if !strings.Contains(lines[i], "\x1b[2m") {
			t.Errorf("row %d is not faint: %q", i, lines[i])
		}
	}
	if !strings.Contains(plainView(m), "it is running") {
		t.Errorf("facts missing:\n%s", plainView(m))
	}
	if !m.View().AltScreen {
		t.Error("modal view lost the alt screen")
	}
}

func TestModalFrameUpdatesUnderneath(t *testing.T) {
	m := keysAndHosts(sized(100, 30))
	m, _ = updateFull(m, promptMsg{req: PromptReq{Kind: PConfirm, Q: Question{Title: "Adopt?"}}, reply: make(chan promptReply, 1)})
	m = feed(m, Event{Kind: KGroup, Name: "Later"}, Event{Kind: KStep, ID: "z", Name: "zed", State: Done, Detail: "ok"})
	if v := plainView(m); !strings.Contains(v, "Later") {
		t.Errorf("rail did not update under the modal:\n%s", v)
	}
}

func TestQOpensStopModal(t *testing.T) {
	m := sized(100, 30)
	m.title = "flocc setup"
	called := 0
	m.interrupt = func() { called++ }
	m = press(m, tea.KeyPressMsg{Code: 'q', Text: "q"})
	v := plainView(m)
	for _, want := range []string{"Stop flocc setup?", "The running step is cancelled.", "Stop", "Keep going"} {
		if !strings.Contains(v, want) {
			t.Errorf("stop modal lacks %q:\n%s", want, v)
		}
	}
	if called != 0 || m.interrupted {
		t.Error("q interrupted before the question was answered")
	}
}

func TestStopModalAnswers(t *testing.T) {
	for _, c := range []struct {
		name string
		keys []tea.KeyPressMsg
		stop bool
	}{
		{"stop", []tea.KeyPressMsg{{Code: 'y', Text: "y"}, {Code: tea.KeyEnter}}, true},
		{"keep going", []tea.KeyPressMsg{{Code: tea.KeyEnter}}, false},
	} {
		m := sized(100, 30)
		called := 0
		m.interrupt = func() { called++ }
		m = press(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		for _, k := range c.keys {
			m = press(m, k)
		}
		if m.form != nil {
			t.Errorf("%s: modal still open", c.name)
		}
		if (called == 1) != c.stop || m.interrupted != c.stop {
			t.Errorf("%s: interrupt called %d times, interrupted = %v", c.name, called, m.interrupted)
		}
	}
}
