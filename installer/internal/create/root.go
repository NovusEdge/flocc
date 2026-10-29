package create

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/rootpass"
	"github.com/NovusEdge/flocc/installer/internal/safe"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// Root runs the Firewalld and Docker steps (package steps) as root on the machine, in one sudo:
// their checks read firewalld and iptables, which only root can.
type Root struct{}

func (Root) Name() string { return "Root pass" }

func (Root) Check(ctx context.Context, e *Env) (bool, string, error) {
	if e.rootDone != "" {
		return true, e.rootDone, nil
	}
	probe := &sshProbe{Runner: e.M}
	idle, detail := steps.CreateRootIdle(ctx, &steps.Env{Run: probe})
	if probe.failed {
		return false, "", fmt.Errorf("cannot reach %s over ssh to check firewalld and Docker; run create again", e.Spec.Machine)
	}
	if idle {
		return true, detail, nil
	}
	return false, "firewalld or Docker runs on " + e.Spec.Machine + "; checking them needs root", nil
}

// sshProbe records a command ssh itself failed (exit 255), which CreateRootIdle would read as "not
// running" and so skip the firewall and Docker steps. systemctl answers no with 1, 3 or 4.
type sshProbe struct {
	host.Runner
	failed bool
}

func (p *sshProbe) Run(ctx context.Context, c host.Cmd) (host.Result, error) {
	r, err := p.Runner.Run(ctx, c)
	p.failed = p.failed || err != nil || r.Code == 255
	return r, err
}

func (Root) Apply(ctx context.Context, e *Env) error {
	m := e.Spec.Machine
	if !e.TTY && !e.sudo {
		return fmt.Errorf("sudo on %s asks for a password; run create in a terminal", m)
	}
	uname, err := e.must(ctx, cmd("uname", "-m"))
	if err != nil {
		return err
	}
	arch, err := ship.ArchFor(uname)
	if err != nil {
		return err
	}
	root, err := e.Checkout()
	if err != nil {
		return fmt.Errorf("host create builds flocc-setup from the flocc checkout it runs in; cd into it: %w", err)
	}
	bin, err := rootpass.Build(ctx, e.Local, e.UI, root, arch, "flocc-setup")
	if err != nil {
		return err
	}
	defer os.RemoveAll(bin)
	reports, err := rootpass.Run(ctx, e.Local, rootpass.Pass{Machine: m, Opts: ControlOpts(), Prefix: "/tmp/flocc-create",
		Files: []string{filepath.Join(bin, "flocc-setup")}, Calls: [][]string{{"--phase", "create-root"}},
		Why: "root pass: sudo on " + m + " may ask for your password"})
	for _, r := range reports {
		ui.Rows(e.UI, rootpass.Events(r))
	}
	if err != nil {
		return err
	}
	if len(reports) != 1 {
		return fmt.Errorf("the root pass on %s sent %d reports, not 1", m, len(reports))
	}
	// RunAll checks every step before it applies any, so a step can end not done with no error.
	for _, s := range reports[0].Steps {
		if !s.Done {
			return fmt.Errorf("%s on %s: %s", safe.Sanitize(s.Name), m, safe.Sanitize(s.Detail))
		}
	}
	e.rootDone = "firewalld and Docker checked as root on " + m
	return nil
}
