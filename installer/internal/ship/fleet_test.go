package ship

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
	if got := estimateHourly(2, 4); got < 0.1655 || got > 0.1657 {
		t.Fatal(got)
	}
}

func TestReadHostsFileSkipsBlanksAndComments(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(p, []byte("# tailnet\n\nbox1.ts.net\n  box2  \n#off.ts.net\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readHostsFile(p)
	if err != nil || strings.Join(got, ",") != "box1.ts.net,box2" {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := readHostsFile(filepath.Join(t.TempDir(), "none")); got != nil || err != nil {
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
			{"id": "2", "name": "b", "state": "STOPPED", "cpu": 2, "memory": 4},
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
	if len(notes) != 0 || len(got) != 2 || got[0].Target != "daytona:a" || got[1].State != "stopped" || got[1].CPU != 2 {
		t.Fatalf("%+v %q", got, notes)
	}
}

const statusJSON = `{"host":"la-smoke","mode":"loop","last_tick":{"at":1,"exit":"0","age_s":120},
"session_cap":2,"five_hour_max_pct":60,"weekly_max_pct":50,
"claims":[{"issue":"NF-1","state":"running","phase":"impl","started_age_s":3700,"updated_age_s":5,"tmux_alive":true,"context_pct":41.6},
{"issue":"NF-2","state":"blocked","phase":"test","started_age_s":90,"updated_age_s":null,"tmux_alive":false,"context_pct":null}],
"usage":{"five_hour_pct":20,"weekly_pct":55,"age_s":30},"disk":{"free_bytes":10737418240,"total_bytes":107374182400}}`

func TestFleetFetchesInParallelAndNeverLeaksTheToken(t *testing.T) {
	remote := asServiceUser([]string{"linear-agents", "status", "--json"})
	opts := "-o StrictHostKeyChecking=accept-new -o BatchMode=yes -o ConnectTimeout=10"
	f := &lockedFake{Fake: host.NewFake().On("ssh "+opts+" tok@gw "+remote, 0, statusJSON)}
	f.Responses["ssh -o BatchMode=yes -o ConnectTimeout=10 down "+remote] = host.Result{Stderr: "ssh: connect to host down: Connection refused", Code: 255}
	resolve := func(_ context.Context, target string) (Dest, error) {
		if target == "daytona:sb" {
			return Dest{SSH: "tok@gw", Token: "tok", Opts: []string{"-o", "StrictHostKeyChecking=accept-new"}}, nil
		}
		return Dest{SSH: target}, nil
	}
	hosts := []FleetHost{
		{Name: "sb", Target: "daytona:sb", State: "started", CPU: 2, MemGiB: 4, Daytona: true},
		{Name: "down", Target: "down"},
		{Name: "old", Target: "daytona:old", State: "stopped", CPU: 1, MemGiB: 2, Daytona: true},
	}
	rows := collect(context.Background(), f, resolve, hosts)
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
	resolve := func(context.Context, string) (Dest, error) { return Dest{SSH: "tok@gw", Token: "tok"}, nil }
	row := fetch(context.Background(), errRunner{errors.New("ssh tok@gw: boom")}, resolve, FleetHost{Name: "sb", Target: "daytona:sb", State: "started"})
	if strings.Contains(row.Err, "tok@") || !strings.Contains(row.Err, "<token>@gw") {
		t.Fatal(row.Err)
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
		"2m ago, ok", "1/2", "20%", "55%", "10.0 GiB", "~$0.17",
		"NF-1", "impl", "1h", "42%", "NF-2", "no",
		"unreachable", "down: exit 255: Connection refused",
		"stopped", "~$0.08",
		"daytona: no CLI config, skipped", "an estimate",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderOmitsTheClaimsTableWhenNothingRuns(t *testing.T) {
	out := plainText(render([]Row{{FleetHost: FleetHost{Name: "x"}, Err: "boom"}}, nil))
	if strings.Contains(out, "ISSUE") {
		t.Fatal(out)
	}
}

func TestAge(t *testing.T) {
	for in, want := range map[float64]string{5: "5s", 119: "1m", 3700: "1h", 200000: "2d"} {
		if got := age(in); got != want {
			t.Errorf("%v: %s want %s", in, got, want)
		}
	}
}
