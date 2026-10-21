package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/tailnet"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// tsStatus is tailscale status --json on the desktop with the laptop and flocc-nimble as peers.
func tsStatus(tagged bool) string {
	tags := "null"
	if tagged {
		tags = `["tag:flocc-host"]`
	}
	return `{"Self":{"ID":"nSELF","HostName":"desktop","TailscaleIPs":["100.64.0.2"],"UserID":11},
"Peer":{"k:a":{"ID":"nLAP","HostName":"laptop","TailscaleIPs":["100.64.0.1"],"UserID":11},
"k:b":{"ID":"nFLOCC","HostName":"flocc-nimble","TailscaleIPs":["100.64.0.7"],"UserID":11,"Tags":` + tags + `}},
"User":{"11":{"ID":11,"LoginName":"owner@example.com"}}}`
}

const (
	defaultPolicy = `{"grants":[{"src":["*"],"dst":["*"],"ip":["*"]}],"ssh":[{"action":"check","src":["autogroup:member"],"dst":["autogroup:self"],"users":["autogroup:nonroot","root"]}]}`
	goodPolicy    = `{"tagOwners":{"tag:flocc-host":["autogroup:admin"]},
"ssh":[{"action":"accept","src":["autogroup:member"],"dst":["tag:flocc-host"],"users":["root","autogroup:nonroot"]}],
"tests":[{"src":"tag:flocc-host","deny":["desktop:22"]}]}`
)

// fakeTailnet serves policies in order, then the last one again, and logs its calls.
type fakeTailnet struct {
	l        *opsLog
	policies []string
	readErr  error
	tagErr   error
	reads    int
}

func (f *fakeTailnet) Policy(context.Context) ([]byte, error) {
	f.l.add("policy")
	if f.readErr != nil {
		return nil, f.readErr
	}
	p := f.policies[min(f.reads, len(f.policies)-1)]
	f.reads++
	return []byte(p), nil
}

func (f *fakeTailnet) SetTags(_ context.Context, id string, tags []string) error {
	f.l.add("settags %s %v", id, tags)
	return f.tagErr
}

func tailnetOps(l *opsLog, api *fakeTailnet, tagged bool) setupOps {
	ops := doneOps(l)
	ops.tailnetClient = func() (tailnetAPI, bool) { return api, api != nil }
	ops.tailscaleStatus = func(context.Context) ([]byte, error) { return []byte(tsStatus(tagged)), nil }
	return ops
}

func TestSetupTailnetWithoutAClientIsSkippedWithItsPageAsATodo(t *testing.T) {
	var l opsLog
	d, out, p := setupDeps(t, nil)
	ops := tailnetOps(&l, nil, true)
	ops.tailnetClient = func() (tailnetAPI, bool) { return nil, false }
	if code := runSetup(context.Background(), d, ops); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	r := rowOf(t, p, "Client")
	_, page := keys.Page(keys.Tailscale, keys.Mapped{})
	if r.State != ui.Skipped || !strings.Contains(r.Detail, "no OAuth client; hosts join with a browser login") {
		t.Errorf("%+v", r)
	}
	if td := strings.Join(todos(p), "\n"); !strings.Contains(td, page) {
		t.Errorf("todos %q lack the page text", td)
	}
	if l.has("policy") || l.has("settags") {
		t.Errorf("calls %v", l.calls)
	}
	if !l.has("keys [" + string(keys.Tailscale) + "]") {
		t.Errorf("the slot was not filled: %v", l.calls)
	}
}

func TestSetupTailnetWritesTheMergedPolicyAndTagsOnlyAfterItPasses(t *testing.T) {
	var l opsLog
	api := &fakeTailnet{l: &l, policies: []string{defaultPolicy, goodPolicy}}
	d, _, p := setupDeps(t, nil)
	if code := runSetup(context.Background(), d, tailnetOps(&l, api, false)); code != 0 {
		t.Fatalf("exit %d; calls %v", code, l.calls)
	}
	b, err := os.ReadFile(filepath.Join(d.home, ".cache", "flocc", "tailnet-policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), tailnet.Preamble) {
		t.Errorf("no preamble:\n%s", b)
	}
	if st, _ := os.Stat(filepath.Join(d.home, ".cache", "flocc", "tailnet-policy.json")); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	if m, err := tailnet.Check([]byte(strings.TrimPrefix(string(b), tailnet.Preamble))); err != nil || len(m) != 0 {
		t.Errorf("the file misses %q (%v):\n%s", []string(m), err, b)
	}
	if r := rowOf(t, p, "Policy"); r.State != ui.Done {
		t.Errorf("%+v", r)
	}
	if !slices.Contains(p.asked, "Pasted and saved the policy?") {
		t.Errorf("asked %v", p.asked)
	}
	inOrder(t, l.calls, "policy", "policy", "settags nFLOCC [tag:flocc-host]")
	if r := rowOf(t, p, "tag flocc-nimble"); r.State != ui.Done || r.Detail != "tagged now" {
		t.Errorf("%+v", r)
	}
}

func TestSetupTailnetTagsAnUntaggedHostOnceWhenThePolicyPasses(t *testing.T) {
	var l opsLog
	api := &fakeTailnet{l: &l, policies: []string{goodPolicy}}
	d, _, p := setupDeps(t, nil)
	runSetup(context.Background(), d, tailnetOps(&l, api, false))
	n := 0
	for _, c := range l.calls {
		if strings.HasPrefix(c, "settags") {
			n++
		}
	}
	if n != 1 || slices.Contains(p.asked, "Pasted and saved the policy?") {
		t.Errorf("settags %d; asked %v; calls %v", n, p.asked, l.calls)
	}
	if _, err := os.Stat(filepath.Join(d.home, ".cache", "flocc", "tailnet-policy.json")); err == nil {
		t.Error("a policy that passes needs no file")
	}
}

func TestSetupTailnetLeavesATaggedHostAlone(t *testing.T) {
	var l opsLog
	api := &fakeTailnet{l: &l, policies: []string{goodPolicy}}
	d, _, p := setupDeps(t, nil)
	runSetup(context.Background(), d, tailnetOps(&l, api, true))
	if l.has("settags") {
		t.Errorf("calls %v", l.calls)
	}
	if r := rowOf(t, p, "tag flocc-nimble"); r.State != ui.Done || r.Detail != "tagged" {
		t.Errorf("%+v", r)
	}
}

func TestSetupTailnetNeverTagsWhileThePolicyFails(t *testing.T) {
	var l opsLog
	api := &fakeTailnet{l: &l, policies: []string{defaultPolicy}}
	d, _, p := setupDeps(t, nil)
	if code := runSetup(context.Background(), d, tailnetOps(&l, api, false)); code != 1 {
		t.Fatalf("exit %d; a pending row is the owner's to finish", code)
	}
	if l.has("settags") {
		t.Errorf("tagged before the policy passed: %v", l.calls)
	}
	// the read, then one re-check per confirm
	if api.reads != 1+policyRechecks {
		t.Errorf("reads %d", api.reads)
	}
	r := rowOf(t, p, "Policy")
	if r.State != ui.Pending || !strings.Contains(r.Detail, "paste ~/.cache/flocc/tailnet-policy.json at "+policyURL) {
		t.Errorf("%+v", r)
	}
	h := rowOf(t, p, "tag flocc-nimble")
	if h.State != ui.Pending || !strings.Contains(h.Detail, machinesURL) {
		t.Errorf("%+v", h)
	}
}

func TestSetupTailnetAClientWithoutPolicyReadIsPending(t *testing.T) {
	var l opsLog
	api := &fakeTailnet{l: &l, readErr: tailnet.ErrRefused}
	d, _, p := setupDeps(t, nil)
	runSetup(context.Background(), d, tailnetOps(&l, api, false))
	r := rowOf(t, p, "Policy")
	if r.State != ui.Pending || !strings.Contains(r.Detail, "the client lacks Policy file read") {
		t.Errorf("%+v", r)
	}
	if l.has("settags") {
		t.Errorf("calls %v", l.calls)
	}
}

func TestSetupTailnetNamesAPolicyThatDoesNotDefineTheTag(t *testing.T) {
	var l opsLog
	api := &fakeTailnet{l: &l, policies: []string{goodPolicy}, tagErr: tailnet.ErrTagNotPermitted}
	d, _, p := setupDeps(t, nil)
	runSetup(context.Background(), d, tailnetOps(&l, api, false))
	if r := rowOf(t, p, "tag flocc-nimble"); r.State != ui.Pending || !strings.Contains(r.Detail, "lacks "+tailnet.Tag) {
		t.Errorf("%+v", r)
	}
}
