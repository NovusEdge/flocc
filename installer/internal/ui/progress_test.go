package ui

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// manualTicks is a Ticker the test drives by hand. A send on the unbuffered channel returns once
// the spinner goroutine has taken the tick, so two sends in a row mean the first frame is drawn.
func manualTicks() (chan time.Time, func() (<-chan time.Time, func())) {
	ch := make(chan time.Time)
	return ch, func() (<-chan time.Time, func()) { return ch, func() {} }
}

func newTestProgress(b *bytes.Buffer, animate, color bool) (*Progress, *fakeClock, chan time.Time) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	ticks, ticker := manualTicks()
	return &Progress{Out: Out{W: b, Color: color}, Animate: animate, Now: clk.Now, Ticker: ticker, Cols: 80}, clk, ticks
}

var packages = Line{N: 3, Total: 9, Name: "Packages", Detail: "not installed"}

func TestPlainRunPrintsAStartLineAndADoneLine(t *testing.T) {
	var b bytes.Buffer
	p, clk, _ := newTestProgress(&b, false, false)
	err := p.Run(packages, func() (Outcome, error) {
		clk.Advance(52 * time.Second)
		return Outcome{Detail: "113 packages"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "  → 3/9 Packages       not installed\n  ✓ 3/9 Packages       113 packages (52s)\n"
	if b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
	if strings.ContainsAny(b.String(), "\r\x1b") {
		t.Fatalf("plain output holds a carriage return or escape: %q", b.String())
	}
}

func TestPlainRunPadsTheCounterToTheWidthOfTheTotal(t *testing.T) {
	var b bytes.Buffer
	p, _, _ := newTestProgress(&b, false, false)
	p.NameWidth = 10
	p.Done(Line{N: 2, Total: 12, Name: "Incus", Detail: "ready"})
	if want := "  ✓  2/12 Incus      ready\n"; b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
}

func TestPlainRunThatFailsReturnsTheErrorAfterACrossLine(t *testing.T) {
	var b bytes.Buffer
	p, _, _ := newTestProgress(&b, false, false)
	boom := errors.New("boom")
	if err := p.Run(packages, func() (Outcome, error) { return Outcome{}, boom }); err != boom {
		t.Fatalf("err=%v", err)
	}
	if want := "  → 3/9 Packages       not installed\n  ✗ 3/9 Packages (0s)\n"; b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
}

func TestPendingOutcomeEndsInAYellowDotNotACheckMark(t *testing.T) {
	var b bytes.Buffer
	p, _, _ := newTestProgress(&b, false, false)
	p.Run(packages, func() (Outcome, error) { return Outcome{Detail: "later", Pending: true}, nil })
	if !strings.Contains(b.String(), "  • 3/9 Packages       later (0s)\n") {
		t.Fatalf("%q", b.String())
	}
}

const clear = "\r\x1b[K"

func TestAnimatedRunDrawsFramesThenReplacesThemWithTheResult(t *testing.T) {
	var b bytes.Buffer
	p, clk, ticks := newTestProgress(&b, true, false)
	err := p.Run(packages, func() (Outcome, error) {
		clk.Advance(41 * time.Second)
		ticks <- time.Time{}
		ticks <- time.Time{} // returns only once the goroutine has finished drawing the first tick
		return Outcome{Detail: "113 packages"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := clear + "  ⠋ 3/9 Packages       not installed   0:00" +
		clear + "  ⠙ 3/9 Packages       not installed   0:41" +
		clear + "  ⠹ 3/9 Packages       not installed   0:41" +
		clear + "  ✓ 3/9 Packages       113 packages (41s)\n"
	if b.String() != want {
		t.Fatalf("got %q\nwant %q", b.String(), want)
	}
}

func TestAnimatedFramesAreOneWritePerRedrawWithNoNewline(t *testing.T) {
	var b bytes.Buffer
	p, _, ticks := newTestProgress(&b, true, false)
	p.Run(packages, func() (Outcome, error) {
		for range 12 {
			ticks <- time.Time{}
		}
		return Outcome{}, nil
	})
	body, _, _ := strings.Cut(b.String(), "  ✓")
	if strings.Contains(body, "\n") {
		t.Fatalf("a frame holds a newline: %q", body)
	}
	for _, f := range "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏" {
		if !strings.ContainsRune(body, f) {
			t.Errorf("frame %c never drawn: %q", f, body)
		}
	}
}

func TestAnimatedResultLineHasTheColoursOfOut(t *testing.T) {
	var b bytes.Buffer
	p, _, _ := newTestProgress(&b, true, true)
	p.Run(packages, func() (Outcome, error) { return Outcome{Detail: "ok"}, nil })
	if !strings.HasSuffix(b.String(), clear+"  \x1b[32m✓\x1b[0m 3/9 Packages       ok (0s)\n") {
		t.Fatalf("%q", b.String())
	}
	if !strings.HasPrefix(b.String(), clear+"  \x1b[33m⠋\x1b[0m 3/9 Packages") {
		t.Fatalf("%q", b.String())
	}
}

func TestAnimatedFailureReplacesTheLineWithARedCrossAndTheTimeItTook(t *testing.T) {
	var b bytes.Buffer
	p, clk, _ := newTestProgress(&b, true, true)
	boom := errors.New("boom")
	if err := p.Run(packages, func() (Outcome, error) { clk.Advance(12 * time.Second); return Outcome{}, boom }); err != boom {
		t.Fatalf("err=%v", err)
	}
	if !strings.HasSuffix(b.String(), clear+"  \x1b[31m✗\x1b[0m 3/9 Packages (12s)\n") || strings.Contains(b.String(), "✓") {
		t.Fatalf("%q", b.String())
	}
}

func TestPlainFailureAddsTheCrossLineAfterTheStartLine(t *testing.T) {
	var b bytes.Buffer
	p, clk, _ := newTestProgress(&b, false, false)
	boom := errors.New("boom")
	p.Run(packages, func() (Outcome, error) { clk.Advance(75 * time.Second); return Outcome{}, boom })
	if want := "  → 3/9 Packages       not installed\n  ✗ 3/9 Packages (1m15s)\n"; b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
}

func TestLiveLineStaysTwoColumnsUnderTheTerminalWidth(t *testing.T) {
	for _, cols := range []int{40, 50, 80} {
		var b bytes.Buffer
		p, _, _ := newTestProgress(&b, true, false)
		p.Cols = cols
		l := packages
		l.Detail = strings.Repeat("x", 200)
		p.Run(l, func() (Outcome, error) { return Outcome{}, errors.New("stop") })
		first := strings.Split(strings.TrimPrefix(b.String(), clear), clear)[0]
		if n := len([]rune(first)); n > cols-2 {
			t.Errorf("cols=%d: line is %d columns, the cursor would sit in the last one: %q", cols, n, first)
		}
	}
}

func TestWidthComesFromTheColumnsVariableWhenOutIsNotATerminal(t *testing.T) {
	t.Setenv("COLUMNS", "50")
	var b bytes.Buffer
	p, _, _ := newTestProgress(&b, true, false)
	p.Cols = 0
	l := packages
	l.Detail = strings.Repeat("x", 200)
	p.Run(l, func() (Outcome, error) { return Outcome{}, errors.New("stop") })
	first := strings.Split(strings.TrimPrefix(b.String(), clear), clear)[0]
	if n := len([]rune(first)); n > 48 || n < 40 {
		t.Fatalf("%d columns for COLUMNS=50: %q", n, first)
	}
	t.Setenv("COLUMNS", "junk")
	if got := p.cols(); got != 80 {
		t.Fatalf("a bad COLUMNS should fall back to 80, got %d", got)
	}
}

// segments splits animated output at each line clear: what is left is a frame, a line written by
// the step, or the result line.
func segments(s string) []string {
	return strings.Split(strings.TrimPrefix(s, clear), clear)
}

func TestOutputWrittenMidSpinNeverSharesALineWithAFrame(t *testing.T) {
	var b bytes.Buffer
	p, _, ticks := newTestProgress(&b, true, false)
	p.Run(packages, func() (Outcome, error) {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); ticks <- time.Time{} }()
		p.Quiet(func() { fmt.Fprintln(&b, "Doctor says hello") })
		wg.Wait()
		p.Quiet(func() { fmt.Fprint(&b, "second\nthird\n") })
		return Outcome{Detail: "ok"}, nil
	})
	var notes, frames int
	for _, seg := range segments(b.String()) {
		switch {
		case strings.Contains(seg, "⠋") || strings.Contains(seg, "⠙") || strings.Contains(seg, "⠹"):
			frames++
			if strings.Contains(seg, "\n") || strings.Contains(seg, "Doctor") || strings.Contains(seg, "second") {
				t.Errorf("a frame shares its segment with output: %q", seg)
			}
		case strings.Contains(seg, "Doctor says hello\n"), seg == "second\nthird\n":
			notes++
		case seg == "", strings.HasPrefix(seg, "  ✓ 3/9"):
		default:
			t.Errorf("unexpected segment %q", seg)
		}
	}
	if notes != 2 || frames < 3 {
		t.Fatalf("notes=%d frames=%d in %q", notes, frames, b.String())
	}
	if !strings.HasSuffix(b.String(), "ok (0s)\n") {
		t.Fatalf("%q", b.String())
	}
}

func TestQuietInPlainModeJustRuns(t *testing.T) {
	var b bytes.Buffer
	p, _, _ := newTestProgress(&b, false, false)
	p.Run(packages, func() (Outcome, error) {
		p.Quiet(func() { fmt.Fprintln(&b, "note") })
		return Outcome{}, nil
	})
	if strings.ContainsAny(b.String(), "\r\x1b") {
		t.Fatalf("%q", b.String())
	}
}

func TestAnimatedDetailIsCutToFitTheTerminalWidth(t *testing.T) {
	var b bytes.Buffer
	p, _, _ := newTestProgress(&b, true, false)
	p.Cols = 60
	l := packages
	l.Detail = strings.Repeat("x", 200)
	p.Run(l, func() (Outcome, error) { return Outcome{}, errors.New("stop") })
	first := strings.Split(strings.TrimPrefix(b.String(), clear), clear)[0]
	if n := len([]rune(first)); n > 58 || !strings.Contains(first, "…") || !strings.HasSuffix(first, "   0:00") {
		t.Fatalf("%d runes: %q", n, first)
	}
}

func TestDoneWithoutACounterKeepsTheCounterColumn(t *testing.T) {
	var b bytes.Buffer
	p, _, _ := newTestProgress(&b, false, false)
	p.NameWidth = 10
	p.Done(Line{N: 2, Total: 6, Name: "Docker", Detail: "ok"})
	p.Done(Line{Total: 6, Name: "Snapshot", Detail: "flocc-a/installed"})
	want := "  ✓ 2/6 Docker     ok\n  ✓     Snapshot   flocc-a/installed\n"
	if b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
}

func TestClockAndDurationFormats(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "0:00", 41 * time.Second: "0:41", 61 * time.Second: "1:01", 3725 * time.Second: "62:05"} {
		if got := clockFmt(d); got != want {
			t.Errorf("clockFmt(%v) = %q, want %q", d, got, want)
		}
	}
	for d, want := range map[time.Duration]string{400 * time.Millisecond: "0s", 52 * time.Second: "52s", 65 * time.Second: "1m05s", 3725 * time.Second: "62m05s"} {
		if got := tookFmt(d); got != want {
			t.Errorf("tookFmt(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestScriptedNoteDuringAnAnimatedStepClearsTheFrameFirst(t *testing.T) {
	var b bytes.Buffer
	p, _, _ := newTestProgress(&b, true, false)
	s := &Scripted{Out: &b, prog: p}
	s.Step(packages, func() (Outcome, error) {
		s.Note("flocc doctor", "all fine")
		return Outcome{Detail: "ok"}, nil
	})
	want := clear + "  ⠋ 3/9 Packages       not installed   0:00" +
		clear + "\nflocc doctor\n  all fine\n" +
		clear + "  ⠋ 3/9 Packages       not installed   0:00" +
		clear + "  ✓ 3/9 Packages       ok (0s)\n"
	if b.String() != want {
		t.Fatalf("got %q\nwant %q", b.String(), want)
	}
}

func TestScriptedDecidesAnimationByItsColorFlag(t *testing.T) {
	var plain, colour bytes.Buffer
	(&Scripted{Out: &plain}).Step(packages, func() (Outcome, error) { return Outcome{}, nil })
	if strings.ContainsAny(plain.String(), "\r\x1b") {
		t.Fatalf("%q", plain.String())
	}
	(&Scripted{Out: &colour, Color: true}).Step(packages, func() (Outcome, error) { return Outcome{}, nil })
	if !strings.Contains(colour.String(), "\r") {
		t.Fatalf("a coloured Scripted should animate: %q", colour.String())
	}
}
