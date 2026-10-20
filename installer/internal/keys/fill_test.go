package keys

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/githubapi"
	"github.com/NovusEdge/flocc/installer/internal/replay"
)

// logStore keeps keys in memory and logs each Set into the fixture's event list, beside the
// pages opened, so a test can read their order.
type logStore struct {
	m      map[Slot]string
	events *[]string
}

func (s logStore) Get(k Slot) (string, error) { return s.m[k], nil }
func (logStore) Where() string                { return "memory" }

func (s logStore) Set(k Slot, v string) error {
	s.m[k] = v
	*s.events = append(*s.events, "set "+string(k))
	return nil
}

// script answers the prompts in order; an exhausted list gives an empty paste or the default.
type script struct {
	secrets  []string
	confirms []bool
}

func (s *script) Secret(string, string) (string, error) {
	if len(s.secrets) == 0 {
		return "", nil
	}
	v := s.secrets[0]
	s.secrets = s.secrets[1:]
	return v, nil
}

func (s *script) Confirm(_ string, def bool) (bool, error) {
	if len(s.confirms) == 0 {
		return def, nil
	}
	v := s.confirms[0]
	s.confirms = s.confirms[1:]
	return v, nil
}

type fixture struct {
	env    Env
	store  logStore
	events []string
	out    bytes.Buffer
	qr     []string
	mu     sync.Mutex
	pushes []*http.Request
}

func newFixture(t *testing.T, gh, lin *replay.Server, p *script) *fixture {
	t.Helper()
	f := &fixture{}
	f.store = logStore{m: map[Slot]string{}, events: &f.events}
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.pushes = append(f.pushes, r)
		f.mu.Unlock()
	}))
	t.Cleanup(ntfy.Close)
	f.env = Env{Store: f.store, Prompt: p, Out: &f.out, HTTP: http.DefaultClient, NtfyBase: ntfy.URL,
		Open:     func(u string) { f.events = append(f.events, "open "+u) },
		QR:       func(s string) { f.qr = append(f.qr, s) },
		NewTopic: func() string { return "flocc-test" }}
	if gh != nil {
		f.env.GitHubAPI = gh.URL
	}
	if lin != nil {
		f.env.LinearAPI = lin.URL + "/graphql"
	}
	return f
}

func ghServer(t *testing.T, routes ...replay.Route) *replay.Server {
	return replay.New(t, "../githubapi/testdata", routes...)
}

// linServer answers the viewer query with file; more routes answer the write probe.
func linServer(t *testing.T, file string, more ...replay.Route) *replay.Server {
	routes := append([]replay.Route{{Method: "POST", Path: "/graphql", Body: "viewer", File: file}}, more...)
	return replay.New(t, "../linearapi/testdata", routes...)
}

var (
	userOK = replay.Route{Method: "GET", Path: "/user", File: "user-ok.http"}
	repoA  = replay.Route{Method: "GET", Path: "/repos/o/a", File: "repo-ok.http"}
	onlyA  = Mapped{Repos: []githubapi.Target{{Slug: "o/a", Base: "main"}}}
)

func TestFillAsksForEachKeyInOrderAndStoresIt(t *testing.T) {
	f := newFixture(t, ghServer(t, userOK), linServer(t, "viewer-ok.http"),
		&script{secrets: []string{"lin_api_k", "github_pat_agent", "github_pat_publish"}})
	if err := Fill(context.Background(), f.env, Mapped{}, Slots); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, e := range f.events {
		order = append(order, strings.SplitN(e, "?", 2)[0])
	}
	want := []string{
		"open https://linear.app/settings/account/security", "set linear_key",
		"open https://github.com/settings/personal-access-tokens/new", "set agent_token",
		"open https://github.com/settings/personal-access-tokens/new", "set publish_token",
		"set ntfy_topic",
	}
	if strings.Join(order, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%q", order)
	}
	if f.store.m[Publish] != "github_pat_publish" || f.store.m[Ntfy] != "flocc-test" {
		t.Fatalf("%v", f.store.m)
	}
	for _, secret := range []string{"lin_api_k", "github_pat_agent", "github_pat_publish"} {
		if strings.Contains(f.out.String(), secret) {
			t.Errorf("%s printed", secret)
		}
	}
}

func TestAKeyInTheWrongSlotIsRefusedBeforeAnyRequest(t *testing.T) {
	gh := ghServer(t, userOK)
	f := newFixture(t, gh, nil, &script{secrets: []string{"lin_api_x", "github_pat_agent"}})
	f.store.m[Linear] = "lin_api_k"
	if err := Fill(context.Background(), f.env, Mapped{}, Slots); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if f.store.m[Agent] != "github_pat_agent" || f.store.m[Publish] != "" || !strings.Contains(f.out.String(), "that is a Linear API key") {
		t.Fatalf("%v\n%s", f.store.m, f.out.String())
	}
	for _, r := range gh.Requests {
		if r.Auth != "Bearer github_pat_agent" {
			t.Errorf("the wrong-slot paste reached GitHub: %+v", r)
		}
	}
}

func TestAPasteIsTrimmedAndNameEqualsValueIsRefused(t *testing.T) {
	gh := ghServer(t, userOK)
	f := newFixture(t, gh, nil, &script{secrets: []string{"AGENT_TOKEN=github_pat_a", "  github_pat_a \n"}})
	f.store.m[Linear] = "lin_api_k"
	Fill(context.Background(), f.env, Mapped{}, Slots)
	if f.store.m[Agent] != "github_pat_a" || !strings.Contains(f.out.String(), "NAME=value") {
		t.Fatalf("%q\n%s", f.store.m[Agent], f.out.String())
	}
	// The refused paste never reached GitHub: every request carries the trimmed value.
	if len(gh.Requests) == 0 {
		t.Fatal("the trimmed paste was not checked")
	}
	for _, r := range gh.Requests {
		if r.Auth != "Bearer github_pat_a" {
			t.Errorf("%+v", r)
		}
	}
}

func TestAnAgentTokenThatCanPushIsRefusedAndAskedForAgain(t *testing.T) {
	gh := ghServer(t, userOK, repoA,
		replay.Route{Method: "POST", Path: "/repos/o/a/git/refs", Auth: "Bearer github_pat_push", File: "refs-unprocessable.http"},
		replay.Route{Method: "POST", Path: "/repos/o/a/git/refs", Auth: "Bearer github_pat_read", File: "refs-forbidden.http"})
	f := newFixture(t, gh, nil, &script{secrets: []string{"github_pat_push", "github_pat_read"}})
	f.store.m[Linear] = "lin_api_k"
	Fill(context.Background(), f.env, onlyA, Slots)
	if f.store.m[Agent] != "github_pat_read" || !strings.Contains(f.out.String(), "o/a: the token can push") ||
		!strings.Contains(f.out.String(), "paste it again") {
		t.Fatalf("%q\n%s", f.store.m[Agent], f.out.String())
	}
}

func TestFillAsksOnlyForTheSlotsNamed(t *testing.T) {
	f := newFixture(t, nil, nil, &script{secrets: []string{"lin_api_k"}})
	f.store.m[Linear] = "lin_api_k"
	if err := Fill(context.Background(), f.env, Mapped{}, []Slot{Linear}); err != nil || len(f.events) != 0 {
		t.Fatalf("%v %v", err, f.events)
	}
}

func TestAReadOnlyLinearKeyIsRefused(t *testing.T) {
	lin := linServer(t, "viewer-ok.http",
		replay.Route{Method: "POST", Path: "/graphql", Body: "issue(id", File: "issue-ok.http"},
		replay.Route{Method: "POST", Path: "/graphql", Body: "issueUpdate", File: "update-forbidden.http"})
	f := newFixture(t, nil, lin, &script{})
	_, _, err := Accept(context.Background(), f.env, Mapped{Teams: []string{"NF"}, Digests: []string{"NF-186"}}, Linear, "lin_api_ro")
	if err == nil || !strings.Contains(err.Error(), "cannot write to NF-186") {
		t.Fatal(err)
	}
}

// An outage says nothing about the key: the same paste is checked again when the owner asks,
// and nothing is stored or asked for again when they do not.
func TestALinearOutageRechecksTheSamePasteAndStoresNothing(t *testing.T) {
	lin := linServer(t, "unavailable.http")
	p := &script{secrets: []string{"lin_api_k", "lin_api_never"}, confirms: []bool{true, false}}
	f := newFixture(t, nil, lin, p)
	err := Fill(context.Background(), f.env, Mapped{}, Slots)
	if err == nil || errors.Is(err, ErrStopped) || !strings.Contains(err.Error(), "could not check Linear") {
		t.Fatal(err)
	}
	if f.store.m[Linear] != "" || len(p.secrets) != 1 || strings.Contains(f.out.String(), "paste it again") {
		t.Fatalf("%v %q\n%s", f.store.m, p.secrets, f.out.String())
	}
	if len(lin.Requests) != 2 || lin.Requests[1].Auth != "lin_api_k" {
		t.Fatalf("%+v", lin.Requests)
	}
}

func TestAGitHubOutageIsNotABadToken(t *testing.T) {
	gh := ghServer(t, replay.Route{Method: "GET", Path: "/user", File: "refs-ratelimited.http"})
	p := &script{secrets: []string{"github_pat_a", "github_pat_never"}, confirms: []bool{false}}
	f := newFixture(t, gh, nil, p)
	f.store.m[Linear] = "lin_api_k"
	err := Fill(context.Background(), f.env, Mapped{}, Slots)
	if err == nil || errors.Is(err, ErrStopped) || !strings.Contains(err.Error(), "could not check GitHub") {
		t.Fatal(err)
	}
	if f.store.m[Agent] != "" || len(p.secrets) != 1 || len(gh.Requests) != 1 {
		t.Fatalf("%v %q %d", f.store.m, p.secrets, len(gh.Requests))
	}
}

func TestNtfyShowsTheAppLinkAndResendsUntilThePushArrives(t *testing.T) {
	f := newFixture(t, nil, nil, &script{confirms: []bool{false, true, true}})
	f.store.m[Linear], f.store.m[Agent], f.store.m[Publish] = "lin_api_k", "github_pat_a", "github_pat_p"
	if err := Fill(context.Background(), f.env, Mapped{}, Slots); err != nil {
		t.Fatal(err)
	}
	if len(f.qr) != 1 || f.qr[0] != "ntfy://ntfy.sh/flocc-test?display=flocc" || !strings.Contains(f.out.String(), "https://ntfy.sh/flocc-test") {
		t.Fatalf("%q\n%s", f.qr, f.out.String())
	}
	if len(f.pushes) != 2 || f.pushes[0].URL.Path != "/flocc-test" || f.pushes[0].Header.Get("Priority") != "5" || f.pushes[0].Header.Get("Title") != "flocc" {
		t.Fatalf("%d pushes", len(f.pushes))
	}
}

func TestGitHubPagesArePrefilled(t *testing.T) {
	m := Mapped{Repos: []githubapi.Target{{Slug: "o/a"}, {Slug: "o/b"}}}
	raw, help := Page(Agent, m)
	u, _ := url.Parse(raw)
	for k, v := range map[string]string{"name": "flocc agent", "expires_in": "366", "target_name": "o",
		"contents": "read", "pull_requests": "read", "issues": "read", "statuses": "read", "actions": "read"} {
		if got := u.Query().Get(k); got != v {
			t.Errorf("%s=%q", k, got)
		}
	}
	if u.Query().Has("checks") {
		t.Error("fine-grained tokens have no checks permission")
	}
	if !strings.Contains(help, "o/a, o/b") {
		t.Fatal(help)
	}
	raw, _ = Page(Publish, m)
	u, _ = url.Parse(raw)
	if q := u.Query(); q.Get("contents") != "write" || q.Get("pull_requests") != "write" || q.Has("issues") {
		t.Fatal(raw)
	}
}

func TestAGitHubPageForTwoOwnersNamesBoth(t *testing.T) {
	raw, help := Page(Agent, Mapped{Repos: []githubapi.Target{{Slug: "o/a"}, {Slug: "p/b"}}})
	u, _ := url.Parse(raw)
	if u.Query().Has("target_name") || !strings.Contains(help, "o and p") || !strings.Contains(help, "one owner") {
		t.Fatalf("%s\n%s", raw, help)
	}
}

func TestCheckNamesEveryProblem(t *testing.T) {
	gh := ghServer(t, userOK, repoA, replay.Route{Method: "POST", Path: "/repos/o/a/git/refs", File: "refs-unprocessable.http"})
	f := newFixture(t, gh, linServer(t, "bad-key.http"), &script{})
	f.store.m[Linear], f.store.m[Agent] = "lin_api_k", "github_pat_push"
	got := strings.Join(Check(context.Background(), f.env, onlyA), "\n")
	for _, want := range []string{"Authentication required", "o/a: the token can push", "GitHub publish token: missing", "ntfy topic: missing"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from:\n%s", want, got)
		}
	}
	// A stored token is edited on GitHub, which keeps its value; there is no prompt to paste into.
	if !strings.Contains(got, `open "flocc agent", Edit`) || !strings.Contains(got, "the token stays the same") ||
		!strings.Contains(got, "flocc keys check") || strings.Contains(got, "paste it again") {
		t.Errorf("wrong remedy:\n%s", got)
	}
}
