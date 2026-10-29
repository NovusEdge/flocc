// Package cli is the desktop flocc command. Desktop verbs run here; any other command line runs
// on a host as the service user.
package cli

import (
	"context"
	"errors"
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

	"github.com/NovusEdge/flocc/installer/internal/dash"
	"github.com/NovusEdge/flocc/installer/internal/githubapi"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/linearapi"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

const usage = `flocc runs Claude sessions on Linear issues. This is its desktop command.

Desktop commands:
  flocc setup                        everything below in order: keys, a host, old versions, logins
  flocc keys                         make, check and store the Linear key and GitHub tokens
  flocc keys check                   check every stored key again
  flocc keys rotate github|linear    replace keys and push them to every host
  flocc host add <name> [--ssh DEST | --daytona SANDBOX]
  flocc host import -H <name> [--ssh DEST | --daytona SANDBOX]
  flocc host create [--cpus N] [--memory GiB] [--disk GiB] <machine> <name>
  flocc host run -H <name> -- <command...>
  flocc ship -H <name> [--interactive]    run it inside the flocc checkout: it ships that commit
  flocc login -H <name> [--email ADDR]

Host commands run on the host as the service user:
  flocc [-H <name>] <command> [args...]   e.g. flocc -H flocc-nimble doctor, flocc release NF-12
  Without -H: $FLOCC_HOST, then the only host, then the host holding the issue named.
  -H all runs status, doctor or logs on every host.
  flocc peek NF-12                   what the session's screen shows now
  flocc press NF-12 Down Enter       answer a dialog it is stuck on
  flocc say NF-12 <message>          type into the session and press Enter

Off a terminal, flocc with no command prints the status of every host.
`

type deps struct {
	out, err  io.Writer
	in        io.Reader
	ui        ui.Session // opened by run when nil; tests set a recorder
	hostsNote string     // the last unreadable-host-files note shown
	mode      ui.Mode
	home      string
	getenv    func(string) string
	run       host.Runner
	tty       bool        // stdin is a terminal, so ssh gets -t
	outTTY    func() bool // stdout is a terminal; nil asks d.out
	dash      func(context.Context) int
	resolve   func(context.Context, string) (ship.Dest, error)
	fleet     func(ctx context.Context, s ui.Session, args []string) int

	store     func() keys.Store // opened on first use: host commands never wait on the keyring
	prompt    prompter
	open      func(url string)
	http      *http.Client
	githubAPI string
	linearAPI string

	repoRoot func() (string, error)
	doShip   func(context.Context, ship.Options, io.Reader) (ship.Summary, error)
	doLogin  func(context.Context, ship.LoginOptions) error
}

func Main(args []string) int {
	home, _ := os.UserHomeDir()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, _ := os.Stdin.Stat()
	d := &deps{out: os.Stdout, in: os.Stdin, err: os.Stderr, home: home, getenv: os.Getenv,
		run: host.Exec{}, tty: st != nil && st.Mode()&os.ModeCharDevice != 0, resolve: ship.Resolve,
		fleet: func(ctx context.Context, s ui.Session, args []string) int { return ship.Fleet(ctx, s, args) }}
	d.store = sync.OnceValue(func() keys.Store { return keys.Open(home, notes{d}) })
	// Key pages are made on the desktop; only a host login, which a phone may finish, gets a QR code.
	d.open = ship.Browse
	d.http = &http.Client{Timeout: 30 * time.Second}
	d.githubAPI, d.linearAPI = githubapi.DefaultBase, linearapi.DefaultURL
	d.repoRoot = func() (string, error) {
		out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		return strings.TrimSpace(string(out)), err
	}
	d.doShip, d.doLogin = ship.Ship, ship.Login
	d.dash = func(ctx context.Context) int {
		o := dash.Options{Source: ship.NewSource(), Self: os.Args[0], In: os.Stdin, Out: os.Stdout, Open: ship.Browse,
			// Not d.store(): d.ui is nil on this path, so its keyring-wait note would be written to
			// stderr over the alternate screen.
			Titles: dashTitles(func() keys.Store { return keys.Open(home, io.Discard) }, d.linearAPI, d.http)}
		if _, err := d.repoRoot(); err != nil {
			o.ShipBlock = "cd into the flocc checkout to ship"
		}
		return dash.Run(ctx, o)
	}
	d.mode, args = ui.StripModeFlags(args)
	return run(ctx, d, args)
}

// dashTitles looks issue titles up with the stored Linear key, which it reads on the first call:
// the keyring can block, and that call runs in a command, not in the dashboard's update. A failed
// read is not kept, so the next poll tries again: the key may have been stored since.
func dashTitles(store func() keys.Store, base string, client *http.Client) func(context.Context, []string) (map[string]dash.Title, error) {
	var (
		mu  sync.Mutex
		got string
	)
	key := func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if got != "" {
			return got, nil
		}
		k, err := store().Get(keys.Linear)
		if err == nil && k == "" {
			err = errors.New("no Linear key stored; run flocc keys")
		}
		if err == nil {
			got = k
		}
		return k, err
	}
	return func(ctx context.Context, want []string) (map[string]dash.Title, error) {
		k, err := key()
		if err != nil {
			return nil, err
		}
		issues, err := (&linearapi.Client{URL: base, Key: k, HTTP: client}).IssuesByKey(ctx, want)
		if err != nil {
			return nil, err
		}
		out := make(map[string]dash.Title, len(issues))
		for _, is := range issues {
			out[is.Key] = dash.Title{Title: is.Title, URL: is.URL}
		}
		return out, nil
	}
}

// handoffOnly is a command whose whole job is one handoff to a child that owns the terminal, so
// no Bubble Tea program may hold it, or answer its terminal queries, around the child.
func handoffOnly(name string, rest []string) bool {
	switch rest[0] {
	case "help", "-h", "--help", "setup", "ship", "login", "keys":
		return false
	case "host":
		return len(rest) > 1 && rest[1] == "run"
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

// bare is flocc with no command and no hosts, or on a terminal: the usage or the dashboard.
func bare(ctx context.Context, d *deps) int {
	if len(listHosts(d)) == 0 {
		fmt.Fprint(d.err, usage)
		fmt.Fprintln(d.err, "No hosts yet; run flocc setup.")
		return 2
	}
	return d.dash(ctx)
}

// notes is where the key store reports a keyring wait or a file mode it fixed: a note on the
// session, or stderr before one is open.
type notes struct{ d *deps }

func (n notes) Write(p []byte) (int, error) {
	if n.d.ui == nil {
		return os.Stderr.Write(p)
	}
	n.d.ui.Note("keys", strings.TrimSpace(string(p)))
	return len(p), nil
}

// showErr draws an error that ended a command as a failed step, unless a failed row already says it.
func (d *deps) showErr(what string, err error) {
	if !d.ui.Failed() {
		d.ui.Step(what).Fail(err)
	}
}

// withoutWatch drops --watch and --interval from a status command that prints once.
func withoutWatch(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--watch", strings.HasPrefix(a, "--interval="):
		case a == "--interval":
			i++
		default:
			out = append(out, a)
		}
	}
	return out
}

// fullScreen is a command that works for a while and asks questions along the way.
func fullScreen(rest []string) bool {
	switch rest[0] {
	case "setup", "ship", "login":
		return true
	case "host":
		return len(rest) > 1 && rest[1] == "create"
	}
	return false
}

// sessionMode is the renderer a command gets: plain for a lone handoff, full-screen for the long
// commands on a terminal at both ends, else inline.
func sessionMode(d *deps, name string, rest []string) ui.Mode {
	if handoffOnly(name, rest) {
		return ui.ModePlain
	}
	def := ui.ModeInline
	if fullScreen(rest) && d.tty {
		def = ui.ModeFull
	}
	return ui.ModeFor(d.mode, d.getenv, d.outTerminal(), def)
}

// openSession gives d a ui session in the mode this command allows, unless a test set one.
func openSession(ctx context.Context, d *deps, name string, rest []string) {
	if d.ui == nil {
		in := d.in
		if in == nil {
			in = strings.NewReader("")
		}
		// A handoff-only command's stdout belongs to the remote command, which a pipe may read.
		out := d.out
		if handoffOnly(name, rest) {
			out = d.err
		}
		d.ui = ui.Open(ctx, sessionMode(d, name, rest), out, in, "flocc "+strings.Join(rest[:min(len(rest), 2)], " "))
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
		if len(listHosts(d)) == 0 || d.dashOK() {
			return bare(ctx, d)
		}
		// Off a terminal the caller is a script or an agent asking how the fleet is doing; the
		// usage told it nothing.
		rest = []string{"status"}
		if name == "" {
			name = "all"
		}
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
		name, rest = "all", withoutWatch(rest)
	}
	openSession(ctx, d, name, rest)
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
	}
	if name == "all" {
		return onAll(ctx, d, rest)
	}
	return onHost(ctx, d, name, rest)
}
