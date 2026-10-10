package ship

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

// sudoEscape mimics sudo -i, which backslash-escapes every byte of its argv that is not
// alphanumeric, '_' or '-' before the login shell parses the joined string.
func sudoEscape(words []string) string {
	var out []string
	for _, w := range words {
		var b strings.Builder
		for _, c := range []byte(w) {
			if c != '_' && c != '-' && (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
				b.WriteByte('\\')
			}
			b.WriteByte(c)
		}
		out = append(out, b.String())
	}
	return strings.Join(out, " ")
}

// shWords has sh parse line into words, the way a shell parses a command line into argv.
func shWords(t *testing.T, line string) []string {
	t.Helper()
	out, err := exec.Command("sh", "-c", "for a in "+line+`; do printf '%s\0' "$a"; done`).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
}

func TestShQuoteRoundTripsThroughSh(t *testing.T) {
	for _, s := range []string{"plain", "it's", "a \"b\" $HOME $(x) `y` \\n; | * ?", "two\nlines", "''"} {
		if got := shWords(t, shQuote(s)); len(got) != 1 || got[0] != s {
			t.Errorf("%q -> %q", s, got)
		}
	}
}

func TestAsServiceUserDeliversTheCommandIntact(t *testing.T) {
	cmd := []string{"echo", `it's "$HOME"`, "&&", "printf", `%s\n`, "$(echo x)", "|", "cat"}
	joined := strings.Join(cmd, " ")
	// The ssh user's shell yields sudo's argv; sudo -i escapes it; the login shell parses it again.
	argv := shWords(t, strings.TrimPrefix(asServiceUser(cmd), "sudo "))
	if len(argv) != 7 || argv[0] != "-iu" || argv[3] != "PATH="+servicePath || argv[6] != joined {
		t.Fatalf("%q", argv)
	}
	got := shWords(t, sudoEscape(argv[2:]))
	if strings.Join(got, "\x00") != strings.Join(argv[2:], "\x00") {
		t.Fatalf("login shell changed the argv: %q", got)
	}
	if got[len(got)-1] != joined {
		t.Fatalf("%q", got)
	}
}

func TestHostRunAttachesATerminalAndScrubsTheToken(t *testing.T) {
	remote := asServiceUser([]string{"tmux", "ls"})
	f := host.NewFake().On("ssh -o StrictHostKeyChecking=accept-new -t tok@h "+remote, 255, "")
	dest := Dest{SSH: "tok@h", Token: "tok", Opts: []string{"-o", "StrictHostKeyChecking=accept-new"}}
	err := hostRun(context.Background(), f, dest, []string{"tmux", "ls"})
	if !f.Ran("interactive: ssh -o StrictHostKeyChecking=accept-new -t tok@h " + remote) {
		t.Fatalf("%v", f.Calls)
	}
	if err != nil && strings.Contains(err.Error(), "tok@") {
		t.Fatal(err)
	}
}
