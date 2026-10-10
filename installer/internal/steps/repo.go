package steps

import (
	"context"
	"os"
	"path/filepath"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

const (
	repoRel      = "agents/linear-agents"
	worktreesRel = "agents/worktrees"
)

type Repo struct{}

func (Repo) Name() string { return "Service repo" }

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func (Repo) Check(ctx context.Context, e *Env) (bool, string, error) {
	want, err := bundleHead(ctx, e, filepath.Join(e.Stage, "linear-agents.bundle"))
	if err != nil {
		return false, "", err
	}
	got := e.Output(ctx, "git", "-C", e.Real(repoRel), "rev-parse", "HEAD")
	if got != want {
		return false, "checkout at " + short(got) + ", bundle at " + short(want), nil
	}
	if _, err := os.Stat(e.HomePath(".local/bin/linear-agents")); err != nil {
		return false, "linear-agents not installed", nil
	}
	return true, "at " + short(want), nil
}

func (Repo) Apply(ctx context.Context, e *Env) error {
	dir := e.Real(repoRel)
	if err := syncFromBundle(ctx, e, filepath.Join(e.Stage, "linear-agents.bundle"), dir); err != nil {
		return err
	}
	for _, c := range []host.Cmd{
		// Go is only for building la-setup on the desktop; the host does not need it.
		{Name: "./setup", Dir: dir, Env: []string{"MISE_DISABLE_TOOLS=go"}},
		{Name: e.Real(".local/bin/mise"), Args: []string{"exec", "--", "uv", "tool", "install", "--force", "--editable", "."}, Dir: dir},
	} {
		if err := e.UI.Spin(c.String(), func() error { _, err := e.MustCmd(ctx, c); return err }); err != nil {
			return err
		}
	}
	return os.MkdirAll(e.HomePath(worktreesRel), 0o755)
}
