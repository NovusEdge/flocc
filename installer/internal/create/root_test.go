package create

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/rootpass"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

const rootDir = "/tmp/flocc-create.Ab12Cd"

// rootMachine is a machine where firewalld runs, with a desktop that answers every step of the
// pass except the sudo line, which carries out.
func rootMachine(t *testing.T, out string, runErr error) (*Env, *fakeUI, *host.Fake, *host.Fake) {
	t.Helper()
	m := host.NewFake().
		On("systemctl is-active --quiet firewalld.service", 0, "").
		On("uname -m", 0, "x86_64\n")
	e, u := testEnv(m)
	local := host.NewFake().OnName("go", 0, "").OnName("scp", 0, "")
	e.Local = local
	e.Checkout = func() (string, error) { return t.TempDir(), nil }
	setup := rootDir + "/flocc-setup"
	local.On(rootpass.SSH(ControlOpts(), false, "laptop", "mktemp -d /tmp/flocc-create.XXXXXX").String(), 0, rootDir+"\n")
	line := rootpass.SSH(ControlOpts(), true, "laptop", "chmod 755 "+setup+" && sudo "+setup+" --phase create-root && rm -rf "+rootDir).String()
	local.OnInteractiveOut(line, out)
	if runErr != nil {
		local.OnInteractive(line, runErr)
	}
	return e, u, m, local
}

func rowFor(u *fakeUI, name string) (ui.Event, bool) {
	for _, r := range u.Rows() {
		if r.Name == name {
			return r, true
		}
	}
	return ui.Event{}, false
}

func TestRootSkipsThePassWithoutFirewalldOrDockerOnceTheTailnetBlockIsIn(t *testing.T) {
	m := host.NewFake().
		On("systemctl is-enabled --quiet flocc-no-tailnet.service", 0, "").
		On("systemctl is-active --quiet flocc-no-tailnet.service", 0, "")
	e, _ := testEnv(m)
	local := host.NewFake()
	e.Local = local
	if err := RunSteps(context.Background(), e, []Step{Root{}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range local.Calls {
		if strings.Contains(c, "mktemp") || strings.Contains(c, "scp") || strings.Contains(c, " -t ") {
			t.Fatalf("ran %q", c)
		}
	}
}

func TestRootRunsThePassWhenOnlyTheTailnetBlockIsMissing(t *testing.T) {
	e, _ := testEnv(host.NewFake())
	if ok, _, err := (Root{}).Check(context.Background(), e); ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
}

func TestRootFailsClosedWhenSSHFailsDuringItsCheck(t *testing.T) {
	m := host.NewFake().On("systemctl is-active --quiet firewalld.service", 255, "")
	e, _ := testEnv(m)
	err := RunSteps(context.Background(), e, []Step{Root{}})
	if err == nil || !strings.Contains(err.Error(), "cannot reach laptop over ssh") {
		t.Fatalf("err = %v", err)
	}
}

func TestRootRefusesAPasswordSudoWithoutATerminal(t *testing.T) {
	m := host.NewFake().On("systemctl is-active --quiet firewalld.service", 0, "")
	e, _ := testEnv(m)
	e.TTY, e.sudo = false, false
	local := host.NewFake()
	e.Local = local
	err := RunSteps(context.Background(), e, []Step{Root{}})
	if err == nil || !strings.Contains(err.Error(), "run create in a terminal") || len(local.Calls) > 0 {
		t.Fatalf("err %v calls %q", err, local.Calls)
	}
}

func TestRootRunsOnePassAndDrawsItsSteps(t *testing.T) {
	r := steps.RootReport{Steps: []steps.ReportStep{
		{Name: "Firewalld", Detail: "incusbr0 in zone flocc-incus", Done: true},
		{Name: "Docker", Detail: "Docker not running", Done: true}}}
	e, u, m, local := rootMachine(t, r.Line()+"\n", nil)
	if err := RunSteps(context.Background(), e, []Step{Root{}}); err != nil {
		t.Fatalf("%v; %v", err, local.Calls)
	}
	for _, name := range []string{"Firewalld", "Docker"} {
		if row, ok := rowFor(u, name); !ok || row.State != ui.Done {
			t.Errorf("row %s: %+v %v", name, row, ok)
		}
	}
	if row, ok := rowFor(u, "Root pass"); !ok || row.State != ui.Done || row.Detail != "firewalld, Docker and the tailnet block checked as root on laptop" {
		t.Errorf("Root pass row: %+v %v", row, ok)
	}
	if len(local.Interactives) != 1 {
		t.Fatalf("interactive commands: %v", local.Calls)
	}
	if got, want := local.Interactives[0].Why, "root pass: sudo on laptop may ask for your password"; got != want {
		t.Errorf("why %q", got)
	}
	dockerUntouched(t, local.Calls)
	dockerUntouched(t, m.Calls)
}

func TestRootFailsOnAStepNotDone(t *testing.T) {
	r := steps.RootReport{Steps: []steps.ReportStep{
		{Name: "Firewalld", Detail: "ok", Done: true},
		{Name: "Docker", Detail: "iptables DOCKER-USER lacks -i incusbr0 -j ACCEPT", Done: false}}}
	e, _, _, _ := rootMachine(t, r.Line()+"\n", nil)
	err := RunSteps(context.Background(), e, []Step{Root{}})
	if err == nil || !strings.Contains(err.Error(), "Docker") || !strings.Contains(err.Error(), "lacks") {
		t.Fatal(err)
	}
}

func TestRootFailsWhenThePassFails(t *testing.T) {
	r := steps.RootReport{Error: "Firewalld: firewalld on this machine uses the iptables backend"}
	e, _, _, _ := rootMachine(t, r.Line()+"\n", errors.New("exit status 1"))
	err := RunSteps(context.Background(), e, []Step{Root{}})
	if err == nil || !strings.Contains(err.Error(), "iptables backend") || !strings.Contains(err.Error(), "setup.log") {
		t.Fatal(err)
	}
}
