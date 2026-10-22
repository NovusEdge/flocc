package steps

import (
	"context"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

func dockerEnv(t *testing.T, f *host.Fake) *Env {
	e := testEnv(t, f)
	e.Distro.Manager = host.Apt
	e.Answers.Docker = true
	return e
}

func dockerHost(groups string) *host.Fake {
	return host.NewFake().
		On("sh -c command -v docker", 0, "/usr/bin/docker").
		On("systemctl is-active docker", 0, "active").
		On("id -nG flocc", 0, groups)
}

func TestDockerIsDoneWhenNotAskedFor(t *testing.T) {
	e := testEnv(t, host.NewFake())
	if ok, detail, err := (HostDocker{}).Check(context.Background(), e); !ok || err != nil || detail != "none asked for" {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
}

func TestDockerSkipsHostsWithoutApt(t *testing.T) {
	e := dockerEnv(t, host.NewFake())
	e.Distro.Manager = host.Pacman
	if ok, detail, _ := (HostDocker{}).Check(context.Background(), e); !ok || detail != "apt hosts only" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestDockerIsNotDoneWithoutTheBinary(t *testing.T) {
	ok, detail, err := (HostDocker{}).Check(context.Background(), dockerEnv(t, host.NewFake()))
	if ok || err != nil || detail != "docker is not installed" {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
}

func TestDockerIsNotDoneWhileTheServiceIsInactive(t *testing.T) {
	f := dockerHost("flocc docker").On("systemctl is-active docker", 3, "inactive")
	if ok, detail, _ := (HostDocker{}).Check(context.Background(), dockerEnv(t, f)); ok || detail != "docker service is not active" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestDockerIsNotDoneWhenTheServiceUserIsNotInTheGroup(t *testing.T) {
	ok, detail, _ := (HostDocker{}).Check(context.Background(), dockerEnv(t, dockerHost("flocc")))
	if ok || detail != "flocc is not in the docker group" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestDockerIsDoneWhenInstalledActiveAndGrouped(t *testing.T) {
	ok, detail, _ := (HostDocker{}).Check(context.Background(), dockerEnv(t, dockerHost("flocc docker")))
	if !ok || detail != "docker active, service user in docker group" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestDockerInLoopModeSkipsTheServiceCheck(t *testing.T) {
	f := host.NewFake().On("sh -c command -v docker", 0, "/usr/bin/docker").On("id -nG flocc", 0, "flocc docker")
	e := dockerEnv(t, f)
	e.Mode = Loop
	ok, detail, _ := (HostDocker{}).Check(context.Background(), e)
	if !ok || !strings.Contains(detail, "without systemd") || f.Ran("systemctl is-active docker") {
		t.Fatalf("%v %q calls %q", ok, detail, f.Calls)
	}
}

func TestDockerApplyInstallsEnablesAndAddsTheUser(t *testing.T) {
	f := host.NewFake().
		On("apt-get install -y docker.io", 0, "").
		On("systemctl enable --now docker", 0, "").
		On("usermod -aG docker flocc", 0, "")
	if err := (HostDocker{}).Apply(context.Background(), dockerEnv(t, f)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"apt-get install -y docker.io", "systemctl enable --now docker", "usermod -aG docker flocc"} {
		if !f.Ran(c) {
			t.Fatalf("missing %q in %q", c, f.Calls)
		}
	}
}

func TestDockerApplyInLoopModeDoesNotTouchSystemd(t *testing.T) {
	f := host.NewFake().On("apt-get install -y docker.io", 0, "").On("usermod -aG docker flocc", 0, "")
	e := dockerEnv(t, f)
	e.Mode = Loop
	if err := (HostDocker{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if f.Ran("systemctl enable --now docker") {
		t.Fatalf("calls %q", f.Calls)
	}
}
