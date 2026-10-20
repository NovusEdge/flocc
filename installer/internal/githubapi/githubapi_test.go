package githubapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/replay"
	"github.com/NovusEdge/flocc/installer/internal/steps"
)

func get(path, file string) replay.Route { return replay.Route{Method: "GET", Path: path, File: file} }
func post(path, file string) replay.Route {
	return replay.Route{Method: "POST", Path: path, File: file}
}

func serve(t *testing.T, routes ...replay.Route) (*replay.Server, *Client) {
	s := replay.New(t, "testdata", routes...)
	return s, &Client{Base: s.URL, Token: "github_pat_test", HTTP: s.Client()}
}

var repoA = []Target{{Slug: "o/a", Base: "main"}}

func TestExpiryReadsTheTokenHeader(t *testing.T) {
	_, c := serve(t, get("/user", "user-ok.http"))
	got, err := c.Expiry(context.Background())
	if want := time.Date(2027, 10, 21, 8, 0, 0, 0, time.UTC); err != nil || !got.Equal(want) {
		t.Fatalf("%v %v", got, err)
	}
}

func TestATokenWithoutExpiryHasTheZeroTime(t *testing.T) {
	_, c := serve(t, get("/user", "user-never.http"))
	if got, err := c.Expiry(context.Background()); err != nil || !got.IsZero() {
		t.Fatalf("%v %v", got, err)
	}
}

func TestAnExpiredTokenIsRefusedOutright(t *testing.T) {
	_, c := serve(t, get("/user", "bad-credentials.http"))
	if _, err := CheckAgent(context.Background(), c, repoA); !errors.Is(err, ErrBadCredentials) {
		t.Fatal(err)
	}
}

func TestCheckAgentAcceptsAReadOnlyToken(t *testing.T) {
	s, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/a", "repo-ok.http"),
		post("/repos/o/a/git/refs", "refs-forbidden.http"))
	got, err := CheckAgent(context.Background(), c, repoA)
	if err != nil || len(got.Problems) != 0 || got.Expires.Year() != 2027 {
		t.Fatalf("%+v %v", got, err)
	}
	if s.Requests[0].Auth != "Bearer github_pat_test" {
		t.Fatalf("%+v", s.Requests[0])
	}
}

func TestCheckAgentRefusesATokenThatCanPush(t *testing.T) {
	_, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/a", "repo-ok.http"),
		post("/repos/o/a/git/refs", "refs-unprocessable.http"))
	got, err := CheckAgent(context.Background(), c, repoA)
	if err != nil || len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "o/a: the token can push") {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCheckNamesARepoTheTokenCannotRead(t *testing.T) {
	_, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/b", "not-found.http"))
	got, err := CheckAgent(context.Background(), c, []Target{{Slug: "o/b", Base: "main"}})
	if err != nil || len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "o/b: the token cannot read it") {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCheckPublishAcceptsPushAndPullRequests(t *testing.T) {
	_, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/a", "repo-ok.http"),
		post("/repos/o/a/git/refs", "refs-unprocessable.http"), post("/repos/o/a/pulls", "pulls-unprocessable.http"))
	if got, err := CheckPublish(context.Background(), c, repoA); err != nil || len(got.Problems) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCheckPublishRefusesATokenThatCannotPush(t *testing.T) {
	_, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/a", "repo-ok.http"),
		post("/repos/o/a/git/refs", "refs-forbidden.http"), post("/repos/o/a/pulls", "pulls-unprocessable.http"))
	got, err := CheckPublish(context.Background(), c, repoA)
	if err != nil || len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "cannot push") {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCheckPublishRefusesATokenThatCannotOpenPullRequests(t *testing.T) {
	_, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/a", "repo-ok.http"),
		post("/repos/o/a/git/refs", "refs-unprocessable.http"), post("/repos/o/a/pulls", "pulls-forbidden.http"))
	got, err := CheckPublish(context.Background(), c, repoA)
	if err != nil || len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "cannot open pull requests") {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestARateLimitedRefProbeIsNotAPassForTheAgentToken(t *testing.T) {
	_, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/a", "repo-ok.http"),
		post("/repos/o/a/git/refs", "refs-ratelimited.http"))
	got, err := CheckAgent(context.Background(), c, repoA)
	if err == nil || !strings.Contains(err.Error(), "secondary rate limit") || len(got.Problems) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestARateLimitedRefProbeDoesNotBlameThePublishToken(t *testing.T) {
	_, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/a", "repo-ok.http"),
		post("/repos/o/a/git/refs", "refs-ratelimited.http"))
	got, err := CheckPublish(context.Background(), c, repoA)
	if err == nil || len(got.Problems) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestARateLimitedPullProbeDoesNotBlameThePublishToken(t *testing.T) {
	_, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/a", "repo-ok.http"),
		post("/repos/o/a/git/refs", "refs-unprocessable.http"), post("/repos/o/a/pulls", "pulls-ratelimited.http"))
	got, err := CheckPublish(context.Background(), c, repoA)
	if err == nil || !strings.Contains(err.Error(), "secondary rate limit") || len(got.Problems) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestAnOversizedResponseIsAnError(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", maxBody+1)))
	}))
	defer s.Close()
	c := &Client{Base: s.URL, Token: "github_pat_test", HTTP: s.Client()}
	if _, err := c.Expiry(context.Background()); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatal(err)
	}
}

func TestListReposStopsAtANextLinkThatPointsAtItself(t *testing.T) {
	q := "/user/repos?per_page=100&affiliation=owner,collaborator,organization_member"
	_, c := serve(t, get(q, "repos-loop.http"))
	if _, err := c.ListRepos(context.Background()); err == nil || !strings.Contains(err.Error(), "repeats") {
		t.Fatal(err)
	}
}

func TestListReposStopsAfterMaxPages(t *testing.T) {
	n := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Link", `</user/repos?page=`+strings.Repeat("1", n)+`>; rel="next"`)
		w.Write([]byte("[]"))
	}))
	defer s.Close()
	c := &Client{Base: s.URL, Token: "github_pat_test", HTTP: s.Client()}
	if _, err := c.ListRepos(context.Background()); err == nil || n != maxPages {
		t.Fatalf("%d pages, %v", n, err)
	}
}

// The probes are the only writes a check sends, and GitHub must refuse both: no ref can point at
// the zero SHA, and no pull request can come from a branch that does not exist.
func TestChecksSendNothingButReadsAndTheTwoProbes(t *testing.T) {
	s, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/a", "repo-ok.http"),
		post("/repos/o/a/git/refs", "refs-unprocessable.http"), post("/repos/o/a/pulls", "pulls-unprocessable.http"))
	if _, err := CheckPublish(context.Background(), c, repoA); err != nil {
		t.Fatal(err)
	}
	for _, r := range s.Requests {
		switch {
		case r.Method == "GET":
		case strings.HasSuffix(r.Path, "/git/refs"):
			var body struct{ SHA string }
			json.Unmarshal([]byte(r.Body), &body)
			if body.SHA != strings.Repeat("0", 40) {
				t.Fatalf("ref probe with a real SHA: %s", r.Body)
			}
		case strings.HasSuffix(r.Path, "/pulls"):
			var body struct{ Head string }
			json.Unmarshal([]byte(r.Body), &body)
			if body.Head != probeHead {
				t.Fatalf("pull probe from a real branch: %s", r.Body)
			}
		default:
			t.Fatalf("unexpected write %s %s", r.Method, r.Path)
		}
	}
}

func TestListReposFollowsTheNextPageOnTheSameServer(t *testing.T) {
	q := "/user/repos?per_page=100&affiliation=owner,collaborator,organization_member"
	_, c := serve(t, get(q, "repos-page1.http"), get(q+"&page=2", "repos-page2.http"))
	raw, err := c.ListRepos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	repos, _, _, err := steps.ParseRepos(raw)
	names := []string{}
	for _, r := range repos {
		names = append(names, r.Name)
	}
	if err != nil || !slices.Equal(names, []string{"a", "b"}) || repos[1].BaseBranch != "trunk" {
		t.Fatalf("%+v %v", repos, err)
	}
}
