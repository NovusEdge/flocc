package create

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
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
	HostKnown(name string) bool
	AddHost(ctx context.Context, name, ssh string) error
	Ship(ctx context.Context, name string) error
	Login(ctx context.Context, name string) error
}

func Steps() []Step { return []Step{Incus{}, Firewalld{}, Docker{}, VM{}, Egress{}, Tailnet{}} }

func Create(ctx context.Context, e *Env, inst Installer, steps []Step) error {
	// Attached, so a Tailscale SSH check-mode URL for the machine shows on the terminal instead
	// of hanging a captured command; ControlPersist carries the approval through the run.
	if err := e.M.Interactive(ctx, cmd("true")); err != nil {
		return fmt.Errorf("ssh %s: %w", e.Spec.Machine, err)
	}
	if err := e.Probe(ctx); err != nil {
		return err
	}
	if err := RunSteps(ctx, e, steps); err != nil {
		return err
	}
	name := e.Spec.Name
	if err := waitTailnet(ctx, e); err != nil {
		return err
	}
	if !inst.HostKnown(name) {
		if err := inst.AddHost(ctx, name, "root@"+name); err != nil {
			return fmt.Errorf("host add: %w", err)
		}
	}
	if err := inst.Ship(ctx, name); err != nil {
		return fmt.Errorf("ship: %w", err)
	}
	if err := inst.Login(ctx, name); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	// host add, ship and login reach the VM, not the machine, and can leave the machine's master
	// idle past ControlPersist; a fresh captured connection would wait on a check-mode URL unseen.
	if err := e.connect(ctx); err != nil {
		return err
	}
	return snapshot(ctx, e)
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
	for range tailnetPolls {
		seen, err := tailnetHas(ctx, e, e.Spec.Name)
		if err != nil {
			return err
		}
		if seen {
			return nil
		}
		e.Sleep(tailnetPoll)
	}
	return fmt.Errorf("%s joined the tailnet, but this desktop does not see it after %s; check tailscale status here, then run create again", e.Spec.Name, tailnetPoll*tailnetPolls)
}

// snapshot takes the new snapshot under a temporary name and swaps it in afterwards, so a failed
// snapshot leaves the last good installed one.
func snapshot(ctx context.Context, e *Env) error {
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
	fmt.Fprintf(e.Out, "✓ %-10s %s/installed; %s returns to it\n", "Snapshot", name, e.incusLine("snapshot restore "+name+" installed"))
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

func Main(args []string, inst Installer) int {
	spec, err := ParseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "flocc host create:", err)
		return 2
	}
	logPath, logf, err := openLog(spec.Name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "flocc host create:", err)
		return 1
	}
	defer logf.Close()
	log := NewRedactLog(logf)
	run := host.Exec{Log: log}
	// Captured commands buffer stderr, so one that stops at a password or host-key prompt would
	// hang with nothing shown; BatchMode makes it fail instead.
	opts := append([]string{"-o", "BatchMode=yes"}, ControlOpts()...)
	st, _ := os.Stdin.Stat()
	e := &Env{M: Remote{R: run, Target: spec.Machine, Opts: opts}, Local: run,
		UI: ui.NewHuh(), Out: os.Stdout, Log: log, Spec: spec, Sleep: time.Sleep,
		TTY: st != nil && st.Mode()&os.ModeCharDevice != 0}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err = e.connect(ctx)
	if err == nil {
		err = Create(ctx, e, inst, Steps())
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nflocc host create: %s\n\nFull log: %s\nRun the same command again to continue.\n", err, logPath)
		return 1
	}
	return 0
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
