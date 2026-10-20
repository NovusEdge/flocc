package create

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

const (
	dockerUnitName = "flocc-docker-user.service"
	dockerUnitPath = "/etc/systemd/system/" + dockerUnitName
)

// dockerRules let the VM through the DROP policy Docker puts on FORWARD, scoped to the bridge,
// as the Incus firewall docs give them.
var dockerRules = [][]string{
	{"-i", Bridge, "-j", "ACCEPT"},
	{"-o", Bridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
}

// dockerUnit re-adds the rules after every Docker start (PartOf restarts it with Docker) and
// removes them when Docker stops. systemd expands $name in Exec lines, so the shell's variable is
// $$t. ip6tables is skipped where Docker made no IPv6 DOCKER-USER chain.
func dockerUnit() string {
	var start, stop []string
	for i := len(dockerRules) - 1; i >= 0; i-- {
		r := strings.Join(dockerRules[i], " ")
		start = append(start, "$$t -C DOCKER-USER "+r+" 2>/dev/null || $$t -I DOCKER-USER "+r)
	}
	for _, rule := range dockerRules {
		stop = append(stop, "$$t -D DOCKER-USER "+strings.Join(rule, " ")+" 2>/dev/null")
	}
	loop := "for t in iptables ip6tables; do $$t -L DOCKER-USER -n >/dev/null 2>&1 || continue; "
	return "[Unit]\n" +
		"Description=Let Incus VMs on " + Bridge + " through Docker's forward drop (flocc)\n" +
		"After=docker.service\n" +
		"PartOf=docker.service\n" +
		"\n[Service]\n" +
		"Type=oneshot\n" +
		"RemainAfterExit=yes\n" +
		"ExecStart=/bin/sh -c '" + loop + strings.Join(start, "; ") + "; done'\n" +
		"ExecStop=/bin/sh -c '" + loop + strings.Join(stop, "; ") + "; done; true'\n" +
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

func (d Docker) backend(ctx context.Context, e *Env) string {
	return dockerBackend(e.out(ctx, cmd("cat", daemonJSON)))
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
	if e.out(ctx, cmd("cat", dockerUnitPath)) != strings.TrimSpace(dockerUnit()) {
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
	// Only this oneshot is restarted (ExecStop, then ExecStart). docker.service never is: on
	// 2026-10-20 a Docker restart killed another session's image build on the shared laptop.
	// With Docker stopped the oneshot stays inactive: once active it would not run again at
	// Docker's start.
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
