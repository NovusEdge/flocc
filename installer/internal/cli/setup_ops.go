package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/cardinal"
	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/create"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/tailnet"
)

// setupOps are the existing functions flocc setup calls, as fields so its tests can replace each.
type setupOps struct {
	checkout  func() (root, version string, err error)
	fillKeys  func(ctx context.Context, slots []keys.Slot) error
	checkKeys func(ctx context.Context) []string
	// tailnetClient is the stored OAuth client; false when the owner has none.
	tailnetClient   func() (tailnetAPI, bool)
	tailscaleStatus func(ctx context.Context) ([]byte, error)
	hosts           func() []hosts.Host
	saveHost        func(h hosts.Host) error
	createLogs      func() []string
	machine         func(ctx context.Context, machine string) machineState
	create          func(ctx context.Context, spec create.Spec) error
	add             func(ctx context.Context, name string, reach hosts.Host) error
	ship            func(ctx context.Context, name string) error
	readHost        func(ctx context.Context, h hosts.Host) hostState
	offline         func(ctx context.Context, name string) bool
	oldUsers        func(ctx context.Context, machine string) ([]oldUser, error)
	findUser        func(ctx context.Context, machine, user string) (oldUser, bool)
	cardinal        func() (saved.Saved, bool, error)
	cardStatus      func(ctx context.Context, s saved.Saved) (steps.CardinalStatus, error)
	cardSetup       func(ctx context.Context, s saved.Saved, o cardinal.SetupOptions) error
	removeOld       func(ctx context.Context, machine string, users []string) error
	login           func(ctx context.Context, name, email string) error
	cardLogin       func(ctx context.Context, s saved.Saved, email string) error
}

// machineState is what a machine allows. Err is set when ssh could not reach it.
type machineState struct {
	Err   error
	Sudo  bool  // sudo -n works there
	Incus error // nil when incus runs as an administrator, else the fix
}

func newSetupOps(d *deps) setupOps {
	mux, _ := ship.SSHMux()
	cd := d.cardinalDeps()
	return setupOps{
		checkout: func() (string, string, error) { return checkoutVersion(d) },
		fillKeys: func(ctx context.Context, s []keys.Slot) error {
			return keys.Fill(ctx, keysEnv(d), mapped(listHosts(d)), s)
		},
		checkKeys: func(ctx context.Context) []string { return keys.Check(ctx, keysEnv(d), mapped(listHosts(d))) },
		tailnetClient: func() (tailnetAPI, bool) {
			c := tailnetClientOf(d)
			return c, c != nil
		},
		tailscaleStatus: func(ctx context.Context) ([]byte, error) {
			out, err := host.Must(ctx, d.run, host.Cmd{Name: "tailscale", Args: []string{"status", "--json"}})
			if err != nil {
				return nil, fmt.Errorf("reading the tailnet on this desktop: %w", err)
			}
			return []byte(out), nil
		},
		hosts:      func() []hosts.Host { return listHosts(d) },
		saveHost:   func(h hosts.Host) error { return hosts.Save(hosts.Dir(d.home), h) },
		createLogs: create.Logged,
		machine:    func(ctx context.Context, m string) machineState { return probeMachine(ctx, d, mux, m) },
		create:     func(ctx context.Context, s create.Spec) error { return runCreate(ctx, d, s) },
		add:        func(ctx context.Context, name string, reach hosts.Host) error { return addHost(ctx, d, name, reach) },
		ship:       func(ctx context.Context, name string) error { return shipHost(ctx, d, name) },
		readHost:   func(ctx context.Context, h hosts.Host) hostState { return readHost(ctx, d, h) },
		offline:    func(ctx context.Context, name string) bool { return tailnetOffline(ctx, d.run, name) },
		oldUsers: func(ctx context.Context, m string) ([]oldUser, error) {
			return findOld(ctx, owner(d, mux, m), steps.OldUsers)
		},
		findUser: func(ctx context.Context, m, user string) (oldUser, bool) {
			found, err := findOld(ctx, owner(d, mux, m), []string{user})
			if err != nil || len(found) == 0 {
				return oldUser{}, false
			}
			return found[0], true
		},
		cardinal: func() (saved.Saved, bool, error) {
			s, err := saved.Load(saved.Dir(d.home), saved.DefaultUser)
			if errors.Is(err, os.ErrNotExist) {
				return s, false, nil
			}
			return s, err == nil, err
		},
		cardStatus: cd.Status,
		cardSetup:  cd.Setup,
		removeOld:  cd.RemoveOld,
		login:      func(ctx context.Context, name, email string) error { return loginWith(ctx, d, name, email) },
		cardLogin:  cd.Login,
	}
}

const tailscaleAPI = "https://api.tailscale.com"

// tailnetClientOf is the stored OAuth client, or nil when none is stored or the keyring fails: both
// leave the owner on the browser login.
func tailnetClientOf(d *deps) *tailnet.Client {
	v, err := d.store().Get(keys.Tailscale)
	id, secret, ok := keys.SplitClient(v)
	if err != nil || !ok {
		return nil
	}
	return &tailnet.Client{HTTP: d.http, Base: tailscaleAPI, ID: id, Secret: secret}
}

// oldUser is an earlier flocc's service user as the owner sees it without sudo.
type oldUser struct {
	Name      string
	UID       string
	Lingering bool
	Since     string   // when its user manager started
	Procs     []string // each process's command line, as that user wrote it
}

// findOld looks for each name on the machine m runs commands on. It fails when ssh does, which
// would otherwise read as "no such user" and hide a still-running old install.
func findOld(ctx context.Context, m host.Runner, names []string) ([]oldUser, error) {
	out := func(name string, args ...string) (string, bool) {
		r, err := m.Run(ctx, host.Cmd{Name: name, Args: args})
		return strings.TrimSpace(r.Stdout), err == nil && r.Code == 0
	}
	var found []oldUser
	for _, n := range names {
		r, err := m.Run(ctx, host.Cmd{Name: "id", Args: []string{"-u", n}})
		switch {
		case err != nil:
			return nil, err
		case r.Code == 255:
			return nil, fmt.Errorf("ssh failed: %s", strings.TrimSpace(host.Tail(r.Out(), 1)))
		case r.Code != 0:
			continue
		}
		u := oldUser{Name: n, UID: strings.TrimSpace(r.Stdout)}
		linger, _ := out("loginctl", "show-user", n, "-p", "Linger", "--value")
		u.Lingering = linger == "yes"
		u.Since, _ = out("loginctl", "show-user", n, "-p", "Timestamp", "--value")
		if procs, _ := out("ps", "-u", n, "-o", "args="); procs != "" {
			u.Procs = strings.Split(procs, "\n")
		}
		found = append(found, u)
	}
	return found, nil
}

// hostState is what flocc setup reads from a host. Err is set when it could not read the host,
// which is reported and never acted on.
type hostState struct {
	Err     string
	Version string   // flocc --version without "flocc "; "" when flocc is not installed there
	Login   *string  // claude_login from status --json; nil when the host did not ask
	Doctor  []string // failing doctor checks ship can repair
	LowDisk string   // doctor's failing disk check
}

// readHost reads flocc --version, status --json and doctor on h as the service user.
func readHost(ctx context.Context, d *deps, h hosts.Host) hostState {
	if err := connectFirst(ctx, d, h); err != nil {
		return hostState{Err: err.Error()}
	}
	dest, err := d.resolve(ctx, h.Target())
	if err != nil {
		return hostState{Err: err.Error()}
	}
	capture := func(args ...string) (host.Result, error) {
		return ship.Capture(ctx, d.run, dest, ship.FloccLine(args), nil)
	}
	why := func(what string, res host.Result, err error) hostState {
		if err != nil {
			return hostState{Err: what + ": " + err.Error()}
		}
		return hostState{Err: fmt.Sprintf("%s: exit %d: %s", what, res.Code, strings.TrimSpace(host.Tail(res.Out(), 1)))}
	}
	res, err := capture("--version")
	switch {
	case err == nil && res.Code == 127:
		return hostState{} // sh found no flocc: ship installs it
	case err != nil || res.Code != 0:
		return why("flocc --version", res, err)
	}
	st := hostState{Version: strings.TrimPrefix(strings.TrimSpace(res.Stdout), "flocc ")}
	if res, err = capture("status", "--json"); err != nil || res.Code != 0 {
		return why("flocc status", res, err)
	}
	var rep ship.Report
	if err := json.Unmarshal([]byte(res.Stdout), &rep); err != nil {
		return hostState{Err: "unreadable status: " + err.Error()}
	}
	st.Login = rep.ClaudeLogin
	// doctor exits 1 when a check fails; anything higher is ssh or the host's shell.
	if res, err = capture("doctor"); err != nil || res.Code > 1 {
		return why("flocc doctor", res, err)
	}
	st.Doctor, st.LowDisk = doctorFails(res.Stdout)
	return st
}

// doctorFails splits doctor's FAIL lines. The login is step 6's and low disk is the owner's to
// free, so neither is a reason to ship.
func doctorFails(out string) (fails []string, disk string) {
	for _, l := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(l, "FAIL  ")
		if !ok {
			continue
		}
		switch name, _, _ := strings.Cut(rest, ": "); name {
		case "claude login":
		case "disk":
			disk = rest
		default:
			fails = append(fails, rest)
		}
	}
	return fails, disk
}

// tailnetOffline reports whether this desktop's tailnet lists name as a peer that is offline.
func tailnetOffline(ctx context.Context, r host.Runner, name string) bool {
	out, err := host.Must(ctx, r, host.Cmd{Name: "tailscale", Args: []string{"status", "--json"}})
	if err != nil {
		return false
	}
	var st struct {
		Peer map[string]struct {
			HostName, DNSName string
			Online            bool
		}
	}
	if json.Unmarshal([]byte(out), &st) != nil {
		return false
	}
	for _, p := range st.Peer {
		label, _, _ := strings.Cut(p.DNSName, ".")
		if strings.EqualFold(label, name) || strings.EqualFold(p.HostName, name) {
			return !p.Online
		}
	}
	return false
}

var pyVersion = regexp.MustCompile(`(?m)^version = "([^"]+)"`)

// checkoutVersion is the flocc checkout this runs in, which ship, create and cardinal build from,
// and the version its pyproject.toml declares.
func checkoutVersion(d *deps) (string, string, error) {
	root, err := d.repoRoot()
	if err != nil {
		return "", "", fmt.Errorf("flocc setup builds from the flocc checkout it runs in; cd into it: %w", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "pyproject.toml"))
	if err != nil {
		return root, "", err
	}
	m := pyVersion.FindSubmatch(b)
	if m == nil {
		return root, "", fmt.Errorf("%s/pyproject.toml declares no version", root)
	}
	return root, string(m[1]), nil
}

// owner runs commands as the owner on machine, as host create's captured commands do.
func owner(d *deps, mux []string, machine string) create.Remote {
	return create.Remote{R: d.run, Target: machine, Opts: append([]string{"-o", "BatchMode=yes"}, mux...)}
}

// probeMachine opens the machine's shared connection on this terminal, so a host-key question or
// a Tailscale approval can be answered, then reads what the machine allows.
func probeMachine(ctx context.Context, d *deps, mux []string, machine string) machineState {
	if err := ship.Connect(ctx, d.run, ship.Dest{SSH: machine, Opts: mux}, machine, d.tty); err != nil {
		return machineState{Err: err}
	}
	sudo, incus := create.Preconditions(ctx, owner(d, mux, machine), machine)
	return machineState{Sudo: sudo, Incus: incus}
}

// runCreate is flocc host create <machine> <name> after its argument checks.
func runCreate(ctx context.Context, d *deps, spec create.Spec) error {
	inst := hostInstaller{d: d}
	if err := create.Check(spec, inst); err != nil {
		return err
	}
	return create.Run(ctx, spec, inst, d.ui, d.tty)
}
