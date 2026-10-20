package cli

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/ship"
)

// fakeSSH answers ssh by destination: Run returns out[dest], and a destination it does not know
// fails as an unreachable host. files answers a remote command containing "cat <path>" with
// files[path] on a reachable host, and exit 1 when path is not in it. Commands are kept whole,
// with the stdin each Run got.
type fakeSSH struct {
	mu          sync.Mutex
	out         map[string]string
	files       map[string]string
	runs        []host.Cmd
	stdins      []string
	interactive []host.Cmd
}

func (f *fakeSSH) Run(_ context.Context, c host.Cmd) (host.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, c)
	in := ""
	if c.Stdin != nil {
		b, _ := io.ReadAll(c.Stdin)
		in = string(b)
	}
	f.stdins = append(f.stdins, in)
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
	return nil
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

func TestAnIssueKeyNoHostOrTwoHostsHoldIsAnError(t *testing.T) {
	d, f, _, errb := hostDeps(t, "a", "b", "c")
	f.out["root@a"] = `{"host":"a","claims":[]}`
	f.out["root@b"] = `{"host":"b","claims":[]}`
	if code := run(context.Background(), d, []string{"release", "NF-12"}); code == 0 || len(f.interactive) != 0 {
		t.Fatalf("exit %d %v", code, f.interactive)
	}
	if msg := errb.String(); !strings.Contains(msg, "no reachable host holds NF-12") || !strings.Contains(msg, "unreachable: c: exit 255: ssh: connect to host root@c: No route to host") || !strings.Contains(msg, "-H") {
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
	if code := run(context.Background(), d, []string{"-H", "all", "logs", "-n", "5", "--fix"}); code != 0 || len(f.runs) != 1 {
		t.Fatalf("exit %d %v", code, f.runs)
	}
}

func TestDashHAllGivesEveryHostAConnectTimeout(t *testing.T) {
	d, f, _, _ := hostDeps(t, "a", "b")
	run(context.Background(), d, []string{"-H", "all", "doctor"})
	if len(f.runs) != 2 {
		t.Fatalf("%v", f.runs)
	}
	for _, c := range f.runs {
		if !strings.Contains(c.String(), "-o ConnectTimeout=10 ") {
			t.Errorf("no ConnectTimeout: %s", c)
		}
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
