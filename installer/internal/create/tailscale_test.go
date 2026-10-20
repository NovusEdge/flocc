package create

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const (
	tsInVM    = "incus exec flocc-a -- tailscale status --json"
	prefsInVM = "incus exec flocc-a -- tailscale debug prefs"
	hasTS     = "incus exec flocc-a -- sh -c command -v tailscale && systemctl is-active --quiet tailscaled"
	installTS = "incus exec flocc-a -- sh -c dpkg --configure -a && curl -fsSL https://tailscale.com/install.sh | sh && systemctl enable --now tailscaled"
	upQR      = "incus exec flocc-a -- tailscale up --ssh --hostname=flocc-a --qr"
	upKey     = "incus exec flocc-a -- tailscale up --ssh --hostname=flocc-a --auth-key=file:/dev/stdin"
)

func joined(dns string) *host.Fake {
	return host.NewFake().
		On(tsInVM, 0, `{"BackendState":"Running","Self":{"DNSName":"`+dns+`"}}`).
		On(prefsInVM, 0, `{"RunSSH":true}`)
}

func TestTailnetJoinedIsDone(t *testing.T) {
	e, _ := testEnv(joined("flocc-a.tail1.ts.net."))
	ok, detail, _ := Tailnet{}.Check(context.Background(), e)
	if !ok || detail != "joined as flocc-a.tail1.ts.net, SSH on" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestTailnetCheckRejectsARenamedNode(t *testing.T) {
	e, _ := testEnv(joined("flocc-a-1.tail1.ts.net."))
	if ok, detail, _ := (Tailnet{}).Check(context.Background(), e); ok || detail != "joined as flocc-a-1, not flocc-a" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestTailnetJoinsByQRWhenNoKeyIsGiven(t *testing.T) {
	f := host.NewFake().
		On(hasTS, 1, "").
		On(installTS, 0, "").
		On(tsInVM, 0, `{"BackendState":"NeedsLogin"}`)
	e, out := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran(installTS) || !f.Ran("interactive: "+upQR) {
		t.Fatalf("%v", f.Calls)
	}
	if !strings.Contains(out.String(), "Disable key expiry") {
		t.Fatal(out.String())
	}
}

// An install cut off mid-dpkg leaves the tailscale binary without a running tailscaled.
func TestTailnetInstallsAgainWhenTailscaledIsNotRunning(t *testing.T) {
	f := host.NewFake().On("incus exec flocc-a -- sh -c command -v tailscale", 0, "/usr/bin/tailscale").
		On(installTS, 0, "").On(tsInVM, 0, `{"BackendState":"NeedsLogin"}`)
	e, _ := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran(installTS) {
		t.Fatalf("%v", f.Calls)
	}
}

func TestTailnetStopsOnANameCollisionBeforeAsking(t *testing.T) {
	f := host.NewFake().On(hasTS, 0, "")
	e, _ := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsStatus)
	err := Tailnet{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "already a node on your tailnet") {
		t.Fatal(err)
	}
	if e.UI.(*fakeUI).asked != 0 {
		t.Fatal("asked for a key")
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "tailscale up") {
			t.Fatalf("joined: %s", c)
		}
	}
}

const (
	tsSet     = "incus exec flocc-a -- tailscale set --ssh --hostname=flocc-a"
	runningAs = `{"BackendState":"Running","Self":{"DNSName":"%s.tail1.ts.net."}}`
)

func TestTailnetFixesARunningNodeWithoutAskingAgain(t *testing.T) {
	f := host.NewFake().On(hasTS, 0, "").On(prefsInVM, 0, `{"RunSSH":false}`).On(tsSet, 0, "").
		OnSeq(tsInVM, host.Result{Stdout: fmt.Sprintf(runningAs, "flocc-a-1")}, host.Result{Stdout: fmt.Sprintf(runningAs, "flocc-a")})
	e, _ := testEnv(f)
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran(tsSet) || e.UI.(*fakeUI).asked != 0 {
		t.Fatalf("%v", f.Calls)
	}
}

// The first read is Apply's state check; set's rename shows up two reads after it.
func TestTailnetWaitsForTheControlServerToConfirmTheRename(t *testing.T) {
	old, renamed := host.Result{Stdout: fmt.Sprintf(runningAs, "flocc-a-1")}, host.Result{Stdout: fmt.Sprintf(runningAs, "flocc-a")}
	f := host.NewFake().On(hasTS, 0, "").On(prefsInVM, 0, `{"RunSSH":true}`).On(tsSet, 0, "").
		OnSeq(tsInVM, old, old, old, renamed)
	e, _ := testEnv(f)
	slept := 0
	e.Sleep = func(time.Duration) { slept++ }
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if slept != 2 {
		t.Fatalf("slept %d times", slept)
	}
}

func TestTailnetRenamedNodeThatStaysRenamedPointsAtTheStaleNode(t *testing.T) {
	f := joined("flocc-a-1.tail1.ts.net.").On(hasTS, 0, "").On(tsSet, 0, "")
	e, _ := testEnv(f)
	var slept time.Duration
	e.Sleep = func(d time.Duration) { slept += d }
	err := Tailnet{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "already a node on your tailnet") {
		t.Fatal(err)
	}
	if slept != 15*time.Second {
		t.Fatalf("gave up after %v", slept)
	}
}

func TestTailnetStopsWaitingForTheRenameWhenCancelled(t *testing.T) {
	f := joined("flocc-a-1.tail1.ts.net.").On(hasTS, 0, "").On(tsSet, 0, "")
	e, _ := testEnv(f)
	ctx, cancel := context.WithCancel(context.Background())
	e.Sleep = func(time.Duration) { cancel() }
	if err := (Tailnet{}).Apply(ctx, e); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAuthKeyGoesOnStdin(t *testing.T) {
	f := host.NewFake().On(hasTS, 0, "").On(upKey, 0, "")
	e, _ := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	e.UI = &fakeUI{key: "  tskey-auth-k1-SECRET\n"}
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if got := f.Stdins[upKey]; got != "tskey-auth-k1-SECRET" {
		t.Fatalf("stdin %q (%v)", got, f.Calls)
	}
	for _, argv := range f.Argv {
		if strings.Contains(strings.Join(argv, " "), "SECRET") {
			t.Fatalf("key in argv: %q", argv)
		}
	}
}

// The key must reach the VM's tailscale and nothing else: not ssh's argv, not the log create
// writes, not the terminal. This runs the real host.Exec against a fake ssh.
func TestAuthKeyNeverReachesArgvOrTheLog(t *testing.T) {
	bin := t.TempDir()
	got := filepath.Join(bin, "stdin")
	script := "#!/bin/sh\ncat > " + got + "\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	const key = "tskey-auth-k1-SECRET"
	var logged bytes.Buffer
	log := NewRedactLog(&logged)
	e, out := testEnv(host.NewFake())
	e.Log = log
	e.M = Remote{R: host.Exec{Log: log}, Target: "laptop"}
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	e.UI = &fakeUI{key: key}
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got); string(b) != key {
		t.Fatalf("the VM read %q", b)
	}
	if strings.Contains(logged.String(), key) || strings.Contains(out.String(), key) {
		t.Fatalf("key leaked:\n%s\n%s", logged.String(), out.String())
	}
	if !strings.Contains(logged.String(), "'--auth-key=file:/dev/stdin'") {
		t.Fatal(logged.String())
	}
}

// A bad key makes tailscale repeat it on stderr. host.Exec writes that to the log before any error
// exists, so the key must already be registered with the redactor.
func TestAuthKeyEchoedByTheVMStaysOutOfTheLogAndTheError(t *testing.T) {
	bin := t.TempDir()
	// Only the command that was given a key fails; the install and status probes before it pass.
	script := "#!/bin/sh\nk=$(cat)\nif [ -n \"$k\" ]; then echo \"invalid key: $k\" >&2; exit 1; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	const key = "tskey-auth-k1-SECRET"
	var logged bytes.Buffer
	log := NewRedactLog(&logged)
	e, out := testEnv(host.NewFake())
	e.Log = log
	e.M = Remote{R: host.Exec{Log: log}, Target: "laptop"}
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	e.UI = &fakeUI{key: key}
	err := Tailnet{}.Apply(context.Background(), e)
	if err == nil {
		t.Fatal("a failing tailscale up passed")
	}
	for name, text := range map[string]string{"error": err.Error(), "log": logged.String(), "terminal": out.String()} {
		if strings.Contains(text, key) {
			t.Errorf("key in the %s:\n%s", name, text)
		}
	}
	if !strings.Contains(logged.String(), "invalid key: <token>") {
		t.Fatalf("the VM's stderr is not in the log:\n%s", logged.String())
	}
}

func TestAuthKeyIsScrubbedFromErrors(t *testing.T) {
	const key = "tskey-auth-k1-SECRET"
	f := host.NewFake().On(hasTS, 0, "").On(upKey, 1, "backend error: invalid key: "+key)
	e, _ := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	e.UI = &fakeUI{key: key}
	err := Tailnet{}.Apply(context.Background(), e)
	if err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "<auth key>") {
		t.Fatal(err)
	}
}
