package host

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
)

type fakeTerm struct {
	why   string
	path  string
	calls int
}

func (f *fakeTerm) Handoff(why string, c *exec.Cmd) error {
	f.calls++
	f.why, f.path = why, c.Path
	return nil
}

func TestInteractiveHandsOffWhenTheChildNeedsTheTerminal(t *testing.T) {
	f := &fakeTerm{}
	if err := (Exec{Term: f}).Interactive(context.Background(), Cmd{Name: "true", Why: "root pass"}); err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 || f.why != "root pass" || !strings.HasSuffix(f.path, "true") {
		t.Fatalf("handoff = %+v", f)
	}
}

// The argv of an ssh to a Daytona sandbox carries its token.
func TestInteractiveWhyDefaultsToTheCommandNameNeverItsArguments(t *testing.T) {
	f := &fakeTerm{}
	(Exec{Term: f}).Interactive(context.Background(), Cmd{Name: "true", Args: []string{"-x", "SECRETTOKEN@host"}})
	if f.why != "true" {
		t.Fatalf("why = %q", f.why)
	}
}

func TestInteractiveRunsDirectlyWithBothStreamsSet(t *testing.T) {
	f := &fakeTerm{}
	var out bytes.Buffer
	c := Cmd{Name: "cat", Stdin: strings.NewReader("x"), Stdout: &out}
	if err := (Exec{Term: f}).Interactive(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if f.calls != 0 || out.String() != "x" {
		t.Fatalf("calls=%d out=%q", f.calls, out.String())
	}
}

func TestInteractiveAttachHandsOffEvenWithStreamsSet(t *testing.T) {
	f := &fakeTerm{}
	var out bytes.Buffer
	(Exec{Term: f}).Interactive(context.Background(), Cmd{Name: "cat", Stdin: strings.NewReader("x"), Stdout: &out, Attach: true})
	if f.calls != 1 {
		t.Fatalf("calls = %d", f.calls)
	}
}
