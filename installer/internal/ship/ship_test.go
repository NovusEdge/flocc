package ship

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/identity"
)

func TestShipErrorsDoNotLeakTheToken(t *testing.T) {
	dest := Dest{SSH: "tok123@ssh.example.test", Token: "tok123"}
	f := host.NewFake().On("ssh tok123@ssh.example.test uname -m", 255, "")
	_, err := ship(context.Background(), f, t.TempDir(), dest, shipOpts{})
	if err == nil || strings.Contains(err.Error(), "tok123") || !strings.Contains(err.Error(), "<token>") {
		t.Fatalf("%v", err)
	}
}

func TestStageFilesArchivesPrivateMarketplaces(t *testing.T) {
	root, dir, cfg := t.TempDir(), t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(root, "build"), 0o755)
	os.WriteFile(filepath.Join(root, "build", "flocc-setup-linux-amd64"), []byte("bin"), 0o755)
	writeJSON(t, filepath.Join(cfg, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{"x@X": true}})
	writeJSON(t, filepath.Join(cfg, "plugins/known_marketplaces.json"), map[string]any{
		"X": map[string]any{"source": map[string]string{"source": "github", "repo": "o/x"}, "installLocation": "/m/X"}})
	f := host.NewFake().
		On("git -C "+root+" config user.name", 0, "Ada L\n").
		On("git -C "+root+" config user.email", 0, "ada@example.test\n").
		On("git -C "+root+" bundle create "+dir+"/flocc.bundle HEAD", 0, "").
		On("git -C /m/X archive --format=tar -o "+dir+"/mp-X.tar HEAD", 0, "").
		On("git -C /m/X rev-parse HEAD", 0, "abc\n")
	err := stageFiles(context.Background(), f, root, dir, "amd64", cfg, func(string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if id, err := identity.Load(filepath.Join(dir, "identity.json")); err != nil || id.Email != "ada@example.test" {
		t.Fatalf("%+v %v", id, err)
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
	p, err := planPlugins(dir, nil, func(repo string) (bool, error) { return repo == "NovusEdge/docket", nil })
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

func TestPlanPluginsKeepsOnlyTheAllowlist(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{
		"docket@NovusEdge": true, "curt@curt": true}})
	writeJSON(t, filepath.Join(dir, "plugins/known_marketplaces.json"), map[string]any{
		"NovusEdge": map[string]any{"source": map[string]string{"source": "github", "repo": "NovusEdge/docket"}},
		"curt":      map[string]any{"source": map[string]string{"source": "github", "repo": "o/curt"}},
	})
	pub := func(string) (bool, error) { return false, nil }
	p, err := planPlugins(dir, []string{"docket@NovusEdge"}, pub)
	if err != nil || !slices.Equal(p.List.Plugins, []string{"docket@NovusEdge"}) || len(p.List.Marketplaces) != 1 {
		t.Fatalf("%+v %v", p.List, err)
	}
	if _, err := planPlugins(dir, []string{"ghost@x"}, pub); err == nil || !strings.Contains(err.Error(), "ghost@x") {
		t.Fatalf("want an error naming ghost@x, got %v", err)
	}
}

func TestReadAllowlistSkipsCommentsAndBlanks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-plugins.txt")
	if err := os.WriteFile(path, []byte("# agents\ndocket@NovusEdge\n\ncurt@curt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readAllowlist(path)
	if err != nil || !slices.Equal(got, []string{"docket@NovusEdge", "curt@curt"}) {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := readAllowlist(filepath.Join(t.TempDir(), "none")); got != nil || err != nil {
		t.Fatalf("missing file: %v %v", got, err)
	}
}

func TestAllowlistWithOnlyCommentsShipsNoPlugins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-plugins.txt")
	if err := os.WriteFile(path, []byte("# nothing yet\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	allow, err := readAllowlist(path)
	if err != nil || allow == nil || len(allow) != 0 {
		t.Fatalf("want a non-nil empty list, got %#v %v", allow, err)
	}
	writeJSON(t, filepath.Join(dir, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{"docket@NovusEdge": true}})
	writeJSON(t, filepath.Join(dir, "plugins/known_marketplaces.json"), map[string]any{
		"NovusEdge": map[string]any{"source": map[string]string{"source": "github", "repo": "NovusEdge/docket"}},
	})
	p, err := planPlugins(dir, allow, func(string) (bool, error) { return false, nil })
	if err != nil || len(p.List.Plugins) != 0 {
		t.Fatalf("want no plugins, got %+v %v", p.List, err)
	}
}

func TestResolveTargetPlainIsPassedThrough(t *testing.T) {
	d, err := resolveTarget(context.Background(), "flocc-smoke", "/none", nil, waiter{})
	if err != nil || d.SSH != "flocc-smoke" || len(d.Opts) != 0 {
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
		case r.Method == "GET" && r.URL.Path == "/api/sandbox/flocc-smoke":
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
	d, err := resolveTarget(context.Background(), "daytona:flocc-smoke", cfg, srv.Client(), w)
	if err != nil || d.SSH != "tok@ssh.example.test" || d.Token != "tok" || *tokens != 1 || *sleeps != 0 || progress.Len() != 0 {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestResolveTargetDaytonaWaitsForStarted(t *testing.T) {
	w, sleeps, progress := fakeWaiter()
	cfg, srv, polls, tokens := daytonaFake(t, "CREATING", "creating", "PULLING_SNAPSHOT", "STARTED")
	if _, err := resolveTarget(context.Background(), "daytona:flocc-smoke", cfg, srv.Client(), w); err != nil {
		t.Fatal(err)
	}
	if *polls != 4 || *sleeps != 3 || *tokens != 1 {
		t.Fatalf("polls %d sleeps %d tokens %d", *polls, *sleeps, *tokens)
	}
	want := "ship: waiting for Daytona sandbox flocc-smoke (state creating)\nship: waiting for Daytona sandbox flocc-smoke (state pulling_snapshot)\n"
	if progress.String() != want {
		t.Fatalf("%q", progress.String())
	}
}

func TestResolveTargetDaytonaFailsAtOnce(t *testing.T) {
	for state, want := range map[string]string{
		"STOPPED":      "daytona start flocc-smoke",
		"archived":     "daytona start flocc-smoke",
		"destroying":   "being deleted; create it again with daytona create",
		"destroyed":    "was deleted; create it again with daytona create",
		"build_failed": "build_failed",
		"error":        "state error",
		"":             "no state field",
	} {
		w, sleeps, _ := fakeWaiter()
		cfg, srv, _, tokens := daytonaFake(t, state)
		_, err := resolveTarget(context.Background(), "daytona:flocc-smoke", cfg, srv.Client(), w)
		if err == nil || !strings.Contains(err.Error(), want) || *tokens != 0 || *sleeps != 0 {
			t.Errorf("%q: %v tokens %d sleeps %d", state, err, *tokens, *sleeps)
		}
	}
}

func TestResolveTargetDaytonaWaitTimesOut(t *testing.T) {
	w, _, progress := fakeWaiter()
	cfg, srv, polls, tokens := daytonaFake(t, "creating")
	_, err := resolveTarget(context.Background(), "daytona:flocc-smoke", cfg, srv.Client(), w)
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
	_, err := resolveTarget(ctx, "daytona:flocc-smoke", cfg, srv.Client(), w)
	if !errors.Is(err, context.Canceled) || *tokens != 0 {
		t.Fatalf("%v", err)
	}
}

func TestResolveTargetDaytonaRefusedLoginWhilePolling(t *testing.T) {
	w, _, _ := fakeWaiter()
	cfg, srv, _, tokens := daytonaFake(t, "creating", "401")
	_, err := resolveTarget(context.Background(), "daytona:flocc-smoke", cfg, srv.Client(), w)
	if err == nil || !strings.Contains(err.Error(), "daytona list to refresh") || *tokens != 0 {
		t.Fatalf("%v", err)
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

func TestStageFilesFailsWithoutAnOwnerIdentity(t *testing.T) {
	f := host.NewFake().On("git -C /r config user.name", 0, "\n").On("git -C /r config user.email", 0, "\n")
	err := stageFiles(context.Background(), f, "/r", t.TempDir(), "amd64", t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "user.name") {
		t.Fatalf("%v", err)
	}
}

// stageOK accepts git and scp, whose arguments hold the random stage directory.
type stageOK struct{ *host.Fake }

func (s *stageOK) Run(ctx context.Context, c host.Cmd) (host.Result, error) {
	switch c.Name {
	case "git":
		if slices.Contains(c.Args, "config") {
			return host.Result{Stdout: "x@x.test\n"}, nil
		}
		return host.Result{}, nil
	case "scp":
		s.Fake.Calls = append(s.Fake.Calls, "scp")
		return host.Result{}, nil
	}
	return s.Fake.Run(ctx, c)
}

func shipRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "build"), 0o755)
	os.WriteFile(filepath.Join(root, "build", "flocc-setup-linux-amd64"), []byte("bin"), 0o755)
	cfg := t.TempDir()
	writeJSON(t, filepath.Join(cfg, "settings.json"), map[string]any{})
	writeJSON(t, filepath.Join(cfg, "plugins/known_marketplaces.json"), map[string]any{})
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	return root
}

func TestRemoteCmd(t *testing.T) {
	for _, c := range []struct {
		interactive bool
		want        string
	}{
		{false, "chmod 755 /tmp/flocc-setup/flocc-setup && sudo -n /tmp/flocc-setup/flocc-setup --answers -"},
		{true, "chmod 755 /tmp/flocc-setup/flocc-setup && sudo /tmp/flocc-setup/flocc-setup"},
	} {
		if got := remoteCmd(c.interactive); got != c.want {
			t.Errorf("interactive=%v: %q", c.interactive, got)
		}
	}
}

// okSudo is a host that passes the probes of a non-interactive ship; the ssh calls that carry
// secrets or sudo pass -T, the uname probe does not.
func okSudo(dest string) *stageOK {
	return &stageOK{host.NewFake().
		On("ssh "+dest+" uname -m", 0, "x86_64\n").
		On("ssh -T "+dest+" sudo -n true", 0, "").
		On("ssh -T "+dest+" "+resetStageNoPrompt, 0, "")}
}

func TestShipSendsTheAnswersOnStdinWithoutATerminal(t *testing.T) {
	root := shipRoot(t)
	doc := []byte("[secrets]\nlinear_key = \"lin_api_s3cret\"\n")
	run := "ssh -T root@h " + remoteCmd(false)
	f := okSudo("root@h")
	if _, err := ship(context.Background(), f, root, Dest{SSH: "root@h"}, shipOpts{Answers: doc, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("interactive: "+run) || f.Stdins[run] != string(doc) {
		t.Fatalf("calls %v stdin %q", f.Calls, f.Stdins[run])
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "ssh -t") || strings.Contains(c, "s3cret") {
			t.Errorf("%s", c)
		}
	}
}

// A RequestTTY setting in the owner's ssh config would otherwise give the run a pty, which
// echoes the answers document back and never passes EOF.
func TestNonInteractiveShipForbidsAPtyOnEverySudoAndSecretCall(t *testing.T) {
	f := okSudo("root@h")
	if _, err := ship(context.Background(), f, shipRoot(t), Dest{SSH: "root@h"}, shipOpts{Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, c := range f.Calls {
		c = strings.TrimPrefix(c, "interactive: ")
		// uname and Part 1's legacy-user probe carry no sudo and no secrets.
		if !strings.HasPrefix(c, "ssh ") || !strings.Contains(c, "sudo ") {
			continue
		}
		checked++
		if !strings.HasPrefix(c, "ssh -T ") {
			t.Errorf("no -T: %s", c)
		}
	}
	if checked != 3 {
		t.Fatalf("want the sudo probe, the stage reset and the run, got %d: %v", checked, f.Calls)
	}
}

func TestSSHNoTTYPutsDashTAfterTheOptions(t *testing.T) {
	c := sshNoTTY(Dest{SSH: "u@h", Opts: []string{"-o", "BatchMode=yes"}}, "true")
	if got := c.String(); got != "ssh -o BatchMode=yes -T u@h true" {
		t.Fatal(got)
	}
}

func TestShipWithoutAnswersStillClosesStdin(t *testing.T) {
	run := "ssh -T h " + remoteCmd(false)
	f := okSudo("h")
	if _, err := ship(context.Background(), f, shipRoot(t), Dest{SSH: "h"}, shipOpts{Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if s, ok := f.Stdins[run]; !ok || s != "" {
		t.Fatalf("the run must get an empty stdin, not the terminal: %q %v", s, ok)
	}
}

func TestShipStopsBeforeStagingWithoutPasswordlessSudo(t *testing.T) {
	f := &stageOK{host.NewFake().
		On("ssh ada@h uname -m", 0, "x86_64\n").
		On("ssh -T ada@h sudo -n true", 1, "").
		On("ssh -T ada@h id -un", 0, "ada\n")}
	_, err := ship(context.Background(), f, shipRoot(t), Dest{SSH: "ada@h"}, shipOpts{Out: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "give ada passwordless sudo, or use root") {
		t.Fatalf("%v", err)
	}
	if f.Ran("scp") || f.Ran("ssh -T ada@h "+resetStageNoPrompt) {
		t.Fatalf("staged anyway: %v", f.Calls)
	}
}

func TestShipNamesAMissingSudo(t *testing.T) {
	f := &stageOK{host.NewFake().On("ssh root@h uname -m", 0, "x86_64\n").On("ssh -T root@h sudo -n true", 127, "")}
	_, err := ship(context.Background(), f, shipRoot(t), Dest{SSH: "root@h"}, shipOpts{Out: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "install sudo") {
		t.Fatalf("%v", err)
	}
}

func TestShipInteractiveKeepsTheTerminal(t *testing.T) {
	f := &stageOK{host.NewFake().On("ssh h uname -m", 0, "x86_64\n")}
	if _, err := ship(context.Background(), f, shipRoot(t), Dest{SSH: "h"}, shipOpts{Interactive: true, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"interactive: ssh -t h " + resetStage, "interactive: ssh -t h " + remoteCmd(true)} {
		if !f.Ran(want) {
			t.Errorf("missing %s in %v", want, f.Calls)
		}
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "sudo -n true") || strings.Contains(c, " -T ") {
			t.Errorf("an interactive ship lets sudo ask on the terminal: %s", c)
		}
	}
}

func TestShipReturnsTheHostsSummaryAndDropsItsLine(t *testing.T) {
	want := Summary{Version: "flocc 0.4.0", Login: "none", LoginPending: true}
	run := "ssh -T root@h " + remoteCmd(false)
	f := okSudo("root@h")
	f.OnInteractiveOut(run, "setup chatter\r\n\r\n"+want.Line()+"\r\n")
	var out strings.Builder
	got, err := ship(context.Background(), f, shipRoot(t), Dest{SSH: "root@h"}, shipOpts{Out: &out})
	if err != nil || got.Version != want.Version || !got.LoginPending {
		t.Fatalf("%+v %v", got, err)
	}
	if out.String() != "setup chatter\r\n\r\n" {
		t.Fatalf("output %q", out.String())
	}
}

func TestShipKeepsTheSummaryWhenTheHostExitsNonZero(t *testing.T) {
	sum := Summary{Version: "flocc 0.4.0", Login: "full", Failed: []string{"FAIL  linear: 401"}}
	run := "ssh -T root@h " + remoteCmd(false)
	f := okSudo("root@h")
	f.OnInteractiveOut(run, sum.Line()+"\n").OnInteractive(run, errors.New("exit status 1"))
	got, err := ship(context.Background(), f, shipRoot(t), Dest{SSH: "root@h"}, shipOpts{Out: io.Discard})
	if err == nil || !got.Failing() {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestShipRefusesAnUnsafeTargetAndInteractiveAnswers(t *testing.T) {
	var u usageError
	if _, err := Ship(context.Background(), Options{Target: "-oProxyCommand=x"}, nil); !errors.As(err, &u) {
		t.Fatalf("%v", err)
	}
	if _, err := Ship(context.Background(), Options{Target: "h", Interactive: true}, strings.NewReader("")); !errors.As(err, &u) {
		t.Fatalf("%v", err)
	}
}

func TestShipRejectsABrokenAnswersDocumentBeforeConnecting(t *testing.T) {
	_, err := Ship(context.Background(), Options{Target: "h"}, strings.NewReader("[secrets]\nlinear_key = \"lin_api_s3cret\n"))
	if err == nil || strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("%v", err)
	}
}

func TestOpenAnswersReadsStdinForDashAndAFileOtherwise(t *testing.T) {
	rc, err := openAnswers("-", strings.NewReader("from stdin"))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(rc); string(b) != "from stdin" {
		t.Fatalf("%q", b)
	}
	path := filepath.Join(t.TempDir(), "a.toml")
	os.WriteFile(path, []byte("from file"), 0o600)
	rc, err = openAnswers(path, strings.NewReader("unused"))
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if b, _ := io.ReadAll(rc); string(b) != "from file" {
		t.Fatalf("%q", b)
	}
}

func TestValidTarget(t *testing.T) {
	for _, ok := range []string{"root@flocc-nimble", "daytona:flocc-smoke", "box.tailnet.ts.net"} {
		if !ValidTarget(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "-oProxyCommand=x", "h;rm -rf ~", "a b", "$(id)", "h`id`"} {
		if ValidTarget(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestMainRefusesInteractiveWithAnswers(t *testing.T) {
	if code := Main([]string{"--interactive", "--answers", "a.toml", "h"}); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestShipRefusesAHostThatRunsTheOldService(t *testing.T) {
	f := host.NewFake().
		On("ssh h uname -m", 0, "x86_64\n").
		On("ssh h id -u "+legacyUser, 0, "998\n")
	_, err := ship(context.Background(), f, t.TempDir(), Dest{SSH: "h"}, shipOpts{})
	if err == nil || !strings.Contains(err.Error(), "a second poller") {
		t.Fatalf("%v", err)
	}
	if len(f.Calls) != 2 {
		t.Fatalf("ran past the refusal: %v", f.Calls)
	}
	dest := Dest{SSH: "tok9@ssh.example.test", Token: "tok9"}
	f = host.NewFake().
		On("ssh tok9@ssh.example.test uname -m", 0, "x86_64\n").
		On("ssh tok9@ssh.example.test id -u "+legacyUser, 0, "998\n")
	_, err = ship(context.Background(), f, t.TempDir(), dest, shipOpts{})
	if err == nil || strings.Contains(err.Error(), "tok9") {
		t.Fatalf("%v", err)
	}
}
