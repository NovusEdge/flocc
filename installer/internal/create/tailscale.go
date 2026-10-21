package create

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/tailnet"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

const (
	// renameWait is how many seconds Apply waits for the control server to confirm a new hostname.
	renameWait = 15
	// settleWait is how many seconds Apply waits for the VM's tailscaled to finish starting.
	settleWait = 15
	// KeyTTL is how long a minted key lives; it is used within seconds of being minted.
	KeyTTL = 10 * time.Minute
)

// errTagNotPermitted is the step's failure when the policy does not define the host tag.
var errTagNotPermitted = errors.New("the tailnet policy lacks " + tailnet.Tag + "; run flocc setup (Tailnet section)")

type tsVM struct {
	state, dnsName string
	key            string // the VM's node public key, the key this desktop's Peer map uses for it
	ssh            bool
	tags           []string
}

func (e *Env) vmTailscale(ctx context.Context) tsVM {
	name := e.Spec.Name
	var st struct {
		BackendState string
		Self         struct {
			DNSName, PublicKey string
			Tags               []string
		}
	}
	var p struct{ RunSSH bool }
	json.Unmarshal([]byte(e.out(ctx, e.incus("exec", name, "--", "tailscale", "status", "--json"))), &st)
	json.Unmarshal([]byte(e.out(ctx, e.incus("exec", name, "--", "tailscale", "debug", "prefs"))), &p)
	return tsVM{state: st.BackendState, dnsName: st.Self.DNSName, key: st.Self.PublicKey, ssh: p.RunSSH, tags: st.Self.Tags}
}

func starting(state string) bool { return state == "" || state == "NoState" || state == "Starting" }

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
	// An install cut off mid-dpkg leaves the binary without a running tailscaled, and apt refuses
	// to install anything until dpkg --configure -a finishes the interrupted run.
	if !e.ok(ctx, e.incus("exec", name, "--", "sh", "-c", "command -v tailscale && systemctl is-active --quiet tailscaled")) {
		if _, err := e.must(ctx, e.incus("exec", name, "--", "sh", "-c", "dpkg --configure -a && curl -fsSL https://tailscale.com/install.sh | sh && systemctl enable --now tailscaled")); err != nil {
			return err
		}
	}
	// After a boot or the install, tailscaled does not answer for a moment, then reports NoState or
	// Starting before it settles on Running, Stopped or NeedsLogin.
	ts := e.vmTailscale(ctx)
	for i := 0; starting(ts.state); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i == settleWait {
			if ts.state == "" {
				return fmt.Errorf("tailscaled in the VM does not answer after %ds; see %s", settleWait, e.incusLine("exec "+name+" -- systemctl status tailscaled"))
			}
			return fmt.Errorf("tailscaled in the VM is still %s after %ds; check the VM's network, then run create again", ts.state, settleWait)
		}
		e.waiting("waiting for tailscaled in the VM", time.Duration(settleWait-i)*time.Second)
		e.Sleep(time.Second)
		ts = e.vmTailscale(ctx)
	}
	if ts.state == "Running" {
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
			e.waiting("waiting for the control server to confirm the name", time.Duration(renameWait-i)*time.Second)
			e.Sleep(time.Second)
		}
	}
	// A VM that has joined before is its own offline peer here while Stopped or NeedsLogin.
	taken, err := tailnetHasExcept(ctx, e, name, ts.key)
	if err != nil {
		return err
	}
	if taken {
		return tailnetTaken(name)
	}
	// --ssh on the first up: once RunSSH is on, up refuses to run without it. up refuses a later run
	// that leaves out a non-default flag it was given before, so every run names the tag.
	up := []string{"exec", name, "--", "tailscale", "up", "--ssh", "--hostname=" + name, "--advertise-tags=" + tailnet.Tag}
	if ts.state == "Stopped" {
		// tailscale down keeps the login, so up rejoins as the same node without a key. Attached,
		// in case the control server asks for a login after all.
		return e.M.Interactive(ctx, e.attached(up...))
	}
	if e.Tailnet != nil {
		return e.joinWithMintedKey(ctx, up)
	}
	key, err := e.UI.Secret(ui.Question{
		Title: "Tailscale auth key for " + name + " (optional)",
		Help:  "Paste a one-off auth key, or leave this empty to log in with the QR code or link that follows. The key is used once and not stored.",
	})
	if err != nil {
		return err
	}
	// Fetching a key can take longer than ControlPersist, and the captured up below would then
	// open a BatchMode connection that waits unseen on a check-mode URL.
	if err := e.connect(ctx); err != nil {
		return err
	}
	if key = strings.TrimSpace(key); key == "" {
		err = e.M.Interactive(ctx, e.attached(append(up, "--qr")...))
	} else {
		err = e.upWithKey(ctx, up, key)
	}
	if err != nil {
		return tagHint(err)
	}
	// A node that came up untagged keeps the key expiry of its owner.
	if !slices.Contains(e.vmTailscale(ctx).tags, tailnet.Tag) {
		e.UI.Todo("Tailscale key expiry: open https://login.tailscale.com/admin/machines, find " + name + " and choose Disable key expiry. An untagged node's key otherwise expires after 180 days.")
	}
	return nil
}

// joinWithMintedKey joins the VM tagged with a one-off key from the OAuth client, with no terminal.
func (e *Env) joinWithMintedKey(ctx context.Context, up []string) error {
	key, err := e.Tailnet.TaggedKey(ctx, tailnet.Tag, KeyTTL)
	if errors.Is(err, tailnet.ErrTagNotPermitted) {
		return errTagNotPermitted
	}
	if err != nil {
		return fmt.Errorf("minting a tagged key: %w", err)
	}
	// Minting can take longer than ControlPersist, as a pasted key's prompt can.
	if err := e.connect(ctx); err != nil {
		return err
	}
	return tagHint(e.upWithKey(ctx, up, key))
}

// upWithKey runs up with key on stdin, never in an argument. The key is registered with the log
// before the first command that can print it, and scrubbed from the error.
func (e *Env) upWithKey(ctx context.Context, up []string, key string) error {
	e.Log.Add(key)
	c := e.incus(append(up, "--auth-key=file:/dev/stdin")...)
	c.Stdin = strings.NewReader(key)
	if _, err := e.must(ctx, c); err != nil {
		return errors.New(strings.ReplaceAll(err.Error(), key, "<auth key>"))
	}
	return nil
}

// tagHint turns tailscale's refusal of the tag into the step to take. tailscale up names the
// rejected tags in its output; that text is not pinned down yet (see the tailnet spec).
func tagHint(err error) error {
	// The error carries the command line, which names --advertise-tags itself.
	if err != nil && strings.Contains(strings.ReplaceAll(err.Error(), "--advertise-tags", ""), "tags") {
		return errTagNotPermitted
	}
	return err
}

// attached is the incus command for a tailscale login that draws a QR code or link on the terminal.
func (e *Env) attached(args ...string) host.Cmd {
	c := e.incus(args...)
	c.Why = "tailscale login in the VM"
	return c
}
