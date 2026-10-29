// Package safe makes text from a remote host safe to print and names the host part of a machine.
package safe

import (
	"strings"
	"unicode"
)

// HostPart is machine without a user part: the owner's destination may be owner@host.
func HostPart(machine string) string {
	if i := strings.LastIndex(machine, "@"); i >= 0 {
		return machine[i+1:]
	}
	return machine
}

// maxField bounds each string a remote binary prints; the remote can rewrite them.
const maxField = 200

// Sanitize makes text from a remote binary safe to print: no control characters, so no escape
// sequence reaches the terminal, and no field longer than maxField runes.
func Sanitize(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == maxField {
			b.WriteString("…")
			break
		}
		if unicode.IsControl(r) {
			r = ' '
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// SanitizeBlock keeps newlines, for multi-line fields such as the pane and the journal.
func SanitizeBlock(s string, lines int) string {
	parts := strings.Split(s, "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	for i, p := range parts {
		parts[i] = Sanitize(p)
	}
	return strings.Join(parts, "\n")
}
