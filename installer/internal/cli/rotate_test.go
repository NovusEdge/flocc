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

// A rotate stopped at the second prompt still pushes the token it stored, so the desktop and the
// hosts never hold different agent tokens.
func TestAStoppedRotatePushesWhatItStored(t *testing.T) {
	d, f := rotateDeps(t, &hostScript{secrets: []string{"github_pat_agent2"}, err: errors.New("interrupted")})
	if code := run(context.Background(), d, []string{"keys", "rotate", "github"}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if in, ok := pushedTo(f, "root@a", "gh-agent.token"); !ok || in != "github_pat_agent2\n" {
		t.Fatalf("runs %v", f.runs)
	}
	if !strings.Contains(d.err.(*bytes.Buffer).String(), "interrupted") {
		t.Fatal(d.err)
	}
}
