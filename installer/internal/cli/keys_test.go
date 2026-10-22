package cli

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

func TestKeysRefusesAKeyOnTheCommandLine(t *testing.T) {
	d, _, errb := testDeps(t)
	for _, arg := range []string{"github_pat_x", "lin_api_x", "ghp_x"} {
		if code := run(context.Background(), d, []string{"keys", arg}); code != 2 || !strings.Contains(errb.String(), "shell history") {
			t.Fatalf("%s: exit %d %q", arg, code, errb.String())
		}
	}
}

func TestReportMakesEachNextStepATodoOnce(t *testing.T) {
	d, _, _ := testDeps(t)
	rec := &ui.Recorder{}
	d.ui = ui.New(rec)
	code := report(d, []string{"GitHub agent token: bad; run flocc keys rotate github", "GitHub publish token: bad; run flocc keys rotate github",
		"ntfy topic: missing; run flocc keys", "other: no next step"})
	d.ui.Close()
	if code != 1 || strings.Join(rec.Todos, "|") != "run flocc keys rotate github|run flocc keys" {
		t.Fatalf("%d %q", code, rec.Todos)
	}
	if report(d, nil) != 0 {
		t.Fatal("no problems must exit 0")
	}
}

func TestKeysCheckFailsTheRowOfAMissingKey(t *testing.T) {
	d, _, _ := testDeps(t)
	rec := &ui.Recorder{}
	d.ui = ui.New(rec)
	writeSecrets(t, d.home, "")
	d.store = func() keys.Store { return keys.Open(d.home, io.Discard) }
	d.http = http.DefaultClient
	if code := run(context.Background(), d, []string{"keys", "check"}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if r := row(rec, "Check Linear API key"); r.State != ui.Failed || r.Err != "missing" {
		t.Fatalf("%+v", rec.Rows())
	}
	if len(rec.Todos) != 1 || rec.Todos[0] != "run flocc keys" {
		t.Fatalf("%q", rec.Todos)
	}
}

func TestMappedJoinsTheHostsTeamsReposAndDigests(t *testing.T) {
	r := func(u, base string) answers.Repo { return answers.Repo{URL: u, BaseBranch: base} }
	m := mapped([]hosts.Host{
		{Name: "a", Answers: answers.Answers{TeamKey: "NF", DigestIssue: "NF-7", Repos: []answers.Repo{r("https://github.com/O/A.git", "main")}}},
		{Name: "b", Answers: answers.Answers{TeamKey: "NF", DigestIssue: "NF-7", Repos: []answers.Repo{r("https://github.com/o/a", "main"), r("https://github.com/o/b", "trunk")}}},
	})
	if strings.Join(m.Teams, ",") != "NF" || len(m.Repos) != 2 || m.Repos[0].Slug != "o/a" || m.Repos[1].Base != "trunk" ||
		strings.Join(m.Digests, ",") != "NF-7" {
		t.Fatalf("%+v", m)
	}
}

type askedSecrets struct {
	*hostScript
	titles []string
}

func (a *askedSecrets) Secret(title, help string, facts ...string) (string, error) {
	a.titles = append(a.titles, title)
	return a.hostScript.Secret(title, help, facts...)
}

func TestKeysOffersTheTailscaleClientEvenWhenTheRequiredKeysAreStored(t *testing.T) {
	d, _, _ := testDeps(t)
	writeSecrets(t, d.home, allKeys)
	d.store = func() keys.Store { return keys.Open(d.home, io.Discard) }
	d.http = &http.Client{Transport: failingTransport{}}
	d.open = func(string) {}
	p := &askedSecrets{hostScript: &hostScript{t: t}}
	d.prompt = p
	run(context.Background(), d, []string{"keys"})
	if len(p.titles) != 1 || p.titles[0] != keys.Tailscale.Title() {
		t.Fatalf("asked %q, want only the Tailscale client", p.titles)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, io.ErrUnexpectedEOF
}
