package create

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// CardinalName is the cardinal VM's Incus instance. One per machine: cardinal reaches only VMs on
// its own machine (d117, d180).
const CardinalName = "flocc-cardinal"

const (
	cardinalUser = "cardinal"
	cardinalHome = "/home/" + cardinalUser
	cardinalKey  = cardinalHome + "/.ssh/id_ed25519"
	// The session's working directory: empty and root's, so nothing in the session's own tree
	// is there to read.
	cardinalSession = "/var/lib/flocc-cardinal/session"
)

// Plugin monitors waking an idle Remote Control session were checked with this version.
var cardinalClaude = [3]int{2, 1, 302}

var validPubKey = regexp.MustCompile(`^ssh-ed25519 [A-Za-z0-9+/]+={0,2}( \S+)?$`)

type CardinalSpec struct {
	Executor  string // the executor VM: its Incus instance and its flocc host name
	RenewHour int    // the local hour of cardinal's daily fresh start
}

func (e *Env) inVM(vm string, argv ...string) host.Cmd {
	return e.incus(append([]string{"exec", vm, "--"}, argv...)...)
}

// asCardinal runs a shell line as cardinal in a login shell, so HOME and PATH are cardinal's.
func (e *Env) asCardinal(line string) host.Cmd {
	return e.inVM(CardinalName, "runuser", "-l", cardinalUser, "-c", line)
}

// cardinalctl is systemctl --user for cardinal. runuser -l opens no PAM session that would set
// XDG_RUNTIME_DIR, which systemctl --user needs to find the manager lingering keeps up.
func (e *Env) cardinalctl(args ...string) host.Cmd {
	return e.asCardinal("XDG_RUNTIME_DIR=/run/user/$(id -u) systemctl --user " + strings.Join(args, " "))
}

type vmFile struct {
	Path  string
	Mode  os.FileMode
	Owner string // "root", cardinalUser or the executor's service user
	Body  []byte
}

const writeScript = `set -e; mkdir -p "$(dirname "$1")"; t="$1.flocc-tmp"; cat > "$t"; chmod "$2" "$t"; mv "$t" "$1"`

// put replaces f inside vm with what it reads on stdin. A file not root's is written by its owner,
// so the directories made on the way are the owner's too.
func (e *Env) put(ctx context.Context, vm string, f vmFile) error {
	argv := []string{"sh", "-c", writeScript, "sh", f.Path, fmt.Sprintf("%o", f.Mode)}
	if f.Owner != "root" {
		argv = append([]string{"runuser", "-u", f.Owner, "--"}, argv...)
	}
	c := e.inVM(vm, argv...)
	c.Stdin = bytes.NewReader(f.Body)
	_, err := e.must(ctx, c)
	return err
}

// read is the file at p inside vm, untrimmed, and whether it could be read.
func (e *Env) read(ctx context.Context, vm, p string) (string, bool) {
	r, err := e.M.Run(ctx, e.inVM(vm, "cat", p))
	if err != nil || r.Code != 0 {
		return "", false
	}
	return r.Stdout, true
}

// bridgeAddr is vm's address on the Incus bridge and the bridge's subnet. The executor also has a
// global address on tailscale0, which cardinal must not use.
func (e *Env) bridgeAddr(ctx context.Context, vm string) (string, *net.IPNet, error) {
	cidr := e.out(ctx, e.incus("network", "get", Bridge, "ipv4.address"))
	_, subnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", nil, fmt.Errorf("reading %s's IPv4 subnet (%q): %w", Bridge, cidr, err)
	}
	ip := e.network(ctx, vm).FirstIPv4(subnet)
	if ip == "" {
		return "", nil, fmt.Errorf("%s has no address on %s; start it with %s", vm, Bridge, e.incusLine("start "+vm))
	}
	return ip, subnet, nil
}

var cardinalTools = []string{"tmux", "ssh", "git", "curl"}

type CardinalBase struct{}

func (CardinalBase) Name() string { return "cardinal user" }

func (CardinalBase) Check(ctx context.Context, e *Env) (bool, string, error) {
	if !e.ok(ctx, e.inVM(CardinalName, "id", "-u", cardinalUser)) {
		return false, "no " + cardinalUser + " user", nil
	}
	for _, t := range cardinalTools {
		if !e.ok(ctx, e.inVM(CardinalName, "sh", "-c", "command -v "+t)) {
			return false, t + " missing", nil
		}
	}
	if !e.ok(ctx, e.inVM(CardinalName, "test", "-d", cardinalSession)) {
		return false, "no " + cardinalSession, nil
	}
	if e.out(ctx, e.inVM(CardinalName, "loginctl", "show-user", cardinalUser, "-p", "Linger", "--value")) != "yes" {
		return false, "lingering off", nil
	}
	return true, cardinalUser + " with tmux, ssh, git and curl, lingering", nil
}

func (CardinalBase) Apply(ctx context.Context, e *Env) error {
	apt := []string{"exec", CardinalName, "--env", "DEBIAN_FRONTEND=noninteractive", "--"}
	for _, args := range [][]string{
		{"apt-get", "update", "-q"},
		{"apt-get", "install", "-y", "-q", "tmux", "openssh-client", "git", "curl", "ca-certificates"},
	} {
		if _, err := e.must(ctx, e.incus(append(append([]string{}, apt...), args...)...)); err != nil {
			return err
		}
	}
	if !e.ok(ctx, e.inVM(CardinalName, "id", "-u", cardinalUser)) {
		if _, err := e.must(ctx, e.inVM(CardinalName, "useradd", "--create-home", "--shell", "/bin/bash", cardinalUser)); err != nil {
			return err
		}
	}
	if _, err := e.must(ctx, e.inVM(CardinalName, "install", "-d", "-m", "755", "-o", "root", "-g", "root", cardinalSession)); err != nil {
		return err
	}
	_, err := e.must(ctx, e.inVM(CardinalName, "loginctl", "enable-linger", cardinalUser))
	return err
}

type CardinalClaude struct{}

func (CardinalClaude) Name() string { return "cardinal's Claude Code" }

func (CardinalClaude) Check(ctx context.Context, e *Env) (bool, string, error) {
	v := e.out(ctx, e.asCardinal(".local/bin/claude --version"))
	switch {
	case v == "":
		return false, "not installed", nil
	case !host.AtLeast(v, cardinalClaude):
		return false, fmt.Sprintf("%s; needs %d.%d.%d", v, cardinalClaude[0], cardinalClaude[1], cardinalClaude[2]), nil
	}
	return true, v, nil
}

func (CardinalClaude) Apply(ctx context.Context, e *Env) error {
	_, err := e.must(ctx, e.asCardinal("curl -fsSL https://claude.ai/install.sh | bash"))
	return err
}

type CardinalKey struct{}

func (CardinalKey) Name() string { return "cardinal's ssh key" }

func (e *Env) cardinalPub(ctx context.Context) string {
	pub, _ := e.read(ctx, CardinalName, cardinalKey+".pub")
	if pub = strings.TrimSpace(pub); validPubKey.MatchString(pub) {
		return pub
	}
	return ""
}

func (CardinalKey) Check(ctx context.Context, e *Env) (bool, string, error) {
	if pub := e.cardinalPub(ctx); pub != "" {
		return true, strings.Fields(pub)[0] + " " + cardinalKey, nil
	}
	return false, "no key", nil
}

func (CardinalKey) Apply(ctx context.Context, e *Env) error {
	// A half-made pair would make ssh-keygen ask whether to overwrite, on a stdin that is empty.
	_, err := e.must(ctx, e.asCardinal("install -d -m 700 ~/.ssh && rm -f ~/.ssh/id_ed25519 ~/.ssh/id_ed25519.pub && ssh-keygen -q -t ed25519 -N '' -C cardinal@"+CardinalName+" -f ~/.ssh/id_ed25519"))
	return err
}
