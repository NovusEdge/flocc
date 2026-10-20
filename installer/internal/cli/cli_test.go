package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func testDeps(t *testing.T) (*deps, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errb bytes.Buffer
	return &deps{out: &out, err: &errb, home: t.TempDir(), getenv: func(string) string { return "" }}, &out, &errb
}

func TestBareFloccPrintsHelpAndFails(t *testing.T) {
	d, out, errb := testDeps(t)
	if code := run(context.Background(), d, nil); code != 2 || out.Len() != 0 || !strings.Contains(errb.String(), "Desktop commands:") {
		t.Fatalf("%d %q %q", code, out, errb)
	}
}

func TestHelpGoesToStdout(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		d, out, _ := testDeps(t)
		if code := run(context.Background(), d, []string{arg}); code != 0 || !strings.Contains(out.String(), "Desktop commands:") {
			t.Fatalf("%s: %d %q", arg, code, out)
		}
	}
}
