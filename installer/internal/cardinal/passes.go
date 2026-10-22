// Package cardinal is the desktop's flocc cardinal: it sets up, logs in, checks and removes the
// owner's always-on assistant on a machine, running flocc-setup's cardinal phases there.
package cardinal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/create"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/rootpass"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// Deps are the desktop's side effects, replaced in tests.
type Deps struct {
	UI       ui.Session
	Home     string
	Run      host.Runner
	TTY      bool
	Open     func(url string)
	RepoRoot func() (string, error)
	// Tailnet is the desktop's own tailnet addresses, which cardinal must fail to reach.
	Tailnet func() []string
	// Mux is the ssh options that share one connection per destination.
	Mux []string
}

const (
	setupPath   = "~/.local/lib/flocc-cardinal/flocc-setup"
	userStage   = ".cache/flocc-cardinal/stage"
	reachLimit  = 20 * time.Second
	identityMsg = "REMOTE HOST IDENTIFICATION HAS CHANGED"
)

var hostStage = regexp.MustCompile(`^/tmp/flocc-cardinal-host\.[A-Za-z0-9]{6,}$`)

// owner runs remote as the owner on the machine.
func (d Deps) owner(machine string, tty bool, remote string) host.Cmd {
	return rootpass.SSH(d.Mux, tty, machine, remote)
}

// user runs remote as the cardinal user, with forwarding pinned off.
func (d Deps) user(s saved.Saved, tty bool, remote string) host.Cmd {
	return rootpass.SSH(append(append([]string{}, saved.Pinned...), d.Mux...), tty, s.Dest(), remote)
}

func (d Deps) onHost(h hosts.Host, remote string) host.Cmd {
	return rootpass.SSH(d.Mux, false, h.SSH, remote)
}

func (d Deps) scp(opts []string, dest string, files ...string) host.Cmd {
	args := append(append([]string{"-q"}, opts...), files...)
	return host.Cmd{Name: "scp", Args: append(args, dest)}
}

// passOutput is the pass's output without its report line, sanitized: cardinal can rewrite the
// binaries the user pass and its units run.
func passOutput(res host.Result, lines int) string { return passText(res.Out(), lines) }

// passText is passOutput for output already in hand; wire lines were drawn as they came.
func passText(out string, lines int) string {
	var keep []string
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(l, steps.CardinalMarker) && !strings.HasPrefix(l, ui.WirePrefix) {
			keep = append(keep, l)
		}
	}
	return saved.SanitizeBlock(strings.TrimSpace(strings.Join(keep, "\n")), lines)
}

// streamPass runs c with its stdin and stdout set, so it is a direct run, and draws the events
// it sends as they come. The report is the "cardinal" data event; steps and notes in it were
// already drawn, so they are dropped. Output is the pass's other text, for errors.
func (d Deps) streamPass(ctx context.Context, c host.Cmd) (r steps.CardinalReport, ok bool, output string, err error) {
	var raw bytes.Buffer
	rp := ui.Replay(d.UI, "", "")
	c.Stdout = io.MultiWriter(rp, &raw)
	err = d.Run.Interactive(ctx, c)
	rp.Close()
	output = passText(raw.String(), 20)
	if text, found := rp.Data("cardinal"); found && json.Unmarshal([]byte(text), &r) == nil {
		ok = true
		r.Steps, r.Notes = nil, nil
	}
	return r, ok, output, err
}

// exitText names how a pass that sent no report ended.
func exitText(err error) string {
	var exit *exec.ExitError
	switch {
	case err == nil:
		return "exit 0"
	case errors.As(err, &exit):
		return fmt.Sprintf("exit %d", exit.ExitCode())
	}
	return saved.Sanitize(err.Error())
}

// build makes flocc and flocc-setup for the machine's architecture, from the checkout this runs in.
func (d Deps) build(ctx context.Context, arch string) (string, error) {
	root, err := d.RepoRoot()
	if err != nil {
		return "", fmt.Errorf("flocc cardinal builds from the flocc checkout it runs in; cd into it: %w", err)
	}
	d.UI.Group("Build")
	return rootpass.Build(ctx, d.Run, d.UI, root, arch, "flocc", "flocc-setup")
}

// inScope is one host cardinal will reach: its desktop host file and its bridge address.
type inScope struct {
	Host hosts.Host
	Addr string
}

type incusInstance struct {
	Name            string                       `json:"name"`
	Devices         map[string]map[string]string `json:"devices"`
	ExpandedDevices map[string]map[string]string `json:"expanded_devices"`
	State           *struct {
		Network create.Network `json:"network"`
	} `json:"state"`
}

// incusPass finds the hosts in scope, their addresses and the bridge's gateway, and pins each
// address so sshd's ListenAddress holds across reboots and lease churn.
func (d Deps) incusPass(ctx context.Context, ce *create.Env, only []string, files []hosts.Host) ([]inScope, []string, string, error) {
	out, err := host.Must(ctx, ce.M, ce.Incus("list", "-f", "json"))
	if err != nil {
		return nil, nil, "", err
	}
	var list []incusInstance
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, nil, "", fmt.Errorf("incus list: %w", err)
	}
	gw, err := host.Must(ctx, ce.M, ce.Incus("network", "get", create.Bridge, "ipv4.address"))
	if err != nil {
		return nil, nil, "", err
	}
	gwIP, bridge, err := net.ParseCIDR(strings.TrimSpace(gw))
	if err != nil {
		return nil, nil, "", fmt.Errorf("%s's ipv4.address %q: %w", create.Bridge, strings.TrimSpace(gw), err)
	}
	gateway := gwIP.String()
	var scope []inScope
	var skipped []string
	for _, h := range files {
		if len(only) > 0 && !slices.Contains(only, h.Name) {
			continue
		}
		if h.SSH == "" {
			skipped = append(skipped, h.Name+": a Daytona host; cardinal reaches only Incus VMs on its machine")
			continue
		}
		i := slices.IndexFunc(list, func(x incusInstance) bool { return x.Name == h.Name })
		if i < 0 {
			skipped = append(skipped, h.Name+": not an Incus instance on "+ce.Spec.Machine)
			continue
		}
		inst := list[i]
		addr := inst.ExpandedDevices["eth0"]["ipv4.address"]
		if addr == "" {
			if inst.State != nil {
				addr = inst.State.Network.FirstIPv4(bridge)
			}
			if addr == "" {
				skipped = append(skipped, h.Name+": no IPv4 address yet; start it and run setup again")
				continue
			}
			verb := "override"
			if _, local := inst.Devices["eth0"]; local {
				verb = "set"
			}
			if _, err := host.Must(ctx, ce.M, ce.Incus("config", "device", verb, h.Name, "eth0", "ipv4.address="+addr)); err != nil {
				return nil, nil, "", err
			}
		}
		scope = append(scope, inScope{Host: h, Addr: addr})
	}
	for _, o := range only {
		if !slices.ContainsFunc(files, func(h hosts.Host) bool { return h.Name == o }) {
			return nil, nil, "", fmt.Errorf("--only %s: no desktop host file of that name", o)
		}
	}
	return scope, skipped, gateway, nil
}

// hostPass stages flocc-setup on the host over the desktop's own route and runs phase as root.
func (d Deps) hostPass(ctx context.Context, h hosts.Host, bin, phase string, doc steps.HostDoc) (steps.CardinalReport, error) {
	probe, err := d.Run.Run(ctx, d.onHost(h, "true"))
	if err == nil && strings.Contains(probe.Stderr, identityMsg) {
		name := saved.HostPart(h.SSH)
		return steps.CardinalReport{}, fmt.Errorf("this desktop's known_hosts entry for %s is stale (installing sshd there changed the host key that Tailscale SSH presents). Compare the new key's fingerprint, read with: incus exec %s -- ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub on the machine, then run: ssh-keygen -R %s, connect once to accept it, and run setup again", name, h.Name, name)
	}
	if err != nil || probe.Code != 0 {
		if err := ship.Connect(ctx, d.Run, ship.Dest{SSH: h.SSH, Opts: d.Mux}, h.Name, d.TTY); err != nil {
			return steps.CardinalReport{}, err
		}
	}
	dir, err := host.Must(ctx, d.Run, d.onHost(h, "mktemp -d /tmp/flocc-cardinal-host.XXXXXX"))
	dir = strings.TrimSpace(dir)
	if err != nil {
		return steps.CardinalReport{}, err
	}
	if !hostStage.MatchString(dir) {
		return steps.CardinalReport{}, fmt.Errorf("mktemp on %s returned %q", h.Name, dir)
	}
	if _, err := host.Must(ctx, d.Run, d.scp(d.Mux, h.SSH+":"+dir+"/", filepath.Join(bin, "flocc-setup"))); err != nil {
		return steps.CardinalReport{}, err
	}
	body, _ := json.Marshal(doc)
	c := d.onHost(h, "chmod 755 "+dir+"/flocc-setup && "+dir+"/flocc-setup --phase "+phase+" --answers - --events; rc=$?; rm -rf "+dir+"; exit $rc")
	c.Stdin = bytes.NewReader(body)
	r, ok, out, err := d.streamPass(ctx, c)
	switch {
	case !ok:
		return r, fmt.Errorf("%s: the host pass ended without a report (%s):\n%s", h.Name, exitText(err), out)
	case r.Error != "":
		return r, fmt.Errorf("%s: %s", h.Name, saved.Sanitize(r.Error))
	}
	return r, nil
}

// refreshKnownHost replaces this desktop's entry for the host after setup installed sshd there,
// when Tailscale SSH starts presenting the new host key. A fresh connection checks it; one that
// still fails takes the entry out again, so the owner accepts the key by hand.
func (d Deps) refreshKnownHost(ctx context.Context, h hosts.Host, hostKey string) error {
	name := saved.HostPart(h.SSH)
	f := strings.Fields(hostKey)
	if len(f) < 3 {
		return fmt.Errorf("%s reported no host key", h.Name)
	}
	if _, err := host.Must(ctx, d.Run, host.Cmd{Name: "ssh-keygen", Args: []string{"-R", name}}); err != nil {
		return err
	}
	kh := filepath.Join(d.Home, ".ssh", "known_hosts")
	fh, err := os.OpenFile(kh, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(fh, "%s %s %s\n", name, f[1], f[2])
	fh.Close()
	if err != nil {
		return err
	}
	check := rootpass.SSH([]string{"-o", "ControlPath=none"}, false, h.SSH, "true")
	if res, err := d.Run.Run(ctx, check); err == nil && res.Code == 0 {
		d.UI.Step("Host key").Skip(h.Name + "'s host key changed with the sshd install; this desktop's known_hosts now holds the new one")
		return nil
	}
	host.Must(ctx, d.Run, host.Cmd{Name: "ssh-keygen", Args: []string{"-R", name}})
	return fmt.Errorf("%s's host key changed with the sshd install, and the new key did not verify; compare its fingerprint (incus exec %s -- ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub on the machine) and accept it on the next connect", h.Name, h.Name)
}

// rootOutcomes reports the first missing root outcome, "" when the root pass has nothing to do.
// None of these needs sudo.
func (d Deps) rootOutcomes(ctx context.Context, s saved.Saved) string {
	// out is the command's stdout when it succeeded, and ok whether it did.
	out := func(remote string) (string, bool) {
		r, err := d.Run.Run(ctx, d.owner(s.Machine, false, remote))
		return r.Stdout, err == nil && r.Code == 0
	}
	uidText, exists := out("id -u " + s.User)
	if !exists {
		return "no " + s.User + " user"
	}
	groups, _ := out("id -nG " + s.User)
	for _, g := range strings.Fields(groups) {
		if slices.Contains(steps.CardinalPrivileged, g) {
			return s.User + " is in group " + g
		}
	}
	uid, _ := strconv.Atoi(strings.TrimSpace(uidText))
	rules, _ := out("cat " + steps.EgressRulesPath(s.User))
	unit, _ := out("cat " + steps.EgressUnitPath)
	_, lingering := out("test \"$(loginctl show-user " + s.User + " -p Linger --value)\" = yes")
	_, active := out("systemctl is-active --quiet flocc-cardinal-egress@" + s.User + ".service")
	switch {
	case !lingering:
		return "lingering off"
	case rules != steps.EgressRules(s.User, uid) || unit != steps.EgressUnit:
		return "tailnet block missing or out of date"
	case !active:
		return "tailnet block not active"
	}
	reach, cancel := context.WithTimeout(ctx, reachLimit)
	defer cancel()
	if r, err := d.Run.Run(reach, d.user(s, false, "true")); err != nil || r.Code != 0 {
		return "the desktop key cannot log in as " + s.User
	}
	return ""
}

// rootCall is one sudo of a root pass: a machine phase run for one user.
type rootCall struct {
	Phase, User string
	Adopt       bool
}

// rootCalls are a setup's sudo calls: the agreed removals of old users, then cardinal's own root
// work when an outcome is missing (why is rootOutcomes'). None means no ssh -t and no password.
func rootCalls(user, why string, o SetupOptions) []rootCall {
	var calls []rootCall
	for _, u := range o.Remove {
		calls = append(calls, rootCall{Phase: "cardinal-purge", User: u})
	}
	if why != "" {
		calls = append(calls, rootCall{Phase: "cardinal-root", User: user, Adopt: o.Adopt})
	}
	return calls
}

func (d Deps) rootPass(ctx context.Context, s saved.Saved, bin, desktopKey string, calls []rootCall) error {
	if !d.TTY {
		return errors.New("the machine's sudo asks for a password; run this in a terminal")
	}
	files := []string{filepath.Join(bin, "flocc-setup")}
	if desktopKey != "" {
		pub := filepath.Join(bin, "desktop.pub")
		if err := os.WriteFile(pub, []byte(desktopKey+"\n"), 0o600); err != nil {
			return err
		}
		files = append(files, pub)
	}
	argv := make([][]string, len(calls))
	for i, c := range calls {
		argv[i] = []string{"--phase", c.Phase, "--user", c.User}
		if c.Adopt {
			argv[i] = append(argv[i], "--adopt")
		}
	}
	reports, err := rootpass.Run(ctx, d.Run, rootpass.Pass{Machine: s.Machine, Opts: d.Mux, Prefix: "/tmp/flocc-cardinal",
		Files: files, Calls: argv, Why: "root pass: sudo on " + s.Machine + " asks for your password"})
	// Each call prints one report, in order, so the i-th report is the i-th call's.
	for i, r := range reports {
		title := "Root pass"
		if i < len(calls) && calls[i].Phase == "cardinal-purge" {
			title = "Removed " + calls[i].User
			if r.Error != "" {
				title = "Removing " + calls[i].User + " failed"
			}
		}
		if len(r.Steps) > 0 || len(r.Notes) > 0 {
			d.UI.Group(title)
			ui.Rows(d.UI, rootpass.Events(r))
		}
	}
	return err
}

// userPass stages the binaries in the user's home over the user's own ssh and runs the pass.
func (d Deps) userPass(ctx context.Context, s saved.Saved, bin string, doc steps.CardinalDoc) (steps.CardinalReport, error) {
	// The first connection opens the master the rest share; a tailnet policy in check mode would
	// otherwise hold it at a prompt BatchMode does not refuse.
	reach, cancel := context.WithTimeout(ctx, reachLimit)
	_, err := host.Must(reach, d.Run, d.user(s, false, "mkdir -p ~/"+userStage))
	cancel()
	if err != nil {
		return steps.CardinalReport{}, err
	}
	opts := append(append([]string{}, saved.Pinned...), d.Mux...)
	if _, err := host.Must(ctx, d.Run, d.scp(opts, s.Dest()+":"+userStage+"/", filepath.Join(bin, "flocc"), filepath.Join(bin, "flocc-setup"))); err != nil {
		return steps.CardinalReport{}, err
	}
	body, _ := json.Marshal(doc)
	c := d.user(s, false, "chmod 755 ~/"+userStage+"/flocc ~/"+userStage+"/flocc-setup && ~/"+userStage+"/flocc-setup --phase cardinal --answers - --events")
	c.Stdin = bytes.NewReader(body)
	r, ok, out, err := d.streamPass(ctx, c)
	switch {
	case !ok:
		return r, fmt.Errorf("the user pass ended without a report (%s):\n%s", exitText(err), out)
	case r.Error != "":
		return r, fmt.Errorf("user pass: %s\n%s", saved.Sanitize(r.Error), out)
	}
	return r, nil
}

func (d Deps) userStep(ctx context.Context, s saved.Saved, bin string, doc steps.CardinalDoc) (steps.CardinalReport, error) {
	st := d.UI.Step("User pass")
	r, err := d.userPass(ctx, s, bin, doc)
	if err != nil {
		st.Fail(err)
		return r, err
	}
	st.Done(s.Dest())
	return r, nil
}

// anthropicURL is what cardinal's login may open in the desktop browser.
func anthropicURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "https" {
		return false
	}
	h := p.Hostname()
	return h == "claude.ai" || h == "claude.com" || strings.HasSuffix(h, ".claude.ai") || strings.HasSuffix(h, ".claude.com")
}
