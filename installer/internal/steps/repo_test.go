package steps

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// cmdRecorder keeps each command whole; Fake's Calls hold only the text, without the environment.
type cmdRecorder struct {
	*host.Fake
	cmds []host.Cmd
}

func (r *cmdRecorder) Run(ctx context.Context, c host.Cmd) (host.Result, error) {
	r.cmds = append(r.cmds, c)
	return r.Fake.Run(ctx, c)
}

func TestRepoApplyPublishesTheBuildAndRemovesTheOldPermissionsFile(t *testing.T) {
	f := &cmdRecorder{Fake: host.NewFake().OnName("git", 0, "").OnName("./setup", 0, "").OnName("/home/flocc/.local/bin/mise", 0, "").OnName("sh", 0, "")}
	e := testEnv(t, f.Fake)
	e.Run = f
	e.Stage = "/stage"
	os.MkdirAll(e.HomePath(repoRel+"/.git"), 0o755)
	perms := e.HomePath(".config/flocc/permissions.toml")
	os.MkdirAll(filepath.Dir(perms), 0o700)
	os.WriteFile(perms, []byte("x"), 0o600)
	if err := (Repo{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(f.cmds, func(c host.Cmd) bool {
		return strings.Contains(c.String(), "uv tool install --force --reinstall") && slices.Contains(c.Env, "UV_TOOL_DIR="+OptDir+"/tools")
	}) {
		t.Errorf("no private install in %v", f.Calls)
	}
	if !slices.ContainsFunc(f.Calls, func(c string) bool { return strings.Contains(c, "cp -r plugin "+OptDir+"/plugin") }) {
		t.Errorf("plugin not copied in %v", f.Calls)
	}
	if _, err := os.Stat(perms); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("permissions.toml still there: %v", err)
	}
}

func TestPublishCmdsInstallAPrivateBuildAndCopyThePlugin(t *testing.T) {
	cmds := publishCmds(testEnv(t, host.NewFake()), "/home/flocc/flocc")
	install := cmds[0]
	if install.Name != "/home/flocc/.local/bin/mise" || strings.Contains(install.String(), "--editable") ||
		!strings.Contains(install.String(), "uv tool install --force --reinstall --managed-python --python 3.13 .") {
		t.Fatalf("%s", install.String())
	}
	for _, want := range []string{"UV_TOOL_DIR=/opt/flocc/tools", "UV_TOOL_BIN_DIR=/opt/flocc/bin", "UV_PYTHON_INSTALL_DIR=/opt/flocc/python", "UV_LINK_MODE=copy"} {
		found := false
		for _, v := range install.Env {
			found = found || v == want
		}
		if !found {
			t.Errorf("env lacks %s: %v", want, install.Env)
		}
	}
	script := cmds[1].Args[1]
	for _, want := range []string{"cp -r plugin /opt/flocc/plugin", "deploy/agent-plugins.txt", "/opt/flocc/.flocc-commit"} {
		if !strings.Contains(script, want) {
			t.Errorf("copy script lacks %s: %s", want, script)
		}
	}
}
