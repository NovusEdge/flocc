package cardinal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/create"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

func testDeps(t *testing.T, f *host.Fake) (Deps, *ui.Recorder) {
	rec := &ui.Recorder{}
	return Deps{UI: ui.New(rec), Home: t.TempDir(), Run: f, Open: func(string) {}}, rec
}

// rowText is every ended step as "name detail", for substring checks.
func rowText(rec *ui.Recorder) string {
	var b strings.Builder
	for _, e := range rec.Rows() {
		b.WriteString(e.Name + " " + e.Detail + e.Err + "\n")
	}
	return b.String()
}

// orderRenderer logs notes and handoffs in the order they happen, which a Recorder keeps apart.
type orderRenderer struct {
	ui.Recorder
	order []string
}

func (o *orderRenderer) Draw(e ui.Event) {
	if e.Kind == ui.KNote {
		o.order = append(o.order, "note: "+e.Text)
	}
	o.Recorder.Draw(e)
}

func (o *orderRenderer) Handoff(why string, c *exec.Cmd) error {
	o.order = append(o.order, "handoff: "+why)
	return o.Recorder.Handoff(why, c)
}

// handoffRunner stands in for host.Exec with a Term: interactive commands go through the session.
type handoffRunner struct {
	*host.Fake
	term host.Terminal
}

func (h handoffRunner) Interactive(ctx context.Context, c host.Cmd) error {
	h.term.Handoff(c.Why, nil)
	return h.Fake.Interactive(ctx, c)
}

var card = saved.Saved{Machine: "owner@zer0-day-nimble", User: "cardinal", RenewHour: 4}

// The live flocc-nimble, as incus list -f json printed it on 2026-10-21 (trimmed to the fields
// read): the guest names its NIC enp5s0 and has a second global address on tailscale0.
func TestIncusPassTakesTheBridgeAddressFromARecordedList(t *testing.T) {
	rec, err := os.ReadFile("testdata/incus-list-flocc-nimble.json")
	if err != nil {
		t.Fatal(err)
	}
	f := host.NewFake().
		On("incus list -f json", 0, string(rec)).
		On("incus network get incusbr0 ipv4.address", 0, "10.131.47.1/24\n").
		On("incus config device override flocc-nimble eth0 ipv4.address=10.131.47.206", 0, "")
	d, _ := testDeps(t, f)
	ce := &create.Env{M: f, Spec: create.Spec{Machine: "zer0-day-nimble"}}
	scope, _, gw, err := d.incusPass(context.Background(), ce, nil, []hosts.Host{{Name: "flocc-nimble", SSH: "root@flocc-nimble"}})
	if err != nil || gw != "10.131.47.1" || len(scope) != 1 || scope[0].Addr != "10.131.47.206" {
		t.Fatalf("gw %q scope %+v err %v; %v", gw, scope, err, f.Calls)
	}
	if !f.Ran("incus config device override flocc-nimble eth0 ipv4.address=10.131.47.206") {
		t.Fatalf("not pinned: %v", f.Calls)
	}
}

const incusJSON = `[
 {"name":"flocc-cardtest","devices":{"eth0":{"type":"nic"}},"expanded_devices":{"eth0":{"type":"nic"}},
  "state":{"network":{"enp5s0":{"addresses":[{"family":"inet","address":"10.131.47.50","scope":"global"}]}}}},
 {"name":"pinned","devices":{},"expanded_devices":{"eth0":{"ipv4.address":"10.131.47.9"}},"state":null},
 {"name":"kairos-dev","devices":{},"expanded_devices":{},"state":null}
]`

func TestIncusPassPinsAddressesAndSkipsOthers(t *testing.T) {
	f := host.NewFake().
		On("incus list -f json", 0, incusJSON).
		On("incus network get incusbr0 ipv4.address", 0, "10.131.47.1/24\n").
		On("incus config device set flocc-cardtest eth0 ipv4.address=10.131.47.50", 0, "")
	d, _ := testDeps(t, f)
	ce := &create.Env{M: f, Spec: create.Spec{Machine: "zer0-day-nimble"}}
	files := []hosts.Host{{Name: "flocc-cardtest", SSH: "root@flocc-cardtest"},
		{Name: "pinned", SSH: "root@pinned"}, {Name: "la-nimble", SSH: "root@la-nimble"}, {Name: "sb", Daytona: "sb"}}
	scope, skipped, gw, err := d.incusPass(context.Background(), ce, nil, files)
	if err != nil {
		t.Fatalf("%v; %v", err, f.Calls)
	}
	if gw != "10.131.47.1" || len(scope) != 2 || scope[0].Addr != "10.131.47.50" || scope[1].Addr != "10.131.47.9" {
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
	on := func(remote string, code int, out string) {
		f.On(d.owner(card.Machine, false, remote).String(), code, out)
	}
	on("id -u cardinal", 0, "1002\n")
	on("id -nG cardinal", 0, "cardinal\n")
	on("cat "+steps.EgressRulesPath("cardinal"), 0, steps.EgressRules("cardinal", 1002))
	on("cat "+steps.EgressUnitPath, 0, steps.EgressUnit)
	on(`test "$(loginctl show-user cardinal -p Linger --value)" = yes`, 0, "")
	on("systemctl is-active --quiet flocc-cardinal-egress@cardinal.service", 0, "")
	f.On(d.user(card, false, "true").String(), 0, "")
	if why := d.rootOutcomes(context.Background(), card); why != "" {
		t.Fatalf("outcomes: %q", why)
	}
	on("cat "+steps.EgressRulesPath("cardinal"), 0, "table inet old\n")
	if why := d.rootOutcomes(context.Background(), card); why != "tailnet block missing or out of date" {
		t.Fatalf("outcomes: %q", why)
	}
	on("id -nG cardinal", 0, "cardinal docker\n")
	if why := d.rootOutcomes(context.Background(), card); why != "cardinal is in group docker" {
		t.Fatalf("outcomes: %q", why)
	}
}

func TestRootPassRefusesWithoutATerminal(t *testing.T) {
	d, _ := testDeps(t, host.NewFake())
	err := d.rootPass(context.Background(), card, t.TempDir(), "ssh-ed25519 AAAA k", []rootCall{{Phase: "cardinal-root", User: "cardinal", Adopt: false}})
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
	if err := d.rootPass(context.Background(), card, bin, "ssh-ed25519 AAAA k", []rootCall{{Phase: "cardinal-root", User: "cardinal", Adopt: true}}); err != nil {
		t.Fatalf("%v; %v", err, f.Calls)
	}
	want := "interactive: " + d.owner(card.Machine, true, "chmod 755 /tmp/flocc-cardinal.Ab12Cd/flocc-setup && sudo /tmp/flocc-cardinal.Ab12Cd/flocc-setup --phase cardinal-root --user cardinal --adopt && rm -rf /tmp/flocc-cardinal.Ab12Cd").String()
	if !f.Ran(want) || strings.Count(strings.Join(f.Calls, "\n"), "sudo") != 1 {
		t.Fatalf("calls %v", f.Calls)
	}
	f.On(d.owner(card.Machine, false, "mktemp -d /tmp/flocc-cardinal.XXXXXX").String(), 0, "/tmp/flocc-setup\n")
	if err := d.rootPass(context.Background(), card, bin, "ssh-ed25519 AAAA k", []rootCall{{Phase: "cardinal-root", User: "cardinal", Adopt: true}}); err == nil {
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
	run := d.user(card, false, "chmod 755 ~/"+userStage+"/flocc ~/"+userStage+"/flocc-setup && ~/"+userStage+"/flocc-setup --phase cardinal --answers - --events").String()
	f.OnInteractiveOut(run, "\x1b]52;c;evil\x07boom\n"+string(ui.Encode(ui.Event{Kind: ui.KData, Name: "cardinal", Text: steps.CardinalReport{Error: "Units: \x1b[2Jbad"}.JSON()}))).
		OnInteractive(run, errors.New("exit status 1"))
	_, err := d.userPass(context.Background(), card, bin, steps.CardinalDoc{User: "cardinal"})
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\x07") || !strings.Contains(err.Error(), "Units:") {
		t.Fatalf("err = %q", err)
	}
	var doc steps.CardinalDoc
	if json.Unmarshal([]byte(f.Stdins[run]), &doc) != nil || doc.User != "cardinal" {
		t.Fatalf("stdin %q", f.Stdins[run])
	}
}

func TestUserPassDrawsTheHostsEventsAndReturnsItsReport(t *testing.T) {
	f := host.NewFake()
	d, rec := testDeps(t, f)
	bin := t.TempDir()
	f.On(d.user(card, false, "mkdir -p ~/"+userStage).String(), 0, "")
	opts := append(append([]string{}, saved.Pinned...), d.Mux...)
	f.On(d.scp(opts, card.Dest()+":"+userStage+"/", filepath.Join(bin, "flocc"), filepath.Join(bin, "flocc-setup")).String(), 0, "")
	run := d.user(card, false, "chmod 755 ~/"+userStage+"/flocc ~/"+userStage+"/flocc-setup && ~/"+userStage+"/flocc-setup --phase cardinal --answers - --events").String()
	f.OnInteractiveOut(run, string(ui.Encode(ui.Event{Kind: ui.KStep, ID: "h1", Name: "Units", State: ui.Done, Detail: "\x1b[2Jactive"}))+
		string(ui.Encode(ui.Event{Kind: ui.KData, Name: "cardinal", Text: steps.CardinalReport{Key: "ssh-ed25519 AAAA", Login: "full"}.JSON()})))
	r, err := d.userPass(context.Background(), card, bin, steps.CardinalDoc{User: "cardinal"})
	if err != nil || r.Key != "ssh-ed25519 AAAA" || r.Login != "full" {
		t.Fatalf("%+v %v", r, err)
	}
	rows := rec.Rows()
	if len(rows) != 1 || rows[0].Name != "Units" || strings.Contains(rows[0].Detail, "\x1b") {
		t.Fatalf("rows %+v", rows)
	}
}

func TestHostPassDrawsTheHostsEventsAndFailsWithoutAReport(t *testing.T) {
	f := host.NewFake()
	d, rec := testDeps(t, f)
	h := hosts.Host{Name: "flocc-nimble", SSH: "root@flocc-nimble"}
	f.On(d.onHost(h, "true").String(), 0, "")
	f.On(d.onHost(h, "mktemp -d /tmp/flocc-cardinal-host.XXXXXX").String(), 0, "/tmp/flocc-cardinal-host.Ab12Cd\n")
	bin := t.TempDir()
	f.On(d.scp(d.Mux, h.SSH+":/tmp/flocc-cardinal-host.Ab12Cd/", filepath.Join(bin, "flocc-setup")).String(), 0, "")
	run := d.onHost(h, "chmod 755 /tmp/flocc-cardinal-host.Ab12Cd/flocc-setup && /tmp/flocc-cardinal-host.Ab12Cd/flocc-setup --phase cardinal-host --answers - --events; rc=$?; rm -rf /tmp/flocc-cardinal-host.Ab12Cd; exit $rc").String()
	f.OnInteractiveOut(run, string(ui.Encode(ui.Event{Kind: ui.KStep, ID: "h1", Name: "sshd", State: ui.Running}))+"boom\n").
		OnInteractive(run, errors.New("exit status 255"))
	_, err := d.hostPass(context.Background(), h, bin, "cardinal-host", steps.HostDoc{})
	if err == nil || !strings.Contains(err.Error(), "without a report") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	rows := rec.Rows()
	if len(rows) != 1 || rows[0].Name != "sshd" || rows[0].State != ui.Failed {
		t.Fatalf("rows %+v", rows)
	}
}

func TestStatusTableIsTheTableDrawStatusDraws(t *testing.T) {
	age := 60.0
	st := steps.CardinalStatus{Unit: "active", Login: "full", Tmux: true, SessionAgeS: &age}
	tb := StatusTable(st)
	var names []string
	for _, r := range tb.Rows {
		names = append(names, r[0])
	}
	if tb.Title != "cardinal" || strings.Join(names, ",") != "unit,session,tmux,login" {
		t.Fatalf("%q %v", tb.Title, names)
	}
	rec := &ui.Recorder{}
	DrawStatus(ui.New(rec), st)
	var tables []ui.Event
	for _, e := range rec.Events {
		if e.Kind == ui.KTable {
			tables = append(tables, e)
		}
	}
	if len(tables) != 1 {
		t.Fatalf("DrawStatus drew %d tables, want 1", len(tables))
	}
	if !reflect.DeepEqual(*tables[0].Table, tb) {
		t.Errorf("DrawStatus drew %+v, StatusTable gave %+v", *tables[0].Table, tb)
	}
}

func TestDrawStatusExitsOneOnFailures(t *testing.T) {
	age := 60.0
	ok := steps.CardinalStatus{Unit: "active", Login: "full", SessionAgeS: &age, LastTurnAgeS: &age}
	rec := &ui.Recorder{}
	if code := DrawStatus(ui.New(rec), ok); code != 0 || len(rec.Rows()) != 0 {
		t.Fatalf("healthy status: code %d rows %v", code, rec.Rows())
	}
	var table *ui.Table
	for _, e := range rec.Events {
		if e.Kind == ui.KTable {
			table = e.Table
		}
	}
	if table == nil || len(table.Headers) != 2 || table.Rows[0][0] != "unit" {
		t.Fatalf("table %+v", table)
	}
	bad := ok
	bad.Login, bad.Pane = "none", "\x1b[31mred\x1b[0m\ncode=SECRET"
	rec = &ui.Recorder{}
	code := DrawStatus(ui.New(rec), bad)
	rows := rec.Rows()
	if code != 1 || len(rows) != 1 || rows[0].State != ui.Failed || rows[0].Name != "login is none" || rows[0].Err != "flocc cardinal login" {
		t.Fatalf("code %d rows %+v", code, rows)
	}
	for _, e := range rec.Events {
		if strings.Contains(e.Text, "\x1b") {
			t.Fatalf("escape in %+v", e)
		}
	}
}

func TestStatusDrawsAnUnreachableMachineAsAFailedStepAndATodo(t *testing.T) {
	f := host.NewFake()
	d, rec := testDeps(t, f)
	f.On(d.user(card, false, setupPath+" --phase cardinal-status").String(), 255, "")
	if code := d.status(context.Background(), card); code != 1 {
		t.Fatalf("exit %d", code)
	}
	rows := rec.Rows()
	if len(rows) != 1 || rows[0].State != ui.Failed || !strings.Contains(rows[0].Err, "unreachable") {
		t.Fatalf("rows %+v", rows)
	}
	d.UI.Close()
	if !slices.Equal(rec.Todos, []string{"flocc cardinal setup --user cardinal"}) {
		t.Fatalf("todos %v", rec.Todos)
	}
}

// hungRunner is ssh held at a Tailscale check prompt until the status timeout kills it.
type hungRunner struct {
	*host.Fake
	stderr string
}

func (h hungRunner) Run(context.Context, host.Cmd) (host.Result, error) {
	return host.Result{Stderr: h.stderr}, context.DeadlineExceeded
}

func TestStatusNamesATailscaleCheckInsteadOfSetup(t *testing.T) {
	d, rec := testDeps(t, host.NewFake())
	d.Run = hungRunner{host.NewFake(), "# Tailscale SSH requires an additional check.\n# To authenticate, visit: https://login.tailscale.com/a/abc123\n"}
	if code := d.status(context.Background(), card); code != 1 {
		t.Fatalf("exit %d", code)
	}
	rows := rec.Rows()
	if len(rows) != 1 || rows[0].State != ui.Failed || !strings.Contains(rows[0].Err, "https://login.tailscale.com/a/abc123") {
		t.Fatalf("rows %+v", rows)
	}
	d.UI.Close()
	if len(rec.Todos) != 0 {
		t.Fatalf("todos %v", rec.Todos)
	}
}

func TestStatusOnATerminalConnectsFirstSoACheckURLShowsAtOnce(t *testing.T) {
	f := host.NewFake()
	d, rec := testDeps(t, f)
	d.TTY = true
	status := d.user(card, false, setupPath+" --phase cardinal-status").String()
	f.On(status, 0, `{"schema":1,"user":"cardinal","unit":"active","login":"full"}`)
	d.status(context.Background(), card)
	connect, ran := -1, -1
	for i, c := range f.Calls {
		if strings.HasPrefix(c, "interactive: ssh") && strings.HasSuffix(c, card.Dest()+" true") {
			connect = i
		}
		if c == status {
			ran = i
		}
	}
	if connect < 0 || ran < connect {
		t.Fatalf("calls %q", f.Calls)
	}
	if d.UI.Failed() {
		t.Fatalf("rows %+v", rec.Rows())
	}
}

func TestAttachWriteWarnsAboutCtrlDBeforeTheHandoff(t *testing.T) {
	f := host.NewFake()
	o := &orderRenderer{}
	d, _ := testDeps(t, f)
	d.UI = ui.New(o)
	d.Run = handoffRunner{f, d.UI}
	f.On(d.user(card, false, "tmux -L cardinal clear-history -t =cardinal").String(), 0, "")
	if err := d.attach(context.Background(), card, true); err != nil {
		t.Fatal(err)
	}
	want := []string{"handoff: cardinal session on " + card.Dest() + ". " + detachWarning}
	if !slices.Equal(o.order, want) {
		t.Fatalf("order %q", o.order)
	}
	o.order = nil
	if err := d.attach(context.Background(), card, false); err != nil {
		t.Fatal(err)
	}
	if len(o.order) != 1 || !strings.HasPrefix(o.order[0], "handoff") {
		t.Fatalf("read-only attach: %q", o.order)
	}
}

// The note would sit on the alternate screen, which the handoff leaves before the owner reads it.
func TestLoginCarriesTheDetachWarningInTheHandoffText(t *testing.T) {
	f := host.NewFake()
	o := &orderRenderer{}
	d, _ := testDeps(t, f)
	d.UI = ui.New(o)
	d.Run = handoffRunner{f, d.UI}
	d.login(context.Background(), card, "")
	want := []string{"handoff: cardinal login on " + card.Dest() + ". " + detachWarning}
	if !slices.Equal(o.order, want) {
		t.Fatalf("order %q", o.order)
	}
}

func TestRootPassHandsOffWithTheReason(t *testing.T) {
	f := host.NewFake()
	rec := &ui.Recorder{}
	d, _ := testDeps(t, f)
	d.UI = ui.New(rec)
	d.Run = handoffRunner{f, d.UI}
	d.TTY = true
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "flocc-setup"), []byte("x"), 0o755)
	f.On(d.owner(card.Machine, false, "mktemp -d /tmp/flocc-cardinal.XXXXXX").String(), 0, "/tmp/flocc-cardinal.Ab12Cd\n")
	f.On(d.scp(d.Mux, card.Machine+":/tmp/flocc-cardinal.Ab12Cd/", filepath.Join(bin, "flocc-setup")).String(), 0, "")
	d.rootPass(context.Background(), card, bin, "", []rootCall{{Phase: "cardinal-purge", User: "cardinal"}})
	if want := []string{"root pass: sudo on " + card.Machine + " asks for your password"}; !slices.Equal(rec.Handoffs, want) {
		t.Fatalf("handoffs %q", rec.Handoffs)
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

func TestMainErrorIsAFailedStepAndALogTodo(t *testing.T) {
	f := host.NewFake()
	d, rec := testDeps(t, f)
	d.RepoRoot = func() (string, error) { return "", errors.New("not in a checkout \x1b[2J") }
	card.Save(saved.Dir(d.Home))
	f.On(d.user(card, false, removeUnits).String(), 0, "")
	f.On(d.owner(card.Machine, false, "uname -m").String(), 0, "x86_64\n")
	if code := Main(context.Background(), d, []string{"remove"}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	d.UI.Close()
	last := rec.Rows()[len(rec.Rows())-1]
	if last.Name != "cardinal remove" || last.State != ui.Failed || !strings.Contains(last.Err, "not in a checkout") || strings.Contains(last.Err, "\x1b") {
		t.Fatalf("rows %+v", rec.Rows())
	}
	if len(rec.Todos) != 1 || !strings.HasPrefix(rec.Todos[0], "Log: ") || !strings.HasSuffix(rec.Todos[0], "cardinal-cardinal.log") {
		t.Fatalf("todos %v", rec.Todos)
	}
}

// prefixFake answers go build and scp calls, whose paths are temp directories; they are still
// recorded.
type prefixFake struct{ *host.Fake }

func (p prefixFake) Run(ctx context.Context, c host.Cmd) (host.Result, error) {
	if c.Name == "go" || c.Name == "scp" {
		p.Fake.Run(ctx, c)
		return host.Result{}, nil
	}
	return p.Fake.Run(ctx, c)
}

func TestRootCallsRunRemovalsFirstAndOnlyWhatHasWork(t *testing.T) {
	rm := SetupOptions{Remove: []string{"linear-agents"}, Adopt: true}
	for _, c := range []struct {
		why  string
		o    SetupOptions
		want []rootCall
	}{
		{"", SetupOptions{}, nil},
		{"", rm, []rootCall{{Phase: "cardinal-purge", User: "linear-agents"}}},
		{"lingering off", SetupOptions{Adopt: true}, []rootCall{{Phase: "cardinal-root", User: "cardinal", Adopt: true}}},
		{"lingering off", rm, []rootCall{{Phase: "cardinal-purge", User: "linear-agents"}, {Phase: "cardinal-root", User: "cardinal", Adopt: true}}},
	} {
		if got := rootCalls("cardinal", c.why, c.o); !slices.Equal(got, c.want) {
			t.Errorf("why %q remove %v: %+v", c.why, c.o.Remove, got)
		}
	}
}

func TestRootPassRunsPurgeAndRootOnOneLineAndPrintsBothReports(t *testing.T) {
	f := host.NewFake()
	d, out := testDeps(t, f)
	d.TTY = true
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "flocc-setup"), []byte("x"), 0o755)
	f.On(d.owner(card.Machine, false, "mktemp -d /tmp/flocc-cardinal.XXXXXX").String(), 0, "/tmp/flocc-cardinal.Ab12Cd\n")
	f.On(d.scp(d.Mux, card.Machine+":/tmp/flocc-cardinal.Ab12Cd/", filepath.Join(bin, "flocc-setup"), filepath.Join(bin, "desktop.pub")).String(), 0, "")
	st := "/tmp/flocc-cardinal.Ab12Cd/flocc-setup"
	line := d.owner(card.Machine, true, "chmod 755 "+st+" && sudo "+st+" --phase cardinal-purge --user linear-agents && sudo "+st+" --phase cardinal-root --user cardinal --adopt && rm -rf /tmp/flocc-cardinal.Ab12Cd").String()
	purged := steps.CardinalReport{Steps: []steps.ReportStep{{Name: "Archive", Detail: "/root/flocc-removed-linear-agents-x.tar.gz", Done: true}}}
	root := steps.CardinalReport{Steps: []steps.ReportStep{{Name: "User", Detail: "exists", Done: true}}}
	f.OnInteractiveOut(line, purged.Line()+"\n"+root.Line()+"\n")
	calls := []rootCall{{Phase: "cardinal-purge", User: "linear-agents"}, {Phase: "cardinal-root", User: "cardinal", Adopt: true}}
	if err := d.rootPass(context.Background(), card, bin, "ssh-ed25519 AAAA k", calls); err != nil {
		t.Fatalf("%v; %v", err, f.Calls)
	}
	interactive := slices.DeleteFunc(slices.Clone(f.Calls), func(c string) bool { return !strings.HasPrefix(c, "interactive: ") })
	if len(interactive) != 1 || interactive[0] != "interactive: "+line {
		t.Fatalf("calls %v", f.Calls)
	}
	var groups []string
	for _, e := range out.Events {
		if e.Kind == ui.KGroup {
			groups = append(groups, e.Name)
		}
	}
	if !slices.Equal(groups, []string{"Removed linear-agents", "Root pass"}) || !strings.Contains(rowText(out), "flocc-removed-linear-agents-x.tar.gz") {
		t.Fatalf("groups %q rows %q", groups, rowText(out))
	}
}

func TestRemoveOldRunsOnlyThePurgeWithoutTheDesktopKey(t *testing.T) {
	f := host.NewFake()
	d, _ := testDeps(t, f)
	d.TTY, d.RepoRoot = true, func() (string, error) { return "/repo", nil }
	m := card.Machine
	f.On("ssh -O check "+m, 0, "")
	f.On(d.owner(m, false, "uname -m").String(), 0, "x86_64\n")
	f.On(d.owner(m, false, "mktemp -d /tmp/flocc-cardinal.XXXXXX").String(), 0, "/tmp/flocc-cardinal.Ab12Cd\n")
	d.Run = prefixFake{f}
	if err := d.RemoveOld(context.Background(), m, []string{"linear-agents"}); err != nil {
		t.Fatalf("%v; %v", err, f.Calls)
	}
	st := "/tmp/flocc-cardinal.Ab12Cd/flocc-setup"
	want := "interactive: " + d.owner(m, true, "chmod 755 "+st+" && sudo "+st+" --phase cardinal-purge --user linear-agents && rm -rf /tmp/flocc-cardinal.Ab12Cd").String()
	if !f.Ran(want) || slices.ContainsFunc(f.Calls, func(c string) bool { return strings.Contains(c, "desktop.pub") }) {
		t.Fatalf("calls %v", f.Calls)
	}
}

func TestStatusReturnsTheParsedReport(t *testing.T) {
	f := host.NewFake()
	d, _ := testDeps(t, f)
	f.On(d.user(card, false, setupPath+" --phase cardinal-status").String(), 0, `{"schema":1,"user":"cardinal","unit":"active","login":"none"}`)
	st, err := d.Status(context.Background(), card)
	if err != nil || st.Unit != "active" || st.Login != "none" {
		t.Fatalf("%+v %v", st, err)
	}
	f.On(d.user(card, false, setupPath+" --phase cardinal-status").String(), 255, "")
	if _, err := d.Status(context.Background(), card); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("err %v", err)
	}
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
		"https://claude.ai/oauth/authorize?x":  true,
		"https://claude.com/cai/oauth":         true,
		"https://claude.ai.evil.example/oauth": false,
		"http://claude.ai/oauth":               false,
		"https://evil.example/oauth":           false,
	} {
		if anthropicURL(u) != want {
			t.Errorf("%s: %v", u, !want)
		}
	}
}
