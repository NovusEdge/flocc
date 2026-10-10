package ship

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

func TestRemoteCmdMakesTheBinaryExecutable(t *testing.T) {
	want := "chmod 755 /tmp/la-setup/la-setup && sudo /tmp/la-setup/la-setup --answers /tmp/la-setup/a.toml --skip-interactive"
	if got := remoteCmd("/x/a.toml", true); got != want {
		t.Fatalf("%q", got)
	}
}

func TestShipErrorsDoNotLeakTheToken(t *testing.T) {
	dest := Dest{SSH: "tok123@ssh.example.test", Token: "tok123"}
	f := host.NewFake().On("ssh tok123@ssh.example.test uname -m", 255, "")
	err := ship(context.Background(), f, t.TempDir(), dest, "", false)
	if err == nil || strings.Contains(err.Error(), "tok123") || !strings.Contains(err.Error(), "<token>") {
		t.Fatalf("%v", err)
	}
}

func TestShipClearsTheStageWithSudoOnATerminal(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "build"), 0o755)
	os.WriteFile(filepath.Join(root, "build", "la-setup-linux-amd64"), []byte("bin"), 0o755)
	cfg := t.TempDir()
	writeJSON(t, filepath.Join(cfg, "settings.json"), map[string]any{})
	writeJSON(t, filepath.Join(cfg, "plugins/known_marketplaces.json"), map[string]any{})
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	f := &bundleOK{host.NewFake().On("ssh h uname -m", 0, "x86_64\n")}
	ship(context.Background(), f, root, Dest{SSH: "h"}, "", false)
	if !f.Ran("interactive: ssh -t h sudo rm -rf /tmp/la-setup && mkdir -p /tmp/la-setup") {
		t.Fatalf("%v", f.Calls)
	}
}

// bundleOK accepts git commands, whose paths hold a random temp dir.
type bundleOK struct{ *host.Fake }

func (b *bundleOK) Run(ctx context.Context, c host.Cmd) (host.Result, error) {
	if c.Name == "git" {
		return host.Result{}, nil
	}
	return b.Fake.Run(ctx, c)
}

func TestStageFilesArchivesPrivateMarketplaces(t *testing.T) {
	root, dir, cfg := t.TempDir(), t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(root, "build"), 0o755)
	os.WriteFile(filepath.Join(root, "build", "la-setup-linux-amd64"), []byte("bin"), 0o755)
	writeJSON(t, filepath.Join(cfg, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{"x@X": true}})
	writeJSON(t, filepath.Join(cfg, "plugins/known_marketplaces.json"), map[string]any{
		"X": map[string]any{"source": map[string]string{"source": "github", "repo": "o/x"}, "installLocation": "/m/X"}})
	f := host.NewFake().
		On("git -C "+root+" bundle create "+dir+"/linear-agents.bundle HEAD", 0, "").
		On("git -C /m/X archive --format=tar -o "+dir+"/mp-X.tar HEAD", 0, "").
		On("git -C /m/X rev-parse HEAD", 0, "abc\n")
	err := stageFiles(context.Background(), f, root, dir, "amd64", "", cfg, func(string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "plugins.json"))
	var got struct {
		Marketplaces []struct{ Name, Archive, Commit string }
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	if len(got.Marketplaces) != 1 || got.Marketplaces[0].Archive != "mp-X.tar" || got.Marketplaces[0].Commit != "abc" {
		t.Fatalf("%s", b)
	}
}

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
	d, err := resolveTarget(context.Background(), "la-smoke", "/none", nil, waiter{})
	if err != nil || d.SSH != "la-smoke" || len(d.Opts) != 0 {
		t.Fatalf("%+v %v", d, err)
	}
}

// daytonaFake serves the sandbox states in order (the last one repeats) and
// records the polls seen before the SSH token was requested.
func daytonaFake(t *testing.T, states ...string) (cfg string, srv *httptest.Server, polls, tokens *int) {
	t.Helper()
	polls, tokens = new(int), new(int)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer acc" || r.Header.Get("X-Daytona-Organization-ID") != "org1" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/sandbox/la-smoke":
			json.NewEncoder(w).Encode(map[string]string{"id": "sb1", "state": states[0]})
		case r.Method == "GET" && r.URL.Path == "/api/sandbox/sb1":
			st := states[min(*polls, len(states)-1)]
			*polls++
			if st == "401" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"state": st})
		case r.Method == "POST" && r.URL.Path == "/api/sandbox/sb1/ssh-access" && r.URL.Query().Get("expiresInMinutes") == "60":
			*tokens++
			json.NewEncoder(w).Encode(map[string]string{"token": "tok", "sshCommand": "ssh tok@ssh.example.test"})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	cfg = filepath.Join(t.TempDir(), "config.json")
	writeJSON(t, cfg, map[string]any{"activeProfile": "initial", "profiles": []any{map[string]any{
		"id": "initial", "activeOrganizationId": "org1",
		"api": map[string]any{"url": srv.URL + "/api", "key": "k", "token": map[string]string{"accessToken": "acc"}}}}})
	return cfg, srv, polls, tokens
}

// fakeWaiter has a clock that only the sleep advances; sleeps counts them and progress collects the log.
func fakeWaiter() (w waiter, sleeps *int, progress *strings.Builder) {
	var elapsed time.Duration
	sleeps, progress = new(int), new(strings.Builder)
	return waiter{
		sleep: func(ctx context.Context, d time.Duration) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			elapsed += d
			*sleeps++
			return nil
		},
		now: func() time.Time { return time.Unix(0, 0).Add(elapsed) },
		log: progress,
	}, sleeps, progress
}

func TestResolveTargetDaytonaGetsAToken(t *testing.T) {
	w, sleeps, progress := fakeWaiter()
	cfg, srv, _, tokens := daytonaFake(t, "started")
	d, err := resolveTarget(context.Background(), "daytona:la-smoke", cfg, srv.Client(), w)
	if err != nil || d.SSH != "tok@ssh.example.test" || d.Token != "tok" || *tokens != 1 || *sleeps != 0 || progress.Len() != 0 {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestResolveTargetDaytonaWaitsForStarted(t *testing.T) {
	w, sleeps, progress := fakeWaiter()
	cfg, srv, polls, tokens := daytonaFake(t, "CREATING", "creating", "PULLING_SNAPSHOT", "STARTED")
	if _, err := resolveTarget(context.Background(), "daytona:la-smoke", cfg, srv.Client(), w); err != nil {
		t.Fatal(err)
	}
	if *polls != 4 || *sleeps != 3 || *tokens != 1 {
		t.Fatalf("polls %d sleeps %d tokens %d", *polls, *sleeps, *tokens)
	}
	want := "ship: waiting for Daytona sandbox la-smoke (state creating)\nship: waiting for Daytona sandbox la-smoke (state pulling_snapshot)\n"
	if progress.String() != want {
		t.Fatalf("%q", progress.String())
	}
}

func TestResolveTargetDaytonaFailsAtOnce(t *testing.T) {
	for state, want := range map[string]string{
		"STOPPED":      "daytona start la-smoke",
		"archived":     "daytona start la-smoke",
		"destroying":   "being deleted; create it again with daytona create",
		"destroyed":    "was deleted; create it again with daytona create",
		"build_failed": "build_failed",
		"error":        "state error",
		"":             "no state field",
	} {
		w, sleeps, _ := fakeWaiter()
		cfg, srv, _, tokens := daytonaFake(t, state)
		_, err := resolveTarget(context.Background(), "daytona:la-smoke", cfg, srv.Client(), w)
		if err == nil || !strings.Contains(err.Error(), want) || *tokens != 0 || *sleeps != 0 {
			t.Errorf("%q: %v tokens %d sleeps %d", state, err, *tokens, *sleeps)
		}
	}
}

func TestResolveTargetDaytonaWaitTimesOut(t *testing.T) {
	w, _, progress := fakeWaiter()
	cfg, srv, polls, tokens := daytonaFake(t, "creating")
	_, err := resolveTarget(context.Background(), "daytona:la-smoke", cfg, srv.Client(), w)
	if err == nil || !strings.Contains(err.Error(), "last state creating") || *tokens != 0 || *polls != 60 {
		t.Fatalf("%v polls %d", err, *polls)
	}
	if strings.Count(progress.String(), "\n") != 1 {
		t.Fatalf("%q", progress.String())
	}
}

func TestResolveTargetDaytonaWaitStopsOnCancel(t *testing.T) {
	w, _, _ := fakeWaiter()
	ctx, cancel := context.WithCancel(context.Background())
	cfg, srv, _, tokens := daytonaFake(t, "creating")
	// Cancel from inside the first sleep so the HTTP calls before it still succeed.
	real := w.sleep
	w.sleep = func(c context.Context, d time.Duration) error { cancel(); return real(c, d) }
	_, err := resolveTarget(ctx, "daytona:la-smoke", cfg, srv.Client(), w)
	if !errors.Is(err, context.Canceled) || *tokens != 0 {
		t.Fatalf("%v", err)
	}
}

func TestResolveTargetDaytonaRefusedLoginWhilePolling(t *testing.T) {
	w, _, _ := fakeWaiter()
	cfg, srv, _, tokens := daytonaFake(t, "creating", "401")
	_, err := resolveTarget(context.Background(), "daytona:la-smoke", cfg, srv.Client(), w)
	if err == nil || !strings.Contains(err.Error(), "daytona list to refresh") || *tokens != 0 {
		t.Fatalf("%v", err)
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
