package ship

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
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

func TestDiscoverSkipsDaytonaWithoutItsConfigAndKeepsTheHostsDir(t *testing.T) {
	dir := t.TempDir()
	hostsDir := filepath.Join(dir, "hosts")
	for _, h := range []hosts.Host{{Name: "box1", SSH: "root@box1"}, {Name: "sb", Daytona: "sb"}} {
		if err := hosts.Save(hostsDir, h); err != nil {
			t.Fatal(err)
		}
	}
	got, notes := discover(context.Background(), filepath.Join(dir, "no-config.json"), hostsDir, http.DefaultClient)
	if len(got) != 1 || got[0].Name != "box1" || got[0].Target != "root@box1" || len(notes) != 1 || !strings.Contains(notes[0], "skipped") {
		t.Fatalf("%+v %q", got, notes)
	}
}

func TestDiscoverListsSandboxesByName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sandbox" || r.Header.Get("X-Daytona-Organization-ID") != "org1" {
			w.WriteHeader(404)
			return
		}
		switch r.URL.Query().Get("cursor") {
		case "":
			json.NewEncoder(w).Encode(map[string]any{"nextCursor": "c/2", "items": []map[string]any{
				{"id": "2", "name": "b", "state": "STOPPED", "cpu": 2, "memory": 4, "disk": 30},
			}})
		case "c/2":
			json.NewEncoder(w).Encode(map[string]any{"nextCursor": nil, "items": []map[string]any{
				{"id": "1", "name": "a", "state": "started", "cpu": 1, "memory": 1},
			}})
		default:
			w.WriteHeader(400)
		}
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

func TestDiscoverNotesASandboxListThatDoesNotAdvance(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// Ignores the cursor and mints a new one each time, as a wrong parameter name would.
		json.NewEncoder(w).Encode(map[string]any{"nextCursor": "c" + strconv.Itoa(calls), "items": []map[string]any{
			{"id": "1", "name": "a", "state": "started", "cpu": 1, "memory": 1},
		}})
	}))
	t.Cleanup(srv.Close)
	cfg := filepath.Join(t.TempDir(), "config.json")
	writeJSON(t, cfg, map[string]any{"activeProfile": "p", "profiles": []any{map[string]any{
		"id": "p", "activeOrganizationId": "org1",
		"api": map[string]any{"url": srv.URL + "/api", "key": "acc"},
	}}})
	got, notes := discover(context.Background(), cfg, filepath.Join(t.TempDir(), "none"), srv.Client())
	if len(got) != 1 || calls != 2 || len(notes) != 1 || !strings.Contains(notes[0], "did not advance") {
		t.Fatalf("calls=%d %+v %q", calls, got, notes)
	}
}

const statusJSON = `{"host":"flocc-smoke","mode":"loop","last_tick":{"at":1,"exit":"0","age_s":120},
"session_cap":2,"five_hour_max_pct":60,"weekly_max_pct":50,
"claims":[{"issue":"NF-1","state":"running","phase":"impl","started_age_s":3700,"updated_age_s":5,"tmux_alive":true,"context_pct":41.6},
{"issue":"NF-2","state":"blocked","phase":"test","started_age_s":90,"updated_age_s":null,"tmux_alive":false,"context_pct":null},
{"issue":"NF-3","state":"running","phase":"impl","started_age_s":10,"updated_age_s":null,"tmux_alive":false,"context_pct":null}],
"usage":{"five_hour_pct":20,"weekly_pct":55,"age_s":30},"disk":{"free_bytes":10737418240,"total_bytes":107374182400}}`

func TestFleetFetchesInParallelAndNeverLeaksTheToken(t *testing.T) {
	remote := asServiceUser([]string{"flocc", "status", "--json"})
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
	rows := collect(context.Background(), f, cache, hosts, nil)
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
	out := plainText(render(rows, nil, []string{"daytona: no CLI config, skipped"}))
	for _, want := range []string{
		"LOGIN", "TOKENS", "2m ago, ok", "2/2", "20%", "55%", "10.0 GiB", "~$0.166",
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
	out := plainText(render([]Row{{FleetHost: FleetHost{Name: "x"}, Err: "boom"}}, nil, nil))
	if strings.Contains(out, "ISSUE") || strings.Contains(out, "CARDINAL") {
		t.Fatal(out)
	}
}

func cardinalFake(t *testing.T, stdout string, code int) (*host.Fake, saved.Saved) {
	t.Helper()
	s := saved.Saved{Machine: "owner@zer0-day-nimble", User: "cardinal"}
	args := append(append([]string{}, saved.Pinned...), "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"cardinal@zer0-day-nimble", "~/.local/lib/flocc-cardinal/flocc-setup --phase cardinal-status --quick")
	f := host.NewFake().On(host.Cmd{Name: "ssh", Args: args}.String(), code, stdout)
	return f, s
}

func TestFleetShowsCardinalAndListsAFailingOne(t *testing.T) {
	ok, _ := json.Marshal(steps.CardinalStatus{Schema: 1, Unit: "active", Login: "full", SessionAgeS: new(7200.0), LastTurnAgeS: new(300.0)})
	failing, _ := json.Marshal(steps.CardinalStatus{Schema: 1, Unit: "failed", Login: "full"})
	for name, c := range map[string]struct {
		stdout   string
		code     int
		wantFail string
	}{
		"healthy":        {string(ok), 0, ""},
		"failing":        {string(failing), 0, "cardinal.service is failed"},
		"unreachable":    {"", 255, "cardinal cardinal: exit 255"},
		"unknown schema": {`{"schema":2}`, 0, "schema 2"},
	} {
		f, s := cardinalFake(t, c.stdout, c.code)
		rows := collectCardinals(context.Background(), f, nil, []saved.Saved{s})
		out := plainText(render(nil, rows, nil))
		if !strings.Contains(out, "CARDINAL") || !strings.Contains(out, "zer0-day-nimble") {
			t.Errorf("%s: no table:\n%s", name, out)
		}
		hasFail := strings.Contains(out, "flocc cardinal status --user cardinal")
		if (c.wantFail != "") != hasFail || c.wantFail != "" && !strings.Contains(out, c.wantFail) {
			t.Errorf("%s:\n%s", name, out)
		}
	}
}

func TestFleetSanitizesCardinalsOutput(t *testing.T) {
	hostile, _ := json.Marshal(steps.CardinalStatus{Schema: 1, Unit: "\x1b]52;c;aGk=\x07active", Login: "\x1b[2Jfull"})
	f, s := cardinalFake(t, string(hostile), 0)
	out := render(nil, collectCardinals(context.Background(), f, nil, []saved.Saved{s}), nil)
	if strings.Contains(out, "\x1b]52") || strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\x07") {
		t.Fatalf("raw escape in %q", out)
	}
}

func TestDiscoverCardinalsSkipsRemovedInstalls(t *testing.T) {
	dir := t.TempDir()
	saved.Saved{Machine: "m", User: "cardinal"}.Save(dir)
	saved.Saved{Machine: "m", User: "cardinal-test", Removed: true}.Save(dir)
	list, notes := discoverCardinals(dir)
	if len(list) != 1 || list[0].User != "cardinal" || len(notes) != 0 {
		t.Fatalf("%+v %v", list, notes)
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
	rows := collect(context.Background(), hangingRunner{}, cache, []FleetHost{{Name: "slow", Target: "slow"}, {Name: "fast", Target: "fast"}}, nil)
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

func TestLoginAndTokenCellsFlagWhatWillStopSessions(t *testing.T) {
	const red, yellow = "\x1b[31m", "\x1b[33m"
	for _, tc := range []struct {
		json, login, tokens, color string
	}{
		{`{"now":0,"claude_login":"full","token_expiry":{"agent":"2592000","publish":"never"}}`, "full", "agent 30d", ""},
		{`{"now":0,"claude_login":"none","token_expiry":{"agent":"864000","publish":"5000000"}}`, "none", "agent 10d", yellow},
		{`{"now":0,"claude_login":"full","token_expiry":{"agent":"never","publish":"86400"}}`, "full", "publish 1d", red},
		{`{"now":0,"claude_login":"full","token_expiry":{"agent":"rejected"}}`, "full", "agent rejected", red},
		{`{"now":0,"token_expiry":{"agent":"never","publish":"never"}}`, "-", "never", ""},
		{`{"now":0}`, "-", "-", ""},
	} {
		var rep Report
		if err := json.Unmarshal([]byte(tc.json), &rep); err != nil {
			t.Fatal(err)
		}
		if got := plainText(loginCell(&rep)); got != tc.login {
			t.Errorf("%s: login %q", tc.json, got)
		}
		cell := tokensCell(&rep)
		if got := plainText(cell); got != tc.tokens || tc.color != "" && !strings.Contains(cell, tc.color) {
			t.Errorf("%s: tokens %q %q", tc.json, got, cell)
		}
	}
	none := Report{ClaudeLogin: new(string)}
	*none.ClaudeLogin = "token-only (oauth_token)"
	if !strings.Contains(loginCell(&none), red) {
		t.Error("a login without Remote Control is not styled as bad")
	}
	// A login the host could not read is neither a lapse nor fine.
	*none.ClaudeLogin = "unknown"
	if got := loginCell(&none); plainText(got) != "unknown" || !strings.Contains(got, yellow) {
		t.Errorf("%q", got)
	}
}

func TestFleetRejectsATightWatchInterval(t *testing.T) {
	for _, iv := range []string{"0s", "-1s", "1s"} {
		if code := Fleet(context.Background(), quietUI(), io.Discard, []string{"--watch", "--interval", iv}); code != 2 {
			t.Errorf("%s: %d", iv, code)
		}
	}
}

// destCache hands an ssh host its destination without minting one, so the sharing must come
// from the cache itself.
func TestStatusesSharesConnectionsForSSHHosts(t *testing.T) {
	shortHome(t)
	f := host.NewFake()
	Statuses(context.Background(), quietUI(), f, []hosts.Host{{Name: "a", SSH: "root@a"}}, "NF-1")
	if len(f.Calls) != 1 || !strings.Contains(f.Calls[0], "ControlMaster=auto") || !strings.Contains(f.Calls[0], "BatchMode=yes") {
		t.Fatalf("%v", f.Calls)
	}
}

func TestAge(t *testing.T) {
	for in, want := range map[float64]string{5: "5s", 119: "1m", 3700: "1h", 200000: "2d"} {
		if got := Age(in); got != want {
			t.Errorf("%v: %s want %s", in, got, want)
		}
	}
}

func tableEvents(rec *ui.Recorder) []*ui.Table {
	var out []*ui.Table
	for _, e := range rec.Events {
		if e.Kind == ui.KTable {
			out = append(out, e.Table)
		}
	}
	return out
}

func TestDrawPutsPlainTablesAndNotesOnTheSession(t *testing.T) {
	var rep Report
	if err := json.Unmarshal([]byte(statusJSON), &rep); err != nil {
		t.Fatal(err)
	}
	rows := []Row{
		{FleetHost: FleetHost{Name: "sb", State: "started", CPU: 2, MemGiB: 4, Daytona: true}, Report: &rep},
		{FleetHost: FleetHost{Name: "down"}, Err: "exit 255: Connection refused"},
	}
	rec := &ui.Recorder{}
	draw(ui.New(rec), rows, nil, []string{"daytona: no CLI config, skipped"})
	tables := tableEvents(rec)
	if len(tables) != 2 || tables[0].Headers[0] != "HOST" || tables[1].Headers[1] != "ISSUE" {
		t.Fatalf("%+v", tables)
	}
	for _, tb := range tables {
		for _, r := range tb.Rows {
			for _, c := range r {
				if strings.Contains(c, "\x1b") {
					t.Fatalf("styling in a cell: %q", c)
				}
			}
		}
	}
	if got := tables[0].Rows[0]; got[0] != "sb" || got[4] != "2/2" || got[10] != "~$0.166" {
		t.Fatalf("%q", got)
	}
	var notes = map[string]string{}
	for _, e := range rec.Events {
		if e.Kind == ui.KNote {
			notes[e.Name] = e.Text
		}
	}
	if !strings.Contains(notes["Problems"], "down: exit 255: Connection refused") ||
		!strings.Contains(notes["Notes"], "daytona: no CLI config, skipped") || !strings.Contains(notes["Notes"], "an estimate") {
		t.Fatalf("%q", notes)
	}
}

func TestAHostWithoutAVersionedReportIsMarkedBehind(t *testing.T) {
	var old, current Report
	json.Unmarshal([]byte(`{"mode":"live","session_cap":2}`), &old)
	json.Unmarshal([]byte(`{"v":1,"mode":"live","session_cap":2,"claims":[{"issue":"NF-1","state":"running","phase":"impl","launches":1,"launch_limit":2,"activity":[{"at":1700000000,"text":"reading"}]}]}`), &current)
	if c := current.Claims[0]; len(c.Activity) != 1 || c.Activity[0].Text != "reading" || c.Launches != 1 || c.LaunchLimit != 2 {
		t.Fatalf("%+v", current.Claims)
	}
	tables, _ := fleetTables([]Row{
		{FleetHost: FleetHost{Name: "old"}, Report: &old},
		{FleetHost: FleetHost{Name: "new"}, Report: &current},
	}, nil)
	if got := unstyled(tables[0].Rows[0][1]); got != "behind" {
		t.Errorf("old host state %q", got)
	}
	if got := unstyled(tables[0].Rows[1][1]); got != "up" {
		t.Errorf("current host state %q", got)
	}
}

func TestCollectOpensAStepPerHostAndEndsEachWithItsRow(t *testing.T) {
	f := &lockedFake{Fake: host.NewFake()}
	cache := newDestCache(nil)
	hs := []FleetHost{{Name: "a", Target: "root@a"}, {Name: "b", Target: "root@b"}, {Name: "off", State: "stopped"}}
	remote := asServiceUser([]string{"flocc", "status", "--json"})
	f.On("ssh -o BatchMode=yes -o ConnectTimeout=10 root@a "+remote, 0, `{"v":1,"mode":"live"}`)
	f.Responses["ssh -o BatchMode=yes -o ConnectTimeout=10 root@b "+remote] = host.Result{Stderr: "refused", Code: 255}
	rec := &ui.Recorder{}
	collect(context.Background(), f, cache, hs, hostSteps(ui.New(rec)))
	got := map[string]ui.Event{}
	for _, e := range rec.Rows() {
		got[e.Name] = e
	}
	if got["a"].State != ui.Done || got["a"].Detail != "up" ||
		got["b"].State != ui.Skipped || !strings.HasPrefix(got["b"].Detail, "unreachable: ") ||
		got["off"].State != ui.Skipped || got["off"].Detail != "stopped" {
		t.Fatalf("%+v", got)
	}
}
