package ship

import (
	"bytes"
	"io"

	"github.com/NovusEdge/flocc/installer/internal/steps"
)

// Summary is what the host's service and login phases end with; Print renders it.
type Summary = steps.Summary

// LineTap passes a stream through and takes out each line that starts with Marker and that Take
// accepts. The stream arrives in pty chunks, so the line can be split anywhere, and a prompt has no
// newline after it: a line is held back only while it could still turn into the marker.
type LineTap struct {
	Out    io.Writer
	Marker string
	// Take gets the line without its line ending; false passes it on.
	Take func(line string) bool

	held    []byte // the current line so far, while it is a prefix of the marker or starts with it
	passing bool   // the current line has diverged from the marker and goes straight through
}

func (t *LineTap) Write(p []byte) (int, error) {
	marker := []byte(t.Marker)
	var emit []byte
	for _, b := range p {
		if t.passing {
			emit = append(emit, b)
			t.passing = b != '\n'
			continue
		}
		t.held = append(t.held, b)
		if b == '\n' {
			if !t.Take(string(bytes.TrimRight(t.held, "\r\n"))) {
				emit = append(emit, t.held...)
			}
			t.held = t.held[:0]
		} else if !bytes.HasPrefix(marker, t.held) && !bytes.HasPrefix(t.held, marker) {
			emit = append(emit, t.held...)
			t.held, t.passing = t.held[:0], true
		}
	}
	if len(emit) > 0 {
		if _, err := t.Out.Write(emit); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush passes on a held partial line, which the stream ended in the middle of.
func (t *LineTap) Flush() {
	if len(t.held) > 0 {
		t.Out.Write(t.held)
		t.held = t.held[:0]
	}
}

// summaryTap takes out the flocc-summary line.
type summaryTap struct {
	LineTap
	sum  Summary
	have bool
}

func newSummaryTap(out io.Writer) *summaryTap {
	t := &summaryTap{}
	t.LineTap = LineTap{Out: out, Marker: steps.SummaryMarker, Take: func(line string) bool {
		s, ok := steps.ParseSummaryLine(line)
		if ok {
			t.sum, t.have = s, true
		}
		return ok
	}}
	return t
}

func (t *summaryTap) Summary() (Summary, bool) { return t.sum, t.have }
