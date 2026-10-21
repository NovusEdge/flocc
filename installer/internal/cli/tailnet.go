package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/tailnet"
)

const (
	policyURL   = "https://login.tailscale.com/admin/acls/file"
	machinesURL = "https://login.tailscale.com/admin/machines"
	// policyRechecks is how many times the owner is asked whether the policy is pasted.
	policyRechecks = 3
)

// tailnetAPI is the part of tailnet.Client that setup uses.
type tailnetAPI interface {
	Policy(ctx context.Context) ([]byte, error)
	SetTags(ctx context.Context, deviceID string, tags []string) error
}

// tailnet is the Tailnet section: it stores the OAuth client if the owner has one, checks the
// policy, and tags each host's node. A host is never tagged before the policy passes, because the
// ssh accept rule for the tag has to exist first or ssh to the host stops when the tag lands.
func (w *wizard) tailnet() {
	w.d.ui.Group("Tailnet")
	st := w.d.ui.Step("Client")
	st.Detail("looking for the OAuth client")
	api, ok := w.ops.tailnetClient()
	// Only the first setup asks: the client is optional, and a later run would otherwise ask again
	// for as long as the owner declines it. flocc keys still offers it.
	if !ok && len(w.ops.hosts()) == 0 {
		if err := w.ops.fillKeys(w.ctx, []keys.Slot{keys.Tailscale}); err != nil {
			w.failAt(st, "Client", err)
			return
		}
		api, ok = w.ops.tailnetClient()
	}
	if ok {
		w.end(st, rowDone, "Client", "stored")
	} else {
		link, page := keys.Page(keys.Tailscale, keys.Mapped{})
		w.end(st, rowSkipped, "Client", "no OAuth client; hosts join with a browser login")
		w.d.ui.Todo("Tailscale OAuth client, to join hosts tagged without a browser login: " + page + " " + link)
	}
	if w.stopped {
		return
	}
	st = w.d.ui.Step("Tailnet nodes")
	st.Detail("reading tailscale status")
	status, err := w.ops.tailscaleStatus(w.ctx)
	var devices []tailnet.Device
	if err == nil {
		devices, err = tailnet.Devices(status)
	}
	if err != nil {
		w.end(st, rowFailed, "Tailnet nodes", err.Error())
		return
	}
	w.end(st, rowDone, "Tailnet nodes", "read from this desktop")
	passed := ok && w.policy(api, devices)
	if w.stopped {
		return
	}
	w.tagHosts(api, passed, devices)
}

// policy checks the tailnet policy and, while it lacks something, writes the merged file for the
// owner to paste and checks again. It reports whether the policy passes.
func (w *wizard) policy(api tailnetAPI, devices []tailnet.Device) bool {
	st := w.d.ui.Step("Policy")
	st.Detail("reading the policy")
	read := func() (tailnet.Missing, []byte, bool) {
		policy, err := api.Policy(w.ctx)
		var missing tailnet.Missing
		if err == nil {
			missing, err = tailnet.Check(policy)
		}
		switch {
		case errors.Is(err, tailnet.ErrRefused):
			w.end(st, rowPending, "Policy", "the client lacks Policy file read; add the scope to the OAuth client, or make a new one")
		case err != nil:
			w.failAt(st, "Policy", err)
		default:
			return missing, policy, true
		}
		return nil, nil, false
	}
	missing, policy, ok := read()
	if !ok {
		return false
	}
	if len(missing) == 0 {
		w.end(st, rowDone, "Policy", "ok")
		return true
	}
	owner := ""
	if len(devices) > 0 {
		owner = devices[0].Owner
	}
	merged, added, err := tailnet.Merge(policy, owner, devices)
	if err != nil {
		w.failAt(st, "Policy", err)
		return false
	}
	path := filepath.Join(w.d.home, ".cache", "flocc", "tailnet-policy.json")
	if err := writePolicy(path, merged); err != nil {
		w.failAt(st, "Policy", err)
		return false
	}
	detail := "missing: " + strings.Join(missing, "; ") + "; paste ~/.cache/flocc/tailnet-policy.json at " + policyURL
	w.d.ui.Note("Tailnet policy", "Added to your policy in that file: "+strings.Join(added, "; ")+"\nYour other rules are kept as they are.\nPaste it at "+policyURL)
	for range policyRechecks {
		st.Detail(detail)
		yes, err := w.confirm("Pasted and saved the policy?", true)
		if err != nil {
			w.failAt(st, "Policy", err)
			return false
		}
		if !yes {
			break
		}
		st.Detail("reading the policy")
		if missing, _, ok = read(); !ok {
			return false
		}
		if len(missing) == 0 {
			w.end(st, rowDone, "Policy", "ok")
			return true
		}
		detail = "still missing: " + strings.Join(missing, "; ") + "; paste ~/.cache/flocc/tailnet-policy.json at " + policyURL
	}
	w.end(st, rowPending, "Policy", detail)
	return false
}

func writePolicy(path string, merged []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, append([]byte(tailnet.Preamble), merged...), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// tagHosts tags each host file's node. canTag is false without a client or with a policy that does
// not pass yet; the host then gets a pending row naming the console.
func (w *wizard) tagHosts(api tailnetAPI, canTag bool, devices []tailnet.Device) {
	for _, h := range w.ops.hosts() {
		if w.stopped {
			return
		}
		if h.SSH == "" {
			continue
		}
		node := saved.HostPart(h.SSH)
		i := slices.IndexFunc(devices, func(d tailnet.Device) bool { return strings.EqualFold(d.Name, node) })
		if i < 0 {
			continue
		}
		dev := devices[i]
		name := "tag " + h.Name
		switch {
		case slices.Contains(dev.Tags, tailnet.Tag):
			w.row(rowDone, name, "tagged")
		case !canTag:
			w.row(rowPending, name, h.Name+" is not tagged "+tailnet.Tag+"; tag it at "+machinesURL+" once the policy above passes")
		default:
			st := w.d.ui.Step(name)
			st.Detail("tagging")
			err := api.SetTags(w.ctx, dev.ID, []string{tailnet.Tag})
			switch {
			case errors.Is(err, tailnet.ErrTagNotPermitted):
				w.end(st, rowPending, name, "the tailnet policy lacks "+tailnet.Tag+"; paste the policy above, then run flocc setup again")
			case errors.Is(err, tailnet.ErrRefused):
				w.end(st, rowPending, name, "the client lacks Devices core on "+tailnet.Tag+"; tag "+h.Name+" at "+machinesURL)
			case err != nil:
				w.failAt(st, name, err)
			default:
				w.end(st, rowDone, name, "tagged now")
			}
		}
	}
}
