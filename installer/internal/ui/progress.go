package ui

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

var spinRunes = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// clearLine returns the cursor to column 0 and erases to the end of the line.
const clearLine = "\r\x1b[K"

// Line names one step in a progress listing: step N of Total, with its check Detail.
type Line struct {
	N, Total     int
	Name, Detail string
	// Attached marks a step that reads the terminal or draws on it (a form, a QR code, an attached
	// ssh): it gets plain start and done lines even where other steps animate.
	Attached bool
}

// Outcome is how a step ended well. Pending ends it in a yellow dot: the step did what it could
// and needs the owner afterwards.
type Outcome struct {
	Detail  string
	Pending bool
}

// Progress shows the step being applied as one live line, "⠙ 3/9 Packages  detail  0:41", and
// replaces it with the result when the step ends. Without Animate, which follows the colour rule,
// it prints a start line and a done line and never writes a carriage return or an escape code
// of its own, so logs and the host's pipe stay clean.
//
// Whatever else writes to the same writer while a step runs must do it through Quiet.
type Progress struct {
	Out       Out
	Animate   bool
	NameWidth int // 14 when zero, the width ui.Scripted's plan lines use
	Cols      int // terminal width the live line must fit; looked up from Out.W when zero
	Now       func() time.Time
	// Ticker returns the redraw ticks and a func that stops them; every 100ms when nil.
	Ticker func() (<-chan time.Time, func())

	mu   sync.Mutex // serialises writes to Out.W while a live line is on screen
	live *liveLine
}

type liveLine struct {
	l     Line
	start time.Time
	frame int
}

// NewProgress animates when w is a terminal, NO_COLOR is unset and TERM is not dumb.
func NewProgress(w io.Writer) *Progress {
	o := NewOut(w)
	return &Progress{Out: o, Animate: o.Color}
}

func (p *Progress) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Progress) ticker() (<-chan time.Time, func()) {
	if p.Ticker != nil {
		return p.Ticker()
	}
	t := time.NewTicker(100 * time.Millisecond)
	return t.C, t.Stop
}

func (p *Progress) cols() int {
	if p.Cols > 0 {
		return p.Cols
	}
	if f, ok := p.Out.W.(*os.File); ok {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			return w
		}
	}
	return 80
}

// text is one line without its newline: the mark, the counter, the name padded to its column and
// the detail, then suffix.
func (p *Progress) text(mark string, l Line, detail, suffix string) string {
	w := p.NameWidth
	if w == 0 {
		w = 14
	}
	counter := fmt.Sprintf("%*d/%d", len(strconv.Itoa(l.Total)), l.N, l.Total)
	s := fmt.Sprintf("%s%s %s %-*s %s%s", indent, mark, counter, w, l.Name, detail, suffix)
	return strings.TrimRight(s, " ")
}

// frame is the live line. The detail is cut so the whole line fits one terminal row: a wrapped
// line would leave a copy of itself behind on every redraw.
func (p *Progress) frame(v *liveLine) string {
	glyph := spinRunes[v.frame%len(spinRunes)]
	suffix := "   " + clockFmt(p.now().Sub(v.start))
	fixed := utf8.RuneCountInString(p.text("x", v.l, "", suffix))
	room := max(p.cols()-1-fixed, 10)
	detail := v.l.Detail
	if r := []rune(detail); len(r) > room {
		detail = string(r[:room-1]) + "…"
	}
	return p.text(p.Out.Warn(string(glyph)), v.l, detail, suffix)
}

// draw replaces the line on screen with the live frame. The caller holds mu.
func (p *Progress) draw() {
	io.WriteString(p.Out.W, clearLine+p.frame(p.live))
}

// Done prints the line of a step that needed no work.
func (p *Progress) Done(l Line) {
	p.Quiet(func() { io.WriteString(p.Out.W, p.text(p.Out.OK("✓"), l, l.Detail, "")+"\n") })
}

// Run applies a step under a live line. An error clears the line and is returned as it is, for
// the caller's own failure output; in plain mode the start line stays.
func (p *Progress) Run(l Line, fn func() (Outcome, error)) error {
	start := p.now()
	var out Outcome
	var err error
	if p.Animate && !l.Attached {
		out, err = p.spin(l, start, fn)
	} else {
		p.Quiet(func() { io.WriteString(p.Out.W, p.text("→", l, l.Detail, "")+"\n") })
		out, err = fn()
	}
	if err != nil {
		return err
	}
	mark := p.Out.OK("✓")
	if out.Pending {
		mark = p.Out.Warn("•")
	}
	p.Quiet(func() {
		io.WriteString(p.Out.W, p.text(mark, l, out.Detail, " ("+tookFmt(p.now().Sub(start))+")")+"\n")
	})
	return nil
}

func (p *Progress) spin(l Line, start time.Time, fn func() (Outcome, error)) (Outcome, error) {
	p.mu.Lock()
	p.live = &liveLine{l: l, start: start}
	p.draw()
	p.mu.Unlock()

	ticks, stop := p.ticker()
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ticks:
				p.mu.Lock()
				p.live.frame++
				p.draw()
				p.mu.Unlock()
			case <-quit:
				return
			}
		}
	}()
	defer func() {
		close(quit)
		<-done
		stop()
		p.mu.Lock()
		p.live = nil
		io.WriteString(p.Out.W, clearLine)
		p.mu.Unlock()
	}()
	return fn()
}

// Quiet runs fn, which writes whole lines to Out.W, with the live line cleared and drawn again
// after. Without a live line it only keeps fn from interleaving with a redraw.
func (p *Progress) Quiet(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.live == nil {
		fn()
		return
	}
	io.WriteString(p.Out.W, clearLine)
	fn()
	p.draw()
}

// clockFmt is the elapsed time of the live line, m:ss.
func clockFmt(d time.Duration) string {
	s := int(d / time.Second)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// tookFmt is the time a finished step took: 52s, 1m05s.
func tookFmt(d time.Duration) string {
	s := int(d / time.Second)
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}
