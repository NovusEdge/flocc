package cli

import (
	"context"
	"os"
)

// hostInstaller gives host create the desktop verbs it runs once the VM is on the tailnet.
type hostInstaller struct{ d *deps }

func (h hostInstaller) HostKnown(name string) bool {
	_, err := os.Stat(hostFile(h.d, name))
	return err == nil
}

func (h hostInstaller) AddHost(ctx context.Context, name, ssh string) error {
	return hostAdd(ctx, h.d, name, ssh)
}

func (h hostInstaller) Ship(ctx context.Context, name string) error { return shipHost(ctx, h.d, name) }

func (h hostInstaller) Login(ctx context.Context, name string) error {
	return loginHost(ctx, h.d, name)
}
