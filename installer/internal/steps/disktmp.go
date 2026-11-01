package steps

import (
	"context"
	"os"
	"path/filepath"
)

// systemd's stock tmp.mount puts /tmp in a tmpfs of half the RAM, so test files that agents
// leave there take memory from the sessions; one nimblefox drift-test run holds 2.2 GiB.
// Masking it leaves /tmp on the root disk from the next boot.
const tmpMountMask = "/etc/systemd/system/tmp.mount"

type DiskTmp struct{}

func (DiskTmp) Name() string { return "Disk /tmp" }

func (DiskTmp) Check(ctx context.Context, e *Env) (bool, string, error) {
	if e.Mode != Systemd {
		return true, "loop mode; /tmp left as it is", nil
	}
	if dest, err := os.Readlink(e.Path(tmpMountMask)); err == nil && dest == "/dev/null" {
		return true, "tmp.mount masked; /tmp is on disk after a reboot", nil
	}
	return false, "/tmp is a tmpfs", nil
}

func (DiskTmp) Apply(ctx context.Context, e *Env) error {
	if e.Mode != Systemd {
		return nil
	}
	p := e.Path(tmpMountMask)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink("/dev/null", p)
}
