package ui

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPlainGolden(t *testing.T) {
	var out strings.Builder
	s := New(NewPlain(&out, strings.NewReader("\n2\n"), false))
	s.Group("Old versions")
	s.Step("zer0-day-nimble").Done("none left")
	s.Step("la-nimble").Skip("kept")
	ok, err := s.Confirm(Question{Title: "Adopt the existing cardinal user?", Facts: []string{"uid 1002, lingering"}, Default: "yes"})
	if err != nil || !ok {
		t.Fatalf("Confirm = %v, %v", ok, err)
	}
	i, err := s.Choose(Question{Title: "No hosts yet"}, []string{"Create", "Add"})
	if err != nil || i != 1 {
		t.Fatalf("Choose = %d, %v", i, err)
	}
	s.Todo("log in on flocc-2")
	s.Close()
	want := `Old versions
  ✓ zer0-day-nimble none left
  · la-nimble      kept

Adopt the existing cardinal user?
  uid 1002, lingering
[Y/n]: 

No hosts yet
  1. Create
  2. Add
Choose [1-2]: 

Still yours to do
  • log in on flocc-2
`
	if got := out.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if strings.ContainsAny(out.String(), "\x1b\r") {
		t.Error("plain output has an escape or a carriage return")
	}
}

func TestPlainConfirmDefaultNo(t *testing.T) {
	var out strings.Builder
	s := New(NewPlain(&out, strings.NewReader("\n"), false))
	ok, err := s.Confirm(Question{Title: "Wipe?"})
	if err != nil || ok {
		t.Fatalf("Confirm = %v, %v; want false", ok, err)
	}
	if !strings.Contains(out.String(), "[y/N]: ") {
		t.Errorf("no [y/N] in %q", out.String())
	}
}

func TestPlainChooseReasksBadNumber(t *testing.T) {
	var out strings.Builder
	s := New(NewPlain(&out, strings.NewReader("9\nx\n1\n"), false))
	i, err := s.Choose(Question{Title: "Pick"}, []string{"a", "b"})
	if err != nil || i != 0 {
		t.Fatalf("Choose = %d, %v", i, err)
	}
	if n := strings.Count(out.String(), "Choose [1-2]: "); n != 3 {
		t.Errorf("asked %d times, want 3", n)
	}
	s = New(NewPlain(&out, strings.NewReader("9\n9\n9\n"), false))
	if _, err := s.Choose(Question{Title: "Pick"}, []string{"a"}); err == nil {
		t.Error("three bad numbers gave no error")
	}
}

func TestPlainEOF(t *testing.T) {
	s := New(NewPlain(&strings.Builder{}, strings.NewReader(""), false))
	if _, err := s.Confirm(Question{Title: "x"}); !errors.Is(err, ErrNoTerminal) {
		t.Errorf("Confirm err = %v", err)
	}
	if _, err := s.Ask(Question{Title: "x"}); !errors.Is(err, ErrNoTerminal) {
		t.Errorf("Ask err = %v", err)
	}
}

func TestPlainAskAndSecret(t *testing.T) {
	s := New(NewPlain(&strings.Builder{}, strings.NewReader("\nhunter2\n"), false))
	got, err := s.Ask(Question{Title: "Name", Default: "kept"})
	if err != nil || got != "kept" {
		t.Errorf("Ask = %q, %v", got, err)
	}
	if got, err := s.Secret(Question{Title: "Key"}); err != nil || got != "hunter2" {
		t.Errorf("Secret = %q, %v", got, err)
	}
}

func TestPlainTable(t *testing.T) {
	var out strings.Builder
	s := New(NewPlain(&out, strings.NewReader(""), false))
	s.Table(Table{Headers: []string{"Host", "State"}, Rows: [][]string{{"la-nimble", "up"}, {"x", "down"}}})
	want := "  Host       State\n  la-nimble  up\n  x          down\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPlainHandoff(t *testing.T) {
	var out strings.Builder
	s := New(NewPlain(&out, strings.NewReader(""), false))
	if err := s.Handoff("claude login", exec.Command("true")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "── claude login ──\n") {
		t.Errorf("out = %q", out.String())
	}
}

func TestPlainSlowStepShownOnce(t *testing.T) {
	var out strings.Builder
	p := NewPlain(&out, strings.NewReader(""), false).(*plain)
	clock := time.Unix(0, 0)
	p.now = func() time.Time { return clock }
	s := New(p)
	st := s.Step("Packages")
	st.Detail("early")
	clock = clock.Add(6 * time.Second)
	st.Detail("apt-get install")
	st.Detail("still going")
	if want := "  → Packages  apt-get install\n"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

func TestPlainLogLine(t *testing.T) {
	var out strings.Builder
	s := New(NewPlain(&out, strings.NewReader(""), false))
	s.LogPath("/tmp/setup.log")
	s.Close()
	if want := "\nLog: /tmp/setup.log\n"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}
