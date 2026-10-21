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
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/dash"
	"github.com/NovusEdge/flocc/installer/internal/githubapi"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/linearapi"
	"github.com/NovusEdge/flocc/installer/internal/qr"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

const usage = `flocc runs Claude sessions on Linear issues. This is its desktop command.

Desktop commands:
  flocc setup                        everything below in order: keys, a host, old versions, cardinal, logins
  flocc keys                         make, check and store the Linear key, GitHub tokens and ntfy topic
  flocc keys check                   check every stored key again
  flocc keys rotate github|linear    replace keys and push them to every host
  flocc host add <name> [--ssh DEST | --daytona SANDBOX]
  flocc host import -H <name> [--ssh DEST | --daytona SANDBOX]
  flocc host create [--cpus N] [--memory GiB] [--disk GiB] <machine> <name>
  flocc host run -H <name> -- <command...>
  flocc ship -H <name> [--interactive]    run it inside the flocc checkout: it ships that commit
  flocc login -H <name> [--email ADDR]
  flocc cardinal setup|login|status|attach|restart|remove [--user NAME] ...
                                     the always-on assistant on a machine; flocc cardinal for details

Host commands run on the host as the service user:
  flocc [-H <name>] <command> [args...]   e.g. flocc -H flocc-nimble doctor, flocc release NF-12
  Without -H: $FLOCC_HOST, then the only host, then the host holding the issue named.
  -H all runs status, doctor or logs on every host.
`

type deps struct {
	out, err io.Writer
	in       io.Reader
	ui       ui.Session // opened by run when nil; tests set a recorder
	mode     ui.Mode
	home     string
	getenv   func(string) string
	run      host.Runner
	tty      bool        // stdin is a terminal, so ssh gets -t
	outTTY   func() bool // stdout is a terminal; nil asks d.out
	dash     func(context.Context) int
	resolve  func(context.Context, string) (ship.Dest, error)
	fleet    func(ctx context.Context, s ui.Session, args []string) int

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

func (d *deps) o() ui.Out  { return ui.NewOut(d.out) }
func (d *deps) oe() ui.Out { return ui.NewOut(d.err) }

func Main(args []string) int {
	home, _ := os.UserHomeDir()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, _ := os.Stdin.Stat()
	d := &deps{out: os.Stdout, in: os.Stdin, err: os.Stderr, home: home, getenv: os.Getenv,
		run: host.Exec{}, tty: st != nil && st.Mode()&os.ModeCharDevice != 0, resolve: ship.Resolve,
		fleet: func(ctx context.Context, s ui.Session, args []string) int { return ship.Fleet(ctx, s, os.Stdout, args) }}
	d.store = sync.OnceValue(func() keys.Store { return keys.Open(home, os.Stderr) })
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
	d.dash = func(ctx context.Context) int {
		return dash.Run(ctx, dash.Options{Source: ship.NewSource(), Self: os.Args[0], In: os.Stdin, Out: os.Stdout})
	}
	d.mode, args = ui.StripModeFlags(args)
	return run(ctx, d, args)
}

// handoffOnly is a command whose whole job is one handoff to a child that owns the terminal, so
// no Bubble Tea program may hold it, or answer its terminal queries, around the child.
func handoffOnly(name string, rest []string) bool {
	switch rest[0] {
	case "help", "-h", "--help", "setup", "ship", "login", "keys":
		return false
	case "host":
		return len(rest) > 1 && rest[1] == "run"
	case "cardinal":
		return len(rest) > 1 && rest[1] == "attach"
	}
	return name != "all"
}

func (d *deps) outTerminal() bool {
	if d.outTTY != nil {
		return d.outTTY()
	}
	f, ok := d.out.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// dashOK holds when the full-screen dashboard may take the terminal: both ends are a terminal and
// no flag or variable asks for plain output.
func (d *deps) dashOK() bool {
	if d.dash == nil || !d.tty || !d.outTerminal() {
		return false
	}
	return ui.ModeFor(d.mode, d.getenv, true, ui.ModeFull) != ui.ModePlain
}

// bare is flocc with no command: the dashboard on a terminal, else the usage.
func bare(ctx context.Context, d *deps) int {
	if len(listHosts(d)) == 0 {
		fmt.Fprint(d.err, usage)
		fmt.Fprintln(d.err, "No hosts yet; run flocc setup.")
		return 2
	}
	if d.dashOK() {
		return d.dash(ctx)
	}
	fmt.Fprint(d.err, usage)
	return 2
}

// openSession gives d a ui session in the mode this command allows, unless a test set one.
func openSession(d *deps, forcePlain bool) {
	if d.ui == nil {
		m := ui.ModeFor(d.mode, d.getenv, d.outTerminal(), ui.ModeInline)
		if forcePlain {
			m = ui.ModePlain
		}
		in := d.in
		if in == nil {
			in = strings.NewReader("")
		}
		d.ui = ui.Open(m, d.out, in)
	}
	if d.prompt == nil {
		d.prompt = sessionPrompt{ui.Prompts{S: d.ui}}
	}
	if e, ok := d.run.(host.Exec); ok {
		e.Term = d.ui
		d.run = e
	}
}

func run(ctx context.Context, d *deps, args []string) (code int) {
	name, rest, err := splitHost(args)
	if err != nil {
		fmt.Fprintln(d.err, "flocc:", err)
		return 2
	}
	if len(rest) == 0 {
		return bare(ctx, d)
	}
	switch rest[0] {
	case "help", "-h", "--help":
		fmt.Fprint(d.out, usage)
		return 0
	}
	if rest[0] == "status" && (name == "" || name == "all") && slices.Contains(rest[1:], "--watch") {
		if d.dashOK() {
			return d.dash(ctx)
		}
		// Off a terminal the redraw loop has nothing to redraw: print the table once.
		name, rest = "all", slices.DeleteFunc(slices.Clone(rest), func(a string) bool { return a == "--watch" })
	}
	openSession(d, handoffOnly(name, rest))
	defer func() {
		if c := d.ui.Close(); code == 0 {
			code = c
		}
	}()
	switch rest[0] {
	case "setup":
		return setupCmd(ctx, d, rest[1:])
	case "ship":
		return shipCmd(ctx, d, name, rest[1:])
	case "login":
		return loginCmd(ctx, d, name, rest[1:])
	case "host":
		return hostCmd(ctx, d, name, rest[1:])
	case "keys":
		return keysCmd(ctx, d, rest[1:])
	case "cardinal":
		// cardinal names its machine, not a host; a -H here would otherwise be dropped unseen.
		if name != "" {
			fmt.Fprintln(d.err, "flocc cardinal takes no -H; limit setup with --only <host>")
			return 2
		}
		return d.cardinal(ctx, rest[1:])
	}
	if name == "all" {
		return onAll(ctx, d, rest)
	}
	return onHost(ctx, d, name, rest)
}
