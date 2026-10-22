package steps

import (
	"context"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// HostDocker installs Docker for checks that start containers (testcontainers) and puts the
// service user in the docker group; without the daemon those checks fail on the missing socket
// and no fix round can help. It is not Docker, the create-host step that opens the firewall for
// Docker on the VM's bridge.
//
// The docker group is root-equivalent on the VM. The owner accepts that because host VMs are
// disposable (cardinal design, "What keeps upstream safe"). The group reaches the service user's
// sessions only after its systemd user manager restarts: its next login, or
// systemctl restart user@<uid>.
type HostDocker struct{}

func (HostDocker) Name() string { return "Docker" }

func (HostDocker) Check(ctx context.Context, e *Env) (bool, string, error) {
	switch {
	case !e.Answers.Docker:
		return true, "none asked for", nil
	case e.Distro.Manager != host.Apt:
		return true, "apt hosts only", nil
	case !e.Ok(ctx, "sh", "-c", "command -v docker"):
		return false, "docker is not installed", nil
	}
	detail := "docker active, service user in docker group"
	if e.Mode == Systemd {
		if !e.Ok(ctx, "systemctl", "is-active", "docker") {
			return false, "docker service is not active", nil
		}
	} else {
		detail = "docker installed (service not checked without systemd), service user in docker group"
	}
	if !slices.Contains(strings.Fields(e.Output(ctx, "id", "-nG", ServiceUser)), "docker") {
		return false, ServiceUser + " is not in the docker group", nil
	}
	return true, detail, nil
}

func (HostDocker) Apply(ctx context.Context, e *Env) error {
	install := host.Cmd{Name: "apt-get", Args: []string{"install", "-y", "docker.io"}, Env: []string{"DEBIAN_FRONTEND=noninteractive"}}
	if err := e.UI.Spin(install.String(), func() error { _, err := e.MustCmd(ctx, install); return err }); err != nil {
		return err
	}
	if e.Mode == Systemd {
		if _, err := e.Must(ctx, "systemctl", "enable", "--now", "docker"); err != nil {
			return err
		}
	}
	_, err := e.Must(ctx, "usermod", "-aG", "docker", ServiceUser)
	return err
}
