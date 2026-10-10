package host

import (
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
