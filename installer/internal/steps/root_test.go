package steps

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/linear-agents/installer/internal/answers"
	"github.com/NovusEdge/linear-agents/installer/internal/host"
	"github.com/NovusEdge/linear-agents/installer/internal/ui"
)

func testEnv(t *testing.T, f *host.Fake) *Env {
	t.Helper()
	return &Env{Run: f, UI: &ui.Scripted{A: &answers.Answers{}, Out: io.Discard}, Root: t.TempDir(),
		Home: "/home/linear-agents", Answers: &answers.Answers{}, Mode: Systemd, Sleep: func(time.Duration) {}}
}

func scriptedSecrets(e *Env, s *answers.Secrets) {
	e.Answers.Secrets = s
	e.UI = &ui.Scripted{A: e.Answers, Out: io.Discard}
}

func TestPackagesCheckNamesMissingTools(t *testing.T) {
	f := host.NewFake()
	for _, tool := range []string{"git", "tmux", "curl", "node", "make", "cc"} {
		f.On("sh -c command -v "+tool, 0, "/usr/bin/"+tool)
	}
	done, detail, _ := Packages{}.Check(context.Background(), testEnv(t, f))
	if done || detail != "missing: gh" {
		t.Fatalf("done=%v detail=%q", done, detail)
	}
}

func TestPackagesApplyInstallsOnlyMissingTools(t *testing.T) {
	f := host.NewFake().On("pacman -Syu --noconfirm --needed github-cli nodejs", 0, "")
	for _, tool := range []string{"git", "tmux", "curl", "make", "cc"} {
		f.On("sh -c command -v "+tool, 0, "/usr/bin/"+tool)
	}
	e := testEnv(t, f)
	e.Distro = host.Distro{ID: "arch", Manager: host.Pacman}
	if err := (Packages{}).Apply(context.Background(), e); err != nil {
		t.Fatalf("%v; calls %v", err, f.Calls)
	}
}

func TestPackagesApplyAddsGHRepoOnRHELOnly(t *testing.T) {
	for _, c := range []struct {
		distro host.Distro
		repo   bool
	}{{host.Distro{ID: "rocky", Manager: host.Dnf}, true}, {host.Distro{ID: "fedora", Manager: host.Dnf}, false}} {
		f := host.NewFake().
			On("dnf install -y dnf-plugins-core", 0, "").
			On("dnf config-manager --add-repo https://cli.github.com/packages/rpm/gh-cli.repo", 0, "").
			On("dnf install -y git tmux curl gh nodejs", 0, "").
			On("sh -c command -v make", 0, "/usr/bin/make").
			On("sh -c command -v cc", 0, "/usr/bin/cc")
		e := testEnv(t, f)
		e.Distro = c.distro
		if err := (Packages{}).Apply(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		if f.Ran("dnf config-manager --add-repo https://cli.github.com/packages/rpm/gh-cli.repo") != c.repo {
			t.Fatalf("%s: repo added = %v", c.distro.ID, !c.repo)
		}
	}
}

func TestPackagesInstallsBuildToolsWhenMissing(t *testing.T) {
	f := host.NewFake().On("pacman -Syu --noconfirm --needed make gcc", 0, "")
	for _, tool := range []string{"git", "tmux", "curl", "gh", "node"} {
		f.On("sh -c command -v "+tool, 0, "/usr/bin/"+tool)
	}
	e := testEnv(t, f)
	e.Distro = host.Distro{ID: "arch", Manager: host.Pacman}
	if err := (Packages{}).Apply(context.Background(), e); err != nil {
		t.Fatalf("%v; calls %v", err, f.Calls)
	}
	if !f.Ran("pacman -Syu --noconfirm --needed make gcc") {
		t.Fatalf("build tools not installed; calls %v", f.Calls)
	}
}

func TestTailscaleCheck(t *testing.T) {
	f := host.NewFake().
		On("tailscale status --json", 0, `{"BackendState":"Running"}`).
		On("tailscale debug prefs", 0, `{"RunSSH":true}`)
	if done, _, _ := (Tailscale{}).Check(context.Background(), testEnv(t, f)); !done {
		t.Fatal("running with SSH should be done")
	}
	f.On("tailscale debug prefs", 0, `{"RunSSH":false}`)
	if done, detail, _ := (Tailscale{}).Check(context.Background(), testEnv(t, f)); done || detail != "Tailscale SSH off" {
		t.Fatalf("detail=%q", detail)
	}
}

func TestTailscaleApplyInLoopModeStartsDaemonAndLogsIn(t *testing.T) {
	daemon := "sh -c mkdir -p /var/lib/tailscale && nohup tailscaled --state=/var/lib/tailscale/tailscaled.state >/var/log/tailscaled.log 2>&1 &"
	f := host.NewFake().
		On("sh -c command -v tailscale", 0, "/usr/bin/tailscale").
		On(daemon, 0, "").
		On("tailscale set --ssh", 0, "").
		OnSeq("tailscale status --json", host.Result{Code: 1}, host.Result{Stdout: `{"BackendState":"NeedsLogin"}`})
	e := testEnv(t, f)
	e.Mode = Loop
	e.Answers.Hostname = "la-smoke"
	if err := (Tailscale{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran(daemon) || !f.Ran("interactive: tailscale up --hostname=la-smoke --ssh") || f.Ran("tailscale set --ssh") {
		t.Fatalf("calls: %v", f.Calls)
	}
	if f.Ran("systemctl enable --now tailscaled") {
		t.Fatal("loop mode must not call systemctl")
	}
}

func TestUserCheckRefusesPrivilegedGroups(t *testing.T) {
	f := host.NewFake().
		On("id -u linear-agents", 0, "1001").
		On("id -nG linear-agents", 0, "linear-agents wheel").
		On("chmod 700 /home/linear-agents", 0, "").
		On("loginctl show-user linear-agents -p Linger --value", 0, "yes")
	e := testEnv(t, f)
	os.MkdirAll(e.Path("/home/linear-agents"), 0o700)
	os.Chmod(e.Path("/home/linear-agents"), 0o700)
	done, detail, _ := (User{}).Check(context.Background(), e)
	if done || !strings.Contains(detail, "wheel") {
		t.Fatalf("done=%v detail=%q", done, detail)
	}
	if err := (User{}).Apply(context.Background(), e); err == nil || !strings.Contains(err.Error(), "gpasswd -d linear-agents wheel") {
		t.Fatalf("apply err=%v", err)
	}
}

func TestUserApplyCreatesUserAndSkipsLingerInLoopMode(t *testing.T) {
	f := host.NewFake().
		On("useradd --create-home --shell /bin/bash linear-agents", 0, "").
		On("chmod 700 /home/linear-agents", 0, "").
		On("id -nG linear-agents", 0, "linear-agents")
	e := testEnv(t, f)
	e.Mode = Loop
	if err := (User{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("useradd --create-home --shell /bin/bash linear-agents") || f.Ran("loginctl enable-linger linear-agents") {
		t.Fatalf("calls: %v", f.Calls)
	}
}
