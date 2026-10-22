package create

import (
	"context"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ship"
)

// Remote runs commands on the machine over ssh. ssh joins its arguments into one line for the
// remote login shell, so each word is quoted on its own.
type Remote struct {
	R      host.Runner
	Target string
	Opts   []string
}

func (r Remote) wrap(c host.Cmd, tty bool) host.Cmd {
	args := append([]string{}, r.Opts...)
	if tty {
		args = append(args, "-t")
	} else {
		// A RequestTTY setting in the owner's ssh config would otherwise give a captured command a
		// pty, which echoes its stdin (the Tailscale auth key) into the log and never passes EOF.
		args = append(args, "-T")
	}
	words := []string{quote(c.Name)}
	for _, a := range c.Args {
		words = append(words, quote(a))
	}
	return host.Cmd{Name: "ssh", Args: append(args, r.Target, strings.Join(words, " ")), Stdin: c.Stdin, Why: c.Why}
}

func (r Remote) Run(ctx context.Context, c host.Cmd) (host.Result, error) {
	return r.R.Run(ctx, r.wrap(c, false))
}

func (r Remote) Interactive(ctx context.Context, c host.Cmd) error {
	return r.R.Interactive(ctx, r.wrap(c, true))
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ControlOpts share one connection per run, so a Tailscale SSH check-mode approval made on the
// first, attached connection covers every captured command after it. They are ship's, so
// ship.Connect's -O check finds the same master. They are empty when the socket path would not
// fit; each command then opens its own connection.
func ControlOpts() []string {
	o, _ := ship.SSHMux()
	return o
}
