// Package create makes a flocc host: an Incus VM on a machine the owner reaches over SSH, joined
// to the tailnet and installed with the desktop's host verbs.
package create

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

const (
	Bridge = "incusbr0"
	Image  = "images:debian/13"
)

type Spec struct {
	Machine   string // ssh destination of the owner's account on the machine
	Name      string // Incus instance, Tailscale hostname and flocc host name
	CPUs      int
	MemoryGiB int
	DiskGiB   int
}

// Prompter is the one question create asks; ui.Huh satisfies it.
type Prompter interface {
	Secret(id, title, help string) (string, error)
}

// RedactLog is the writer behind the run's log. A secret that only becomes known mid-run, such as
// the Tailscale auth key, is registered with Add, and every later write has it replaced by
// <token>: host.Exec logs a command's output before any error exists. It rebuilds Part 2a's
// host.Redact, which takes its secrets when it is made.
type RedactLog struct {
	mu      sync.Mutex
	dst     io.Writer
	secrets []string
	w       io.Writer
}

func NewRedactLog(dst io.Writer) *RedactLog { return &RedactLog{dst: dst, w: dst} }

func (l *RedactLog) Add(secret string) {
	if l == nil || secret == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.secrets = append(l.secrets, secret)
	l.w = host.Redact(l.dst, l.secrets...)
}

func (l *RedactLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

type Env struct {
	M     host.Runner // runs on the machine
	Local host.Runner // runs on this desktop
	UI    Prompter
	Out   io.Writer
	Log   *RedactLog // the log host.Exec writes to; Add registers a secret before it is used
	Spec  Spec
	Sleep func(time.Duration)
	TTY   bool // stdin is a terminal, where ship.Connect can open the machine's master

	prog      *ui.Progress
	stepTotal int  // steps in the last RunSteps, which the Snapshot line aligns its counter column to
	sudo      bool // sudo -n works on the machine
	incusSudo bool // the account is not in incus-admin, so incus goes through sudo
}

func cmd(name string, args ...string) host.Cmd { return host.Cmd{Name: name, Args: args} }

// root runs c as root. Most commands are captured with no terminal, so sudo must never prompt.
func (e *Env) root(c host.Cmd) host.Cmd {
	c.Args = append([]string{"-n", c.Name}, c.Args...)
	c.Name = "sudo"
	return c
}

func (e *Env) incus(args ...string) host.Cmd {
	if e.incusSudo {
		return e.root(cmd("incus", args...))
	}
	return cmd("incus", args...)
}

// Incus is incus on the machine as Probe found it runs: directly, or through sudo -n.
func (e *Env) Incus(args ...string) host.Cmd { return e.incus(args...) }

func (e *Env) ok(ctx context.Context, c host.Cmd) bool {
	r, err := e.M.Run(ctx, c)
	return err == nil && r.Code == 0
}

// out is c's trimmed stdout, or "" when c fails.
func (e *Env) out(ctx context.Context, c host.Cmd) string {
	r, err := e.M.Run(ctx, c)
	if err != nil || r.Code != 0 {
		return ""
	}
	return strings.TrimSpace(r.Stdout)
}

func (e *Env) must(ctx context.Context, c host.Cmd) (string, error) { return host.Must(ctx, e.M, c) }

// Probe finds how to run incus on the machine and whether sudo works there without a password.
func (e *Env) Probe(ctx context.Context) error {
	e.sudo = e.ok(ctx, cmd("sudo", "-n", "true"))
	// incus query also succeeds for the restricted incus group, through that user's own project,
	// so group membership decides, not the query alone.
	admin := slices.Contains(strings.Fields(e.out(ctx, cmd("id", "-nG"))), "incus-admin")
	switch {
	case admin && e.ok(ctx, cmd("incus", "query", "/1.0")):
		e.incusSudo = false
	case e.sudo && e.ok(ctx, e.root(cmd("incus", "query", "/1.0"))):
		e.incusSudo = true
	default:
		return fmt.Errorf("cannot run incus as an administrator on %s: give your account passwordless sudo there, or add it to the incus-admin group with sudo usermod -aG incus-admin \"$USER\" (the restricted incus group is not enough), %s", e.Spec.Machine, regroup(e.Spec.Machine, ControlOpts()))
	}
	return nil
}

// Preconditions reports what the machine allows before anything changes there: whether sudo
// works without a password, which create's firewall and Docker steps need even to check, and
// whether incus runs as an administrator (nil), else the fix. m runs commands on the machine.
func Preconditions(ctx context.Context, m host.Runner, machine string) (sudo bool, incus error) {
	e := &Env{M: m, Spec: Spec{Machine: machine}}
	incus = e.Probe(ctx)
	return e.sudo, incus
}

// regroup says how a new group reaches create's commands: the shared master logged in before the
// usermod and serves every command until ControlPersist after its last use.
func regroup(machine string, opts []string) string {
	for _, o := range opts {
		if strings.HasPrefix(o, "ControlPath=") {
			return "then close create's shared connection, which keeps your old groups for 10 minutes: ssh -o " + quote(o) + " -O exit " + machine
		}
	}
	return "then log in again"
}

// needRoot stops a step that reads or changes the machine's firewall, which only root can.
func (e *Env) needRoot(what string) error {
	if e.sudo {
		return nil
	}
	return fmt.Errorf("%s on %s needs root, and sudo there asks for a password, which create cannot type. Give your account passwordless sudo on %s (a file in /etc/sudoers.d/ written with visudo), then run create again", what, e.Spec.Machine, e.Spec.Machine)
}

type Step interface {
	Name() string
	// Check reports whether the step's outcome already holds. It never changes the machine.
	Check(ctx context.Context, e *Env) (done bool, detail string, err error)
	Apply(ctx context.Context, e *Env) error
}

// RunSteps checks each step just before it would run: a later step's check needs the earlier
// steps' outcome, such as the VM existing.
//
// The steps are numbered by position, done or not, because which of them run is known only as
// each is reached. An applied step shows a live line unless it is Attached.
func RunSteps(ctx context.Context, e *Env, steps []Step) error {
	p := e.progress()
	e.stepTotal = len(steps)
	for i, s := range steps {
		done, detail, err := s.Check(ctx, e)
		if err != nil {
			return fmt.Errorf("%s: %w", s.Name(), err)
		}
		l := ui.Line{N: i + 1, Total: len(steps), Name: s.Name(), Detail: detail, Attached: attachedStep(s)}
		if done {
			p.Done(l)
			continue
		}
		err = p.Run(l, func() (ui.Outcome, error) {
			if err := s.Apply(ctx, e); err != nil {
				return ui.Outcome{}, fmt.Errorf("%s: %w", s.Name(), err)
			}
			done, detail, err := s.Check(ctx, e)
			if err != nil {
				return ui.Outcome{}, fmt.Errorf("%s: %w", s.Name(), err)
			}
			if !done {
				return ui.Outcome{}, fmt.Errorf("%s: applied, but the check still fails: %s", s.Name(), detail)
			}
			return ui.Outcome{Detail: detail}, nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// attachedStep reports whether the step reads the terminal or draws on it, as a step that shows
// a QR code or asks for a key does: a live line under that would be redrawn into it.
func attachedStep(s Step) bool {
	a, ok := s.(interface{ Attached() bool })
	return ok && a.Attached()
}

func (e *Env) progress() *ui.Progress {
	if e.prog == nil {
		e.prog = ui.NewProgress(e.Out)
		e.prog.NameWidth = 10
	}
	return e.prog
}

func (e *Env) o() ui.Out { return ui.NewOut(e.Out) }

type tsNode struct{ HostName, DNSName string }

func (n *tsNode) named(name string) bool {
	if n == nil {
		return false
	}
	label, _, _ := strings.Cut(n.DNSName, ".")
	return strings.EqualFold(label, name) || strings.EqualFold(n.HostName, name)
}

// tailnetHas reports whether a node on the owner's tailnet already uses name, as this desktop
// sees the tailnet.
func tailnetHas(ctx context.Context, e *Env, name string) (bool, error) {
	return tailnetHasExcept(ctx, e, name, "")
}

// tailnetHasExcept is tailnetHas without the peer whose node key is except. The VM is such a peer
// while its tailscaled is down or logged out, and this desktop lists it offline under its name.
func tailnetHasExcept(ctx context.Context, e *Env, name, except string) (bool, error) {
	out, err := host.Must(ctx, e.Local, cmd("tailscale", "status", "--json"))
	if err != nil {
		return false, fmt.Errorf("reading the tailnet on this desktop: %w", err)
	}
	var st struct {
		Self *tsNode
		Peer map[string]*tsNode
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return false, fmt.Errorf("reading the tailnet on this desktop: %w", err)
	}
	if st.Self.named(name) {
		return true, nil
	}
	// Peer is keyed by node public key, the form Self.PublicKey takes in the VM's own status.
	for k, p := range st.Peer {
		if k != except && p.named(name) {
			return true, nil
		}
	}
	return false, nil
}

func tailnetTaken(name string) error {
	return fmt.Errorf("%s is already a node on your tailnet; remove it at https://login.tailscale.com/admin/machines, or pick another name", name)
}
