package create

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

const (
	dockerUnitName = "flocc-docker-user.service"
	dockerUnitPath = "/etc/systemd/system/" + dockerUnitName
)

// dockerReturns hand traffic into Docker's bridges back to Docker's own chains, which admit only
// published ports. Below them, dockerAccepts would open every container's ports to the VM.
var dockerReturns = [][]string{
	{"-i", Bridge, "-o", "docker0", "-j", "RETURN"},
	{"-i", Bridge, "-o", "br-+", "-j", "RETURN"},
}

// dockerAccepts let the VM through the DROP policy Docker puts on FORWARD, scoped to the bridge,
// as the Incus firewall docs give them.
var dockerAccepts = [][]string{
	{"-i", Bridge, "-j", "ACCEPT"},
	{"-o", Bridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
}

// dockerRules is DOCKER-USER's wanted order, top first.
var dockerRules = append(slices.Clone(dockerReturns), dockerAccepts...)

// dockerUnit re-adds the rules after every Docker start (PartOf restarts it with Docker). It has
// no ExecStop: a stopped Docker leaves FORWARD at DROP, so removing the rules then would cut the
// VM off. systemd expands $name in Exec lines, so the shell's variables are $$t and $$m. ip6tables
// is skipped where Docker made no IPv6 DOCKER-USER chain. Any rule left out fails the unit, whose
// status is all a boot leaves to see.
//
// -I puts a rule at the top, so the rules go in bottom first, and once one has gone in, every rule
// above it is deleted and inserted again: an ACCEPT added under RETURNs already there would
// otherwise sit above them.
func dockerUnit() string {
	var start []string
	for i := len(dockerRules) - 1; i >= 0; i-- {
		r := strings.Join(dockerRules[i], " ")
		start = append(start, "[ $$m = 0 ] || $$t -D DOCKER-USER "+r+" 2>/dev/null; "+
			"[ $$m = 0 ] && $$t -C DOCKER-USER "+r+" 2>/dev/null || { $$t -I DOCKER-USER "+r+" && m=1 || rc=1; }")
	}
	loop := "rc=0; for t in iptables ip6tables; do " +
		"[ $$t = iptables ] || $$t -L DOCKER-USER -n >/dev/null 2>&1 || continue; m=0; "
	return "[Unit]\n" +
		"Description=Let Incus VMs on " + Bridge + " through Docker's forward drop (flocc)\n" +
		"After=docker.service\n" +
		"PartOf=docker.service\n" +
		"\n[Service]\n" +
		"Type=oneshot\n" +
		"RemainAfterExit=yes\n" +
		"ExecStart=/bin/sh -c '" + loop + strings.Join(start, "; ") + "; done; exit $$rc'\n" +
		"\n[Install]\n" +
		"WantedBy=docker.service\n"
}

const daemonJSON = "/etc/docker/daemon.json"

// dockerBackend is the firewall-backend key of daemon.json, read from the file because docker info
// needs a running daemon. A Docker without the key uses iptables; the nftables backend arrived
// with 29.0.
func dockerBackend(daemon string) string {
	var cfg struct {
		Backend string `json:"firewall-backend"`
	}
	if json.Unmarshal([]byte(daemon), &cfg) != nil || cfg.Backend == "" {
		return "iptables"
	}
	return cfg.Backend
}

type Docker struct{}

func (Docker) Name() string { return "Docker" }

// backend reads daemon.json as root: it may be 0600. With sudo -n working, a failed read means
// the file is absent, which is Docker's iptables default.
func (d Docker) backend(ctx context.Context, e *Env) string {
	return dockerBackend(e.out(ctx, e.root(cmd("cat", daemonJSON))))
}

func (d Docker) Check(ctx context.Context, e *Env) (bool, string, error) {
	running := e.ok(ctx, cmd("systemctl", "is-active", "--quiet", "docker.service"))
	// A stopped or socket-activated Docker still sets FORWARD to DROP at its next start.
	if !running && !e.ok(ctx, cmd("systemctl", "is-enabled", "--quiet", "docker.service")) &&
		!e.ok(ctx, cmd("systemctl", "is-active", "--quiet", "docker.socket")) {
		return true, "Docker not running", nil
	}
	if err := e.needRoot("Reading Docker's firewall rules"); err != nil {
		return false, "", err
	}
	backend := d.backend(ctx, e)
	if strings.HasPrefix(backend, "nftables") {
		return false, "Docker uses the nftables firewall backend", nil
	}
	// sudo's umask is the owner's ORed with 022, so under umask 077 tee leaves the unit 0600.
	if e.out(ctx, e.root(cmd("cat", dockerUnitPath))) != strings.TrimSpace(dockerUnit()) {
		return false, dockerUnitPath + " missing or out of date", nil
	}
	if !e.ok(ctx, cmd("systemctl", "is-enabled", "--quiet", dockerUnitName)) {
		return false, dockerUnitName + " not enabled", nil
	}
	if !running {
		return true, "Docker stopped; " + dockerUnitName + " adds the rules at its next start", nil
	}
	for _, tool := range []string{"iptables", "ip6tables"} {
		// Only where Docker made an IPv6 DOCKER-USER chain, as the unit does.
		if tool == "ip6tables" && !e.ok(ctx, e.root(cmd(tool, "-L", "DOCKER-USER", "-n"))) {
			continue
		}
		for _, r := range dockerRules {
			if !e.ok(ctx, e.root(cmd(tool, append([]string{"-C", "DOCKER-USER"}, r...)...))) {
				return false, tool + " DOCKER-USER lacks " + strings.Join(r, " "), nil
			}
		}
		// -S prints rules as iptables-save does. A rule that -C finds but -S spells another way is
		// left out of the order check rather than failing a chain that may well be right.
		rules := strings.Split(e.out(ctx, e.root(cmd(tool, "-S", "DOCKER-USER"))), "\n")
		at := func(r []string) int { return slices.Index(rules, "-A DOCKER-USER "+strings.Join(r, " ")) }
		accept := at(dockerAccepts[0])
		for _, r := range dockerReturns {
			if accept >= 0 && at(r) > accept {
				return false, tool + " DOCKER-USER has " + strings.Join(dockerAccepts[0], " ") + " above " +
					strings.Join(r, " ") + "; delete that ACCEPT and run create again", nil
			}
		}
	}
	return true, "DOCKER-USER lets " + Bridge + " through (" + backend + " backend)", nil
}

func (d Docker) Apply(ctx context.Context, e *Env) error {
	if strings.HasPrefix(d.backend(ctx, e), "nftables") {
		return errors.New("Docker on " + e.Spec.Machine + " uses the nftables firewall backend, which has no DOCKER-USER chain; create supports only Docker's iptables backend")
	}
	running := e.ok(ctx, cmd("systemctl", "is-active", "--quiet", "docker.service"))
	tee := e.root(cmd("tee", dockerUnitPath))
	tee.Stdin = strings.NewReader(dockerUnit())
	if _, err := e.must(ctx, tee); err != nil {
		return err
	}
	// Only this oneshot is restarted. docker.service never is: on 2026-10-20 a Docker restart
	// killed another session's image build on the shared laptop. With Docker stopped the oneshot
	// stays inactive: once active it would not run again at Docker's start.
	steps := [][]string{{"daemon-reload"}, {"enable", dockerUnitName}}
	if running {
		steps = append(steps, []string{"restart", dockerUnitName})
	}
	for _, args := range steps {
		if _, err := e.must(ctx, e.root(cmd("systemctl", args...))); err != nil {
			return err
		}
	}
	return nil
}
