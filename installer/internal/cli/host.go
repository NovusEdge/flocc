package cli

import (
	"context"
	"fmt"

	"github.com/NovusEdge/flocc/installer/internal/ship"
)

// hostCmd is flocc host <sub>: args is the line after "host", name the -H value.
func hostCmd(ctx context.Context, d *deps, name string, args []string) int {
	if len(args) > 0 && args[0] == "run" {
		return hostRun(ctx, d, name, args[1:])
	}
	fmt.Fprint(d.err, usage)
	return 2
}

// hostRun is flocc host run -H <name> -- <command...>: a raw command, not the host's flocc.
func hostRun(ctx context.Context, d *deps, name string, args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(d.err, "usage: flocc host run -H <name> -- <command...>")
		return 2
	}
	h, err := pickHost(ctx, d, name, nil)
	if err != nil {
		fmt.Fprintln(d.err, "flocc:", err)
		return 2
	}
	dest, err := d.resolve(ctx, h.Target())
	if err == nil {
		err = ship.OnHost(ctx, d.run, dest, args, d.tty)
	}
	return ship.ExitCode(err, "flocc")
}
