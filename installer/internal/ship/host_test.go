package ship

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
	"github.com/NovusEdge/linear-agents/installer/internal/steps"
)

// shWords has sh parse line into words, the way the ssh user's shell parses the remote command.
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
	cmd := []string{"printf", `'%s\n'`, `'$1 $$'`, `'it'\''s'`, "'`id`'", "'a\nb'"}
	argv := shWords(t, strings.TrimPrefix(asServiceUser(cmd), "sudo "))
	want := []string{"-u", steps.ServiceUser, "-H", "env", "HOME=" + serviceHome, steps.ServicePath(serviceHome), "sh", "-c"}
	if len(argv) != len(want)+1 || strings.Join(argv[:len(want)], "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("%q", argv)
	}
	script := argv[len(want)]
	if script != "cd && "+strings.Join(cmd, " ") {
		t.Fatalf("%q", script)
	}
	// The test host has no service home, so run the script without its cd.
	out, err := exec.Command("sh", "-c", strings.TrimPrefix(script, "cd && ")).Output()
	if want := "$1 $$\nit's\n`id`\na\nb\n"; err != nil || string(out) != want {
		t.Fatalf("%q %v", out, err)
	}
}

func TestHostRunAttachesATerminalAndScrubsTheToken(t *testing.T) {
	remote := asServiceUser([]string{"tmux", "ls"})
	line := "ssh -o StrictHostKeyChecking=accept-new -t tok@h " + remote
	f := host.NewFake().OnInteractive(line, errors.New("ssh tok@h failed"))
	dest := Dest{SSH: "tok@h", Token: "tok", Opts: []string{"-o", "StrictHostKeyChecking=accept-new"}}
	err := hostRun(context.Background(), f, dest, []string{"tmux", "ls"})
	if !f.Ran("interactive: " + line) {
		t.Fatalf("%v", f.Calls)
	}
	if err == nil || strings.Contains(err.Error(), "tok@") || !strings.Contains(err.Error(), "<token>@h") {
		t.Fatal(err)
	}
}
