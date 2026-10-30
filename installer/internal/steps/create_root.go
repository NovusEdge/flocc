package steps

import (
	"context"
	"strings"
)

// IncusBridge is the Incus bridge create puts VMs on; create.Bridge is it.
const IncusBridge = "incusbr0"

func CreateRootSteps() []Step { return []Step{Firewalld{}, Docker{}, TailnetBlock{}} }

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

// CreateRootIdle reports whether no create root step has work, which needs no root: the desktop
// runs it over ssh to skip the sudo. The tailnet block counts as done by its unit alone, since
// reading the nft table needs root; the root pass checks the table itself.
func CreateRootIdle(ctx context.Context, e *Env) (bool, string) {
	if _, docker := dockerPresent(ctx, e); firewalldRunning(ctx, e) || docker {
		return false, ""
	}
	if !e.Ok(ctx, "systemctl", "is-enabled", "--quiet", tailnetUnitName) ||
		!e.Ok(ctx, "systemctl", "is-active", "--quiet", tailnetUnitName) {
		return false, ""
	}
	return true, strings.Join([]string{noFirewalld, noDocker, "tailnet block installed"}, "; ")
}
