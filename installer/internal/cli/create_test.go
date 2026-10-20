package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// create.Main writes to os.Stderr, so the test reads it there: hostCmd's fall-through for an
// unknown sub-verb also returns 2, with the desktop usage on d.err.
func TestHostCreateIsADesktopVerbThatChecksItsArguments(t *testing.T) {
	d, _, errb := testDeps(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	code := run(context.Background(), d, []string{"host", "create", "only-one-arg"})
	os.Stderr = stderr
	w.Close()
	got, _ := io.ReadAll(r)
	if code != 2 || !strings.Contains(string(got), "flocc host create: usage: flocc host create [--cpus N]") || errb.Len() != 0 {
		t.Fatalf("%d stderr=%q d.err=%q", code, got, errb)
	}
}

func TestHostInstallerKnowsAHostByItsFile(t *testing.T) {
	d, _, _ := testDeps(t)
	h := hostInstaller{d: d}
	if h.HostKnown("flocc-two") {
		t.Fatal("known before host add")
	}
	path := hostFile(d, "flocc-two")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("ssh = \"root@flocc-two\"\n"), 0o600)
	if !h.HostKnown("flocc-two") {
		t.Fatal("unknown after host add")
	}
}

func TestUsageNamesHostCreate(t *testing.T) {
	if !strings.Contains(usage, "flocc host create [--cpus N] [--memory GiB] [--disk GiB] <machine> <name>") {
		t.Fatal(usage)
	}
}
