package create

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// wantFw is a machine already set up for uplink zone up.
func wantFw(up string) fwState {
	return fwState{
		Zones:      []string{"docker", "flocc-incus", "public", "trusted"},
		Policies:   []string{"allow-host-ipv6", "docker-forwarding", "flocc-incus-out", "flocc-no-docker"},
		BridgeZone: "flocc-incus", Services: []string{"dhcp", "dns"},
		OutIn: []string{"flocc-incus"}, OutEg: []string{up}, OutTarget: "ACCEPT",
		NoDockerIn: []string{"flocc-incus"}, NoDockerEg: []string{"docker"}, NoDockerTarget: "REJECT",
		NoDockerPriority: -100, DockerFwdPriority: -1,
	}
}

// laptopFw is the laptop as of 2026-10-20: incusbr0 in trusted, Docker's zone and policy, nothing
// of flocc's.
func laptopFw() fwState {
	return fwState{
		Zones:      []string{"docker", "public", "trusted"},
		Policies:   []string{"allow-host-ipv6", "docker-forwarding"},
		BridgeZone: "trusted", DockerFwdPriority: -1,
	}
}

// onFirewall answers readFirewall's queries from s, as the runtime or the permanent configuration.
func onFirewall(f *host.Fake, permanent bool, s fwState) {
	q := "sudo -n firewall-cmd "
	if permanent {
		q += "--permanent "
	}
	j := func(v []string) string { return strings.Join(v, " ") + "\n" }
	f.On(q+"--get-zones", 0, j(s.Zones))
	f.On(q+"--get-policies", 0, j(s.Policies))
	if s.BridgeZone == "" {
		f.On(q+"--get-zone-of-interface=incusbr0", 2, "no zone\n")
	} else {
		f.On(q+"--get-zone-of-interface=incusbr0", 0, s.BridgeZone+"\n")
	}
	f.On(q+"--zone=flocc-incus --list-services", 0, j(s.Services))
	f.On(q+"--zone=flocc-incus --list-ports", 0, j(s.Ports))
	f.On(q+"--policy=flocc-incus-out --list-ingress-zones", 0, j(s.OutIn))
	f.On(q+"--policy=flocc-incus-out --list-egress-zones", 0, j(s.OutEg))
	f.On(q+"--policy=flocc-incus-out --get-target", 0, s.OutTarget+"\n")
	f.On(q+"--policy=flocc-no-docker --list-ingress-zones", 0, j(s.NoDockerIn))
	f.On(q+"--policy=flocc-no-docker --list-egress-zones", 0, j(s.NoDockerEg))
	f.On(q+"--policy=flocc-no-docker --get-target", 0, s.NoDockerTarget+"\n")
	f.On(q+"--policy=flocc-no-docker --get-priority", 0, strconv.Itoa(s.NoDockerPriority)+"\n")
	f.On(q+"--policy=docker-forwarding --get-priority", 0, strconv.Itoa(s.DockerFwdPriority)+"\n")
}

// fwMachine is a machine with firewalld running and its default route on wlan0, in zone public.
func fwMachine(runtime, permanent fwState) *host.Fake {
	f := host.NewFake().
		On("systemctl is-active --quiet firewalld.service", 0, "").
		On("ip -j route show default", 0, `[{"dst":"default","gateway":"192.168.1.1","dev":"wlan0","protocol":"dhcp"}]`).
		On("sudo -n firewall-cmd --get-zone-of-interface=wlan0", 0, "public\n").
		On("sudo -n firewall-cmd --get-default-zone", 0, "public\n")
	onFirewall(f, false, runtime)
	onFirewall(f, true, permanent)
	return f
}

func TestFwFixesForTheLaptopAsItIs(t *testing.T) {
	var got []string
	for _, args := range fwFixes(laptopFw(), "public") {
		got = append(got, strings.Join(args, " "))
	}
	want := []string{
		"--permanent --new-zone=flocc-incus",
		"--permanent --zone=flocc-incus --add-service=dhcp",
		"--permanent --zone=flocc-incus --add-service=dns",
		"--permanent --zone=flocc-incus --change-interface=incusbr0",
		"--permanent --new-policy=flocc-incus-out",
		"--permanent --policy=flocc-incus-out --add-ingress-zone=flocc-incus",
		"--permanent --policy=flocc-incus-out --add-egress-zone=public",
		"--permanent --policy=flocc-incus-out --set-target=ACCEPT",
		"--permanent --new-policy=flocc-no-docker",
		"--permanent --policy=flocc-no-docker --add-ingress-zone=flocc-incus",
		"--permanent --policy=flocc-no-docker --add-egress-zone=docker",
		"--permanent --policy=flocc-no-docker --set-target=REJECT",
		"--permanent --policy=flocc-no-docker --set-priority=-100",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}

func TestFwFixesNothingWhenSetUp(t *testing.T) {
	if c := fwFixes(wantFw("public"), "public"); len(c) != 0 {
		t.Fatalf("%q", c)
	}
	if p := fwProblems(wantFw("public"), "public"); len(p) != 0 {
		t.Fatalf("%q", p)
	}
}

func TestFwZoneAllowsOnlyDhcpAndDns(t *testing.T) {
	s := wantFw("public")
	s.Services = []string{"dhcp", "dns", "ssh", "mdns"}
	s.Ports = []string{"8080/tcp"}
	got := fwFixes(s, "public")
	want := [][]string{
		{"--permanent", "--zone=flocc-incus", "--remove-service=ssh"},
		{"--permanent", "--zone=flocc-incus", "--remove-service=mdns"},
		{"--permanent", "--zone=flocc-incus", "--remove-port=8080/tcp"},
	}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("%q", got)
	}
	if p := fwProblems(s, "public"); len(p) != 1 || !strings.Contains(p[0], "not only dhcp and dns") {
		t.Fatalf("%q", p)
	}
}

func TestFwFixesFollowTheUplinkZone(t *testing.T) {
	got := fwFixes(wantFw("home"), "public")
	want := [][]string{
		{"--permanent", "--policy=flocc-incus-out", "--remove-egress-zone=home"},
		{"--permanent", "--policy=flocc-incus-out", "--add-egress-zone=public"},
	}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("%q", got)
	}
}

func TestFwNoDockerRunsBeforeDockerForwarding(t *testing.T) {
	s := wantFw("public")
	s.DockerFwdPriority = -200
	got := fwFixes(s, "public")
	if len(got) != 1 || strings.Join(got[0], " ") != "--permanent --policy=flocc-no-docker --set-priority=-201" {
		t.Fatalf("%q", got)
	}
}

func TestFwWithoutDockerHasNoDockerPolicy(t *testing.T) {
	s := laptopFw()
	s.Zones = []string{"public", "trusted"}
	s.Policies = []string{"allow-host-ipv6"}
	for _, args := range fwFixes(s, "public") {
		if strings.Contains(strings.Join(args, " "), "flocc-no-docker") {
			t.Fatalf("%q", args)
		}
	}
}

func TestFirewalldSkippedWhenNotRunning(t *testing.T) {
	f := host.NewFake().On("systemctl is-active --quiet firewalld.service", 3, "inactive\n")
	e, _ := testEnv(f)
	e.sudo = false
	ok, detail, err := Firewalld{}.Check(context.Background(), e)
	if !ok || err != nil || !strings.Contains(detail, "firewalld not running") {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
	if len(f.Calls) != 1 {
		t.Fatalf("%v", f.Calls)
	}
}

func TestFirewalldNeedsPasswordlessSudo(t *testing.T) {
	f := fwMachine(laptopFw(), laptopFw())
	e, _ := testEnv(f)
	e.sudo = false
	if _, _, err := (Firewalld{}).Check(context.Background(), e); err == nil || !strings.Contains(err.Error(), "passwordless sudo") {
		t.Fatal(err)
	}
	if err := (Firewalld{}).Apply(context.Background(), e); err == nil || !strings.Contains(err.Error(), "passwordless sudo") {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "firewall-cmd") {
			t.Errorf("ran before stopping: %s", c)
		}
	}
}

func TestFirewalldCheckPassesWithoutWriting(t *testing.T) {
	f := fwMachine(wantFw("public"), wantFw("public"))
	e, _ := testEnv(f)
	ok, detail, err := Firewalld{}.Check(context.Background(), e)
	if !ok || err != nil || !strings.Contains(detail, "forwarded only to zone public") {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "--new-") || strings.Contains(c, "--add-") || strings.Contains(c, "--set-") || strings.Contains(c, "--reload") {
			t.Errorf("writes: %s", c)
		}
	}
}

func TestFirewalldDetailSaysWhenTailscaleSharesTheUplinkZone(t *testing.T) {
	f := fwMachine(wantFw("public"), wantFw("public")).
		On("ip link show dev tailscale0", 0, "").
		On("sudo -n firewall-cmd --get-zone-of-interface=tailscale0", 0, "public\n")
	e, _ := testEnv(f)
	ok, detail, _ := Firewalld{}.Check(context.Background(), e)
	if !ok || !strings.Contains(detail, "tailscale0 is in the same zone") {
		t.Fatalf("%v %q", ok, detail)
	}
}

// An interface bound to no zone falls in the default zone, which is usually the uplink's.
func TestFirewalldDetailSaysWhenAnUnboundTailscaleFallsInTheUplinkZone(t *testing.T) {
	f := fwMachine(wantFw("public"), wantFw("public")).
		On("ip link show dev tailscale0", 0, "").
		On("sudo -n firewall-cmd --get-zone-of-interface=tailscale0", 2, "no zone\n")
	e, _ := testEnv(f)
	ok, detail, _ := Firewalld{}.Check(context.Background(), e)
	if !ok || !strings.Contains(detail, "tailscale0 is in the same zone") {
		t.Fatalf("%v %q", ok, detail)
	}
}

// firewall-cmd also answers "no zone" for an interface that does not exist.
func TestFirewalldDetailIsQuietWithoutTailscale(t *testing.T) {
	f := fwMachine(wantFw("public"), wantFw("public")).
		On("ip link show dev tailscale0", 1, "").
		On("sudo -n firewall-cmd --get-zone-of-interface=tailscale0", 2, "no zone\n")
	e, _ := testEnv(f)
	ok, detail, _ := Firewalld{}.Check(context.Background(), e)
	if !ok || strings.Contains(detail, "tailscale0") {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestFwBackend(t *testing.T) {
	for conf, want := range map[string]string{
		"":                             "nftables",
		"# FirewallBackend=iptables\n": "nftables",
		"DefaultZone=public\nFirewallBackend=nftables\n": "nftables",
		"FirewallBackend = iptables\n":                   "iptables",
	} {
		if got := fwBackend(conf); got != want {
			t.Errorf("%q: %q", conf, got)
		}
	}
}

// With the iptables backend firewalld's rules sit behind Docker's DOCKER-USER ACCEPT for
// incusbr0, so flocc-no-docker would never see the VM's traffic.
func TestFirewalldStopsOnTheIptablesBackend(t *testing.T) {
	f := fwMachine(laptopFw(), laptopFw()).
		On("sudo -n cat /etc/firewalld/firewalld.conf", 0, "DefaultZone=public\nFirewallBackend=iptables\n")
	e, _ := testEnv(f)
	ok, detail, err := Firewalld{}.Check(context.Background(), e)
	if ok || err != nil || !strings.Contains(detail, "iptables backend") {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
	err = (Firewalld{}).Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "FirewallBackend=nftables") || !strings.Contains(err.Error(), "DOCKER-USER") {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "--new-") || strings.Contains(c, "--add-") || strings.Contains(c, "--set-") || strings.Contains(c, "--reload") {
			t.Errorf("writes: %s", c)
		}
	}
}

const bridgesJSON = `[{"ifindex":3,"ifname":"incusbr0"},{"ifindex":4,"ifname":"docker0"},` +
	`{"ifindex":5,"ifname":"br-0123456789ab"},{"ifindex":6,"ifname":"br-lan"}]`

// fwDockerMachine is fwMachine set up, with Docker running and its bridges in the given zones;
// "" is an unbound bridge.
func fwDockerMachine(docker0, br string) *host.Fake {
	f := fwMachine(wantFw("public"), wantFw("public")).
		On("systemctl is-active --quiet docker.service", 0, "").
		On("ip -j link show type bridge", 0, bridgesJSON)
	for dev, z := range map[string]string{"docker0": docker0, "br-0123456789ab": br} {
		if z == "" {
			f.On("sudo -n firewall-cmd --get-zone-of-interface="+dev, 2, "no zone\n")
		} else {
			f.On("sudo -n firewall-cmd --get-zone-of-interface="+dev, 0, z+"\n")
		}
	}
	return f
}

func TestFirewalldPassesWithDockersBridgesInItsZone(t *testing.T) {
	f := fwDockerMachine("docker", "docker")
	e, _ := testEnv(f)
	ok, detail, err := Firewalld{}.Check(context.Background(), e)
	if !ok || err != nil {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
	if f.Ran("sudo -n firewall-cmd --get-zone-of-interface=br-lan") {
		t.Fatalf("read a bridge that is not Docker's: %v", f.Calls)
	}
}

// flocc-no-docker rejects only traffic into zone docker; a Docker bridge elsewhere, such as the
// uplink's zone, is open to the VM through flocc-incus-out.
func TestFirewalldCatchesADockerBridgeOutsideDockersZone(t *testing.T) {
	for _, z := range []string{"public", ""} {
		e, _ := testEnv(fwDockerMachine("docker", z))
		ok, detail, err := Firewalld{}.Check(context.Background(), e)
		if ok || err != nil || !strings.Contains(detail, "Docker's bridge br-0123456789ab is in zone public, so flocc-no-docker does not cover it") ||
			!strings.Contains(detail, "firewall-cmd --zone=docker --change-interface=br-0123456789ab") {
			t.Fatalf("%q: %v %q %v", z, ok, detail, err)
		}
	}
}

// With Docker stopped no container runs, and Docker binds its bridges again when it starts.
func TestFirewalldIgnoresBridgesWhileDockerIsStopped(t *testing.T) {
	f := fwDockerMachine("public", "public").
		On("systemctl is-active --quiet docker.service", 3, "inactive\n")
	e, _ := testEnv(f)
	if ok, detail, err := (Firewalld{}).Check(context.Background(), e); !ok || err != nil {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
}

// A reload unbinds Docker's bridges until Docker, told over D-Bus, binds them again.
func TestFirewalldApplyWaitsForDockerToRebindItsBridges(t *testing.T) {
	f := fwDockerMachine("docker", "docker").
		On("sudo -n firewall-cmd --reload", 0, "success\n").
		OnSeq("sudo -n firewall-cmd --get-zone-of-interface=docker0",
			host.Result{Code: 2, Stdout: "no zone\n"}, host.Result{Code: 2, Stdout: "no zone\n"}, host.Result{Stdout: "docker\n"})
	e, _ := testEnv(f)
	sleeps := 0
	e.Sleep = func(time.Duration) { sleeps++ }
	if err := (Firewalld{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if sleeps != 2 {
		t.Fatalf("sleeps=%d %v", sleeps, f.Calls)
	}
	if ok, detail, _ := (Firewalld{}).Check(context.Background(), e); !ok {
		t.Fatal(detail)
	}
	dockerUntouched(t, f.Calls)
}

// Docker's zone is in the runtime configuration only: the permanent check must still want
// flocc-no-docker, or the two checks would disagree and no write could satisfy both.
func TestFirewalldWantsNoDockerWhenOnlyTheRuntimeHasDockersZone(t *testing.T) {
	perm := wantFw("public")
	perm.Zones = []string{"flocc-incus", "public", "trusted"}
	perm.Policies = []string{"allow-host-ipv6", "flocc-incus-out"}
	perm.NoDockerIn, perm.NoDockerEg, perm.NoDockerTarget, perm.NoDockerPriority = nil, nil, "", 0
	e, _ := testEnv(fwMachine(wantFw("public"), perm))
	ok, detail, _ := Firewalld{}.Check(context.Background(), e)
	if ok || !strings.HasPrefix(detail, "permanent: ") || !strings.Contains(detail, "flocc-no-docker") {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestFirewalldCheckCatchesAStaleRuntime(t *testing.T) {
	runtime := wantFw("public")
	runtime.BridgeZone = "trusted"
	e, _ := testEnv(fwMachine(runtime, wantFw("public")))
	ok, detail, _ := Firewalld{}.Check(context.Background(), e)
	if ok || !strings.HasPrefix(detail, "runtime: incusbr0 is in zone \"trusted\"") {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestFirewalldApplyWritesPermanentThenReloadsOnce(t *testing.T) {
	f := fwMachine(laptopFw(), laptopFw()).
		On("sudo -n firewall-cmd --reload", 0, "success\n")
	for _, args := range fwFixes(laptopFw(), "public") {
		f.On("sudo -n firewall-cmd "+strings.Join(args, " "), 0, "success\n")
	}
	e, _ := testEnv(f)
	if err := (Firewalld{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	for _, c := range f.Calls {
		if c == "sudo -n firewall-cmd --reload" {
			reloads++
		}
	}
	if reloads != 1 || f.Calls[len(f.Calls)-1] != "sudo -n firewall-cmd --reload" {
		t.Fatalf("%v", f.Calls)
	}
	if !f.Ran("sudo -n firewall-cmd --permanent --zone=flocc-incus --change-interface=incusbr0") {
		t.Fatalf("%v", f.Calls)
	}
	dockerUntouched(t, f.Calls)
	for _, c := range rootCalls(f.Calls) {
		if !strings.HasPrefix(c, "sudo -n ") {
			t.Errorf("not sudo -n: %s", c)
		}
	}
}

// The machine was set up while its uplink was in zone home; wlan0 is now in public.
func TestFirewalldRepointsTheForwardPolicyWhenTheUplinkChangesZone(t *testing.T) {
	f := fwMachine(wantFw("home"), wantFw("home")).
		On("sudo -n firewall-cmd --permanent --policy=flocc-incus-out --remove-egress-zone=home", 0, "success\n").
		On("sudo -n firewall-cmd --permanent --policy=flocc-incus-out --add-egress-zone=public", 0, "success\n").
		On("sudo -n firewall-cmd --reload", 0, "success\n")
	e, _ := testEnv(f)
	ok, detail, _ := Firewalld{}.Check(context.Background(), e)
	if ok || !strings.Contains(detail, "policy flocc-incus-out does not forward flocc-incus to public") {
		t.Fatalf("%v %q", ok, detail)
	}
	if err := (Firewalld{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("sudo -n firewall-cmd --permanent --policy=flocc-incus-out --remove-egress-zone=home") ||
		f.Calls[len(f.Calls)-1] != "sudo -n firewall-cmd --reload" {
		t.Fatalf("%v", f.Calls)
	}
}

func TestUplinkZoneFallsBackToTheDefaultZone(t *testing.T) {
	f := host.NewFake().
		On("ip -j route show default", 0, `[{"dev":"enp0s31f6"}]`).
		On("sudo -n firewall-cmd --get-zone-of-interface=enp0s31f6", 2, "no zone\n").
		On("sudo -n firewall-cmd --get-default-zone", 0, "home\n")
	e, _ := testEnv(f)
	if z, err := uplinkZone(context.Background(), e); err != nil || z != "home" {
		t.Fatalf("%q %v", z, err)
	}
}

func TestUplinkZoneNeedsADefaultRoute(t *testing.T) {
	e, _ := testEnv(host.NewFake().On("ip -j route show default", 0, "[]"))
	if _, err := uplinkZone(context.Background(), e); err == nil || !strings.Contains(err.Error(), "no default route") {
		t.Fatal(err)
	}
}
