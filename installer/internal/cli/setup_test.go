package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"charm.land/huh/v2"

	"github.com/NovusEdge/flocc/installer/internal/cardinal"
	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/create"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

type opsLog struct{ calls []string }

func (l *opsLog) add(format string, a ...any) { l.calls = append(l.calls, fmt.Sprintf(format, a...)) }

func (l *opsLog) has(prefix string) bool {
	return slices.ContainsFunc(l.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

// inOrder fails unless want appear in calls in this order; other calls may come between.
func inOrder(t *testing.T, calls []string, want ...string) {
	t.Helper()
	i := 0
	for _, c := range calls {
		if i < len(want) && c == want[i] {
			i++
		}
	}
	if i < len(want) {
		t.Fatalf("missing %q in order; calls:\n  %s", want[i], strings.Join(calls, "\n  "))
	}
}

var nimble = hosts.Host{Name: "flocc-nimble", SSH: "root@flocc-nimble", Machine: "zer0-day-nimble"}

// doneOps is a desktop with nothing left to do: one host on zer0-day-nimble, every key stored.
func doneOps(l *opsLog) setupOps {
	return setupOps{
		checkout:        func() (string, string, error) { return "/repo", "0.8.0", nil },
		fillKeys:        func(_ context.Context, s []keys.Slot) error { l.add("keys %v", s); return nil },
		checkKeys:       func(context.Context) []string { return nil },
		tailnetClient:   func() (tailnetAPI, bool) { return nil, false },
		tailscaleStatus: func(context.Context) ([]byte, error) { return []byte(tsStatus(true)), nil },
		hosts:           func() []hosts.Host { return []hosts.Host{nimble} },
		saveHost:        func(h hosts.Host) error { l.add("save %s machine=%s", h.Name, h.Machine); return nil },
		createLogs:      func() []string { return nil },
		machine: func(_ context.Context, m string) machineState {
			l.add("machine %s", m)
			return machineState{Sudo: true}
		},
		create: func(_ context.Context, s create.Spec) error { l.add("create %s %s", s.Machine, s.Name); return nil },
		add: func(_ context.Context, name string, r hosts.Host) error {
			l.add("add %s %s", name, r.Target())
			return nil
		},
		ship: func(_ context.Context, name string) error { l.add("ship %s", name); return nil },
		readHost: func(_ context.Context, h hosts.Host) hostState {
			l.add("read %s", h.Name)
			full := "full"
			return hostState{Version: "0.8.0", Login: &full}
		},
		offline:  func(context.Context, string) bool { return false },
		oldUsers: func(_ context.Context, m string) ([]oldUser, error) { l.add("old %s", m); return nil, nil },
		findUser: func(context.Context, string, string) (oldUser, bool) { return oldUser{}, false },
		cardinal: func() (saved.Saved, bool, error) {
			return saved.Saved{Machine: "zer0-day-nimble", User: "cardinal", RenewHour: 4}, true, nil
		},
		cardStatus: func(context.Context, saved.Saved) (steps.CardinalStatus, error) {
			l.add("cardinal status")
			return steps.CardinalStatus{Unit: "active", Login: "full"}, nil
		},
		cardSetup: func(_ context.Context, s saved.Saved, o cardinal.SetupOptions) error {
			l.add("cardinal setup %s adopt=%v remove=%v", s.Machine, o.Adopt, o.Remove)
			return nil
		},
		removeOld: func(_ context.Context, m string, users []string) error { l.add("remove %s %v", m, users); return nil },
		login:     func(_ context.Context, name, email string) error { l.add("login %s %s", name, email); return nil },
		cardLogin: func(_ context.Context, _ saved.Saved, email string) error {
			l.add("cardinal login %s", email)
			return nil
		},
	}
}

// setupScript is the session's renderer in setup tests. It answers by title prefix: a bool for
// Confirm, a string for Ask, an int for Choose. An unscripted Confirm takes its default, so a test
// can see what the default is; an unscripted Ask is left empty; an unscripted Choose fails.
type setupScript struct {
	ui.Recorder
	answers map[string]any
	err     error // returned by the prompt whose title starts with errAt
	errAt   string
	asked   []string
}

func (s *setupScript) Prompt(p ui.PromptReq) (any, error) {
	s.Recorder.Asked = append(s.Recorder.Asked, p)
	title := p.Q.Title
	s.asked = append(s.asked, title)
	if s.errAt != "" && strings.HasPrefix(title, s.errAt) {
		return nil, s.err
	}
	for prefix, v := range s.answers {
		if strings.HasPrefix(title, prefix) {
			return v, nil
		}
	}
	switch p.Kind {
	case ui.PConfirm:
		return p.Q.Default == "yes", nil
	case ui.PAsk:
		return "", nil
	}
	return nil, fmt.Errorf("unscripted prompt %q", title)
}

// shown is everything the session drew, as one string for Contains checks.
type shown struct{ s *setupScript }

func (o shown) String() string {
	var b strings.Builder
	for _, e := range o.s.Events {
		fmt.Fprintf(&b, "%s %s %s %s %s %s\n", e.Kind, e.Name, e.State, e.Detail, e.Err, e.Text)
	}
	return b.String()
}

func setupDeps(t *testing.T, answers map[string]any) (*deps, shown, *setupScript) {
	t.Helper()
	d, _, _ := testDeps(t)
	p := &setupScript{answers: answers}
	d.ui, d.tty = ui.New(p), true
	return d, shown{p}, p
}

func rowOf(t *testing.T, p *setupScript, name string) ui.Event {
	t.Helper()
	for _, r := range p.Rows() {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no row %q in %+v", name, p.Rows())
	return ui.Event{}
}

func todos(p *setupScript) []string {
	var out []string
	for _, e := range p.Events {
		if e.Kind == ui.KTodo {
			out = append(out, e.Text)
		}
	}
	return out
}

func TestSetupNeedsATerminal(t *testing.T) {
	d, _, _ := setupDeps(t, nil)
	d.tty = false
	if code := setupCmd(context.Background(), d, nil); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestSetupStopsWithoutTheCheckout(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.checkout = func() (string, string, error) { return "", "", errors.New("not a git checkout") }
	d, out, _ := setupDeps(t, nil)
	if code := runSetup(context.Background(), d, ops); code != 1 || l.has("keys") || !strings.Contains(out.String(), "not a git checkout") {
		t.Fatalf("exit %d calls %v out %q", code, l.calls, out)
	}
}

func TestSetupCreatesTheFirstHostBetweenTheKeyHalves(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	made := false
	ops.hosts = func() []hosts.Host {
		if made {
			return []hosts.Host{nimble}
		}
		return nil
	}
	ops.create = func(_ context.Context, s create.Spec) error {
		l.add("create %s %s", s.Machine, s.Name)
		made = true
		return nil
	}
	d, _, p := setupDeps(t, map[string]any{"No hosts yet": 0, "Host name": "flocc-nimble", "Machine to make": "zer0-day-nimble"})
	runSetup(context.Background(), d, ops)
	inOrder(t, l.calls,
		fmt.Sprintf("keys %v", []keys.Slot{keys.Linear, keys.Agent}),
		"machine zer0-day-nimble",
		"create zer0-day-nimble flocc-nimble",
		fmt.Sprintf("keys %v", []keys.Slot{keys.Publish, keys.Ntfy}))
	if slices.ContainsFunc(p.asked, func(q string) bool { return strings.HasPrefix(q, "Which machine holds") }) {
		t.Fatal("asked the machine of a host create just made on one")
	}
}

func TestSetupRefusesCreateWithoutPasswordlessSudo(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.hosts = func() []hosts.Host { return nil }
	ops.machine = func(context.Context, string) machineState { return machineState{Sudo: false} }
	d, out, _ := setupDeps(t, map[string]any{"No hosts yet": 0, "Host name": "flocc-nimble", "Machine to make": "zer0-day-nimble"})
	if code := runSetup(context.Background(), d, ops); code != 1 || l.has("create") || !strings.Contains(out.String(), "passwordless sudo") {
		t.Fatalf("exit %d calls %v out %q", code, l.calls, out)
	}
}

func TestSetupOffersToContinueAStoppedCreate(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.hosts = func() []hosts.Host { return nil }
	ops.createLogs = func() []string { return []string{"flocc-nimble"} }
	d, _, p := setupDeps(t, map[string]any{"Continue creating flocc-nimble": true, "Machine to make": "zer0-day-nimble"})
	runSetup(context.Background(), d, ops)
	if !l.has("create zer0-day-nimble flocc-nimble") || slices.ContainsFunc(p.asked, func(q string) bool { return strings.HasPrefix(q, "No hosts yet") }) {
		t.Fatalf("calls %v asked %v", l.calls, p.asked)
	}
	// Unanswered, the offer defaults to no and the wizard asks create-or-add.
	l = opsLog{}
	d, _, p = setupDeps(t, map[string]any{"No hosts yet": 1, "Host name": "box", "How to reach": "root@box"})
	runSetup(context.Background(), d, ops)
	if l.has("create") || !slices.ContainsFunc(p.asked, func(q string) bool { return strings.HasPrefix(q, "No hosts yet") }) {
		t.Fatalf("calls %v asked %v", l.calls, p.asked)
	}
}

func TestSetupAddsAnExistingHostAndShipsIt(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.hosts = func() []hosts.Host { return nil }
	d, _, _ := setupDeps(t, map[string]any{"No hosts yet": 1, "Host name": "box", "How to reach": "root@box"})
	runSetup(context.Background(), d, ops)
	inOrder(t, l.calls, "add box root@box", "ship box")
}

func TestSetupAsksAHostsMachineOnceAndSavesNoneButNotAnEmptyAnswer(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.hosts = func() []hosts.Host {
		return []hosts.Host{{Name: "a", SSH: "root@a"}, {Name: "b", SSH: "root@b"}, {Name: "sb", Daytona: "sb"}, nimble}
	}
	d, _, p := setupDeps(t, map[string]any{"Which machine holds a?": "none"})
	runSetup(context.Background(), d, ops)
	if !l.has("save a machine=none") || l.has("save b") || l.has("save sb") || l.has("save flocc-nimble") {
		t.Fatalf("calls %v", l.calls)
	}
	n := 0
	for _, q := range p.asked {
		if strings.HasPrefix(q, "Which machine holds") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("asked %v", p.asked)
	}
}

func TestSetupStopsAtACancelledPrompt(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.hosts = func() []hosts.Host { return []hosts.Host{{Name: "a", SSH: "root@a"}} }
	d, out, p := setupDeps(t, nil)
	p.errAt, p.err = "Which machine holds", huh.ErrUserAborted
	if code := runSetup(context.Background(), d, ops); code != 1 || l.has(fmt.Sprintf("keys %v", []keys.Slot{keys.Publish, keys.Ntfy})) || !strings.Contains(out.String(), "Stopped") {
		t.Fatalf("exit %d calls %v out %q", code, l.calls, out)
	}
}

func TestSetupShipsAnOldOrFailingHostAndReportsTheRest(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	none := "none"
	ops.hosts = func() []hosts.Host {
		var list []hosts.Host
		for _, n := range []string{"old", "broken", "nologin", "full-disk", "gone"} {
			list = append(list, hosts.Host{Name: n, SSH: "root@" + n, Machine: hosts.MachineNone})
		}
		return list
	}
	reads := map[string][]hostState{
		"old":       {{Version: "0.7.0"}, {Version: "0.8.0"}},
		"broken":    {{Version: "0.8.0", Doctor: []string{"tmux: no server"}}, {Version: "0.8.0"}},
		"nologin":   {{Version: "0.8.0", Login: &none}},
		"full-disk": {{Version: "0.8.0", LowDisk: "disk: 1.0 GB free"}},
		"gone":      {{Err: "ssh: connect timed out"}},
	}
	ops.readHost = func(_ context.Context, h hosts.Host) hostState {
		l.add("read %s", h.Name)
		st := reads[h.Name][0]
		if len(reads[h.Name]) > 1 {
			reads[h.Name] = reads[h.Name][1:]
		}
		return st
	}
	ops.offline = func(_ context.Context, name string) bool { return name == "gone" }
	d, out, _ := setupDeps(t, nil)
	code := runSetup(context.Background(), d, ops)
	if code != 1 || !l.has("ship old") || !l.has("ship broken") || l.has("ship nologin") || l.has("ship full-disk") || l.has("ship gone") {
		t.Fatalf("exit %d calls %v", code, l.calls)
	}
	for _, want := range []string{"admin/machines", "gone.toml", "disk: 1.0 GB free"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in %q", want, out)
		}
	}
}

// A host's row opens before its slow reads, so a terminal shows what the wizard is waiting on.
func TestSetupShowsAHostRowBeforeReadingAndShipping(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	d, _, p := setupDeps(t, nil)
	running := func(detail string) bool {
		return slices.ContainsFunc(p.Events, func(e ui.Event) bool {
			return e.Kind == ui.KStep && e.Name == "flocc-nimble" && e.State == ui.Running && e.Detail == detail
		})
	}
	reads := 0
	ops.readHost = func(context.Context, hosts.Host) hostState {
		reads++
		if reads == 1 && !running("reading flocc --version, status, doctor") {
			t.Error("no Running event before the first read")
		}
		if reads == 2 && !running("shipping the checkout: flocc is not installed there") {
			t.Error("no shipping detail before the second read")
		}
		if reads == 1 {
			return hostState{}
		}
		return hostState{Version: "0.8.0"}
	}
	ops.ship = func(context.Context, string) error {
		if !running("shipping the checkout: flocc is not installed there") {
			t.Error("no Running event before ship")
		}
		return nil
	}
	if code := runSetup(context.Background(), d, ops); code != 0 || reads != 2 {
		t.Fatalf("exit %d reads %d", code, reads)
	}
	if r := rowOf(t, p, "flocc-nimble"); r.State != ui.Done || r.Detail != "flocc 0.8.0, doctor ok" {
		t.Fatalf("%+v", r)
	}
}

func TestSetupGroupsRowsBySection(t *testing.T) {
	var l opsLog
	d, _, p := setupDeps(t, nil)
	runSetup(context.Background(), d, doneOps(&l))
	var groups []string
	for _, e := range p.Events {
		if e.Kind == ui.KGroup {
			groups = append(groups, e.Name)
		}
	}
	if want := []string{"Checkout", "Keys", "Tailnet", "Hosts", "Keys", "Old versions", "Cardinal", "Logins"}; !slices.Equal(groups, want) {
		t.Fatalf("%q", groups)
	}
}

func TestSetupSendsWhatTheOwnerMustDoToTheTodoBox(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.readHost = func(context.Context, hosts.Host) hostState {
		return hostState{Version: "0.8.0", LowDisk: "disk: 1.0 GB free"}
	}
	d, _, p := setupDeps(t, nil)
	if code := runSetup(context.Background(), d, ops); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if r := rowOf(t, p, "flocc-nimble"); r.State != ui.Pending {
		t.Fatalf("%+v", r)
	}
	// The first to-do is the missing Tailscale OAuth client.
	if got := todos(p); len(got) != 2 || !strings.HasPrefix(got[1], "flocc-nimble: disk: 1.0 GB free") {
		t.Fatalf("%q", got)
	}
}

func TestSetupFailsAHostStillFailingAfterShip(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.readHost = func(_ context.Context, h hosts.Host) hostState {
		l.add("read %s", h.Name)
		return hostState{Version: "0.8.0", Doctor: []string{"repo flocc: cannot fetch"}}
	}
	d, out, _ := setupDeps(t, nil)
	if code := runSetup(context.Background(), d, ops); code != 1 || strings.Count(strings.Join(l.calls, "\n"), "ship flocc-nimble") != 1 || !strings.Contains(out.String(), "cannot fetch") {
		t.Fatalf("exit %d calls %v out %q", code, l.calls, out)
	}
}

func TestDoctorFailsLeavesTheLoginAndDiskOutOfShip(t *testing.T) {
	out := "ok    git: /usr/bin/git\nFAIL  claude login: none; run flocc login\nFAIL  disk: 1.0 GB free under /w, below min_free_disk_gb 5\n" +
		"FAIL  tmux: no server; systemctl --user start flocc-tmux.service\n15 checks, 3 failed\n"
	fails, disk := doctorFails(out)
	if !slices.Equal(fails, []string{"tmux: no server; systemctl --user start flocc-tmux.service"}) || !strings.HasPrefix(disk, "disk: 1.0 GB free") {
		t.Fatalf("%q %q", fails, disk)
	}
}

func TestTailnetOfflineFindsThePeerByName(t *testing.T) {
	f := host.NewFake().On("tailscale status --json", 0,
		`{"Peer":{"k1":{"HostName":"la-nimble","DNSName":"la-nimble.tail.ts.net.","Online":false},"k2":{"HostName":"flocc-nimble","DNSName":"flocc-nimble.tail.ts.net.","Online":true}}}`)
	if !tailnetOffline(context.Background(), f, "la-nimble") || tailnetOffline(context.Background(), f, "flocc-nimble") || tailnetOffline(context.Background(), f, "nope") {
		t.Fatal("wrong")
	}
}

// withOld gives zer0-day-nimble the linear-agents user until a removal runs.
func withOld(l *opsLog, ops *setupOps) {
	gone := false
	ops.oldUsers = func(_ context.Context, m string) ([]oldUser, error) {
		l.add("old %s", m)
		if gone {
			return nil, nil
		}
		return []oldUser{{Name: "linear-agents", Lingering: true, Since: "Tue 2026-10-20 01:41:28 EEST",
			Procs: []string{"/usr/lib/systemd/systemd --user", "/usr/bin/tmux -L linear-agents -D", "claude --print \x1b[31mred"}}}, nil
	}
	setup, remove := ops.cardSetup, ops.removeOld
	ops.cardSetup = func(ctx context.Context, s saved.Saved, o cardinal.SetupOptions) error {
		gone = gone || len(o.Remove) > 0
		return setup(ctx, s, o)
	}
	ops.removeOld = func(ctx context.Context, m string, u []string) error {
		gone = true
		return remove(ctx, m, u)
	}
}

func TestSetupShowsAnOldUserAndKeepsItByDefault(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	withOld(&l, &ops)
	d, out, _ := setupDeps(t, nil)
	runSetup(context.Background(), d, ops)
	for _, want := range []string{"linear-agents user on zer0-day-nimble", "lingering", "tmux -L linear-agents", "may still be polling Linear"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in %q", want, out)
		}
	}
	if strings.Contains(out.String(), "\x1b[31m") {
		t.Error("printed the old user's escape sequence")
	}
	if l.has("remove") || l.has("cardinal setup") {
		t.Fatalf("removed without a yes: %v", l.calls)
	}
}

func TestSetupRemovesInCardinalsRootPassWhenSetupRuns(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	withOld(&l, &ops)
	ops.cardStatus = func(context.Context, saved.Saved) (steps.CardinalStatus, error) {
		l.add("cardinal status")
		return steps.CardinalStatus{Unit: "failed", Login: "full"}, nil
	}
	d, _, _ := setupDeps(t, map[string]any{"Remove the linear-agents user": true})
	runSetup(context.Background(), d, ops)
	inOrder(t, l.calls, "cardinal setup zer0-day-nimble adopt=true remove=[linear-agents]", "old zer0-day-nimble")
	if l.has("remove ") {
		t.Fatalf("a second root pass: %v", l.calls)
	}
}

func TestSetupRemovesAloneBesideAHealthyCardinal(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	withOld(&l, &ops)
	d, out, _ := setupDeps(t, map[string]any{"Remove the linear-agents user": true})
	if code := runSetup(context.Background(), d, ops); code != 0 {
		t.Fatalf("exit %d %q", code, out)
	}
	if !l.has("remove zer0-day-nimble [linear-agents]") || l.has("cardinal setup") || !strings.Contains(out.String(), "Revoke") {
		t.Fatalf("calls %v out %q", l.calls, out)
	}
}

func TestSetupNoOldUserMeansNoRootWork(t *testing.T) {
	var l opsLog
	d, _, _ := setupDeps(t, nil)
	if code := runSetup(context.Background(), d, doneOps(&l)); code != 0 || l.has("remove") || l.has("cardinal setup") {
		t.Fatalf("exit %d calls %v", code, l.calls)
	}
}

func TestSetupCardinalWhenNoneIsSaved(t *testing.T) {
	cases := []struct {
		name    string
		hasUser bool
		answers map[string]any
		want    string // "" when setup must not run
		code    int
	}{
		{"set up by default", false, nil, "cardinal setup zer0-day-nimble adopt=false remove=[]", 0},
		{"declined", false, map[string]any{"Set up cardinal": false}, "", 0},
		{"adopt", true, map[string]any{"Adopt the existing cardinal user": true}, "cardinal setup zer0-day-nimble adopt=true remove=[]", 0},
		{"adopt defaults to no", true, nil, "", 0},
	}
	for _, c := range cases {
		var l opsLog
		ops := doneOps(&l)
		ops.cardinal = func() (saved.Saved, bool, error) { return saved.Saved{}, false, nil }
		ops.findUser = func(_ context.Context, m, u string) (oldUser, bool) {
			return oldUser{Name: u, UID: "1002", Lingering: true, Procs: []string{"/usr/bin/tmux -L cardinal -D"}}, c.hasUser && u == "cardinal"
		}
		d, _, p := setupDeps(t, c.answers)
		code := runSetup(context.Background(), d, ops)
		if code != c.code || (c.want == "") == l.has("cardinal setup") || (c.want != "" && !l.has(c.want)) {
			t.Errorf("%s: exit %d calls %v", c.name, code, l.calls)
		}
		if c.hasUser {
			q := p.Recorder.Asked[0].Q
			if !slices.Contains(q.Facts, "uid 1002") || !slices.Contains(q.Facts, "lingering") || !slices.Contains(q.Facts, "running /usr/bin/tmux -L cardinal -D") {
				t.Errorf("%s: facts %q", c.name, q.Facts)
			}
		}
	}
}

func TestSetupDoesNotQuietlyRebuildARemovedCardinal(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.cardinal = func() (saved.Saved, bool, error) {
		return saved.Saved{Machine: "zer0-day-nimble", User: "cardinal", Removed: true}, true, nil
	}
	d, _, p := setupDeps(t, nil)
	if code := runSetup(context.Background(), d, ops); code != 0 || l.has("cardinal setup") ||
		!slices.ContainsFunc(p.asked, func(q string) bool { return strings.HasPrefix(q, "Set up cardinal on zer0-day-nimble again") }) {
		t.Fatalf("exit %d calls %v asked %v", code, l.calls, p.asked)
	}
}

func TestSetupWithoutIncusFailsCardinalButStillRemoves(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	withOld(&l, &ops)
	ops.cardStatus = func(context.Context, saved.Saved) (steps.CardinalStatus, error) {
		return steps.CardinalStatus{}, errors.New("cardinal@zer0-day-nimble unreachable")
	}
	ops.machine = func(context.Context, string) machineState {
		return machineState{Incus: errors.New("cannot run incus as an administrator; add it to the incus-admin group")}
	}
	d, out, _ := setupDeps(t, map[string]any{"Remove the linear-agents user": true})
	if code := runSetup(context.Background(), d, ops); code != 1 || l.has("cardinal setup") || !l.has("remove zer0-day-nimble [linear-agents]") || !strings.Contains(out.String(), "incus-admin") {
		t.Fatalf("exit %d calls %v out %q", code, l.calls, out)
	}
}

func TestSetupTreatsOwnerAtMachineAndMachineAsOne(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.hosts = func() []hosts.Host {
		return []hosts.Host{nimble, {Name: "two", SSH: "root@two", Machine: "owner@zer0-day-nimble"}}
	}
	d, _, _ := setupDeps(t, nil)
	runSetup(context.Background(), d, ops)
	if strings.Count(strings.Join(l.calls, "\n"), "old ") != 1 {
		t.Fatalf("calls %v", l.calls)
	}
}

func TestSetupSkipsOldVersionsAndCardinalOnAMachineWhoseHostFailed(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.hosts = func() []hosts.Host {
		return []hosts.Host{nimble, {Name: "other", SSH: "root@other", Machine: "box"}}
	}
	ops.readHost = func(_ context.Context, h hosts.Host) hostState {
		if h.Name == "flocc-nimble" {
			return hostState{Err: "ssh: timed out"}
		}
		return hostState{Version: "0.8.0"}
	}
	d, _, _ := setupDeps(t, nil)
	runSetup(context.Background(), d, ops)
	if l.has("old zer0-day-nimble") || !l.has("old box") || l.has("cardinal status") {
		t.Fatalf("calls %v", l.calls)
	}
}

func TestFindOldReadsTheUserWithoutSudo(t *testing.T) {
	f := host.NewFake().
		On("id -u linear-agents", 0, "1001\n").
		On("loginctl show-user linear-agents -p Linger --value", 0, "yes\n").
		On("loginctl show-user linear-agents -p Timestamp --value", 0, "Tue 2026-10-20 01:41:28 EEST\n").
		On("ps -u linear-agents -o args=", 0, "/usr/lib/systemd/systemd --user\n/usr/bin/tmux -L linear-agents -D\n")
	got, err := findOld(context.Background(), f, []string{"linear-agents", "nobody-else"})
	if err != nil || len(got) != 1 || !got[0].Lingering || got[0].Since == "" || len(got[0].Procs) != 2 {
		t.Fatalf("%+v", got)
	}
	if slices.ContainsFunc(f.Calls, func(c string) bool { return strings.Contains(c, "sudo") }) {
		t.Fatal("used sudo")
	}
}

func TestFindOldFailsWhenSSHDoes(t *testing.T) {
	f := host.NewFake().On("id -u linear-agents", 255, "Host key verification failed.\n")
	if got, err := findOld(context.Background(), f, []string{"linear-agents"}); err == nil || got != nil {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestSetupDoesNotCallAnUnreachableMachineClean(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.machine = func(context.Context, string) machineState { return machineState{Err: errors.New("host key unknown")} }
	d, out, _ := setupDeps(t, nil)
	if code := runSetup(context.Background(), d, ops); code != 1 || l.has("old ") || strings.Contains(out.String(), "none on") {
		t.Fatalf("exit %d calls %v out %q", code, l.calls, out)
	}
	l = opsLog{}
	ops = doneOps(&l)
	ops.oldUsers = func(context.Context, string) ([]oldUser, error) { return nil, errors.New("ssh failed") }
	d, out, _ = setupDeps(t, nil)
	if code := runSetup(context.Background(), d, ops); code != 1 || strings.Contains(out.String(), "none on") || !strings.Contains(out.String(), "could not look") {
		t.Fatalf("exit %d out %q", code, out)
	}
}

func TestSetupDoesNotReportRemovedWhenTheRescanFails(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	withOld(&l, &ops)
	scan := ops.oldUsers
	scans := 0
	ops.oldUsers = func(ctx context.Context, m string) ([]oldUser, error) {
		if scans++; scans > 1 {
			return nil, errors.New("ssh failed")
		}
		return scan(ctx, m)
	}
	ops.removeOld = func(context.Context, string, []string) error { return errors.New("sudo: bad password") }
	d, out, _ := setupDeps(t, map[string]any{"Remove the linear-agents user": true})
	if code := runSetup(context.Background(), d, ops); code != 1 || strings.Contains(out.String(), "removed linear-agents") ||
		!strings.Contains(out.String(), "sudo: bad password") {
		t.Fatalf("exit %d out %q", code, out)
	}
}

func TestSetupAfterASetupOfARemovedCardinalChecksStatusWithoutAsking(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	removed := true
	ops.cardinal = func() (saved.Saved, bool, error) {
		return saved.Saved{Machine: "zer0-day-nimble", User: "cardinal", Removed: removed}, true, nil
	}
	setup := ops.cardSetup
	ops.cardSetup = func(ctx context.Context, s saved.Saved, o cardinal.SetupOptions) error {
		removed = false // what Deps.setup saves
		return setup(ctx, s, o)
	}
	d, _, _ := setupDeps(t, map[string]any{"Set up cardinal on zer0-day-nimble again": true})
	runSetup(context.Background(), d, ops)
	if !l.has("cardinal setup") {
		t.Fatalf("first run: %v", l.calls)
	}
	d, out, p := setupDeps(t, nil)
	if code := runSetup(context.Background(), d, ops); code != 0 || !strings.Contains(out.String(), "status ok") ||
		slices.ContainsFunc(p.asked, func(q string) bool { return strings.HasPrefix(q, "Set up cardinal") }) {
		t.Fatalf("exit %d asked %v out %q", code, p.asked, out)
	}
}

func TestSetupOnAFreshDesktopRunsEveryStepInOrder(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	made := false
	ops.hosts = func() []hosts.Host {
		if made {
			return []hosts.Host{nimble}
		}
		return nil
	}
	ops.create = func(_ context.Context, s create.Spec) error {
		l.add("create %s %s", s.Machine, s.Name)
		made = true
		return nil
	}
	ops.cardinal = func() (saved.Saved, bool, error) { return saved.Saved{}, false, nil }
	ops.cardStatus = func(context.Context, saved.Saved) (steps.CardinalStatus, error) {
		l.add("cardinal status")
		return steps.CardinalStatus{Unit: "active", Login: "none"}, nil
	}
	d, _, _ := setupDeps(t, map[string]any{"No hosts yet": 0, "Host name": "flocc-nimble", "Machine to make": "zer0-day-nimble",
		"claude.ai pool account for cardinal": "pool@example.com"})
	if code := runSetup(context.Background(), d, ops); code != 0 {
		t.Fatalf("exit %d; calls %v", code, l.calls)
	}
	inOrder(t, l.calls,
		fmt.Sprintf("keys %v", []keys.Slot{keys.Linear, keys.Agent}),
		"create zer0-day-nimble flocc-nimble",
		"read flocc-nimble",
		fmt.Sprintf("keys %v", []keys.Slot{keys.Publish, keys.Ntfy}),
		"old zer0-day-nimble",
		"cardinal setup zer0-day-nimble adopt=false remove=[]",
		"cardinal status",
		"cardinal login pool@example.com")
}

func TestSetupOnAFinishedDesktopOnlyReads(t *testing.T) {
	var l opsLog
	d, out, p := setupDeps(t, nil)
	if code := runSetup(context.Background(), d, doneOps(&l)); code != 0 {
		t.Fatalf("exit %d %q", code, out)
	}
	for _, c := range l.calls {
		if !strings.HasPrefix(c, "keys ") && !strings.HasPrefix(c, "read ") && !strings.HasPrefix(c, "old ") && !strings.HasPrefix(c, "machine ") && c != "cardinal status" {
			t.Errorf("changed something: %q", c)
		}
	}
	if len(p.asked) > 0 {
		t.Errorf("asked %v", p.asked)
	}
}

func TestSetupLogsInWhatIsNotFullAndOnlyReportsUnknown(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	none, unknown := "none", "unknown"
	ops.hosts = func() []hosts.Host {
		return []hosts.Host{nimble, {Name: "b", SSH: "root@b", Machine: "zer0-day-nimble"}, {Name: "c", SSH: "root@c", Machine: "zer0-day-nimble"}}
	}
	ops.readHost = func(_ context.Context, h hosts.Host) hostState {
		switch h.Name {
		case "b":
			return hostState{Version: "0.8.0", Login: &none}
		case "c":
			return hostState{Version: "0.8.0", Login: &unknown}
		}
		return hostState{Version: "0.8.0"}
	}
	d, _, _ := setupDeps(t, map[string]any{"claude.ai pool account for b": "pool@example.com"})
	if code := runSetup(context.Background(), d, ops); code != 0 {
		t.Fatalf("exit %d calls %v", code, l.calls)
	}
	if !l.has("login b pool@example.com") || l.has("login c") || l.has("login flocc-nimble") || l.has("cardinal login") {
		t.Fatalf("calls %v", l.calls)
	}
}

func TestSetupRefusesALoginAddressThatIsNotOne(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	none := "none"
	ops.readHost = func(context.Context, hosts.Host) hostState { return hostState{Version: "0.8.0", Login: &none} }
	d, _, _ := setupDeps(t, map[string]any{"claude.ai pool account for flocc-nimble": "me; rm -rf ~"})
	if code := runSetup(context.Background(), d, ops); code != 1 || l.has("login ") {
		t.Fatalf("exit %d calls %v", code, l.calls)
	}
}
