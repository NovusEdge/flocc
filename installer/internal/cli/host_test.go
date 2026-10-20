package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/linearapi"
	"github.com/NovusEdge/flocc/installer/internal/ship"
)

// hostScript ticks every repo offered, picks the first team and pastes secrets in order. With
// secrets used up it returns err, as a Ctrl-C at the prompt does, or else an empty paste.
// Confirms answer from confirms; an unscripted one fails the test, since a default of yes on a
// recheck would otherwise repeat until go test times out.
type hostScript struct {
	t         *testing.T
	secrets   []string
	err       error
	confirms  []bool
	teamAsked bool
	notes     []string
}

func (s *hostScript) Note(title, _ string) { s.notes = append(s.notes, title) }

func (s *hostScript) Confirm(title string, _ bool) (bool, error) {
	if len(s.confirms) == 0 {
		s.t.Helper()
		s.t.Fatalf("unscripted confirm: %q", title)
	}
	v := s.confirms[0]
	s.confirms = s.confirms[1:]
	return v, nil
}

func (s *hostScript) Secret(string, string) (string, error) {
	if len(s.secrets) == 0 {
		return "", s.err
	}
	v := s.secrets[0]
	s.secrets = s.secrets[1:]
	return v, nil
}

func (s *hostScript) Repos(a *answers.Answers, found []answers.Repo, _ func(string) (string, bool)) error {
	a.Repos = found
	return nil
}

func (s *hostScript) Team(ts []linearapi.Team) (linearapi.Team, error) {
	s.teamAsked = true
	return ts[0], nil
}

// writeSecrets makes the file store, so no test reaches the machine's keyring.
func writeSecrets(t *testing.T, home, body string) {
	t.Helper()
	p := filepath.Join(home, ".config/flocc/secrets.toml")
	os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const allKeys = "linear_key = 'lin_api_k'\nagent_token = 'github_pat_agent'\npublish_token = 'github_pat_publish'\nntfy_topic = 'flocc-t'\n"

// fakeLinear keeps labels and issues, so a second host add finds what the first one made.
// issueErr, when set, is the error every issue lookup gets, as a rate limit or outage gives.
type fakeLinear struct {
	mu                                         sync.Mutex
	labels                                     []string
	issues                                     map[string]string // key -> title
	issueErr                                   string
	labelCreates, issueCreates, finds, updates int
}

func (f *fakeLinear) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var req struct {
		Query     string
		Variables map[string]any
	}
	json.NewDecoder(r.Body).Decode(&req)
	q, v := req.Query, req.Variables
	issue := func(key string) map[string]any {
		return map[string]any{"id": "id-" + key, "identifier": key, "title": f.issues[key], "state": map[string]any{"type": "unstarted"}}
	}
	var data any
	switch {
	case strings.Contains(q, "issueUpdate"):
		// The write probe: the title must come back unchanged.
		if title := f.issues[strings.TrimPrefix(v["id"].(string), "id-")]; v["title"] != title {
			http.Error(w, "the probe changed the title", 400)
			return
		}
		f.updates++
		data = map[string]any{"issueUpdate": map[string]any{"success": true}}
	case strings.Contains(q, "issueLabelCreate"):
		f.labels = append(f.labels, v["name"].(string))
		f.labelCreates++
		data = map[string]any{"issueLabelCreate": map[string]any{"success": true}}
	case strings.Contains(q, "issueCreate"):
		key := fmt.Sprintf("NF-%d", 100+len(f.issues))
		f.issues[key] = v["title"].(string)
		f.issueCreates++
		data = map[string]any{"issueCreate": map[string]any{"issue": issue(key)}}
	case strings.Contains(q, "issueLabels"):
		var nodes []any
		for _, n := range v["names"].([]any) {
			if slices.Contains(f.labels, n.(string)) {
				nodes = append(nodes, map[string]any{"name": n, "team": map[string]any{"key": "NF"}})
			}
		}
		data = map[string]any{"teams": map[string]any{"nodes": []any{map[string]any{"id": "team-nf"}}},
			"issueLabels": map[string]any{"nodes": nodes}}
	case strings.Contains(q, "issues(first"):
		f.finds++
		var nodes []any
		for k, title := range f.issues {
			if title == v["title"] {
				nodes = append(nodes, issue(k))
			}
		}
		data = map[string]any{"issues": map[string]any{"nodes": nodes}}
	case strings.Contains(q, "issue(id"):
		key := v["id"].(string)
		msg := f.issueErr
		if _, ok := f.issues[key]; !ok && msg == "" {
			msg = "Entity not found: Issue"
		}
		if msg != "" {
			json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": msg}}})
			return
		}
		data = map[string]any{"issue": issue(key)}
	case strings.Contains(q, "viewer"):
		data = map[string]any{"viewer": map[string]any{"name": "Ada", "email": "ada@example.test"},
			"teams": map[string]any{"nodes": []any{map[string]any{"id": "team-nf", "key": "NF", "name": "Nimblefox"}}}}
	default:
		http.Error(w, "unexpected query", 400)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// fakeGitHub lists repo o/a; only github_pat_publish may push and open pull requests.
func fakeGitHub(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publish := r.Header.Get("Authorization") == "Bearer github_pat_publish"
		switch {
		case r.URL.Path == "/user":
			w.Write([]byte(`{"login":"o"}`))
		case r.URL.Path == "/user/repos":
			w.Write([]byte(`[{"name":"a","html_url":"https://github.com/o/a","default_branch":"main","private":true,"size":3,"owner":{"login":"o"}}]`))
		case r.Method == "GET" && r.URL.Path == "/repos/o/a":
			w.Write([]byte(`{"default_branch":"main","private":true}`))
		case publish && (strings.HasSuffix(r.URL.Path, "/git/refs") || strings.HasSuffix(r.URL.Path, "/pulls")):
			w.WriteHeader(422)
		case strings.HasSuffix(r.URL.Path, "/git/refs"):
			// Only this message marks a refusal: a rate limit or SSO is a 403 too.
			w.WriteHeader(403)
			w.Write([]byte(`{"message":"Resource not accessible by personal access token"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func addDeps(t *testing.T, secrets string) (*deps, *fakeLinear, *hostScript) {
	t.Helper()
	d, _, _ := testDeps(t)
	lin := &fakeLinear{issues: map[string]string{}}
	ls := httptest.NewServer(lin)
	t.Cleanup(ls.Close)
	writeSecrets(t, d.home, secrets)
	p := &hostScript{t: t}
	d.store = func() keys.Store { return keys.Open(d.home, io.Discard) }
	d.prompt = p
	d.http = http.DefaultClient
	d.githubAPI, d.linearAPI = fakeGitHub(t).URL, ls.URL
	ntfy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(ntfy.Close)
	d.ntfyBase, d.newTopic = ntfy.URL, func() string { return "flocc-t" }
	d.open, d.qr = func(string) {}, func(string) {}
	return d, lin, p
}

func loadHost(t *testing.T, d *deps, name string) hosts.Host {
	t.Helper()
	h, err := hosts.Load(hosts.Dir(d.home), name)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHostAddMakesTheDigestAndLabelsOnce(t *testing.T) {
	d, lin, p := addDeps(t, allKeys)
	for range 2 {
		if err := hostAdd(context.Background(), d, "h1", ""); err != nil {
			t.Fatal(err)
		}
	}
	if lin.labelCreates != 4 || lin.issueCreates != 1 || p.teamAsked {
		t.Fatalf("labels %d issues %d team asked %v", lin.labelCreates, lin.issueCreates, p.teamAsked)
	}
	if !slices.Equal(lin.labels, []string{"claude", "escalation", "needs-decision", "a"}) || lin.issues["NF-100"] != digestTitle {
		t.Fatalf("%v %v", lin.labels, lin.issues)
	}
	// The key check after each add proved the Linear key can write, on the digest.
	if lin.updates != 2 {
		t.Fatalf("write probes %d", lin.updates)
	}
	h := loadHost(t, d, "h1")
	if h.SSH != "root@h1" || h.Hostname != "h1" || h.TeamKey != "NF" || h.DigestIssue != "NF-100" ||
		len(h.Repos) != 1 || h.Repos[0].URL != "https://github.com/o/a" {
		t.Fatalf("%+v", h)
	}
	if hostFile(d, "h1") != filepath.Join(d.home, ".config/flocc/hosts/h1.toml") {
		t.Fatal(hostFile(d, "h1"))
	}
}

func TestHostAddSharesTheDigestOfAHostOnTheSameTeam(t *testing.T) {
	d, lin, _ := addDeps(t, allKeys)
	lin.issues["NF-7"] = "digest"
	b := hosts.Host{Name: "b", SSH: "root@b", Answers: answers.Answers{TeamKey: "NF", DigestIssue: "NF-7"}}
	if err := hosts.Save(hosts.Dir(d.home), b); err != nil {
		t.Fatal(err)
	}
	if err := hostAdd(context.Background(), d, "h1", "root@10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	h := loadHost(t, d, "h1")
	if h.DigestIssue != "NF-7" || h.SSH != "root@10.0.0.5" || lin.issueCreates != 0 || lin.finds != 0 {
		t.Fatalf("%+v creates %d finds %d", h, lin.issueCreates, lin.finds)
	}
}

// A blip at Linear must not look like a missing digest: an imported digest is not titled
// "flocc digest", so falling through would make a second one and rewrite the host file.
func TestADigestLookupErrorIsNotAMissingDigest(t *testing.T) {
	d, lin, _ := addDeps(t, allKeys)
	lin.issueErr = "Rate limit exceeded"
	h1 := hosts.Host{Name: "h1", SSH: "root@h1", Answers: answers.Answers{TeamKey: "NF", DigestIssue: "NF-186"}}
	if err := hosts.Save(hosts.Dir(d.home), h1); err != nil {
		t.Fatal(err)
	}
	err := hostAdd(context.Background(), d, "h1", "")
	if err == nil || !strings.Contains(err.Error(), "Rate limit") || lin.issueCreates != 0 || lin.finds != 0 {
		t.Fatalf("%v creates %d finds %d", err, lin.issueCreates, lin.finds)
	}
	if h := loadHost(t, d, "h1"); h.DigestIssue != "NF-186" {
		t.Fatalf("%+v", h)
	}
}

func TestHostAddSkipsADigestAnotherHostNamesButLinearNoLongerHas(t *testing.T) {
	d, lin, _ := addDeps(t, allKeys)
	b := hosts.Host{Name: "b", SSH: "root@b", Answers: answers.Answers{TeamKey: "NF", DigestIssue: "NF-7"}}
	if err := hosts.Save(hosts.Dir(d.home), b); err != nil {
		t.Fatal(err)
	}
	// NF-7 is unknown to fakeLinear, so Linear answers "Entity not found", as for a deleted issue.
	if err := hostAdd(context.Background(), d, "h1", ""); err != nil {
		t.Fatal(err)
	}
	if h := loadHost(t, d, "h1"); h.DigestIssue == "NF-7" || lin.issueCreates != 1 {
		t.Fatalf("%+v creates %d", h, lin.issueCreates)
	}
}

// A fresh desktop: host add asks for each key where it needs it, instead of stopping.
func TestHostAddAsksForAMissingKeyOnTheSpot(t *testing.T) {
	d, lin, p := addDeps(t, "")
	p.secrets = []string{"lin_api_k", "github_pat_agent", "github_pat_publish"}
	p.confirms = []bool{true} // the ntfy test push arrived
	if err := hostAdd(context.Background(), d, "h1", ""); err != nil {
		t.Fatal(err)
	}
	st := d.store()
	for slot, want := range map[keys.Slot]string{keys.Linear: "lin_api_k", keys.Agent: "github_pat_agent", keys.Publish: "github_pat_publish", keys.Ntfy: "flocc-t"} {
		if v, _ := st.Get(slot); v != want {
			t.Errorf("%s: %q", slot, v)
		}
	}
	if h := loadHost(t, d, "h1"); h.DigestIssue != "NF-100" || lin.labelCreates != 4 {
		t.Fatalf("%+v labels %d", h, lin.labelCreates)
	}
}

func TestHostAddStopsAtAnEmptyPasteBeforeWritingAnything(t *testing.T) {
	d, lin, _ := addDeps(t, "")
	err := hostAdd(context.Background(), d, "h1", "")
	if !errors.Is(err, keys.ErrStopped) || lin.labelCreates != 0 {
		t.Fatal(err)
	}
	if _, err := hosts.Load(hosts.Dir(d.home), "h1"); err == nil {
		t.Fatal("a host file was written without keys")
	}
}

func TestHostAddSaysWhichKeyMissesTheNewRepo(t *testing.T) {
	d, _, _ := addDeps(t, strings.Replace(allKeys, "github_pat_publish", "github_pat_weak", 1))
	err := hostAdd(context.Background(), d, "h1", "")
	if err == nil || !strings.Contains(err.Error(), "o/a: the token cannot push") || !strings.Contains(err.Error(), "the token stays the same") {
		t.Fatal(err)
	}
	loadHost(t, d, "h1") // saved even though a key does not cover it yet
}

const hostAnswers = "team_key = \"NF\"\ndigest_issue = \"NF-186\"\nntfy_topic = \"flocc-host\"\n\n" +
	"[[repos]]\nname = \"a\"\nurl = \"https://github.com/o/a\"\nbase_branch = \"main\"\ncheck = \"just check\"\n"

func importDeps(t *testing.T) (*deps, *fakeSSH, *fakeLinear) {
	t.Helper()
	d, lin, _ := addDeps(t, "")
	lin.issues["NF-186"] = "Digest"
	f := &fakeSSH{out: map[string]string{"root@h1": ""}, files: map[string]string{".config/flocc/answers.toml": hostAnswers}}
	d.run = f
	d.resolve = func(_ context.Context, target string) (ship.Dest, error) { return ship.Dest{SSH: target}, nil }
	return d, f, lin
}

func TestHostImportCopiesTheHostsAnswersAndKeepsItsTopic(t *testing.T) {
	d, f, _ := importDeps(t)
	if code := run(context.Background(), d, []string{"host", "import", "-H", "h1"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	h := loadHost(t, d, "h1")
	if h.TeamKey != "NF" || h.DigestIssue != "NF-186" || h.NtfyTopic != "" || len(h.Repos) != 1 {
		t.Fatalf("%+v", h)
	}
	if topic, _ := d.store().Get(keys.Ntfy); topic != "flocc-host" {
		t.Fatalf("topic %q", topic)
	}
	words := shWords(t, f.runs[0].Args[len(f.runs[0].Args)-1])
	if script := words[len(words)-1]; script != "cd && cat .config/flocc/answers.toml" {
		t.Fatalf("%q", script)
	}
}

// Moving a host onto the desktop store needs no new tokens: each key comes through the same
// checks as a paste, and one that fails them is left for flocc keys.
func TestHostImportTakesTheHostsKeysThroughThePasteChecks(t *testing.T) {
	d, f, lin := importDeps(t)
	f.files[".config/flocc/linear.key"] = "lin_api_k\n"
	f.files[".config/flocc/gh-agent.token"] = "github_pat_agent\n"
	f.files[".config/flocc/gh-publish.token"] = "github_pat_weak\n"
	if code := run(context.Background(), d, []string{"host", "import", "-H", "h1"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	st := d.store()
	if v, _ := st.Get(keys.Linear); v != "lin_api_k" {
		t.Fatalf("linear %q", v)
	}
	if v, _ := st.Get(keys.Agent); v != "github_pat_agent" {
		t.Fatalf("agent %q", v)
	}
	if v, _ := st.Get(keys.Publish); v != "" {
		t.Fatalf("a publish token that cannot push was taken: %q", v)
	}
	if lin.updates != 1 {
		t.Fatalf("the Linear key's write probe ran %d times", lin.updates)
	}
	out := d.out.(*bytes.Buffer).String()
	if !strings.Contains(out, "Took the GitHub agent token (read-only) from h1") || !strings.Contains(out, "o/a: the token cannot push") {
		t.Fatal(out)
	}
	for _, secret := range []string{"lin_api_k", "github_pat_agent", "github_pat_weak"} {
		if strings.Contains(out, secret) {
			t.Errorf("%s printed", secret)
		}
	}
	// Each key file is read without a terminal, so a forced pty cannot echo it.
	for _, c := range f.runs {
		if !slices.Contains(c.Args, "-T") {
			t.Errorf("no -T: %s", c)
		}
	}
}

// An outage says nothing about the host's key: it is checked again while the owner asks, never
// stored unchecked, and the owner is not sent to make a new one.
func TestHostImportRechecksAKeyLinearCouldNotCheck(t *testing.T) {
	d, f, lin := importDeps(t)
	lin.issueErr = "Rate limit exceeded"
	f.files[".config/flocc/linear.key"] = "lin_api_k\n"
	p := d.prompt.(*hostScript)
	p.confirms = []bool{true, false}
	if code := run(context.Background(), d, []string{"host", "import", "-H", "h1"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if v, _ := d.store().Get(keys.Linear); v != "" {
		t.Fatalf("stored unchecked: %q", v)
	}
	out := d.out.(*bytes.Buffer).String()
	if len(p.confirms) != 0 || strings.Count(out, "Rate limit exceeded") != 2 || strings.Contains(out, "new one") {
		t.Fatalf("confirms left %v\n%s", p.confirms, out)
	}
}
