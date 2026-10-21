package steps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

func userEnv(t *testing.T, f *host.Fake) *Env {
	e := testEnv(t, f)
	e.Home = "/home/cardinal"
	e.UID = 1002
	e.Stage = t.TempDir()
	return e
}

const userctlPrefix = "systemctl --user "

var hosts2 = []CardinalHostFile{{Name: "flocc-nimble", Addr: "10.0.0.5", KnownHost: "10.0.0.5 ssh-ed25519 AAAAhost"}}

func TestHostFilesHoldOnlySSHAndDropStaleOnes(t *testing.T) {
	e := userEnv(t, host.NewFake())
	dir := e.HomePath(cardinalHosts)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "old.toml"), []byte("ssh = 'root@x'\n"), 0o600)
	s := HostFiles{Hosts: hosts2}
	if err := s.Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if done, detail, _ := s.Check(context.Background(), e); !done {
		t.Fatal(detail)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "flocc-nimble.toml"))
	if string(b) != "ssh = \"root@10.0.0.5\"\n" {
		t.Fatalf("host file %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.toml")); !os.IsNotExist(err) {
		t.Fatal("stale host file kept")
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %o", st.Mode().Perm())
	}
}

func TestBriefOverwritesALocalEdit(t *testing.T) {
	e := userEnv(t, host.NewFake())
	b := Brief{User: "cardinal", Machine: "m"}
	os.MkdirAll(e.HomePath(""), 0o755)
	os.WriteFile(e.HomePath("CLAUDE.md"), []byte("edited"), 0o644)
	if done, _, _ := b.Check(context.Background(), e); done {
		t.Fatal("edited brief passed")
	}
	b.Apply(context.Background(), e)
	if done, _, _ := b.Check(context.Background(), e); !done {
		t.Fatal("brief not rewritten")
	}
}

func TestSSHKeyIsMadeOnceWithTheInstallComment(t *testing.T) {
	f := host.NewFake().On("ssh-keygen -q -t ed25519 -N  -C cardinal@zer0-day-nimble -f /home/cardinal/.ssh/id_ed25519", 0, "")
	e := userEnv(t, f)
	k := SSHKey{User: "cardinal", Machine: "zer0-day-nimble"}
	if err := k.Apply(context.Background(), e); err != nil {
		t.Fatalf("%v; %v", err, f.Calls)
	}
	os.WriteFile(e.HomePath(cardinalKey), []byte("priv"), 0o600)
	if err := k.Apply(context.Background(), e); err == nil {
		t.Fatal("a key without its .pub was replaced")
	}
}

func TestKnownHostsReplacesOnlyTheHostsLines(t *testing.T) {
	e := userEnv(t, host.NewFake())
	os.MkdirAll(e.HomePath(".ssh"), 0o700)
	os.WriteFile(e.HomePath(".ssh/known_hosts"), []byte("github.com ssh-ed25519 AAAAgh\n10.0.0.5 ssh-ed25519 AAAAold\n10.0.0.50 ssh-ed25519 AAAAother\n"), 0o644)
	k := KnownHosts{Hosts: hosts2}
	if done, _, _ := k.Check(context.Background(), e); done {
		t.Fatal("old key passed")
	}
	k.Apply(context.Background(), e)
	b, _ := os.ReadFile(e.HomePath(".ssh/known_hosts"))
	want := "github.com ssh-ed25519 AAAAgh\n10.0.0.50 ssh-ed25519 AAAAother\n10.0.0.5 ssh-ed25519 AAAAhost\n"
	if string(b) != want {
		t.Fatalf("known_hosts:\n%s", b)
	}
	if done, _, _ := k.Check(context.Background(), e); !done {
		t.Fatal("rerun not clean")
	}
}

func TestPrestartAddsOnlyWhatIsMissing(t *testing.T) {
	e := userEnv(t, host.NewFake())
	os.MkdirAll(e.HomePath(""), 0o755)
	os.WriteFile(e.HomePath(".claude.json"), []byte(`{"oauthAccount":{"n":12345678901234567890},"mcpServers":{"notion":{"type":"http"}}}`), 0o600)
	if err := CardinalPrestart(e); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(e.HomePath(".claude.json"))
	var m struct {
		Onboarding bool                                  `json:"hasCompletedOnboarding"`
		Projects   map[string]map[string]bool            `json:"projects"`
		Servers    map[string]map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if !m.Onboarding || !m.Projects["/home/cardinal"]["hasTrustDialogAccepted"] ||
		string(m.Servers["linear"]["url"]) != `"https://mcp.linear.app/mcp"` || m.Servers["notion"] == nil ||
		!strings.Contains(string(b), "12345678901234567890") {
		t.Fatalf("claude.json:\n%s", b)
	}
	st, _ := os.Stat(e.HomePath(".claude.json"))
	mtime := st.ModTime()
	if err := CardinalPrestart(e); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(e.HomePath(".claude.json")); !st.ModTime().Equal(mtime) {
		t.Fatal("a complete file was rewritten")
	}
}

func TestClaudeStateOnlyReadsWhileCardinalRuns(t *testing.T) {
	f := host.NewFake().On(userctlPrefix+"is-active --quiet cardinal.service", 0, "")
	e := userEnv(t, f)
	done, detail, _ := ClaudeState{}.Check(context.Background(), e)
	if !done || !strings.Contains(detail, "applies at the next start") {
		t.Fatalf("done=%v %q", done, detail)
	}
	if _, err := os.Stat(e.HomePath(".claude.json")); !os.IsNotExist(err) {
		t.Fatal("check wrote the file")
	}
}

func TestHostsReachableFailsWhenTheTailnetConnects(t *testing.T) {
	old := dialTailnet
	defer func() { dialTailnet = old }()
	dialTailnet = func(a string) error {
		if a == "100.96.135.49" {
			return nil
		}
		return errors.New("timeout")
	}
	e := userEnv(t, host.NewFake())
	h := HostsReachable{Tailnet: []string{"fd7a::1", "100.96.135.49"}}
	if done, detail, _ := h.Check(context.Background(), e); done || !strings.Contains(detail, "100.96.135.49") {
		t.Fatalf("done=%v %q", done, detail)
	}
	if err := h.Apply(context.Background(), e); err == nil || errors.Is(err, ErrPending) {
		t.Fatalf("err = %v", err)
	}
}

func TestHostsReachableIsPendingUntilTheHostTakesTheKey(t *testing.T) {
	old := dialTailnet
	defer func() { dialTailnet = old }()
	dialTailnet = func(string) error { return errors.New("timeout") }
	e := userEnv(t, host.NewFake())
	if err := (HostsReachable{Hosts: hosts2}).Apply(context.Background(), e); !errors.Is(err, ErrPending) {
		t.Fatalf("err = %v", err)
	}
}

func unitsFake() *host.Fake {
	return host.NewFake().
		On(userctlPrefix+"daemon-reload", 0, "").
		On(userctlPrefix+"enable cardinal.service", 0, "").
		On(userctlPrefix+"enable --now cardinal-renew.timer", 0, "").
		On(userctlPrefix+"reset-failed cardinal.service", 0, "").
		On(userctlPrefix+"start cardinal.service", 0, "").
		On("/home/cardinal/.local/bin/claude auth status", 0, `{"loggedIn":true,"authMethod":"claude.ai"}`)
}

func TestUnitsStartAfterResetFailedOnceTheLoginIsFull(t *testing.T) {
	f := unitsFake()
	e := userEnv(t, f)
	if err := (Units{User: "cardinal", RenewHour: 4}).Apply(context.Background(), e); err != nil {
		t.Fatalf("%v; %v", err, f.Calls)
	}
	reset, start := index(f.Calls, userctlPrefix+"reset-failed cardinal.service"), index(f.Calls, userctlPrefix+"start cardinal.service")
	if reset < 0 || start < reset {
		t.Fatalf("calls %v", f.Calls)
	}
	b, _ := os.ReadFile(e.HomePath(".config/systemd/user/cardinal-renew.timer"))
	if !strings.Contains(string(b), "04,05:00/15") {
		t.Fatalf("timer %s", b)
	}
}

func TestUnitsNeverRestartARunningCardinal(t *testing.T) {
	f := unitsFake().On(userctlPrefix+"is-active --quiet cardinal.service", 0, "")
	e := userEnv(t, f)
	var notes []string
	if err := (Units{User: "cardinal", RenewHour: 4, Notes: &notes}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "start cardinal.service") || strings.Contains(c, "restart") {
			t.Fatalf("ran %q", c)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "next renewal") {
		t.Fatalf("notes %v", notes)
	}
}

func TestUnitsStopTheHandSetup(t *testing.T) {
	f := unitsFake().
		OnSeq(userctlPrefix+"is-active --quiet "+handUnit, host.Result{Code: 0}, host.Result{Code: 3}).
		On(userctlPrefix+"stop "+handUnit, 0, "").
		On("tmux kill-session -t =cardinal", 0, "")
	e := userEnv(t, f)
	var notes []string
	if err := (Units{User: "cardinal", RenewHour: 4, Notes: &notes}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran(userctlPrefix+"stop "+handUnit) || !f.Ran("tmux kill-session -t =cardinal") || index(f.Calls, "tmux kill-session -t =cardinal") > index(f.Calls, userctlPrefix+"start cardinal.service") {
		t.Fatalf("calls %v", f.Calls)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "hand setup") {
		t.Fatalf("notes %v", notes)
	}
}
