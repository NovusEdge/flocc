// Package cli is the desktop flocc command. Desktop verbs run here; any other command line runs
// on a host as the service user.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/NovusEdge/flocc/installer/internal/ship"
)

const usage = `flocc runs Claude sessions on Linear issues. This is its desktop command.

Desktop commands:
  flocc keys                         make, check and store the Linear key, GitHub tokens and ntfy topic
  flocc keys check                   check every stored key again
  flocc keys rotate github|linear    replace keys and push them to every host
  flocc host add <name> [--ssh DEST | --daytona SANDBOX]
  flocc host import -H <name> [--ssh DEST | --daytona SANDBOX]
  flocc host run -H <name> -- <command...>
  flocc ship -H <name> [--interactive]    run it inside the flocc checkout: it ships that commit
  flocc login -H <name> [--email ADDR]

Host commands run on the host as the service user:
  flocc [-H <name>] <command> [args...]   e.g. flocc -H flocc-nimble doctor, flocc release NF-12
  Without -H: $FLOCC_HOST, then the only host, then the host holding the issue named.
  -H all runs status, doctor or logs on every host.
`

type deps struct {
	out, err io.Writer
	home     string
	getenv   func(string) string
}

func Main(args []string) int {
	home, _ := os.UserHomeDir()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return run(ctx, &deps{out: os.Stdout, err: os.Stderr, home: home, getenv: os.Getenv}, args)
}

func run(ctx context.Context, d *deps, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(d.err, usage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(d.out, usage)
		return 0
	case "ship":
		return ship.Main(args[1:])
	case "login":
		return ship.LoginMain(args[1:])
	case "fleet":
		return ship.FleetMain(args[1:], d.out)
	case "host":
		return ship.HostMain(args[1:])
	}
	fmt.Fprintf(d.err, "flocc: unknown command %q; flocc help lists them\n", args[0])
	return 2
}
