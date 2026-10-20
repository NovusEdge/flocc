package create

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

type fakeUI struct {
	key   string
	asked int
	onAsk func()
}

func (u *fakeUI) Secret(string, string, string) (string, error) {
	u.asked++
	if u.onAsk != nil {
		u.onAsk()
	}
	return u.key, nil
}

// testEnv runs machine commands on m directly, without the ssh wrapper, so tests name the
// commands as the machine sees them. With TTY, e.connect's attached ssh succeeds on any Fake.
func testEnv(m *host.Fake) (*Env, *bytes.Buffer) {
	var out bytes.Buffer
	return &Env{
		M: m, Local: host.NewFake(), UI: &fakeUI{}, Out: &out, Log: NewRedactLog(io.Discard),
		Spec:  Spec{Machine: "laptop", Name: "flocc-a", CPUs: 4, MemoryGiB: 8, DiskGiB: 40},
		Sleep: func(time.Duration) {}, sudo: true, TTY: true,
	}, &out
}

type recorder struct{ run, interactive []host.Cmd }

func (r *recorder) Run(_ context.Context, c host.Cmd) (host.Result, error) {
	r.run = append(r.run, c)
	return host.Result{}, nil
}

func (r *recorder) Interactive(_ context.Context, c host.Cmd) error {
	r.interactive = append(r.interactive, c)
	return nil
}

func isDocker(unit string) bool {
	return slices.Contains([]string{"docker", "docker.service", "docker.socket", "dockerd"}, unit)
}

// touchesDocker reports whether c could start, stop, reload or kill dockerd. It reads c as words,
// so a verb or unit in any order among flags and other units counts, as does one behind sudo,
// sh -c or a ; or &&.
func touchesDocker(c string) bool {
	c = strings.NewReplacer("'", " ", `"`, " ", "`", " ", "(", " ", ")", " ", "&", ";", "|", ";", "\n", ";").Replace(c)
	for _, seg := range strings.Split(c, ";") {
		f := strings.Fields(seg)
		for i, w := range f {
			var words []string // the arguments after w, without flags
			for _, a := range f[i+1:] {
				if !strings.HasPrefix(a, "-") {
					words = append(words, a)
				}
			}
			switch base := path.Base(w); {
			case base == "systemctl":
				readOnly := []string{"is-active", "is-enabled", "is-failed", "status", "show", "cat", "list-units", "list-unit-files"}
				if len(words) > 0 && !slices.Contains(readOnly, words[0]) && slices.ContainsFunc(words[1:], isDocker) {
					return true
				}
			case base == "service" || base == "invoke-rc.d":
				if len(words) > 0 && isDocker(words[0]) && (len(words) < 2 || words[1] != "status") {
					return true
				}
			case strings.HasSuffix(w, "init.d/docker"):
				if len(words) == 0 || words[0] != "status" {
					return true
				}
			case base == "kill" || base == "pkill" || base == "killall":
				if slices.ContainsFunc(words, func(a string) bool { return isDocker(a) || strings.Contains(a, "dockerd") }) {
					return true
				}
			case base == "dockerd":
				if i == 0 || slices.Contains([]string{"sudo", "-n", "env", "exec", "nohup", "--", "-c"}, f[i-1]) {
					return true
				}
			}
		}
	}
	return false
}

func TestTouchesDocker(t *testing.T) {
	for _, c := range []string{
		"sudo -n systemctl enable --now docker.service",
		"sudo -n systemctl disable --now docker.service",
		"sudo -n systemctl --no-block restart docker",
		"sudo -n systemctl restart --no-block docker.service",
		"sudo -n systemctl try-reload-or-restart docker",
		"sudo -n systemctl reload-or-try-restart docker",
		"sudo -n systemctl force-reload docker",
		"sudo -n systemctl condrestart docker",
		"sudo -n systemctl mask docker.socket",
		"sudo -n /usr/bin/systemctl stop docker.socket",
		"sudo -n systemctl restart flocc-docker-user.service docker.service",
		"sh -c 'iptables -L && systemctl restart docker'",
		"sudo -n /etc/init.d/docker restart",
		"sudo -n service docker restart",
		"sudo -n kill $(pidof dockerd)",
		"sudo -n pkill dockerd",
		"sudo -n pkill -f /usr/bin/dockerd",
		"sudo -n killall dockerd",
		"sudo -n dockerd",
	} {
		if !touchesDocker(c) {
			t.Errorf("not caught: %s", c)
		}
	}
	for _, c := range []string{
		"systemctl is-active docker",
		"systemctl is-active --quiet docker.service",
		"systemctl is-enabled --quiet docker.service",
		"systemctl show -p ActiveState docker.service",
		"sudo -n systemctl daemon-reload",
		"sudo -n systemctl enable --now flocc-docker-user.service",
		"sudo -n systemctl restart flocc-docker-user.service",
		"service docker status",
		"pidof dockerd",
		"After=docker.service",
	} {
		if touchesDocker(c) {
			t.Errorf("wrongly caught: %s", c)
		}
	}
}

// dockerUntouched fails when any command could stop, start or reload dockerd.
func dockerUntouched(t *testing.T, calls []string) {
	t.Helper()
	for _, c := range calls {
		if touchesDocker(c) {
			t.Errorf("touches Docker: %s", c)
		}
	}
}

// rootCalls lists the commands that need root: sudo, firewall-cmd, iptables, ip6tables, tee,
// docker, and the systemctl verbs that change state. Each must be a sudo -n call.
func rootCalls(calls []string) []string {
	var r []string
	for _, c := range calls {
		f := strings.Fields(c)
		switch {
		case len(f) == 0:
		case slices.Contains([]string{"sudo", "firewall-cmd", "iptables", "ip6tables", "tee", "docker"}, f[0]):
			r = append(r, c)
		case f[0] == "systemctl" && len(f) > 1 && slices.Contains([]string{"enable", "disable", "start", "stop", "restart", "daemon-reload"}, f[1]):
			r = append(r, c)
		}
	}
	return r
}

// shWords has sh parse line into words, the way the remote login shell parses ssh's command.
func shWords(t *testing.T, line string) []string {
	t.Helper()
	out, err := exec.Command("sh", "-c", "for a in "+line+`; do printf '%s\0' "$a"; done`).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
}

func TestRemoteQuotesEachWordForTheRemoteShell(t *testing.T) {
	rec := &recorder{}
	r := Remote{R: rec, Target: "laptop", Opts: []string{"-o", "BatchMode=yes"}}
	argv := []string{"incus", "exec", "flocc-a", "--", "sh", "-c", "echo $HOME 'it''s' `id`; a b"}
	r.Run(context.Background(), host.Cmd{Name: argv[0], Args: argv[1:], Stdin: strings.NewReader("secret")})
	got := rec.run[0]
	// -T: a RequestTTY in the owner's ssh config would echo stdin, a secret, back and hold EOF.
	if got.Name != "ssh" || !slices.Equal(got.Args[:4], []string{"-o", "BatchMode=yes", "-T", "laptop"}) || len(got.Args) != 5 {
		t.Fatalf("%q", got.Args)
	}
	if words := shWords(t, got.Args[4]); !slices.Equal(words, argv) {
		t.Fatalf("%q", words)
	}
	if got.Stdin == nil {
		t.Fatal("stdin dropped")
	}
	if b, _ := io.ReadAll(got.Stdin); string(b) != "secret" {
		t.Fatalf("stdin %q", b)
	}
}

func TestRemoteInteractiveAsksForATerminal(t *testing.T) {
	rec := &recorder{}
	Remote{R: rec, Target: "laptop"}.Interactive(context.Background(), cmd("true"))
	if got := rec.interactive[0]; got.String() != "ssh -t laptop 'true'" {
		t.Fatalf("%q", got.String())
	}
}

func TestControlOptsShareOneConnection(t *testing.T) {
	o := strings.Join(ControlOpts(), " ")
	for _, want := range []string{"ControlMaster=auto", "ControlPersist=", "ControlPath="} {
		if !strings.Contains(o, want) {
			t.Fatalf("%s lacks %s", o, want)
		}
	}
}

func TestProbeRunsIncusDirectlyForIncusAdmins(t *testing.T) {
	f := host.NewFake().On("id -nG", 0, "me incus-admin wheel\n").On("incus query /1.0", 0, "{}")
	e, _ := testEnv(f)
	if err := e.Probe(context.Background()); err != nil || e.incusSudo || e.sudo {
		t.Fatalf("err=%v incusSudo=%v sudo=%v", err, e.incusSudo, e.sudo)
	}
	if e.incus("list").String() != "incus list" {
		t.Fatal(e.incus("list").String())
	}
}

func TestProbeFallsBackToSudo(t *testing.T) {
	f := host.NewFake().On("sudo -n true", 0, "").On("sudo -n incus query /1.0", 0, "{}")
	e, _ := testEnv(f)
	if err := e.Probe(context.Background()); err != nil || !e.incusSudo || !e.sudo {
		t.Fatalf("err=%v incusSudo=%v sudo=%v", err, e.incusSudo, e.sudo)
	}
	if e.incus("list").String() != "sudo -n incus list" {
		t.Fatal(e.incus("list").String())
	}
}

// incus query /1.0 also works for the restricted incus group, through that user's own project,
// where incusbr0 and the other instances do not exist.
func TestProbeDoesNotTrustTheRestrictedIncusGroup(t *testing.T) {
	f := host.NewFake().On("id -nG", 0, "me incus\n").On("incus query /1.0", 0, "{}")
	e, _ := testEnv(f)
	err := e.Probe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "incus-admin") || !strings.Contains(err.Error(), "restricted") {
		t.Fatal(err)
	}
}

func TestProbeUsesSudoForAMemberOfTheRestrictedIncusGroup(t *testing.T) {
	f := host.NewFake().On("id -nG", 0, "me incus\n").On("incus query /1.0", 0, "{}").
		On("sudo -n true", 0, "").On("sudo -n incus query /1.0", 0, "{}")
	e, _ := testEnv(f)
	if err := e.Probe(context.Background()); err != nil || !e.incusSudo {
		t.Fatalf("err=%v incusSudo=%v", err, e.incusSudo)
	}
}

func TestProbeStopsWithoutIncusAccess(t *testing.T) {
	e, _ := testEnv(host.NewFake())
	err := e.Probe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "incus-admin") || !strings.Contains(err.Error(), "laptop") {
		t.Fatal(err)
	}
}

// A live shared connection keeps the groups it logged in with, so usermod alone changes nothing
// for create until that master closes.
func TestProbeSaysToCloseTheSharedConnectionAfterUsermod(t *testing.T) {
	opts := []string{"-o", "ControlMaster=auto", "-o", "ControlPath=/h/.ssh/flocc-%C", "-o", "ControlPersist=10m"}
	if got := regroup("laptop", opts); !strings.Contains(got, "ssh -o 'ControlPath=/h/.ssh/flocc-%C' -O exit laptop") {
		t.Fatal(got)
	}
	if got := regroup("laptop", nil); got != "then log in again" {
		t.Fatal(got)
	}
	e, _ := testEnv(host.NewFake())
	if err := e.Probe(context.Background()); err == nil || !strings.Contains(err.Error(), regroup("laptop", ControlOpts())) {
		t.Fatal(err)
	}
}

func TestNeedRootNamesTheFix(t *testing.T) {
	e, _ := testEnv(host.NewFake())
	if e.needRoot("x") != nil {
		t.Fatal("sudo works, so root is available")
	}
	e.sudo = false
	if err := e.needRoot("Reading firewalld"); err == nil || !strings.Contains(err.Error(), "passwordless sudo") {
		t.Fatal(err)
	}
}

func TestRedactLogHidesSecretsAddedAfterItsFirstWrite(t *testing.T) {
	var b bytes.Buffer
	l := NewRedactLog(&b)
	l.Write([]byte("before tskey-1\n"))
	l.Add("tskey-1")
	l.Add("")
	l.Write([]byte("after tskey-1\n"))
	if b.String() != "before tskey-1\nafter <token>\n" {
		t.Fatalf("%q", b.String())
	}
	var none *RedactLog
	none.Add("x") // an Env built without a log must not panic
}

type fakeStep struct {
	name        string
	done        bool
	applied     int
	applyErr    error
	staysUndone bool
}

func (s *fakeStep) Name() string { return s.name }
func (s *fakeStep) Check(context.Context, *Env) (bool, string, error) {
	return s.done, "detail " + s.name, nil
}
func (s *fakeStep) Apply(context.Context, *Env) error {
	s.applied++
	if s.applyErr != nil {
		return s.applyErr
	}
	s.done = !s.staysUndone
	return nil
}

func TestRunStepsSkipsDoneStepsAndAppliesTheRest(t *testing.T) {
	a, b := &fakeStep{name: "a", done: true}, &fakeStep{name: "b"}
	e, out := testEnv(host.NewFake())
	if err := RunSteps(context.Background(), e, []Step{a, b}); err != nil {
		t.Fatal(err)
	}
	if a.applied != 0 || b.applied != 1 {
		t.Fatalf("a=%d b=%d", a.applied, b.applied)
	}
	if !strings.Contains(out.String(), "✓ a") || !strings.Contains(out.String(), "• b") || !strings.Contains(out.String(), "✓ b") {
		t.Fatal(out.String())
	}
}

func TestRunStepsStopsAtTheFirstFailure(t *testing.T) {
	a, b := &fakeStep{name: "a", applyErr: errors.New("boom")}, &fakeStep{name: "b"}
	e, _ := testEnv(host.NewFake())
	err := RunSteps(context.Background(), e, []Step{a, b})
	if err == nil || err.Error() != "a: boom" || b.applied != 0 {
		t.Fatalf("err=%v b=%d", err, b.applied)
	}
}

func TestRunStepsFailsWhenTheCheckStillFails(t *testing.T) {
	e, _ := testEnv(host.NewFake())
	err := RunSteps(context.Background(), e, []Step{&fakeStep{name: "a", staysUndone: true}})
	if err == nil || !strings.Contains(err.Error(), "check still fails: detail a") {
		t.Fatal(err)
	}
}

// tsStatus is a tailnet that already has a node named flocc-a (testEnv's VM name); tsEmpty has
// only this desktop.
const (
	tsStatus = `{"Self":{"HostName":"desk","DNSName":"desk.tail1.ts.net."},
 "Peer":{"k1":{"HostName":"other","DNSName":"Flocc-A.tail1.ts.net."},"k2":{"HostName":"flocc-b","DNSName":"box.tail1.ts.net."}}}`
	tsEmpty = `{"Self":{"HostName":"desk","DNSName":"desk.tail1.ts.net."},"Peer":{}}`
)

func TestTailnetHasMatchesDNSLabelOrHostName(t *testing.T) {
	e, _ := testEnv(host.NewFake())
	e.Local = host.NewFake().On("tailscale status --json", 0, tsStatus)
	for name, want := range map[string]bool{"flocc-a": true, "flocc-b": true, "desk": true, "flocc-c": false} {
		if got, err := tailnetHas(context.Background(), e, name); err != nil || got != want {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
}

func TestTailnetHasExceptSkipsOnlyThatNodeKey(t *testing.T) {
	e, _ := testEnv(host.NewFake())
	e.Local = host.NewFake().On("tailscale status --json", 0, tsStatus)
	for except, want := range map[string]bool{"k1": false, "k2": true, "": true} {
		if got, err := tailnetHasExcept(context.Background(), e, "flocc-a", except); err != nil || got != want {
			t.Errorf("except %q: %v %v", except, got, err)
		}
	}
}

func TestTailnetHasFailsWhenThisDesktopIsOffTheTailnet(t *testing.T) {
	e, _ := testEnv(host.NewFake())
	e.Local = host.NewFake().On("tailscale status --json", 1, "")
	if _, err := tailnetHas(context.Background(), e, "x"); err == nil || !strings.Contains(err.Error(), "tailnet") {
		t.Fatal(err)
	}
}
