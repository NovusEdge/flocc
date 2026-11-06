package steps

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const (
	repoRel      = "flocc"
	worktreesRel = "worktrees"
)

type Repo struct{}

func (Repo) Name() string { return "Service repo" }

// Short is the first seven characters of a commit, the form every screen shows.
func Short(sha string) string {
	return sha[:min(7, len(sha))]
}

func (Repo) Check(ctx context.Context, e *Env) (bool, string, error) {
	want, err := bundleHead(ctx, e, filepath.Join(e.Stage, "flocc.bundle"))
	if err != nil {
		return false, "", err
	}
	got := e.Output(ctx, "git", "-C", e.Real(repoRel), "rev-parse", "HEAD")
	if got != want {
		return false, "checkout at " + Short(got) + ", bundle at " + Short(want), nil
	}
	if _, err := os.Stat(e.HomePath(".local/bin/flocc")); err != nil {
		return false, "flocc not installed", nil
	}
	if b, _ := os.ReadFile(e.Path(OptDir + "/.flocc-commit")); strings.TrimSpace(string(b)) != want {
		return false, OptDir + " not published at " + Short(want), nil
	}
	if _, err := os.Stat(e.Path(OptDir + "/bin/flocc-agent")); err != nil {
		return false, "flocc-agent not published", nil
	}
	return true, "at " + Short(want), nil
}

// publishCmds put the flocc build, the plugin and the plugin allowlist in /opt/flocc, where
// flocc-agent reads them and cannot change them. Not editable, and with its own Python: flocc's
// home is mode 700.
func publishCmds(e *Env, dir string) []host.Cmd {
	env := []string{
		"UV_TOOL_DIR=" + OptDir + "/tools",
		"UV_TOOL_BIN_DIR=" + OptDir + "/bin",
		"UV_PYTHON_INSTALL_DIR=" + OptDir + "/python",
		"UV_LINK_MODE=copy",
	}
	copyScript := "rm -rf " + OptDir + "/plugin " + OptDir + "/deploy && cp -r plugin " + OptDir + "/plugin" +
		" && mkdir -p " + OptDir + "/deploy && cp deploy/agent-plugins.txt " + OptDir + "/deploy/" +
		" && git rev-parse HEAD > " + OptDir + "/.flocc-commit"
	return []host.Cmd{
		{Name: e.Real(".local/bin/mise"), Args: []string{"exec", "--", "uv", "tool", "install", "--force", "--reinstall", "--managed-python", "--python", "3.13", "."}, Dir: dir, Env: env},
		{Name: "sh", Args: []string{"-c", copyScript}, Dir: dir},
	}
}

func (Repo) Apply(ctx context.Context, e *Env) error {
	dir := e.Real(repoRel)
	if err := syncFromBundle(ctx, e, filepath.Join(e.Stage, "flocc.bundle"), dir); err != nil {
		return err
	}
	cmds := []host.Cmd{
		// Go is only for building flocc-setup on the desktop; the host does not need it.
		{Name: "./setup", Dir: dir, Env: []string{"MISE_DISABLE_TOOLS=go"}},
		{Name: e.Real(".local/bin/mise"), Args: []string{"exec", "--", "uv", "tool", "install", "--force", "--editable", "."}, Dir: dir},
	}
	for _, c := range append(cmds, publishCmds(e, dir)...) {
		if err := e.SpinCmd(ctx, c); err != nil {
			return err
		}
	}
	// 0.12 kept a permissions file beside the config; sessions run in bypass mode now.
	if err := os.Remove(e.HomePath(".config/flocc/permissions.toml")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
