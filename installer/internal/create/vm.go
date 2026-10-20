package create

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

const (
	vmPoll    = 5 * time.Second
	vmPolls   = 60
	egressURL = "https://pkgs.tailscale.com/"
)

type instance struct {
	Status string            `json:"status"`
	Config map[string]string `json:"config"`
}

func (e *Env) instance(ctx context.Context) (instance, bool) {
	var in instance
	raw := e.out(ctx, e.incus("query", "/1.0/instances/"+e.Spec.Name))
	return in, raw != "" && json.Unmarshal([]byte(raw), &in) == nil
}

// ipv4 is the VM's first global IPv4 address, or "" before DHCP. The agent reports the guest's
// own interface names (enp5s0 on Debian), so none is assumed.
func (e *Env) ipv4(ctx context.Context) string {
	var st struct {
		Network map[string]struct {
			Addresses []struct{ Family, Address, Scope string } `json:"addresses"`
		} `json:"network"`
	}
	if json.Unmarshal([]byte(e.out(ctx, e.incus("query", "/1.0/instances/"+e.Spec.Name+"/state"))), &st) != nil {
		return ""
	}
	for _, n := range slices.Sorted(maps.Keys(st.Network)) {
		for _, a := range st.Network[n].Addresses {
			if n != "lo" && a.Family == "inet" && a.Scope == "global" {
				return a.Address
			}
		}
	}
	return ""
}

type VM struct{}

func (VM) Name() string { return "VM" }

func (VM) Check(ctx context.Context, e *Env) (bool, string, error) {
	in, ok := e.instance(ctx)
	switch {
	case !ok:
		return false, "no instance " + e.Spec.Name, nil
	case in.Status != "Running":
		return false, e.Spec.Name + " is " + strings.ToLower(in.Status), nil
	case in.Config["boot.autostart"] != "true":
		return false, "boot.autostart is off", nil
	}
	ip := e.ipv4(ctx)
	if ip == "" || !e.ok(ctx, e.incus("exec", e.Spec.Name, "--", "true")) {
		return false, "no IPv4 address or agent yet", nil
	}
	return true, e.Spec.Name + " running at " + ip, nil
}

func (VM) Apply(ctx context.Context, e *Env) error {
	name := e.Spec.Name
	in, exists := e.instance(ctx)
	if !exists {
		taken, err := tailnetHas(ctx, e, name)
		if err != nil {
			return err
		}
		if taken {
			return tailnetTaken(name)
		}
		if _, err := e.must(ctx, e.incus("launch", Image, name, "--vm", "--network", Bridge,
			"-c", "boot.autostart=true",
			"-c", fmt.Sprintf("limits.cpu=%d", e.Spec.CPUs),
			"-c", fmt.Sprintf("limits.memory=%dGiB", e.Spec.MemoryGiB),
			"-d", fmt.Sprintf("root,size=%dGiB", e.Spec.DiskGiB))); err != nil {
			return err
		}
	} else {
		if in.Config["boot.autostart"] != "true" {
			if _, err := e.must(ctx, e.incus("config", "set", name, "boot.autostart=true")); err != nil {
				return err
			}
		}
		if in.Status != "Running" {
			if _, err := e.must(ctx, e.incus("start", name)); err != nil {
				return err
			}
		}
	}
	for range vmPolls {
		// A cancelled ctx makes every probe fail like "no address yet"; without this the loop would
		// sleep out the full five minutes after Ctrl-C.
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.ipv4(ctx) != "" && e.ok(ctx, e.incus("exec", name, "--", "true")) {
			return nil
		}
		e.Sleep(vmPoll)
	}
	return fmt.Errorf("%s has no IPv4 address after %s; see incus console %s --show-log", name, vmPoll*vmPolls, name)
}

type Egress struct{}

func (Egress) Name() string { return "Egress" }

func (Egress) Check(ctx context.Context, e *Env) (bool, string, error) {
	if e.ok(ctx, e.incus("exec", e.Spec.Name, "--", "curl", "-fsS", "-o", "/dev/null", "--max-time", "15", egressURL)) {
		return true, "HTTPS reaches " + egressURL, nil
	}
	return false, "no HTTPS from the VM; see \"Making a host with flocc host create\" in docs/vm-setup.md", nil
}

func (Egress) Apply(ctx context.Context, e *Env) error {
	name := e.Spec.Name
	if !e.ok(ctx, e.incus("exec", name, "--", "sh", "-c", "command -v curl")) {
		for _, args := range [][]string{{"apt-get", "update", "-q"}, {"apt-get", "install", "-y", "-q", "curl", "ca-certificates"}} {
			if _, err := e.must(ctx, e.incus(append([]string{"exec", name, "--env", "DEBIAN_FRONTEND=noninteractive", "--"}, args...)...)); err != nil {
				return fmt.Errorf("the VM cannot install curl, so it likely has no way out; see \"Making a host with flocc host create\" in docs/vm-setup.md: %w", err)
			}
		}
	}
	// DHCP can hand out the address before the VM's resolver answers.
	for range 6 {
		if ok, _, _ := (Egress{}).Check(ctx, e); ok {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e.Sleep(vmPoll)
	}
	return nil
}
