package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// hostCmd's fall-through for an unknown sub-verb also returns 2, with the desktop usage on d.err.
func TestHostCreateIsADesktopVerbThatChecksItsArguments(t *testing.T) {
	d, _, errb := testDeps(t)
	code := run(context.Background(), d, []string{"host", "create", "only-one-arg"})
	if code != 2 || !strings.Contains(errb.String(), "flocc host create: usage: flocc host create [--cpus N]") {
		t.Fatalf("%d d.err=%q", code, errb)
	}
}

// Ship and host add would hit these only after the firewall, the VM and the tailnet changed.
func TestHostCreateChecksTheCheckoutBeforeConnecting(t *testing.T) {
	d, _, _ := testDeps(t)
	rec := &ui.Recorder{}
	d.ui = ui.New(rec)
	d.repoRoot = func() (string, error) { return "", errors.New("exit status 128") }
	if code := hostCreate(context.Background(), d, []string{"box", "flocc-a"}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	rows := rec.Rows()
	if len(rows) != 1 || rows[0].Name != "Check" || rows[0].State != ui.Failed || !strings.Contains(rows[0].Err, "flocc checkout") {
		t.Fatalf("%+v", rows)
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

func TestHostInstallerShipTellsTheOwnerToReshipTheOtherHosts(t *testing.T) {
	d, f, _, rec := shipDeps(t, allKeys)
	f.out["status"] = `{"Self":{"ID":"n1","HostName":"a","TailscaleIPs":["100.64.0.9"]},"Peer":{},"User":{}}`
	if err := hosts.Save(hosts.Dir(d.home), hosts.Host{Name: "b", SSH: "root@b"}); err != nil {
		t.Fatal(err)
	}
	if err := (hostInstaller{d: d}).Ship(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	d.ui.Close()
	if len(rec.Todos) != 1 || !strings.Contains(rec.Todos[0], "flocc ship -H b") || !strings.Contains(rec.Todos[0], "from a") {
		t.Fatalf("todos %q", rec.Todos)
	}
}

func TestUsageNamesHostCreate(t *testing.T) {
	if !strings.Contains(usage, "flocc host create [--cpus N] [--memory GiB] [--disk GiB] <machine> <name>") {
		t.Fatal(usage)
	}
}

// The interface must be a plain nil without a client: a nil *tailnet.Client inside it would pass
// create's nil check and then fail mid-run.
func TestHostInstallerMintsKeysOnlyWithAStoredClient(t *testing.T) {
	d, _, _ := testDeps(t)
	path := filepath.Join(d.home, ".config", "flocc", "secrets.toml")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("linear = \"x\"\n"), 0o600)
	d.store = func() keys.Store { return keys.Open(d.home, io.Discard) }
	if m := (hostInstaller{d: d}).Tailnet(); m != nil {
		t.Fatalf("minter %v without a client", m)
	}
	os.WriteFile(path, []byte("tailscale_oauth = \"cid:secret\"\n"), 0o600)
	if m := (hostInstaller{d: d}).Tailnet(); m == nil {
		t.Fatal("no minter with a stored client")
	}
}
