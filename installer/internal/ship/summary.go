package ship

import (
	"bytes"
	"io"

	"github.com/NovusEdge/flocc/installer/internal/steps"
)

// Summary is what the host's service and login phases end with; Print renders it.
type Summary = steps.Summary

var marker = []byte(steps.SummaryMarker)

// summaryTap passes the host's output through and takes out the flocc-summary line. The output
// arrives in pty chunks, so the line can be split anywhere, and the sign-in prompt has no newline
// after it: a line is held back only while it could still turn into the marker.
type summaryTap struct {
	out     io.Writer
	held    []byte // the current line so far, while it is a prefix of the marker or starts with it
	passing bool   // the current line has diverged from the marker and goes straight through
	sum     Summary
	have    bool
}

func (t *summaryTap) Write(p []byte) (int, error) {
	var emit []byte
	for _, b := range p {
		if t.passing {
			emit = append(emit, b)
			t.passing = b != '\n'
			continue
		}
		t.held = append(t.held, b)
		if b == '\n' {
			if s, ok := steps.ParseSummaryLine(string(bytes.TrimRight(t.held, "\r\n"))); ok {
				t.sum, t.have = s, true
			} else {
				emit = append(emit, t.held...)
			}
			t.held = t.held[:0]
		} else if !bytes.HasPrefix(marker, t.held) && !bytes.HasPrefix(t.held, marker) {
			emit = append(emit, t.held...)
			t.held, t.passing = t.held[:0], true
		}
	}
	if len(emit) > 0 {
		if _, err := t.out.Write(emit); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush passes on a held partial line, which the stream ended in the middle of.
func (t *summaryTap) Flush() {
	if len(t.held) > 0 {
		t.out.Write(t.held)
		t.held = t.held[:0]
	}
}

func (t *summaryTap) Summary() (Summary, bool) { return t.sum, t.have }
