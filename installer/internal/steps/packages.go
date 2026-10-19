package steps

import (
	"context"
	"slices"
	"strings"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

// make and cc: repos' ./setup builds native code (nimblefox compiles pgvector).
var tools = []string{"git", "tmux", "curl", "gh", "node", "make", "cc"}

// pkgName maps a tool to its package; only missing tools are installed, since reinstalling
// curl on RHEL 9 conflicts with its default curl-minimal.
func pkgName(m host.Manager, tool string) string {
	switch {
	case tool == "node":
		return "nodejs"
	case tool == "cc":
		return "gcc"
	case tool == "gh" && m == host.Pacman:
		return "github-cli"
	}
	return tool
}

type Packages struct{}

func (Packages) Name() string { return "Packages" }

func missingTools(ctx context.Context, e *Env) []string {
	var missing []string
	for _, t := range tools {
		if !e.Ok(ctx, "sh", "-c", "command -v "+t) {
			missing = append(missing, t)
		}
	}
	return missing
}

func (Packages) Check(ctx context.Context, e *Env) (bool, string, error) {
	if m := missingTools(ctx, e); len(m) > 0 {
		return false, "missing: " + strings.Join(m, ", "), nil
	}
	return true, strings.Join(tools, ", "), nil
}

func (Packages) Apply(ctx context.Context, e *Env) error {
	missing := missingTools(ctx, e)
	if len(missing) == 0 {
		return nil
	}
	var pkgs []string
	for _, t := range missing {
		pkgs = append(pkgs, pkgName(e.Distro.Manager, t))
	}
	var cmds []host.Cmd
	switch e.Distro.Manager {
	case host.Apt:
		// gh comes from the distribution, which has it since Debian 12 and Ubuntu 22.04.
		env := []string{"DEBIAN_FRONTEND=noninteractive"}
		cmds = []host.Cmd{
			{Name: "apt-get", Args: []string{"update"}, Env: env},
			{Name: "apt-get", Args: append([]string{"install", "-y", "ca-certificates"}, pkgs...), Env: env},
		}
	case host.Pacman:
		cmds = []host.Cmd{{Name: "pacman", Args: append([]string{"-Syu", "--noconfirm", "--needed"}, pkgs...)}}
	case host.Dnf:
		if e.Distro.NeedsGHRepo() && slices.Contains(missing, "gh") {
			cmds = append(cmds,
				host.Cmd{Name: "dnf", Args: []string{"install", "-y", "dnf-plugins-core"}},
				host.Cmd{Name: "dnf", Args: []string{"config-manager", "--add-repo", "https://cli.github.com/packages/rpm/gh-cli.repo"}})
		}
		cmds = append(cmds, host.Cmd{Name: "dnf", Args: append([]string{"install", "-y"}, pkgs...)})
	}
	for _, c := range cmds {
		if err := e.UI.Spin(c.String(), func() error { _, err := e.MustCmd(ctx, c); return err }); err != nil {
			return err
		}
	}
	return nil
}
