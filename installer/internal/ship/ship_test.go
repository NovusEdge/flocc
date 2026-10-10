package ship

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	b, _ := json.Marshal(v)
	os.WriteFile(path, b, 0o644)
}

func TestPlanPluginsBundlesPrivateAndSkipsDisabled(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{
		"docket@NovusEdge": true, "curt@curt": true, "off@curt": false, "ghost@nowhere": true}})
	writeJSON(t, filepath.Join(dir, "plugins/known_marketplaces.json"), map[string]any{
		"NovusEdge": map[string]any{"source": map[string]string{"source": "github", "repo": "NovusEdge/docket"}, "installLocation": "/m/NovusEdge"},
		"curt":      map[string]any{"source": map[string]string{"source": "github", "repo": "o/curt"}, "installLocation": "/m/curt"},
	})
	p, err := planPlugins(dir, func(repo string) (bool, error) { return repo == "NovusEdge/docket", nil })
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.List.Plugins, []string{"curt@curt", "docket@NovusEdge"}) {
		t.Fatalf("plugins %v", p.List.Plugins)
	}
	if p.ArchiveFrom["NovusEdge"] != "/m/NovusEdge" || len(p.ArchiveFrom) != 1 {
		t.Fatalf("archives %v", p.ArchiveFrom)
	}
	for _, m := range p.List.Marketplaces {
		if m.Name == "curt" && m.GitHub != "o/curt" || m.Name == "NovusEdge" && m.Archive != "mp-NovusEdge.tar" {
			t.Fatalf("marketplace %+v", m)
		}
	}
	if len(p.Warnings) != 1 {
		t.Fatalf("warnings %v", p.Warnings)
	}
}

func TestResolveTargetPlainIsPassedThrough(t *testing.T) {
	d, err := resolveTarget(context.Background(), "la-smoke", "/none", nil)
	if err != nil || d.SSH != "la-smoke" || len(d.Opts) != 0 {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestResolveTargetDaytonaGetsAToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer acc" || r.Header.Get("X-Daytona-Organization-ID") != "org1" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/sandbox/la-smoke":
			json.NewEncoder(w).Encode(map[string]string{"id": "sb1"})
		case r.Method == "POST" && r.URL.Path == "/api/sandbox/sb1/ssh-access" && r.URL.Query().Get("expiresInMinutes") == "60":
			json.NewEncoder(w).Encode(map[string]string{"token": "tok", "sshCommand": "ssh tok@ssh.example.test"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	cfg := filepath.Join(t.TempDir(), "config.json")
	writeJSON(t, cfg, map[string]any{"activeProfile": "initial", "profiles": []any{map[string]any{
		"id": "initial", "activeOrganizationId": "org1",
		"api": map[string]any{"url": srv.URL + "/api", "key": "k", "token": map[string]string{"accessToken": "acc"}}}}})
	d, err := resolveTarget(context.Background(), "daytona:la-smoke", cfg, srv.Client())
	if err != nil || d.SSH != "tok@ssh.example.test" || d.Token != "tok" {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestRedactHidesTheToken(t *testing.T) {
	var b strings.Builder
	redact{&b, "tok123"}.Write([]byte("$ ssh tok123@ssh.app.daytona.io uname -m\n"))
	if strings.Contains(b.String(), "tok123") || !strings.Contains(b.String(), "<token>@ssh.app.daytona.io") {
		t.Fatalf("%q", b.String())
	}
}

func TestArchFor(t *testing.T) {
	for in, want := range map[string]string{"x86_64\n": "amd64", "aarch64": "arm64"} {
		if got, err := archFor(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	if _, err := archFor("riscv64"); err == nil {
		t.Error("riscv64: want error")
	}
}
