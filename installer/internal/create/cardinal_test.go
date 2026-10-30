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
