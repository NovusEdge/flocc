package steps

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const (
	sshdDropIn      = "/etc/ssh/sshd_config.d/cardinal.conf"
	sysctlFile      = "/etc/sysctl.d/60-flocc-cardinal.conf"
	hostKeyFile     = "/etc/ssh/ssh_host_ed25519_key.pub"
	cardinalVarDir  = "/var/lib/flocc-cardinal"
	sshInstalled    = cardinalVarDir + "/ssh-installed"
	installsDir     = cardinalVarDir + "/installs"
	authorizedKeys  = "/root/.ssh/authorized_keys"
	nonlocalBindOn  = "net.ipv4.ip_nonlocal_bind = 1\n"
	installNameChar = `^[a-z_][a-z0-9_-]*@[A-Za-z0-9][A-Za-z0-9.-]*$`
)

var installName = regexp.MustCompile(installNameChar)

func sshdConf(addr string) string {
	return "# flocc cardinal setup: cardinal reaches root over the Incus bridge, by key only.\n" +
		"ListenAddress " + addr + "\n" +
		"PasswordAuthentication no\n" +
		"KbdInteractiveAuthentication no\n" +
		"PermitRootLogin prohibit-password\n"
}

// KeyLine is cardinal's authorized_keys line: usable only from the machine over the bridge, and
// for nothing but a session.
func KeyLine(gateway, key string) string {
	return fmt.Sprintf(`from="%s",no-agent-forwarding,no-port-forwarding,no-X11-forwarding %s`, gateway, key)
}

// keyBlob is a line's key type and base64, the part that names the key whatever the options and
// comment around it.
func keyBlob(line string) string {
	f := strings.Fields(line)
	for i := 0; i+1 < len(f); i++ {
		if ValidPubKey(f[i] + " " + f[i+1]) {
			return f[i] + " " + f[i+1]
		}
	}
	return ""
}

// MergeAuthorizedKeys puts line in place of the first line carrying any of keys, drops any other
// line carrying one, and appends line when none did. Every other line keeps its place.
func MergeAuthorizedKeys(existing []byte, line string, keys []string) []byte {
	blobs := map[string]bool{}
	for _, k := range keys {
		if b := keyBlob(k); b != "" {
			blobs[b] = true
		}
	}
	var out []string
	placed := false
	for _, l := range strings.Split(strings.TrimRight(string(existing), "\n"), "\n") {
		switch {
		case l == "":
		case !blobs[keyBlob(l)]:
			out = append(out, l)
		case !placed && line != "":
			out = append(out, line)
			placed = true
		}
	}
	if !placed && line != "" {
		out = append(out, line)
	}
	if len(out) == 0 {
		return nil
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

// writeIfChanged reports whether it wrote.
func writeIfChanged(path string, b []byte, mode os.FileMode) (bool, error) {
	if sameFile(path, b) {
		return false, nil
	}
	return true, writeAtomic(path, b, mode)
}

func validHostDoc(d HostDoc) error {
	switch {
	case net.ParseIP(d.Addr).To4() == nil:
		return fmt.Errorf("address %q is not IPv4", d.Addr)
	case net.ParseIP(d.Gateway).To4() == nil:
		return fmt.Errorf("gateway %q is not IPv4", d.Gateway)
	case !installName.MatchString(d.Install):
		return fmt.Errorf("install %q is not <user>@<machine>", d.Install)
	case d.Key != "" && !ValidPubKey(d.Key):
		return errors.New("the key is not one public key line")
	}
	return nil
}

// CardinalHost readies a host for cardinal: sshd on the bridge address only, key login only, and
// cardinal's key restricted to the machine. The drop-in goes in before the install, so the sshd
// that apt starts never runs with Debian's defaults (password login, every address).
func CardinalHost(ctx context.Context, e *Env, d HostDoc) (CardinalReport, error) {
	var r CardinalReport
	if err := validHostDoc(d); err != nil {
		return r, err
	}
	if e.Distro.Manager != host.Apt {
		return r, fmt.Errorf("cardinal's hosts come from flocc host create, which makes Debian VMs; this host is %s", e.Distro.ID)
	}
	confChanged, err := writeIfChanged(e.Path(sshdDropIn), []byte(sshdConf(d.Addr)), 0o644)
	if err != nil {
		return r, err
	}
	sysChanged, err := writeIfChanged(e.Path(sysctlFile), []byte(nonlocalBindOn), 0o644)
	if err != nil {
		return r, err
	}
	// sshd can start before DHCP brings the address at boot; nonlocal bind lets it bind anyway.
	if sysChanged {
		if _, err := e.Must(ctx, "sysctl", "-p", sysctlFile); err != nil {
			return r, err
		}
	}
	if !e.Ok(ctx, "test", "-x", "/usr/sbin/sshd") {
		if _, err := e.MustCmd(ctx, host.Cmd{Name: "apt-get", Args: []string{"install", "-y", "openssh-server"}, Env: []string{"DEBIAN_FRONTEND=noninteractive"}}); err != nil {
			return r, err
		}
		if err := writeAtomic(e.Path(sshInstalled), nil, 0o644); err != nil {
			return r, err
		}
		r.Installed = true
	}
	hk, err := os.ReadFile(e.Path(hostKeyFile))
	if err != nil {
		return r, err
	}
	f := strings.Fields(string(hk))
	if len(f) < 2 {
		return r, fmt.Errorf("%s holds no key", hostKeyFile)
	}
	r.HostKey = d.Addr + " " + f[0] + " " + f[1]
	if _, err := e.Must(ctx, "sshd", "-t"); err != nil {
		return r, err
	}
	if confChanged || r.Installed {
		if _, err := e.Must(ctx, "systemctl", "restart", "ssh.service"); err != nil {
			return r, err
		}
	}
	if _, err := e.Must(ctx, "systemctl", "enable", "--now", "ssh.service"); err != nil {
		return r, err
	}
	r.Steps = append(r.Steps, ReportStep{Name: "sshd", Detail: "on " + d.Addr + ", key login only", Done: true})
	if d.Key == "" {
		r.Steps = append(r.Steps, ReportStep{Name: "Key line", Detail: "waits for cardinal's key", Pending: true})
		return r, nil
	}
	if err := os.MkdirAll(e.Path("/root/.ssh"), 0o700); err != nil {
		return r, err
	}
	old, _ := os.ReadFile(e.Path(authorizedKeys))
	merged := MergeAuthorizedKeys(old, KeyLine(d.Gateway, d.Key), append([]string{d.Key}, d.OldKeys...))
	if _, err := writeIfChanged(e.Path(authorizedKeys), merged, 0o600); err != nil {
		return r, err
	}
	if _, err := writeIfChanged(e.Path(filepath.Join(installsDir, d.Install)), []byte(d.Key+"\n"), 0o644); err != nil {
		return r, err
	}
	r.Steps = append(r.Steps, ReportStep{Name: "Key line", Detail: "from " + d.Gateway + " only", Done: true})
	return r, nil
}

// CardinalHostRemove takes this install's access off the host. The shared pieces go only with the
// last install, and sshd itself only when setup installed it: a hand-installed sshd keeps running,
// so its drop-in stays, or it would listen everywhere with password login at its next restart.
func CardinalHostRemove(ctx context.Context, e *Env, d HostDoc) (CardinalReport, error) {
	var r CardinalReport
	if !installName.MatchString(d.Install) {
		return r, fmt.Errorf("install %q is not <user>@<machine>", d.Install)
	}
	keys := slices.Clone(d.OldKeys)
	if d.Key != "" {
		keys = append(keys, d.Key)
	}
	record := e.Path(filepath.Join(installsDir, d.Install))
	if b, err := os.ReadFile(record); err == nil {
		keys = append(keys, strings.TrimSpace(string(b)))
	}
	if old, err := os.ReadFile(e.Path(authorizedKeys)); err == nil {
		if _, err := writeIfChanged(e.Path(authorizedKeys), MergeAuthorizedKeys(old, "", keys), 0o600); err != nil {
			return r, err
		}
	}
	if err := os.Remove(record); err != nil && !errors.Is(err, os.ErrNotExist) {
		return r, err
	}
	r.Steps = append(r.Steps, ReportStep{Name: "Key line", Detail: "removed", Done: true})
	left, _ := filepath.Glob(e.Path(filepath.Join(installsDir, "*")))
	_, markerErr := os.Stat(e.Path(sshInstalled))
	switch {
	case len(left) > 0:
		r.Notes = append(r.Notes, fmt.Sprintf("%s still used by another install; sshd left as it is", plural(len(left), "install record")))
	case markerErr != nil:
		r.Notes = append(r.Notes, "sshd was not installed by setup, so it keeps running with cardinal.conf")
	default:
		if _, err := e.Must(ctx, "systemctl", "disable", "--now", "ssh.service", "ssh.socket"); err != nil {
			return r, err
		}
		for _, p := range []string{sshdDropIn, sysctlFile, sshInstalled} {
			if err := os.Remove(e.Path(p)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return r, err
			}
		}
		r.Notes = append(r.Notes, "ssh stopped and disabled; openssh-server and its host keys stay")
	}
	return r, nil
}
