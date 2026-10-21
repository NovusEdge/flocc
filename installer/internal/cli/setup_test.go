package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"charm.land/huh/v2"

	"github.com/NovusEdge/flocc/installer/internal/create"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
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
		checkout:   func() (string, string, error) { return "/repo", "0.8.0", nil },
		fillKeys:   func(_ context.Context, s []keys.Slot) error { l.add("keys %v", s); return nil },
		checkKeys:  func(context.Context) []string { return nil },
		hosts:      func() []hosts.Host { return []hosts.Host{nimble} },
		saveHost:   func(h hosts.Host) error { l.add("save %s machine=%s", h.Name, h.Machine); return nil },
		createLogs: func() []string { return nil },
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
		offline: func(context.Context, string) bool { return false },
	}
}

// setupScript answers by title prefix: a bool for Confirm, a string for Ask, an int for Choose.
// An unscripted Confirm takes its default, so a test can see what the default is; an unscripted
// Ask is left empty. The other prompter methods are nil and panic: the wizard must not call them.
type setupScript struct {
	prompter
	answers map[string]any
	err     error // returned by the prompt whose title starts with errAt
	errAt   string
	asked   []string
}

func (s *setupScript) answer(title string) (any, error) {
	s.asked = append(s.asked, title)
	if s.errAt != "" && strings.HasPrefix(title, s.errAt) {
		return nil, s.err
	}
	for p, v := range s.answers {
		if strings.HasPrefix(title, p) {
			return v, nil
		}
	}
	return nil, nil
}

func (s *setupScript) Confirm(title string, def bool) (bool, error) {
	v, err := s.answer(title)
	if v == nil || err != nil {
		return def, err
	}
	return v.(bool), nil
}

func (s *setupScript) Ask(title, _ string) (string, error) {
	v, err := s.answer(title)
	if v == nil || err != nil {
		return "", err
	}
	return v.(string), nil
}

func (s *setupScript) Choose(title string, _ []string) (int, error) {
	v, err := s.answer(title)
	if err != nil {
		return 0, err
	}
	if v == nil {
		return 0, fmt.Errorf("unscripted choice %q", title)
	}
	return v.(int), nil
}

func setupDeps(t *testing.T, answers map[string]any) (*deps, *bytes.Buffer, *setupScript) {
	t.Helper()
	d, out, _ := testDeps(t)
	p := &setupScript{answers: answers}
	d.prompt, d.tty = p, true
	return d, out, p
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
