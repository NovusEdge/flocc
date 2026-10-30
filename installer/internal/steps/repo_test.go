package steps

import (
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

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
