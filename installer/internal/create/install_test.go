package create

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

type fakeInst struct {
	known    bool
	readyErr error
	knownErr error
	shipErr  error
	// draws is the session a verb of the desktop draws its own failed step on, as ship does.
	draws ui.Session
	calls []string
}

func (f *fakeInst) Ready() error {
	f.calls = append(f.calls, "ready")
	return f.readyErr
}

func (f *fakeInst) RepoRoot() (string, error) { return "/repo", nil }

func (f *fakeInst) HostKnown(name, ssh string) (bool, error) {
	f.calls = append(f.calls, "known "+name+" "+ssh)
	return f.known, f.knownErr
}

func (f *fakeInst) AddHost(_ context.Context, name, ssh, machine string) error {
	f.calls = append(f.calls, "add "+name+" "+ssh+" "+machine)
	return nil
}

func (f *fakeInst) Ship(_ context.Context, name string) error {
	f.calls = append(f.calls, "ship "+name)
	if f.draws != nil && f.shipErr != nil {
		f.draws.Step("Copy").Fail(f.shipErr)
	}
	return f.shipErr
}

func (f *fakeInst) Login(_ context.Context, name string) error {
	f.calls = append(f.calls, "login "+name)
	return nil
}

const (
	snapQuery  = "incus query /1.0/instances/flocc-a/snapshots/installed"
	snapCreate = "incus snapshot create flocc-a installed-new"
	snapDelete = "incus snapshot delete flocc-a installed"
	snapRename = "incus snapshot rename flocc-a installed-new installed"
)

func createMachine() *host.Fake {
	return host.NewFake().On("id -nG", 0, "me incus-admin\n").On("incus query /1.0", 0, "{}").
		On(snapCreate, 0, "").On(snapRename, 0, "")
}

// createEnv is testEnv with a desktop whose tailnet already lists flocc-a, as it does once the
// VM has joined.
func createEnv(m *host.Fake) (*Env, *fakeUI) {
	e, u := testEnv(m)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsStatus)
	e.TTY = true
	return e, u
}

func rowNames(u *fakeUI) []string {
	var names []string
	for _, r := range u.Rows() {
		names = append(names, r.Name)
	}
	return names
}

func TestCreateRunsStepsThenTheVerbsThenSnapshots(t *testing.T) {
	f := createMachine()
	e, u := createEnv(f)
	inst := &fakeInst{}
	step := &fakeStep{name: "s"}
	if err := Create(context.Background(), e, inst, []Step{step}); err != nil {
		t.Fatal(err)
	}
	if f.Calls[0] != "interactive: true" || step.applied != 1 {
		t.Fatalf("%v applied=%d", f.Calls, step.applied)
	}
	if want := []string{"known flocc-a root@flocc-a", "add flocc-a root@flocc-a laptop", "ship flocc-a", "login flocc-a"}; !slices.Equal(inst.calls, want) {
		t.Fatalf("%q", inst.calls)
	}
	if !f.Ran(snapCreate) || !f.Ran(snapRename) || f.Ran(snapDelete) {
		t.Fatalf("%v", f.Calls)
	}
	want := []string{"Connect", "s", "Tailnet name", "Snapshot"}
	if got := rowNames(u); !slices.Equal(got, want) {
		t.Fatalf("rows %q", got)
	}
	if last := u.Rows()[len(want)-1]; !strings.Contains(last.Detail, "incus snapshot restore flocc-a installed") {
		t.Fatalf("%+v", last)
	}
}

func TestLoggedNamesEachCreateLog(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	os.MkdirAll(filepath.Join(dir, "flocc"), 0o700)
	for _, n := range []string{"create-a.log", "create-flocc-nimble.log", "cardinal-cardinal.log"} {
		os.WriteFile(filepath.Join(dir, "flocc", n), nil, 0o600)
	}
	if got := Logged(); !slices.Equal(got, []string{"a", "flocc-nimble"}) {
		t.Fatalf("%v", got)
	}
}

func TestCreateWaitsForThisDesktopToSeeTheNode(t *testing.T) {
	e, u := createEnv(createMachine())
	e.Local = host.NewFake().OnSeq("tailscale status --json", host.Result{Stdout: tsEmpty}, host.Result{Stdout: tsEmpty}, host.Result{Stdout: tsStatus})
	sleeps := 0
	e.Sleep = func(time.Duration) { sleeps++ }
	inst := &fakeInst{}
	if err := Create(context.Background(), e, inst, nil); err != nil {
		t.Fatal(err)
	}
	if sleeps != 2 || inst.calls[0] != "known flocc-a root@flocc-a" {
		t.Fatalf("sleeps=%d %q", sleeps, inst.calls)
	}
	var left []string
	for _, ev := range u.Events {
		if ev.Name == "Tailnet name" && ev.Detail != "" && ev.State == ui.Running {
			left = append(left, ev.Detail)
		}
	}
	if want := []string{"waiting for the tailnet name, 60s left", "waiting for the tailnet name, 58s left"}; !slices.Equal(left, want) {
		t.Fatalf("%q", left)
	}
}

func TestCreateStopsWhenThisDesktopNeverSeesTheNode(t *testing.T) {
	e, u := createEnv(createMachine())
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	inst := &fakeInst{}
	err := Create(context.Background(), e, inst, nil)
	if err == nil || !strings.Contains(err.Error(), "does not see it after 1m0s") || len(inst.calls) != 0 {
		t.Fatalf("%v %q", err, inst.calls)
	}
	if rows := u.Rows(); rows[len(rows)-1].Name != "Tailnet name" || rows[len(rows)-1].State != ui.Failed {
		t.Fatalf("%+v", rows)
	}
}

func TestCreateSkipsHostAddForAKnownHost(t *testing.T) {
	e, _ := createEnv(createMachine())
	inst := &fakeInst{known: true}
	if err := Create(context.Background(), e, inst, nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"known flocc-a root@flocc-a", "ship flocc-a", "login flocc-a"}; !slices.Equal(inst.calls, want) {
		t.Fatalf("%q", inst.calls)
	}
}

func TestCreateStopsWhenTheHostFileReachesSomethingElse(t *testing.T) {
	e, _ := createEnv(createMachine())
	inst := &fakeInst{known: true, knownErr: errors.New("flocc-a.toml reaches daytona:flocc-a")}
	err := Create(context.Background(), e, inst, nil)
	if err == nil || !strings.Contains(err.Error(), "daytona:flocc-a") || len(inst.calls) != 1 {
		t.Fatalf("%v %q", err, inst.calls)
	}
}

func TestCreateStopsWhenShipFails(t *testing.T) {
	f := createMachine()
	e, u := createEnv(f)
	inst := &fakeInst{shipErr: errors.New("doctor reports 2 FAIL lines")}
	err := Create(context.Background(), e, inst, nil)
	if err == nil || err.Error() != "ship: doctor reports 2 FAIL lines" || slices.Contains(inst.calls, "login flocc-a") || f.Ran(snapCreate) {
		t.Fatalf("%v %q %v", err, inst.calls, f.Calls)
	}
	if rows := u.Rows(); rows[len(rows)-1].Name != "Ship" || rows[len(rows)-1].Err != "ship: doctor reports 2 FAIL lines" {
		t.Fatalf("%+v", rows)
	}
}

func TestCreateDoesNotRepeatAFailureTheVerbAlreadyDrew(t *testing.T) {
	e, u := createEnv(createMachine())
	inst := &fakeInst{shipErr: errors.New("copy failed"), draws: e.UI}
	if err := Create(context.Background(), e, inst, nil); err == nil {
		t.Fatal("no error")
	}
	var failed []string
	for _, r := range u.Rows() {
		if r.State == ui.Failed {
			failed = append(failed, r.Name)
		}
	}
	if !slices.Equal(failed, []string{"Copy"}) {
		t.Fatalf("failed rows %q", failed)
	}
}

func TestCreateStopsWhenAStepFails(t *testing.T) {
	e, _ := testEnv(createMachine())
	inst := &fakeInst{}
	err := Create(context.Background(), e, inst, []Step{&fakeStep{name: "Firewalld", applyErr: errors.New("no")}})
	if err == nil || len(inst.calls) != 0 {
		t.Fatalf("%v %q", err, inst.calls)
	}
}

func TestCreateStopsWhenTheMachineIsUnreachable(t *testing.T) {
	f := host.NewFake().OnInteractive("true", errors.New("exit status 255"))
	e, _ := testEnv(f)
	err := Create(context.Background(), e, &fakeInst{}, nil)
	if err == nil || !strings.Contains(err.Error(), "ssh laptop") || len(f.Calls) != 1 {
		t.Fatalf("%v %v", err, f.Calls)
	}
}

func TestSnapshotSwapsTheNewOneInAfterItExists(t *testing.T) {
	f := host.NewFake().On(snapQuery, 0, "{}").On(snapDelete, 0, "").On(snapCreate, 0, "").On(snapRename, 0, "")
	e, _ := testEnv(f)
	if err := snapshot(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	mk, del, mv := slices.Index(f.Calls, snapCreate), slices.Index(f.Calls, snapDelete), slices.Index(f.Calls, snapRename)
	if mk < 0 || del < mk || mv < del {
		t.Fatalf("%v", f.Calls)
	}
}

func TestSnapshotRestoreLineCarriesSudoWhenIncusNeedsIt(t *testing.T) {
	f := host.NewFake().On("sudo -n "+snapCreate, 0, "").On("sudo -n "+snapRename, 0, "")
	e, u := testEnv(f)
	e.incusSudo = true
	if err := snapshot(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if rows := u.Rows(); len(rows) != 1 || !strings.Contains(rows[0].Detail, "; sudo incus snapshot restore flocc-a installed returns") {
		t.Fatalf("%+v", rows)
	}
}

func TestSnapshotKeepsTheOldOneWhenTheNewOneFails(t *testing.T) {
	f := host.NewFake().On(snapQuery, 0, "{}").On(snapCreate, 1, "no space left")
	e, u := testEnv(f)
	if err := snapshot(context.Background(), e); err == nil || f.Ran(snapDelete) {
		t.Fatalf("%v %v", err, f.Calls)
	}
	if rows := u.Rows(); len(rows) != 1 || rows[0].State != ui.Failed {
		t.Fatalf("%+v", rows)
	}
}

const snapQueryNew = "incus query /1.0/instances/flocc-a/snapshots/installed-new"

func TestSnapshotClearsALeftoverNewOne(t *testing.T) {
	f := host.NewFake().On(snapQueryNew, 0, "{}").On(snapQuery, 0, "{}").On("incus snapshot delete flocc-a installed-new", 0, "").
		On(snapCreate, 0, "").On(snapDelete, 0, "").On(snapRename, 0, "")
	e, _ := testEnv(f)
	if err := snapshot(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(f.Calls, "incus snapshot delete flocc-a installed-new"); i < 0 || i > slices.Index(f.Calls, snapCreate) {
		t.Fatalf("%v", f.Calls)
	}
}

// A run stopped after deleting installed and before the rename leaves installed-new as the only
// good snapshot; the next run must keep it.
func TestSnapshotRecoversTheNewOneWhenTheOldOneIsGone(t *testing.T) {
	f := host.NewFake().On(snapQueryNew, 0, "{}").OnSeq(snapQuery, host.Result{Code: 1}, host.Result{Stdout: "{}"}).
		On(snapRename, 0, "").On(snapCreate, 0, "").On(snapDelete, 0, "")
	e, _ := testEnv(f)
	if err := snapshot(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	var changes []string
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "incus snapshot ") {
			changes = append(changes, c)
		}
	}
	if want := []string{snapRename, snapCreate, snapDelete, snapRename}; !slices.Equal(changes, want) {
		t.Fatalf("%q", changes)
	}
}

// host add, ship and login can outlast ControlPersist, so the machine's master is opened again
// on the terminal before the snapshot's captured commands.
func TestCreateReconnectsToTheMachineBeforeTheSnapshot(t *testing.T) {
	f := createMachine().On("tailscale status --json", 0, tsStatus)
	e, _ := createEnv(f)
	e.Local = f
	if err := Create(context.Background(), e, &fakeInst{}, nil); err != nil {
		t.Fatal(err)
	}
	check := slices.IndexFunc(f.Calls, func(c string) bool { return strings.HasSuffix(c, "-O check laptop") })
	warm := slices.IndexFunc(f.Calls, func(c string) bool {
		return strings.HasPrefix(c, "interactive: ssh ") && strings.HasSuffix(c, " laptop true")
	})
	seen := slices.Index(f.Calls, "tailscale status --json")
	if check < seen || warm < check || slices.Index(f.Calls, snapCreate) < warm {
		t.Fatalf("%v", f.Calls)
	}
}

func TestStepsRunInTheSpecsOrder(t *testing.T) {
	var names []string
	for _, s := range Steps() {
		names = append(names, s.Name())
	}
	if want := []string{"Incus", "Root pass", "VM", "Egress", "Tailscale"}; !slices.Equal(names, want) {
		t.Fatalf("%q", names)
	}
}

func TestParseArgs(t *testing.T) {
	s, err := ParseArgs([]string{"zer0-day-nimble", "flocc-two"})
	if err != nil || s != (Spec{Machine: "zer0-day-nimble", Name: "flocc-two", CPUs: 4, MemoryGiB: 8, DiskGiB: 40}) {
		t.Fatalf("%+v %v", s, err)
	}
	s, err = ParseArgs([]string{"--cpus", "2", "--memory", "6", "--disk", "60", "me@box", "f2"})
	if err != nil || s.CPUs != 2 || s.MemoryGiB != 6 || s.DiskGiB != 60 || s.Machine != "me@box" {
		t.Fatalf("%+v %v", s, err)
	}
	for _, bad := range [][]string{
		nil, {"box"}, {"box", "a", "b"},
		{"-oProxyCommand=x", "f2"}, {"box", "Flocc"}, {"box", "1abc"}, {"box", "ends-"}, {"box", "has_underscore"},
		{"--disk", "5", "box", "f2"}, {"--memory", "0", "box", "f2"},
	} {
		if _, err := ParseArgs(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCheckStopsAtTheCheckoutBeforeLookingAtTheHostFile(t *testing.T) {
	inst := &fakeInst{readyErr: errors.New("flocc ship ships the flocc checkout it runs in; cd into it")}
	err := Check(Spec{Name: "flocc-a"}, inst)
	if err == nil || !strings.Contains(err.Error(), "flocc ship ships the flocc checkout") || !slices.Equal(inst.calls, []string{"ready"}) {
		t.Fatalf("%v %q", err, inst.calls)
	}
}

func TestCheckLooksAtTheHostFile(t *testing.T) {
	inst := &fakeInst{knownErr: errors.New("flocc-a.toml reaches daytona:flocc-a")}
	err := Check(Spec{Name: "flocc-a"}, inst)
	if err == nil || !strings.Contains(err.Error(), "daytona:flocc-a") || !slices.Equal(inst.calls, []string{"ready", "known flocc-a root@flocc-a"}) {
		t.Fatalf("%v %q", err, inst.calls)
	}
}

// With no ssh on PATH a Run that went on to connect fails without reaching any machine.
func TestRunNamesItsLogAndFailsTheConnectStep(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	u := &fakeUI{}
	err := Run(context.Background(), Spec{Machine: "box", Name: "flocc-a"}, &fakeInst{}, ui.New(u), false)
	var logs []string
	for _, ev := range u.Events {
		if ev.Kind == ui.KLog {
			logs = append(logs, ev.Text)
		}
	}
	rows := u.Rows()
	if err == nil || len(logs) != 1 || !strings.HasSuffix(logs[0], "create-flocc-a.log") || len(rows) != 1 || rows[0].Name != "Connect" || rows[0].State != ui.Failed {
		t.Fatalf("%v %q %+v", err, logs, rows)
	}
}
