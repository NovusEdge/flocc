package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/NovusEdge/flocc/installer/internal/create"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/ship"
)

// setupOps are the existing functions flocc setup calls, as fields so its tests can replace each.
type setupOps struct {
	checkout   func() (root, version string, err error)
	fillKeys   func(ctx context.Context, slots []keys.Slot) error
	checkKeys  func(ctx context.Context) []string
	hosts      func() []hosts.Host
	saveHost   func(h hosts.Host) error
	createLogs func() []string
	machine    func(ctx context.Context, machine string) machineState
	create     func(ctx context.Context, spec create.Spec) error
	add        func(ctx context.Context, name string, reach hosts.Host) error
	ship       func(ctx context.Context, name string) error
}

// machineState is what a machine allows. Err is set when ssh could not reach it.
type machineState struct {
	Err   error
	Sudo  bool  // sudo -n works there
	Incus error // nil when incus runs as an administrator, else the fix
}

func newSetupOps(d *deps) setupOps {
	mux, _ := ship.SSHMux()
	return setupOps{
		checkout: func() (string, string, error) { return checkoutVersion(d) },
		fillKeys: func(ctx context.Context, s []keys.Slot) error {
			return keys.Fill(ctx, keysEnv(d), mapped(listHosts(d)), s)
		},
		checkKeys:  func(ctx context.Context) []string { return keys.Check(ctx, keysEnv(d), mapped(listHosts(d))) },
		hosts:      func() []hosts.Host { return listHosts(d) },
		saveHost:   func(h hosts.Host) error { return hosts.Save(hosts.Dir(d.home), h) },
		createLogs: create.Logged,
		machine:    func(ctx context.Context, m string) machineState { return probeMachine(ctx, d, mux, m) },
		create:     func(ctx context.Context, s create.Spec) error { return runCreate(ctx, d, s) },
		add:        func(ctx context.Context, name string, reach hosts.Host) error { return addHost(ctx, d, name, reach) },
		ship:       func(ctx context.Context, name string) error { return shipHost(ctx, d, name) },
	}
}

var pyVersion = regexp.MustCompile(`(?m)^version = "([^"]+)"`)

// checkoutVersion is the flocc checkout this runs in, which ship, create and cardinal build from,
// and the version its pyproject.toml declares.
func checkoutVersion(d *deps) (string, string, error) {
	root, err := d.repoRoot()
	if err != nil {
		return "", "", fmt.Errorf("flocc setup builds from the flocc checkout it runs in; cd into it: %w", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "pyproject.toml"))
	if err != nil {
		return root, "", err
	}
	m := pyVersion.FindSubmatch(b)
	if m == nil {
		return root, "", fmt.Errorf("%s/pyproject.toml declares no version", root)
	}
	return root, string(m[1]), nil
}

// owner runs commands as the owner on machine, as host create's captured commands do.
func owner(d *deps, mux []string, machine string) create.Remote {
	return create.Remote{R: d.run, Target: machine, Opts: append([]string{"-o", "BatchMode=yes"}, mux...)}
}

// probeMachine opens the machine's shared connection on this terminal, so a host-key question or
// a Tailscale approval can be answered, then reads what the machine allows.
func probeMachine(ctx context.Context, d *deps, mux []string, machine string) machineState {
	if err := ship.Connect(ctx, d.run, ship.Dest{SSH: machine, Opts: mux}, machine, d.tty); err != nil {
		return machineState{Err: err}
	}
	sudo, incus := create.Preconditions(ctx, owner(d, mux, machine), machine)
	return machineState{Sudo: sudo, Incus: incus}
}

// runCreate is flocc host create <machine> <name> after its argument checks.
func runCreate(ctx context.Context, d *deps, spec create.Spec) error {
	inst := hostInstaller{d: d}
	if err := inst.Ready(); err != nil {
		return err
	}
	if _, err := inst.HostKnown(spec.Name, "root@"+spec.Name); err != nil {
		return err
	}
	logPath, err := create.Run(ctx, spec, inst, d.out, d.tty)
	if err != nil && logPath != "" {
		return fmt.Errorf("%w\nFull log: %s", err, logPath)
	}
	return err
}
