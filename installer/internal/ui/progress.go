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

// ColumnsEnv carries the desktop terminal's width to flocc-setup, whose stdout is a pipe.
const ColumnsEnv = "COLUMNS"

// Width is the width of w when it is a terminal, else 0.
func Width(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if c, _, err := term.GetSize(int(f.Fd())); err == nil && c > 0 {
			return c
		}
	}
	return 0
}

// cols is the width the live line must fit: set by the caller, the terminal's own, or the
// desktop's as ColumnsEnv carried it across the pipe.
func (p *Progress) cols() int {
	if p.Cols > 0 {
		return p.Cols
	}
	if w := Width(p.Out.W); w > 0 {
		return w
	}
	if n, err := strconv.Atoi(os.Getenv(ColumnsEnv)); err == nil && n > 0 {
		return n
	}
	return 80
}

// head is a line up to the name: the mark and the counter, blank for a line that belongs to no step.
func (p *Progress) head(mark string, l Line) string {
	if l.Total == 0 {
		return indent + mark + " "
	}
	w := len(strconv.Itoa(l.Total))
	counter := strings.Repeat(" ", 2*w+1)
	if l.N > 0 {
		counter = fmt.Sprintf("%*d/%d", w, l.N, l.Total)
	}
	return indent + mark + " " + counter + " "
}

// text is one line without its newline: the mark, the counter, the name padded to its column and
// the detail, then suffix.
func (p *Progress) text(mark string, l Line, detail, suffix string) string {
	w := p.NameWidth
	if w == 0 {
		w = 14
	}
	s := fmt.Sprintf("%s%-*s %s%s", p.head(mark, l), w, l.Name, detail, suffix)
	return strings.TrimRight(s, " ")
}

// frame is the live line. The detail is cut so the whole line fits one terminal row: a wrapped
// line would leave a copy of itself behind on every redraw. It stays two columns short of the
// width, so the cursor never rests in the last column, where terminals differ on wrapping.
func (p *Progress) frame(v *liveLine) string {
	glyph := spinRunes[v.frame%len(spinRunes)]
	suffix := "   " + clockFmt(p.now().Sub(v.start))
	fixed := utf8.RuneCountInString(p.text("x", v.l, "", suffix))
	room := max(p.cols()-2-fixed, 1)
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

// Run applies a step under a live line. An error ends it in a red "✗ 3/9 Name (12s)" line and is
// returned as it is, for the caller's own failure output.
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
		p.Quiet(func() {
			io.WriteString(p.Out.W, p.head(p.Out.Fail("✗"), l)+l.Name+" ("+tookFmt(p.now().Sub(start))+")\n")
		})
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
