package steps

import (
	"context"
	"os"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

func containersEnv(t *testing.T, f *host.Fake) *Env {
	e := testEnv(t, f)
	e.Distro.Manager = host.Apt
	e.Answers.Docker = true
	return e
}

func podmanHost(serviceGroups string) *host.Fake {
	return host.NewFake().
		On("sh -c command -v podman", 0, "/usr/bin/podman").
		On("docker compose version", 0, "Docker Compose version 2.26.1").
		On("id -nG flocc", 0, serviceGroups)
}

func TestContainersAreNotDoneWithoutCompose(t *testing.T) {
	f := host.NewFake().On("sh -c command -v podman", 0, "/usr/bin/podman").On("apt-cache show docker-cli", 0, "")
	if ok, detail, _ := (Containers{}).Check(context.Background(), containersEnv(t, f)); ok || detail != "docker compose is not installed" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestContainersSkipComposeWhereTheDistroLacksDockerCLI(t *testing.T) {
	f := host.NewFake().On("sh -c command -v podman", 0, "/usr/bin/podman").On("id -nG flocc", 0, "flocc")
	e := containersEnv(t, f)
	writeSubIDs(t, e)
	if ok, detail, _ := (Containers{}).Check(context.Background(), e); !ok {
		t.Fatalf("%v %q", ok, detail)
	}
	f.OnName("apt-get", 0, "")
	if err := (Containers{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("apt-get install -y podman uidmap slirp4netns") || f.Ran("apt-get install -y podman uidmap slirp4netns docker-cli docker-compose") {
		t.Errorf("ran %v", f.Calls)
	}
}

func TestContainersAreDoneWhenNotAskedFor(t *testing.T) {
	if ok, detail, _ := (Containers{}).Check(context.Background(), testEnv(t, host.NewFake())); !ok || detail != "none asked for" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestContainersSkipHostsWithoutApt(t *testing.T) {
	e := containersEnv(t, host.NewFake())
	e.Distro.Manager = host.Pacman
	if ok, detail, _ := (Containers{}).Check(context.Background(), e); !ok || detail != "apt hosts only" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestContainersAreNotDoneWithoutPodman(t *testing.T) {
	if ok, detail, _ := (Containers{}).Check(context.Background(), containersEnv(t, host.NewFake())); ok || detail != "podman is not installed" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestContainersNeedSubordinateIDsForTheAgent(t *testing.T) {
	e := containersEnv(t, podmanHost("flocc flocc-work"))
	os.MkdirAll(e.Path("/etc"), 0o755)
	os.WriteFile(e.Path("/etc/subuid"), []byte("flocc:100000:65536\n"), 0o644)
	os.WriteFile(e.Path("/etc/subgid"), []byte("flocc:100000:65536\n"), 0o644)
	if ok, detail, _ := (Containers{}).Check(context.Background(), e); ok || detail != "flocc-agent has no /etc/subuid range" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestContainersAreNotDoneWhileTheServiceIsInTheDockerGroup(t *testing.T) {
	e := containersEnv(t, podmanHost("flocc flocc-work docker"))
	writeSubIDs(t, e)
	if ok, detail, _ := (Containers{}).Check(context.Background(), e); ok || detail != "flocc is still in the docker group" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestContainersAreDone(t *testing.T) {
	e := containersEnv(t, podmanHost("flocc flocc-work"))
	writeSubIDs(t, e)
	if ok, detail, _ := (Containers{}).Check(context.Background(), e); !ok || detail != "rootless Podman for flocc-agent" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestContainersApplyInstallsPodmanAddsIDsAndLeavesDockerdAlone(t *testing.T) {
	f := host.NewFake().OnName("apt-get", 0, "").OnName("usermod", 0, "").OnName("gpasswd", 0, "").
		On("id -nG flocc", 0, "flocc docker").On("apt-cache show docker-cli", 0, "")
	e := containersEnv(t, f)
	os.MkdirAll(e.Path("/etc"), 0o755)
	os.WriteFile(e.Path("/etc/subuid"), []byte("flocc:100000:65536\n"), 0o644)
	os.WriteFile(e.Path("/etc/subgid"), []byte("flocc:100000:65536\n"), 0o644)
	if err := (Containers{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"apt-get install -y podman uidmap slirp4netns docker-cli docker-compose",
		"usermod --add-subuids 165536-231071 --add-subgids 165536-231071 flocc-agent",
		"gpasswd -d flocc docker",
	} {
		if !f.Ran(want) {
			t.Errorf("did not run %q; ran %v", want, f.Calls)
		}
	}
	dockerUntouched(t, f.Calls)
}

func writeSubIDs(t *testing.T, e *Env) {
	t.Helper()
	os.MkdirAll(e.Path("/etc"), 0o755)
	for _, f := range []string{"/etc/subuid", "/etc/subgid"} {
		os.WriteFile(e.Path(f), []byte("flocc-agent:165536:65536\n"), 0o644)
	}
}
