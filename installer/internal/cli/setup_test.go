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
