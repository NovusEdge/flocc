package ui

import (
	"fmt"
	"image/color"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// driver feeds one model's Update, whichever renderer it belongs to.
type driver struct {
	send func(tea.Msg) tea.Cmd
	in   io.Reader
}

func drivers(now *time.Time, in io.Reader) map[string]*driver {
	clock := func() time.Time { return *now }
	fm := newFullModel(clock, "dev")
	fm.hold.in = in
	im := newInlineModel(clock)
	im.hold.in = in
	return map[string]*driver{
		"full": {in: in, send: func(msg tea.Msg) tea.Cmd {
			next, cmd := fm.Update(msg)
			fm = next.(fullModel)
			return cmd
		}},
		"inline": {in: in, send: func(msg tea.Msg) tea.Cmd {
			next, cmd := im.Update(msg)
			im = next.(inlineModel)
			return cmd
		}},
	}
}

func handoff() handoffMsg {
	return handoffMsg{why: "-", cmd: exec.Command("true"), reply: make(chan error, 1)}
}

func isExec(cmd tea.Cmd) bool { return cmd != nil && fmt.Sprintf("%T", cmd()) == "tea.execMsg" }

var (
	kbReply = tea.KeyboardEnhancementsMsg{}
	bgReply = tea.BackgroundColorMsg{Color: color.Black}
)

// flushes replaces flushInput for the test and records each input it was asked to flush.
func flushes(t *testing.T) *[]io.Reader {
	t.Helper()
	var got []io.Reader
	old := flushInput
	flushInput = func(in io.Reader) { got = append(got, in) }
	t.Cleanup(func() { flushInput = old })
	return &got
}

func TestHandoffWaitsForBothReplies(t *testing.T) {
	for name := range drivers(new(time.Time), nil) {
		t.Run(name, func(t *testing.T) {
			got := flushes(t)
			in := strings.NewReader("")
			now := time.Unix(0, 0)
			d := drivers(&now, in)[name]
			if cmd := d.send(handoff()); cmd != nil {
				t.Fatal("handoff started before any reply")
			}
			if cmd := d.send(kbReply); isExec(cmd) {
				t.Fatal("handoff started with one reply of two missing")
			}
			if len(*got) != 0 {
				t.Fatal("input flushed while the handoff was held")
			}
			cmd := d.send(bgReply)
			if !isExec(cmd) {
				t.Fatalf("held handoff did not start after both replies: %v", cmd)
			}
			if len(*got) != 1 || (*got)[0] != in {
				t.Errorf("flushInput calls = %v, want one on the model's input", *got)
			}
		})
	}
}

func TestHandoffStartsAtTheDeadlineWithoutReplies(t *testing.T) {
	for name := range drivers(new(time.Time), nil) {
		t.Run(name, func(t *testing.T) {
			got := flushes(t)
			now := time.Unix(0, 0)
			d := drivers(&now, nil)[name]
			d.send(handoff())
			if cmd := d.send(replyDeadline{}); isExec(cmd) {
				t.Fatal("a deadline tick before replyWait started the handoff")
			}
			now = now.Add(replyWait)
			if cmd := d.send(replyDeadline{}); !isExec(cmd) {
				t.Fatalf("deadline did not start the held handoff: %v", cmd)
			}
			if len(*got) != 1 {
				t.Errorf("flushInput ran %d times, want 1", len(*got))
			}
		})
	}
}

// bubbletea sends the keyboard query with the first frame only (cursed_renderer.go flush): a
// restore after an exec re-enables the protocol without asking, so a second handoff has no
// reply to wait for.
func TestHandoffAfterTheRepliesStartsAtOnce(t *testing.T) {
	for name := range drivers(new(time.Time), nil) {
		t.Run(name, func(t *testing.T) {
			got := flushes(t)
			now := time.Unix(0, 0)
			d := drivers(&now, nil)[name]
			d.send(kbReply)
			d.send(bgReply)
			for i := range 2 {
				if !isExec(d.send(handoff())) {
					t.Fatalf("handoff %d waited although the replies were in", i+1)
				}
			}
			if len(*got) != 2 {
				t.Errorf("flushInput ran %d times, want once per handoff", len(*got))
			}
		})
	}
}

func TestGateCountsOnlyWhatWasAsked(t *testing.T) {
	var g replyGate
	now := time.Unix(0, 0)
	if g.got(kbReply) {
		t.Error("counted a reply nobody asked for")
	}
	g.want(2, now)
	if !g.got(kbReply) || !g.got(bgReply) || g.got(kbReply) {
		t.Error("gate did not count exactly two replies")
	}
	if !g.ready(now) {
		t.Error("gate not ready with every reply in")
	}
	g.want(1, now)
	if g.ready(now) || !g.ready(now.Add(replyWait)) {
		t.Error("gate ready state does not follow replyWait")
	}
	if g.got(tea.KeyPressMsg{Code: 'x'}) {
		t.Error("counted a key press as a reply")
	}
}
