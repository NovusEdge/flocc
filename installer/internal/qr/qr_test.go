package qr

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteDrawsBlocksWithCRLFLines(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, "https://claude.ai/oauth/authorize?code=true&state=abc"); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	lines := strings.Count(out, "\n")
	if !strings.Contains(out, "█") || lines < 10 || strings.Count(out, "\r\n") != lines {
		t.Fatalf("%d lines, %d CRLF:\n%q", lines, strings.Count(out, "\r\n"), out)
	}
}

func TestWriteRefusesTextThatDoesNotFit(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, strings.Repeat("a", 5000)); err == nil || b.Len() != 0 {
		t.Fatalf("err=%v wrote %d bytes", err, b.Len())
	}
}
