package cli

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// shipCall is what the fake Ship got: the answers document is read, as Ship reads it.
type shipCall struct {
	o         ship.Options
	doc       []byte
	noAnswers bool // the answers reader was nil
}

func shipDeps(t *testing.T, secrets string) (*deps, *fakeSSH, *[]shipCall, *ui.Recorder) {
	t.Helper()
	d, f, _, _ := hostDeps(t, "a")
	rec := &ui.Recorder{}
	d.ui = ui.New(rec)
	writeSecrets(t, d.home, secrets)
	d.store = func() keys.Store { return keys.Open(d.home, io.Discard) }
	d.http, d.githubAPI, d.open = http.DefaultClient, fakeGitHub(t).URL, func(string) {}
	d.prompt = &hostScript{}
	h := hosts.Host{Name: "a", SSH: "root@a", Answers: answers.Answers{TeamKey: "NF", DigestIssue: "NF-1",
		Repos: []answers.Repo{{Name: "a", URL: "https://github.com/o/a", BaseBranch: "main", Check: "just check"}}}}
	if err := hosts.Save(hosts.Dir(d.home), h); err != nil {
		t.Fatal(err)
	}
	var calls []shipCall
	d.repoRoot = func() (string, error) { return "/repo", nil }
	d.doShip = func(_ context.Context, o ship.Options, in io.Reader) (ship.Summary, error) {
		c := shipCall{o: o, noAnswers: in == nil}
		if in != nil {
			c.doc, _ = io.ReadAll(in)
		}
		calls = append(calls, c)
		return ship.Summary{Version: "flocc 0.5.0", Login: "none", LoginPending: true}, nil
	}
	return d, f, &calls, rec
}

func TestShipSendsTheHostFileAndTheKeysOnStdinAndDrawsTheSummary(t *testing.T) {
	d, _, calls, rec := shipDeps(t, allKeys)
	d.tty = true // so Ship opens the connection where a Tailscale approval URL shows
	if code := run(context.Background(), d, []string{"ship", "-H", "a"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	c := (*calls)[0]
	if c.o.Target != "root@a" || c.o.Interactive || !c.o.TTY || c.o.Out != d.out || c.o.UI != d.ui || c.noAnswers {
		t.Fatalf("%+v", c)
	}
	a, err := answers.Parse(c.doc)
	if err != nil || a.TeamKey != "NF" || a.NtfyTopic != "flocc-t" || a.Secrets.LinearKey != "lin_api_k" ||
		a.Secrets.AgentToken != "github_pat_agent" || a.Secrets.PublishToken != "github_pat_publish" {
		t.Fatalf("%+v %v", a, err)
	}
	if f := a.Secrets.A2A; f == nil || f.Name != "a" || f.Token == "" || f.Hosts["a"].URL != "http://a:7443" {
		t.Fatalf("a2a %+v", f)
	}
	if b, err := os.ReadFile(filepath.Join(d.home, ".config", "flocc", "a2a.toml")); err != nil || !strings.Contains(string(b), `name = 'owner'`) {
		t.Fatalf("desktop file %q %v", b, err)
	}
	// The summary names the host, not its ssh destination, in the command it asks for.
	var out string
	for _, e := range rec.Rows() {
		out += e.Name + ": " + e.Detail + "\n"
	}
	if !strings.Contains(out, "version: flocc 0.5.0") || !strings.Contains(out, "flocc login -H a") ||
		strings.Contains(out, "root@a") || strings.Contains(out, "<host>") {
		t.Fatal(out)
	}
}

func TestShipAsksForAMissingKeyThenShips(t *testing.T) {
	d, _, calls, _ := shipDeps(t, strings.Replace(allKeys, "publish_token = 'github_pat_publish'\n", "", 1))
	d.prompt = &hostScript{secrets: []string{"github_pat_publish"}}
	if err := shipHost(context.Background(), d, "a"); err != nil {
		t.Fatal(err)
	}
	a, _ := answers.Parse((*calls)[0].doc)
	if a == nil || a.Secrets.PublishToken != "github_pat_publish" {
		t.Fatalf("%+v", a)
	}
}

func TestShipStopsBeforeShippingWhenAKeyIsNotGivenOrTheHostHasNoAnswers(t *testing.T) {
	d, _, calls, _ := shipDeps(t, strings.Replace(allKeys, "publish_token = 'github_pat_publish'\n", "", 1))
	if err := shipHost(context.Background(), d, "a"); err == nil || !strings.Contains(err.Error(), "empty paste") {
		t.Fatal(err)
	}
	hosts.Save(hosts.Dir(d.home), hosts.Host{Name: "b", SSH: "root@b"})
	writeSecrets(t, d.home, allKeys)
	if err := shipHost(context.Background(), d, "b"); err == nil || !strings.Contains(err.Error(), "flocc host import -H b") ||
		strings.Index(err.Error(), "flocc host import") > strings.Index(err.Error(), "flocc host add b") {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("shipped anyway: %+v", *calls)
	}
}

func TestInteractiveShipSendsNoAnswersAndCopiesTheHostsBack(t *testing.T) {
	d, f, calls, _ := shipDeps(t, allKeys)
	f.out["root@a"] = "team_key = \"NF\"\ndigest_issue = \"NF-9\"\n\n[[repos]]\nname = \"a\"\nurl = \"https://github.com/o/a\"\nbase_branch = \"main\"\ncheck = \"just check\"\n"
	if code := run(context.Background(), d, []string{"ship", "-H", "a", "--interactive"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if c := (*calls)[0]; !c.o.Interactive || !c.noAnswers {
		t.Fatalf("%+v", c)
	}
	if h, _ := hosts.Load(hosts.Dir(d.home), "a"); h.DigestIssue != "NF-9" {
		t.Fatalf("%+v", h)
	}
}

func TestLoginRunsOnTheChosenHost(t *testing.T) {
	d, _, _, _ := shipDeps(t, allKeys)
	var got ship.LoginOptions
	d.doLogin = func(_ context.Context, o ship.LoginOptions) error {
		got = o
		return nil
	}
	if code := run(context.Background(), d, []string{"login", "-H", "a", "--email", "o@example.test"}); code != 0 ||
		got.Target != "root@a" || got.Name != "a" || got.Email != "o@example.test" || got.Out != d.out || got.UI != d.ui {
		t.Fatalf("exit %d %+v", code, got)
	}
	if code := run(context.Background(), d, []string{"login", "-H", "a", "--email", "a@b;id"}); code != 2 {
		t.Fatalf("an unsafe email: exit %d", code)
	}
}
