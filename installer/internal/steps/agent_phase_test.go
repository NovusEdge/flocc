package steps

import (
	"context"
	"os"
	"testing"

	"github.com/NovusEdge/flocc/deploy"
	"github.com/NovusEdge/flocc/installer/internal/host"
)

func TestMiseIsInstalledWhenMissing(t *testing.T) {
	f := host.NewFake().OnName("sh", 0, "")
	e := testEnv(t, f)
	if done, _, _ := (Mise{}).Check(context.Background(), e); done {
		t.Fatal("done without mise")
	}
	if err := (Mise{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("sh -c curl -fsSL https://mise.run | sh") {
		t.Fatal(f.Calls)
	}
}

func TestAgentTmuxInstallsAndStartsTheUnit(t *testing.T) {
	f := host.NewFake().OnName("systemctl", 0, "").OnName("tmux", 0, "")
	e := testEnv(t, f)
	if err := (AgentTmux{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	want, _ := deploy.Files.ReadFile("flocc-tmux.service")
	got, _ := os.ReadFile(e.HomePath(".config/systemd/user/flocc-tmux.service"))
	if string(got) != string(want) {
		t.Fatal("unit not written")
	}
	if !f.Ran("systemctl --user enable --now flocc-tmux.service") {
		t.Fatal(f.Calls)
	}
}

func TestAgentTmuxKeepsAServerUpInLoopMode(t *testing.T) {
	f := host.NewFake().OnName("sh", 0, "")
	e := testEnv(t, f)
	e.Mode = Loop
	if err := (AgentTmux{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("sh -c umask 027 && exec tmux -L flocc new-session -d -s flocc-keep 'sleep infinity'") {
		t.Fatal(f.Calls)
	}
}

func TestPodmanSocketIsStartedForFloccAgent(t *testing.T) {
	f := host.NewFake().OnName("systemctl", 0, "")
	e := testEnv(t, f)
	e.Answers.Docker = true
	e.Distro.Manager = host.Apt
	if err := (PodmanSocket{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("systemctl --user enable --now podman.socket") {
		t.Fatal(f.Calls)
	}
}

func TestPodmanSocketIsSkippedWithoutContainersOrSystemd(t *testing.T) {
	e := testEnv(t, host.NewFake())
	if ok, detail, _ := (PodmanSocket{}).Check(context.Background(), e); !ok || detail != "none asked for" {
		t.Fatalf("%v %q", ok, detail)
	}
	e.Answers.Docker, e.Distro.Manager, e.Mode = true, host.Apt, Loop
	if ok, detail, _ := (PodmanSocket{}).Check(context.Background(), e); !ok || detail != "needs systemd; checks get no container socket" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestAgentSessionsSkipTheBypassWarning(t *testing.T) {
	if !pushSettings["skipDangerousModePermissionPrompt"] {
		t.Fatal("the bypass warning would stop the first session")
	}
}
