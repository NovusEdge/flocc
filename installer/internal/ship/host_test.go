package ship

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/steps"
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

func TestOnHostAttachesATerminalOnlyWhenAskedAndScrubsTheToken(t *testing.T) {
	remote := asServiceUser([]string{"tmux", "ls"})
	line := "ssh -o StrictHostKeyChecking=accept-new -t tok@h " + remote
	f := host.NewFake().OnInteractive(line, errors.New("ssh tok@h failed"))
	dest := Dest{SSH: "tok@h", Token: "tok", Opts: []string{"-o", "StrictHostKeyChecking=accept-new"}}
	err := OnHost(context.Background(), f, dest, []string{"tmux", "ls"}, true)
	if !f.Ran("interactive: " + line) {
		t.Fatalf("%v", f.Calls)
	}
	if err == nil || strings.Contains(err.Error(), "tok@") || !strings.Contains(err.Error(), "<token>@h") {
		t.Fatal(err)
	}
	OnHost(context.Background(), f, Dest{SSH: "h"}, []string{"tmux", "ls"}, false)
	if !f.Ran("interactive: ssh h " + remote) {
		t.Fatalf("%v", f.Calls)
	}
}

// -T, because a RequestTTY setting in the owner's ssh config would give cat a pty, which echoes
// the secret on stdin into the output and never passes EOF.
func TestCaptureSendsStdinWithoutATerminalAndScrubsOutput(t *testing.T) {
	remote := asServiceUser([]string{"cat"})
	line := "ssh -o BatchMode=yes -T tok@h " + remote
	f := host.NewFake()
	f.Responses[line] = host.Result{Stderr: "warning tok@h", Code: 3}
	res, err := Capture(context.Background(), f, Dest{SSH: "tok@h", Token: "tok"}, []string{"cat"}, strings.NewReader("in"))
	if err != nil || res.Code != 3 || f.Stdins[line] != "in" || strings.Contains(res.Stderr, "tok@") {
		t.Fatalf("%+v %v %v", res, err, f.Stdins)
	}
}

// Capture buffers stderr, where ssh prints a Tailscale check-mode URL, so the connection is
// opened first where the owner can see it.
func TestConnectOpensTheConnectionOnTheTerminalUnlessAMasterIsLive(t *testing.T) {
	dest := Dest{SSH: "root@h", Opts: []string{"-o", "ControlPath=/s"}}
	check, warm := "ssh -o ControlPath=/s -O check root@h", "ssh -o ConnectTimeout=10 -o ControlPath=/s -T root@h true"
	f := host.NewFake().On(check, 255, "")
	if err := Connect(context.Background(), f, dest, "h1", true); err != nil || !f.Ran("interactive: "+warm) {
		t.Fatalf("%v %v", err, f.Calls)
	}
	f = host.NewFake().On(check, 0, "")
	if err := Connect(context.Background(), f, dest, "h1", true); err != nil || len(f.Calls) != 1 {
		t.Fatalf("%v %v", err, f.Calls)
	}
	f = host.NewFake().On(check, 255, "").OnInteractive(warm, errors.New("exit status 255"))
	if err := Connect(context.Background(), f, dest, "h1", true); err == nil || !strings.Contains(err.Error(), "could not reach h1") {
		t.Fatal(err)
	}
}

func TestConnectWithoutATerminalNeverPrompts(t *testing.T) {
	dest := Dest{SSH: "root@h"}
	warm := "ssh -o BatchMode=yes -o ConnectTimeout=10 -T root@h true"
	f := host.NewFake().On("ssh -O check root@h", 255, "")
	f.Responses[warm] = host.Result{Stderr: "# Tailscale SSH requires an additional check.\n# To authenticate, visit: https://login.tailscale.com/a/x\n", Code: 255}
	err := Connect(context.Background(), f, dest, "h1", false)
	if err == nil || !strings.Contains(err.Error(), "could not reach h1 without a prompt") ||
		!strings.Contains(err.Error(), "https://login.tailscale.com/a/x") || !strings.Contains(err.Error(), "run it in a terminal") {
		t.Fatal(err)
	}
	if !f.Ran(warm) || slices.ContainsFunc(f.Calls, func(c string) bool { return strings.HasPrefix(c, "interactive: ") }) {
		t.Fatalf("%v", f.Calls)
	}
	f.Responses[warm] = host.Result{}
	if err := Connect(context.Background(), f, dest, "h1", false); err != nil {
		t.Fatal(err)
	}
}

func TestFloccLineQuotesEachArgument(t *testing.T) {
	got := shWords(t, strings.Join(FloccLine([]string{"escalate", "--summary", "it's $HOME and `id`"}), " "))
	if strings.Join(got, "|") != "flocc|escalate|--summary|it's $HOME and `id`" {
		t.Fatalf("%q", got)
	}
}
