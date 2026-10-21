package create

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// fwRebindTries is how many seconds Apply waits after its reload for Docker to bind its bridges.
const fwRebindTries = 10

const (
	fwZone     = "flocc-incus"
	fwOut      = "flocc-incus-out"
	fwNoDocker = "flocc-no-docker"
	dockerZone = "docker"
	dockerFwd  = "docker-forwarding"
)

// fwServices is what the VM may reach on the machine itself: Incus's dnsmasq.
var fwServices = []string{"dhcp", "dns"}

type fwState struct {
	Zones, Policies        []string
	BridgeZone             string
	Services, Ports        []string
	OutIn, OutEg           []string
	OutTarget              string
	NoDockerIn, NoDockerEg []string
	NoDockerTarget         string
	NoDockerPriority       int
	DockerFwdPriority      int
}

func readFirewall(ctx context.Context, e *Env, permanent bool) fwState {
	q := func(args ...string) string {
		if permanent {
			args = append([]string{"--permanent"}, args...)
		}
		return e.out(ctx, e.root(cmd("firewall-cmd", args...)))
	}
	f := strings.Fields
	s := fwState{Zones: f(q("--get-zones")), Policies: f(q("--get-policies")), BridgeZone: q("--get-zone-of-interface=" + Bridge)}
	if slices.Contains(s.Zones, fwZone) {
		s.Services = f(q("--zone="+fwZone, "--list-services"))
		s.Ports = f(q("--zone="+fwZone, "--list-ports"))
	}
	// firewall-cmd 2.5 answers --get-target and --get-priority for a policy only with --permanent;
	// --info-policy carries both in either configuration.
	if slices.Contains(s.Policies, fwOut) {
		s.OutIn = f(q("--policy="+fwOut, "--list-ingress-zones"))
		s.OutEg = f(q("--policy="+fwOut, "--list-egress-zones"))
		s.OutTarget, _ = policyInfo(q("--info-policy=" + fwOut))
	}
	if slices.Contains(s.Policies, fwNoDocker) {
		s.NoDockerIn = f(q("--policy="+fwNoDocker, "--list-ingress-zones"))
		s.NoDockerEg = f(q("--policy="+fwNoDocker, "--list-egress-zones"))
		s.NoDockerTarget, s.NoDockerPriority = policyInfo(q("--info-policy=" + fwNoDocker))
	}
	if slices.Contains(s.Policies, dockerFwd) {
		_, s.DockerFwdPriority = policyInfo(q("--info-policy=" + dockerFwd))
	}
	return s
}

// policyInfo reads the target and priority lines of firewall-cmd --info-policy.
func policyInfo(out string) (target string, priority int) {
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), ":")
		if !ok {
			continue
		}
		switch v = strings.TrimSpace(v); k {
		case "target":
			target = v
		case "priority":
			priority, _ = strconv.Atoi(v)
		}
	}
	return target, priority
}

// readBoth reads the runtime and the permanent configuration. Docker's zone counts as present in
// both when either has it, so the two checks cannot disagree about wanting flocc-no-docker. Docker
// creates the zone permanently today; a runtime-only zone ends at firewall-cmd refusing to name it.
func readBoth(ctx context.Context, e *Env) (runtime, permanent fwState) {
	runtime, permanent = readFirewall(ctx, e, false), readFirewall(ctx, e, true)
	if slices.Contains(runtime.Zones, dockerZone) || slices.Contains(permanent.Zones, dockerZone) {
		for _, s := range []*fwState{&runtime, &permanent} {
			if !slices.Contains(s.Zones, dockerZone) {
				s.Zones = append(s.Zones, dockerZone)
			}
		}
	}
	return runtime, permanent
}

// noDockerPriority runs the reject ahead of docker-forwarding, which accepts forwarding from
// every zone into Docker's bridges, published ports included. Lower values run first.
func noDockerPriority(s fwState) int { return min(-100, s.DockerFwdPriority-1) }

func fwProblems(s fwState, up string) []string {
	if !slices.Contains(s.Zones, fwZone) {
		return []string{"no zone " + fwZone}
	}
	var p []string
	if s.BridgeZone != fwZone {
		p = append(p, fmt.Sprintf("%s is in zone %q", Bridge, s.BridgeZone))
	}
	if !sameSet(s.Services, fwServices) || len(s.Ports) > 0 {
		p = append(p, fmt.Sprintf("zone %s allows %q, not only dhcp and dns", fwZone, slices.Concat(s.Services, s.Ports)))
	}
	if !sameSet(s.OutIn, []string{fwZone}) || !sameSet(s.OutEg, []string{up}) || s.OutTarget != "ACCEPT" {
		p = append(p, fmt.Sprintf("policy %s does not forward %s to %s", fwOut, fwZone, up))
	}
	if slices.Contains(s.Zones, dockerZone) && (!sameSet(s.NoDockerIn, []string{fwZone}) || !sameSet(s.NoDockerEg, []string{dockerZone}) ||
		s.NoDockerTarget != "REJECT" || s.NoDockerPriority > noDockerPriority(s)) {
		p = append(p, fmt.Sprintf("policy %s does not keep %s out of Docker's networks", fwNoDocker, fwZone))
	}
	return p
}

// fwFixes are the firewall-cmd argument lists that turn the permanent configuration s into the
// wanted one. Each is permanent; Apply reloads once after them.
func fwFixes(s fwState, up string) [][]string {
	var c [][]string
	perm := func(args ...string) { c = append(c, append([]string{"--permanent"}, args...)) }
	if !slices.Contains(s.Zones, fwZone) {
		perm("--new-zone=" + fwZone)
	}
	for _, svc := range s.Services {
		if !slices.Contains(fwServices, svc) {
			perm("--zone="+fwZone, "--remove-service="+svc)
		}
	}
	for _, svc := range fwServices {
		if !slices.Contains(s.Services, svc) {
			perm("--zone="+fwZone, "--add-service="+svc)
		}
	}
	for _, port := range s.Ports {
		perm("--zone="+fwZone, "--remove-port="+port)
	}
	if s.BridgeZone != fwZone {
		perm("--zone="+fwZone, "--change-interface="+Bridge)
	}
	c = append(c, fixPolicy(fwOut, slices.Contains(s.Policies, fwOut), s.OutIn, s.OutEg, s.OutTarget, fwZone, up, "ACCEPT")...)
	if slices.Contains(s.Zones, dockerZone) {
		c = append(c, fixPolicy(fwNoDocker, slices.Contains(s.Policies, fwNoDocker), s.NoDockerIn, s.NoDockerEg, s.NoDockerTarget, fwZone, dockerZone, "REJECT")...)
		// A policy that does not exist yet reads as priority 0, above any wanted value.
		if want := noDockerPriority(s); s.NoDockerPriority > want {
			perm("--policy="+fwNoDocker, "--set-priority="+strconv.Itoa(want))
		}
	}
	return c
}

func fixPolicy(name string, exists bool, in, eg []string, target, wantIn, wantEg, wantTarget string) [][]string {
	var c [][]string
	set := func(arg string) { c = append(c, []string{"--permanent", "--policy=" + name, arg}) }
	if !exists {
		c = append(c, []string{"--permanent", "--new-policy=" + name})
	}
	for _, z := range in {
		if z != wantIn {
			set("--remove-ingress-zone=" + z)
		}
	}
	if !slices.Contains(in, wantIn) {
		set("--add-ingress-zone=" + wantIn)
	}
	for _, z := range eg {
		if z != wantEg {
			set("--remove-egress-zone=" + z)
		}
	}
	if !slices.Contains(eg, wantEg) {
		set("--add-egress-zone=" + wantEg)
	}
	if target != wantTarget {
		set("--set-target=" + wantTarget)
	}
	return c
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}

// zoneOf is the runtime zone that handles dev's traffic, or "" when firewalld names none.
func zoneOf(ctx context.Context, e *Env, dev string) string {
	if z := e.out(ctx, e.root(cmd("firewall-cmd", "--get-zone-of-interface="+dev))); z != "" && z != "no zone" {
		return z
	}
	// An interface bound to no zone falls in the default zone.
	return e.out(ctx, e.root(cmd("firewall-cmd", "--get-default-zone")))
}

// uplinkZone is the firewalld zone of the default route's interface, where the VM may forward to.
func uplinkZone(ctx context.Context, e *Env) (string, error) {
	var routes []struct {
		Dev string `json:"dev"`
	}
	if json.Unmarshal([]byte(e.out(ctx, cmd("ip", "-j", "route", "show", "default"))), &routes) != nil || len(routes) == 0 || routes[0].Dev == "" {
		return "", fmt.Errorf("%s has no default route", e.Spec.Machine)
	}
	if z := zoneOf(ctx, e, routes[0].Dev); z != "" {
		return z, nil
	}
	return "", fmt.Errorf("firewalld names no zone for %s", routes[0].Dev)
}

const firewalldConf = "/etc/firewalld/firewalld.conf"

// fwBackend is firewalld.conf's FirewallBackend; firewalld defaults to nftables since 0.6.
func fwBackend(conf string) string {
	for line := range strings.Lines(conf) {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(k) == "FirewallBackend" {
			return strings.TrimSpace(v)
		}
	}
	return "nftables"
}

func (Firewalld) iptables(ctx context.Context, e *Env) bool {
	return strings.EqualFold(fwBackend(e.out(ctx, e.root(cmd("cat", firewalldConf)))), "iptables")
}

// dockerBridge matches the bridges Docker makes: docker0, and br- with a network ID's first 12 hex
// digits for each user-defined network.
var dockerBridge = regexp.MustCompile(`^(docker0|br-[0-9a-f]{12})$`)

// dockerBridgeProblem names a running Docker's bridge that is outside zone docker, where
// flocc-no-docker does not reach it. Docker binds its bridges at runtime only, and again after
// each reload; with Docker stopped no container runs, and Docker binds them when it starts.
func dockerBridgeProblem(ctx context.Context, e *Env) (string, error) {
	if !e.ok(ctx, cmd("systemctl", "is-active", "--quiet", "docker.service")) {
		return "", nil
	}
	var links []struct {
		Name string `json:"ifname"`
	}
	if err := json.Unmarshal([]byte(e.out(ctx, cmd("ip", "-j", "link", "show", "type", "bridge"))), &links); err != nil {
		return "", fmt.Errorf("cannot list the bridges on %s: %w", e.Spec.Machine, err)
	}
	for _, l := range links {
		if !dockerBridge.MatchString(l.Name) {
			continue
		}
		if z := zoneOf(ctx, e, l.Name); z != dockerZone {
			return fmt.Sprintf("Docker's bridge %s is in zone %s, so flocc-no-docker does not cover it and the VM reaches its containers. "+
				"Create leaves Docker alone: bind it with sudo firewall-cmd --zone=docker --change-interface=%s, which lasts until the next reload, "+
				"or restart Docker when nothing depends on it, then run create again", l.Name, cmp.Or(z, "none"), l.Name), nil
		}
	}
	return "", nil
}

type Firewalld struct{}

func (Firewalld) Name() string { return "Firewalld" }

func (f Firewalld) Check(ctx context.Context, e *Env) (bool, string, error) {
	if !e.ok(ctx, cmd("systemctl", "is-active", "--quiet", "firewalld.service")) {
		return true, "firewalld not running; nothing keeps the VM off this machine's services", nil
	}
	if err := e.needRoot("Reading firewalld"); err != nil {
		return false, "", err
	}
	if f.iptables(ctx, e) {
		return false, "firewalld uses the iptables backend", nil
	}
	up, err := uplinkZone(ctx, e)
	if err != nil {
		return false, "", err
	}
	runtime, permanent := readBoth(ctx, e)
	for _, c := range []struct {
		where string
		s     fwState
	}{{"runtime", runtime}, {"permanent", permanent}} {
		if p := fwProblems(c.s, up); len(p) > 0 {
			return false, c.where + ": " + strings.Join(p, "; "), nil
		}
	}
	if p, err := dockerBridgeProblem(ctx, e); err != nil || p != "" {
		return false, p, err
	}
	detail := fmt.Sprintf("%s in zone %s (dhcp, dns), forwarded only to zone %s", Bridge, fwZone, up)
	// Forwarded traffic that leaves through tailscale0 is masqueraded as the machine.
	if e.ok(ctx, cmd("ip", "link", "show", "dev", "tailscale0")) && zoneOf(ctx, e, "tailscale0") == up {
		detail += "; tailscale0 is in the same zone, so the VM can also leave through this machine's tailnet"
	}
	return true, detail, nil
}

func (f Firewalld) Apply(ctx context.Context, e *Env) error {
	if err := e.needRoot("Changing firewalld"); err != nil {
		return err
	}
	// Docker jumps to DOCKER-USER first in FORWARD, and its ACCEPT for incusbr0 then decides before
	// any firewalld rule in iptables; a reload also flushes create's DOCKER-USER rules until Docker
	// restarts.
	if f.iptables(ctx, e) {
		return fmt.Errorf("firewalld on %s uses the iptables backend, where Docker's DOCKER-USER chain accepts the VM's traffic before flocc-no-docker sees it; "+
			"create supports only firewalld's nftables backend: set FirewallBackend=nftables in %s, restart firewalld, then run create again", e.Spec.Machine, firewalldConf)
	}
	up, err := uplinkZone(ctx, e)
	if err != nil {
		return err
	}
	_, permanent := readBoth(ctx, e)
	for _, args := range fwFixes(permanent, up) {
		if _, err := e.must(ctx, e.root(cmd("firewall-cmd", args...))); err != nil {
			return err
		}
	}
	docker := e.ok(ctx, cmd("systemctl", "is-active", "--quiet", "docker.service"))
	// New zones and policies exist only in the permanent configuration until a reload.
	if _, err := e.must(ctx, e.root(cmd("firewall-cmd", "--reload"))); err != nil || !docker {
		return err
	}
	// The reload unbinds Docker's bridges until Docker, told over D-Bus, binds them again; the
	// re-check after Apply would otherwise catch them in between.
	for range fwRebindTries {
		if p, err := dockerBridgeProblem(ctx, e); err != nil || p == "" {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e.Sleep(time.Second)
	}
	return nil
}
