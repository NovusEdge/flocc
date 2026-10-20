package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/ship"
)

// fakeSSH answers ssh by destination: Run returns out[dest], and a destination it does not know
// fails as an unreachable host, also for ship.Connect's interactive "true". files answers a
// remote command containing "cat <path>" with files[path] on a reachable host, and exit 1 when
// path is not in it. ssh -O check succeeds for a destination in masters. Commands are kept
// whole, with the stdin each Run got, and order holds both kinds as they came.
type fakeSSH struct {
	mu          sync.Mutex
	out         map[string]string
	files       map[string]string
	masters     map[string]bool
	runs        []host.Cmd
	stdins      []string
	interactive []host.Cmd
	order       []call
}

func (f *fakeSSH) Run(_ context.Context, c host.Cmd) (host.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, c)
	f.order = append(f.order, call{false, c})
	in := ""
	if c.Stdin != nil {
		b, _ := io.ReadAll(c.Stdin)
		in = string(b)
	}
	f.stdins = append(f.stdins, in)
	if slices.Contains(c.Args, "-O") {
		if f.masters[c.Args[len(c.Args)-1]] {
			return host.Result{}, nil
		}
		return host.Result{Stderr: "Control socket connect: No such file or directory", Code: 255}, nil
	}
	dest := c.Args[len(c.Args)-2]
	out, ok := f.out[dest]
	if !ok {
		return host.Result{Stderr: "ssh: connect to host " + dest + ": No route to host", Code: 255}, nil
	}
	if f.files != nil {
		if _, path, found := strings.Cut(c.Args[len(c.Args)-1], "cat "); found {
			path = strings.Trim(path, `'"`)
			body, ok := f.files[path]
			if !ok {
				return host.Result{Stderr: "cat: " + path + ": No such file or directory", Code: 1}, nil
			}
			return host.Result{Stdout: body}, nil
		}
	}
	return host.Result{Stdout: out}, nil
}

func (f *fakeSSH) Interactive(_ context.Context, c host.Cmd) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interactive = append(f.interactive, c)
	f.order = append(f.order, call{true, c})
	dest := c.Args[len(c.Args)-2]
	if _, ok := f.out[dest]; !ok && c.Args[len(c.Args)-1] == "true" {
		return errors.New("exit status 255")
	}
	return nil
}

type call struct {
	interactive bool
	c           host.Cmd
}

func isCapture(c host.Cmd) bool { return strings.HasPrefix(c.Args[len(c.Args)-1], "sudo ") }

// captured is the runs that reached the host's service user: no -O check, no warm-up.
func (f *fakeSSH) captured() []host.Cmd {
	var out []host.Cmd
	for _, c := range f.runs {
		if isCapture(c) {
			out = append(out, c)
		}
	}
	return out
}

// warmUps is the destination of each ship.Connect warm-up, in order. It fails the test when a
// warm-up ran without a terminal or came after a capture to its destination.
func warmUps(t *testing.T, f *fakeSSH) []string {
	t.Helper()
	var warm []string
	captured := map[string]bool{}
	for _, k := range f.order {
		a := k.c.Args
		if slices.Contains(a, "-O") {
			continue
		}
		dest := a[len(a)-2]
		switch {
		case a[len(a)-1] == "true" && !k.interactive:
			t.Fatalf("warm-up without a terminal: %s", k.c)
		case a[len(a)-1] == "true":
			if captured[dest] {
				t.Fatalf("%s warmed up after a capture: %v", dest, f.order)
			}
			warm = append(warm, dest)
		case isCapture(k.c):
			captured[dest] = true
		}
	}
	return warm
}

// hostDeps makes one host file per name, reached as root@<name>. HOME is the test's, because
// ship.Statuses makes ~/.ssh for its connection sharing.
func hostDeps(t *testing.T, names ...string) (*deps, *fakeSSH, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	d, out, errb := testDeps(t)
	t.Setenv("HOME", d.home)
	for _, n := range names {
		if err := hosts.Save(hosts.Dir(d.home), hosts.Host{Name: n, SSH: "root@" + n}); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeSSH{out: map[string]string{}}
	d.run = f
	d.resolve = func(_ context.Context, target string) (ship.Dest, error) { return ship.Dest{SSH: target}, nil }
	d.fleet = func([]string) int { t.Fatal("fleet view called"); return 0 }
	return d, f, out, errb
}

// shWords has sh split line into words, as the ssh user's shell does.
func shWords(t *testing.T, line string) []string {
	t.Helper()
	out, err := exec.Command("sh", "-c", "for a in "+line+`; do printf '%s\0' "$a"; done`).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
}

func TestSplitHostStopsAtDoubleDash(t *testing.T) {
	name, rest, err := splitHost([]string{"release", "-H", "a", "NF-1", "--", "-H", "x"})
	if err != nil || name != "a" || strings.Join(rest, " ") != "release NF-1 -- -H x" {
		t.Fatalf("%q %q %v", name, rest, err)
	}
	if name, _, _ := splitHost([]string{"--host=b", "status"}); name != "b" {
		t.Fatal(name)
	}
	if _, _, err := splitHost([]string{"-H", "a", "-H", "b", "status"}); err == nil {
		t.Fatal("two -H accepted")
	}
	if _, _, err := splitHost([]string{"status", "-H"}); err == nil {
		t.Fatal("-H without a name accepted")
	}
}

func TestHostChoiceOrder(t *testing.T) {
	d, _, _, _ := hostDeps(t, "a", "b")
	ctx := context.Background()
	if h, err := pickHost(ctx, d, "b", nil); err != nil || h.Name != "b" {
		t.Fatalf("%+v %v", h, err)
	}
	d.getenv = func(k string) string { return map[string]string{"FLOCC_HOST": "a"}[k] }
	if h, err := pickHost(ctx, d, "", []string{"status"}); err != nil || h.Name != "a" {
		t.Fatalf("%+v %v", h, err)
	}
	if _, err := pickHost(ctx, d, "c", nil); err == nil || !strings.Contains(err.Error(), "flocc host add c") {
		t.Fatal(err)
	}
	if _, err := pickHost(ctx, d, "all", []string{"release", "NF-1"}); err == nil || !strings.Contains(err.Error(), "-H all runs only") {
		t.Fatal(err)
	}
	d.getenv = func(k string) string { return map[string]string{"FLOCC_HOST": "all"}[k] }
	if _, err := pickHost(ctx, d, "", []string{"doctor"}); err == nil ||
		err.Error() != "FLOCC_HOST cannot be all; use -H all for status, doctor or logs" {
		t.Fatal(err)
	}
	d.getenv = func(string) string { return "" }
	if _, err := pickHost(ctx, d, "", []string{"status"}); err == nil || !strings.Contains(err.Error(), "a, b") {
		t.Fatal(err)
	}
	one, _, _, _ := hostDeps(t, "solo")
	if h, err := pickHost(ctx, one, "", []string{"tick"}); err != nil || h.Name != "solo" {
		t.Fatalf("%+v %v", h, err)
	}
	none, _, _, _ := hostDeps(t)
	if _, err := pickHost(ctx, none, "", []string{"tick"}); err == nil || !strings.Contains(err.Error(), "flocc host add") {
		t.Fatal(err)
	}
}

const holdsNF12 = `{"host":"b","claims":[{"issue":"NF-12","state":"blocked","phase":"impl"}]}`

func TestAnIssueKeyPicksTheHostHoldingTheClaim(t *testing.T) {
	d, f, _, _ := hostDeps(t, "a", "b")
	f.out["root@a"] = `{"host":"a","claims":[]}`
	f.out["root@b"] = holdsNF12
	if code := run(context.Background(), d, []string{"release", "NF-12", "--keep-worktree"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(f.interactive) != 1 || f.interactive[0].Args[len(f.interactive[0].Args)-2] != "root@b" {
		t.Fatalf("%v", f.interactive)
	}
}

// The lookup's status fetches run in parallel and keep stderr, so each host's connection opens
// on the terminal first, one at a time.
func TestAnIssueKeyLookupOpensEachConnectionOnTheTerminalFirst(t *testing.T) {
	d, f, _, errb := hostDeps(t, "a", "b", "c")
	d.tty = true
	f.out["root@a"], f.out["root@b"] = `{"host":"a","claims":[]}`, holdsNF12
	if code := run(context.Background(), d, []string{"release", "NF-12"}); code != 0 {
		t.Fatalf("exit %d\n%s", code, errb)
	}
	if got := warmUps(t, f); !slices.Equal(got, []string{"root@a", "root@b", "root@c"}) {
		t.Fatalf("warm-ups %v", got)
	}
	first := slices.IndexFunc(f.order, func(k call) bool { return isCapture(k.c) })
	last := slices.IndexFunc(f.order, func(k call) bool { return k.interactive && strings.HasSuffix(k.c.String(), "root@c true") })
	if first < last {
		t.Fatalf("a status fetch ran before c's warm-up: %v", f.order)
	}
	// c failed to connect, so it is not fetched; b still holds the claim and is picked.
	if n := len(f.captured()); n != 2 {
		t.Fatalf("%d fetches: %v", n, f.captured())
	}
}

func TestAnIssueKeyNoHostOrTwoHostsHoldIsAnError(t *testing.T) {
	d, f, _, errb := hostDeps(t, "a", "b", "c")
	f.out["root@a"] = `{"host":"a","claims":[]}`
	f.out["root@b"] = `{"host":"b","claims":[]}`
	if code := run(context.Background(), d, []string{"release", "NF-12"}); code == 0 || len(f.interactive) != 0 {
		t.Fatalf("exit %d %v", code, f.interactive)
	}
	if msg := errb.String(); !strings.Contains(msg, "no reachable host holds NF-12") || !strings.Contains(msg, "unreachable: could not reach c without a prompt (ssh: connect to host root@c: No route to host)") || !strings.Contains(msg, "-H") {
		t.Fatal(msg)
	}
	f.out["root@a"], f.out["root@b"], f.out["root@c"] = holdsNF12, holdsNF12, holdsNF12
	errb.Reset()
	if code := run(context.Background(), d, []string{"release", "NF-12"}); code == 0 || !strings.Contains(errb.String(), "a, b and c") {
		t.Fatalf("exit %d %q", code, errb.String())
	}
}

func TestHostCommandArgumentsArriveIntact(t *testing.T) {
	d, f, _, _ := hostDeps(t, "a")
	args := []string{"escalate", "--summary", "it's $HOME and `id`", "--action", "two  words"}
	if code := run(context.Background(), d, append([]string{"-H", "a"}, args...)); code != 0 {
		t.Fatalf("exit %d", code)
	}
	c := f.interactive[0]
	words := shWords(t, c.Args[len(c.Args)-1])
	script := strings.TrimPrefix(words[len(words)-1], "cd && ")
	out, err := exec.Command("sh", "-c", `flocc() { printf '%s\n' "$@"; }; `+script).Output()
	if err != nil || string(out) != strings.Join(args, "\n")+"\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestSSHGetsATerminalOnlyWhenStdinIsOne(t *testing.T) {
	for _, tty := range []bool{false, true} {
		d, f, _, _ := hostDeps(t, "a")
		d.tty = tty
		run(context.Background(), d, []string{"doctor"})
		if got := strings.Contains(f.interactive[0].String(), "ssh -t "); got != tty {
			t.Errorf("tty=%v: %s", tty, f.interactive[0])
		}
	}
}

func TestDashHAllRunsOnlyReadOnlyCommands(t *testing.T) {
	for _, args := range [][]string{{"release", "NF-1"}, {"tick"}, {"logs", "-f"}, {"logs", "--follow"}, {"logs", "--fo"}, {"logs", "--foll"}, {"logs", "-fn5"}, {"logs", "-n5", "-f"}} {
		d, f, _, errb := hostDeps(t, "a")
		if code := run(context.Background(), d, append([]string{"-H", "all"}, args...)); code != 2 || len(f.runs)+len(f.interactive) != 0 {
			t.Errorf("%v: exit %d, ran %v %v", args, code, f.runs, f.interactive)
		}
		if errb.Len() == 0 {
			t.Errorf("%v: no reason given", args)
		}
	}
}

func TestDashHAllLogsWithoutFollowRunsEverywhere(t *testing.T) {
	d, f, _, _ := hostDeps(t, "a")
	f.out["root@a"] = "line\n"
	if code := run(context.Background(), d, []string{"-H", "all", "logs", "-n", "5", "--fix"}); code != 0 || len(f.captured()) != 1 {
		t.Fatalf("exit %d %v", code, f.runs)
	}
}

func TestDashHAllGivesEveryHostAConnectTimeout(t *testing.T) {
	d, f, _, _ := hostDeps(t, "a", "b")
	f.out["root@a"], f.out["root@b"] = "", ""
	run(context.Background(), d, []string{"-H", "all", "doctor"})
	if len(f.captured()) != 2 {
		t.Fatalf("%v", f.runs)
	}
	for _, c := range f.runs {
		if !strings.Contains(c.String(), "-o ConnectTimeout=10 ") {
			t.Errorf("no ConnectTimeout: %s", c)
		}
	}
}

// A Tailscale check-mode URL goes to ssh's stderr, which Capture keeps, so each host's
// connection opens on the terminal first, one at a time, before the captures run in parallel.
func TestDashHAllOpensEachConnectionOnTheTerminalBeforeAnyCapture(t *testing.T) {
	d, f, out, _ := hostDeps(t, "a", "b", "c")
	d.tty = true
	f.out["root@a"], f.out["root@c"] = "ok\n", "ok\n"
	f.masters = map[string]bool{"root@c": true}
	if code := run(context.Background(), d, []string{"-H", "all", "doctor"}); code == 0 {
		t.Fatal("exit 0 with b unreachable")
	}
	if got := warmUps(t, f); !slices.Equal(got, []string{"root@a", "root@b"}) {
		t.Fatalf("warm-ups %v", got)
	}
	first := slices.IndexFunc(f.order, func(k call) bool { return isCapture(k.c) })
	last := slices.IndexFunc(f.order, func(k call) bool { return k.interactive && strings.Contains(k.c.String(), "root@b") })
	if first < last {
		t.Fatalf("a capture ran before b's warm-up: %v", f.order)
	}
	if len(f.captured()) != 2 || !strings.Contains(out.String(), "== b ==\ncould not reach b") {
		t.Fatalf("%v\n%s", f.captured(), out)
	}
}

func TestDashHAllStatusIsTheFleetView(t *testing.T) {
	d, _, _, _ := hostDeps(t, "a")
	var got []string
	d.fleet = func(args []string) int { got = args; return 0 }
	if code := run(context.Background(), d, []string{"status", "-H", "all", "--watch"}); code != 0 || strings.Join(got, " ") != "--watch" {
		t.Fatalf("exit %d args %q", code, got)
	}
}

func TestDashHAllDoctorReportsEveryHostAndTheUnreachableOne(t *testing.T) {
	d, f, out, _ := hostDeps(t, "a", "b")
	f.out["root@a"] = "ok    config: /x\n"
	code := run(context.Background(), d, []string{"-H", "all", "doctor"})
	o := out.String()
	if code == 0 || !strings.Contains(o, "== a ==\nok    config: /x\n") || !strings.Contains(o, "== b ==") || !strings.Contains(o, "No route to host") {
		t.Fatalf("exit %d\n%s", code, o)
	}
	for _, c := range f.runs {
		if strings.Contains(c.String(), " -t ") {
			t.Errorf("-H all asked for a terminal: %s", c)
		}
	}
}

func TestHostRunPassesARawCommand(t *testing.T) {
	d, f, _, _ := hostDeps(t, "a")
	if code := run(context.Background(), d, []string{"host", "run", "-H", "a", "--", "tmux", "-L", "flocc", "ls"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	words := shWords(t, f.interactive[0].Args[len(f.interactive[0].Args)-1])
	if script := words[len(words)-1]; script != "cd && tmux -L flocc ls" {
		t.Fatalf("%q", script)
	}
}
