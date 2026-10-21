package host

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseOSRelease(t *testing.T) {
	cases := []struct {
		text    string
		manager Manager
		ghRepo  bool
	}{
		{"ID=debian\nVERSION_ID=\"13\"\n", Apt, false},
		{"ID=ubuntu\nID_LIKE=debian\n", Apt, false},
		{"ID=arch\n", Pacman, false},
		{"ID=manjaro\nID_LIKE=arch\n", Pacman, false},
		{"ID=fedora\n", Dnf, false},
		{"ID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\n", Dnf, true},
		{"ID=rhel\nID_LIKE=\"fedora\"\n", Dnf, true},
	}
	for _, c := range cases {
		d, err := ParseOSRelease(c.text)
		if err != nil || d.Manager != c.manager || d.NeedsGHRepo() != c.ghRepo {
			t.Errorf("%q: got %+v ghRepo=%v err=%v", c.text, d, d.NeedsGHRepo(), err)
		}
	}
	if _, err := ParseOSRelease("ID=alpine\n"); err == nil || !strings.Contains(err.Error(), "alpine") {
		t.Errorf("alpine: want an error naming it, got %v", err)
	}
}

func TestAtLeast(t *testing.T) {
	min := [3]int{2, 1, 293}
	for s, want := range map[string]bool{
		"2.1.293 (Claude Code)": true,
		"2.1.295":               true,
		"2.2.0":                 true,
		"3.0.0":                 true,
		"2.1.292":               false,
		"2.0.999":               false,
		"garbage":               false,
	} {
		if AtLeast(s, min) != want {
			t.Errorf("AtLeast(%q) != %v", s, want)
		}
	}
}

func TestVersionTakesTheFirstTriple(t *testing.T) {
	if v, ok := Version("flocc 0.8.0"); !ok || v != [3]int{0, 8, 0} {
		t.Fatalf("%v %v", v, ok)
	}
	if _, ok := Version("flocc dev"); ok {
		t.Fatal("parsed a version from none")
	}
}

func TestMustReturnsCmdErrorWithTail(t *testing.T) {
	f := NewFake().On("git fetch", 1, "")
	f.Responses["git fetch"] = Result{Stderr: "fatal: nope\n", Code: 1}
	_, err := Must(context.Background(), f, Cmd{Name: "git", Args: []string{"fetch"}})
	var ce *CmdError
	if !errors.As(err, &ce) || ce.Code != 1 || !strings.Contains(err.Error(), "fatal: nope") {
		t.Fatalf("got %v", err)
	}
}

func TestTail(t *testing.T) {
	if got := Tail("a\nb\nc\n", 2); got != "b\nc" {
		t.Fatalf("got %q", got)
	}
}

func TestFakeSeqAnswersInOrderThenRepeatsTheLast(t *testing.T) {
	f := NewFake().OnSeq("x", Result{Code: 1}, Result{Stdout: "ok"})
	var got []int
	for range 3 {
		r, _ := f.Run(context.Background(), Cmd{Name: "x"})
		got = append(got, r.Code)
	}
	if got[0] != 1 || got[1] != 0 || got[2] != 0 {
		t.Fatalf("codes %v", got)
	}
}

func TestFakeUnknownCommandExits127(t *testing.T) {
	r, err := NewFake().Run(context.Background(), Cmd{Name: "nope"})
	if err != nil || r.Code != 127 {
		t.Fatalf("got %+v %v", r, err)
	}
}

func TestExecCapturesOutputAndExitCode(t *testing.T) {
	var log bytes.Buffer
	r, err := Exec{Log: &log}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "echo out; echo err >&2; exit 3"}})
	if err != nil || r.Code != 3 || r.Stdout != "out\n" || r.Stderr != "err\n" {
		t.Fatalf("got %+v %v", r, err)
	}
	for _, want := range []string{"$ sh -c echo out; echo err >&2; exit 3", "out\n", "err\n"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log %q lacks %q", log.String(), want)
		}
	}
}

func TestExecMissingBinaryErrors(t *testing.T) {
	if _, err := (Exec{}).Run(context.Background(), Cmd{Name: "la-no-such-binary"}); err == nil {
		t.Fatal("want an error")
	}
}

func TestExecAppliesDirAndEnv(t *testing.T) {
	dir := t.TempDir()
	r, err := Exec{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "pwd; echo $LA_X"}, Dir: dir, Env: []string{"LA_X=1"}})
	if err != nil || r.Code != 0 {
		t.Fatalf("got %+v %v", r, err)
	}
	lines := strings.Fields(r.Stdout)
	if len(lines) != 2 || lines[1] != "1" || !strings.HasSuffix(lines[0], strings.TrimPrefix(dir, "/private")) {
		t.Fatalf("stdout %q, dir %q", r.Stdout, dir)
	}
}

func TestExecCancelledContextReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { cancel() }()
	_, err := Exec{}.Run(ctx, Cmd{Name: "sleep", Args: []string{"5"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestExecLogRedactsSecretsButTheCallerGetsTheOutput(t *testing.T) {
	var log strings.Builder
	e := Exec{Log: Redact(&log, "s3cret", "")}
	res, err := e.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", `printf '%s\n' "$S"; printf '%s' "$S" >&2`},
		Env: []string{"S=s3cret"}})
	if err != nil || res.Stdout != "s3cret\n" || res.Stderr != "s3cret" {
		t.Fatalf("%+v %v", res, err)
	}
	if strings.Contains(log.String(), "s3cret") || strings.Count(log.String(), "<token>") != 2 {
		t.Fatalf("log %q", log.String())
	}
}

func TestRedactHidesTheToken(t *testing.T) {
	var b strings.Builder
	Redact(&b, "tok123").Write([]byte("$ ssh tok123@ssh.app.daytona.io uname -m\n"))
	if strings.Contains(b.String(), "tok123") || !strings.Contains(b.String(), "<token>@ssh.app.daytona.io") {
		t.Fatalf("%q", b.String())
	}
}

// strings.ReplaceAll with an empty old string inserts the new one between every byte.
func TestRedactWithNoSecretsPassesThrough(t *testing.T) {
	var b strings.Builder
	Redact(&b, "", "").Write([]byte("plain\n"))
	if b.String() != "plain\n" {
		t.Fatalf("%q", b.String())
	}
}

func TestRedactHidesASecretThatContainsAnotherWhicheverComesFirst(t *testing.T) {
	for _, secrets := range [][]string{{"abc", "abcdef"}, {"abcdef", "abc"}} {
		var b strings.Builder
		Redact(&b, secrets...).Write([]byte("key abcdef end\n"))
		if b.String() != "key <token> end\n" {
			t.Errorf("%v: %q", secrets, b.String())
		}
	}
}

func TestExecPassesStdin(t *testing.T) {
	res, err := Exec{}.Run(context.Background(), Cmd{Name: "cat", Stdin: strings.NewReader("doc\n")})
	if err != nil || res.Stdout != "doc\n" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestExecInteractiveUsesTheCmdStreams(t *testing.T) {
	var out bytes.Buffer
	if err := (Exec{}).Interactive(context.Background(), Cmd{Name: "cat", Stdin: strings.NewReader("x"), Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "x" {
		t.Fatalf("%q", out.String())
	}
}

func TestFakeRecordsStdinAndWritesInteractiveOutput(t *testing.T) {
	f := NewFake().On("cat", 0, "").OnInteractiveOut("ssh -t h x", "hello\r\n")
	f.Run(context.Background(), Cmd{Name: "cat", Stdin: strings.NewReader("doc")})
	var out bytes.Buffer
	f.Interactive(context.Background(), Cmd{Name: "ssh", Args: []string{"-t", "h", "x"}, Stdin: strings.NewReader("in"), Stdout: &out})
	if f.Stdins["cat"] != "doc" || f.Stdins["ssh -t h x"] != "in" || out.String() != "hello\r\n" {
		t.Fatalf("%v %q", f.Stdins, out.String())
	}
}
