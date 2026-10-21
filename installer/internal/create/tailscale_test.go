package create

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/tailnet"
)

const (
	tsInVM    = "incus exec flocc-a -- tailscale status --json"
	prefsInVM = "incus exec flocc-a -- tailscale debug prefs"
	hasTS     = "incus exec flocc-a -- sh -c command -v tailscale && systemctl is-active --quiet tailscaled"
	installTS = "incus exec flocc-a -- sh -c dpkg --configure -a && curl -fsSL https://tailscale.com/install.sh | sh && systemctl enable --now tailscaled"
	upQR      = "incus exec flocc-a -- tailscale up --ssh --hostname=flocc-a --advertise-tags=tag:flocc-host --qr"
	upKey     = "incus exec flocc-a -- tailscale up --ssh --hostname=flocc-a --advertise-tags=tag:flocc-host --auth-key=file:/dev/stdin"
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
	e, u := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran(installTS) || !f.Ran("interactive: "+upQR) {
		t.Fatalf("%v", f.Calls)
	}
	e.UI.Close()
	if len(u.Todos) != 1 || !strings.Contains(u.Todos[0], "Disable key expiry") {
		t.Fatal(u.Todos)
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
	dockerUntouched(t, f.Calls)
}

func TestTailnetStopsOnANameCollisionBeforeAsking(t *testing.T) {
	f := host.NewFake().On(hasTS, 0, "").On(tsInVM, 0, `{"BackendState":"NeedsLogin","Self":{"PublicKey":"nodekey:bb"}}`)
	e, u := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsStatus)
	err := Tailnet{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "already a node on your tailnet") {
		t.Fatal(err)
	}
	if len(u.Asked) != 0 {
		t.Fatal("asked for a key")
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "tailscale up") {
			t.Fatalf("joined: %s", c)
		}
	}
}

// tsWithVM is this desktop's view once the VM has joined: the VM, node key nodekey:aa, is a peer,
// offline while its tailscaled is down or logged out.
const tsWithVM = `{"Self":{"HostName":"desk","DNSName":"desk.tail1.ts.net."},
 "Peer":{"nodekey:aa":{"HostName":"flocc-a","DNSName":"flocc-a.tail1.ts.net."}}}`

func TestTailnetRejoinsAStoppedVMAsItself(t *testing.T) {
	up := "interactive: incus exec flocc-a -- tailscale up --ssh --hostname=flocc-a --advertise-tags=tag:flocc-host"
	f := host.NewFake().On(hasTS, 0, "").On(tsInVM, 0, `{"BackendState":"Stopped","Self":{"PublicKey":"nodekey:aa"}}`)
	e, u := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsWithVM)
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran(up) || len(u.Asked) != 0 {
		t.Fatalf("asked=%d %v", len(u.Asked), f.Calls)
	}
}

func TestTailnetLogsAnExpiredVMInAgainAsItself(t *testing.T) {
	f := host.NewFake().On(hasTS, 0, "").On(tsInVM, 0, `{"BackendState":"NeedsLogin","Self":{"PublicKey":"nodekey:aa"}}`)
	e, u := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsWithVM)
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("interactive: "+upQR) || len(u.Asked) != 1 {
		t.Fatalf("asked=%d %v", len(u.Asked), f.Calls)
	}
}

// Right after a boot tailscaled does not answer, then reports NoState, before it knows it is logged out.
func TestTailnetWaitsForTailscaledToSettle(t *testing.T) {
	f := host.NewFake().On(hasTS, 0, "").
		OnSeq(tsInVM, host.Result{Code: 1}, host.Result{Stdout: `{"BackendState":"NoState"}`}, host.Result{Stdout: `{"BackendState":"NeedsLogin"}`})
	e, _ := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	slept := 0
	e.Sleep = func(time.Duration) { slept++ }
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if slept != 2 || !f.Ran("interactive: "+upQR) {
		t.Fatalf("slept %d %v", slept, f.Calls)
	}
}

func TestTailnetStopsWhenTailscaledNeverAnswers(t *testing.T) {
	f := host.NewFake().On(hasTS, 0, "")
	e, _ := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsWithVM)
	var slept time.Duration
	e.Sleep = func(d time.Duration) { slept += d }
	err := Tailnet{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "does not answer") || slept != 15*time.Second {
		t.Fatalf("%v after %v", err, slept)
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
	e, u := testEnv(f)
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran(tsSet) || len(u.Asked) != 0 {
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
	f := host.NewFake().On(hasTS, 0, "").On(tsInVM, 0, `{"BackendState":"NeedsLogin"}`).On(upKey, 0, "")
	e, u := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	u.key = "  tskey-auth-k1-SECRET\n"
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

// Fetching a key can outlast ControlPersist; the captured up would then open a BatchMode
// connection that waits unseen on a check-mode URL.
func TestAuthKeyPromptIsFollowedByAReconnect(t *testing.T) {
	f := host.NewFake().On(hasTS, 0, "").On(tsInVM, 0, `{"BackendState":"NeedsLogin"}`).On(upKey, 0, "").
		On("tailscale status --json", 0, tsEmpty)
	e, u := testEnv(f)
	e.Local = f
	asked := -1
	u.key, u.onAsk = "tskey-auth-k1-SECRET", func() { asked = len(f.Calls) }
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	warm := slices.IndexFunc(f.Calls, func(c string) bool {
		return strings.HasPrefix(c, "interactive: ssh ") && strings.HasSuffix(c, " laptop true")
	})
	if asked < 0 || warm < asked || slices.Index(f.Calls, upKey) < warm {
		t.Fatalf("asked at %d: %v", asked, f.Calls)
	}
}

// needsLogin makes a fake ssh answer the VM's tailscale status as a node that is not logged in.
const needsLogin = `case "$*" in *status*) echo '{"BackendState":"NeedsLogin"}'; exit 0;; esac` + "\n"

// The key must reach the VM's tailscale and nothing else: not ssh's argv, not the log create
// writes, not the terminal. This runs the real host.Exec against a fake ssh.
func TestAuthKeyNeverReachesArgvOrTheLog(t *testing.T) {
	bin := t.TempDir()
	got := filepath.Join(bin, "stdin")
	script := "#!/bin/sh\n" + needsLogin + "cat >> " + got + "\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	const key = "tskey-auth-k1-SECRET"
	var logged bytes.Buffer
	log := NewRedactLog(&logged)
	e, u := testEnv(host.NewFake())
	e.Log = log
	e.M = Remote{R: host.Exec{Log: log}, Target: "laptop"}
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	u.key = key
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got); string(b) != key {
		t.Fatalf("the VM read %q", b)
	}
	if shown := fmt.Sprintf("%+v", u.Events); strings.Contains(logged.String(), key) || strings.Contains(shown, key) {
		t.Fatalf("key leaked:\n%s\n%s", logged.String(), shown)
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
	script := "#!/bin/sh\n" + needsLogin + "k=$(cat)\nif [ -n \"$k\" ]; then echo \"invalid key: $k\" >&2; exit 1; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	const key = "tskey-auth-k1-SECRET"
	var logged bytes.Buffer
	log := NewRedactLog(&logged)
	e, u := testEnv(host.NewFake())
	e.Log = log
	e.M = Remote{R: host.Exec{Log: log}, Target: "laptop"}
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	u.key = key
	err := Tailnet{}.Apply(context.Background(), e)
	if err == nil {
		t.Fatal("a failing tailscale up passed")
	}
	for name, text := range map[string]string{"error": err.Error(), "log": logged.String(), "events": fmt.Sprintf("%+v", u.Events)} {
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
	f := host.NewFake().On(hasTS, 0, "").On(tsInVM, 0, `{"BackendState":"NeedsLogin"}`).On(upKey, 1, "backend error: invalid key: "+key)
	e, u := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	u.key = key
	err := Tailnet{}.Apply(context.Background(), e)
	if err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "<auth key>") {
		t.Fatal(err)
	}
}

// minter is the OAuth client's key minting, with the arguments it was called with.
type minter struct {
	key       string
	err       error
	tag       string
	ttl       time.Duration
	callCount int
}

func (m *minter) TaggedKey(_ context.Context, tag string, ttl time.Duration) (string, error) {
	m.callCount++
	m.tag, m.ttl = tag, ttl
	return m.key, m.err
}

const mintedKey = "tskey-auth-k1-MINTED"

func TestMintedKeyJoinsTaggedOnStdinWithoutAskingOrAnExpiryTodo(t *testing.T) {
	f := host.NewFake().On(hasTS, 0, "").On(tsInVM, 0, `{"BackendState":"NeedsLogin"}`).On(upKey, 0, "")
	e, u := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	m := &minter{key: mintedKey}
	e.Tailnet = m
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if m.tag != "tag:flocc-host" || m.ttl != 10*time.Minute || m.callCount != 1 {
		t.Errorf("minted %+v", m)
	}
	if got := f.Stdins[upKey]; got != mintedKey {
		t.Errorf("stdin %q (%v)", got, f.Calls)
	}
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "interactive: ") && strings.Contains(c, "tailscale up") {
			t.Errorf("a minted key needs no terminal: %s", c)
		}
		if strings.Contains(c, mintedKey) {
			t.Errorf("key in argv: %s", c)
		}
	}
	e.UI.Close()
	if len(u.Asked) != 0 || len(u.Todos) != 0 {
		t.Errorf("asked %d, todos %q", len(u.Asked), u.Todos)
	}
}

func TestMintedKeyIsRedactedFromTheLogBeforeItIsUsed(t *testing.T) {
	f := host.NewFake().On(hasTS, 0, "").On(tsInVM, 0, `{"BackendState":"NeedsLogin"}`).On(upKey, 0, "")
	e, _ := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	var logged bytes.Buffer
	e.Log = NewRedactLog(&logged)
	e.Tailnet = &minter{key: mintedKey}
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	e.Log.Write([]byte("tailscale printed " + mintedKey + "\n"))
	if got := logged.String(); strings.Contains(got, mintedKey) || !strings.Contains(got, "<token>") {
		t.Errorf("log %q", got)
	}
}

func TestMintedKeyNeverReachesTheLogOrEventsThroughARealExec(t *testing.T) {
	bin := t.TempDir()
	got := filepath.Join(bin, "stdin")
	script := "#!/bin/sh\n" + needsLogin + "cat >> " + got + "\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	var logged bytes.Buffer
	log := NewRedactLog(&logged)
	e, u := testEnv(host.NewFake())
	e.Log = log
	e.M = Remote{R: host.Exec{Log: log}, Target: "laptop"}
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	e.Tailnet = &minter{key: mintedKey}
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got); string(b) != mintedKey {
		t.Fatalf("the VM read %q", b)
	}
	if shown := fmt.Sprintf("%+v", u.Events); strings.Contains(logged.String(), mintedKey) || strings.Contains(shown, mintedKey) {
		t.Fatalf("key leaked:\n%s\n%s", logged.String(), shown)
	}
	if !strings.Contains(logged.String(), "--advertise-tags=tag:flocc-host") {
		t.Fatal(logged.String())
	}
}

func TestMintedKeyRefusedForTheTagNamesTheSetupSection(t *testing.T) {
	f := host.NewFake().On(hasTS, 0, "").On(tsInVM, 0, `{"BackendState":"NeedsLogin"}`)
	e, _ := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	e.Tailnet = &minter{err: tailnet.ErrTagNotPermitted}
	err := Tailnet{}.Apply(context.Background(), e)
	if err == nil || err.Error() != "the tailnet policy lacks tag:flocc-host; run flocc setup (Tailnet section)" {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "tailscale up") {
			t.Fatalf("joined: %s", c)
		}
	}
}

func TestUpRefusingTheTagNamesTheSetupSection(t *testing.T) {
	for name, withClient := range map[string]bool{"minted": true, "pasted": false} {
		f := host.NewFake().On(hasTS, 0, "").On(tsInVM, 0, `{"BackendState":"NeedsLogin"}`).
			On(upKey, 1, "requested tags [tag:flocc-host] are invalid or not permitted")
		e, u := testEnv(f)
		e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
		u.key = "tskey-auth-k1-PASTED"
		if withClient {
			e.Tailnet = &minter{key: mintedKey}
		}
		err := Tailnet{}.Apply(context.Background(), e)
		if err == nil || !strings.Contains(err.Error(), "run flocc setup (Tailnet section)") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOtherUpFailuresAreNotReadAsTheTag(t *testing.T) {
	f := host.NewFake().On(hasTS, 0, "").On(tsInVM, 0, `{"BackendState":"NeedsLogin"}`).On(upKey, 1, "backend error: invalid key")
	e, u := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	u.key = "tskey-auth-k1-PASTED"
	if err := (Tailnet{}).Apply(context.Background(), e); err == nil || strings.Contains(err.Error(), "Tailnet section") {
		t.Fatal(err)
	}
}

func TestExpiryTodoGoesWhenThePastedKeyBroughtItUpTagged(t *testing.T) {
	f := host.NewFake().On(hasTS, 0, "").
		OnSeq(tsInVM, host.Result{Stdout: `{"BackendState":"NeedsLogin"}`},
			host.Result{Stdout: `{"BackendState":"Running","Self":{"Tags":["tag:flocc-host"]}}`}).On(upKey, 0, "")
	e, u := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	u.key = "tskey-auth-k1-PASTED"
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	e.UI.Close()
	if len(u.Todos) != 0 {
		t.Errorf("todos %q", u.Todos)
	}
}
