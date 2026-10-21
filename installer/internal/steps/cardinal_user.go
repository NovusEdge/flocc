package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// cardinalPush differs from a host's: cardinal is the owner's assistant, so a push when it decides
// something is worth a ping is the point.
var cardinalPush = map[string]bool{"inputNeededNotifEnabled": true, "agentPushNotifEnabled": true}

const (
	cardinalLib    = ".local/lib/flocc-cardinal"
	cardinalSetup  = cardinalLib + "/flocc-setup"
	cardinalFlocc  = ".local/bin/flocc"
	cardinalKey    = ".ssh/id_ed25519"
	cardinalUnits  = ".config/systemd/user"
	cardinalHosts  = ".config/flocc/hosts"
	linearMCPURL   = "https://mcp.linear.app/mcp"
	handUnit       = "cardinal-tmux.service"
	cardinalTCPOut = 3 * time.Second
)

// writeAtomic replaces path with b through a temp file in its directory, so a reader never sees
// half a file and a running binary keeps its old inode.
func writeAtomic(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func sameFile(path string, want []byte) bool {
	b, err := os.ReadFile(path)
	return err == nil && bytes.Equal(b, want)
}

type CardinalClaude struct{}

func (CardinalClaude) Name() string { return "Claude Code" }

func (CardinalClaude) Check(ctx context.Context, e *Env) (bool, string, error) {
	ok, v := claudeVersion(ctx, e)
	switch {
	case !ok:
		return false, "claude " + v + "; needs 2.1.295", nil
	case !pushSettingsApplied(e, cardinalPush):
		return false, "push settings not applied", nil
	}
	return true, v, nil
}

func (CardinalClaude) Apply(ctx context.Context, e *Env) error {
	if ok, _ := claudeVersion(ctx, e); !ok {
		if _, err := e.Must(ctx, "sh", "-c", "curl -fsSL https://claude.ai/install.sh | bash"); err != nil {
			return err
		}
	}
	return applyPushSettings(e, cardinalPush)
}

// Binaries puts the staged flocc and flocc-setup where cardinal and its units run them.
type Binaries struct{}

func (Binaries) Name() string { return "Binaries" }

func (Binaries) pairs(e *Env) [][2]string {
	return [][2]string{{filepath.Join(e.Stage, "flocc"), e.HomePath(cardinalFlocc)}, {filepath.Join(e.Stage, "flocc-setup"), e.HomePath(cardinalSetup)}}
}

func (b Binaries) Check(ctx context.Context, e *Env) (bool, string, error) {
	for _, p := range b.pairs(e) {
		want, err := os.ReadFile(p[0])
		if err != nil {
			return false, "", err
		}
		if !sameFile(p[1], want) {
			return false, filepath.Base(p[1]) + " out of date", nil
		}
	}
	return true, "flocc and flocc-setup current", nil
}

func (b Binaries) Apply(ctx context.Context, e *Env) error {
	for _, p := range b.pairs(e) {
		want, err := os.ReadFile(p[0])
		if err != nil {
			return err
		}
		if err := writeAtomic(p[1], want, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// HostFiles hold only how cardinal reaches each host. The desktop's own host files also carry
// repos, permissions and the ntfy topic, which cardinal's flocc status does not need.
type HostFiles struct{ Hosts []CardinalHostFile }

func (HostFiles) Name() string { return "Host files" }

func hostFile(h CardinalHostFile) []byte { return []byte(fmt.Sprintf("ssh = %q\n", "root@"+h.Addr)) }

func (f HostFiles) stale(e *Env) []string {
	paths, _ := filepath.Glob(filepath.Join(e.HomePath(cardinalHosts), "*.toml"))
	var out []string
	for _, p := range paths {
		if !slices.ContainsFunc(f.Hosts, func(h CardinalHostFile) bool { return h.Name+".toml" == filepath.Base(p) }) {
			out = append(out, p)
		}
	}
	return out
}

func (f HostFiles) Check(ctx context.Context, e *Env) (bool, string, error) {
	if st, err := os.Stat(e.HomePath(cardinalHosts)); err != nil || st.Mode().Perm() != 0o700 {
		return false, "hosts directory missing or not 700", nil
	}
	for _, h := range f.Hosts {
		if !sameFile(filepath.Join(e.HomePath(cardinalHosts), h.Name+".toml"), hostFile(h)) {
			return false, h.Name + " missing or out of date", nil
		}
	}
	if s := f.stale(e); len(s) > 0 {
		return false, plural(len(s), "stale host file"), nil
	}
	return true, plural(len(f.Hosts), "host"), nil
}

func (f HostFiles) Apply(ctx context.Context, e *Env) error {
	dir := e.HomePath(cardinalHosts)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	for _, h := range f.Hosts {
		if err := writeAtomic(filepath.Join(dir, h.Name+".toml"), hostFile(h), 0o600); err != nil {
			return err
		}
	}
	for _, p := range f.stale(e) {
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	return nil
}

// Brief is ~/CLAUDE.md. The repo is its only source, so a local edit is overwritten.
type Brief struct{ User, Machine string }

func (Brief) Name() string { return "Brief" }

func (b Brief) Check(ctx context.Context, e *Env) (bool, string, error) {
	if !sameFile(e.HomePath("CLAUDE.md"), CardinalBrief(b.User, b.Machine)) {
		return false, "CLAUDE.md missing or changed", nil
	}
	return true, "CLAUDE.md current", nil
}

func (b Brief) Apply(ctx context.Context, e *Env) error {
	return writeAtomic(e.HomePath("CLAUDE.md"), CardinalBrief(b.User, b.Machine), 0o644)
}

// SSHKey is made on the machine and never replaced: its private half never leaves it, and the
// hosts hold its public half.
type SSHKey struct{ User, Machine string }

func (SSHKey) Name() string { return "SSH key" }

// CardinalPubKey is the user's public key line, "" before SSHKey made it.
func CardinalPubKey(e *Env) string {
	b, err := os.ReadFile(e.HomePath(cardinalKey + ".pub"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (k SSHKey) Check(ctx context.Context, e *Env) (bool, string, error) {
	if _, err := os.Stat(e.HomePath(cardinalKey)); err != nil || CardinalPubKey(e) == "" {
		return false, "no key", nil
	}
	return true, "ed25519", nil
}

func (k SSHKey) Apply(ctx context.Context, e *Env) error {
	if err := os.MkdirAll(e.HomePath(".ssh"), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(e.HomePath(cardinalKey)); err == nil {
		return errors.New(e.Real(cardinalKey) + " exists without its .pub; restore the .pub or move both away, then run setup again")
	}
	_, err := e.Must(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", k.User+"@"+k.Machine, "-f", e.Real(cardinalKey))
	return err
}

// KnownHosts pins each host's key as the desktop read it, so cardinal never trusts on first use.
type KnownHosts struct{ Hosts []CardinalHostFile }

func (KnownHosts) Name() string { return "known_hosts" }

func lineNames(line string, addr string) bool {
	f := strings.Fields(line)
	return len(f) > 0 && slices.Contains(strings.Split(f[0], ","), addr)
}

func (k KnownHosts) merged(existing []byte) []byte {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(string(existing), "\n"), "\n") {
		if l == "" || slices.ContainsFunc(k.Hosts, func(h CardinalHostFile) bool { return lineNames(l, h.Addr) }) {
			continue
		}
		out = append(out, l)
	}
	for _, h := range k.Hosts {
		out = append(out, h.KnownHost)
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

func (k KnownHosts) Check(ctx context.Context, e *Env) (bool, string, error) {
	b, _ := os.ReadFile(e.HomePath(".ssh/known_hosts"))
	if !bytes.Equal(b, k.merged(b)) {
		return false, "host keys missing or changed", nil
	}
	return true, plural(len(k.Hosts), "host key"), nil
}

func (k KnownHosts) Apply(ctx context.Context, e *Env) error {
	b, _ := os.ReadFile(e.HomePath(".ssh/known_hosts"))
	if err := os.MkdirAll(e.HomePath(".ssh"), 0o700); err != nil {
		return err
	}
	return writeAtomic(e.HomePath(".ssh/known_hosts"), k.merged(b), 0o644)
}

// cardinalState writes onboarding, the trust for ~ and the Linear MCP entry into ~/.claude.json,
// reporting what it added. dryRun only reports.
func cardinalState(e *Env, dryRun bool) ([]string, error) {
	var added []string
	edit := func(file map[string]json.RawMessage) (bool, error) {
		if string(file["hasCompletedOnboarding"]) != "true" {
			file["hasCompletedOnboarding"] = json.RawMessage("true")
			added = append(added, "onboarding")
		}
		trusted, err := trustFolder(file, e.Home)
		if err != nil {
			return false, err
		}
		if trusted {
			added = append(added, "trust for ~")
		}
		servers := map[string]json.RawMessage{}
		if raw, ok := file["mcpServers"]; ok {
			if err := json.Unmarshal(raw, &servers); err != nil {
				return false, fmt.Errorf("mcpServers: %w", err)
			}
		}
		if _, ok := servers["linear"]; !ok {
			servers["linear"], _ = json.Marshal(map[string]string{"type": "http", "url": linearMCPURL})
			if file["mcpServers"], err = json.Marshal(servers); err != nil {
				return false, err
			}
			added = append(added, "Linear MCP")
		}
		return len(added) > 0 && !dryRun, nil
	}
	err := editClaudeJSON(e.HomePath(".claude.json"), edit)
	return added, err
}

// CardinalPrestart runs before every start of cardinal.service, while no session runs: a running
// session rewrites ~/.claude.json from memory and drops keys written behind its back.
func CardinalPrestart(e *Env) error {
	_, err := cardinalState(e, false)
	return err
}

func unitActive(ctx context.Context, e *Env, unit string) bool {
	r, err := e.Run.Run(ctx, e.userctl("is-active", "--quiet", unit))
	return err == nil && r.Code == 0
}

type ClaudeState struct{}

func (ClaudeState) Name() string { return "Claude state" }

func (ClaudeState) Check(ctx context.Context, e *Env) (bool, string, error) {
	missing, err := cardinalState(e, true)
	switch {
	case err != nil:
		return false, "", err
	case len(missing) == 0:
		return true, "onboarding, trust and Linear MCP set", nil
	case unitActive(ctx, e, "cardinal.service"):
		return true, strings.Join(missing, ", ") + " applies at the next start", nil
	}
	return false, strings.Join(missing, ", ") + " missing", nil
}

func (ClaudeState) Apply(ctx context.Context, e *Env) error { return CardinalPrestart(e) }

// dialTailnet is a var so tests can stand in for the network.
var dialTailnet = func(addr string) error {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(addr, "22"), cardinalTCPOut)
	if err == nil {
		c.Close()
	}
	return err
}

// HostsReachable checks cardinal's own route to each host, and that the tailnet is closed to it.
type HostsReachable struct {
	Hosts   []CardinalHostFile
	Tailnet []string
}

func (HostsReachable) Name() string { return "Hosts reachable" }

func (h HostsReachable) leak() string {
	for _, a := range h.Tailnet {
		if dialTailnet(a) == nil {
			return a
		}
	}
	return ""
}

func (h HostsReachable) unreachable(ctx context.Context, e *Env) []string {
	var out []string
	for _, x := range h.Hosts {
		if !e.Ok(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "root@"+x.Addr, "true") ||
			!e.Ok(ctx, e.Real(cardinalFlocc), "-H", x.Name, "status") {
			out = append(out, x.Name)
		}
	}
	return out
}

func (h HostsReachable) Check(ctx context.Context, e *Env) (bool, string, error) {
	if a := h.leak(); a != "" {
		return false, "reaches the desktop's tailnet address " + a, nil
	}
	if u := h.unreachable(ctx, e); len(u) > 0 {
		return false, "unreachable: " + strings.Join(u, ", "), nil
	}
	return true, plural(len(h.Hosts), "host") + " reached, tailnet blocked", nil
}

func (h HostsReachable) Apply(ctx context.Context, e *Env) error {
	if a := h.leak(); a != "" {
		user := filepath.Base(e.Home)
		return fmt.Errorf("%s reaches %s on the tailnet, so the egress block is not holding; check flocc-cardinal-egress@%s on the machine, then run setup again", user, a, user)
	}
	if len(h.unreachable(ctx, e)) > 0 {
		return ErrPending
	}
	return nil
}

// Units installs cardinal's units and starts cardinal once its login is full. It never restarts a
// running cardinal: that would end the owner's conversation.
type Units struct {
	User      string
	RenewHour int
	// Notes collects what the summary must say: changes that wait for the next start, and the end
	// of the hand setup's session.
	Notes *[]string
}

func (Units) Name() string { return "Units" }

func (u Units) filesCurrent(e *Env) bool {
	for name, b := range CardinalUnits(u.RenewHour) {
		if !sameFile(e.HomePath(filepath.Join(cardinalUnits, name)), b) {
			return false
		}
	}
	return true
}

func (u Units) enabled(ctx context.Context, e *Env) bool {
	r, err := e.Run.Run(ctx, e.userctl("is-enabled", "--quiet", "cardinal.service", "cardinal-renew.timer"))
	return err == nil && r.Code == 0
}

func (u Units) Check(ctx context.Context, e *Env) (bool, string, error) {
	active := unitActive(ctx, e, "cardinal.service")
	switch {
	case !u.filesCurrent(e):
		return false, "unit files missing or out of date", nil
	case !u.enabled(ctx, e):
		return false, "not enabled", nil
	case unitActive(ctx, e, handUnit):
		return false, "the hand setup's " + handUnit + " still runs", nil
	case active:
		return true, "cardinal running", nil
	case claudeLogin(ctx, e) != "full":
		return true, "installed; starts after flocc cardinal login", nil
	}
	return false, "cardinal not running", nil
}

func (u Units) Apply(ctx context.Context, e *Env) error {
	active := unitActive(ctx, e, "cardinal.service")
	if !u.filesCurrent(e) {
		for name, b := range CardinalUnits(u.RenewHour) {
			if err := writeAtomic(e.HomePath(filepath.Join(cardinalUnits, name)), b, 0o644); err != nil {
				return err
			}
		}
		if _, err := e.MustCmd(ctx, e.userctl("daemon-reload")); err != nil {
			return err
		}
		if active {
			u.note("unit changes apply at the next renewal or flocc cardinal restart")
		}
	}
	for _, c := range [][]string{{"enable", "cardinal.service"}, {"enable", "--now", "cardinal-renew.timer"}} {
		if _, err := e.MustCmd(ctx, e.userctl(c...)); err != nil {
			return err
		}
	}
	if unitActive(ctx, e, handUnit) {
		if _, err := e.MustCmd(ctx, e.userctl("stop", handUnit)); err != nil {
			return err
		}
		u.note("the hand setup's session ended; cardinal.service starts a fresh one")
	}
	// The hand setup ran on tmux's default socket; a session left there would be a second cardinal.
	e.Ok(ctx, "tmux", "kill-session", "-t", "="+u.User)
	if active || claudeLogin(ctx, e) != "full" {
		return nil
	}
	return startCardinal(ctx, e)
}

func (u Units) note(s string) {
	if u.Notes != nil {
		*u.Notes = append(*u.Notes, s)
	}
}

// startCardinal clears a start-limit failure first: once the unit has failed five starts in an
// hour, systemd refuses a manual start too.
func startCardinal(ctx context.Context, e *Env) error {
	for _, c := range [][]string{{"reset-failed", "cardinal.service"}, {"start", "cardinal.service"}} {
		if _, err := e.MustCmd(ctx, e.userctl(c...)); err != nil {
			return err
		}
	}
	return nil
}

func CardinalUserSteps(doc CardinalDoc, notes *[]string) []Step {
	return []Step{
		CardinalClaude{},
		Binaries{},
		HostFiles{Hosts: doc.Hosts},
		Brief{User: doc.User, Machine: doc.Machine},
		SSHKey{User: doc.User, Machine: doc.Machine},
		KnownHosts{Hosts: doc.Hosts},
		ClaudeState{},
		HostsReachable{Hosts: doc.Hosts, Tailnet: doc.Tailnet},
		Units{User: doc.User, RenewHour: doc.RenewHour, Notes: notes},
	}
}
