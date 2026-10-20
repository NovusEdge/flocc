// Package cli is the desktop flocc command. Desktop verbs run here; any other command line runs
// on a host as the service user.
package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/githubapi"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/linearapi"
	"github.com/NovusEdge/flocc/installer/internal/qr"
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
	run      host.Runner
	tty      bool // stdin is a terminal, so ssh gets -t
	resolve  func(context.Context, string) (ship.Dest, error)
	fleet    func(args []string) int

	store     func() keys.Store // opened on first use: host commands never wait on the keyring
	prompt    prompter
	open      func(url string)
	qr        func(text string)
	http      *http.Client
	githubAPI string
	linearAPI string
	ntfyBase  string
	newTopic  func() string

	repoRoot func() (string, error)
	doShip   func(context.Context, ship.Options, io.Reader) (ship.Summary, error)
	doLogin  func(context.Context, ship.LoginOptions) error
}

func Main(args []string) int {
	home, _ := os.UserHomeDir()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, _ := os.Stdin.Stat()
	d := &deps{out: os.Stdout, err: os.Stderr, home: home, getenv: os.Getenv,
		run: host.Exec{}, tty: st != nil && st.Mode()&os.ModeCharDevice != 0, resolve: ship.Resolve,
		fleet: func(args []string) int { return ship.FleetMain(args, os.Stdout) }}
	d.store = sync.OnceValue(func() keys.Store { return keys.Open(home, os.Stderr) })
	d.prompt = huhPrompt{}
	d.open = ship.OpenURL
	d.qr = func(text string) {
		if err := qr.Write(os.Stdout, text); err != nil {
			fmt.Fprintln(os.Stderr, "no QR code:", err)
		}
	}
	d.http = &http.Client{Timeout: 30 * time.Second}
	d.githubAPI, d.linearAPI, d.ntfyBase = githubapi.DefaultBase, linearapi.DefaultURL, "https://ntfy.sh"
	d.newTopic = answers.RandomTopic
	d.repoRoot = func() (string, error) {
		out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		return strings.TrimSpace(string(out)), err
	}
	d.doShip, d.doLogin = ship.Ship, ship.Login
	return run(ctx, d, args)
}

func run(ctx context.Context, d *deps, args []string) int {
	name, rest, err := splitHost(args)
	if err != nil {
		fmt.Fprintln(d.err, "flocc:", err)
		return 2
	}
	if len(rest) == 0 {
		fmt.Fprint(d.err, usage)
		return 2
	}
	switch rest[0] {
	case "help", "-h", "--help":
		fmt.Fprint(d.out, usage)
		return 0
	case "ship":
		return shipCmd(ctx, d, name, rest[1:])
	case "login":
		return loginCmd(ctx, d, name, rest[1:])
	case "host":
		return hostCmd(ctx, d, name, rest[1:])
	case "keys":
		return keysCmd(ctx, d, rest[1:])
	}
	if name == "all" {
		return onAll(ctx, d, rest)
	}
	return onHost(ctx, d, name, rest)
}
