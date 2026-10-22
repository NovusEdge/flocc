package keys

import (
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
	"github.com/NovusEdge/flocc/installer/internal/ui"
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

// script answers the prompts in order. An exhausted secrets list gives an empty paste; an
// exhausted confirms list fails the test, because a default of true on "Check the same paste
// again?" would otherwise repeat until go test times out. newFixture sets t.
type script struct {
	t        *testing.T
	secrets  []string
	confirms []bool
	facts    [][]string // what each Secret was shown with
}

func (s *script) Secret(_, _ string, facts ...string) (string, error) {
	s.facts = append(s.facts, facts)
	if len(s.secrets) == 0 {
		return "", nil
	}
	v := s.secrets[0]
	s.secrets = s.secrets[1:]
	return v, nil
}

func (s *script) Confirm(title string, _ bool) (bool, error) {
	if len(s.confirms) == 0 {
		s.t.Helper()
		s.t.Fatalf("unscripted confirm: %q", title)
	}
	v := s.confirms[0]
	s.confirms = s.confirms[1:]
	return v, nil
}

type fixture struct {
	env    Env
	store  logStore
	events []string
	rec    *ui.Recorder
	mu     sync.Mutex
	pushes []*http.Request
}

// text is every word the session was sent: notes, rows, details and errors.
func (f *fixture) text() string {
	var b strings.Builder
	for _, e := range f.rec.Events {
		b.WriteString(e.Name + " " + e.Detail + " " + e.Err + " " + e.Text + "\n")
	}
	return b.String()
}

// row is the last event of the step named name, or a zero Event.
func (f *fixture) row(name string) ui.Event {
	var got ui.Event
	for _, e := range f.rec.Rows() {
		if e.Name == name {
			got = e
		}
	}
	return got
}

func newFixture(t *testing.T, gh, lin *replay.Server, p *script) *fixture {
	t.Helper()
	f := &fixture{rec: &ui.Recorder{}}
	p.t = t
	f.store = logStore{m: map[Slot]string{}, events: &f.events}
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.pushes = append(f.pushes, r)
		f.mu.Unlock()
	}))
	t.Cleanup(ntfy.Close)
	f.env = Env{Store: f.store, Prompt: p, UI: ui.New(f.rec), HTTP: http.DefaultClient, NtfyBase: ntfy.URL,
		Open:     func(u string) { f.events = append(f.events, "open "+u) },
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
		&script{secrets: []string{"lin_api_k", "github_pat_agent", "github_pat_publish"}, confirms: []bool{true}})
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
		if strings.Contains(f.text(), secret) {
			t.Errorf("%s printed", secret)
		}
	}
}

func TestFillAsksThroughTheUISession(t *testing.T) {
	rec := &ui.Recorder{Answers: []any{"lin_api_k", "github_pat_agent", "github_pat_publish", true}}
	f := newFixture(t, ghServer(t, userOK), linServer(t, "viewer-ok.http"), &script{})
	f.env.Prompt = ui.Prompts{S: ui.New(rec)}
	if err := Fill(context.Background(), f.env, Mapped{}, Slots); err != nil {
		t.Fatal(err)
	}
	if len(rec.Asked) != 4 || rec.Asked[0].Kind != ui.PSecret || rec.Asked[3].Kind != ui.PConfirm {
		t.Fatalf("%+v", rec.Asked)
	}
}

func TestEachLiveCheckIsAStepWithItsOwnDetail(t *testing.T) {
	f := newFixture(t, ghServer(t, userOK), linServer(t, "viewer-ok.http"),
		&script{secrets: []string{"lin_api_k", "github_pat_agent", "github_pat_publish"}, confirms: []bool{true}})
	if err := Fill(context.Background(), f.env, Mapped{}, Slots); err != nil {
		t.Fatal(err)
	}
	asking := map[string]string{}
	for _, e := range f.rec.Events {
		if e.Kind == ui.KStep && e.State == ui.Running && e.Detail != "" {
			asking[e.Name] = e.Detail
		}
	}
	want := map[string]string{"Check Linear API key": "asking Linear", "Check GitHub agent token (read-only)": "asking GitHub",
		"Check GitHub publish token": "asking GitHub", "Check ntfy topic": "sending a test push"}
	for name, detail := range want {
		if asking[name] != detail {
			t.Errorf("%s: %q", name, asking[name])
		}
	}
	if r := f.row("Store Linear API key"); r.State != ui.Done || r.Detail != "in memory" {
		t.Errorf("%+v", r)
	}
	for _, e := range f.rec.Events {
		for _, secret := range []string{"lin_api_k", "github_pat_agent"} {
			if strings.Contains(e.Name+e.Detail+e.Err+e.Text, secret) {
				t.Errorf("%s reached an event: %+v", secret, e)
			}
		}
	}
}

// A refused paste is asked for again, so its row must not fail the session.
func TestARefusedPasteEndsItsRowSkippedAndTheSessionStaysClean(t *testing.T) {
	f := newFixture(t, nil, nil, &script{secrets: []string{"github_pat_x", "lin_api_k"}})
	f.env.LinearAPI = linServer(t, "viewer-ok.http").URL + "/graphql"
	if err := Fill(context.Background(), f.env, Mapped{}, []Slot{Linear}); err != nil {
		t.Fatal(err)
	}
	var states []ui.State
	for _, r := range f.rec.Rows() {
		if r.Name == "Check Linear API key" {
			states = append(states, r.State)
		}
	}
	if len(states) != 2 || states[0] != ui.Skipped || states[1] != ui.Done || f.env.UI.Failed() {
		t.Fatalf("%v", states)
	}
}

// The full-screen modal covers the rows, so the page's steps and its link go with the question.
func TestThePasteIsAskedWithTheStepsAndTheLinkAsFacts(t *testing.T) {
	p := &script{secrets: []string{"lin_api_k"}}
	f := newFixture(t, nil, nil, p)
	f.env.LinearAPI = linServer(t, "viewer-ok.http").URL + "/graphql"
	if err := Fill(context.Background(), f.env, Mapped{}, []Slot{Linear}); err != nil {
		t.Fatal(err)
	}
	link, page := Page(Linear, Mapped{})
	if len(p.facts) != 1 || len(p.facts[0]) != 2 || p.facts[0][0] != page || p.facts[0][1] != link {
		t.Fatalf("facts %q, want the page and the link", p.facts)
	}
}

func tailscaleServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.URL.Path != "/api/v2/oauth/token" || r.Form.Get("client_id") != "kid" || r.Form.Get("client_secret") != "ksecret" {
			status = http.StatusUnauthorized
		}
		w.WriteHeader(status)
		w.Write([]byte(`{"access_token":"t","expires_in":3600}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func TestTheTailscaleSlotIsOptionalAndAnEmptyPasteSkipsIt(t *testing.T) {
	p := &script{}
	f := newFixture(t, nil, nil, p)
	if err := Fill(context.Background(), f.env, Mapped{}, []Slot{Tailscale}); err != nil {
		t.Fatal(err)
	}
	if f.store.m[Tailscale] != "" || f.env.UI.Failed() {
		t.Fatalf("%v", f.store.m)
	}
	if page := strings.Join(p.facts[0], "\n");!strings.Contains(page, "https://login.tailscale.com/admin/settings/oauth") ||
		!strings.Contains(page, "Auth keys (write) and Devices core (write)") || !strings.Contains(page, "tag:flocc-host") {
		t.Fatal(page)
	}
}

func TestTheTailscaleClientIsCheckedByAskingForAToken(t *testing.T) {
	ts := tailscaleServer(t, http.StatusOK)
	f := newFixture(t, nil, nil, &script{secrets: []string{"kid", "kid:ksecret"}})
	f.env.TailscaleAPI = ts.URL
	if err := Fill(context.Background(), f.env, Mapped{}, []Slot{Tailscale}); err != nil {
		t.Fatal(err)
	}
	if f.store.m[Tailscale] != "kid:ksecret" {
		t.Fatalf("%v", f.store.m)
	}
	if r := f.row("Check Tailscale OAuth client"); r.State != ui.Done || r.Detail != "token ok" {
		t.Errorf("%+v", f.rec.Rows())
	}
	if !strings.Contains(f.text(), "a colon, then the secret") {
		t.Errorf("a paste without a colon was not refused:\n%s", f.text())
	}
	if strings.Contains(f.text(), "ksecret") {
		t.Error("the secret reached an event")
	}
}

func TestTailscaleRefusingTheClientIsNamedAndNothingIsStored(t *testing.T) {
	ts := tailscaleServer(t, http.StatusUnauthorized)
	f := newFixture(t, nil, nil, &script{secrets: []string{"kid:ksecret", ""}})
	f.env.TailscaleAPI = ts.URL
	if err := Fill(context.Background(), f.env, Mapped{}, []Slot{Tailscale}); err != nil {
		t.Fatal(err)
	}
	if f.store.m[Tailscale] != "" || !strings.Contains(f.text(), "Tailscale refused the client; make a new one") {
		t.Fatalf("%v\n%s", f.store.m, f.text())
	}
}

func TestTailscaleOutageLeavesTheClientUnchecked(t *testing.T) {
	ts := tailscaleServer(t, http.StatusBadGateway)
	f := newFixture(t, nil, nil, &script{secrets: []string{"kid:ksecret"}, confirms: []bool{false}})
	f.env.TailscaleAPI = ts.URL
	err := Fill(context.Background(), f.env, Mapped{}, []Slot{Tailscale})
	if err == nil || !strings.Contains(err.Error(), "could not check Tailscale") || f.store.m[Tailscale] != "" {
		t.Fatal(err, f.store.m)
	}
}

func TestFillWithTheRequiredSlotsNeverAsksForTailscale(t *testing.T) {
	f := newFixture(t, nil, nil, &script{})
	f.store.m[Linear], f.store.m[Agent], f.store.m[Publish], f.store.m[Ntfy] = "lin_api_k", "github_pat_a", "github_pat_p", "t"
	if err := Fill(context.Background(), f.env, Mapped{}, Slots); err != nil || len(f.rec.Asked) != 0 {
		t.Fatalf("%v %+v", err, f.rec.Asked)
	}
}

func TestCheckFailsTheRowOfEachProblemAndChecksAStoredTailscaleClient(t *testing.T) {
	ts := tailscaleServer(t, http.StatusUnauthorized)
	gh := ghServer(t, replay.Route{Method: "GET", Path: "/user", File: "bad-credentials.http"})
	f := newFixture(t, gh, linServer(t, "bad-key.http"), &script{})
	f.env.TailscaleAPI = ts.URL
	f.store.m[Linear], f.store.m[Agent], f.store.m[Publish], f.store.m[Tailscale] = "lin_api_k", "github_pat_a", "github_pat_p", "kid:ksecret"
	got := Check(context.Background(), f.env, onlyA)
	if len(got) != 5 || !strings.HasSuffix(got[3], "Tailscale refused the client; make a new one") {
		t.Fatalf("%q", got)
	}
	if r := f.row("Check Linear API key"); r.State != ui.Failed || strings.Contains(r.Err, "; run ") {
		t.Errorf("%+v", r)
	}
	if r := f.row("Check ntfy topic"); r.State != ui.Failed || r.Err != "missing" {
		t.Errorf("%+v", r)
	}
	if !f.env.UI.Failed() {
		t.Error("session not failed")
	}
}

func TestAKeyInTheWrongSlotIsRefusedBeforeAnyRequest(t *testing.T) {
	gh := ghServer(t, userOK)
	f := newFixture(t, gh, nil, &script{secrets: []string{"lin_api_x", "github_pat_agent"}})
	f.store.m[Linear] = "lin_api_k"
	if err := Fill(context.Background(), f.env, Mapped{}, Slots); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if f.store.m[Agent] != "github_pat_agent" || f.store.m[Publish] != "" || !strings.Contains(f.text(), "that is a Linear API key") {
		t.Fatalf("%v\n%s", f.store.m, f.text())
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
	if f.store.m[Agent] != "github_pat_a" || !strings.Contains(f.text(), "NAME=value") {
		t.Fatalf("%q\n%s", f.store.m[Agent], f.text())
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
	if f.store.m[Agent] != "github_pat_read" || !strings.Contains(f.text(), "o/a: the token can push") ||
		!strings.Contains(f.text(), "paste it again") {
		t.Fatalf("%q\n%s", f.store.m[Agent], f.text())
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
	if f.store.m[Linear] != "" || len(p.secrets) != 1 || strings.Contains(f.text(), "paste it again") {
		t.Fatalf("%v %q\n%s", f.store.m, p.secrets, f.text())
	}
	if len(lin.Requests) != 2 || lin.Requests[1].Auth != "lin_api_k" {
		t.Fatalf("%+v", lin.Requests)
	}
}

// A 400 validation error or a 404 is Linear refusing the call, not an outage: a second check of
// the same paste would fail the same way, so the owner is not offered one.
func TestALinearErrorThatIsNoOutageOffersNoRecheck(t *testing.T) {
	lin := linServer(t, "validation-error.http")
	f := newFixture(t, nil, lin, &script{secrets: []string{"lin_api_k"}})
	err := Fill(context.Background(), f.env, Mapped{}, []Slot{Linear})
	if !errors.Is(err, ErrStopped) || f.store.m[Linear] != "" || len(lin.Requests) != 1 {
		t.Fatalf("%v %v %d", err, f.store.m, len(lin.Requests))
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
	var note ui.Event
	for _, e := range f.rec.Events {
		if e.Kind == ui.KNote && e.Name == "ntfy topic" {
			note = e
		}
	}
	if !strings.Contains(note.Text, "https://ntfy.sh/flocc-test") || !strings.Contains(note.Text, "█") || strings.Contains(note.Text, "\r") {
		t.Fatalf("%q", note.Text)
	}
	if f.row("Check ntfy topic").State != ui.Done || len(f.rec.Todos) != 0 {
		t.Fatalf("%+v %v", f.rec.Rows(), f.rec.Todos)
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

func TestReplaceKeepsAKeyLeftEmptyAndStoresANewOne(t *testing.T) {
	f := newFixture(t, ghServer(t, userOK), nil, &script{secrets: []string{"github_pat_new", ""}})
	f.store.m[Agent], f.store.m[Publish] = "github_pat_old", "github_pat_pub"
	changed, err := Replace(context.Background(), f.env, Mapped{}, []Slot{Agent, Publish})
	if err != nil || len(changed) != 1 || changed[0] != Agent {
		t.Fatalf("%v %v", changed, err)
	}
	if f.store.m[Agent] != "github_pat_new" || f.store.m[Publish] != "github_pat_pub" {
		t.Fatalf("%v", f.store.m)
	}
}

// A stored key has no prompt to paste a new one into, so the line names the command that has.
func TestCheckSendsARejectedKeyToRotate(t *testing.T) {
	gh := ghServer(t, replay.Route{Method: "GET", Path: "/user", File: "bad-credentials.http"})
	f := newFixture(t, gh, linServer(t, "bad-key.http"), &script{})
	f.store.m[Linear], f.store.m[Agent], f.store.m[Publish] = "lin_api_k", "github_pat_a", "github_pat_p"
	got := Check(context.Background(), f.env, onlyA)
	if len(got) != 4 {
		t.Fatalf("%q", got)
	}
	for i, verb := range []string{"linear", "github", "github"} {
		if !strings.HasSuffix(got[i], "; run flocc keys rotate "+verb) {
			t.Errorf("%q", got[i])
		}
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
