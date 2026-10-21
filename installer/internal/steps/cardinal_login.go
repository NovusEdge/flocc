package steps

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

type CardinalLoginOptions struct {
	User, Email string
	Consent     func(ctx context.Context, c host.Cmd) error // nil runs the real remote-control
}

// CardinalLogin gives cardinal a full claude.ai login and the Remote Control consent, then starts
// it. A running session would rewrite the new login and consent out of ~/.claude.json, so it stops
// cardinal first.
func CardinalLogin(ctx context.Context, e *Env, o CardinalLoginOptions) error {
	if o.Consent == nil {
		o.Consent = func(ctx context.Context, c host.Cmd) error {
			return answerConsent(ctx, c, os.Stdout, defaultConsentWaits)
		}
	}
	if unitActive(ctx, e, "cardinal.service") {
		stop, err := e.UI.Confirm("cardinal is running, and its session would drop the new login. Stop it for the login? It starts again at the end.", true)
		if err != nil {
			return err
		}
		if !stop {
			return ErrStopped
		}
		if _, err := e.MustCmd(ctx, e.userctl("stop", "cardinal.service")); err != nil {
			return err
		}
	}
	if claudeLogin(ctx, e) != "full" {
		args := []string{"auth", "login", "--claudeai"}
		if o.Email != "" {
			args = append(args, "--email", o.Email)
		}
		if err := e.Run.Interactive(ctx, host.Cmd{Name: e.claude(), Args: args}); err != nil {
			return err
		}
	}
	if err := requireFullLogin(ctx, e); err != nil {
		return err
	}
	if err := CardinalPrestart(e); err != nil {
		return err
	}
	if err := o.Consent(ctx, host.Cmd{Name: e.claude(), Args: []string{"remote-control"}, Dir: e.Home}); err != nil {
		return err
	}
	if err := requireFullLogin(ctx, e); err != nil {
		return err
	}
	if err := startCardinal(ctx, e); err != nil {
		return err
	}
	for range 30 {
		if e.Ok(ctx, "tmux", "-L", o.User, "has-session", "-t", "="+o.User) {
			return nil
		}
		if err := waitFor(ctx, e, time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("cardinal.service started, but its tmux session did not appear in 30s; see flocc cardinal status")
}
