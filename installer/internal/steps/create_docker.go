package steps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	dockerUnitName = "flocc-docker-user.service"
	dockerUnitPath = "/etc/systemd/system/" + dockerUnitName
)

// dockerJump runs the VM's traffic through Docker's own forwarding chain, which admits only
// published ports on every Docker bridge, whatever its name, and returns the rest. Below it,
// dockerAccept would open every container's ports to the VM. Docker has the chain from 28.0 on.
var dockerJump = []string{"-i", IncusBridge, "-j", "DOCKER-FORWARD"}

// dockerAccept and the conntrack rule let the VM through the DROP policy Docker puts on FORWARD,
// scoped to the bridge, as the Incus firewall docs give them.
var dockerAccept = []string{"-i", IncusBridge, "-j", "ACCEPT"}

// dockerRules is DOCKER-USER's wanted order, top first.
var dockerRules = [][]string{
	dockerJump,
	dockerAccept,
	{"-o", IncusBridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
}

// xtArgs puts -w first: it waits for the xtables lock, which Docker and firewalld hold while they
// change rules; without it iptables-legacy fails at once.
func xtArgs(args ...string) []string { return append([]string{"-w"}, args...) }

// dockerUnit re-adds the rules after every Docker start (PartOf restarts it with Docker). It has
// no ExecStop: a stopped Docker leaves FORWARD at DROP, so removing the rules then would cut the
// VM off. systemd expands $name in Exec lines, so the shell's variables are $$t and $$m. ip6tables
// is skipped where Docker made no IPv6 DOCKER-USER chain. Any rule left out fails the unit, whose
// status is all a boot leaves to see. -w is as in xtArgs.
//
// -I puts a rule at the top, so the rules go in bottom first, and once one has gone in, every rule
// above it is deleted and inserted again: an ACCEPT added under a jump already there would
// otherwise sit above it.
func dockerUnit() string {
	var start []string
	for i := len(dockerRules) - 1; i >= 0; i-- {
		r := strings.Join(dockerRules[i], " ")
		start = append(start, "[ $$m = 0 ] || $$t -w -D DOCKER-USER "+r+" 2>/dev/null; "+
			"[ $$m = 0 ] && $$t -w -C DOCKER-USER "+r+" 2>/dev/null || { $$t -w -I DOCKER-USER "+r+" && m=1 || rc=1; }")
	}
	loop := "rc=0; for t in iptables ip6tables; do " +
		"[ $$t = iptables ] || $$t -w -L DOCKER-USER -n >/dev/null 2>&1 || continue; m=0; "
	return "[Unit]\n" +
		"Description=Let Incus VMs on " + IncusBridge + " through Docker's forward drop (flocc)\n" +
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

// backend: a failed read means the file is absent, which is Docker's iptables default.
func (d Docker) backend(e *Env) string {
	b, _ := os.ReadFile(e.Path(daemonJSON))
	return dockerBackend(string(b))
}

func (d Docker) Check(ctx context.Context, e *Env) (bool, string, error) {
	running, present := dockerPresent(ctx, e)
	if !present {
		return true, noDocker, nil
	}
	backend := d.backend(e)
	if strings.HasPrefix(backend, "nftables") {
		return false, "Docker uses the nftables firewall backend", nil
	}
	if running {
		if msg := d.noForward(ctx, e); msg != "" {
			return false, msg, nil
		}
	}
	unit, _ := os.ReadFile(e.Path(dockerUnitPath))
	if strings.TrimSpace(string(unit)) != strings.TrimSpace(dockerUnit()) {
		return false, dockerUnitPath + " missing or out of date", nil
	}
	if !e.Ok(ctx, "systemctl", "is-enabled", "--quiet", dockerUnitName) {
		return false, dockerUnitName + " not enabled", nil
	}
	if !running {
		return true, "Docker stopped; " + dockerUnitName + " adds the rules at its next start", nil
	}
	for _, tool := range d.tools(ctx, e) {
		for _, r := range dockerRules {
			if !e.Ok(ctx, tool, xtArgs(append([]string{"-C", "DOCKER-USER"}, r...)...)...) {
				return false, tool + " DOCKER-USER lacks " + strings.Join(r, " "), nil
			}
		}
		// -S prints rules as iptables-save does. A rule that -C finds but -S spells another way is
		// left out of the order check rather than failing a chain that may well be right.
		rules := strings.Split(e.Output(ctx, tool, xtArgs("-S", "DOCKER-USER")...), "\n")
		at := func(r []string) int { return slices.Index(rules, "-A DOCKER-USER "+strings.Join(r, " ")) }
		if accept := at(dockerAccept); accept >= 0 && at(dockerJump) > accept {
			return false, tool + " DOCKER-USER has " + strings.Join(dockerAccept, " ") + " above " +
				strings.Join(dockerJump, " ") + "; delete that ACCEPT and run create again", nil
		}
	}
	return true, "DOCKER-USER lets " + IncusBridge + " through (" + backend + " backend)", nil
}

// tools are the iptables commands whose DOCKER-USER chain gets the rules: ip6tables only where
// Docker made an IPv6 one, as the unit does.
func (d Docker) tools(ctx context.Context, e *Env) []string {
	if e.Ok(ctx, "ip6tables", xtArgs("-L", "DOCKER-USER", "-n")...) {
		return []string{"iptables", "ip6tables"}
	}
	return []string{"iptables"}
}

// noForward says why a running Docker cannot take dockerJump, or "" when it can.
func (d Docker) noForward(ctx context.Context, e *Env) string {
	for _, tool := range d.tools(ctx, e) {
		if !e.Ok(ctx, tool, xtArgs("-L", "DOCKER-FORWARD", "-n")...) {
			chain := "DOCKER-FORWARD"
			if tool != "iptables" {
				chain = tool + " " + chain
			}
			return "Docker 28 or newer is needed: " + chain + " chain missing"
		}
	}
	return ""
}

func (d Docker) Apply(ctx context.Context, e *Env) error {
	if strings.HasPrefix(d.backend(e), "nftables") {
		return errors.New("Docker on this machine uses the nftables firewall backend, which has no DOCKER-USER chain; create supports only Docker's iptables backend")
	}
	running := e.Ok(ctx, "systemctl", "is-active", "--quiet", "docker.service")
	if running {
		if msg := d.noForward(ctx, e); msg != "" {
			return errors.New(msg)
		}
	}
	if err := os.MkdirAll(e.Path(filepath.Dir(dockerUnitPath)), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(e.Path(dockerUnitPath), []byte(dockerUnit()), 0o644); err != nil {
		return err
	}
	// Only this oneshot is restarted. docker.service never is: on 2026-10-20 a Docker restart
	// killed another session's image build on the shared laptop. With Docker stopped the oneshot
	// stays inactive: once active it would not run again at Docker's start.
	calls := [][]string{{"daemon-reload"}, {"enable", dockerUnitName}}
	if running {
		calls = append(calls, []string{"restart", dockerUnitName})
	}
	for _, args := range calls {
		if _, err := e.Must(ctx, "systemctl", args...); err != nil {
			return err
		}
	}
	return nil
}
