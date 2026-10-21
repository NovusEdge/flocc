package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/NovusEdge/flocc/installer/internal/create"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
)

// hostCreate is flocc host create after "host create". The steps of the run, and the failure of
// any of them, are rows of the session.
func hostCreate(ctx context.Context, d *deps, args []string) int {
	spec, err := create.ParseArgs(args)
	if err != nil {
		fmt.Fprintln(d.err, "flocc host create:", err)
		return 2
	}
	inst := hostInstaller{d: d}
	if err := create.Check(spec, inst); err != nil {
		d.ui.Step("Check").Fail(err)
		return 1
	}
	if err := create.Run(ctx, spec, inst, d.ui, d.tty); err != nil {
		d.ui.Todo("Run the same command again to continue.")
		return 1
	}
	return 0
}

// hostInstaller gives host create the desktop verbs it runs once the VM is on the tailnet.
type hostInstaller struct{ d *deps }

func (h hostInstaller) Ready() error { return needCheckout(h.d) }

func (h hostInstaller) HostKnown(name, ssh string) (bool, error) {
	got, err := hosts.Load(hosts.Dir(h.d.home), name)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	case got.Target() != ssh:
		return false, fmt.Errorf("%s reaches %s, not %s; remove that file or pick another name", hostFile(h.d, name), got.Target(), ssh)
	}
	return true, nil
}

func (h hostInstaller) AddHost(ctx context.Context, name, ssh, machine string) error {
	return hostAdd(ctx, h.d, name, ssh, machine)
}

func (h hostInstaller) Ship(ctx context.Context, name string) error { return shipHost(ctx, h.d, name) }

func (h hostInstaller) Login(ctx context.Context, name string) error {
	return loginHost(ctx, h.d, name)
}
