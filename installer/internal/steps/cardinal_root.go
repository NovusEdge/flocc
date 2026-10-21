package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

var cardinalStageName = regexp.MustCompile(`^/tmp/flocc-cardinal\.[A-Za-z0-9]{6,}$`)

// ValidCardinalStage reports whether dir is a name mktemp -d /tmp/flocc-cardinal.XXXXXX gives.
func ValidCardinalStage(dir string) bool { return cardinalStageName.MatchString(dir) }

// CheckCardinalStage refuses a stage the root pass cannot trust. The owner's mktemp made it, so
// anything not owned by SUDO_UID, a symlink or a looser mode means someone else had a hand in it.
// files are the ones the pass reads from it.
func CheckCardinalStage(dir string, sudoUID int, files ...string) error {
	if !ValidCardinalStage(dir) {
		return fmt.Errorf("the root pass runs from a /tmp/flocc-cardinal.XXXXXX stage, not %s", dir)
	}
	owned := func(p string, wantDir bool) error {
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		sys, _ := st.Sys().(*syscall.Stat_t)
		switch {
		case st.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%s is a symlink", p)
		case wantDir && !st.IsDir(), !wantDir && !st.Mode().IsRegular():
			return fmt.Errorf("%s is not a %s", p, map[bool]string{true: "directory", false: "regular file"}[wantDir])
		case sys == nil || int(sys.Uid) != sudoUID:
			return fmt.Errorf("%s is not owned by uid %d", p, sudoUID)
		case wantDir && st.Mode().Perm() != 0o700:
			return fmt.Errorf("%s is mode %o, not 700", p, st.Mode().Perm())
		}
		return nil
	}
	if err := owned(dir, true); err != nil {
		return err
	}
	for _, f := range files {
		if err := owned(filepath.Join(dir, f), false); err != nil {
			return err
		}
	}
	return nil
}

// CardinalPrivileged are groups that give root on the machine, or read it all.
var CardinalPrivileged = []string{"sudo", "wheel", "admin", "adm", "systemd-journal", "disk", "incus", "incus-admin", "lxd", "docker", "libvirt"}

// cardinalHome is the user's home from the passwd database, "" when the user does not exist.
func cardinalHome(ctx context.Context, e *Env, user string) string {
	f := strings.Split(e.Output(ctx, "getent", "passwd", user), ":")
	if len(f) < 7 {
		return ""
	}
	return f[5]
}

func cardinalUID(ctx context.Context, e *Env, user string) (int, error) {
	return strconv.Atoi(e.Output(ctx, "id", "-u", user))
}

type CardinalTools struct{}

func (CardinalTools) Name() string { return "Tools" }

var cardinalTools = []string{"tmux", "curl", "ssh-keygen", "script", "nft"}

func (CardinalTools) missing(ctx context.Context, e *Env) []string {
	var miss []string
	for _, t := range cardinalTools {
		if !e.Ok(ctx, "sh", "-c", "command -v "+t) {
			miss = append(miss, t)
		}
	}
	return miss
}

func (t CardinalTools) Check(ctx context.Context, e *Env) (bool, string, error) {
	if m := t.missing(ctx, e); len(m) > 0 {
		return false, "missing: " + strings.Join(m, ", "), nil
	}
	return true, strings.Join(cardinalTools, ", "), nil
}

// Apply never installs: the machine is shared, and its packages are its owner's to choose.
func (t CardinalTools) Apply(ctx context.Context, e *Env) error {
	return fmt.Errorf("%s missing on this machine; install them with its package manager (script is in util-linux, ssh-keygen in OpenSSH's client package) and run setup again", strings.Join(t.missing(ctx, e), ", "))
}

// CardinalUser makes the user, or takes over one the saved file names (Adopt).
type CardinalUser struct {
	User  string
	Adopt bool
}

func (CardinalUser) Name() string { return "User" }

func (u CardinalUser) problems(ctx context.Context, e *Env, home string) (string, error) {
	st, err := os.Lstat(e.Path(home))
	switch {
	case err != nil:
		return "no home directory", nil
	case !st.IsDir():
		return "", fmt.Errorf("%s's home %s is not a directory", u.User, home)
	case st.Mode().Perm() != 0o700:
		return fmt.Sprintf("home is mode %o", st.Mode().Perm()), nil
	}
	if e.Output(ctx, "loginctl", "show-user", u.User, "-p", "Linger", "--value") != "yes" {
		return "lingering off", nil
	}
	return "", nil
}

func (u CardinalUser) refusals(ctx context.Context, e *Env) error {
	var errs []error
	for _, g := range strings.Fields(e.Output(ctx, "id", "-nG", u.User)) {
		if slices.Contains(CardinalPrivileged, g) {
			errs = append(errs, fmt.Errorf("%s is in group %s, which reaches root on this machine; run sudo gpasswd -d %s %s, then setup again", u.User, g, u.User, g))
		}
	}
	if strings.Contains(e.Output(ctx, "sudo", "-l", "-U", u.User), "may run the following") {
		errs = append(errs, fmt.Errorf("sudo -l -U %s lists rules; remove them from /etc/sudoers or /etc/sudoers.d with visudo, then setup again", u.User))
	}
	return errors.Join(errs...)
}

func (u *CardinalUser) Check(ctx context.Context, e *Env) (bool, string, error) {
	home := cardinalHome(ctx, e, u.User)
	if home == "" {
		return false, "no " + u.User + " user", nil
	}
	if !u.Adopt {
		return false, u.User + " exists and is not a cardinal install", nil
	}
	if p, err := u.problems(ctx, e, home); err != nil || p != "" {
		return false, p, err
	}
	if err := u.refusals(ctx, e); err != nil {
		return false, "privileged", nil
	}
	return true, "exists, home 700, lingering, no privileged group", nil
}

// Apply has a pointer receiver: a user it creates is this install's, which RunAll's check after
// Apply must see.
func (u *CardinalUser) Apply(ctx context.Context, e *Env) error {
	home := cardinalHome(ctx, e, u.User)
	switch {
	case home == "":
		if _, err := e.Must(ctx, "useradd", "--create-home", "--shell", "/bin/bash", u.User); err != nil {
			return err
		}
		u.Adopt = true
		home = cardinalHome(ctx, e, u.User)
	case !u.Adopt:
		return fmt.Errorf("a user named %s already exists and no saved cardinal file names it; if it is a cardinal install, run setup again with --adopt", u.User)
	}
	// Lstat first: chmod follows a symlink. /home is root's, so the entry cannot change after.
	st, err := os.Lstat(e.Path(home))
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s's home %s is not a directory", u.User, home)
	}
	if _, err := e.Must(ctx, "chmod", "700", home); err != nil {
		return err
	}
	if err := u.refusals(ctx, e); err != nil {
		return err
	}
	_, err = e.Must(ctx, "loginctl", "enable-linger", u.User)
	return err
}

var pubKeyLine = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/=]+( [!-~]+)?$`)

// ValidPubKey reports whether s is one public key line with an optional one-word comment.
func ValidPubKey(s string) bool { return pubKeyLine.MatchString(s) }

// desktopKeyScript runs as the user; $1 is the key line and $2 the home, so neither is ever part
// of the script's text.
const desktopKeyScript = `umask 077 && mkdir -p "$2/.ssh" && chmod 700 "$2/.ssh" && touch "$2/.ssh/authorized_keys" && chmod 600 "$2/.ssh/authorized_keys" && { grep -qxF -- "$1" "$2/.ssh/authorized_keys" || printf '%s\n' "$1" >> "$2/.ssh/authorized_keys"; }`

// DesktopKey lets the owner's desktop key log in as the user. Root never writes in the user's
// home: the user could plant a symlink there and have root write through it.
type DesktopKey struct{ User string }

func (DesktopKey) Name() string { return "Desktop key" }

func (d DesktopKey) key(e *Env) (string, error) {
	b, err := os.ReadFile(filepath.Join(e.Stage, "desktop.pub"))
	if err != nil {
		return "", err
	}
	k := strings.TrimSpace(string(b))
	if !ValidPubKey(k) {
		return "", errors.New("desktop.pub is not one public key line")
	}
	return k, nil
}

func (d DesktopKey) asUser(args ...string) host.Cmd {
	return host.Cmd{Name: "runuser", Args: append([]string{"-u", d.User, "--"}, args...)}
}

func (d DesktopKey) Check(ctx context.Context, e *Env) (bool, string, error) {
	k, err := d.key(e)
	if err != nil {
		return false, "", err
	}
	home := cardinalHome(ctx, e, d.User)
	if home == "" {
		return false, "no user yet", nil
	}
	r, err := e.Run.Run(ctx, d.asUser("grep", "-qxF", "--", k, home+"/.ssh/authorized_keys"))
	if err != nil || r.Code != 0 {
		return false, "not in authorized_keys", nil
	}
	return true, "in authorized_keys", nil
}

func (d DesktopKey) Apply(ctx context.Context, e *Env) error {
	k, err := d.key(e)
	if err != nil {
		return err
	}
	home := cardinalHome(ctx, e, d.User)
	_, err = e.MustCmd(ctx, d.asUser("/bin/sh", "-c", desktopKeyScript, "sh", k, home))
	return err
}

// EgressUnit loads EgressRules at boot. The desktop compares both files with what the machine
// holds, so a release that changes them runs the root pass again.
const EgressUnit = `[Unit]
Description=Block %i's connections to the tailnet
# user@<uid>.service runs after systemd-user-sessions, so the block holds before any of the
# user's processes start, lingering ones included.
Before=systemd-user-sessions.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=nft -f /etc/flocc-cardinal/%i.nft
ExecStop=nft delete table inet flocc-cardinal-%i

[Install]
WantedBy=multi-user.target
`

const EgressUnitPath = "/etc/systemd/system/flocc-cardinal-egress@.service"

func EgressRulesPath(user string) string { return "/etc/flocc-cardinal/" + user + ".nft" }

// EgressRules keeps uid off the tailnet: Tailscale authenticates the machine's node, which is the
// owner's, so any local user could otherwise reach the owner's other machines as the owner.
// MagicDNS stays open because the machine resolves through it, and tailscaled answers it locally.
// The empty table and delete first make the file reloadable over an older copy.
func EgressRules(user string, uid int) string {
	t := "flocc-cardinal-" + user
	u := strconv.Itoa(uid)
	return "table inet " + t + "\ndelete table inet " + t + "\ntable inet " + t + ` {
	chain output {
		type filter hook output priority 0; policy accept;
		meta skuid ` + u + ` ip daddr 100.100.100.100 meta l4proto { tcp, udp } th dport 53 accept
		meta skuid ` + u + ` ip6 daddr fd7a:115c:a1e0::53 meta l4proto { tcp, udp } th dport 53 accept
		meta skuid ` + u + ` ip daddr 100.64.0.0/10 drop
		meta skuid ` + u + ` ip6 daddr fd7a:115c:a1e0::/48 drop
	}
}
`
}

type Egress struct{ User string }

func (Egress) Name() string { return "Egress block" }

func (g Egress) unit() string { return "flocc-cardinal-egress@" + g.User + ".service" }

func (g Egress) want(ctx context.Context, e *Env) (string, error) {
	uid, err := cardinalUID(ctx, e, g.User)
	if err != nil {
		return "", fmt.Errorf("uid of %s: %w", g.User, err)
	}
	return EgressRules(g.User, uid), nil
}

func (g Egress) Check(ctx context.Context, e *Env) (bool, string, error) {
	want, err := g.want(ctx, e)
	if err != nil {
		return false, "no user yet", nil
	}
	rules, _ := os.ReadFile(e.Path(EgressRulesPath(g.User)))
	unit, _ := os.ReadFile(e.Path(EgressUnitPath))
	switch {
	case string(rules) != want || string(unit) != EgressUnit:
		return false, "rules not installed or out of date", nil
	case !e.Ok(ctx, "systemctl", "is-enabled", "--quiet", g.unit()):
		return false, "not enabled", nil
	case !e.Ok(ctx, "systemctl", "is-active", "--quiet", g.unit()):
		return false, "not active", nil
	}
	return true, "tailnet blocked for " + g.User, nil
}

func (g Egress) Apply(ctx context.Context, e *Env) error {
	want, err := g.want(ctx, e)
	if err != nil {
		return err
	}
	old, _ := os.ReadFile(e.Path(EgressRulesPath(g.User)))
	for _, d := range []string{"/etc/flocc-cardinal", filepath.Dir(EgressUnitPath)} {
		if err := os.MkdirAll(e.Path(d), 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(e.Path(EgressRulesPath(g.User)), []byte(want), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(e.Path(EgressUnitPath), []byte(EgressUnit), 0o644); err != nil {
		return err
	}
	start := "start"
	if !bytes.Equal(old, []byte(want)) {
		start = "restart"
	}
	for _, c := range [][]string{{"daemon-reload"}, {"enable", g.unit()}, {start, g.unit()}} {
		if _, err := e.Must(ctx, "systemctl", c...); err != nil {
			return err
		}
	}
	return nil
}

func CardinalRootSteps(user string, adopt bool) []Step {
	return []Step{CardinalTools{}, &CardinalUser{User: user, Adopt: adopt}, DesktopKey{User: user}, Egress{User: user}}
}

// purgeWait bounds the wait for the user's processes to end; userdel refuses while any is left,
// and tmux's pane scopes outlive the session.
const purgeWait = 10 * time.Second

// CardinalPurge deletes the user and its home, then the egress block, which must hold until no
// process of the user is left.
func CardinalPurge(ctx context.Context, e *Env, user string) error {
	if cardinalHome(ctx, e, user) != "" {
		e.Ok(ctx, "loginctl", "disable-linger", user)
		e.Ok(ctx, "loginctl", "terminate-user", user)
		for waited := time.Duration(0); ; waited += 500 * time.Millisecond {
			e.Ok(ctx, "pkill", "-KILL", "-u", user)
			if !e.Ok(ctx, "pgrep", "-u", user) {
				break
			}
			if waited >= purgeWait {
				return fmt.Errorf("%s still has processes after %s; stop them and run remove --purge again", user, purgeWait)
			}
			e.Sleep(500 * time.Millisecond)
		}
		if _, err := e.Must(ctx, "userdel", "-r", user); err != nil {
			return err
		}
	}
	e.Ok(ctx, "systemctl", "disable", "--now", Egress{User: user}.unit())
	if err := os.Remove(e.Path(EgressRulesPath(user))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
