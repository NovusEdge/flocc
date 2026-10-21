package cardinal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/create"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/steps"
)

func testDeps(t *testing.T, f *host.Fake) (Deps, *strings.Builder) {
	var out strings.Builder
	return Deps{Out: &out, Err: &out, Home: t.TempDir(), Run: f, Open: func(string) {}}, &out
}

var card = saved.Saved{Machine: "owner@zer0-day-nimble", User: "cardinal", RenewHour: 4}

const incusJSON = `[
 {"name":"flocc-nimble","devices":{},"expanded_devices":{"eth0":{"network":"incusbr0","type":"nic"}},
  "state":{"network":{"eth0":{"addresses":[{"family":"inet6","address":"fd42::1"},{"family":"inet","address":"10.131.47.206"}]}}}},
 {"name":"flocc-cardtest","devices":{"eth0":{"type":"nic"}},"expanded_devices":{"eth0":{"type":"nic"}},
  "state":{"network":{"eth0":{"addresses":[{"family":"inet","address":"10.131.47.50"}]}}}},
 {"name":"pinned","devices":{},"expanded_devices":{"eth0":{"ipv4.address":"10.131.47.9"}},"state":null},
 {"name":"kairos-dev","devices":{},"expanded_devices":{},"state":null}
]`

func TestIncusPassPinsAddressesAndSkipsOthers(t *testing.T) {
	f := host.NewFake().
		On("incus list -f json", 0, incusJSON).
		On("incus network get incusbr0 ipv4.address", 0, "10.131.47.1/24\n").
		On("incus config device override flocc-nimble eth0 ipv4.address=10.131.47.206", 0, "").
		On("incus config device set flocc-cardtest eth0 ipv4.address=10.131.47.50", 0, "")
	d, _ := testDeps(t, f)
	ce := &create.Env{M: f, Spec: create.Spec{Machine: "zer0-day-nimble"}}
	files := []hosts.Host{{Name: "flocc-nimble", SSH: "root@flocc-nimble"}, {Name: "flocc-cardtest", SSH: "root@flocc-cardtest"},
		{Name: "pinned", SSH: "root@pinned"}, {Name: "la-nimble", SSH: "root@la-nimble"}, {Name: "sb", Daytona: "sb"}}
	scope, skipped, gw, err := d.incusPass(context.Background(), ce, nil, files)
	if err != nil {
		t.Fatalf("%v; %v", err, f.Calls)
	}
	if gw != "10.131.47.1" || len(scope) != 3 || scope[0].Addr != "10.131.47.206" || scope[2].Addr != "10.131.47.9" {
		t.Fatalf("gw %q scope %+v", gw, scope)
	}
	if len(skipped) != 2 || !strings.Contains(skipped[0], "la-nimble: not an Incus instance") || !strings.Contains(skipped[1], "Daytona") {
		t.Fatalf("skipped %v", skipped)
	}
	if slices.ContainsFunc(f.Calls, func(c string) bool { return strings.Contains(c, "device") && strings.Contains(c, "pinned") }) {
		t.Fatal("re-pinned an instance that already has an address")
	}
	if _, _, _, err := d.incusPass(context.Background(), ce, []string{"nope"}, files); err == nil {
		t.Fatal("--only with an unknown host passed")
	}
}

func TestUserCommandsPinForwardingAndUseTheHostPart(t *testing.T) {
	d, _ := testDeps(t, host.NewFake())
	c := d.user(card, true, "tmux -L cardinal attach -r -t =cardinal")
	argv := strings.Join(c.Args, " ")
	if !strings.Contains(argv, "-o ForwardAgent=no -o ForwardX11=no -o ClearAllForwardings=yes") || !strings.Contains(argv, " cardinal@zer0-day-nimble ") {
		t.Fatalf("argv %q", argv)
	}
}

func TestRootPassIsSkippedWhenItsOutcomesHold(t *testing.T) {
	d, _ := testDeps(t, host.NewFake())
	f := d.Run.(*host.Fake)
	for _, remote := range []string{"id -u cardinal", `test "$(loginctl show-user cardinal -p Linger --value)" = yes`, "systemctl is-active --quiet flocc-cardinal-egress@cardinal.service"} {
		f.On(d.owner(card.Machine, false, remote).String(), 0, "")
	}
	f.On(d.user(card, false, "true").String(), 0, "")
	if why := d.rootOutcomes(context.Background(), card); why != "" {
		t.Fatalf("outcomes: %q", why)
	}
	f.On(d.owner(card.Machine, false, "systemctl is-active --quiet flocc-cardinal-egress@cardinal.service").String(), 3, "")
	if why := d.rootOutcomes(context.Background(), card); why != "no tailnet block" {
		t.Fatalf("outcomes: %q", why)
	}
}

func TestRootPassRefusesWithoutATerminal(t *testing.T) {
	d, _ := testDeps(t, host.NewFake())
	err := d.rootPass(context.Background(), card, t.TempDir(), "ssh-ed25519 AAAA k", "cardinal-root", false)
	if err == nil || !strings.Contains(err.Error(), "run this in a terminal") {
		t.Fatalf("err = %v", err)
	}
}

func TestRootPassRunsOneSudoFromTheMktempStage(t *testing.T) {
	f := host.NewFake()
	d, _ := testDeps(t, f)
	d.TTY = true
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "flocc-setup"), []byte("x"), 0o755)
	f.On(d.owner(card.Machine, false, "mktemp -d /tmp/flocc-cardinal.XXXXXX").String(), 0, "/tmp/flocc-cardinal.Ab12Cd\n")
	f.On(d.scp(d.Mux, card.Machine+":/tmp/flocc-cardinal.Ab12Cd/", filepath.Join(bin, "flocc-setup"), filepath.Join(bin, "desktop.pub")).String(), 0, "")
	if err := d.rootPass(context.Background(), card, bin, "ssh-ed25519 AAAA k", "cardinal-root", true); err != nil {
		t.Fatalf("%v; %v", err, f.Calls)
	}
	want := "interactive: " + d.owner(card.Machine, true, "chmod 755 /tmp/flocc-cardinal.Ab12Cd/flocc-setup && sudo /tmp/flocc-cardinal.Ab12Cd/flocc-setup --phase cardinal-root --user cardinal --adopt && rm -rf /tmp/flocc-cardinal.Ab12Cd").String()
	if !f.Ran(want) || strings.Count(strings.Join(f.Calls, "\n"), "sudo") != 1 {
		t.Fatalf("calls %v", f.Calls)
	}
	f.On(d.owner(card.Machine, false, "mktemp -d /tmp/flocc-cardinal.XXXXXX").String(), 0, "/tmp/flocc-setup\n")
	if err := d.rootPass(context.Background(), card, bin, "ssh-ed25519 AAAA k", "cardinal-root", true); err == nil {
		t.Fatal("accepted a stage mktemp did not name")
	}
}

func TestHostPassNamesTheFixForAStaleHostKey(t *testing.T) {
	f := host.NewFake()
	d, _ := testDeps(t, f)
	h := hosts.Host{Name: "flocc-nimble", SSH: "root@flocc-nimble"}
	f.Responses[d.onHost(h, "true").String()] = host.Result{Code: 255, Stderr: "@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@\n"}
	_, err := d.hostPass(context.Background(), h, t.TempDir(), "cardinal-host", steps.HostDoc{})
	if err == nil || !strings.Contains(err.Error(), "ssh-keygen -R flocc-nimble") || !strings.Contains(err.Error(), "incus exec flocc-nimble") {
		t.Fatalf("err = %v", err)
	}
}

func TestUserPassReportsAndSanitizesFailures(t *testing.T) {
	f := host.NewFake()
	d, _ := testDeps(t, f)
	bin := t.TempDir()
	f.On(d.user(card, false, "mkdir -p ~/"+userStage).String(), 0, "")
	opts := append(append([]string{}, saved.Pinned...), d.Mux...)
	f.On(d.scp(opts, card.Dest()+":"+userStage+"/", filepath.Join(bin, "flocc"), filepath.Join(bin, "flocc-setup")).String(), 0, "")
	run := d.user(card, false, "chmod 755 ~/"+userStage+"/flocc ~/"+userStage+"/flocc-setup && ~/"+userStage+"/flocc-setup --phase cardinal --answers -").String()
	f.Responses[run] = host.Result{Code: 1, Stdout: "\x1b]52;c;evil\x07boom\n" + steps.CardinalReport{Error: "Units: \x1b[2Jbad"}.Line() + "\n"}
	_, err := d.userPass(context.Background(), card, bin, steps.CardinalDoc{User: "cardinal"})
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\x07") || !strings.Contains(err.Error(), "Units:") {
		t.Fatalf("err = %q", err)
	}
	var doc steps.CardinalDoc
	if json.Unmarshal([]byte(f.Stdins[run]), &doc) != nil || doc.User != "cardinal" {
		t.Fatalf("stdin %q", f.Stdins[run])
	}
}

func TestRenderStatusExitsOneOnFailures(t *testing.T) {
	age := 60.0
	ok := steps.CardinalStatus{Unit: "active", Login: "full", SessionAgeS: &age, LastTurnAgeS: &age}
	if _, code := RenderStatus(ok); code != 0 {
		t.Fatal("healthy status failed")
	}
	bad := ok
	bad.Login, bad.Pane = "none", "\x1b[31mred\x1b[0m\ncode=SECRET"
	out, code := RenderStatus(bad)
	if code != 1 || !strings.Contains(out, "flocc cardinal login") || strings.Contains(out, "\x1b") {
		t.Fatalf("code %d out %q", code, out)
	}
}

func TestMainRemoveMarksTheSavedFileRemoved(t *testing.T) {
	f := host.NewFake()
	d, _ := testDeps(t, f)
	d.RepoRoot = func() (string, error) { return "/repo", nil }
	s := card
	s.Hosts = nil
	s.Save(saved.Dir(d.Home))
	f.On(d.user(s, false, removeUnits).String(), 0, "")
	f.On(d.owner(s.Machine, false, "uname -m").String(), 0, "x86_64\n")
	d.Run = prefixFake{f}
	if code := Main(context.Background(), d, []string{"remove"}); code != 0 {
		t.Fatalf("exit %d; %v", code, f.Calls)
	}
	got, err := saved.Load(saved.Dir(d.Home), "cardinal")
	if err != nil || !got.Removed {
		t.Fatalf("%+v %v", got, err)
	}
}

// prefixFake answers go build calls, whose output paths are temp directories.
type prefixFake struct{ *host.Fake }

func (p prefixFake) Run(ctx context.Context, c host.Cmd) (host.Result, error) {
	if c.Name == "go" {
		return host.Result{}, nil
	}
	return p.Fake.Run(ctx, c)
}

func TestMainRefusesABadUserAndUnknownSubcommand(t *testing.T) {
	d, _ := testDeps(t, host.NewFake())
	for _, args := range [][]string{{"status", "--user", "../x"}, {"frob"}, {"status", "extra"}} {
		if code := Main(context.Background(), d, args); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

func TestAnthropicURL(t *testing.T) {
	for u, want := range map[string]bool{
		"https://claude.ai/oauth/authorize?x": true,
		"https://claude.com/cai/oauth":        true,
		"https://claude.ai.evil.example/oauth": false,
		"http://claude.ai/oauth":              false,
		"https://evil.example/oauth":          false,
	} {
		if anthropicURL(u) != want {
			t.Errorf("%s: %v", u, !want)
		}
	}
}
