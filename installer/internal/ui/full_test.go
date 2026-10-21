package ui

import (
	"io"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

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

func TestBannerCmdPrintsWhyBeforeOutput(t *testing.T) {
	var buf strings.Builder
	c := &bannerCmd{cmd: exec.Command("sh", "-c", "echo hi"), why: "log in"}
	c.SetStdout(&buf)
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "── log in ──\nhi\n"; got != want {
		t.Errorf("output %q, want %q", got, want)
	}
	buf.Reset()
	c = &bannerCmd{cmd: exec.Command("sh", "-c", "echo hi"), why: "-"}
	c.SetStdout(&buf)
	c.Run()
	if buf.String() != "hi\n" {
		t.Errorf("why \"-\" printed a banner: %q", buf.String())
	}
}

func TestFullHandoffUsesBanner(t *testing.T) {
	m := sized(100, 30)
	_, cmd := updateFull(m, handoffMsg{why: "log in", cmd: exec.Command("true"), reply: make(chan error, 1)})
	if cmd == nil {
		t.Fatal("no cmd for a handoff")
	}
}

func TestRecordText(t *testing.T) {
	m := feed(sized(100, 30),
		Event{Kind: KGroup, Name: "Keys"},
		Event{Kind: KStep, ID: "s1", Name: "ssh key", State: Running},
		Event{Kind: KStep, ID: "s1", Name: "ssh key", State: Done, Detail: "ed25519"},
		Event{Kind: KStep, ID: "s2", Name: "still going", State: Running},
		Event{Kind: KGroup, Name: "Empty"},
		Event{Kind: KGroup, Name: "Hosts"},
		Event{Kind: KStep, ID: "s3", Name: "flocc-1", State: Failed, Err: "boom"},
		Event{Kind: KLog, Text: "/tmp/run.log"},
	)
	got := ansiRE.ReplaceAllString(recordText(m, []string{"open port 22"}), "")
	want := "\nKeys\n  ✓ ssh key" + strings.Repeat(" ", nameWidth-len("ssh key")) + " ed25519\n" +
		"\nHosts\n  ✗ flocc-1" + strings.Repeat(" ", nameWidth-len("flocc-1")) + " boom\n" +
		"\nStill yours to do\n  • open port 22\n\nLog: /tmp/run.log\n"
	if got != want {
		t.Errorf("record:\n%q\nwant:\n%q", got, want)
	}
}

func waitDone(t *testing.T, r *full) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("full renderer never ended")
	}
}

func TestFullProgramWritesRecordOnClose(t *testing.T) {
	var out syncBuf
	pr, pw := io.Pipe()
	defer pw.Close()
	s := New(newFull(&out, pr, "flocc setup", tea.WithWindowSize(100, 30)))
	s.Group("Keys")
	s.Step("ssh key").Done("ed25519")
	s.Todo("open port 22")
	s.LogPath("/tmp/run.log")
	s.Close()
	got := ansiRE.ReplaceAllString(out.String(), "")
	i := strings.LastIndex(got, "Keys\n")
	if i < 0 || !strings.Contains(got[i:], "✓ ssh key") || !strings.Contains(got[i:], "open port 22") || !strings.Contains(got[i:], "Log: /tmp/run.log") {
		t.Errorf("record missing after the frame: %q", got)
	}
}

func TestFullHandsOverToInlineBelowMinimum(t *testing.T) {
	var out syncBuf
	pr, pw := io.Pipe()
	defer pw.Close()
	rec := &Recorder{}
	r := newFullWith(&out, pr, "flocc setup", func(io.Writer, io.Reader) Renderer { return rec }, tea.WithWindowSize(70, 20))
	waitDone(t, r)
	// Draws after the swap go straight to the inline renderer.
	r.Draw(Event{Kind: KStep, ID: "b", Name: "second", State: Done})
	r.Close([]string{"todo"})
	var names []string
	for _, e := range rec.Rows() {
		names = append(names, e.Name)
	}
	if !rec.Closed || len(rec.Todos) != 1 {
		t.Errorf("inline renderer not closed with the to-dos: %+v", rec)
	}
	if want := []string{"second"}; !slices.Equal(names, want) {
		t.Errorf("rows %v, want %v", names, want)
	}
}

func TestFullHandOverReplaysEventsInOrder(t *testing.T) {
	var out syncBuf
	pr, pw := io.Pipe()
	defer pw.Close()
	rec := &Recorder{}
	// The size message arrives with the program's first events, so draw before it can quit.
	r := newFullWith(&out, pr, "t", func(io.Writer, io.Reader) Renderer { return rec })
	evs := []Event{
		{Kind: KGroup, Name: "G"},
		{Kind: KStep, ID: "a", Name: "first", State: Running},
		{Kind: KStep, ID: "a", Name: "first", State: Done},
		{Kind: KStep, ID: "b", Name: "live", State: Running},
	}
	for _, e := range evs {
		r.Draw(e)
	}
	r.p.Send(tea.WindowSizeMsg{Width: 60, Height: 10})
	waitDone(t, r)
	r.Draw(Event{Kind: KStep, ID: "b", Name: "live", State: Done})
	r.Close(nil)
	if !slices.Equal(rec.Events[:len(evs)], evs) {
		t.Errorf("replay %v, want %v", rec.Events, evs)
	}
	if last := rec.Events[len(rec.Events)-1]; last.ID != "b" || last.State != Done {
		t.Errorf("later draw not forwarded: %v", last)
	}
}

func TestFullPromptAfterHandOverGoesToInline(t *testing.T) {
	var out syncBuf
	pr, pw := io.Pipe()
	defer pw.Close()
	rec := &Recorder{Answers: []any{true}}
	r := newFullWith(&out, pr, "t", func(io.Writer, io.Reader) Renderer { return rec }, tea.WithWindowSize(60, 10))
	waitDone(t, r)
	v, err := r.Prompt(PromptReq{Kind: PConfirm, Q: Question{Title: "ok?"}})
	if err != nil || v != true || len(rec.Asked) != 1 {
		t.Errorf("Prompt = %v, %v; asked %d", v, err, len(rec.Asked))
	}
	r.Close(nil)
}

func TestFullPanicRestoresTerminalAndPrintsRecord(t *testing.T) {
	var out syncBuf
	pr, pw := io.Pipe()
	defer pw.Close()
	r := newFull(&out, pr, "t", tea.WithWindowSize(100, 30))
	r.Draw(Event{Kind: KStep, ID: "a", Name: "first", State: Done, Detail: "ok"})
	r.Draw(Event{Kind: KTodo, Text: "do this"})
	// Kill before Run has started races inside Bubble Tea; Draw used to wait for the loop.
	for out.String() == "" {
		time.Sleep(5 * time.Millisecond)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("restoreOnPanic swallowed the panic")
			}
		}()
		defer r.restoreOnPanic()
		panic("boom")
	}()
	waitDone(t, r)
	got := ansiRE.ReplaceAllString(out.String(), "")
	if !strings.Contains(got, "✓ first") || !strings.Contains(got, "do this") {
		t.Errorf("no record after the panic: %q", got)
	}
}
