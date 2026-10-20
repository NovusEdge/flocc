package linearapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestRejectedKeyAndOutageAreToldApart(t *testing.T) {
	for _, tc := range []struct {
		file         string
		status       int
		rejected     bool
		unavailable  bool
		keyAdviceOut bool
	}{
		{"bad-key.http", 400, true, false, false},
		{"unavailable.http", 503, false, true, true},
		{"too-many-requests.http", 429, false, true, true},
		{"rate-limited.http", 400, false, true, true},
		{"bad-gateway.http", 502, false, true, true},
	} {
		t.Run(tc.file, func(t *testing.T) {
			_, c := serve(t, tc.file)
			_, err := CheckKey(context.Background(), c, []string{"NF"})
			var le *Error
			if !errors.As(err, &le) || le.Status != tc.status {
				t.Fatalf("%v", err)
			}
			if errors.Is(err, ErrKeyRejected) != tc.rejected || errors.Is(err, ErrUnavailable) != tc.unavailable {
				t.Fatalf("rejected=%v unavailable=%v: %v", errors.Is(err, ErrKeyRejected), errors.Is(err, ErrUnavailable), err)
			}
			if tc.keyAdviceOut && (strings.Contains(err.Error(), "Full access") || !strings.Contains(err.Error(), "could not check Linear")) {
				t.Fatal(err)
			}
		})
	}
}

func TestCanWriteDoesNotBlameTheKeyForAnOutage(t *testing.T) {
	for _, update := range []string{"unavailable.http", "too-many-requests.http", "rate-limited.http"} {
		_, c := writeServer(t, update)
		err := CanWrite(context.Background(), c, "NF-186")
		if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrKeyRejected) || strings.Contains(err.Error(), "Full access") {
			t.Fatalf("%s: %v", update, err)
		}
	}
}

func TestCanWriteBlamesTheKeyWhenLinearRefusesIt(t *testing.T) {
	_, c := writeServer(t, "update-forbidden.http")
	if err := CanWrite(context.Background(), c, "NF-186"); !errors.Is(err, ErrKeyRejected) {
		t.Fatal(err)
	}
}

func TestATransportFailureIsUnavailable(t *testing.T) {
	s := replay.New(t, "testdata")
	url := s.URL + "/graphql"
	s.Close()
	c := &Client{URL: url, Key: "lin_api_test", HTTP: s.Client()}
	if _, err := c.Viewer(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestCanWriteWritesNothingWhenTheReadFails(t *testing.T) {
	s := replay.New(t, "testdata", replay.Route{Method: "POST", Path: "/graphql", Body: "issue(id", File: "issue-not-found.http"})
	c := &Client{URL: s.URL + "/graphql", Key: "lin_api_test", HTTP: s.Client()}
	if err := CanWrite(context.Background(), c, "NF-9999"); !errors.Is(err, ErrNotFound) || len(s.Requests) != 1 {
		t.Fatalf("%v %d", err, len(s.Requests))
	}
}

func TestCanWriteWritesNothingWhenLinearReturnsNoIssue(t *testing.T) {
	s := replay.New(t, "testdata", replay.Route{Method: "POST", Path: "/graphql", Body: "issue(id", File: "issue-null.http"})
	c := &Client{URL: s.URL + "/graphql", Key: "lin_api_test", HTTP: s.Client()}
	err := CanWrite(context.Background(), c, "NF-186")
	if err == nil || len(s.Requests) != 1 {
		t.Fatalf("%v %d", err, len(s.Requests))
	}
}

// A reply that cannot be trusted as Linear's answer about the key must not read as a rejected key.
func TestUntrustworthyAuthRepliesAreUnavailableNotRejected(t *testing.T) {
	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"errors":[{"message":"`))
		w.Write(bytes.Repeat([]byte("a"), maxReply))
		w.Write([]byte(`"}]}`))
	}))
	defer oversized.Close()
	_, html401 := serve(t, "proxy-401.http")
	_, auth503 := serve(t, "auth-error-503.http")
	clients := map[string]*Client{
		"401 HTML":                    html401,
		"403 oversized":               {URL: oversized.URL, Key: "lin_api_test", HTTP: oversized.Client()},
		"AUTHENTICATION_ERROR on 503": auth503,
	}
	for name, c := range clients {
		t.Run(name, func(t *testing.T) {
			_, err := CheckKey(context.Background(), c, nil)
			if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrKeyRejected) || strings.Contains(err.Error(), "Full access") {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestAReplyOverTheLimitIsRefused(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"viewer":{"name":"`))
		w.Write(bytes.Repeat([]byte("a"), maxReply))
		w.Write([]byte(`"}}}`))
	}))
	defer big.Close()
	c := &Client{URL: big.URL, Key: "lin_api_test", HTTP: big.Client()}
	_, err := c.Viewer(context.Background())
	if err == nil || !strings.Contains(err.Error(), "over 4 MiB") || !errors.Is(err, ErrUnavailable) {
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
