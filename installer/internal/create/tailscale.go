package create

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// renameWait is how many seconds Apply waits for the control server to confirm a new hostname.
const renameWait = 15

type tsVM struct {
	state, dnsName string
	ssh            bool
}

func (e *Env) vmTailscale(ctx context.Context) tsVM {
	name := e.Spec.Name
	var st struct {
		BackendState string
		Self         struct{ DNSName string }
	}
	var p struct{ RunSSH bool }
	json.Unmarshal([]byte(e.out(ctx, e.incus("exec", name, "--", "tailscale", "status", "--json"))), &st)
	json.Unmarshal([]byte(e.out(ctx, e.incus("exec", name, "--", "tailscale", "debug", "prefs"))), &p)
	return tsVM{state: st.BackendState, dnsName: st.Self.DNSName, ssh: p.RunSSH}
}

type Tailnet struct{}

func (Tailnet) Name() string { return "Tailscale" }

func (Tailnet) Check(ctx context.Context, e *Env) (bool, string, error) {
	ts := e.vmTailscale(ctx)
	label, _, _ := strings.Cut(ts.dnsName, ".")
	switch {
	case ts.state == "":
		return false, "tailscaled not running in the VM", nil
	case ts.state != "Running":
		return false, "state " + ts.state, nil
	case !strings.EqualFold(label, e.Spec.Name):
		return false, "joined as " + label + ", not " + e.Spec.Name, nil
	case !ts.ssh:
		return false, "Tailscale SSH off", nil
	}
	return true, "joined as " + strings.TrimSuffix(ts.dnsName, ".") + ", SSH on", nil
}

func (Tailnet) Apply(ctx context.Context, e *Env) error {
	name := e.Spec.Name
	if !e.ok(ctx, e.incus("exec", name, "--", "sh", "-c", "command -v tailscale")) {
		if _, err := e.must(ctx, e.incus("exec", name, "--", "sh", "-c", "curl -fsSL https://tailscale.com/install.sh | sh")); err != nil {
			return err
		}
	}
	if e.vmTailscale(ctx).state == "Running" {
		// Logged in already, renamed or without SSH; a second login would make a second node.
		if _, err := e.must(ctx, e.incus("exec", name, "--", "tailscale", "set", "--ssh", "--hostname="+name)); err != nil {
			return err
		}
		// set only edits local prefs; the new name comes back in a later netmap from the control
		// server, so status shows the old name for a while.
		for i := 0; ; i++ {
			if label, _, _ := strings.Cut(e.vmTailscale(ctx).dnsName, "."); strings.EqualFold(label, name) {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if i == renameWait {
				// set cannot take a name another node holds: the tailnet still has a stale node of this name.
				return tailnetTaken(name)
			}
			e.Sleep(time.Second)
		}
	}
	taken, err := tailnetHas(ctx, e, name)
	if err != nil {
		return err
	}
	if taken {
		return tailnetTaken(name)
	}
	key, err := e.UI.Secret("tailscale_auth_key", "Tailscale auth key for "+name+" (optional)",
		"Paste a one-off auth key, or leave this empty to log in with the QR code or link that follows. The key is used once and not stored.")
	if err != nil {
		return err
	}
	// --ssh on the first up: once RunSSH is on, up refuses to run without it.
	up := []string{"exec", name, "--", "tailscale", "up", "--ssh", "--hostname=" + name}
	if key = strings.TrimSpace(key); key == "" {
		err = e.M.Interactive(ctx, e.incus(append(up, "--qr")...))
	} else {
		// Before the first command that can print it: the log gets tailscale's output as it comes.
		e.Log.Add(key)
		c := e.incus(append(up, "--auth-key=file:/dev/stdin")...)
		c.Stdin = strings.NewReader(key)
		if _, err = e.must(ctx, c); err != nil {
			err = errors.New(strings.ReplaceAll(err.Error(), key, "<auth key>"))
		}
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Out, "\nTailscale key expiry: open https://login.tailscale.com/admin/machines, find %s and choose Disable key expiry. An untagged node's key otherwise expires after 180 days.\n\n", name)
	return nil
}
