package steps

import (
	"context"
	"os"
	"time"

	"github.com/NovusEdge/flocc/deploy"
	"github.com/NovusEdge/flocc/installer/internal/host"
)

// Mise installs mise for flocc-agent; Tools then sets the host file's tools on it, and the repos'
// ./setup and checks find them through its shims.
type Mise struct{}

func (Mise) Name() string { return "mise" }

func (Mise) Check(ctx context.Context, e *Env) (bool, string, error) {
	if _, err := os.Stat(e.HomePath(miseRel)); err != nil {
		return false, "not installed", nil
	}
	return true, e.Real(miseRel), nil
}

func (Mise) Apply(ctx context.Context, e *Env) error {
	return e.UI.Spin("Installing mise", func() error {
		_, err := e.Must(ctx, "sh", "-c", "curl -fsSL https://mise.run | sh")
		return err
	})
}

// AgentTmux runs the tmux server the sessions live in, as flocc-agent. Ticks reach it through
// flocc-agent-run with tmux -N, which never starts a server.
type AgentTmux struct{}

func (AgentTmux) Name() string { return "Session server" }

func (AgentTmux) Check(ctx context.Context, e *Env) (bool, string, error) {
	if e.Mode == Loop {
		if e.Ok(ctx, "tmux", "-N", "-L", "flocc", "list-sessions") {
			return true, "tmux server up", nil
		}
		return false, "no tmux server", nil
	}
	if u := staleUnit(e, deploy.AgentUnits); u != "" {
		return false, u + " not installed or out of date", nil
	}
	if r, err := e.Run.Run(ctx, e.userctl("is-active", "--quiet", "flocc-tmux.service")); err != nil || r.Code != 0 {
		return false, "flocc-tmux.service not running", nil
	}
	return true, "flocc-tmux.service running", nil
}

func (AgentTmux) Apply(ctx context.Context, e *Env) error {
	if e.Mode == Loop {
		// The panes inherit the server's umask, as they would the unit's UMask.
		_, err := e.Must(ctx, "sh", "-c", "umask 027 && exec tmux -L flocc new-session -d -s flocc-keep 'sleep infinity'")
		return err
	}
	if _, err := writeUnits(e, deploy.AgentUnits); err != nil {
		return err
	}
	for _, c := range [][]string{{"daemon-reload"}, {"enable", "--now", "flocc-tmux.service"}} {
		if _, err := e.MustCmd(ctx, e.userctl(c...)); err != nil {
			return err
		}
	}
	// enable --now returns before the server exists; the service phase's doctor asks for it.
	for i := 0; i < 10 && !e.Ok(ctx, "tmux", "-N", "-L", "flocc", "list-sessions"); i++ {
		e.Sleep(500 * time.Millisecond)
	}
	return nil
}

// PodmanSocket starts flocc-agent's podman.socket, the Docker-compatible API that
// flocc-agent-run points DOCKER_HOST at. The unit ships with the podman package the root phase
// installed; socket activation starts the API on the first connection.
type PodmanSocket struct{}

func (PodmanSocket) Name() string { return "Container socket" }

func (PodmanSocket) Check(ctx context.Context, e *Env) (bool, string, error) {
	switch {
	case !e.Answers.Docker || e.Distro.Manager != host.Apt:
		return true, "none asked for", nil
	case e.Mode != Systemd:
		return true, "needs systemd; checks get no container socket", nil
	}
	if r, err := e.Run.Run(ctx, e.userctl("is-active", "--quiet", "podman.socket")); err != nil || r.Code != 0 {
		return false, "podman.socket not running", nil
	}
	return true, "podman.socket running", nil
}

func (PodmanSocket) Apply(ctx context.Context, e *Env) error {
	_, err := e.MustCmd(ctx, e.userctl("enable", "--now", "podman.socket"))
	return err
}
