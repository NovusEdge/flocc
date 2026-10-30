package create

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const bridgeGet = "incus network get incusbr0 ipv4.address"

func cardinalEnv(f *host.Fake) *Env {
	e, _ := testEnv(f)
	e.Cardinal = CardinalSpec{Executor: "flocc-a", RenewHour: 4}
	return e
}

func TestBridgeAddrPicksTheAddressOnTheBridge(t *testing.T) {
	f := host.NewFake().On(bridgeGet, 0, "10.10.0.1/24\n").On(stateQuery, 0, vmWithIP)
	ip, subnet, err := cardinalEnv(f).bridgeAddr(context.Background(), "flocc-a")
	if err != nil || ip != "10.10.0.5" || subnet.String() != "10.10.0.0/24" {
		t.Fatalf("%q %v %v", ip, subnet, err)
	}
}

func TestPutWritesThroughStdinAsTheOwner(t *testing.T) {
	f := host.NewFake().OnName("incus", 0, "")
	e := cardinalEnv(f)
	if err := e.put(context.Background(), CardinalName, vmFile{"/home/cardinal/x", 0o600, cardinalUser, []byte("body")}); err != nil {
		t.Fatal(err)
	}
	call := f.Calls[0]
	if !strings.HasPrefix(call, "incus exec flocc-cardinal -- runuser -u cardinal -- sh -c ") || !strings.HasSuffix(call, " sh /home/cardinal/x 600") {
		t.Fatal(call)
	}
	if f.Stdins[call] != "body" {
		t.Fatalf("%q", f.Stdins[call])
	}
	f.Calls = nil
	e.put(context.Background(), CardinalName, vmFile{"/etc/x", 0o644, "root", []byte("b")})
	if strings.Contains(f.Calls[0], "runuser") {
		t.Fatal(f.Calls[0])
	}
}

func TestCardinalBaseMakesTheUserTheSessionDirAndLingers(t *testing.T) {
	f := host.NewFake().
		On("incus exec flocc-cardinal -- id -u cardinal", 1, "").
		OnName("incus", 0, "")
	if err := (CardinalBase{}).Apply(context.Background(), cardinalEnv(f)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"incus exec flocc-cardinal --env DEBIAN_FRONTEND=noninteractive -- apt-get install -y -q tmux openssh-client git curl ca-certificates",
		"incus exec flocc-cardinal -- useradd --create-home --shell /bin/bash cardinal",
		"incus exec flocc-cardinal -- install -d -m 755 -o root -g root /var/lib/flocc-cardinal/session",
		"incus exec flocc-cardinal -- loginctl enable-linger cardinal",
	} {
		if !f.Ran(want) {
			t.Errorf("missing %s in %v", want, f.Calls)
		}
	}
}

func TestCardinalClaudeNeedsTheMonitorVersion(t *testing.T) {
	ver := "incus exec flocc-cardinal -- runuser -l cardinal -c .local/bin/claude --version"
	f := host.NewFake().On(ver, 0, "2.1.299 (Claude Code)\n")
	if ok, detail, _ := (CardinalClaude{}).Check(context.Background(), cardinalEnv(f)); ok || !strings.Contains(detail, "needs 2.1.302") {
		t.Fatal(detail)
	}
	f.On(ver, 0, "2.1.304 (Claude Code)\n")
	if ok, _, _ := (CardinalClaude{}).Check(context.Background(), cardinalEnv(f)); !ok {
		t.Fatal("2.1.304 refused")
	}
}

func TestCardinalKeyMakesAnEd25519KeyOnlyWhenMissing(t *testing.T) {
	pub := "incus exec flocc-cardinal -- cat /home/cardinal/.ssh/id_ed25519.pub"
	f := host.NewFake().On(pub, 1, "").OnName("incus", 0, "")
	e := cardinalEnv(f)
	if ok, _, _ := (CardinalKey{}).Check(context.Background(), e); ok {
		t.Fatal("done without a key")
	}
	if err := (CardinalKey{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(f.Calls, func(c string) bool { return strings.Contains(c, "ssh-keygen -q -t ed25519 -N ''") }) {
		t.Fatal(f.Calls)
	}
	f.On(pub, 0, "ssh-ed25519 AAAAC3Nza cardinal@flocc-cardinal\n")
	if ok, _, _ := (CardinalKey{}).Check(context.Background(), e); !ok {
		t.Fatal("a key is not done")
	}
}

const (
	executorInstance   = "incus query /1.0/instances/flocc-a"
	cardinalInstance   = "incus query /1.0/instances/flocc-cardinal"
	cardinalStateQuery = "incus query /1.0/instances/flocc-cardinal/state"
	profileNIC         = `{"status":"Running","config":{},"devices":{},"expanded_devices":{"eth0":{"type":"nic","network":"incusbr0"}}}`
	cardinalWithIP     = `{"status":"Running","network":{"enp5s0":{"addresses":[{"family":"inet","address":"10.10.0.7","scope":"global"}]}}}`
)

func linkFake() *host.Fake {
	return host.NewFake().
		On(bridgeGet, 0, "10.10.0.1/24").
		On(stateQuery, 0, vmWithIP).
		On(executorInstance, 0, profileNIC).
		On(cardinalStateQuery, 0, cardinalWithIP).
		On(cardinalInstance, 0, profileNIC)
}

func TestPinOverridesAProfileNicAndSetsAnOwnOne(t *testing.T) {
	f := linkFake().OnName("incus", 0, "")
	ip, err := cardinalEnv(f).pin(context.Background(), "flocc-a")
	if err != nil || ip != "10.10.0.5" || !f.Ran("incus config device override flocc-a eth0 ipv4.address=10.10.0.5") {
		t.Fatalf("%q %v %v", ip, err, f.Calls)
	}
	own := `{"status":"Running","devices":{"eth0":{"type":"nic","network":"incusbr0"}},"expanded_devices":{"eth0":{"type":"nic","network":"incusbr0"}}}`
	f = linkFake().On(executorInstance, 0, own).OnName("incus", 0, "")
	cardinalEnv(f).pin(context.Background(), "flocc-a")
	if !f.Ran("incus config device set flocc-a eth0 ipv4.address=10.10.0.5") {
		t.Fatal(f.Calls)
	}
	done := `{"status":"Running","devices":{"eth0":{"type":"nic","network":"incusbr0","ipv4.address":"10.10.0.5"}},"expanded_devices":{"eth0":{"type":"nic","network":"incusbr0","ipv4.address":"10.10.0.5"}}}`
	f = linkFake().On(executorInstance, 0, done)
	if ip, ok := cardinalEnv(f).pinned(context.Background(), "flocc-a"); !ok || ip != "10.10.0.5" {
		t.Fatal("a pinned address is not pinned")
	}
}

func TestKeyLineIsForcedRestrictedAndFromCardinalOnly(t *testing.T) {
	got := keyLine("10.10.0.7", "ssh-ed25519 AAAAC3Nza cardinal@flocc-cardinal")
	want := `restrict,command="exec /home/flocc/.local/bin/flocc-cardinal-gate",from="10.10.0.7" ssh-ed25519 AAAAC3Nza cardinal`
	if got != want {
		t.Fatalf("%s", got)
	}
}

func TestMergeKeysReplacesOnlyCardinalsLine(t *testing.T) {
	old := "ssh-ed25519 OTHER owner@desk\nrestrict,command=\"x\" ssh-ed25519 AAAAC3Nza cardinal\n"
	got := mergeKeys(old, "restrict ssh-ed25519 AAAAC3Nza cardinal")
	if got != "ssh-ed25519 OTHER owner@desk\nrestrict ssh-ed25519 AAAAC3Nza cardinal\n" {
		t.Fatalf("%q", got)
	}
	if mergeKeys("", "L ssh-ed25519 K c") != "L ssh-ed25519 K c\n" {
		t.Fatal("empty file")
	}
}

func TestExecutorSSHPinsAndWritesTheDropInsBeforeInstallingSshd(t *testing.T) {
	f := linkFake().
		On("incus exec flocc-a -- sh -c command -v sshd", 1, "").
		OnName("incus", 0, "")
	if err := (ExecutorSSH{}).Apply(context.Background(), cardinalEnv(f)); err != nil {
		t.Fatal(err)
	}
	at := func(s string) int {
		return slices.IndexFunc(f.Calls, func(c string) bool { return strings.Contains(c, s) })
	}
	pin, dropIn, unit, install := at("device override flocc-a eth0"), at(sshdDropIn), at(sshUnitDropIn), at("openssh-server")
	if pin < 0 || dropIn < pin || unit < 0 || install < dropIn || install < unit || !f.Ran("incus exec flocc-a -- systemctl restart ssh") {
		t.Fatalf("%v", f.Calls)
	}
	conf := f.Stdins[f.Calls[dropIn]]
	for _, want := range []string{"ListenAddress 10.10.0.5\n", "PasswordAuthentication no\n", "AllowUsers flocc\n", "ClientAliveInterval 30\n"} {
		if !strings.Contains(conf, want) {
			t.Errorf("drop-in lacks %q:\n%s", want, conf)
		}
	}
	if !strings.Contains(f.Stdins[f.Calls[unit]], "After=network-online.target") {
		t.Error("sshd does not wait for the network")
	}
}

func TestExecutorFirewallLetsCardinalReachOnlySshd(t *testing.T) {
	f := linkFake().On("incus exec flocc-a -- sh -c command -v nft", 0, "/usr/sbin/nft").OnName("incus", 0, "")
	if err := (ExecutorFirewall{}).Apply(context.Background(), cardinalEnv(f)); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("incus config device override flocc-cardinal eth0 ipv4.address=10.10.0.7") {
		t.Fatal("cardinal's address not pinned")
	}
	i := slices.IndexFunc(f.Calls, func(c string) bool { return strings.Contains(c, fwFile) })
	rules := f.Stdins[f.Calls[i]]
	if !strings.Contains(rules, "ip saddr 10.10.0.7 tcp dport 22 accept\n    ip saddr 10.10.0.7 drop\n") {
		t.Fatalf("%s", rules)
	}
	for _, want := range []string{"incus exec flocc-a -- systemctl enable flocc-cardinal-fw.service", "incus exec flocc-a -- systemctl restart flocc-cardinal-fw.service"} {
		if !f.Ran(want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestExecutorKeyNeedsTheShippedGate(t *testing.T) {
	f := host.NewFake().
		On("incus exec flocc-cardinal -- cat /home/cardinal/.ssh/id_ed25519.pub", 0, "ssh-ed25519 AAAAC3Nza cardinal@x\n").
		On("incus exec flocc-a -- test -x /home/flocc/.local/bin/flocc-cardinal-gate", 1, "")
	err := (ExecutorKey{}).Apply(context.Background(), cardinalEnv(f))
	if err == nil || !strings.Contains(err.Error(), "flocc ship -H flocc-a") {
		t.Fatal(err)
	}
}

func TestCardinalFilesRenderTheHourTheExecutorAndTheLink(t *testing.T) {
	files, err := cardinalFiles(CardinalSpec{Executor: "flocc-a", RenewHour: 4}, "10.10.0.5", "ssh-ed25519 HOSTKEY root@flocc-a\n")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]vmFile{}
	for _, f := range files {
		by[f.Path] = f
	}
	units := "/home/cardinal/.config/systemd/user/"
	checks := map[string]string{
		units + "cardinal-renew.timer":                    "OnCalendar=*-*-* 04:00:00",
		units + "cardinal.service":                        "flocc executor VM flocc-a.",
		"/etc/flocc-cardinal/ssh_config":                  "HostName 10.10.0.5",
		"/etc/flocc-cardinal/known_hosts":                 "10.10.0.5 ssh-ed25519 HOSTKEY\n",
		"/etc/claude-code/managed-settings.json":          `"dontAsk"`,
		"/opt/cardinal/plugin/.claude-plugin/plugin.json": `"cardinal"`,
		"/opt/cardinal/plugin/monitors/monitors.json":     `"always"`,
		"/opt/cardinal/plugin/skills/triage/SKILL.md":     "name: triage",
	}
	for path, want := range checks {
		if !strings.Contains(string(by[path].Body), want) {
			t.Errorf("%s lacks %q", path, want)
		}
	}
	if by["/opt/cardinal/plugin/bin/follow"].Mode != 0o755 || by[units+"cardinal.service"].Owner != cardinalUser {
		t.Fatal("modes or owners")
	}
	if filesSum(files) == filesSum(files[1:]) {
		t.Fatal("the sum ignores a file")
	}
}

func TestCardinalFilesRefuseAMalformedHostKey(t *testing.T) {
	if _, err := cardinalFiles(CardinalSpec{Executor: "flocc-a"}, "10.10.0.5", "garbage"); err == nil {
		t.Fatal("accepted")
	}
}
