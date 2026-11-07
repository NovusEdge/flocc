package create

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/deploy"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
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
	if err := e.aptIn(ctx, CardinalName, "tmux", "openssh-client", "git", "curl", "ca-certificates"); err != nil {
		return err
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

const (
	cardinalEtc    = "/etc/flocc-cardinal"
	cardinalPlugin = "/opt/cardinal/plugin"
	gate           = "/home/" + steps.ServiceUser + "/.local/bin/flocc-cardinal-gate"
	floccKeys      = "/home/" + steps.ServiceUser + "/.ssh/authorized_keys"
	sshdDropIn     = "/etc/ssh/sshd_config.d/flocc-cardinal.conf"
	sshUnitDropIn  = "/etc/systemd/system/ssh.service.d/flocc-cardinal.conf"
	hostKeyFile    = "/etc/ssh/ssh_host_ed25519_key.pub"
	fwFile         = cardinalEtc + "/fw.nft"
	fwUnit         = "/etc/systemd/system/flocc-cardinal-fw.service"
)

// sshd binds ListenAddress when it starts; started before the bridge address is up, it fails.
const sshAfterNetwork = "[Unit]\nWants=network-online.target\nAfter=network-online.target\n"

// nicOf is the instance's NIC on the bridge, whether the profile or the instance holds it.
func nicOf(in instance) string {
	for _, name := range slices.Sorted(maps.Keys(in.ExpandedDevices)) {
		d := in.ExpandedDevices[name]
		if d["type"] == "nic" && (d["network"] == Bridge || d["parent"] == Bridge) {
			return name
		}
	}
	return ""
}

// pin makes vm's current bridge address its fixed DHCP lease: sshd, ssh_config, the key's from=
// and the firewall name it, and a new lease would move it under them.
func (e *Env) pin(ctx context.Context, vm string) (string, error) {
	ip, _, err := e.bridgeAddr(ctx, vm)
	if err != nil {
		return "", err
	}
	in, ok := e.instanceOf(ctx, vm)
	nic := nicOf(in)
	if !ok || nic == "" {
		return "", fmt.Errorf("%s has no NIC on %s", vm, Bridge)
	}
	if in.Devices[nic]["ipv4.address"] == ip {
		return ip, nil
	}
	// override copies the profile's NIC into the instance; set changes one the instance holds.
	verb := "override"
	if _, own := in.Devices[nic]; own {
		verb = "set"
	}
	_, err = e.must(ctx, e.incus("config", "device", verb, vm, nic, "ipv4.address="+ip))
	return ip, err
}

func (e *Env) pinned(ctx context.Context, vm string) (string, bool) {
	ip, _, err := e.bridgeAddr(ctx, vm)
	if err != nil {
		return "", false
	}
	in, ok := e.instanceOf(ctx, vm)
	nic := nicOf(in)
	return ip, ok && nic != "" && in.Devices[nic]["ipv4.address"] == ip
}

// sshdConf is the executor's sshd: on the bridge address only, by key only, for the service user
// only. The owner reaches the VM through Tailscale SSH, which this sshd does not serve.
func sshdConf(addr string) string {
	return "# Written by flocc host cardinal: cardinal reaches flocc over the Incus bridge, by key.\n" +
		"ListenAddress " + addr + "\n" +
		"PasswordAuthentication no\n" +
		"KbdInteractiveAuthentication no\n" +
		"PermitRootLogin no\n" +
		"AllowUsers " + steps.ServiceUser + "\n" +
		// A vanished client must end the session, or its flocc events --follow lingers.
		"ClientAliveInterval 30\n" +
		"ClientAliveCountMax 3\n"
}

func (e *Env) systemctlIn(vm string, args ...string) host.Cmd {
	return e.inVM(vm, append([]string{"systemctl"}, args...)...)
}

func (e *Env) aptIn(ctx context.Context, vm string, pkgs ...string) error {
	apt := []string{"exec", vm, "--env", "DEBIAN_FRONTEND=noninteractive", "--", "apt-get"}
	for _, args := range [][]string{{"update", "-q"}, append([]string{"install", "-y", "-q"}, pkgs...)} {
		if _, err := e.must(ctx, e.incus(append(append([]string{}, apt...), args...)...)); err != nil {
			return err
		}
	}
	return nil
}

type ExecutorSSH struct{}

func (ExecutorSSH) Name() string { return "executor sshd" }

func (ExecutorSSH) Check(ctx context.Context, e *Env) (bool, string, error) {
	x := e.Cardinal.Executor
	ip, ok := e.pinned(ctx, x)
	if !ok {
		return false, x + "'s bridge address not pinned", nil
	}
	if got, _ := e.read(ctx, x, sshdDropIn); got != sshdConf(ip) {
		return false, "sshd not set up for cardinal", nil
	}
	if got, _ := e.read(ctx, x, sshUnitDropIn); got != sshAfterNetwork {
		return false, "sshd does not wait for the network", nil
	}
	if !e.ok(ctx, e.systemctlIn(x, "is-active", "--quiet", "ssh")) {
		return false, "sshd not running", nil
	}
	return true, "sshd on " + ip + ":22, key only, " + steps.ServiceUser + " only", nil
}

func (ExecutorSSH) Apply(ctx context.Context, e *Env) error {
	x := e.Cardinal.Executor
	ip, err := e.pin(ctx, x)
	if err != nil {
		return err
	}
	// Before the install, so the sshd apt starts never runs with Debian's defaults: password
	// login on every address.
	for _, f := range []vmFile{{sshdDropIn, 0o644, "root", []byte(sshdConf(ip))}, {sshUnitDropIn, 0o644, "root", []byte(sshAfterNetwork)}} {
		if err := e.put(ctx, x, f); err != nil {
			return err
		}
	}
	if _, err := e.must(ctx, e.systemctlIn(x, "daemon-reload")); err != nil {
		return err
	}
	if !e.ok(ctx, e.inVM(x, "sh", "-c", "command -v sshd")) {
		if err := e.aptIn(ctx, x, "openssh-server"); err != nil {
			return err
		}
	}
	_, err = e.must(ctx, e.systemctlIn(x, "restart", "ssh"))
	return err
}

// cardinalMAC is the hardware address Incus generated for cardinal's bridge NIC and keeps in the
// instance config. The firewall matches it, not an IPv4 address, because cardinal also has IPv6
// addresses on the bridge that an ip saddr rule never sees.
func (e *Env) cardinalMAC(ctx context.Context) (string, bool) {
	in, ok := e.instanceOf(ctx, CardinalName)
	nic := nicOf(in)
	if !ok || nic == "" {
		return "", false
	}
	mac := in.Config["volatile."+nic+".hwaddr"]
	return mac, mac != ""
}

// fwRules lets the cardinal VM reach the executor's sshd and nothing else on it. The first two
// lines make the file replace the table whether or not it exists.
func fwRules(cardinalMAC, cardinalIP string) string {
	return "# Written by flocc host cardinal: the cardinal VM reaches this VM's sshd and nothing else.\n" +
		"table inet flocc_cardinal\n" +
		"delete table inet flocc_cardinal\n" +
		"table inet flocc_cardinal {\n" +
		"  chain input {\n" +
		"    type filter hook input priority 0; policy accept;\n" +
		"    ether saddr " + cardinalMAC + " ip saddr " + cardinalIP + " tcp dport 22 accept\n" +
		"    ether saddr " + cardinalMAC + " drop\n" +
		"  }\n" +
		"}\n"
}

const fwUnitBody = "[Unit]\nDescription=flocc: the cardinal VM reaches only sshd here\nBefore=ssh.service\n\n" +
	"[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=/usr/sbin/nft -f " + fwFile +
	"\nExecStop=/usr/sbin/nft delete table inet flocc_cardinal\n\n[Install]\nWantedBy=multi-user.target\n"

type ExecutorFirewall struct{}

func (ExecutorFirewall) Name() string { return "executor firewall" }

func (ExecutorFirewall) Check(ctx context.Context, e *Env) (bool, string, error) {
	x := e.Cardinal.Executor
	cip, ok := e.pinned(ctx, CardinalName)
	if !ok {
		return false, "cardinal's bridge address not pinned", nil
	}
	mac, ok := e.cardinalMAC(ctx)
	if !ok {
		return false, "cardinal's NIC has no hardware address yet", nil
	}
	if got, _ := e.read(ctx, x, fwFile); got != fwRules(mac, cip) {
		return false, "no firewall for cardinal", nil
	}
	if got, _ := e.read(ctx, x, fwUnit); got != fwUnitBody {
		return false, "firewall unit missing", nil
	}
	if !e.ok(ctx, e.systemctlIn(x, "is-active", "--quiet", "flocc-cardinal-fw.service")) {
		return false, "firewall not loaded", nil
	}
	return true, cip + " reaches only port 22 on " + x, nil
}

func (ExecutorFirewall) Apply(ctx context.Context, e *Env) error {
	x := e.Cardinal.Executor
	cip, err := e.pin(ctx, CardinalName)
	if err != nil {
		return err
	}
	mac, ok := e.cardinalMAC(ctx)
	if !ok {
		return fmt.Errorf("%s's NIC has no hardware address", CardinalName)
	}
	if !e.ok(ctx, e.inVM(x, "sh", "-c", "command -v nft")) {
		if err := e.aptIn(ctx, x, "nftables"); err != nil {
			return err
		}
	}
	for _, f := range []vmFile{{fwFile, 0o644, "root", []byte(fwRules(mac, cip))}, {fwUnit, 0o644, "root", []byte(fwUnitBody)}} {
		if err := e.put(ctx, x, f); err != nil {
			return err
		}
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "flocc-cardinal-fw.service"}, {"restart", "flocc-cardinal-fw.service"}} {
		if _, err := e.must(ctx, e.systemctlIn(x, args...)); err != nil {
			return err
		}
	}
	return nil
}

// keyLine is cardinal's authorized_keys line on the executor: from cardinal's pinned address
// only, for nothing but the gate. The exec makes the gate replace sshd's shell, so its parent is
// sshd's session and events --follow notices a vanished client whatever shell flocc has.
func keyLine(from, pub string) string {
	f := strings.Fields(pub)
	return fmt.Sprintf(`restrict,command="exec %s",from="%s" %s %s cardinal`, gate, from, f[0], f[1])
}

// mergeKeys drops every line carrying line's key and appends line; the owner's other keys keep
// their place.
func mergeKeys(existing, line string) string {
	blob := strings.Fields(line)[2]
	var out []string
	for _, l := range strings.Split(existing, "\n") {
		if strings.TrimSpace(l) == "" || slices.Contains(strings.Fields(l), blob) {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(append(out, line), "\n") + "\n"
}

type ExecutorKey struct{}

func (ExecutorKey) Name() string { return "cardinal's key on the executor" }

func (e *Env) wantKeyLine(ctx context.Context) (string, error) {
	pub := e.cardinalPub(ctx)
	if pub == "" {
		return "", fmt.Errorf("cardinal has no ssh key yet")
	}
	cip, ok := e.pinned(ctx, CardinalName)
	if !ok {
		return "", fmt.Errorf("cardinal's bridge address is not pinned yet")
	}
	return keyLine(cip, pub), nil
}

func (ExecutorKey) Check(ctx context.Context, e *Env) (bool, string, error) {
	x := e.Cardinal.Executor
	if !e.ok(ctx, e.inVM(x, "test", "-x", gate)) {
		return false, gate + " missing", nil
	}
	want, err := e.wantKeyLine(ctx)
	if err != nil {
		return false, err.Error(), nil
	}
	got, _ := e.read(ctx, x, floccKeys)
	if !slices.Contains(strings.Split(got, "\n"), want) {
		return false, "cardinal's key not in " + floccKeys, nil
	}
	return true, "forced command " + gate, nil
}

func (ExecutorKey) Apply(ctx context.Context, e *Env) error {
	x := e.Cardinal.Executor
	if !e.ok(ctx, e.inVM(x, "test", "-x", gate)) {
		return fmt.Errorf("%s has no %s; run flocc ship -H %s with this checkout first", x, gate, x)
	}
	want, err := e.wantKeyLine(ctx)
	if err != nil {
		return err
	}
	if _, err := e.must(ctx, e.inVM(x, "runuser", "-u", steps.ServiceUser, "--", "install", "-d", "-m", "700", "/home/"+steps.ServiceUser+"/.ssh")); err != nil {
		return err
	}
	got, _ := e.read(ctx, x, floccKeys)
	return e.put(ctx, x, vmFile{floccKeys, 0o600, steps.ServiceUser, []byte(mergeKeys(got, want))})
}

func sshConfig(ip string) string {
	return "Host executor\n" +
		"  HostName " + ip + "\n" +
		"  User " + steps.ServiceUser + "\n" +
		"  IdentityFile " + cardinalKey + "\n" +
		"  IdentitiesOnly yes\n" +
		"  UserKnownHostsFile " + cardinalEtc + "/known_hosts\n" +
		"  StrictHostKeyChecking yes\n" +
		"  BatchMode yes\n" +
		"  ConnectTimeout 10\n" +
		"  ServerAliveInterval 30\n" +
		"  ServerAliveCountMax 3\n" +
		"  LogLevel ERROR\n"
}

// cardinalFiles is everything CardinalFiles puts in the cardinal VM, rendered.
func cardinalFiles(spec CardinalSpec, ip, hostKey string) ([]vmFile, error) {
	hk := strings.Fields(hostKey)
	if len(hk) < 2 || !strings.HasPrefix(hk[0], "ssh-") {
		return nil, fmt.Errorf("the executor's host key %q is not a public key line", strings.TrimSpace(hostKey))
	}
	var files []vmFile
	err := fs.WalkDir(deploy.Cardinal, "cardinal/plugin", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := deploy.Cardinal.ReadFile(p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if path.Dir(p) == "cardinal/plugin/bin" {
			mode = 0o755
		}
		files = append(files, vmFile{cardinalPlugin + strings.TrimPrefix(p, "cardinal/plugin"), mode, "root", b})
		return nil
	})
	if err != nil {
		return nil, err
	}
	read := func(name string) []byte { b, _ := deploy.Cardinal.ReadFile("cardinal/" + name); return b }
	render := strings.NewReplacer("{{executor}}", spec.Executor, "{{hour}}", fmt.Sprintf("%02d", spec.RenewHour))
	files = append(files,
		vmFile{"/etc/claude-code/managed-settings.json", 0o644, "root", read("managed-settings.json")},
		vmFile{cardinalEtc + "/session-settings.json", 0o644, "root", read("session-settings.json")},
		vmFile{cardinalEtc + "/ssh_config", 0o644, "root", []byte(sshConfig(ip))},
		vmFile{cardinalEtc + "/known_hosts", 0o644, "root", []byte(ip + " " + hk[0] + " " + hk[1] + "\n")},
	)
	for _, u := range []string{"cardinal.service", "cardinal-renew.service", "cardinal-renew.timer"} {
		files = append(files, vmFile{cardinalHome + "/.config/systemd/user/" + u, 0o644, cardinalUser, []byte(render.Replace(string(read(u))))})
	}
	return files, nil
}

func filesSum(files []vmFile) string {
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s %o %s %d\n", f.Path, f.Mode, f.Owner, len(f.Body))
		h.Write(f.Body)
	}
	return hex.EncodeToString(h.Sum(nil)) + "\n"
}

type CardinalFiles struct{}

func (CardinalFiles) Name() string { return "cardinal's plugin and units" }

func (e *Env) cardinalFileSet(ctx context.Context) ([]vmFile, error) {
	x := e.Cardinal.Executor
	ip, ok := e.pinned(ctx, x)
	if !ok {
		return nil, fmt.Errorf("%s's bridge address is not pinned; the executor sshd step pins it", x)
	}
	hostKey, ok := e.read(ctx, x, hostKeyFile)
	if !ok {
		return nil, fmt.Errorf("%s has no %s; the executor sshd step installs it", x, hostKeyFile)
	}
	return cardinalFiles(e.Cardinal, ip, hostKey)
}

func (CardinalFiles) Check(ctx context.Context, e *Env) (bool, string, error) {
	files, err := e.cardinalFileSet(ctx)
	if err != nil {
		return false, "", err
	}
	if got, _ := e.read(ctx, CardinalName, cardinalEtc+"/sum"); got != filesSum(files) {
		return false, "missing or out of date", nil
	}
	return true, fmt.Sprintf("plugin, units and the link to %s; fresh start at %02d:00", e.Cardinal.Executor, e.Cardinal.RenewHour), nil
}

func (CardinalFiles) Apply(ctx context.Context, e *Env) error {
	files, err := e.cardinalFileSet(ctx)
	if err != nil {
		return err
	}
	// Whole: a file a later plugin drops must not linger.
	if _, err := e.must(ctx, e.inVM(CardinalName, "rm", "-rf", cardinalPlugin)); err != nil {
		return err
	}
	for _, f := range append(files, vmFile{cardinalEtc + "/sum", 0o644, "root", []byte(filesSum(files))}) {
		if err := e.put(ctx, CardinalName, f); err != nil {
			return err
		}
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", "cardinal-renew.timer"}, {"try-restart", "cardinal.service"}} {
		if _, err := e.must(ctx, e.cardinalctl(args...)); err != nil {
			return err
		}
	}
	return nil
}

func CardinalSteps() []Step {
	return []Step{Incus{}, Root{}, VM{}, Egress{}, CardinalBase{}, CardinalClaude{}, ExecutorSSH{},
		ExecutorFirewall{}, CardinalKey{}, ExecutorKey{}, CardinalFiles{}, CardinalLink{},
		CardinalLogin{}, CardinalStart{}}
}

// RunCardinal makes or repairs the cardinal VM beside spec.Machine's executor c.Executor. Every
// step checks first, so a second run only does what is missing.
func RunCardinal(ctx context.Context, spec Spec, c CardinalSpec, s ui.Session, tty bool, checkout func() (string, error)) error {
	e, logf, err := openEnv(spec, s, tty, checkout)
	if err != nil {
		return err
	}
	defer logf.Close()
	e.Cardinal = c
	if err := e.connect(ctx); err != nil {
		return failStep(s.Step("Connect"), err)
	}
	if err := connectStep(ctx, e); err != nil {
		return err
	}
	return RunSteps(ctx, e, CardinalSteps())
}

type CardinalLink struct{}

func (CardinalLink) Name() string { return "cardinal's link" }

func (e *Env) linkCheck() host.Cmd { return e.asCardinal(cardinalPlugin + "/bin/executor status") }

func (CardinalLink) Check(ctx context.Context, e *Env) (bool, string, error) {
	if e.ok(ctx, e.linkCheck()) {
		return true, "cardinal reaches " + e.Cardinal.Executor + " through the forced command", nil
	}
	return false, "cardinal cannot run flocc status on " + e.Cardinal.Executor, nil
}

func (CardinalLink) Apply(ctx context.Context, e *Env) error {
	return fmt.Errorf("cardinal's ssh to %s fails; to see why, run on %s: %s; and see \"The link\" in docs/book/cardinal.md",
		e.Cardinal.Executor, e.Spec.Machine,
		e.incusLine("exec "+CardinalName+" -- runuser -l cardinal -c '"+cardinalPlugin+"/bin/executor status'"))
}

type CardinalLogin struct{}

func (CardinalLogin) Name() string { return "cardinal's Claude login" }

func (CardinalLogin) Check(ctx context.Context, e *Env) (bool, string, error) {
	var s struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	json.Unmarshal([]byte(e.out(ctx, e.asCardinal(".local/bin/claude auth status"))), &s)
	if !s.LoggedIn || s.AuthMethod != "claude.ai" {
		return false, "no claude.ai login", nil
	}
	return true, "claude.ai subscription", nil
}

func (CardinalLogin) Apply(ctx context.Context, e *Env) error {
	if !e.TTY {
		return fmt.Errorf("cardinal's Claude login needs you at a terminal; run flocc host cardinal in one")
	}
	attached := func(line, why string) host.Cmd {
		c := e.incus("exec", "-t", CardinalName, "--", "runuser", "-l", cardinalUser, "-c", line)
		c.Why = why
		return c
	}
	if err := e.M.Interactive(ctx, attached(".local/bin/claude auth login --claudeai", "Sign cardinal in to Claude with your claude.ai account")); err != nil {
		return err
	}
	// Ctrl-C is how the owner leaves remote-control once it is connected, so its exit status says
	// nothing; the step's check reads the login again.
	e.M.Interactive(ctx, attached("cd "+cardinalSession+" && ~/.local/bin/claude remote-control", "Remote Control consent: answer y, wait for the line saying it is connected, then press Ctrl-C"))
	return nil
}

// trustDir is cardinal's ~/.claude.json with onboarding done and dir trusted, so the first start
// does not wait at a dialog nobody is there to answer; every other key is kept.
func trustDir(raw, dir string) ([]byte, error) {
	m := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, fmt.Errorf("%s/.claude.json: %w", cardinalHome, err)
		}
	}
	projects, _ := m["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	entry, _ := projects[dir].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	entry["hasTrustDialogAccepted"] = true
	projects[dir] = entry
	m["projects"] = projects
	m["hasCompletedOnboarding"] = true
	return json.MarshalIndent(m, "", "  ")
}

type CardinalStart struct{}

func (CardinalStart) Name() string { return "cardinal's session" }

func (CardinalStart) Check(ctx context.Context, e *Env) (bool, string, error) {
	if !e.ok(ctx, e.cardinalctl("is-active", "--quiet", "cardinal.service")) {
		return false, "cardinal.service not running", nil
	}
	return true, "cardinal on Sonnet with Remote Control, woken by flocc events", nil
}

func (CardinalStart) Apply(ctx context.Context, e *Env) error {
	// Only while cardinal is down: a running claude rewrites .claude.json from memory.
	raw, _ := e.read(ctx, CardinalName, cardinalHome+"/.claude.json")
	trusted, err := trustDir(raw, cardinalSession)
	if err != nil {
		return err
	}
	if err := e.put(ctx, CardinalName, vmFile{cardinalHome + "/.claude.json", 0o600, cardinalUser, trusted}); err != nil {
		return err
	}
	_, err = e.must(ctx, e.cardinalctl("enable", "--now", "cardinal.service"))
	return err
}
