package cardinal

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/create"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/steps"
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
		fmt.Fprint(d.Err, Usage)
		return 2
	}
	sub := args[0]
	fs := flag.NewFlagSet("flocc cardinal "+sub, flag.ContinueOnError)
	fs.SetOutput(d.Err)
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
		fmt.Fprint(d.Err, Usage)
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if !saved.ValidUser(*user) {
		fmt.Fprintf(d.Err, "flocc cardinal: %q is not a user name\n", *user)
		return 2
	}
	if fs.NArg() > 0 && sub != "setup" || fs.NArg() > 1 {
		fmt.Fprint(d.Err, Usage)
		return 2
	}
	dir := saved.Dir(d.Home)
	s, err := saved.Load(dir, *user)
	switch {
	case sub == "setup" && errors.Is(err, os.ErrNotExist):
		s = saved.Saved{User: *user, RenewHour: 4}
	case err != nil:
		fmt.Fprintf(d.Err, "flocc cardinal: %v; run flocc cardinal setup --user %s <machine> first\n", err, *user)
		return 2
	default:
		*adopt = true // the saved file names this user, so it is ours to take over
	}
	if sub == "setup" && fs.NArg() == 1 {
		s.Machine = fs.Arg(0)
	}
	if s.Machine == "" || strings.HasPrefix(s.Machine, "-") || !hosts.ValidTarget(s.Machine) {
		fmt.Fprintf(d.Err, "flocc cardinal: give the machine's ssh destination: flocc cardinal setup %s\n", "<machine>")
		return 2
	}
	if d.Run == nil {
		if err := os.MkdirAll(filepath.Dir(logPath(d.Home, s.User)), 0o700); err != nil {
			fmt.Fprintln(d.Err, "flocc cardinal:", err)
			return 1
		}
		f, err := os.OpenFile(logPath(d.Home, s.User), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintln(d.Err, "flocc cardinal:", err)
			return 1
		}
		defer f.Close()
		d.Run = host.Exec{Log: f}
	}
	switch sub {
	case "setup":
		err = d.setup(ctx, s, only, *key, *adopt)
	case "login":
		if *email != "" && !ship.ValidEmail(*email) {
			fmt.Fprintf(d.Err, "flocc cardinal: %q is not an email address\n", *email)
			return 2
		}
		err = d.login(ctx, s, *email)
	case "status":
		return d.status(ctx, s)
	case "attach":
		err = d.attach(ctx, s, *write)
	case "restart":
		_, err = host.Must(ctx, d.Run, d.user(s, false, "systemctl --user reset-failed cardinal.service; systemctl --user restart cardinal.service"))
		if err == nil {
			fmt.Fprintln(d.Out, "cardinal restarted with a fresh session and loop")
		}
	case "remove":
		err = d.remove(ctx, s, *purge)
	}
	if err != nil {
		// Errors carry output from cardinal's side (its shell startup files, its binaries, the hosts
		// it is root on), so no escape sequence of theirs may reach this terminal.
		fmt.Fprintf(d.Err, "\nflocc cardinal %s: %s\n\nLog: %s\n", sub, saved.SanitizeBlock(err.Error(), 40), logPath(d.Home, s.User))
		return 1
	}
	return 0
}

func logPath(home, user string) string {
	return filepath.Join(home, ".cache", "flocc", "cardinal-"+user+".log")
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
	ce := &create.Env{M: create.Remote{R: d.Run, Target: s.Machine, Opts: opts}, Local: d.Run, Out: d.Out,
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

func (d Deps) setup(ctx context.Context, s saved.Saved, only []string, keyPath string, adopt bool) error {
	key, err := d.desktopKey(keyPath)
	if err != nil {
		return err
	}
	ce, err := d.machineEnv(ctx, s)
	if err != nil {
		return err
	}
	files, err := hosts.List(hosts.Dir(d.Home))
	if err != nil {
		fmt.Fprintln(d.Err, "hosts:", err)
	}
	scope, skipped, gateway, err := d.incusPass(ctx, ce, only, files)
	if err != nil {
		return err
	}
	fmt.Fprintf(d.Out, "\nIncus pass\n")
	for _, h := range scope {
		fmt.Fprintf(d.Out, "  ✓ %-16s %s, pinned\n", h.Host.Name, h.Addr)
	}
	for _, sk := range skipped {
		fmt.Fprintf(d.Out, "  · skipped %s\n", sk)
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
	if err := s.Save(saved.Dir(d.Home)); err != nil {
		return err
	}
	if why := d.rootOutcomes(ctx, s); why != "" {
		fmt.Fprintf(d.Out, "\nRoot pass (%s): sudo asks for your password on %s once\n", why, s.Machine)
		if err := d.rootPass(ctx, s, bin, key, "cardinal-root", adopt); err != nil {
			return err
		}
		if why := d.rootOutcomes(ctx, s); why != "" {
			return fmt.Errorf("after the root pass: %s. ssh %s may be refused by the machine's sshd (AllowUsers) or the tailnet policy; setup does not change either", why, s.Dest())
		}
	} else {
		fmt.Fprintf(d.Out, "\nRoot pass: nothing to do\n")
	}
	// The user pass keeps exactly the host files it is sent, so it runs only once every host pass
	// succeeded: a failed host's file would otherwise be deleted from cardinal.
	runHosts := func(oldKeys []string) ([]steps.CardinalHostFile, error) {
		var lines []steps.CardinalHostFile
		var failed []string
		for _, h := range scope {
			doc := steps.HostDoc{Addr: h.Addr, Gateway: gateway, Key: s.Key, OldKeys: oldKeys, Install: s.User + "@" + saved.HostPart(s.Machine)}
			r, err := d.hostPass(ctx, h.Host, bin, "cardinal-host", doc)
			if err == nil && r.Installed {
				err = d.refreshKnownHost(ctx, h.Host, r.HostKey)
			}
			if err != nil {
				fmt.Fprintf(d.Out, "\nHost %s\n  ✗ %s\n", h.Host.Name, saved.SanitizeBlock(err.Error(), 20))
				failed = append(failed, h.Host.Name)
				continue
			}
			d.printSteps("Host "+h.Host.Name, r)
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
	r, err := d.userPass(ctx, s, bin, doc)
	if err != nil {
		return err
	}
	if r.Key != "" && r.Key != s.Key {
		var old []string
		if s.Key != "" {
			old = []string{s.Key}
			fmt.Fprintln(d.Out, "\ncardinal's key changed on the machine; saving it and pushing it to the hosts again")
		}
		s.Key = r.Key
		if err := s.Save(saved.Dir(d.Home)); err != nil {
			return err
		}
		if doc.Hosts, err = runHosts(old); err != nil {
			return err
		}
		if r, err = d.userPass(ctx, s, bin, doc); err != nil {
			return err
		}
	}
	d.printSteps("User pass", r)
	fmt.Fprintf(d.Out, "\nlogin    %s\n", saved.Sanitize(r.Login))
	if r.Login != "full" {
		fmt.Fprintf(d.Out, "next     flocc cardinal login --user %s --email <pool account>\n", s.User)
	}
	return nil
}

func (d Deps) login(ctx context.Context, s saved.Saved, email string) error {
	remote := setupPath + " --phase cardinal-login"
	if email != "" {
		remote += " --email " + email
	}
	var r steps.CardinalReport
	tap := reportTap(d.Out, &r)
	c := d.user(s, true, remote)
	c.Stdout = ship.WatchURLs(tap, d.Open, anthropicURL)
	err := d.Run.Interactive(ctx, c)
	tap.Flush()
	if err != nil {
		return err
	}
	fmt.Fprintf(d.Out, "\ncardinal is running as %s. Still yours to do, once:\n", s.User)
	fmt.Fprintf(d.Out, "  Linear: flocc cardinal attach --user %s --write, type /mcp, pick linear, Authenticate,\n", s.User)
	fmt.Fprintln(d.Out, "  approve for team NF, paste the whole localhost address from the browser into the pane,")
	fmt.Fprintln(d.Out, "  then detach with Ctrl-b d. The pane's history is cleared on detach.")
	return nil
}

func (d Deps) status(ctx context.Context, s saved.Saved) int {
	// Full status health-checks every MCP server and host, so it gets longer than the outcome checks.
	ctx, cancel := context.WithTimeout(ctx, 3*reachLimit)
	defer cancel()
	res, err := d.Run.Run(ctx, d.user(s, false, setupPath+" --phase cardinal-status"))
	if err != nil || res.Code != 0 {
		why := passOutput(res, 5)
		if err != nil {
			why = err.Error()
		}
		fmt.Fprintf(d.Out, "%s unreachable: %s\n  remedy: flocc cardinal setup --user %s\n", s.Dest(), why, s.User)
		return 1
	}
	var st steps.CardinalStatus
	switch err := json.Unmarshal([]byte(res.Stdout), &st); {
	case err != nil:
		fmt.Fprintf(d.Out, "unreadable status from %s: %v\n", s.Dest(), err)
		return 1
	case st.Schema != steps.CardinalStatusSchema:
		fmt.Fprintf(d.Out, "%s reports status schema %d; run flocc cardinal setup --user %s\n", s.Dest(), st.Schema, s.User)
		return 1
	}
	out, code := RenderStatus(st)
	fmt.Fprint(d.Out, out)
	return code
}

func (d Deps) attach(ctx context.Context, s saved.Saved, write bool) error {
	t := "=" + s.User
	remote := "tmux -L " + s.User + " attach -r -t " + t
	if write {
		remote = "tmux -L " + s.User + " attach -t " + t
	}
	err := d.Run.Interactive(ctx, d.user(s, true, remote))
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
		fmt.Fprintf(d.Out, "  · units: %s\n", saved.SanitizeBlock(err.Error(), 10))
	} else {
		fmt.Fprintln(d.Out, "  ✓ units stopped and deleted; the home, login, key and ~/state.md stay")
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
			fmt.Fprintf(d.Out, "  ✗ %s: no desktop host file; its key line stays\n", name)
			failed = append(failed, name)
			continue
		}
		r, err := d.hostPass(ctx, files[i], bin, "cardinal-host-remove", steps.HostDoc{Key: s.Key, Install: s.User + "@" + saved.HostPart(s.Machine)})
		if err != nil {
			fmt.Fprintf(d.Out, "  ✗ %s: %s\n", name, saved.SanitizeBlock(err.Error(), 10))
			failed = append(failed, name)
			continue
		}
		d.printSteps("Host "+name, r)
	}
	dir := saved.Dir(d.Home)
	if purge {
		fmt.Fprintf(d.Out, "\nPurge: sudo asks for your password on %s once\n", s.Machine)
		if err := d.rootPass(ctx, s, bin, "", "cardinal-purge", false); err != nil {
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
