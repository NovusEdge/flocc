// Package replay serves recorded HTTP responses to tests. A recording is the raw output of
// `curl --http1.1 -si`, so one captured from the real API drops in unchanged.
package replay

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Route answers requests with this method and request URI (query included) with File. When
// Body is set the request body must also contain it: GraphQL sends every query to one path. When
// Auth is set the Authorization header must equal it, so two tokens can get different answers.
type Route struct {
	Method, Path, Body, Auth, File string
}

type Request struct {
	Method, Path, Auth, Body string
}

type Server struct {
	*httptest.Server
	mu       sync.Mutex
	Requests []Request
}

// New fails the test on any request no route matches; the first matching route wins.
func New(t *testing.T, dir string, routes ...Route) *Server {
	t.Helper()
	s := &Server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req := Request{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), string(body)}
		s.mu.Lock()
		s.Requests = append(s.Requests, req)
		s.mu.Unlock()
		for _, rt := range routes {
			if rt.Method != req.Method || rt.Path != req.Path || !strings.Contains(req.Body, rt.Body) ||
				rt.Auth != "" && rt.Auth != req.Auth {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, rt.File))
			if err != nil {
				t.Errorf("replay: %v", err)
				w.WriteHeader(599)
				return
			}
			resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), r)
			if err != nil {
				t.Errorf("replay: %s: %v", rt.File, err)
				w.WriteHeader(599)
				return
			}
			for k, vs := range resp.Header {
				if k == "Content-Length" || k == "Transfer-Encoding" {
					continue
				}
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
			return
		}
		t.Errorf("replay: no recording for %s %s %s", req.Method, req.Path, req.Body)
		w.WriteHeader(599)
	}))
	t.Cleanup(s.Close)
	return s
}
