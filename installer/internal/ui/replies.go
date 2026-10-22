package ui

import (
	"io"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"
)

// replyWait is how long a handoff waits for the terminal's answers to the program's startup
// queries. A terminal that ignores them (the kitty keyboard query, the background colour) never
// answers, so the wait must end.
const replyWait = 500 * time.Millisecond

// replyDeadline ends the wait for the replies.
type replyDeadline struct{}

func replyTimer() tea.Cmd {
	return tea.Tick(replyWait, func(time.Time) tea.Msg { return replyDeadline{} })
}

// replyGate counts the replies to queries the program just sent. A reply that lands after a child
// process has taken the tty is echoed into its output (`^[[?1u`).
type replyGate struct {
	pending int
	until   time.Time
}

func (g *replyGate) want(n int, now time.Time) { g.pending, g.until = n, now.Add(replyWait) }

// got reports whether msg is a reply the gate was waiting for.
func (g *replyGate) got(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyboardEnhancementsMsg, tea.BackgroundColorMsg:
		if g.pending > 0 {
			g.pending--
			return true
		}
	}
	return false
}

func (g replyGate) ready(now time.Time) bool { return g.pending <= 0 || !now.Before(g.until) }

// handoffHold keeps a handoff back until the terminal has answered. Bubble Tea sends the keyboard
// query with the first frame and not again after an exec (cursed_renderer.go flush: the query goes
// out only when there is no previous view or its keyboard enhancements or screen changed), so the
// two replies are all there is to wait for. The session sends one handoff at a time and waits for
// its reply, so one slot is enough.
type handoffHold struct {
	gate replyGate
	held *handoffMsg
	in   io.Reader
}

func newHandoffHold(now time.Time) handoffHold {
	var h handoffHold
	h.gate.want(2, now)
	return h
}

// step counts msg if it is a reply and takes it in if it is a handoff (consumed). start is the
// handoff to run now, when one is held and the terminal has answered or the wait is over. The
// input is flushed first: a reply that arrived but was not read yet would go to the child.
func (h *handoffHold) step(msg tea.Msg, now time.Time) (start *handoffMsg, consumed bool) {
	h.gate.got(msg)
	if hm, ok := msg.(handoffMsg); ok {
		h.held, consumed = &hm, true
	}
	if h.held == nil || !h.gate.ready(now) {
		return nil, consumed
	}
	start, h.held = h.held, nil
	flushInput(h.in)
	return start, consumed
}

// flushInput discards what the terminal sent and nobody read, when in is a terminal.
var flushInput = func(in io.Reader) {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		flushTTY(int(f.Fd()))
	}
}
