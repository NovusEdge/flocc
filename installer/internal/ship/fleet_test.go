package ship

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

// lockedFake serializes the Fake, which collect drives from one goroutine per host.
type lockedFake struct {
	mu sync.Mutex
	*host.Fake
}

func (l *lockedFake) Run(ctx context.Context, c host.Cmd) (host.Result, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Fake.Run(ctx, c)
}

func plainText(s string) string {
	var b bytes.Buffer
	lipgloss.Fprint(&b, s)
	return b.String()
}

func TestEstimateHourly(t *testing.T) {
	// 2 vCPU, 4 GiB, 25 GiB disk of which 5 are free: 0.1008 + 0.0648 + 20 * 0.000108.
	if got := estimateHourly(2, 4, 25); got < 0.1677 || got > 0.1679 {
		t.Fatal(got)
	}
	if got := estimateHourly(1, 1, 3); got < 0.0665 || got > 0.0667 {
		t.Fatal(got)
	}
}

func TestCostIsOnlyShownForStartedSandboxes(t *testing.T) {
	started := FleetHost{Daytona: true, State: "started", CPU: 1, MemGiB: 1, DiskGiB: 5}
	if got := plainText(costCell(started)); got != "~$0.067" {
		t.Fatal(got)
	}
	for _, state := range []string{"stopped", "archived"} {
		started.State = state
		if got := plainText(costCell(started)); got != "-" {
			t.Fatalf("%s: %q", state, got)
		}
	}
}

func TestReadHostsFileSkipsBlanksAndComments(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(p, []byte("# tailnet\n\nbox1.ts.net\n  box2  \n#off.ts.net\n-oProxyCommand=id\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, rejected, err := readHostsFile(p)
	if err != nil || strings.Join(got, ",") != "box1.ts.net,box2" || strings.Join(rejected, ",") != "-oProxyCommand=id" {
		t.Fatalf("%q %q %v", got, rejected, err)
	}
	if got, _, err := readHostsFile(filepath.Join(t.TempDir(), "none")); got != nil || err != nil {
		t.Fatalf("%q %v", got, err)
	}
}

func TestDiscoverSkipsDaytonaWithoutItsConfigAndKeepsTheHostsFile(t *testing.T) {
	dir := t.TempDir()
	hosts := filepath.Join(dir, "hosts")
	if err := os.WriteFile(hosts, []byte("box1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, notes := discover(context.Background(), filepath.Join(dir, "no-config.json"), hosts, http.DefaultClient)
	if len(got) != 1 || got[0].Target != "box1" || len(notes) != 1 || !strings.Contains(notes[0], "skipped") {
		t.Fatalf("%+v %q", got, notes)
	}
}

func TestDiscoverListsSandboxesByName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sandbox" || r.Header.Get("X-Daytona-Organization-ID") != "org1" {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": "2", "name": "b", "state": "STOPPED", "cpu": 2, "memory": 4, "disk": 30},
			{"id": "1", "name": "a", "state": "started", "cpu": 1, "memory": 1},
		})
	}))
	t.Cleanup(srv.Close)
	cfg := filepath.Join(t.TempDir(), "config.json")
	writeJSON(t, cfg, map[string]any{"activeProfile": "p", "profiles": []any{map[string]any{
		"id": "p", "activeOrganizationId": "org1",
		"api": map[string]any{"url": srv.URL + "/api", "key": "acc"},
	}}})
	got, notes := discover(context.Background(), cfg, filepath.Join(t.TempDir(), "none"), srv.Client())
	if len(notes) != 0 || len(got) != 2 || got[0].Target != "daytona:a" || got[1].State != "stopped" || got[1].CPU != 2 || got[1].ID != "2" || got[1].DiskGiB != 30 {
		t.Fatalf("%+v %q", got, notes)
	}
}

const statusJSON = `{"host":"la-smoke","mode":"loop","last_tick":{"at":1,"exit":"0","age_s":120},
"session_cap":2,"five_hour_max_pct":60,"weekly_max_pct":50,
"claims":[{"issue":"NF-1","state":"running","phase":"impl","started_age_s":3700,"updated_age_s":5,"tmux_alive":true,"context_pct":41.6},
{"issue":"NF-2","state":"blocked","phase":"test","started_age_s":90,"updated_age_s":null,"tmux_alive":false,"context_pct":null},
{"issue":"NF-3","state":"running","phase":"impl","started_age_s":10,"updated_age_s":null,"tmux_alive":false,"context_pct":null}],
"usage":{"five_hour_pct":20,"weekly_pct":55,"age_s":30},"disk":{"free_bytes":10737418240,"total_bytes":107374182400}}`

func TestFleetFetchesInParallelAndNeverLeaksTheToken(t *testing.T) {
	remote := asServiceUser([]string{"linear-agents", "status", "--json"})
	opts := "-o StrictHostKeyChecking=accept-new -o BatchMode=yes -o ConnectTimeout=10"
	f := &lockedFake{Fake: host.NewFake().On("ssh "+opts+" tok@gw "+remote, 0, statusJSON)}
	f.Responses["ssh -o BatchMode=yes -o ConnectTimeout=10 down "+remote] = host.Result{Stderr: "ssh: connect to host down: Connection refused", Code: 255}
	cache := newDestCache(func(context.Context, FleetHost) (Dest, error) {
		return Dest{SSH: "tok@gw", Token: "tok", Opts: []string{"-o", "StrictHostKeyChecking=accept-new"}}, nil
	})
	hosts := []FleetHost{
		{Name: "sb", Target: "daytona:sb", State: "started", CPU: 2, MemGiB: 4, Daytona: true},
		{Name: "down", Target: "down"},
		{Name: "old", Target: "daytona:old", State: "stopped", CPU: 1, MemGiB: 2, Daytona: true},
	}
	rows := collect(context.Background(), f, cache, hosts)
	if rows[0].Report == nil || rows[0].Report.Claims[0].Issue != "NF-1" {
		t.Fatalf("%+v", rows[0])
	}
	if rows[1].Report != nil || !strings.Contains(rows[1].Err, "exit 255") {
		t.Fatalf("%+v", rows[1])
	}
	if rows[2].Report != nil || rows[2].Err != "" || len(f.Calls) != 2 {
		t.Fatalf("a stopped sandbox must not be contacted: %+v %v", rows[2], f.Calls)
	}
}

func TestFetchScrubsTheTokenFromErrors(t *testing.T) {
	cache := newDestCache(func(context.Context, FleetHost) (Dest, error) { return Dest{SSH: "tok@gw", Token: "tok"}, nil })
	row := fetch(context.Background(), errRunner{errors.New("ssh tok@gw: boom")}, cache, FleetHost{Name: "sb", Target: "daytona:sb", State: "started", Daytona: true})
	if strings.Contains(row.Err, "tok@") || !strings.Contains(row.Err, "<token>@gw") {
		t.Fatal(row.Err)
	}
	if len(cache.m) != 0 {
		t.Fatal("a failed host keeps its cached token")
	}
}

func TestDestCacheReusesATokenUntilItIsNearExpiry(t *testing.T) {
	now := time.Unix(0, 0)
	mints := 0
	c := newDestCache(func(context.Context, FleetHost) (Dest, error) { mints++; return Dest{SSH: "t@gw"}, nil })
	c.now = func() time.Time { return now }
	h := FleetHost{Target: "daytona:sb", Daytona: true}
	for _, at := range []time.Duration{0, 10 * time.Minute, 49 * time.Minute, 51 * time.Minute} {
		now = time.Unix(0, 0).Add(at)
		if _, err := c.resolve(context.Background(), h); err != nil {
			t.Fatal(err)
		}
	}
	if mints != 2 {
		t.Fatal(mints)
	}
	if d, _ := c.resolve(context.Background(), FleetHost{Target: "box"}); d.SSH != "box" || mints != 2 {
		t.Fatalf("an ssh host needs no token: %+v", d)
	}
}

type codeRunner struct{ code int }

func (c codeRunner) Run(context.Context, host.Cmd) (host.Result, error) {
	return host.Result{Stderr: "failed", Code: c.code}, nil
}
func (c codeRunner) Interactive(context.Context, host.Cmd) error { return nil }

func TestOnlyAnSSHFailureDropsTheCachedToken(t *testing.T) {
	h := FleetHost{Name: "sb", Target: "daytona:sb", State: "started", Daytona: true}
	for code, cached := range map[int]bool{1: true, 127: true, 255: false} {
		cache := newDestCache(func(context.Context, FleetHost) (Dest, error) { return Dest{SSH: "tok@gw", Token: "tok"}, nil })
		if row := fetch(context.Background(), codeRunner{code}, cache, h); row.Err == "" {
			t.Fatal("expected an error row")
		}
		if got := len(cache.m) == 1; got != cached {
			t.Errorf("exit %d: cached %v", code, got)
		}
	}
}

type errRunner struct{ err error }

func (e errRunner) Run(context.Context, host.Cmd) (host.Result, error) { return host.Result{}, e.err }
func (e errRunner) Interactive(context.Context, host.Cmd) error        { return e.err }

func TestRenderShowsHostsClaimsFailuresAndNotes(t *testing.T) {
	var rep Report
	if err := json.Unmarshal([]byte(statusJSON), &rep); err != nil {
		t.Fatal(err)
	}
	rows := []Row{
		{FleetHost: FleetHost{Name: "sb", State: "started", CPU: 2, MemGiB: 4, Daytona: true}, Report: &rep},
		{FleetHost: FleetHost{Name: "down"}, Err: "exit 255: Connection refused"},
		{FleetHost: FleetHost{Name: "old", State: "stopped", CPU: 1, MemGiB: 2, Daytona: true}},
	}
	out := plainText(render(rows, []string{"daytona: no CLI config, skipped"}))
	for _, want := range []string{
		"2m ago, ok", "2/2", "20%", "55%", "10.0 GiB", "~$0.166",
		"NF-1", "impl", "1h", "42%",
		"unreachable", "down: exit 255: Connection refused",
		"stopped",
		"daytona: no CLI config, skipped", "an estimate", "show - (their rate",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// SESSIONS counts running claims, and only a running claim can have a dead session.
	tmux := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(strings.ReplaceAll(line, "│", " ")); len(f) > 0 && strings.HasPrefix(f[0], "sb") && len(f) > 2 && strings.HasPrefix(f[1], "NF-") {
			tmux[f[1]] = f[len(f)-1]
		}
	}
	if tmux["NF-1"] != "yes" || tmux["NF-2"] != "-" || tmux["NF-3"] != "no" {
		t.Errorf("%v in:\n%s", tmux, out)
	}
}

func TestTickCellFlagsAStuckTick(t *testing.T) {
	var rep Report
	json.Unmarshal([]byte(`{"last_tick":{"exit":null,"age_s":1200}}`), &rep)
	if got := plainText(tickCell(&rep)); got != "20m ago, still running" {
		t.Fatal(got)
	}
	if !strings.Contains(tickCell(&rep), "\x1b[33m") {
		t.Fatal("a stuck tick is not styled as a warning")
	}
}

func TestRenderOmitsTheClaimsTableWhenNothingRuns(t *testing.T) {
	out := plainText(render([]Row{{FleetHost: FleetHost{Name: "x"}, Err: "boom"}}, nil))
	if strings.Contains(out, "ISSUE") {
		t.Fatal(out)
	}
}

// hangingRunner blocks the "slow" host until its context ends and answers the others at once.
type hangingRunner struct{}

func (hangingRunner) Run(ctx context.Context, c host.Cmd) (host.Result, error) {
	if strings.Contains(c.String(), " slow ") {
		<-ctx.Done()
		return host.Result{}, ctx.Err()
	}
	return host.Result{Stdout: statusJSON}, nil
}
func (hangingRunner) Interactive(context.Context, host.Cmd) error { return nil }

func TestOneSlowHostDoesNotDelayTheOthersPastTheTimeout(t *testing.T) {
	old := hostTimeout
	hostTimeout = 300 * time.Millisecond
	t.Cleanup(func() { hostTimeout = old })
	cache := newDestCache(nil)
	start := time.Now()
	rows := collect(context.Background(), hangingRunner{}, cache, []FleetHost{{Name: "slow", Target: "slow"}, {Name: "fast", Target: "fast"}})
	if took := time.Since(start); took > 2*time.Second {
		t.Fatal(took)
	}
	if rows[0].Report != nil || rows[0].Err == "" || rows[1].Report == nil {
		t.Fatalf("%+v %+v", rows[0], rows[1])
	}
}

func TestTickAndDiskCellsFlagProblems(t *testing.T) {
	const red, yellow = "\x1b[31m", "\x1b[33m"
	for _, tc := range []struct {
		json, text, color string
	}{
		{`{"last_tick":{"exit":null,"age_s":30}}`, "30s ago, running", ""},
		{`{"last_tick":{"exit":"0","age_s":700}}`, "11m ago, ok", yellow},
		{`{"last_tick":{"exit":"0","age_s":60}}`, "1m ago, ok", ""},
		{`{"last_tick":{"exit":"2","age_s":60}}`, "1m ago, exit 2", red},
		{`{}`, "-", ""},
	} {
		var rep Report
		if err := json.Unmarshal([]byte(tc.json), &rep); err != nil {
			t.Fatal(err)
		}
		got := tickCell(&rep)
		if plainText(got) != tc.text || tc.color != "" && !strings.Contains(got, tc.color) {
			t.Errorf("%s: %q", tc.json, got)
		}
	}
	var low, fine Report
	json.Unmarshal([]byte(`{"disk":{"free_bytes":14,"total_bytes":100}}`), &low)
	json.Unmarshal([]byte(`{"disk":{"free_bytes":15,"total_bytes":100}}`), &fine)
	if !strings.Contains(diskCell(&low), red) || strings.Contains(diskCell(&fine), red) {
		t.Errorf("%q %q", diskCell(&low), diskCell(&fine))
	}
}

func TestFleetRejectsATightWatchInterval(t *testing.T) {
	for _, iv := range []string{"0s", "-1s", "1s"} {
		if code := FleetMain([]string{"--watch", "--interval", iv}, io.Discard); code != 2 {
			t.Errorf("%s: %d", iv, code)
		}
	}
}

func TestAge(t *testing.T) {
	for in, want := range map[float64]string{5: "5s", 119: "1m", 3700: "1h", 200000: "2d"} {
		if got := age(in); got != want {
			t.Errorf("%v: %s want %s", in, got, want)
		}
	}
}
