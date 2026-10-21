package tailnet

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const secret = "tskey-client-SECRET"

func server(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return &Client{HTTP: s.Client(), Base: s.URL, ID: "cid", Secret: secret}
}

func tokenOr(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/api/v2/oauth/token" {
		return false
	}
	w.Write([]byte(`{"access_token":"tok-1","expires_in":3600}`))
	return true
}

func TestTokenExchangesTheClientAndCaches(t *testing.T) {
	var calls atomic.Int32
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		r.ParseForm()
		if r.Method != "POST" || r.URL.Path != "/api/v2/oauth/token" ||
			r.PostForm.Get("client_id") != "cid" || r.PostForm.Get("client_secret") != secret ||
			r.PostForm.Get("grant_type") != "client_credentials" {
			t.Errorf("request = %s %s %v", r.Method, r.URL.Path, r.PostForm)
		}
		tokenOr(w, r)
	})
	for range 2 {
		if tok, err := c.Token(context.Background()); err != nil || tok != "tok-1" {
			t.Fatalf("token = %q, %v", tok, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("token endpoint called %d times", calls.Load())
	}
	c.tokenExpires = time.Now().Add(30 * time.Second) // inside the one-minute margin
	c.Token(context.Background())
	if calls.Load() != 2 {
		t.Fatalf("expiring token not refreshed: %d calls", calls.Load())
	}
}

func TestRefusedTokenNamesTheStatusNotTheSecret(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, "invalid client "+secret)
	})
	_, err := c.Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), secret) {
		t.Fatalf("err = %v", err)
	}
}

func TestTaggedKeyAsksForAOneOffPreauthorizedKey(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenOr(w, r) {
			return
		}
		if r.URL.Path != "/api/v2/tailnet/-/keys" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer tok-1" {
			t.Errorf("request = %s %s %s", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var got map[string]any
		json.NewDecoder(r.Body).Decode(&got)
		want := `{"capabilities":{"devices":{"create":{"ephemeral":false,"preauthorized":true,"reusable":false,"tags":["tag:flocc-host"]}}},"expirySeconds":600}`
		gotJSON, _ := json.Marshal(got)
		if string(gotJSON) != want {
			t.Errorf("body = %s", gotJSON)
		}
		io.WriteString(w, `{"key":"tskey-auth-xyz"}`)
	})
	key, err := c.TaggedKey(context.Background(), Tag, 10*time.Minute)
	if err != nil || key != "tskey-auth-xyz" {
		t.Fatalf("key = %q, %v", key, err)
	}
}

func TestTaggedKeyMapsAnUnpermittedTag(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenOr(w, r) {
			return
		}
		w.WriteHeader(403)
		io.WriteString(w, `requested tags [tag:flocc-host] are invalid or not permitted`)
	})
	_, err := c.TaggedKey(context.Background(), Tag, time.Minute)
	if !errors.Is(err, ErrTagNotPermitted) {
		t.Fatalf("err = %v", err)
	}
}

func TestOtherRefusalsKeepTheirStatusAndLeakNothing(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenOr(w, r) {
			return
		}
		w.WriteHeader(500)
		io.WriteString(w, "boom tok-1 "+secret)
	})
	_, err := c.TaggedKey(context.Background(), Tag, time.Minute)
	if err == nil || errors.Is(err, ErrTagNotPermitted) || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v", err)
	}
	for _, s := range []string{secret, "tok-1"} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error leaks %q: %v", s, err)
		}
	}
}

func TestSetTagsPostsTheList(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenOr(w, r) {
			return
		}
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/api/v2/device/n123/tags" || r.Method != "POST" || string(b) != `{"tags":["tag:flocc-host"]}` {
			t.Errorf("request = %s %s %s", r.Method, r.URL.Path, b)
		}
	})
	if err := c.SetTags(context.Background(), "n123", []string{Tag}); err != nil {
		t.Fatal(err)
	}
}

func TestSetTagsMapsAnUnpermittedTag(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenOr(w, r) {
			return
		}
		w.WriteHeader(400)
		io.WriteString(w, `tag:flocc-host is not defined in tagOwners`)
	})
	if err := c.SetTags(context.Background(), "n1", []string{Tag}); !errors.Is(err, ErrTagNotPermitted) {
		t.Fatalf("err = %v", err)
	}
}

func TestPolicyAsksForJSON(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenOr(w, r) {
			return
		}
		if r.URL.Path != "/api/v2/tailnet/-/acl" || r.Method != "GET" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("request = %s %s %s", r.Method, r.URL.Path, r.Header.Get("Accept"))
		}
		io.WriteString(w, `{"acls":[]}`)
	})
	b, err := c.Policy(context.Background())
	if err != nil || string(b) != `{"acls":[]}` {
		t.Fatalf("policy = %s, %v", b, err)
	}
}
