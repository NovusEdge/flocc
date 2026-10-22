package ui

import (
	"image/color"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/flocc/installer/internal/ui/theme"
)

var fixedNow = func() time.Time { return time.Unix(0, 0) }

func step(id, name string, st State, detail string) drawMsg {
	return drawMsg{Event{Kind: KStep, ID: id, Name: name, State: st, Detail: detail}}
}

func update(m inlineModel, msg tea.Msg) (inlineModel, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(inlineModel), cmd
}

func TestInlineLiveAndEnded(t *testing.T) {
	m := newInlineModel(fixedNow)
	m, _ = update(m, tea.WindowSizeMsg{Width: 60, Height: 20})
	m, _ = update(m, step("s1", "Packages", Running, "apt-get install"))
	if v := m.View().Content; !strings.Contains(v, "Packages") || !strings.Contains(v, "apt-get install") {
		t.Fatalf("live view %q", v)
	}
	m, cmd := update(m, step("s1", "Packages", Done, "113 packages"))
	if strings.Contains(m.View().Content, "Packages") {
		t.Error("ended step still live")
	}
	if cmd == nil {
		t.Fatal("no Println for the ended row")
	}
}

func upMark(dark bool) string { return theme.New(dark).Up.Render("✓") }

func TestInlineAsksTheBackgroundAndSwitchesPalette(t *testing.T) {
	m := newInlineModel(fixedNow)
	m.th = theme.New(true)
	asked := false
	if batch, ok := m.Init()().(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil && c() == tea.RequestBackgroundColor() {
				asked = true
			}
		}
	}
	if !asked {
		t.Error("Init does not request the background colour")
	}
	m, _ = update(m, promptMsg{req: PromptReq{Kind: PConfirm, Q: Question{Title: "x?"}}, reply: make(chan promptReply, 1)})
	m, _ = update(m, step("a", "one", Done, "ok"))
	m, _ = update(m, tea.BackgroundColorMsg{Color: color.White})
	m, _ = update(m, step("b", "two", Done, "ok"))
	if !strings.Contains(m.queue[0], upMark(true)) || !strings.Contains(m.queue[1], upMark(false)) {
		t.Errorf("rows did not switch from dark to light: %q", m.queue)
	}
}

func TestInlineSpinnerIsAccent(t *testing.T) {
	m := newInlineModel(fixedNow)
	m.th = theme.New(true)
	m, _ = update(m, tea.BackgroundColorMsg{Color: color.Black})
	m, _ = update(m, step("s", "Packages", Running, ""))
	if want := theme.New(true).Accent.Render(spinner.MiniDot.Frames[0]); !strings.Contains(m.View().Content, want) {
		t.Errorf("spinner not in accent: %q", m.View().Content)
	}
}

func TestInlineTailLinesAreDim(t *testing.T) {
	m := newInlineModel(fixedNow)
	m.th = theme.New(true)
	m, _ = update(m, tea.WindowSizeMsg{Width: 60, Height: 10})
	m, _ = update(m, step("s", "Packages", Running, ""))
	m, _ = update(m, drawMsg{Event{Kind: KLine, ID: "s", Text: "tail"}})
	if !strings.Contains(m.View().Content, m.th.Dim.Render(strings.Repeat(" ", 6)+"tail")) {
		t.Errorf("tail not dim: %q", m.View().Content)
	}
}

func TestInlineFormFollowsTheBackground(t *testing.T) {
	accent := func(dark bool) string { return strings.Split(theme.New(dark).Accent.Render("x"), "x")[0] }
	open := func(bg color.Color) inlineModel {
		m := newInlineModel(fixedNow)
		m.th = theme.New(true)
		if bg != nil {
			m, _ = update(m, tea.BackgroundColorMsg{Color: bg})
		}
		m, _ = update(m, promptMsg{req: PromptReq{Kind: PChoose, Q: Question{Title: "pick"}, Options: []string{"a", "b"}}, reply: make(chan promptReply, 1)})
		return m
	}
	if v := open(color.White).View().Content; !strings.Contains(v, accent(false)) || strings.Contains(v, accent(true)) {
		t.Errorf("form opened on a light terminal is not light: %q", v)
	}
	if v := open(nil).View().Content; !strings.Contains(v, accent(true)) || strings.Contains(v, accent(false)) {
		t.Errorf("form opened on a dark terminal is not dark: %q", v)
	}
	m := open(nil)
	m, _ = update(m, tea.BackgroundColorMsg{Color: color.White})
	if v := m.View().Content; !strings.Contains(v, accent(false)) || strings.Contains(v, accent(true)) {
		t.Errorf("open form kept the dark palette after a white background: %q", v)
	}
}

func TestInlineQueuesRowsWhilePromptOpen(t *testing.T) {
	m := newInlineModel(fixedNow)
	reply := make(chan promptReply, 1)
	m, _ = update(m, promptMsg{req: PromptReq{Kind: PConfirm, Q: Question{Title: "Adopt?"}}, reply: reply})
	m, _ = update(m, step("s2", "flocc-2", Done, "ok"))
	if len(m.queue) != 1 {
		t.Fatalf("queue %v", m.queue)
	}
}

func TestInlineCutsToWidth(t *testing.T) {
	m := newInlineModel(fixedNow)
	m, _ = update(m, tea.WindowSizeMsg{Width: 30, Height: 10})
	m, _ = update(m, step("s1", "Packages", Running, strings.Repeat("x", 100)))
	m, _ = update(m, drawMsg{Event{Kind: KLine, ID: "s1", Text: strings.Repeat("y", 100)}})
	for _, l := range strings.Split(m.View().Content, "\n") {
		if w := lipgloss.Width(l); w > 28 {
			t.Errorf("line %q is %d wide", l, w)
		}
	}
}

func TestInlineHandoffReturnsCmd(t *testing.T) {
	m := newInlineModel(fixedNow)
	m, _ = update(m, kbReply)
	m, _ = update(m, bgReply)
	_, cmd := update(m, handoffMsg{why: "login", cmd: exec.Command("true"), reply: make(chan error, 1)})
	if cmd == nil {
		t.Fatal("no cmd for a handoff")
	}
}

func TestInlineCtrlCWithoutFormInterrupts(t *testing.T) {
	m := newInlineModel(fixedNow)
	called := false
	m.interrupt = func() { called = true }
	m, _ = update(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !m.interrupted || !called {
		t.Errorf("interrupted = %v, signalled = %v", m.interrupted, called)
	}
}

func TestInlineKeepsLastThreeLines(t *testing.T) {
	m := newInlineModel(fixedNow)
	m, _ = update(m, step("s1", "Packages", Running, ""))
	for _, l := range []string{"a", "b", "c", "d"} {
		m, _ = update(m, drawMsg{Event{Kind: KLine, ID: "s1", Text: l}})
	}
	if got := strings.Join(m.steps[0].lines, ","); got != "b,c,d" {
		t.Errorf("lines %q, want b,c,d", got)
	}
}

func TestInlineGroupIsBold(t *testing.T) {
	if got := groupText("Old versions"); got != "\n\x1b[1mOld versions\x1b[0m" {
		t.Errorf("groupText = %q", got)
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestInlineProgramAnswersConfirm(t *testing.T) {
	var out syncBuf
	pr, pw := io.Pipe()
	defer pw.Close()
	s := New(newInline(&out, pr, tea.WithWindowSize(80, 24)))
	s.Group("Old versions")
	s.Step("a").Done("ok")
	type res struct {
		ok  bool
		err error
	}
	got := make(chan res, 1)
	go func() {
		ok, err := s.Confirm(Question{Title: "Adopt?", Default: "yes"})
		got <- res{ok, err}
	}()
	deadline := time.After(5 * time.Second)
	for !strings.Contains(out.String(), "Adopt?") {
		select {
		case <-deadline:
			t.Fatalf("form never drew: %q", out.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	pw.Write([]byte("\r"))
	select {
	case g := <-got:
		if g.err != nil || !g.ok {
			t.Errorf("Confirm = %v, %v", g.ok, g.err)
		}
	case <-deadline:
		t.Fatal("Confirm never returned")
	}
	s.Close()
	if !strings.Contains(out.String(), "Old versions") || !strings.Contains(out.String(), "ok") {
		t.Errorf("scrollback missing rows: %q", out.String())
	}
}

// tea.ExecProcess blocks the program loop, so a draw from the child's own stdout must not wait for it.
func TestInlineDrawDuringAHandoffDoesNotBlock(t *testing.T) {
	var out syncBuf
	s := New(newInline(&out, strings.NewReader(""), tea.WithWindowSize(80, 24)))
	st := s.Step("Replayed")
	c := exec.Command("sh", "-c", "echo x; sleep 0.3")
	c.Stdout = lineWriter(func(l string) { st.Line(l) })
	done := make(chan error, 1)
	go func() { done <- s.Handoff("-", c) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handoff hung on a draw from its own output")
	}
	st.Done("ok")
	s.Close()
}

type lineWriter func(string)

func (w lineWriter) Write(p []byte) (int, error) {
	w(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
