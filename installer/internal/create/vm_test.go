package create

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const (
	launch     = "incus launch images:debian/13 flocc-a --vm --network incusbr0 -c boot.autostart=true -c limits.cpu=4 -c limits.memory=8GiB -d root,size=40GiB"
	vmRunning  = `{"status":"Running","config":{"boot.autostart":"true"}}`
	vmNoIP     = `{"status":"Running","network":{"lo":{"addresses":[{"family":"inet","address":"127.0.0.1","scope":"local"}]},"enp5s0":{"addresses":[]}}}`
	vmWithIP   = `{"status":"Running","network":{"lo":{"addresses":[{"family":"inet","address":"127.0.0.1","scope":"local"}]},"enp5s0":{"addresses":[{"family":"inet6","address":"fd42::5","scope":"global"},{"family":"inet","address":"10.10.0.5","scope":"global"}]}}}`
	stateQuery = "incus query /1.0/instances/flocc-a/state"
	agentUp    = "incus exec flocc-a -- true"
)

func TestVMRunningWithAnAddressIsDone(t *testing.T) {
	f := host.NewFake().On("incus query /1.0/instances/flocc-a", 0, vmRunning).On(stateQuery, 0, vmWithIP).On(agentUp, 0, "")
	e, _ := testEnv(f)
	if ok, detail, _ := (VM{}).Check(context.Background(), e); !ok || detail != "flocc-a running at 10.10.0.5" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestVMLaunchesAndWaitsForAnAddress(t *testing.T) {
	f := host.NewFake().
		On(launch, 0, "").
		OnSeq(stateQuery, host.Result{Stdout: vmNoIP}, host.Result{Stdout: vmWithIP}).
		On(agentUp, 0, "")
	e, _ := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	sleeps := 0
	e.Sleep = func(d time.Duration) { sleeps++ }
	if err := (VM{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran(launch) || sleeps != 1 {
		t.Fatalf("sleeps=%d %v", sleeps, f.Calls)
	}
	dockerUntouched(t, f.Calls)
}

func TestVMStopsWhenTheTailnetHasTheName(t *testing.T) {
	f := host.NewFake()
	e, _ := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsStatus)
	err := VM{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "already a node on your tailnet") || !strings.Contains(err.Error(), "https://login.tailscale.com/admin/machines") {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "incus launch") {
			t.Fatalf("launched: %s", c)
		}
	}
}

func TestVMResumesAHalfMadeInstance(t *testing.T) {
	f := host.NewFake().
		On("incus query /1.0/instances/flocc-a", 0, `{"status":"Stopped","config":{}}`).
		On("incus config set flocc-a boot.autostart=true", 0, "").
		On("incus start flocc-a", 0, "").
		On(stateQuery, 0, vmWithIP).
		On(agentUp, 0, "")
	e, _ := testEnv(f)
	e.Local = host.NewFake() // a tailnet read would fail the test: the name is ours already
	if err := (VM{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("incus config set flocc-a boot.autostart=true") || !f.Ran("incus start flocc-a") || f.Ran(launch) {
		t.Fatalf("%v", f.Calls)
	}
}

// Ctrl-C ends the wait with the instance already launched; the re-run must not launch again.
func TestVMWaitStopsOnCancelAndARerunDoesNotRelaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := host.NewFake().
		On(launch, 0, "").
		On("incus query /1.0/instances/flocc-a", 127, "").
		On(stateQuery, 0, vmNoIP)
	e, _ := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	sleeps := 0
	e.Sleep = func(time.Duration) {
		if sleeps++; sleeps == 2 {
			cancel()
		}
	}
	if err := (VM{}).Apply(ctx, e); !errors.Is(err, context.Canceled) || sleeps != 2 {
		t.Fatalf("sleeps=%d %v", sleeps, err)
	}

	g := host.NewFake().
		On("incus query /1.0/instances/flocc-a", 0, `{"status":"Stopped","config":{}}`).
		On("incus config set flocc-a boot.autostart=true", 0, "").
		On("incus start flocc-a", 0, "").
		On(stateQuery, 0, vmWithIP).
		On(agentUp, 0, "")
	e2, _ := testEnv(g)
	e2.Local = host.NewFake()
	if ok, _, _ := (VM{}).Check(context.Background(), e2); ok {
		t.Fatal("a stopped instance is not done")
	}
	if err := (VM{}).Apply(context.Background(), e2); err != nil {
		t.Fatal(err)
	}
	if g.Ran(launch) || !g.Ran("incus start flocc-a") || !g.Ran("incus config set flocc-a boot.autostart=true") {
		t.Fatalf("%v", g.Calls)
	}
}

func TestVMCheckWantsAutostart(t *testing.T) {
	f := host.NewFake().On("incus query /1.0/instances/flocc-a", 0, `{"status":"Running","config":{}}`)
	e, _ := testEnv(f)
	if ok, detail, _ := (VM{}).Check(context.Background(), e); ok || detail != "boot.autostart is off" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestVMWaitGivesUp(t *testing.T) {
	f := host.NewFake().
		On("incus query /1.0/instances/flocc-a", 0, vmRunning).
		On(stateQuery, 0, vmNoIP)
	e, _ := testEnv(f)
	err := VM{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "no IPv4 address after 5m0s") || !strings.Contains(err.Error(), "incus console flocc-a --show-log") {
		t.Fatal(err)
	}
}

const curlCheck = "incus exec flocc-a -- curl -fsS -o /dev/null --max-time 15 https://pkgs.tailscale.com/"

func TestEgressInstallsCurlWhenMissing(t *testing.T) {
	f := host.NewFake().
		On("incus exec flocc-a -- sh -c command -v curl", 1, "").
		On("incus exec flocc-a --env DEBIAN_FRONTEND=noninteractive -- apt-get update -q", 0, "").
		On("incus exec flocc-a --env DEBIAN_FRONTEND=noninteractive -- apt-get install -y -q curl ca-certificates", 0, "").
		On(curlCheck, 0, "")
	e, _ := testEnv(f)
	if err := (Egress{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("incus exec flocc-a --env DEBIAN_FRONTEND=noninteractive -- apt-get install -y -q curl ca-certificates") {
		t.Fatalf("%v", f.Calls)
	}
}

func TestEgressNamesTheFirewallWhenAptFails(t *testing.T) {
	f := host.NewFake().
		On("incus exec flocc-a -- sh -c command -v curl", 1, "").
		On("incus exec flocc-a --env DEBIAN_FRONTEND=noninteractive -- apt-get update -q", 100, "Could not resolve deb.debian.org")
	e, _ := testEnv(f)
	err := Egress{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "docs/vm-setup.md") {
		t.Fatal(err)
	}
}

func TestEgressReportsACancelDuringApt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := host.NewFake().
		On("incus exec flocc-a -- sh -c command -v curl", 1, "").
		On("incus exec flocc-a --env DEBIAN_FRONTEND=noninteractive -- apt-get update -q", 130, "")
	e, _ := testEnv(f)
	err := Egress{}.Apply(ctx, e)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "no way out") {
		t.Fatal(err)
	}
}

func TestEgressCheckFailureNamesTheDocs(t *testing.T) {
	e, _ := testEnv(host.NewFake().On(curlCheck, 7, "").On("incus exec flocc-a -- sh -c command -v curl", 0, "/usr/bin/curl\n"))
	if ok, detail, _ := (Egress{}).Check(context.Background(), e); ok || !strings.Contains(detail, "docs/vm-setup.md") {
		t.Fatalf("%v %q", ok, detail)
	}
}

// A fresh Debian image has no curl; its first check must not read as a VM with no way out.
func TestEgressCheckBeforeCurlIsInstalled(t *testing.T) {
	e, _ := testEnv(host.NewFake().On("incus exec flocc-a -- sh -c command -v curl", 1, ""))
	if ok, detail, _ := (Egress{}).Check(context.Background(), e); ok || detail != "curl is not installed in the VM yet" {
		t.Fatalf("%v %q", ok, detail)
	}
}
