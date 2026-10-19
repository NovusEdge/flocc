package steps

import (
	"context"
	"os"
	"path/filepath"
)

const (
	tmpfilesConf = "/etc/tmpfiles.d/claude-scratch.conf"
	// Claude Code scratchpads untouched for two days; the service removes a run's own sooner.
	tmpfilesLine = "e /tmp/claude-* - - - 2d\n"
)

type Scratch struct{}

func (Scratch) Name() string { return "Scratch cleanup" }

func (Scratch) Check(ctx context.Context, e *Env) (bool, string, error) {
	if e.Mode != Systemd {
		return true, "loop mode; cleaned by the service", nil
	}
	b, err := os.ReadFile(e.Path(tmpfilesConf))
	if err == nil && string(b) == tmpfilesLine {
		return true, tmpfilesConf, nil
	}
	return false, "no tmpfiles rule", nil
}

func (Scratch) Apply(ctx context.Context, e *Env) error {
	if e.Mode != Systemd {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(e.Path(tmpfilesConf)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(e.Path(tmpfilesConf), []byte(tmpfilesLine), 0o644)
}
