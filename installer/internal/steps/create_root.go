package steps

import (
	"context"
	"strings"
)

// IncusBridge is the Incus bridge create puts VMs on; create.Bridge is it.
const IncusBridge = "incusbr0"

func CreateRootSteps() []Step { return []Step{Firewalld{}, Docker{}} }

func firewalldRunning(ctx context.Context, e *Env) bool {
	return e.Ok(ctx, "systemctl", "is-active", "--quiet", "firewalld.service")
}

// dockerPresent: a stopped or socket-activated Docker still sets FORWARD to DROP at its next start.
func dockerPresent(ctx context.Context, e *Env) (running, present bool) {
	running = e.Ok(ctx, "systemctl", "is-active", "--quiet", "docker.service")
	return running, running || e.Ok(ctx, "systemctl", "is-enabled", "--quiet", "docker.service") ||
		e.Ok(ctx, "systemctl", "is-active", "--quiet", "docker.socket")
}

const (
	noFirewalld = "firewalld not running; nothing keeps the VM off this machine's services"
	noDocker    = "Docker not running"
)

// CreateRootIdle reports whether neither create root step has work, which needs no root: the
// desktop runs it over ssh to skip the sudo.
func CreateRootIdle(ctx context.Context, e *Env) (bool, string) {
	if _, docker := dockerPresent(ctx, e); firewalldRunning(ctx, e) || docker {
		return false, ""
	}
	return true, strings.Join([]string{noFirewalld, noDocker}, "; ")
}
