package steps

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// subIDCount is the range useradd gives a new user on Debian and Ubuntu.
const subIDCount = 65536

// Containers gives flocc-agent rootless Podman for checks that start containers (testcontainers);
// the agent phase starts its podman.socket and flocc-agent-run points DOCKER_HOST at it. It is
// not Docker, the create-host step that opens the firewall for Docker on the VM's bridge.
//
// The docker group is root-equivalent, so neither user may be in it. A host that had the Docker
// step keeps its docker.io package and daemon: nothing here stops or restarts dockerd, and with
// nobody in the group it serves no one.
type Containers struct{}

func (Containers) Name() string { return "Containers" }

func (Containers) Check(ctx context.Context, e *Env) (bool, string, error) {
	switch {
	case !e.Answers.Docker:
		return true, "none asked for", nil
	case e.Distro.Manager != host.Apt:
		return true, "apt hosts only", nil
	case !e.Ok(ctx, "sh", "-c", "command -v podman"):
		return false, "podman is not installed", nil
	}
	for _, f := range []string{"/etc/subuid", "/etc/subgid"} {
		if !hasSubIDs(e.Path(f), AgentUser) {
			return false, AgentUser + " has no " + f + " range", nil
		}
	}
	if inGroup(ctx, e, ServiceUser, "docker") {
		return false, ServiceUser + " is still in the docker group", nil
	}
	return true, "rootless Podman for " + AgentUser, nil
}

func (Containers) Apply(ctx context.Context, e *Env) error {
	install := host.Cmd{Name: "apt-get", Args: []string{"install", "-y", "podman", "uidmap", "slirp4netns"}, Env: []string{"DEBIAN_FRONTEND=noninteractive"}}
	if err := e.SpinCmd(ctx, install); err != nil {
		return err
	}
	if !hasSubIDs(e.Path("/etc/subuid"), AgentUser) || !hasSubIDs(e.Path("/etc/subgid"), AgentUser) {
		// One range past every range already given out, in both files, so the two stay equal.
		start := max(nextFree(e.Path("/etc/subuid")), nextFree(e.Path("/etc/subgid")), 100000)
		ids := fmt.Sprintf("%d-%d", start, start+subIDCount-1)
		if _, err := e.Must(ctx, "usermod", "--add-subuids", ids, "--add-subgids", ids, AgentUser); err != nil {
			return err
		}
	}
	if inGroup(ctx, e, ServiceUser, "docker") {
		_, err := e.Must(ctx, "gpasswd", "-d", ServiceUser, "docker")
		return err
	}
	return nil
}

// hasSubIDs reports whether an /etc/subuid-style file gives user a non-empty range.
func hasSubIDs(path, user string) bool {
	b, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) != 3 || f[0] != user {
			continue
		}
		_, err1 := strconv.Atoi(f[1])
		count, err2 := strconv.Atoi(f[2])
		if err1 == nil && err2 == nil && count > 0 {
			return true
		}
	}
	return false
}

// nextFree is one past the end of the highest range in the file; 0 when it has none.
func nextFree(path string) int {
	b, _ := os.ReadFile(path)
	end := 0
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) != 3 {
			continue
		}
		start, err1 := strconv.Atoi(f[1])
		count, err2 := strconv.Atoi(f[2])
		if err1 == nil && err2 == nil {
			end = max(end, start+count)
		}
	}
	return end
}
