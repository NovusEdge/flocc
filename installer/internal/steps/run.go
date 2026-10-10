package steps

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/NovusEdge/linear-agents/deploy"
	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

type Run struct{}

func (Run) Name() string { return "Run" }

func (e *Env) userctl(args ...string) host.Cmd {
	// sudo -iu gives no XDG_RUNTIME_DIR, and systemctl --user needs it to find the manager.
	return host.Cmd{Name: "systemctl", Args: append([]string{"--user"}, args...),
		Env: []string{"XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(e.UID)}}
}

// Sessions and checks inherit the loop's PATH, as they inherit the unit's.
func loopCommand(home string) string { return "env " + servicePath(home) + " linear-agents run" }

func (e *Env) loopPane(ctx context.Context) string {
	return e.Output(ctx, "tmux", "-N", "-L", "linear-agents", "list-panes", "-t", "=la-loop", "-F", "#{pane_start_command}")
}

func (Run) Check(ctx context.Context, e *Env) (bool, string, error) {
	if e.Mode == Loop {
		if strings.Contains(e.loopPane(ctx), "linear-agents run") {
			return true, "la-loop ticking", nil
		}
		return false, "loop not running", nil
	}
	for _, u := range deploy.Units {
		want, _ := deploy.Files.ReadFile(u)
		got, err := os.ReadFile(e.HomePath(filepath.Join(".config/systemd/user", u)))
		if err != nil || !bytes.Equal(got, want) {
			return false, u + " not installed or out of date", nil
		}
	}
	if r, err := e.Run.Run(ctx, e.userctl("is-active", "--quiet", "linear-agents-tmux.service")); err != nil || r.Code != 0 {
		return false, "tmux service not running", nil
	}
	if r, err := e.Run.Run(ctx, e.userctl("is-enabled", "--quiet", "linear-agents.timer")); err != nil || r.Code != 0 {
		return false, "timer not enabled", nil
	}
	return true, "timer enabled", nil
}

func (Run) Apply(ctx context.Context, e *Env) error {
	if e.Mode == Systemd {
		dir := e.HomePath(".config/systemd/user")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		for _, u := range deploy.Units {
			b, _ := deploy.Files.ReadFile(u)
			if err := os.WriteFile(filepath.Join(dir, u), b, 0o644); err != nil {
				return err
			}
		}
		for _, c := range []host.Cmd{e.userctl("daemon-reload"), e.userctl("enable", "--now", "linear-agents-tmux.service")} {
			if _, err := e.MustCmd(ctx, c); err != nil {
				return err
			}
		}
		// enable --now returns before the tmux server exists; doctor's tmux check needs it.
		for i := 0; i < 10 && !e.Ok(ctx, "tmux", "-N", "-L", "linear-agents", "list-sessions"); i++ {
			e.Sleep(500 * time.Millisecond)
		}
	} else if e.loopPane(ctx) == "" {
		// Idle until doctor passes; doctor's tmux check needs a server, and ticks never start one.
		if _, err := e.Must(ctx, "tmux", "-L", "linear-agents", "new-session", "-d", "-s", "la-loop", "sleep infinity"); err != nil {
			return err
		}
	}
	out, fails, ok := doctor(ctx, e)
	e.UI.Note("linear-agents doctor", out)
	if len(fails) > 0 {
		return fmt.Errorf("doctor reports %s; fix them and run just ship again", plural(len(fails), "FAIL line"))
	}
	if !ok {
		return fmt.Errorf("linear-agents doctor did not run cleanly; see its output above")
	}
	start, err := e.UI.Confirm("Doctor reports no FAIL line. Start ticking every 3 minutes?", true)
	if err != nil {
		return err
	}
	if !start {
		return ErrStopped
	}
	if e.Mode == Systemd {
		_, err = e.MustCmd(ctx, e.userctl("enable", "--now", "linear-agents.timer"))
	} else {
		_, err = e.Must(ctx, "tmux", "-L", "linear-agents", "respawn-pane", "-k", "-t", "=la-loop", loopCommand(e.Home))
	}
	return err
}
