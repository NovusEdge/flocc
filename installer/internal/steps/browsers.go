package steps

import (
	"context"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// Browsers installs the system libraries and fonts Playwright's chromium needs. The service user
// cannot install them, and without them every browser launch in an e2e check fails within
// milliseconds (NF-240, 2026-10-20 and 10-21).
type Browsers struct{}

func (Browsers) Name() string { return "Browsers" }

func (Browsers) npx(e *Env, dry bool) host.Cmd {
	args := []string{"-y", "playwright@" + e.Answers.Playwright, "install-deps"}
	if dry {
		args = append(args, "--dry-run")
	}
	return host.Cmd{Name: "npx", Args: append(args, "chromium"), Env: []string{"DEBIAN_FRONTEND=noninteractive"}}
}

// missing counts the packages under the dry run's "Missing system dependencies (N):" line.
func missing(out string) int {
	n, listing := 0, false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "Missing system dependencies"):
			listing = true
		case listing && strings.HasPrefix(line, "  ") && strings.TrimSpace(line) != "":
			n++
		}
	}
	return n
}

func (b Browsers) Check(ctx context.Context, e *Env) (bool, string, error) {
	switch {
	case e.Answers.Playwright == "":
		return true, "none asked for", nil
	case e.Distro.Manager != host.Apt:
		return true, "apt hosts only", nil
	}
	r, err := e.Run.Run(ctx, b.npx(e, true))
	if err != nil || r.Code != 0 {
		return false, "playwright " + e.Answers.Playwright + " dry run did not run", nil
	}
	if n := missing(r.Stdout); n > 0 {
		return false, Plural(n, "missing package"), nil
	}
	return true, "chromium deps for playwright " + e.Answers.Playwright, nil
}

func (b Browsers) Apply(ctx context.Context, e *Env) error {
	c := b.npx(e, false)
	return e.UI.Spin(c.String(), func() error { _, err := e.MustCmd(ctx, c); return err })
}
