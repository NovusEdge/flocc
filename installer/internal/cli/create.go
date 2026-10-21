package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/NovusEdge/flocc/installer/internal/hosts"
)

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

func (h hostInstaller) AddHost(ctx context.Context, name, ssh string) error {
	return hostAdd(ctx, h.d, name, ssh, "")
}

func (h hostInstaller) Ship(ctx context.Context, name string) error { return shipHost(ctx, h.d, name) }

func (h hostInstaller) Login(ctx context.Context, name string) error {
	return loginHost(ctx, h.d, name)
}
