package create

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// Installer runs the desktop's host verbs (Part 2b) once the VM is on the tailnet.
type Installer interface {
	// Ready fails when ship would, for a reason known before create changes the machine.
	Ready() error
	// RepoRoot is the flocc checkout this runs in.
	RepoRoot() (string, error)
	// HostKnown reports whether name has a host file, and fails when that file reaches the host
	// some other way than ssh: skipping host add would then ship to that host, not the VM.
	HostKnown(name, ssh string) (bool, error)
	AddHost(ctx context.Context, name, ssh, machine string) error
	Ship(ctx context.Context, name string) error
	Login(ctx context.Context, name string) error
}

func Steps() []Step { return []Step{Incus{}, Root{}, VM{}, Egress{}, Tailnet{}} }

func Create(ctx context.Context, e *Env, inst Installer, steps []Step) error {
	st := e.UI.Step("Connect")
	st.Detail("ssh " + e.Spec.Machine)
	// Attached, so a Tailscale SSH check-mode URL for the machine shows on the terminal instead
	// of hanging a captured command; ControlPersist carries the approval through the run.
	if err := e.M.Interactive(ctx, host.Cmd{Name: "true", Why: "ssh to " + e.Spec.Machine}); err != nil {
		return failStep(st, fmt.Errorf("ssh %s: %w", e.Spec.Machine, err))
	}
	if err := e.Probe(ctx); err != nil {
		return failStep(st, err)
	}
	st.Done(e.Spec.Machine)
	if err := RunSteps(ctx, e, steps); err != nil {
		return err
	}
	name := e.Spec.Name
	if err := waitTailnet(ctx, e); err != nil {
		return err
	}
	ssh := "root@" + name
	known, err := inst.HostKnown(name, ssh)
	if err != nil {
		return failStep(e.UI.Step("Host file"), err)
	}
	if !known {
		if err := e.phase("Add host", "host add", func() error { return inst.AddHost(ctx, name, ssh, e.Spec.Machine) }); err != nil {
			return err
		}
	}
	if err := e.phase("Ship", "ship", func() error { return inst.Ship(ctx, name) }); err != nil {
		return err
	}
	if err := e.phase("Login", "login", func() error { return inst.Login(ctx, name) }); err != nil {
		return err
	}
	// host add, ship and login reach the VM, not the machine, and can leave the machine's master
	// idle past ControlPersist; a fresh captured connection would wait on a check-mode URL unseen.
	if err := e.connect(ctx); err != nil {
		return failStep(e.UI.Step("Connect"), err)
	}
	return snapshot(ctx, e)
}

// phase runs one of the installer's verbs. The verbs draw their own steps, so a failure that
// already failed a row is not drawn again; one that failed none (a bad host name, a missing key) is
// drawn here, and always when an earlier row had already failed or ended pending.
func (e *Env) phase(name, verb string, fn func() error) error {
	already := e.UI.Failed()
	err := fn()
	if err == nil {
		return nil
	}
	err = fmt.Errorf("%s: %w", verb, err)
	if already || !e.UI.Failed() {
		e.UI.Step(name).Fail(err)
	}
	return err
}

// connect opens the machine's shared master on this terminal without BatchMode, so the owner can
// answer a host-key question or a Tailscale check-mode URL; it does nothing while one is live.
func (e *Env) connect(ctx context.Context) error {
	return ship.Connect(ctx, e.Local, ship.Dest{SSH: e.Spec.Machine, Opts: ControlOpts()}, e.Spec.Machine, e.TTY)
}

const (
	tailnetPoll  = 2 * time.Second
	tailnetPolls = 30
)

// waitTailnet waits until this desktop's tailnet lists the node: host add and ship reach it by
// name right after tailscale up returns, which can be before this desktop has heard of it.
func waitTailnet(ctx context.Context, e *Env) error {
	st := e.UI.Step("Tailnet name")
	for i := range tailnetPolls {
		seen, err := tailnetHas(ctx, e, e.Spec.Name)
		if err != nil {
			return failStep(st, err)
		}
		if seen {
			st.Done(e.Spec.Name + " is on this desktop's tailnet")
			return nil
		}
		st.Detail(fmt.Sprintf("waiting for the tailnet name, %ds left", (tailnetPolls-i)*int(tailnetPoll/time.Second)))
		e.Sleep(tailnetPoll)
	}
	return failStep(st, fmt.Errorf("%s joined the tailnet, but this desktop does not see it after %s; check tailscale status here, then run create again", e.Spec.Name, tailnetPoll*tailnetPolls))
}

// snapshot takes the new snapshot under a temporary name and swaps it in afterwards, so a failed
// snapshot leaves the last good installed one.
func snapshot(ctx context.Context, e *Env) error {
	st := e.UI.Step("Snapshot")
	st.Detail("taking installed-new")
	if err := takeSnapshot(ctx, e); err != nil {
		return failStep(st, err)
	}
	name := e.Spec.Name
	st.Done(name + "/installed; " + e.incusLine("snapshot restore "+name+" installed") + " returns to it")
	return nil
}

func takeSnapshot(ctx context.Context, e *Env) error {
	name := e.Spec.Name
	has := func(snap string) bool { return e.ok(ctx, e.incus("query", "/1.0/instances/"+name+"/snapshots/"+snap)) }
	do := func(args ...string) error {
		_, err := e.must(ctx, e.incus(append([]string{"snapshot"}, args...)...))
		return err
	}
	if has("installed-new") {
		// Without installed, a run stopped between the delete and the rename below: installed-new
		// is the only good snapshot.
		args := []string{"delete", name, "installed-new"}
		if !has("installed") {
			args = []string{"rename", name, "installed-new", "installed"}
		}
		if err := do(args...); err != nil {
			return err
		}
	}
	if err := do("create", name, "installed-new"); err != nil {
		return err
	}
	if has("installed") {
		if err := do("delete", name, "installed"); err != nil {
			return err
		}
	}
	if err := do("rename", name, "installed-new", "installed"); err != nil {
		return err
	}
	return nil
}

const usage = "usage: flocc host create [--cpus N] [--memory GiB] [--disk GiB] <machine> <name>"

// validName fits an Incus instance name and a MagicDNS label at once.
var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

func ParseArgs(args []string) (Spec, error) {
	fs := flag.NewFlagSet("flocc host create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	s := Spec{}
	fs.IntVar(&s.CPUs, "cpus", 4, "vCPUs")
	fs.IntVar(&s.MemoryGiB, "memory", 8, "memory in GiB")
	fs.IntVar(&s.DiskGiB, "disk", 40, "root disk in GiB")
	if err := fs.Parse(args); err != nil {
		return s, fmt.Errorf("%w\n%s", err, usage)
	}
	if fs.NArg() != 2 {
		return s, errors.New(usage)
	}
	s.Machine, s.Name = fs.Arg(0), fs.Arg(1)
	switch {
	case s.Machine == "" || strings.HasPrefix(s.Machine, "-"):
		return s, fmt.Errorf("machine %q: give an ssh destination", s.Machine)
	case !validName.MatchString(s.Name):
		return s, fmt.Errorf("name %q: use 2 to 63 lowercase letters, digits and '-', starting with a letter", s.Name)
	case s.CPUs < 1 || s.MemoryGiB < 1 || s.DiskGiB < 10:
		return s, errors.New("--cpus and --memory must be at least 1, --disk at least 10")
	}
	return s, nil
}

// Check fails when ship or host add would, for a reason known before create changes the machine,
// the firewall, the VM or the tailnet.
func Check(spec Spec, inst Installer) error {
	if err := inst.Ready(); err != nil {
		return err
	}
	_, err := inst.HostKnown(spec.Name, "root@"+spec.Name)
	return err
}

// Run makes the host once its arguments and the installer are checked, logging to
// ~/.cache/flocc/create-<name>.log, which the session's record names. Every failure after the log
// opens is a failed step of s.
func Run(ctx context.Context, spec Spec, inst Installer, s ui.Session, tty bool) error {
	logPath, logf, err := openLog(spec.Name)
	if err != nil {
		return failStep(s.Step("Log"), err)
	}
	defer logf.Close()
	s.LogPath(logPath)
	log := NewRedactLog(logf)
	run := host.Exec{Log: log, Term: s}
	// Captured commands buffer stderr, so one that stops at a password or host-key prompt would
	// hang with nothing shown; BatchMode makes it fail instead.
	opts := append([]string{"-o", "BatchMode=yes"}, ControlOpts()...)
	e := &Env{M: Remote{R: run, Target: spec.Machine, Opts: opts}, Local: run,
		UI: s, Log: log, Spec: spec, Sleep: time.Sleep, TTY: tty, Checkout: inst.RepoRoot}
	if t, ok := inst.(interface{ Tailnet() KeyMinter }); ok {
		e.Tailnet = t.Tailnet()
	}
	if err := e.connect(ctx); err != nil {
		return failStep(s.Step("Connect"), err)
	}
	return Create(ctx, e, inst, Steps())
}

// Logged names each host a create run has logged for. A run that stopped before host add left
// its log and no host file, which flocc setup offers to continue.
func Logged() []string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "flocc", "create-*.log"))
	var names []string
	for _, p := range paths {
		names = append(names, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "create-"), ".log"))
	}
	return names
}

func openLog(name string) (string, *os.File, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", nil, err
	}
	dir = filepath.Join(dir, "flocc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "create-"+name+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	return path, f, err
}
