package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostCreateIsADesktopVerbThatChecksItsArguments(t *testing.T) {
	d, _, _ := testDeps(t)
	if code := run(context.Background(), d, []string{"host", "create", "only-one-arg"}); code != 2 {
		t.Fatal(code)
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
