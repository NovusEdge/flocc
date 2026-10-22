package cardinal

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/create"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

const Usage = `usage:
  flocc cardinal setup [--user NAME] [--only HOST]... [--key PATH] [--adopt] [<machine>]
  flocc cardinal login [--user NAME] [--email ADDR]
  flocc cardinal status [--user NAME]
  flocc cardinal attach [--user NAME] [--write]
  flocc cardinal restart [--user NAME]
  flocc cardinal remove [--user NAME] [--purge]
`

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// Main runs flocc cardinal <sub>. It returns the exit code.
func Main(ctx context.Context, d Deps, args []string) int {
	if len(args) == 0 {
		return d.refuse("cardinal", Usage)
	}
	sub := args[0]
	fs := flag.NewFlagSet("flocc cardinal "+sub, flag.ContinueOnError)
	var flagErr strings.Builder
	fs.SetOutput(&flagErr)
	user := fs.String("user", saved.DefaultUser, "the cardinal user on the machine")
	var only multi
	key, adopt, email, write, purge := new(string), new(bool), new(string), new(bool), new(bool)
	switch sub {
	case "setup":
		fs.Var(&only, "only", "limit setup to this desktop host file; repeat for more")
		key = fs.String("key", "", "the desktop public key that may log in as the user")
		adopt = fs.Bool("adopt", false, "take over an existing user that no saved file names")
	case "login":
		email = fs.String("email", "", "the claude.ai pool account to sign in as")
	case "attach":
		write = fs.Bool("write", false, "attach with typing, for the Linear authorisation")
	case "remove":
		purge = fs.Bool("purge", false, "also delete the user, its home and its login")
	case "status", "restart":
	default:
		return d.refuse(sub, Usage)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return d.refuse(sub, strings.TrimSpace(flagErr.String()))
	}
	if !saved.ValidUser(*user) {
		return d.refuse(sub, fmt.Sprintf("%q is not a user name", *user))
	}
	if fs.NArg() > 0 && sub != "setup" || fs.NArg() > 1 {
		return d.refuse(sub, Usage)
	}
	dir := saved.Dir(d.Home)
	s, err := saved.Load(dir, *user)
	switch {
	case sub == "setup" && errors.Is(err, os.ErrNotExist):
		s = saved.Saved{User: *user, RenewHour: 4}
	case err != nil:
		return d.refuse(sub, fmt.Sprintf("%v; run flocc cardinal setup --user %s <machine> first", err, *user))
	default:
		*adopt = true // the saved file names this user, so it is ours to take over
	}
	if sub == "setup" && fs.NArg() == 1 {
		s.Machine = fs.Arg(0)
	}
	if s.Machine == "" || strings.HasPrefix(s.Machine, "-") || !hosts.ValidTarget(s.Machine) {
		return d.refuse(sub, "give the machine's ssh destination: flocc cardinal setup <machine>")
	}
	if d.Run == nil {
		f, err := openLog(d.Home, s.User)
		if err != nil {
			d.UI.Step("cardinal " + sub).Fail(err)
			return 1
		}
		defer f.Close()
		d.Run = host.Exec{Log: f, Term: d.UI}
	}
	switch sub {
	case "setup":
		err = d.setup(ctx, s, SetupOptions{Only: only, Key: *key, Adopt: *adopt})
	case "login":
		if *email != "" && !ship.ValidEmail(*email) {
			return d.refuse(sub, fmt.Sprintf("%q is not an email address", *email))
		}
		err = d.login(ctx, s, *email)
	case "status":
		return d.status(ctx, s)
	case "attach":
		err = d.attach(ctx, s, *write)
	case "restart":
		_, err = host.Must(ctx, d.Run, d.user(s, false, "systemctl --user reset-failed cardinal.service; systemctl --user restart cardinal.service"))
		if err == nil {
			d.UI.Step("Restart").Done("cardinal restarted with a fresh session and loop")
		}
	case "remove":
		err = d.remove(ctx, s, *purge)
	}
	if err != nil {
		// Errors carry output from cardinal's side (its shell startup files, its binaries, the hosts
		// it is root on), so no escape sequence of theirs may reach this terminal.
		d.UI.Step("cardinal " + sub).Fail(errors.New(saved.SanitizeBlock(err.Error(), 40)))
		d.UI.Todo("Log: " + logPath(d.Home, s.User))
		return 1
	}
	return 0
}

// refuse is a usage or argument error: exit 2, nothing run.
func (d Deps) refuse(sub, msg string) int {
	d.UI.Step("cardinal " + sub).Fail(errors.New(msg))
	return 2
}

func logPath(home, user string) string {
	return filepath.Join(home, ".cache", "flocc", "cardinal-"+user+".log")
}

func openLog(home, user string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(logPath(home, user)), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(logPath(home, user), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

// logged runs f as Main runs a subcommand: with a runner that logs to the install's log unless d
// has one, and with f's error sanitized and naming the log, since it carries cardinal's output.
func (d Deps) logged(user string, f func(Deps) error) error {
	if d.Run == nil {
		lf, err := openLog(d.Home, user)
		if err != nil {
			return err
		}
		defer lf.Close()
		d.Run = host.Exec{Log: lf, Term: d.UI}
	}
	if err := f(d); err != nil {
		return fmt.Errorf("%s\nLog: %s", saved.SanitizeBlock(err.Error(), 40), logPath(d.Home, user))
	}
	return nil
}

// desktopKey is the --key file or the first default key, as one line with at most one comment word.
func (d Deps) desktopKey(path string) (string, error) {
	paths := []string{path}
	if path == "" {
		paths = nil
		for _, n := range []string{"id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub"} {
			paths = append(paths, filepath.Join(d.Home, ".ssh", n))
		}
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		f := strings.Fields(string(b))
		if len(f) > 3 {
			f = f[:3]
		}
		k := strings.Join(f, " ")
		if !steps.ValidPubKey(k) {
			return "", fmt.Errorf("%s is not a public key line", p)
		}
		return k, nil
	}
	return "", errors.New("no desktop public key; pass --key PATH")
}

func (d Deps) machineEnv(ctx context.Context, s saved.Saved) (*create.Env, error) {
	if err := ship.Connect(ctx, d.Run, ship.Dest{SSH: s.Machine, Opts: d.Mux}, s.Machine, d.TTY); err != nil {
		return nil, err
	}
	opts := append([]string{"-o", "BatchMode=yes"}, d.Mux...)
	ce := &create.Env{M: create.Remote{R: d.Run, Target: s.Machine, Opts: opts}, Local: d.Run, UI: d.UI,
		Spec: create.Spec{Machine: s.Machine}, TTY: d.TTY}
	return ce, ce.Probe(ctx)
}

func (d Deps) machineBuild(ctx context.Context, s saved.Saved) (string, error) {
	m, err := host.Must(ctx, d.Run, d.owner(s.Machine, false, "uname -m"))
	if err != nil {
		return "", err
	}
	arch, err := ship.ArchFor(m)
	if err != nil {
		return "", err
	}
	return d.build(ctx, arch)
}

// SetupOptions are flocc cardinal setup's flags, and the old users flocc setup agreed to remove,
// which the root pass deletes before cardinal's own root work.
type SetupOptions struct {
	Only   []string
	Key    string
	Adopt  bool
	Remove []string
}

func (d Deps) setup(ctx context.Context, s saved.Saved, o SetupOptions) error {
	key, err := d.desktopKey(o.Key)
	if err != nil {
		return err
	}
	d.UI.Group("Incus pass")
	ce, err := d.machineEnv(ctx, s)
	if err != nil {
		return err
	}
	files, err := hosts.List(hosts.Dir(d.Home))
	if err != nil {
		d.UI.Step("Host files").Skip(err.Error())
	}
	scope, skipped, gateway, err := d.incusPass(ctx, ce, o.Only, files)
	if err != nil {
		return err
	}
	for _, h := range scope {
		d.UI.Step(h.Host.Name).Done(h.Addr + ", pinned")
	}
	for _, sk := range skipped {
		name, why, _ := strings.Cut(sk, ": ")
		d.UI.Step(name).Skip("skipped: " + why)
	}
	bin, err := d.machineBuild(ctx, s)
	if err != nil {
		return err
	}
	defer os.RemoveAll(bin)
	s.Hosts = nil
	for _, h := range scope {
		s.Hosts = append(s.Hosts, h.Host.Name)
	}
	// Saved before the root pass, so a run that stops after useradd adopts the user next time.
	// A setup is no longer a removed install; flocc setup reads this flag to decide whether to ask.
	s.Removed = false
	if err := s.Save(saved.Dir(d.Home)); err != nil {
		return err
	}
	why := d.rootOutcomes(ctx, s)
	if calls := rootCalls(s.User, why, o); len(calls) > 0 {
		reasons := []string{}
		if len(o.Remove) > 0 {
			reasons = append(reasons, "removing "+strings.Join(o.Remove, ", "))
		}
		if why != "" {
			reasons = append(reasons, why)
		}
		d.UI.Note("Root pass", strings.Join(reasons, "; ")+"\nsudo asks for your password on "+s.Machine+" once")
		if err := d.rootPass(ctx, s, bin, key, calls); err != nil {
			return err
		}
		if why != "" {
			if why := d.rootOutcomes(ctx, s); why != "" {
				return fmt.Errorf("after the root pass: %s. ssh %s may be refused by the machine's sshd (AllowUsers) or the tailnet policy; setup does not change either", why, s.Dest())
			}
		}
	} else {
		d.UI.Group("Root pass")
		d.UI.Step("Outcomes").Skip("nothing to do")
	}
	// The user pass keeps exactly the host files it is sent, so it runs only once every host pass
	// succeeded: a failed host's file would otherwise be deleted from cardinal.
	runHosts := func(oldKeys []string) ([]steps.CardinalHostFile, error) {
		var lines []steps.CardinalHostFile
		var failed []string
		for _, h := range scope {
			doc := steps.HostDoc{Addr: h.Addr, Gateway: gateway, Key: s.Key, OldKeys: oldKeys, Install: s.User + "@" + saved.HostPart(s.Machine)}
			d.UI.Group("Host " + h.Host.Name)
			st := d.UI.Step("Host pass")
			r, err := d.hostPass(ctx, h.Host, bin, "cardinal-host", doc)
			if err == nil && r.Installed {
				err = d.refreshKnownHost(ctx, h.Host, r.HostKey)
			}
			if err != nil {
				st.Fail(errors.New(saved.SanitizeBlock(err.Error(), 20)))
				failed = append(failed, h.Host.Name)
				continue
			}
			st.Done(h.Addr)
			ui.Rows(d.UI, reportEvents(r))
			lines = append(lines, steps.CardinalHostFile{Name: h.Host.Name, Addr: h.Addr, KnownHost: r.HostKey})
		}
		if len(failed) > 0 {
			return nil, fmt.Errorf("%s failed, so the user pass did not run and cardinal keeps its host files; run setup again once fixed", strings.Join(failed, ", "))
		}
		return lines, nil
	}
	doc := steps.CardinalDoc{User: s.User, Machine: saved.HostPart(s.Machine), RenewHour: s.RenewHour}
	if d.Tailnet != nil {
		doc.Tailnet = d.Tailnet()
	}
	if doc.Hosts, err = runHosts(nil); err != nil {
		return err
	}
	d.UI.Group("User pass")
	r, err := d.userStep(ctx, s, bin, doc)
	if err != nil {
		return err
	}
	if r.Key != "" && r.Key != s.Key {
		var old []string
		if s.Key != "" {
			old = []string{s.Key}
			d.UI.Note("User pass", "cardinal's key changed on the machine; saving it and pushing it to the hosts again")
		}
		s.Key = r.Key
		if err := s.Save(saved.Dir(d.Home)); err != nil {
			return err
		}
		if doc.Hosts, err = runHosts(old); err != nil {
			return err
		}
		d.UI.Group("User pass")
		if r, err = d.userStep(ctx, s, bin, doc); err != nil {
			return err
		}
	}
	ui.Rows(d.UI, reportEvents(r))
	d.UI.Step("Login").Done(saved.Sanitize(r.Login))
	if r.Login != "full" {
		d.UI.Todo("flocc cardinal login --user " + s.User + " --email <pool account>")
	}
	return nil
}

func (d Deps) login(ctx context.Context, s saved.Saved, email string) error {
	remote := setupPath + " --phase cardinal-login"
	if email != "" {
		remote += " --email " + email
	}
	var rs []steps.CardinalReport
	tap := reportTap(os.Stdout, &rs)
	c := d.user(s, true, remote)
	c.Why = "cardinal login on " + s.Dest() + ". " + detachWarning
	c.Stdout = ship.WatchURLs(tap, d.Open, anthropicURL)
	err := d.Run.Interactive(ctx, c)
	tap.Flush()
	d.UI.Group("Login")
	for _, r := range rs {
		ui.Rows(d.UI, reportEvents(r))
	}
	if err != nil {
		return err
	}
	d.UI.Step("Login").Done("cardinal is running as " + s.User)
	d.UI.Todo("Linear: run flocc cardinal attach --user " + s.User + " --write, type /mcp, pick linear, Authenticate, " +
		"approve for team NF, paste the whole localhost address from the browser into the pane, " +
		"then detach with Ctrl-b d. The pane's history is cleared on detach.")
	return nil
}

// unreachable is a status that never ran; status() names setup as its remedy.
type unreachable struct{ error }

var tailscaleCheck = regexp.MustCompile(`https://login\.tailscale\.com/a/\S+`)

func (d Deps) readStatus(ctx context.Context, s saved.Saved) (steps.CardinalStatus, error) {
	// Full status health-checks every MCP server and host, so it gets longer than the outcome checks.
	ctx, cancel := context.WithTimeout(ctx, 3*reachLimit)
	defer cancel()
	var st steps.CardinalStatus
	res, err := d.Run.Run(ctx, d.user(s, false, setupPath+" --phase cardinal-status"))
	if err != nil || res.Code != 0 {
		// A check-mode SSH rule holds ssh at its URL until the timeout, so err alone reads as a dead machine.
		if u := tailscaleCheck.FindString(res.Stderr); u != "" {
			return st, fmt.Errorf("%s waits on a Tailscale SSH check: open %s, approve it, then run this again", s.Dest(), u)
		}
		why := passOutput(res, 5)
		if err != nil {
			why = err.Error()
		}
		return st, unreachable{fmt.Errorf("%s unreachable: %s", s.Dest(), why)}
	}
	if err := json.Unmarshal([]byte(res.Stdout), &st); err != nil {
		return st, fmt.Errorf("unreadable status from %s: %v", s.Dest(), err)
	}
	if st.Schema != steps.CardinalStatusSchema {
		return st, fmt.Errorf("%s reports status schema %d; run flocc cardinal setup --user %s", s.Dest(), st.Schema, s.User)
	}
	return st, nil
}

func (d Deps) status(ctx context.Context, s saved.Saved) int {
	st, err := d.readStatus(ctx, s)
	if err != nil {
		d.UI.Step("cardinal status").Fail(err)
		var u unreachable
		if errors.As(err, &u) {
			d.UI.Todo("flocc cardinal setup --user " + s.User)
		}
		return 1
	}
	return DrawStatus(d.UI, st)
}

// Setup, Status, Login and RemoveOld are flocc setup's way in: Main's subcommands without its
// flag parsing.
func (d Deps) Setup(ctx context.Context, s saved.Saved, o SetupOptions) error {
	return d.logged(s.User, func(d Deps) error { return d.setup(ctx, s, o) })
}

func (d Deps) Status(ctx context.Context, s saved.Saved) (steps.CardinalStatus, error) {
	var st steps.CardinalStatus
	err := d.logged(s.User, func(d Deps) error {
		var err error
		st, err = d.readStatus(ctx, s)
		return err
	})
	return st, err
}

func (d Deps) Login(ctx context.Context, s saved.Saved, email string) error {
	return d.logged(s.User, func(d Deps) error { return d.login(ctx, s, email) })
}

// RemoveOld runs only the purge of old users on machine, for a machine whose cardinal is healthy,
// declined or elsewhere. It needs no incus, so a machine without it can still be cleaned.
func (d Deps) RemoveOld(ctx context.Context, machine string, users []string) error {
	s := saved.Saved{Machine: machine, User: saved.DefaultUser}
	return d.logged(s.User, func(d Deps) error {
		if err := ship.Connect(ctx, d.Run, ship.Dest{SSH: machine, Opts: d.Mux}, machine, d.TTY); err != nil {
			return err
		}
		bin, err := d.machineBuild(ctx, s)
		if err != nil {
			return err
		}
		defer os.RemoveAll(bin)
		d.UI.Note("Root pass", "removing "+strings.Join(users, ", ")+"\nsudo asks for your password on "+machine+" once")
		calls := rootCalls(s.User, "", SetupOptions{Remove: users})
		return d.rootPass(ctx, s, bin, "", calls)
	})
}

// detachWarning is part of the handoff text of every writable attach and of login, because a
// handoff leaves the alternate screen before anything drawn on it is read. A double Ctrl-D exits
// claude, which moves the conversation into a Claude background daemon outside cardinal.service.
const detachWarning = "Detach with Ctrl-b d. Ctrl-D twice ends cardinal's session; the service starts a fresh one."

func (d Deps) attach(ctx context.Context, s saved.Saved, write bool) error {
	t := "=" + s.User
	remote := "tmux -L " + s.User + " attach -r -t " + t
	if write {
		remote = "tmux -L " + s.User + " attach -t " + t
	}
	c := d.user(s, true, remote)
	c.Why = "cardinal session on " + s.Dest()
	c.Attach = true
	if write {
		c.Why += ". " + detachWarning
	}
	err := d.Run.Interactive(ctx, c)
	if write {
		// The Linear authorisation pastes an address that carries the OAuth code.
		host.Must(ctx, d.Run, d.user(s, false, "tmux -L "+s.User+" clear-history -t "+t))
	}
	return err
}

const removeUnits = "systemctl --user disable --now cardinal.service cardinal-renew.timer; " +
	"rm -f ~/.config/systemd/user/cardinal.service ~/.config/systemd/user/cardinal-renew.service ~/.config/systemd/user/cardinal-renew.timer; " +
	"systemctl --user daemon-reload"

func (d Deps) remove(ctx context.Context, s saved.Saved, purge bool) error {
	if _, err := host.Must(ctx, d.Run, d.user(s, false, removeUnits)); err != nil {
		d.UI.Step("Units").Skip(saved.SanitizeBlock(err.Error(), 10))
	} else {
		d.UI.Step("Units").Done("stopped and deleted; the home, login, key and ~/state.md stay")
	}
	bin, err := d.machineBuild(ctx, s)
	if err != nil {
		return err
	}
	defer os.RemoveAll(bin)
	files, _ := hosts.List(hosts.Dir(d.Home))
	var failed []string
	for _, name := range s.Hosts {
		i := slices.IndexFunc(files, func(h hosts.Host) bool { return h.Name == name })
		if i < 0 {
			d.UI.Group("Host " + name)
			d.UI.Step("Host pass").Fail(errors.New("no desktop host file; its key line stays"))
			failed = append(failed, name)
			continue
		}
		d.UI.Group("Host " + name)
		st := d.UI.Step("Host pass")
		r, err := d.hostPass(ctx, files[i], bin, "cardinal-host-remove", steps.HostDoc{Key: s.Key, Install: s.User + "@" + saved.HostPart(s.Machine)})
		if err != nil {
			st.Fail(errors.New(saved.SanitizeBlock(err.Error(), 10)))
			failed = append(failed, name)
			continue
		}
		st.Done("")
		ui.Rows(d.UI, reportEvents(r))
	}
	dir := saved.Dir(d.Home)
	if purge {
		d.UI.Note("Purge", "sudo asks for your password on "+s.Machine+" once")
		if err := d.rootPass(ctx, s, bin, "", []rootCall{{Phase: "cardinal-purge", User: s.User}}); err != nil {
			return err
		}
		if err := saved.Delete(dir, s.User); err != nil {
			return err
		}
	} else {
		s.Removed = true
		if err := s.Save(dir); err != nil {
			return err
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%s not cleaned; run remove again once reachable", strings.Join(failed, ", "))
	}
	return nil
}
