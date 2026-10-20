package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/keys"
)

func rotateDeps(t *testing.T, p *hostScript) (*deps, *fakeSSH) {
	t.Helper()
	p.t = t
	d, f, _, _ := hostDeps(t, "a", "b")
	writeSecrets(t, d.home, allKeys)
	d.store = func() keys.Store { return keys.Open(d.home, io.Discard) }
	d.prompt = p
	d.open = func(string) {}
	d.http = http.DefaultClient
	d.githubAPI = fakeGitHub(t).URL
	f.out["root@a"] = ""
	return d, f
}

// pushedTo is the stdin of the push of file to dest, and whether there was one.
func pushedTo(f *fakeSSH, dest, file string) (string, bool) {
	for i, c := range f.runs {
		if c.Args[len(c.Args)-2] == dest && strings.Contains(c.String(), file) {
			return f.stdins[i], true
		}
	}
	return "", false
}

func TestRotatePushesTheNewTokenOnStdinAndNamesAnUnreachableHost(t *testing.T) {
	d, f := rotateDeps(t, &hostScript{secrets: []string{"github_pat_agent2", ""}})
	if code := run(context.Background(), d, []string{"keys", "rotate", "github"}); code != 1 {
		t.Fatalf("exit %d\n%s\n%s", code, d.out, d.err)
	}
	if v, _ := d.store().Get(keys.Agent); v != "github_pat_agent2" {
		t.Fatal(v)
	}
	for _, c := range f.runs {
		if strings.Contains(c.String(), "github_pat_agent2") {
			t.Fatalf("the token is in a command line: %s", c)
		}
	}
	for _, c := range f.captured() {
		// With RequestTTY force in the owner's ssh config, a pty would echo the token.
		if !slices.Contains(c.Args, "-T") {
			t.Fatalf("no -T: %s", c)
		}
	}
	if in, ok := pushedTo(f, "root@a", "gh-agent.token"); !ok || in != "github_pat_agent2\n" {
		t.Fatalf("runs %v stdins %q", f.runs, f.stdins)
	}
	if _, ok := pushedTo(f, "root@a", "gh-publish.token"); ok {
		t.Fatal("the publish token was kept, so it must not be pushed")
	}
	out, errb := d.out.(*bytes.Buffer).String(), d.err.(*bytes.Buffer).String()
	if !strings.Contains(out, "Pushed to a") || !strings.Contains(out, "Do not revoke the old token") ||
		!strings.Contains(out, "daily token check") || !strings.Contains(errb, "b: ") {
		t.Fatalf("%s\n%s", out, errb)
	}
}

// The push to each host waits for its connection to open on the terminal, where a Tailscale
// check-mode URL can be seen, and then writes every changed key over it.
func TestRotateOpensEachHostsConnectionOnTheTerminalOnce(t *testing.T) {
	// Nothing pasted and a yes: both stored tokens are pushed again.
	d, f := rotateDeps(t, &hostScript{confirms: []bool{true}})
	d.tty = true
	if code := run(context.Background(), d, []string{"keys", "rotate", "github"}); code != 1 {
		t.Fatalf("exit %d\n%s\n%s", code, d.out, d.err)
	}
	if got := warmUps(t, f); !slices.Equal(got, []string{"root@a", "root@b"}) || len(f.captured()) != 2 {
		t.Fatalf("warm-ups %v captures %v", got, f.captured())
	}
	if errb := d.err.(*bytes.Buffer).String(); !strings.Contains(errb, "b: could not reach b") {
		t.Fatal(errb)
	}
}

// Nothing is stored until every key has been asked, so a rotate stopped at the second prompt
// leaves the desktop and the hosts with the same keys.
func TestAStoppedRotateChangesNothing(t *testing.T) {
	d, f := rotateDeps(t, &hostScript{secrets: []string{"github_pat_agent2"}, err: errors.New("interrupted")})
	if code := run(context.Background(), d, []string{"keys", "rotate", "github"}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if v, _ := d.store().Get(keys.Agent); v != "github_pat_agent" || len(f.runs) != 0 {
		t.Fatalf("%q %v", v, f.runs)
	}
	if !strings.Contains(d.err.(*bytes.Buffer).String(), "interrupted") {
		t.Fatal(d.err)
	}
}

// cancelAtSecret cancels the run as Ctrl-C does while the n-th paste is being checked.
type cancelAtSecret struct {
	*hostScript
	cancel func()
	n, at  int
}

func (c *cancelAtSecret) Secret(title, help string) (string, error) {
	v, err := c.hostScript.Secret(title, help)
	if c.n++; c.n == c.at {
		c.cancel()
	}
	return v, err
}

func TestCtrlCDuringTheSecondCheckStoresAndPushesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &hostScript{secrets: []string{"github_pat_agent2", "github_pat_publish2"}}
	d, f := rotateDeps(t, p)
	d.prompt = &cancelAtSecret{hostScript: p, cancel: cancel, at: 2}
	if code := run(ctx, d, []string{"keys", "rotate", "github"}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	for s, want := range map[keys.Slot]string{keys.Agent: "github_pat_agent", keys.Publish: "github_pat_publish"} {
		if v, _ := d.store().Get(s); v != want {
			t.Fatalf("%s is %q", s, v)
		}
	}
	if len(f.runs) != 0 {
		t.Fatalf("pushed: %v", f.runs)
	}
}

// A push that missed a host leaves the new key in the store; rotating again with nothing to
// paste offers to push the stored keys, so the retry the message names works.
func TestRotateAgainPushesTheStoredKeysToTheHostThatMissedThem(t *testing.T) {
	p := &hostScript{secrets: []string{"github_pat_agent2", ""}}
	d, f := rotateDeps(t, p)
	if code := run(context.Background(), d, []string{"keys", "rotate", "github"}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	f.out["root@b"] = ""
	f.runs, f.stdins = nil, nil
	p.confirms = []bool{true}
	if code := run(context.Background(), d, []string{"keys", "rotate", "github"}); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, d.out, d.err)
	}
	if in, ok := pushedTo(f, "root@b", "gh-agent.token"); !ok || in != "github_pat_agent2\n" {
		t.Fatalf("runs %v stdins %q", f.runs, f.stdins)
	}
	if in, ok := pushedTo(f, "root@b", "gh-publish.token"); !ok || in != "github_pat_publish\n" {
		t.Fatalf("runs %v stdins %q", f.runs, f.stdins)
	}
}

func TestRotateWithNothingToPasteAndNoRepushChangesNothing(t *testing.T) {
	p := &hostScript{confirms: []bool{false}}
	d, f := rotateDeps(t, p)
	if code := run(context.Background(), d, []string{"keys", "rotate", "linear"}); code != 0 || len(f.runs) != 0 {
		t.Fatalf("exit %d runs %v", code, f.runs)
	}
	if !strings.Contains(d.out.(*bytes.Buffer).String(), "Nothing changed") {
		t.Fatal(d.out)
	}
}
