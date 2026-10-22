// Package themetest holds the helpers frame tests share.
package themetest

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

var update = flag.Bool("update", false, "rewrite golden files")

func Strip(s string) string { return ansi.Strip(s) }

// Golden compares got, stripped of escapes and trailing spaces, with
// testdata/<name>.golden in the test's package directory; -update rewrites it.
func Golden(t *testing.T, name, got string) {
	t.Helper()
	lines := strings.Split(Strip(got), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	got = strings.Join(lines, "\n")
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	want := strings.Split(string(b), "\n")
	for i := 0; i < max(len(want), len(lines)); i++ {
		var w, g string
		if i < len(want) {
			w = want[i]
		}
		if i < len(lines) {
			g = lines[i]
		}
		if w != g || (i >= len(want)) != (i >= len(lines)) {
			t.Fatalf("%s differs at line %d:\n want %q\n  got %q", path, i+1, w, g)
		}
	}
}
