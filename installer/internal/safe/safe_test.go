package safe

import (
	"strings"
	"testing"
)

func TestSanitizeStripsEscapesAndCuts(t *testing.T) {
	got := Sanitize("ok\x1b]52;c;aGk=\x07\x1b[2J" + strings.Repeat("x", 300))
	if strings.ContainsAny(got, "\x1b\x07") || len([]rune(got)) > maxField+1 {
		t.Fatalf("%q", got)
	}
}
