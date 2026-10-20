package create

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

type fakeInst struct {
	known   bool
	shipErr error
	calls   []string
}

func (f *fakeInst) HostKnown(name string) bool {
	f.calls = append(f.calls, "known "+name)
	return f.known
}

func (f *fakeInst) AddHost(_ context.Context, name, ssh string) error {
	f.calls = append(f.calls, "add "+name+" "+ssh)
	return nil
}

func (f *fakeInst) Ship(_ context.Context, name string) error {
	f.calls = append(f.calls, "ship "+name)
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
func createEnv(m *host.Fake) (*Env, *bytes.Buffer) {
	e, out := testEnv(m)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsStatus)
	e.TTY = true
	return e, out
}

func TestCreateRunsStepsThenTheVerbsThenSnapshots(t *testing.T) {
	f := createMachine()
	e, out := createEnv(f)
	inst := &fakeInst{}
	step := &fakeStep{name: "s"}
	if err := Create(context.Background(), e, inst, []Step{step}); err != nil {
		t.Fatal(err)
	}
	if f.Calls[0] != "interactive: true" || step.applied != 1 {
		t.Fatalf("%v applied=%d", f.Calls, step.applied)
	}
	if want := []string{"known flocc-a", "add flocc-a root@flocc-a", "ship flocc-a", "login flocc-a"}; !slices.Equal(inst.calls, want) {
		t.Fatalf("%q", inst.calls)
	}
	if !f.Ran(snapCreate) || !f.Ran(snapRename) || f.Ran(snapDelete) {
		t.Fatalf("%v", f.Calls)
	}
	if !strings.Contains(out.String(), "incus snapshot restore flocc-a installed") {
		t.Fatal(out.String())
	}
}

func TestCreateWaitsForThisDesktopToSeeTheNode(t *testing.T) {
	e, _ := createEnv(createMachine())
	e.Local = host.NewFake().OnSeq("tailscale status --json", host.Result{Stdout: tsEmpty}, host.Result{Stdout: tsEmpty}, host.Result{Stdout: tsStatus})
	sleeps := 0
	e.Sleep = func(time.Duration) { sleeps++ }
	inst := &fakeInst{}
	if err := Create(context.Background(), e, inst, nil); err != nil {
		t.Fatal(err)
	}
	if sleeps != 2 || inst.calls[0] != "known flocc-a" {
		t.Fatalf("sleeps=%d %q", sleeps, inst.calls)
	}
}

func TestCreateStopsWhenThisDesktopNeverSeesTheNode(t *testing.T) {
	e, _ := createEnv(createMachine())
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	inst := &fakeInst{}
	err := Create(context.Background(), e, inst, nil)
	if err == nil || !strings.Contains(err.Error(), "does not see it after 1m0s") || len(inst.calls) != 0 {
		t.Fatalf("%v %q", err, inst.calls)
	}
}

func TestCreateSkipsHostAddForAKnownHost(t *testing.T) {
	e, _ := createEnv(createMachine())
	inst := &fakeInst{known: true}
	if err := Create(context.Background(), e, inst, nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"known flocc-a", "ship flocc-a", "login flocc-a"}; !slices.Equal(inst.calls, want) {
		t.Fatalf("%q", inst.calls)
	}
}

func TestCreateStopsWhenShipFails(t *testing.T) {
	f := createMachine()
	e, _ := createEnv(f)
	inst := &fakeInst{shipErr: errors.New("doctor reports 2 FAIL lines")}
	err := Create(context.Background(), e, inst, nil)
	if err == nil || err.Error() != "ship: doctor reports 2 FAIL lines" || slices.Contains(inst.calls, "login flocc-a") || f.Ran(snapCreate) {
		t.Fatalf("%v %q %v", err, inst.calls, f.Calls)
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
	e, out := testEnv(f)
	e.incusSudo = true
	if err := snapshot(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "; sudo incus snapshot restore flocc-a installed returns") {
		t.Fatal(out.String())
	}
}

func TestSnapshotKeepsTheOldOneWhenTheNewOneFails(t *testing.T) {
	f := host.NewFake().On(snapQuery, 0, "{}").On(snapCreate, 1, "no space left")
	e, _ := testEnv(f)
	if err := snapshot(context.Background(), e); err == nil || f.Ran(snapDelete) {
		t.Fatalf("%v %v", err, f.Calls)
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
	if want := []string{"Incus", "Firewalld", "Docker", "VM", "Egress", "Tailscale"}; !slices.Equal(names, want) {
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

func TestMainRejectsBadArgumentsBeforeConnecting(t *testing.T) {
	if code := Main([]string{"box"}, &fakeInst{}); code != 2 {
		t.Fatal(code)
	}
}
