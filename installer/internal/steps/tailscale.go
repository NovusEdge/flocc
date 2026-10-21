package steps

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/tailnet"
)

type Tailscale struct{}

func (Tailscale) Name() string { return "Tailscale" }

// tsState is "" when tailscaled does not answer.
func tsState(ctx context.Context, e *Env) string {
	var s struct{ BackendState string }
	if json.Unmarshal([]byte(e.Output(ctx, "tailscale", "status", "--json")), &s) != nil {
		return ""
	}
	return s.BackendState
}

func tsSSH(ctx context.Context, e *Env) bool {
	var p struct{ RunSSH bool }
	return json.Unmarshal([]byte(e.Output(ctx, "tailscale", "debug", "prefs")), &p) == nil && p.RunSSH
}

func (Tailscale) Check(ctx context.Context, e *Env) (bool, string, error) {
	state := tsState(ctx, e)
	switch {
	case state == "":
		return false, "tailscaled not running", nil
	case e.SkipInteractive:
		return true, "tailscaled running (login skipped)", nil
	case state != "Running":
		return false, "state " + state, nil
	case !tsSSH(ctx, e):
		return false, "Tailscale SSH off", nil
	}
	return true, "logged in, SSH on", nil
}

func (Tailscale) Apply(ctx context.Context, e *Env) error {
	if !e.Ok(ctx, "sh", "-c", "command -v tailscale") {
		install := []string{"sh", "-c", "curl -fsSL https://tailscale.com/install.sh | sh"}
		if e.Distro.Manager == host.Pacman {
			install = []string{"pacman", "-S", "--noconfirm", "--needed", "tailscale"}
		}
		if err := e.UI.Spin("Installing Tailscale", func() error { _, err := e.Must(ctx, install[0], install[1:]...); return err }); err != nil {
			return err
		}
	}
	if tsState(ctx, e) == "" {
		var err error
		if e.Mode == Systemd {
			_, err = e.Must(ctx, "systemctl", "enable", "--now", "tailscaled")
		} else {
			// No init to supervise it; a sandbox stop ends it and the next ship starts it again.
			_, err = e.Must(ctx, "sh", "-c", "mkdir -p /var/lib/tailscale && nohup tailscaled --state=/var/lib/tailscale/tailscaled.state >/var/log/tailscaled.log 2>&1 &")
		}
		if err != nil {
			return err
		}
		for range 20 {
			if tsState(ctx, e) != "" {
				break
			}
			e.Sleep(500 * time.Millisecond)
		}
		if tsState(ctx, e) == "" {
			return errors.New("tailscaled did not start; see /var/log/tailscaled.log or journalctl -u tailscaled")
		}
	}
	if e.SkipInteractive {
		return nil
	}
	if tsState(ctx, e) != "Running" {
		if key := scriptedTailscaleKey(e); key != "" {
			return joinWithKey(ctx, e, key)
		}
		def := e.Answers.Hostname
		if def == "" {
			def = "flocc"
		}
		name, err := e.UI.Text("Tailscale hostname for this host", def)
		if err != nil {
			return err
		}
		e.Answers.Hostname = name
		// --ssh here, not a later set: once RunSSH is on, up refuses to run without it.
		if err := e.Run.Interactive(ctx, cmd("tailscale", "up", "--hostname="+name, "--ssh")); err != nil {
			return err
		}
		e.UI.Note("Tailscale key expiry", "Open https://login.tailscale.com/admin/machines, find "+name+
			" and choose Disable key expiry. An untagged node's key otherwise expires after 180 days.")
		return nil
	}
	_, err := e.Must(ctx, "tailscale", "set", "--ssh")
	return err
}

func scriptedTailscaleKey(e *Env) string {
	if e.Answers == nil || e.Answers.Secrets == nil {
		return ""
	}
	return e.Answers.Secrets.TailscaleAuthKey
}

// joinWithKey joins tagged with the desktop's minted key, which has no login and no key expiry to
// follow up. The key goes on stdin so it never shows in a process list.
func joinWithKey(ctx context.Context, e *Env, key string) error {
	name := e.Answers.Hostname
	if name == "" {
		name = "flocc"
	}
	c := cmd("tailscale", "up", "--ssh", "--hostname="+name, "--advertise-tags="+tailnet.Tag, "--auth-key=file:/dev/stdin")
	c.Stdin = strings.NewReader(key)
	if _, err := e.MustCmd(ctx, c); err != nil {
		return errors.New(strings.ReplaceAll(err.Error(), key, "<auth key>"))
	}
	return nil
}
