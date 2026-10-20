package linearapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/replay"
)

func serve(t *testing.T, file string) (*replay.Server, *Client) {
	s := replay.New(t, "testdata", replay.Route{Method: "POST", Path: "/graphql", Body: "viewer", File: file})
	return s, &Client{URL: s.URL + "/graphql", Key: "lin_api_test", HTTP: s.Client()}
}

func TestCheckKeyAcceptsAKeyThatSeesTheTeam(t *testing.T) {
	s, c := serve(t, "viewer-ok.http")
	v, err := CheckKey(context.Background(), c, []string{"NF"})
	if err != nil || v.Name != "Ada Lovelace" || len(v.Teams) != 1 || v.Teams[0].ID != "team-nf" {
		t.Fatalf("%+v %v", v, err)
	}
	// Personal API keys go in the header bare, without "Bearer".
	if s.Requests[0].Auth != "lin_api_test" {
		t.Fatalf("%+v", s.Requests[0])
	}
}

func TestCheckKeyWithNoHostsNeedsOnlyATeam(t *testing.T) {
	_, c := serve(t, "viewer-other-team.http")
	if _, err := CheckKey(context.Background(), c, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCheckKeyRefusesAKeyForAnotherTeam(t *testing.T) {
	_, c := serve(t, "viewer-other-team.http")
	_, err := CheckKey(context.Background(), c, []string{"NF"})
	if err == nil || !strings.Contains(err.Error(), "cannot see team NF") || !strings.Contains(err.Error(), "XY") {
		t.Fatal(err)
	}
}

func TestCheckKeyRefusesARejectedKey(t *testing.T) {
	_, c := serve(t, "bad-key.http")
	_, err := CheckKey(context.Background(), c, []string{"NF"})
	if err == nil || !strings.Contains(err.Error(), "Authentication required") {
		t.Fatal(err)
	}
}

func writeServer(t *testing.T, update string) (*replay.Server, *Client) {
	s := replay.New(t, "testdata",
		replay.Route{Method: "POST", Path: "/graphql", Body: "issue(id", File: "issue-ok.http"},
		replay.Route{Method: "POST", Path: "/graphql", Body: "issueUpdate", File: update})
	return s, &Client{URL: s.URL + "/graphql", Key: "lin_api_test", HTTP: s.Client()}
}

// The probe writes back the title it read, so the issue is left as it was.
func TestCanWriteSetsTheTitleItRead(t *testing.T) {
	s, c := writeServer(t, "update-ok.http")
	if err := CanWrite(context.Background(), c, "NF-186"); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Variables struct{ ID, Title string }
	}
	json.Unmarshal([]byte(s.Requests[1].Body), &body)
	if body.Variables.ID != "issue-186" || body.Variables.Title != "flocc digest" {
		t.Fatalf("%s", s.Requests[1].Body)
	}
}

func TestCanWriteRefusesAReadOnlyKey(t *testing.T) {
	_, c := writeServer(t, "update-forbidden.http")
	err := CanWrite(context.Background(), c, "NF-186")
	if err == nil || !strings.Contains(err.Error(), "cannot write") || !strings.Contains(err.Error(), "Full access") {
		t.Fatal(err)
	}
}

func TestAnIssueLinearDoesNotHaveIsErrNotFound(t *testing.T) {
	s := replay.New(t, "testdata", replay.Route{Method: "POST", Path: "/graphql", Body: "issue(id", File: "issue-not-found.http"})
	c := &Client{URL: s.URL + "/graphql", Key: "lin_api_test", HTTP: s.Client()}
	if _, err := c.Issue(context.Background(), "NF-9999"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
