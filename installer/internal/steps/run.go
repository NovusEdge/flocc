package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/NovusEdge/flocc/deploy"
	"github.com/NovusEdge/flocc/installer/internal/host"
)

type Run struct{}

// DoctorFailed is the Run step's error when doctor reports FAIL lines other than a pending
// Claude login; the service phase still prints its summary for it.
type DoctorFailed struct{ Fails []string }

func (d *DoctorFailed) Error() string {
	return fmt.Sprintf("doctor reports %s; fix them and run flocc ship -H <host> again", Plural(len(d.Fails), "FAIL line"))
}

const loginFail = "FAIL  claude login:"

func withoutLogin(fails []string) []string {
	return slices.DeleteFunc(slices.Clone(fails), func(f string) bool { return strings.HasPrefix(f, loginFail) })
}

// timerOn reports whether ticks run: the timer in systemd mode, the loop command in loop mode.
func timerOn(ctx context.Context, e *Env) bool {
	if e.Mode == Loop {
		return strings.Contains(e.loopPane(ctx), "flocc run")
	}
	r, err := e.Run.Run(ctx, e.userctl("is-enabled", "--quiet", "flocc.timer"))
	return err == nil && r.Code == 0
}

// ForgetHolds lifts every broken-main hold: a ship may have fixed the host or the checkout, and
// the next issue's ./setup tests main again. It runs on every ship, not in Run.Apply, which a
// healthy host skips. A failure (an older flocc without the flag) must not fail the ship.
func ForgetHolds(ctx context.Context, e *Env) {
	r, err := e.Run.Run(ctx, host.Cmd{Name: e.Real(".local/bin/flocc"), Args: []string{"canary", "--forget"}, Env: []string{ServicePath(e.Home)}})
	if err == nil && r.Code == 0 && strings.TrimSpace(r.Stdout) != "" {
		e.UI.Note("flocc canary --forget", r.Stdout)
	}
}

func (Run) Name() string { return "Run" }

func (e *Env) userctl(args ...string) host.Cmd {
	// sudo -iu gives no XDG_RUNTIME_DIR, and systemctl --user needs it to find the manager.
	return host.Cmd{Name: "systemctl", Args: append([]string{"--user"}, args...),
		Env: []string{"XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(e.UID)}}
}

// Sessions and checks inherit the loop's PATH, as they inherit the unit's. When the pane's
// command exits, tmux closes the window and, as the last one, the server; the loop keeps it alive.
func loopCommand(home string) string {
	return "sh -c 'while :; do env " + ServicePath(home) + " flocc run; sleep 10; done'"
}

func (e *Env) loopPane(ctx context.Context) string {
	return e.Output(ctx, "tmux", "-N", "-L", "flocc", "list-panes", "-t", "=flocc-loop", "-F", "#{pane_start_command}")
}

func (Run) Check(ctx context.Context, e *Env) (bool, string, error) {
	if e.Mode == Loop {
		if timerOn(ctx, e) {
			return true, "flocc-loop ticking", nil
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
	if _, err := os.Stat(e.HomePath(".config/systemd/user/flocc-tmux.service")); err == nil {
		return false, "old flocc-tmux.service still installed", nil
	}
	if !timerOn(ctx, e) {
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
		// 0.12 ran the session server as this user; it is flocc-agent's now.
		old := filepath.Join(dir, "flocc-tmux.service")
		if _, err := os.Stat(old); err == nil {
			e.Run.Run(ctx, e.userctl("disable", "--now", "flocc-tmux.service"))
			if err := os.Remove(old); err != nil {
				return err
			}
		}
		if _, err := e.MustCmd(ctx, e.userctl("daemon-reload")); err != nil {
			return err
		}
	} else if e.loopPane(ctx) == "" {
		// Idle until doctor passes; the loop pane lives on this user's own tmux server.
		if _, err := e.Must(ctx, "tmux", "-L", "flocc", "new-session", "-d", "-s", "flocc-loop", "sleep infinity"); err != nil {
			return err
		}
	}
	out, fails, ok := doctor(ctx, e)
	e.lastDoctor = &doctorResult{out: out, fails: fails, ok: ok}
	e.UI.Note("flocc doctor", out)
	// A pending login fails doctor until flocc login runs, which starts the timer itself.
	pending := slices.Contains(e.Pending, ClaudeLogin{}.Name())
	others := fails
	if pending {
		others = withoutLogin(fails)
	}
	switch {
	case len(others) > 0:
		return &DoctorFailed{Fails: others}
	case !ok && len(fails) == 0:
		return errors.New("flocc doctor did not run cleanly; see its output above")
	case pending:
		return ErrPending
	}
	start, err := e.UI.Confirm("Doctor reports no FAIL line. Start ticking every 3 minutes?", true)
	if err != nil {
		return err
	}
	if !start {
		return ErrStopped
	}
	if e.Mode == Systemd {
		_, err = e.MustCmd(ctx, e.userctl("enable", "--now", "flocc.timer"))
	} else {
		_, err = e.Must(ctx, "tmux", "-L", "flocc", "respawn-pane", "-k", "-t", "=flocc-loop:", loopCommand(e.Home))
	}
	return err
}
