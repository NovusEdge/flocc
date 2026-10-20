package cli

import (
	"context"
	"errors"
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
	if known, err := h.HostKnown("flocc-two", "root@flocc-two"); known || err != nil {
		t.Fatalf("known before host add: %v", err)
	}
	path := hostFile(d, "flocc-two")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("ssh = \"root@flocc-two\"\n"), 0o600)
	if known, err := h.HostKnown("flocc-two", "root@flocc-two"); !known || err != nil {
		t.Fatalf("unknown after host add: %v", err)
	}
}

// Skipping host add for such a file would ship to that host instead of the new VM.
func TestHostInstallerStopsAtAHostFileThatReachesSomethingElse(t *testing.T) {
	for _, body := range []string{"daytona = \"flocc-two\"\n", "ssh = \"me@flocc-two\"\n"} {
		d, _, _ := testDeps(t)
		path := hostFile(d, "flocc-two")
		os.MkdirAll(filepath.Dir(path), 0o700)
		os.WriteFile(path, []byte(body), 0o600)
		_, err := hostInstaller{d: d}.HostKnown("flocc-two", "root@flocc-two")
		if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "root@flocc-two") {
			t.Fatalf("%s: %v", body, err)
		}
	}
}

func TestHostInstallerIsReadyOnlyInsideTheCheckout(t *testing.T) {
	d, _, _ := testDeps(t)
	d.repoRoot = func() (string, error) { return "", errors.New("exit status 128") }
	if err := (hostInstaller{d: d}).Ready(); err == nil || !strings.Contains(err.Error(), "flocc checkout") {
		t.Fatal(err)
	}
	d.repoRoot = func() (string, error) { return "/repo", nil }
	if err := (hostInstaller{d: d}).Ready(); err != nil {
		t.Fatal(err)
	}
}

func TestUsageNamesHostCreate(t *testing.T) {
	if !strings.Contains(usage, "flocc host create [--cpus N] [--memory GiB] [--disk GiB] <machine> <name>") {
		t.Fatal(usage)
	}
}
