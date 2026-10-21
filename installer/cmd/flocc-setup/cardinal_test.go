package main

import (
	"io"
	"strings"
	"testing"
)

func TestCardinalPhaseIsFoundBeforeParsing(t *testing.T) {
	for args, want := range map[string]bool{
		"--phase cardinal-status --quick": true,
		"--phase=cardinal-root --user c":  true,
		"-phase cardinal":                 true,
		"--phase service --answers -":     false,
		"--answers -":                     false,
		"--phase cardinal-bogus":          false,
	} {
		if _, ok := cardinalPhase(strings.Fields(args)); ok != want {
			t.Errorf("%q: %v", args, ok)
		}
	}
}

func TestCheckCardinalPhase(t *testing.T) {
	for _, c := range []struct {
		phase string
		euid  int
		ok    bool
	}{
		{"cardinal-root", 0, true}, {"cardinal-root", 1000, false},
		{"cardinal-host", 0, true}, {"cardinal-purge", 1002, false},
		{"cardinal", 1002, true}, {"cardinal", 0, false},
		{"cardinal-status", 1002, true}, {"cardinal-prestart", 0, false},
	} {
		if err := checkCardinalPhase(c.phase, c.euid); (err == nil) != c.ok {
			t.Errorf("%s as %d: %v", c.phase, c.euid, err)
		}
	}
}

func TestRunCardinalRefusesABadUserBeforeTheStage(t *testing.T) {
	var errb strings.Builder
	if code := runCardinal([]string{"--phase", "cardinal-root", "--user", "-rf"}, nil, io.Discard, &errb); code != 2 || !strings.Contains(errb.String(), "not a user name") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}
