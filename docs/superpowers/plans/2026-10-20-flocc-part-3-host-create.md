# flocc Part 3: host create (0.6.0) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `flocc host create <machine> <name>` makes a flocc host on a machine the owner reaches over SSH: it checks Incus, scopes the machine's firewall to the VM (no broad settings, Docker never restarted), launches a Debian 13 VM, joins it to the tailnet, runs `host add`, `ship` and `login`, and snapshots it; the `just` host recipes go.

**Architecture:** A new Go package `installer/internal/create` holds an ordered list of check-then-apply steps that run on the machine through an SSH runner (`Remote`), plus a small `Installer` interface through which create calls Part 2b's `host add`, `ship` and `login`; the desktop binary's `host` verb dispatches `create` to `create.Main`. This plan is written against the code as it is after Part 1 and Part 2b land: Go module `github.com/NovusEdge/flocc` (today `github.com/NovusEdge/linear-agents`), Python package `src/flocc/` (today `src/linear_agents/`), desktop binary `installer/cmd/flocc`, service user `flocc`, and every other name in the spec's Part 1 Names table.

**Tech Stack:** Go 1.27 (`go test ./...` from the repo root), the existing `host.Runner`/`host.Fake`/`host.Exec`, `charm.land/huh/v2` through `ui.Huh` for the one secret prompt; Python 3.13 with uv for the version and leftover-recipe tests; Incus 6, firewalld 2.x, Docker 29 (iptables backend) on the machine.

**Spec:** `docs/superpowers/specs/2026-10-20-flocc-design.md`, section "Part 3: host create" (and "Part 2b" for the verbs it calls).

## Global Constraints

- Create runs over SSH as the owner's account on `<machine>`, using `sudo` or `incus-admin` group membership (the restricted `incus` group is not enough), not root SSH.
- Each step checks before it changes anything, so re-running create continues a half-made host.
- Docker is never restarted: on 2026-10-20 a restart killed another session's build. No command create runs may start, stop, restart, reload or kill `docker.service`, `docker.socket` or `dockerd`.
- firewalld: `incusbr0` goes in a zone allowing only `dhcp` and `dns`, forwarded only to the default route's interface. Agents in the VM run unreviewed code and must not reach the machine's own services.
- Docker with the iptables backend: two `DOCKER-USER` rules scoped to `incusbr0` (accept from it; accept related/established to it), installed by a oneshot unit with `After=docker.service` and `PartOf=docker.service`. With the nftables backend create stops and says so. A Docker that is installed but stopped (enabled, or socket-activated) gets the unit enabled too, so the rules arrive with its next start; create does not start it.
- VM: `images:debian/13`, `boot.autostart=true`, wait for an IPv4 address with outbound HTTPS. Check `tailscale status` for `<name>` first; an existing node of that name stops create.
- Tailscale join: `tailscale up --ssh --qr`; a one-off auth key may be pasted instead, passed as `--auth-key=file:/dev/stdin`, never stored, never in argv or a log. The log writer redacts the key before `tailscale up` runs, so output that repeats it is redacted too. Print the key-expiry instruction.
- Then `flocc host add`, `flocc ship`, `flocc login`, then Incus snapshot `installed`, replacing an older one. The new snapshot is taken first and swapped in, so a failure keeps the last good one.
- Tailscale SSH check mode can stop a command for a browser re-auth; create shows that URL rather than hang.
- `docs/vm-setup.md` states that the VM joins the tailnet as the owner, untagged, so it can reach every tailnet device and, through the uplink, the LAN; closing that is a separate decision.
- The broad laptop settings of 2026-10-19/20 (`incusbr0` in zone `trusted`, `ip-forward-no-drop`, FORWARD ACCEPT) are reverted without restarting Docker.
- 0.6.0 removes the `just` recipe wrappers of the desktop verbs.
- House rules: comments only for facts the code cannot show; `git commit -s`; no Co-Authored-By line and no AI attribution in commits.
- Steps that touch a real machine (zer0-day-nimble, flocc-nimble) are manual controller steps, never an implementer's.

## Review Focus

1. The laptop's uplink moves to an interface in another firewalld zone (Wi-Fi to Ethernet) after create: re-running create must re-point the forward policy to the new zone, not leave the old one. Pinned in Task 3 (`TestFwFixesFollowTheUplinkZone`).
2. A VM deleted and made again under the same name: the old tailnet node still exists, and create must stop with the admin-console instruction instead of joining as `<name>-1` while ship targets the stale node; a VM that did join as `<name>-1` must not pass the check. Pinned in Tasks 5 and 6 (`TestVMStopsWhenTheTailnetHasTheName`, `TestTailnetCheckRejectsARenamedNode`).
3. The owner's account is in `incus-admin` but has no passwordless sudo, and firewalld or Docker runs: create must stop before reading or changing the firewall, with the sudoers instruction, and never hang on a password prompt; every root command must carry `sudo -n`. Pinned in Tasks 1, 3 and 4 (`TestProbe*`, `TestFirewalldNeedsPasswordlessSudo`, `TestEveryRootCommandIsNonInteractive`).
4. Ctrl-C during the VM wait, then a re-run: the instance exists, may be stopped and may lack `boot.autostart`; create must start it and set autostart without launching again. Pinned in Task 5 (`TestVMResumesAHalfMadeInstance`).
5. A bad auth key: tailscale's output can repeat what it was given, `host.Exec` writes that output to the log before any error exists, and create's error text is printed and logged. The key must be scrubbed from the error and redacted from the log. Pinned in Tasks 1 and 6 (`TestRedactLogHidesSecretsAddedAfterItsFirstWrite`, `TestAuthKeyIsScrubbedFromErrors`, `TestAuthKeyEchoedByTheVMStaysOutOfTheLogAndTheError`).

## Where the spec is silent, this plan decides

- Names on the machine: firewalld zone `flocc-incus`, policies `flocc-incus-out` and `flocc-no-docker`, unit `flocc-docker-user.service`.
- Docker's `docker-forwarding` policy accepts forwarding from every zone into Docker's bridges, so a dhcp-and-dns zone alone still lets the VM reach Docker's published ports. Create adds `flocc-no-docker` (REJECT, ordered before `docker-forwarding`).
- firewalld forwards by zone, not interface: "only to the default route's interface" becomes "only to that interface's zone"; the docs say other interfaces in that zone are reachable too.
- The unit also adds the two rules to ip6tables where Docker made an IPv6 `DOCKER-USER` chain, since Docker's IPv6 forward drop would otherwise stall the VM's IPv6 connections.
- Root commands need passwordless sudo (`sudo -n`); without it create stops with instructions. Incus alone works through `incus-admin`.
- Check-mode URLs: the first SSH connection to the machine runs attached to the terminal and `ControlPersist` keeps it, instead of parsing captured output.
- firewalld changes are written permanently and applied with one `--reload`, the documented way to create zones and policies.
- VM size defaults to 4 vCPUs, 8 GiB and 40 GiB (Part 1's flocc-nimble example), with `--cpus`, `--memory`, `--disk`.
- The snapshot is retaken on every successful run, since ship may have changed the host. It is taken as `installed-new`, the old `installed` is deleted, and `installed-new` is renamed, so a failed snapshot keeps the last good one.
- Docker's backend is read from the `firewall-backend` key of `/etc/docker/daemon.json` (absent means iptables), because `docker info` needs a running daemon and Docker may be installed and stopped.
- The Probe accepts direct `incus` only for an account in `incus-admin`: `incus query /1.0` also succeeds for the restricted `incus` group, through that user's own project, where the advice to create `incusbr0` would be wrong.
- Before `host add`, create waits for this desktop's `tailscale status` to list the new node, since `host add` and `ship` resolve `<name>` right after `tailscale up` returns.
- Docker's zone `docker` counts as present in both firewalld configurations when either has it, so the runtime and permanent checks cannot disagree about wanting `flocc-no-docker`.
- The `just` recipes removed in 0.6.0 are the ones that run `flocc`; the dev, build and Daytona-CLI recipes stay.

## Unconfirmed, checked by the controller in Task 9

The plan relies on these outputs and behaviors, taken from docs or one reading and never checked on the laptop. Task 9 checks each on zer0-day-nimble. Nobody probes them on the owner's desktop: firewall-cmd and systemctl there raise a polkit password dialog.

- `docker info --format '{{json .FirewallBackend}}'` prints `{"Driver":"iptables…",…}` and agrees with the `firewall-backend` key of `/etc/docker/daemon.json` that create reads (Task 9 Step 2).
- Docker re-adds its firewalld zone and its iptables chains after `firewall-cmd --reload` (Step 4).
- `docker network create` leaves `DOCKER-USER` alone; the spec lists this too (Step 4).
- `firewall-cmd --permanent --get-zone-of-interface=incusbr0` answers for the permanent configuration, and `--get-zone-of-interface` for an unbound interface exits non-zero or prints `no zone` (Steps 2 and 3: create's Firewalld step reports `runtime: no zone flocc-incus` before it applies, since `fwProblems` names the missing zone first).
- `incus query /1.0/storage-pools/<pool>/resources` carries `space.used`/`space.total`, and `/1.0/instances/<name>/state` lists the guest's addresses under `network.<iface>.addresses` with `family` and `scope` (Step 3: the Incus and VM steps pass).
- `tailscale up --auth-key=file:/dev/stdin` reads the key through `incus exec` over ssh (Step 3, when a key is pasted rather than the QR code scanned).

---

## File Structure

| File | Responsibility | Task |
|---|---|---|
| `installer/internal/create/create.go` | `Spec`, `Env`, `RedactLog`, command helpers, `Probe`, `Step`, `RunSteps`, tailnet lookup | 1 |
| `installer/internal/create/remote.go` | `Remote` runner: commands on the machine over ssh; `ControlOpts` | 1 |
| `installer/internal/create/create_test.go` | test env, recording runner, Docker guard helper, Task 1 tests | 1 |
| `installer/internal/create/incus.go` (+ `_test`) | Incus readiness step | 2 |
| `installer/internal/create/firewalld.go` (+ `_test`) | firewalld zone and policies step | 3 |
| `installer/internal/create/docker.go` (+ `_test`) | `DOCKER-USER` oneshot unit step | 4 |
| `installer/internal/create/vm.go` (+ `_test`) | VM launch and egress steps | 5 |
| `installer/internal/create/tailscale.go` (+ `_test`) | Tailscale join step | 6 |
| `installer/internal/create/install.go` (+ `_test`) | `Installer`, `Steps`, `Create`, snapshot, `ParseArgs`, `Main` | 7 |
| `installer/internal/cli/create.go` (+ `_test`), `host.go`, `cli.go` (Part 2b's package; names as in Task 7's Consumes) | `hostInstaller` adapter, `host create` → `create.Main`, usage line | 7 |
| `justfile`, `tests/test_just_wrappers_removed.py` | recipe wrappers removed | 8 |
| `docs/vm-setup.md`, `CHANGELOG.md`, `pyproject.toml`, `plugin/.claude-plugin/plugin.json` | docs, release 0.6.0 | 8 |
| `docs/vm-setup.md` (record), `~/.claude/CLAUDE.md`, issue NF-215 | manual run on zer0-day-nimble; instructions that name removed recipes | 9 (controller) |

## Dependencies and parallelism

- Task 1 first.
- Tasks 2, 3, 4, 5 and 6 depend only on Task 1 and touch disjoint files: they may run in parallel, under the Execution note below.
- Task 7 depends on Tasks 1–6.
- Task 8 depends on Task 7 (the docs quote create's messages and flags).
- Task 9 is manual, for the controller, after Task 8 is merged to the branch and built.

## Execution

Tasks 2 to 6 compile as one Go package, so a task's failing-test step (`undefined: Incus`, `undefined: fwState`) breaks `go test` for every sibling in the same checkout, and each task's "verify it fails" and "verify it passes" would give wrong answers. Tasks that run in parallel therefore each get their own git worktree and branch, cut from Task 1's commit, and the controller merges the branches into `flocc-spec` in dependency order (2, 3, 4, 5, 6; their files are disjoint, so the merges do not conflict), then runs `go test ./installer/internal/create/` on the result before starting Task 7. Without worktrees, run Tasks 2 to 6 one at a time.

---

### Task 1: create package core: env, SSH runner, step runner

**Depends on:** Part 2b merged (nothing in this plan).

**Files:**
- Create: `installer/internal/create/create.go`
- Create: `installer/internal/create/remote.go`
- Test: `installer/internal/create/create_test.go`

**Interfaces:**
- Consumes: `host.Runner`, `host.Cmd` with Part 2a's `Stdin io.Reader` (nil means `/dev/null` for `Run`; `host.Exec` hands it to the process and never logs it), `host.Result`, `host.Must`, `host.Fake` (`On`, `OnSeq`, `Calls`, `Argv`, `Ran`, and Part 2a's `Stdins map[string]string`, the stdin each command got, keyed by `String()`), and Part 2a's `host.Redact(w io.Writer, secrets ...string) io.Writer` (2a Task 1, the redacting runner log). It takes its secrets when it is made, so `RedactLog` rebuilds it each time create learns one.
- Produces (package `create`):
  - `const Bridge = "incusbr0"`, `const Image = "images:debian/13"`
  - `type Spec struct { Machine, Name string; CPUs, MemoryGiB, DiskGiB int }`
  - `type Prompter interface { Secret(id, title, help string) (string, error) }`
  - `type RedactLog struct{…}` (an `io.Writer`), `NewRedactLog(dst io.Writer) *RedactLog`, `(*RedactLog).Add(secret string)` (nil-safe)
  - `type Env struct { M, Local host.Runner; UI Prompter; Out io.Writer; Log *RedactLog; Spec Spec; Sleep func(time.Duration); sudo, incusSudo bool }`
  - helpers `cmd(name string, args ...string) host.Cmd`, `(e *Env) root(c host.Cmd) host.Cmd`, `(e *Env) incus(args ...string) host.Cmd`, `(e *Env) ok(ctx, c) bool`, `(e *Env) out(ctx, c) string`, `(e *Env) must(ctx, c) (string, error)`, `(e *Env) needRoot(what string) error`
  - `(e *Env) Probe(ctx) error`
  - `type Step interface { Name() string; Check(ctx, *Env) (bool, string, error); Apply(ctx, *Env) error }`, `RunSteps(ctx, *Env, []Step) error`
  - `tailnetHas(ctx, *Env, name string) (bool, error)`, `tailnetTaken(name string) error`
  - `type Remote struct { R host.Runner; Target string; Opts []string }` implementing `host.Runner`; `ControlOpts() []string`
  - test helpers in `create_test.go`: `testEnv(m *host.Fake) (*Env, *bytes.Buffer)`, `type fakeUI struct{ key string; asked int }`, `type recorder`, `dockerUntouched(t, calls []string)`, `rootCalls(calls []string) []string`

- [ ] **Step 1: Write the failing tests**

`installer/internal/create/create_test.go`:

```go
package create

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

type fakeUI struct {
	key   string
	asked int
}

func (u *fakeUI) Secret(string, string, string) (string, error) {
	u.asked++
	return u.key, nil
}

// testEnv runs machine commands on m directly, without the ssh wrapper, so tests name the
// commands as the machine sees them.
func testEnv(m *host.Fake) (*Env, *bytes.Buffer) {
	var out bytes.Buffer
	return &Env{
		M: m, Local: host.NewFake(), UI: &fakeUI{}, Out: &out, Log: NewRedactLog(io.Discard),
		Spec:  Spec{Machine: "laptop", Name: "flocc-a", CPUs: 4, MemoryGiB: 8, DiskGiB: 40},
		Sleep: func(time.Duration) {}, sudo: true,
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

var touchesDocker = regexp.MustCompile(`systemctl (start|stop|restart|try-restart|reload|reload-or-restart|kill|disable|mask) docker(\.service|\.socket)?( |$)|\bdockerd\b|service docker|pkill|killall`)

// dockerUntouched fails when any command could stop, start or reload dockerd.
func dockerUntouched(t *testing.T, calls []string) {
	t.Helper()
	for _, c := range calls {
		if touchesDocker.MatchString(c) {
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
	if got.Name != "ssh" || !slices.Equal(got.Args[:3], []string{"-o", "BatchMode=yes", "laptop"}) || len(got.Args) != 4 {
		t.Fatalf("%q", got.Args)
	}
	if words := shWords(t, got.Args[3]); !slices.Equal(words, argv) {
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

func TestTailnetHasFailsWhenThisDesktopIsOffTheTailnet(t *testing.T) {
	e, _ := testEnv(host.NewFake())
	e.Local = host.NewFake().On("tailscale status --json", 1, "")
	if _, err := tailnetHas(context.Background(), e, "x"); err == nil || !strings.Contains(err.Error(), "tailnet") {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./installer/internal/create/`
Expected: FAIL, build errors such as `undefined: Env` and `undefined: Remote`.

- [ ] **Step 3: Write `create.go`**

```go
// Package create makes a flocc host: an Incus VM on a machine the owner reaches over SSH, joined
// to the tailnet and installed with the desktop's host verbs.
package create

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const (
	Bridge = "incusbr0"
	Image  = "images:debian/13"
)

type Spec struct {
	Machine   string // ssh destination of the owner's account on the machine
	Name      string // Incus instance, Tailscale hostname and flocc host name
	CPUs      int
	MemoryGiB int
	DiskGiB   int
}

// Prompter is the one question create asks; ui.Huh satisfies it.
type Prompter interface {
	Secret(id, title, help string) (string, error)
}

// RedactLog is the writer behind the run's log. A secret that only becomes known mid-run, such as
// the Tailscale auth key, is registered with Add, and every later write has it replaced by
// <token>: host.Exec logs a command's output before any error exists. It rebuilds Part 2a's
// host.Redact, which takes its secrets when it is made.
type RedactLog struct {
	mu      sync.Mutex
	dst     io.Writer
	secrets []string
	w       io.Writer
}

func NewRedactLog(dst io.Writer) *RedactLog { return &RedactLog{dst: dst, w: dst} }

func (l *RedactLog) Add(secret string) {
	if l == nil || secret == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.secrets = append(l.secrets, secret)
	l.w = host.Redact(l.dst, l.secrets...)
}

func (l *RedactLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

type Env struct {
	M     host.Runner // runs on the machine
	Local host.Runner // runs on this desktop
	UI    Prompter
	Out   io.Writer
	Log   *RedactLog // the log host.Exec writes to; Add registers a secret before it is used
	Spec  Spec
	Sleep func(time.Duration)

	sudo      bool // sudo -n works on the machine
	incusSudo bool // the account is not in incus-admin, so incus goes through sudo
}

func cmd(name string, args ...string) host.Cmd { return host.Cmd{Name: name, Args: args} }

// root runs c as root. Most commands are captured with no terminal, so sudo must never prompt.
func (e *Env) root(c host.Cmd) host.Cmd {
	c.Args = append([]string{"-n", c.Name}, c.Args...)
	c.Name = "sudo"
	return c
}

func (e *Env) incus(args ...string) host.Cmd {
	if e.incusSudo {
		return e.root(cmd("incus", args...))
	}
	return cmd("incus", args...)
}

func (e *Env) ok(ctx context.Context, c host.Cmd) bool {
	r, err := e.M.Run(ctx, c)
	return err == nil && r.Code == 0
}

// out is c's trimmed stdout, or "" when c fails.
func (e *Env) out(ctx context.Context, c host.Cmd) string {
	r, err := e.M.Run(ctx, c)
	if err != nil || r.Code != 0 {
		return ""
	}
	return strings.TrimSpace(r.Stdout)
}

func (e *Env) must(ctx context.Context, c host.Cmd) (string, error) { return host.Must(ctx, e.M, c) }

// Probe finds how to run incus on the machine and whether sudo works there without a password.
func (e *Env) Probe(ctx context.Context) error {
	e.sudo = e.ok(ctx, cmd("sudo", "-n", "true"))
	// incus query also succeeds for the restricted incus group, through that user's own project,
	// so group membership decides, not the query alone.
	admin := slices.Contains(strings.Fields(e.out(ctx, cmd("id", "-nG"))), "incus-admin")
	switch {
	case admin && e.ok(ctx, cmd("incus", "query", "/1.0")):
		e.incusSudo = false
	case e.sudo && e.ok(ctx, e.root(cmd("incus", "query", "/1.0"))):
		e.incusSudo = true
	default:
		return fmt.Errorf("cannot run incus as an administrator on %s: add your account to the incus-admin group (sudo usermod -aG incus-admin \"$USER\", then log in again; the restricted incus group is not enough), or give it passwordless sudo", e.Spec.Machine)
	}
	return nil
}

// needRoot stops a step that reads or changes the machine's firewall, which only root can.
func (e *Env) needRoot(what string) error {
	if e.sudo {
		return nil
	}
	return fmt.Errorf("%s on %s needs root, and sudo there asks for a password, which create cannot type. Give your account passwordless sudo on %s (a file in /etc/sudoers.d/ written with visudo), then run create again", what, e.Spec.Machine, e.Spec.Machine)
}

type Step interface {
	Name() string
	// Check reports whether the step's outcome already holds. It never changes the machine.
	Check(ctx context.Context, e *Env) (done bool, detail string, err error)
	Apply(ctx context.Context, e *Env) error
}

// RunSteps checks each step just before it would run: a later step's check needs the earlier
// steps' outcome, such as the VM existing.
func RunSteps(ctx context.Context, e *Env, steps []Step) error {
	for _, s := range steps {
		done, detail, err := s.Check(ctx, e)
		if err != nil {
			return fmt.Errorf("%s: %w", s.Name(), err)
		}
		if !done {
			fmt.Fprintf(e.Out, "• %-10s %s\n", s.Name(), detail)
			if err := s.Apply(ctx, e); err != nil {
				return fmt.Errorf("%s: %w", s.Name(), err)
			}
			if done, detail, err = s.Check(ctx, e); err != nil {
				return fmt.Errorf("%s: %w", s.Name(), err)
			}
			if !done {
				return fmt.Errorf("%s: applied, but the check still fails: %s", s.Name(), detail)
			}
		}
		fmt.Fprintf(e.Out, "✓ %-10s %s\n", s.Name(), detail)
	}
	return nil
}

type tsNode struct{ HostName, DNSName string }

func (n *tsNode) named(name string) bool {
	if n == nil {
		return false
	}
	label, _, _ := strings.Cut(n.DNSName, ".")
	return strings.EqualFold(label, name) || strings.EqualFold(n.HostName, name)
}

// tailnetHas reports whether a node on the owner's tailnet already uses name, as this desktop
// sees the tailnet.
func tailnetHas(ctx context.Context, e *Env, name string) (bool, error) {
	out, err := host.Must(ctx, e.Local, cmd("tailscale", "status", "--json"))
	if err != nil {
		return false, fmt.Errorf("reading the tailnet on this desktop: %w", err)
	}
	var st struct {
		Self *tsNode
		Peer map[string]*tsNode
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return false, fmt.Errorf("reading the tailnet on this desktop: %w", err)
	}
	if st.Self.named(name) {
		return true, nil
	}
	for _, p := range st.Peer {
		if p.named(name) {
			return true, nil
		}
	}
	return false, nil
}

func tailnetTaken(name string) error {
	return fmt.Errorf("%s is already a node on your tailnet; remove it at https://login.tailscale.com/admin/machines, or pick another name", name)
}
```

- [ ] **Step 4: Write `remote.go`**

```go
package create

import (
	"context"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// Remote runs commands on the machine over ssh. ssh joins its arguments into one line for the
// remote login shell, so each word is quoted on its own.
type Remote struct {
	R      host.Runner
	Target string
	Opts   []string
}

func (r Remote) wrap(c host.Cmd, tty bool) host.Cmd {
	args := append([]string{}, r.Opts...)
	if tty {
		args = append(args, "-t")
	}
	words := []string{quote(c.Name)}
	for _, a := range c.Args {
		words = append(words, quote(a))
	}
	return host.Cmd{Name: "ssh", Args: append(args, r.Target, strings.Join(words, " ")), Stdin: c.Stdin}
}

func (r Remote) Run(ctx context.Context, c host.Cmd) (host.Result, error) {
	return r.R.Run(ctx, r.wrap(c, false))
}

func (r Remote) Interactive(ctx context.Context, c host.Cmd) error {
	return r.R.Interactive(ctx, r.wrap(c, true))
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ControlOpts share one connection per run, so a Tailscale SSH check-mode approval made on the
// first, attached connection covers every captured command after it.
func ControlOpts() []string {
	return []string{"-o", "ControlMaster=auto", "-o", "ControlPath=~/.ssh/flocc-%C", "-o", "ControlPersist=10m"}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./installer/internal/create/ && go vet ./installer/internal/create/ && test -z "$(gofmt -l installer/internal/create)"`
Expected: `ok  	github.com/NovusEdge/flocc/installer/internal/create`, no vet or gofmt output.

- [ ] **Step 6: Commit**

```bash
git add installer/internal/create
git commit -s -m "create: env, ssh runner and step runner for host create"
```

---

### Task 2: Incus readiness step

**Depends on:** Task 1.

**Files:**
- Create: `installer/internal/create/incus.go`
- Test: `installer/internal/create/incus_test.go`

**Interfaces:**
- Consumes: `Env`, `cmd`, `(e *Env).incus/out/ok`, `Bridge` (Task 1).
- Produces: `type Incus struct{}` implementing `Step` (name `"Incus"`); `memAvailable(meminfo string) (uint64, error)`; `gib(b uint64) string`.

- [ ] **Step 1: Write the failing tests**

`installer/internal/create/incus_test.go`:

```go
package create

import (
	"context"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const meminfo16 = "MemTotal:       32000000 kB\nMemFree:         1000000 kB\nMemAvailable:   16777216 kB\n"

func readyIncus() *host.Fake {
	return host.NewFake().
		On("incus query /1.0/networks/incusbr0", 0, `{"managed":true,"type":"bridge"}`).
		On("incus profile device get default root pool", 0, "default\n").
		On("incus query /1.0/storage-pools/default/resources", 0, `{"space":{"used":10737418240,"total":107374182400}}`).
		On("cat /proc/meminfo", 0, meminfo16)
}

func TestIncusReady(t *testing.T) {
	e, _ := testEnv(readyIncus())
	ok, detail, err := Incus{}.Check(context.Background(), e)
	if err != nil || !ok || !strings.Contains(detail, "pool default 90.0 GiB free") {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
}

func TestIncusWithoutTheBridgeStopsWithTheCommands(t *testing.T) {
	f := readyIncus().On("incus query /1.0/networks/incusbr0", 1, "")
	e, _ := testEnv(f)
	if ok, _, _ := (Incus{}).Check(context.Background(), e); ok {
		t.Fatal("ready without a bridge")
	}
	err := Incus{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "incus admin init --minimal") || !strings.Contains(err.Error(), "incus network create incusbr0") {
		t.Fatal(err)
	}
}

func TestIncusRefusesAnUnmanagedBridge(t *testing.T) {
	e, _ := testEnv(readyIncus().On("incus query /1.0/networks/incusbr0", 0, `{"managed":false,"type":"bridge"}`))
	if ok, _, _ := (Incus{}).Check(context.Background(), e); ok {
		t.Fatal("ready with an unmanaged bridge")
	}
}

func TestIncusWithoutARootPool(t *testing.T) {
	e, _ := testEnv(readyIncus().On("incus profile device get default root pool", 1, ""))
	err := Incus{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "incus profile device add default root disk path=/ pool=default") {
		t.Fatal(err)
	}
}

func TestIncusPoolTooSmall(t *testing.T) {
	f := readyIncus().On("incus query /1.0/storage-pools/default/resources", 0, `{"space":{"used":96636764160,"total":107374182400}}`)
	e, _ := testEnv(f)
	ok, detail, _ := Incus{}.Check(context.Background(), e)
	if ok || !strings.Contains(detail, "10.0 GiB free; the VM needs 40 GiB") {
		t.Fatalf("%v %q", ok, detail)
	}
	if err := (Incus{}).Apply(context.Background(), e); err == nil || !strings.Contains(err.Error(), "--disk") {
		t.Fatal(err)
	}
}

func TestIncusTooLittleMemory(t *testing.T) {
	e, _ := testEnv(readyIncus().On("cat /proc/meminfo", 0, "MemAvailable:    4194304 kB\n"))
	ok, detail, _ := Incus{}.Check(context.Background(), e)
	if ok || !strings.Contains(detail, "4.0 GiB of memory available; the VM needs 8 GiB") {
		t.Fatalf("%v %q", ok, detail)
	}
	if err := (Incus{}).Apply(context.Background(), e); err == nil || !strings.Contains(err.Error(), "--memory") {
		t.Fatal(err)
	}
}

func TestIncusSkipsRoomChecksForAnExistingVM(t *testing.T) {
	f := readyIncus().On("incus query /1.0/instances/flocc-a", 0, `{"status":"Running"}`).
		On("cat /proc/meminfo", 0, "MemAvailable:    1024 kB\n")
	e, _ := testEnv(f)
	if ok, _, err := (Incus{}).Check(context.Background(), e); !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	if f.Ran("cat /proc/meminfo") || f.Ran("incus query /1.0/storage-pools/default/resources") {
		t.Fatalf("%v", f.Calls)
	}
}

func TestIncusApplyNeverChangesIncus(t *testing.T) {
	f := readyIncus().On("incus query /1.0/networks/incusbr0", 1, "")
	e, _ := testEnv(f)
	Incus{}.Apply(context.Background(), e)
	for _, c := range f.Calls {
		if !strings.HasPrefix(c, "incus query") && !strings.HasPrefix(c, "incus profile device get") && c != "cat /proc/meminfo" {
			t.Errorf("changes the machine: %s", c)
		}
	}
}

func TestMemAvailable(t *testing.T) {
	if b, err := memAvailable(meminfo16); err != nil || b != 16<<30 {
		t.Fatalf("%d %v", b, err)
	}
	if _, err := memAvailable("MemTotal: 1 kB\n"); err == nil {
		t.Fatal("no MemAvailable line accepted")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./installer/internal/create/ -run 'Incus|MemAvailable'`
Expected: FAIL, `undefined: Incus`.

- [ ] **Step 3: Write `incus.go`**

```go
package create

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Incus struct{}

func (Incus) Name() string { return "Incus" }

type incusReport struct {
	ok          bool
	detail, fix string
}

func inspectIncus(ctx context.Context, e *Env) (incusReport, error) {
	var nw struct {
		Managed bool   `json:"managed"`
		Type    string `json:"type"`
	}
	if json.Unmarshal([]byte(e.out(ctx, e.incus("query", "/1.0/networks/"+Bridge))), &nw) != nil || !nw.Managed || nw.Type != "bridge" {
		return incusReport{detail: "no managed bridge " + Bridge,
			fix: "incus admin init --minimal     (a fresh Incus: it makes " + Bridge + " and a default pool)\n  incus network create " + Bridge + "     (an Incus already set up without it)"}, nil
	}
	if e.ok(ctx, e.incus("query", "/1.0/instances/"+e.Spec.Name)) {
		// Room was checked when the VM was made; a re-run must not refuse the VM for using it.
		return incusReport{ok: true, detail: Bridge + "; " + e.Spec.Name + " exists"}, nil
	}
	pool := e.out(ctx, e.incus("profile", "device", "get", "default", "root", "pool"))
	if pool == "" {
		return incusReport{detail: "the default profile has no root disk",
			fix: "incus storage create default dir\n  incus profile device add default root disk path=/ pool=default"}, nil
	}
	var res struct {
		Space struct{ Used, Total uint64 } `json:"space"`
	}
	if err := json.Unmarshal([]byte(e.out(ctx, e.incus("query", "/1.0/storage-pools/"+pool+"/resources"))), &res); err != nil {
		return incusReport{}, fmt.Errorf("reading storage pool %s: %w", pool, err)
	}
	var free uint64
	if res.Space.Total > res.Space.Used {
		free = res.Space.Total - res.Space.Used
	}
	if free < uint64(e.Spec.DiskGiB)<<30 {
		return incusReport{detail: fmt.Sprintf("pool %s has %s free; the VM needs %d GiB", pool, gib(free), e.Spec.DiskGiB),
			fix: "free space in pool " + pool + ", or run create with a smaller --disk"}, nil
	}
	avail, err := memAvailable(e.out(ctx, cmd("cat", "/proc/meminfo")))
	if err != nil {
		return incusReport{}, err
	}
	if avail < uint64(e.Spec.MemoryGiB)<<30 {
		return incusReport{detail: fmt.Sprintf("%s of memory available; the VM needs %d GiB", gib(avail), e.Spec.MemoryGiB),
			fix: "stop another VM (incus stop <name>), or run create with a smaller --memory"}, nil
	}
	return incusReport{ok: true, detail: fmt.Sprintf("%s; pool %s %s free; %s of memory available", Bridge, pool, gib(free), gib(avail))}, nil
}

func (Incus) Check(ctx context.Context, e *Env) (bool, string, error) {
	r, err := inspectIncus(ctx, e)
	return r.ok, r.detail, err
}

// Apply never sets Incus up itself: the pool and the network are the owner's choice on a
// machine shared with other work.
func (Incus) Apply(ctx context.Context, e *Env) error {
	r, err := inspectIncus(ctx, e)
	if err != nil {
		return err
	}
	return fmt.Errorf("%s. On %s run:\n  %s\nthen run create again", r.detail, e.Spec.Machine, r.fix)
}

func memAvailable(meminfo string) (uint64, error) {
	for _, line := range strings.Split(meminfo, "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == "MemAvailable:" && f[2] == "kB" {
			kb, err := strconv.ParseUint(f[1], 10, 64)
			return kb << 10, err
		}
	}
	return 0, errors.New("/proc/meminfo has no MemAvailable line")
}

func gib(b uint64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./installer/internal/create/ -run 'Incus|MemAvailable' -v`
Expected: every listed test `PASS`.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/create/incus.go installer/internal/create/incus_test.go
git commit -s -m "create: check Incus has the bridge, room and memory for the VM"
```

---

### Task 3: firewalld step

**Depends on:** Task 1.

**Files:**
- Create: `installer/internal/create/firewalld.go`
- Test: `installer/internal/create/firewalld_test.go`

**Interfaces:**
- Consumes: `Env`, `cmd`, `(e *Env).root/out/must/ok/needRoot`, `Bridge`, test helpers `testEnv`, `dockerUntouched`, `rootCalls` (Task 1).
- Produces: `type Firewalld struct{}` implementing `Step` (name `"Firewalld"`); constants `fwZone = "flocc-incus"`, `fwOut = "flocc-incus-out"`, `fwNoDocker = "flocc-no-docker"`; `type fwState`; `readFirewall`, `readBoth`, `fwProblems`, `fwFixes`, `fixPolicy`, `noDockerPriority`, `uplinkZone`, `sameSet`.

Design facts the code relies on, from current docs:

- A new zone or policy is permanent-only (`firewall-cmd --permanent --new-zone`, `--new-policy`) and appears in the runtime configuration after `firewall-cmd --reload`; a reload discards runtime-only changes ([firewall-cmd(1)](https://firewalld.org/documentation/man-pages/firewall-cmd.html)).
- A policy has ingress and egress zones and a target of `CONTINUE` (default), `ACCEPT`, `REJECT` or `DROP` ([firewalld.policy(5)](https://firewalld.org/documentation/man-pages/firewalld.policy.html), [firewalld.policies(5)](https://firewalld.org/documentation/man-pages/firewalld.policies.html), whose example is exactly `--new-policy`, `--add-ingress-zone`, `--add-egress-zone`, `--set-target ACCEPT`, `--reload`).
- Policy priority: `-1` is the default for new policies, a priority below 0 runs before all zone rules, and a lower value has higher precedence ([firewall-cmd(1)](https://firewalld.org/documentation/man-pages/firewall-cmd.html), [firewalld "Zone Priorities"](https://firewalld.org/2023/04/zone-priorities)).
- With firewalld running, Docker puts its bridges in a zone `docker` with target ACCEPT and adds a policy `docker-forwarding` that accepts forwarding from any zone into it ([Docker: Packet filtering and firewalls, "Integration with firewalld"](https://docs.docker.com/engine/network/packet-filtering-firewalls/)). A zone for `incusbr0` with only dhcp and dns therefore does not keep the VM from Docker's published ports, which are DNATed into the `docker` zone: `flocc-no-docker` rejects `flocc-incus` → `docker` ahead of `docker-forwarding`.
- Incus's own firewalld page puts the bridge in `trusted` ([Incus: How to configure your firewall](https://linuxcontainers.org/incus/docs/main/howto/network_bridge_firewalld/)) and warns that this "might inadvertently introduce a security risk"; that is the broad setting this step replaces.

- [ ] **Step 1: Write the failing tests**

`installer/internal/create/firewalld_test.go`:

```go
package create

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// wantFw is a machine already set up for uplink zone up.
func wantFw(up string) fwState {
	return fwState{
		Zones:      []string{"docker", "flocc-incus", "public", "trusted"},
		Policies:   []string{"allow-host-ipv6", "docker-forwarding", "flocc-incus-out", "flocc-no-docker"},
		BridgeZone: "flocc-incus", Services: []string{"dhcp", "dns"},
		OutIn: []string{"flocc-incus"}, OutEg: []string{up}, OutTarget: "ACCEPT",
		NoDockerIn: []string{"flocc-incus"}, NoDockerEg: []string{"docker"}, NoDockerTarget: "REJECT",
		NoDockerPriority: -100, DockerFwdPriority: -1,
	}
}

// laptopFw is the laptop as of 2026-10-20: incusbr0 in trusted, Docker's zone and policy, nothing
// of flocc's.
func laptopFw() fwState {
	return fwState{
		Zones:      []string{"docker", "public", "trusted"},
		Policies:   []string{"allow-host-ipv6", "docker-forwarding"},
		BridgeZone: "trusted", DockerFwdPriority: -1,
	}
}

// onFirewall answers readFirewall's queries from s, as the runtime or the permanent configuration.
func onFirewall(f *host.Fake, permanent bool, s fwState) {
	q := "sudo -n firewall-cmd "
	if permanent {
		q += "--permanent "
	}
	j := func(v []string) string { return strings.Join(v, " ") + "\n" }
	f.On(q+"--get-zones", 0, j(s.Zones))
	f.On(q+"--get-policies", 0, j(s.Policies))
	if s.BridgeZone == "" {
		f.On(q+"--get-zone-of-interface=incusbr0", 2, "no zone\n")
	} else {
		f.On(q+"--get-zone-of-interface=incusbr0", 0, s.BridgeZone+"\n")
	}
	f.On(q+"--zone=flocc-incus --list-services", 0, j(s.Services))
	f.On(q+"--zone=flocc-incus --list-ports", 0, j(s.Ports))
	f.On(q+"--policy=flocc-incus-out --list-ingress-zones", 0, j(s.OutIn))
	f.On(q+"--policy=flocc-incus-out --list-egress-zones", 0, j(s.OutEg))
	f.On(q+"--policy=flocc-incus-out --get-target", 0, s.OutTarget+"\n")
	f.On(q+"--policy=flocc-no-docker --list-ingress-zones", 0, j(s.NoDockerIn))
	f.On(q+"--policy=flocc-no-docker --list-egress-zones", 0, j(s.NoDockerEg))
	f.On(q+"--policy=flocc-no-docker --get-target", 0, s.NoDockerTarget+"\n")
	f.On(q+"--policy=flocc-no-docker --get-priority", 0, strconv.Itoa(s.NoDockerPriority)+"\n")
	f.On(q+"--policy=docker-forwarding --get-priority", 0, strconv.Itoa(s.DockerFwdPriority)+"\n")
}

// fwMachine is a machine with firewalld running and its default route on wlan0, in zone public.
func fwMachine(runtime, permanent fwState) *host.Fake {
	f := host.NewFake().
		On("systemctl is-active --quiet firewalld.service", 0, "").
		On("ip -j route show default", 0, `[{"dst":"default","gateway":"192.168.1.1","dev":"wlan0","protocol":"dhcp"}]`).
		On("sudo -n firewall-cmd --get-zone-of-interface=wlan0", 0, "public\n")
	onFirewall(f, false, runtime)
	onFirewall(f, true, permanent)
	return f
}

func TestFwFixesForTheLaptopAsItIs(t *testing.T) {
	var got []string
	for _, args := range fwFixes(laptopFw(), "public") {
		got = append(got, strings.Join(args, " "))
	}
	want := []string{
		"--permanent --new-zone=flocc-incus",
		"--permanent --zone=flocc-incus --add-service=dhcp",
		"--permanent --zone=flocc-incus --add-service=dns",
		"--permanent --zone=flocc-incus --change-interface=incusbr0",
		"--permanent --new-policy=flocc-incus-out",
		"--permanent --policy=flocc-incus-out --add-ingress-zone=flocc-incus",
		"--permanent --policy=flocc-incus-out --add-egress-zone=public",
		"--permanent --policy=flocc-incus-out --set-target=ACCEPT",
		"--permanent --new-policy=flocc-no-docker",
		"--permanent --policy=flocc-no-docker --add-ingress-zone=flocc-incus",
		"--permanent --policy=flocc-no-docker --add-egress-zone=docker",
		"--permanent --policy=flocc-no-docker --set-target=REJECT",
		"--permanent --policy=flocc-no-docker --set-priority=-100",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}

func TestFwFixesNothingWhenSetUp(t *testing.T) {
	if c := fwFixes(wantFw("public"), "public"); len(c) != 0 {
		t.Fatalf("%q", c)
	}
	if p := fwProblems(wantFw("public"), "public"); len(p) != 0 {
		t.Fatalf("%q", p)
	}
}

func TestFwZoneAllowsOnlyDhcpAndDns(t *testing.T) {
	s := wantFw("public")
	s.Services = []string{"dhcp", "dns", "ssh", "mdns"}
	s.Ports = []string{"8080/tcp"}
	got := fwFixes(s, "public")
	want := [][]string{
		{"--permanent", "--zone=flocc-incus", "--remove-service=ssh"},
		{"--permanent", "--zone=flocc-incus", "--remove-service=mdns"},
		{"--permanent", "--zone=flocc-incus", "--remove-port=8080/tcp"},
	}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("%q", got)
	}
	if p := fwProblems(s, "public"); len(p) != 1 || !strings.Contains(p[0], "not only dhcp and dns") {
		t.Fatalf("%q", p)
	}
}

func TestFwFixesFollowTheUplinkZone(t *testing.T) {
	got := fwFixes(wantFw("home"), "public")
	want := [][]string{
		{"--permanent", "--policy=flocc-incus-out", "--remove-egress-zone=home"},
		{"--permanent", "--policy=flocc-incus-out", "--add-egress-zone=public"},
	}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("%q", got)
	}
}

func TestFwNoDockerRunsBeforeDockerForwarding(t *testing.T) {
	s := wantFw("public")
	s.DockerFwdPriority = -200
	got := fwFixes(s, "public")
	if len(got) != 1 || strings.Join(got[0], " ") != "--permanent --policy=flocc-no-docker --set-priority=-201" {
		t.Fatalf("%q", got)
	}
}

func TestFwWithoutDockerHasNoDockerPolicy(t *testing.T) {
	s := laptopFw()
	s.Zones = []string{"public", "trusted"}
	s.Policies = []string{"allow-host-ipv6"}
	for _, args := range fwFixes(s, "public") {
		if strings.Contains(strings.Join(args, " "), "flocc-no-docker") {
			t.Fatalf("%q", args)
		}
	}
}

func TestFirewalldSkippedWhenNotRunning(t *testing.T) {
	f := host.NewFake().On("systemctl is-active --quiet firewalld.service", 3, "inactive\n")
	e, _ := testEnv(f)
	e.sudo = false
	ok, detail, err := Firewalld{}.Check(context.Background(), e)
	if !ok || err != nil || !strings.Contains(detail, "firewalld not running") {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
	if len(f.Calls) != 1 {
		t.Fatalf("%v", f.Calls)
	}
}

func TestFirewalldNeedsPasswordlessSudo(t *testing.T) {
	e, _ := testEnv(fwMachine(laptopFw(), laptopFw()))
	e.sudo = false
	if _, _, err := (Firewalld{}).Check(context.Background(), e); err == nil || !strings.Contains(err.Error(), "passwordless sudo") {
		t.Fatal(err)
	}
}

func TestFirewalldCheckPassesWithoutWriting(t *testing.T) {
	f := fwMachine(wantFw("public"), wantFw("public"))
	e, _ := testEnv(f)
	ok, detail, err := Firewalld{}.Check(context.Background(), e)
	if !ok || err != nil || !strings.Contains(detail, "forwarded only to zone public") {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "--new-") || strings.Contains(c, "--add-") || strings.Contains(c, "--set-") || strings.Contains(c, "--reload") {
			t.Errorf("writes: %s", c)
		}
	}
}

func TestFirewalldDetailSaysWhenTailscaleSharesTheUplinkZone(t *testing.T) {
	f := fwMachine(wantFw("public"), wantFw("public")).
		On("sudo -n firewall-cmd --get-zone-of-interface=tailscale0", 0, "public\n")
	e, _ := testEnv(f)
	ok, detail, _ := Firewalld{}.Check(context.Background(), e)
	if !ok || !strings.Contains(detail, "tailscale0 is in the same zone") {
		t.Fatalf("%v %q", ok, detail)
	}
}

// Docker's zone is in the runtime configuration only: the permanent check must still want
// flocc-no-docker, or the two checks would disagree and no write could satisfy both.
func TestFirewalldWantsNoDockerWhenOnlyTheRuntimeHasDockersZone(t *testing.T) {
	perm := wantFw("public")
	perm.Zones = []string{"flocc-incus", "public", "trusted"}
	perm.Policies = []string{"allow-host-ipv6", "flocc-incus-out"}
	perm.NoDockerIn, perm.NoDockerEg, perm.NoDockerTarget, perm.NoDockerPriority = nil, nil, "", 0
	e, _ := testEnv(fwMachine(wantFw("public"), perm))
	ok, detail, _ := Firewalld{}.Check(context.Background(), e)
	if ok || !strings.HasPrefix(detail, "permanent: ") || !strings.Contains(detail, "flocc-no-docker") {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestFirewalldCheckCatchesAStaleRuntime(t *testing.T) {
	runtime := wantFw("public")
	runtime.BridgeZone = "trusted"
	e, _ := testEnv(fwMachine(runtime, wantFw("public")))
	ok, detail, _ := Firewalld{}.Check(context.Background(), e)
	if ok || !strings.HasPrefix(detail, "runtime: incusbr0 is in zone \"trusted\"") {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestFirewalldApplyWritesPermanentThenReloadsOnce(t *testing.T) {
	f := fwMachine(laptopFw(), laptopFw()).
		On("sudo -n firewall-cmd --reload", 0, "success\n")
	for _, args := range fwFixes(laptopFw(), "public") {
		f.On("sudo -n firewall-cmd "+strings.Join(args, " "), 0, "success\n")
	}
	e, _ := testEnv(f)
	if err := (Firewalld{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	for _, c := range f.Calls {
		if c == "sudo -n firewall-cmd --reload" {
			reloads++
		}
	}
	if reloads != 1 || f.Calls[len(f.Calls)-1] != "sudo -n firewall-cmd --reload" {
		t.Fatalf("%v", f.Calls)
	}
	if !f.Ran("sudo -n firewall-cmd --permanent --zone=flocc-incus --change-interface=incusbr0") {
		t.Fatalf("%v", f.Calls)
	}
	dockerUntouched(t, f.Calls)
	for _, c := range rootCalls(f.Calls) {
		if !strings.HasPrefix(c, "sudo -n ") {
			t.Errorf("not sudo -n: %s", c)
		}
	}
}

func TestUplinkZoneFallsBackToTheDefaultZone(t *testing.T) {
	f := host.NewFake().
		On("ip -j route show default", 0, `[{"dev":"enp0s31f6"}]`).
		On("sudo -n firewall-cmd --get-zone-of-interface=enp0s31f6", 2, "no zone\n").
		On("sudo -n firewall-cmd --get-default-zone", 0, "home\n")
	e, _ := testEnv(f)
	if z, err := uplinkZone(context.Background(), e); err != nil || z != "home" {
		t.Fatalf("%q %v", z, err)
	}
}

func TestUplinkZoneNeedsADefaultRoute(t *testing.T) {
	e, _ := testEnv(host.NewFake().On("ip -j route show default", 0, "[]"))
	if _, err := uplinkZone(context.Background(), e); err == nil || !strings.Contains(err.Error(), "no default route") {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./installer/internal/create/ -run 'Fw|Firewalld|Uplink'`
Expected: FAIL, `undefined: fwState`.

- [ ] **Step 3: Write `firewalld.go`**

```go
package create

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const (
	fwZone     = "flocc-incus"
	fwOut      = "flocc-incus-out"
	fwNoDocker = "flocc-no-docker"
	dockerZone = "docker"
	dockerFwd  = "docker-forwarding"
)

// fwServices is what the VM may reach on the machine itself: Incus's dnsmasq.
var fwServices = []string{"dhcp", "dns"}

type fwState struct {
	Zones, Policies        []string
	BridgeZone             string
	Services, Ports        []string
	OutIn, OutEg           []string
	OutTarget              string
	NoDockerIn, NoDockerEg []string
	NoDockerTarget         string
	NoDockerPriority       int
	DockerFwdPriority      int
}

func readFirewall(ctx context.Context, e *Env, permanent bool) fwState {
	q := func(args ...string) string {
		if permanent {
			args = append([]string{"--permanent"}, args...)
		}
		return e.out(ctx, e.root(cmd("firewall-cmd", args...)))
	}
	f := strings.Fields
	s := fwState{Zones: f(q("--get-zones")), Policies: f(q("--get-policies")), BridgeZone: q("--get-zone-of-interface=" + Bridge)}
	if slices.Contains(s.Zones, fwZone) {
		s.Services = f(q("--zone="+fwZone, "--list-services"))
		s.Ports = f(q("--zone="+fwZone, "--list-ports"))
	}
	if slices.Contains(s.Policies, fwOut) {
		s.OutIn = f(q("--policy="+fwOut, "--list-ingress-zones"))
		s.OutEg = f(q("--policy="+fwOut, "--list-egress-zones"))
		s.OutTarget = q("--policy="+fwOut, "--get-target")
	}
	if slices.Contains(s.Policies, fwNoDocker) {
		s.NoDockerIn = f(q("--policy="+fwNoDocker, "--list-ingress-zones"))
		s.NoDockerEg = f(q("--policy="+fwNoDocker, "--list-egress-zones"))
		s.NoDockerTarget = q("--policy="+fwNoDocker, "--get-target")
		s.NoDockerPriority, _ = strconv.Atoi(q("--policy="+fwNoDocker, "--get-priority"))
	}
	if slices.Contains(s.Policies, dockerFwd) {
		s.DockerFwdPriority, _ = strconv.Atoi(q("--policy="+dockerFwd, "--get-priority"))
	}
	return s
}

// readBoth reads the runtime and the permanent configuration. Docker's zone counts as present in
// both when either has it, so the two checks cannot disagree about wanting flocc-no-docker. Docker
// creates the zone permanently today; a runtime-only zone ends at firewall-cmd refusing to name it.
func readBoth(ctx context.Context, e *Env) (runtime, permanent fwState) {
	runtime, permanent = readFirewall(ctx, e, false), readFirewall(ctx, e, true)
	if slices.Contains(runtime.Zones, dockerZone) || slices.Contains(permanent.Zones, dockerZone) {
		for _, s := range []*fwState{&runtime, &permanent} {
			if !slices.Contains(s.Zones, dockerZone) {
				s.Zones = append(s.Zones, dockerZone)
			}
		}
	}
	return runtime, permanent
}

// noDockerPriority runs the reject ahead of docker-forwarding, which accepts forwarding from
// every zone into Docker's bridges, published ports included. Lower values run first.
func noDockerPriority(s fwState) int { return min(-100, s.DockerFwdPriority-1) }

func fwProblems(s fwState, up string) []string {
	if !slices.Contains(s.Zones, fwZone) {
		return []string{"no zone " + fwZone}
	}
	var p []string
	if s.BridgeZone != fwZone {
		p = append(p, fmt.Sprintf("%s is in zone %q", Bridge, s.BridgeZone))
	}
	if !sameSet(s.Services, fwServices) || len(s.Ports) > 0 {
		p = append(p, fmt.Sprintf("zone %s allows %q, not only dhcp and dns", fwZone, slices.Concat(s.Services, s.Ports)))
	}
	if !sameSet(s.OutIn, []string{fwZone}) || !sameSet(s.OutEg, []string{up}) || s.OutTarget != "ACCEPT" {
		p = append(p, fmt.Sprintf("policy %s does not forward %s to %s", fwOut, fwZone, up))
	}
	if slices.Contains(s.Zones, dockerZone) && (!sameSet(s.NoDockerIn, []string{fwZone}) || !sameSet(s.NoDockerEg, []string{dockerZone}) ||
		s.NoDockerTarget != "REJECT" || s.NoDockerPriority > noDockerPriority(s)) {
		p = append(p, fmt.Sprintf("policy %s does not keep %s out of Docker's networks", fwNoDocker, fwZone))
	}
	return p
}

// fwFixes are the firewall-cmd argument lists that turn the permanent configuration s into the
// wanted one. Each is permanent; Apply reloads once after them.
func fwFixes(s fwState, up string) [][]string {
	var c [][]string
	perm := func(args ...string) { c = append(c, append([]string{"--permanent"}, args...)) }
	if !slices.Contains(s.Zones, fwZone) {
		perm("--new-zone=" + fwZone)
	}
	for _, svc := range s.Services {
		if !slices.Contains(fwServices, svc) {
			perm("--zone="+fwZone, "--remove-service="+svc)
		}
	}
	for _, svc := range fwServices {
		if !slices.Contains(s.Services, svc) {
			perm("--zone="+fwZone, "--add-service="+svc)
		}
	}
	for _, port := range s.Ports {
		perm("--zone="+fwZone, "--remove-port="+port)
	}
	if s.BridgeZone != fwZone {
		perm("--zone="+fwZone, "--change-interface="+Bridge)
	}
	c = append(c, fixPolicy(fwOut, slices.Contains(s.Policies, fwOut), s.OutIn, s.OutEg, s.OutTarget, fwZone, up, "ACCEPT")...)
	if slices.Contains(s.Zones, dockerZone) {
		c = append(c, fixPolicy(fwNoDocker, slices.Contains(s.Policies, fwNoDocker), s.NoDockerIn, s.NoDockerEg, s.NoDockerTarget, fwZone, dockerZone, "REJECT")...)
		// A policy that does not exist yet reads as priority 0, above any wanted value.
		if want := noDockerPriority(s); s.NoDockerPriority > want {
			perm("--policy="+fwNoDocker, "--set-priority="+strconv.Itoa(want))
		}
	}
	return c
}

func fixPolicy(name string, exists bool, in, eg []string, target, wantIn, wantEg, wantTarget string) [][]string {
	var c [][]string
	set := func(arg string) { c = append(c, []string{"--permanent", "--policy=" + name, arg}) }
	if !exists {
		c = append(c, []string{"--permanent", "--new-policy=" + name})
	}
	for _, z := range in {
		if z != wantIn {
			set("--remove-ingress-zone=" + z)
		}
	}
	if !slices.Contains(in, wantIn) {
		set("--add-ingress-zone=" + wantIn)
	}
	for _, z := range eg {
		if z != wantEg {
			set("--remove-egress-zone=" + z)
		}
	}
	if !slices.Contains(eg, wantEg) {
		set("--add-egress-zone=" + wantEg)
	}
	if target != wantTarget {
		set("--set-target=" + wantTarget)
	}
	return c
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}

// uplinkZone is the firewalld zone of the default route's interface, where the VM may forward to.
func uplinkZone(ctx context.Context, e *Env) (string, error) {
	var routes []struct {
		Dev string `json:"dev"`
	}
	if json.Unmarshal([]byte(e.out(ctx, cmd("ip", "-j", "route", "show", "default"))), &routes) != nil || len(routes) == 0 || routes[0].Dev == "" {
		return "", fmt.Errorf("%s has no default route", e.Spec.Machine)
	}
	dev := routes[0].Dev
	if z := e.out(ctx, e.root(cmd("firewall-cmd", "--get-zone-of-interface="+dev))); z != "" && z != "no zone" {
		return z, nil
	}
	// An interface bound to no zone falls in the default zone.
	if z := e.out(ctx, e.root(cmd("firewall-cmd", "--get-default-zone"))); z != "" {
		return z, nil
	}
	return "", fmt.Errorf("firewalld names no zone for %s", dev)
}

type Firewalld struct{}

func (Firewalld) Name() string { return "Firewalld" }

func (Firewalld) Check(ctx context.Context, e *Env) (bool, string, error) {
	if !e.ok(ctx, cmd("systemctl", "is-active", "--quiet", "firewalld.service")) {
		return true, "firewalld not running; nothing keeps the VM off this machine's services", nil
	}
	if err := e.needRoot("Reading firewalld"); err != nil {
		return false, "", err
	}
	up, err := uplinkZone(ctx, e)
	if err != nil {
		return false, "", err
	}
	runtime, permanent := readBoth(ctx, e)
	for _, c := range []struct {
		where string
		s     fwState
	}{{"runtime", runtime}, {"permanent", permanent}} {
		if p := fwProblems(c.s, up); len(p) > 0 {
			return false, c.where + ": " + strings.Join(p, "; "), nil
		}
	}
	detail := fmt.Sprintf("%s in zone %s (dhcp, dns), forwarded only to zone %s", Bridge, fwZone, up)
	// Forwarded traffic that leaves through tailscale0 is masqueraded as the machine.
	if e.out(ctx, e.root(cmd("firewall-cmd", "--get-zone-of-interface=tailscale0"))) == up {
		detail += "; tailscale0 is in the same zone, so the VM can also leave through this machine's tailnet"
	}
	return true, detail, nil
}

func (Firewalld) Apply(ctx context.Context, e *Env) error {
	up, err := uplinkZone(ctx, e)
	if err != nil {
		return err
	}
	_, permanent := readBoth(ctx, e)
	for _, args := range fwFixes(permanent, up) {
		if _, err := e.must(ctx, e.root(cmd("firewall-cmd", args...))); err != nil {
			return err
		}
	}
	// New zones and policies exist only in the permanent configuration until a reload.
	_, err = e.must(ctx, e.root(cmd("firewall-cmd", "--reload")))
	return err
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./installer/internal/create/ -run 'Fw|Firewalld|Uplink' -v`
Expected: every listed test `PASS`.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/create/firewalld.go installer/internal/create/firewalld_test.go
git commit -s -m "create: scope firewalld to dhcp and dns and a forward policy to the uplink"
```

---

### Task 4: Docker DOCKER-USER step

**Depends on:** Task 1.

**Files:**
- Create: `installer/internal/create/docker.go`
- Test: `installer/internal/create/docker_test.go`

**Interfaces:**
- Consumes: `Env`, `cmd`, `(e *Env).root/out/must/ok/needRoot`, `Bridge`, `host.Cmd.Stdin` and `host.Fake.Stdins` (Part 2a), test helpers `testEnv`, `dockerUntouched`, `rootCalls` (Task 1).
- Produces: `type Docker struct{}` implementing `Step` (name `"Docker"`); `dockerUnitName = "flocc-docker-user.service"`, `dockerUnitPath = "/etc/systemd/system/flocc-docker-user.service"`; `daemonJSON = "/etc/docker/daemon.json"`; `dockerRules [][]string`; `dockerUnit() string`; `dockerBackend(daemon string) string`.

Design facts, from current docs:

- Docker adds unconditional jumps from `FORWARD` to `DOCKER-USER`, the chain for user rules, and sets the `FORWARD` policy to `DROP` when it enables forwarding ([Docker with iptables](https://docs.docker.com/engine/network/firewall-iptables/)).
- The Incus docs give exactly these two rules for the bridge, and say they must be made persistent across reboots by a distribution-specific method ([Incus: "Prevent connectivity issues with Incus and Docker"](https://linuxcontainers.org/incus/docs/main/howto/network_bridge_firewalld/)).
- The nftables backend (`"firewall-backend": "nftables"`, experimental since Docker 29.0.0) has no `DOCKER-USER` chain ([Docker with nftables](https://docs.docker.com/engine/network/firewall-nftables/)). Create reads the backend from that key of `/etc/docker/daemon.json` (absent means iptables) rather than from `docker info`, which needs a running daemon; `docker info --format '{{json .FirewallBackend}}'` is expected to print `{"Driver":"iptables+firewalld",...}` and agree (listed under Unconfirmed; Task 9 checks it on the laptop).
- Docker installed but stopped, or socket-activated, still sets `FORWARD` to `DROP` when it next starts. The step therefore installs and enables the unit then too (`WantedBy=docker.service` runs it at that start) and skips the rule checks and the unit restart, since there is no `DOCKER-USER` chain to test or fill until Docker runs.
- systemd expands `$NAME` in `Exec*=` lines and turns `$$` into `$`, so the shell's loop variable is written `$$t`.

- [ ] **Step 1: Write the failing tests**

`installer/internal/create/docker_test.go`:

```go
package create

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const (
	daemonDefault  = `{"log-driver":"json-file"}`
	daemonNftables = `{"firewall-backend":"nftables"}`
)

// dockerMachine has Docker running with the given daemon.json and, when installed, the unit and
// both iptables rules. ip6tables has no DOCKER-USER chain unless a test adds one.
func dockerMachine(daemon string, installed bool) *host.Fake {
	f := host.NewFake().
		On("systemctl is-active --quiet docker.service", 0, "").
		On("cat "+daemonJSON, 0, daemon)
	if installed {
		f.On("cat "+dockerUnitPath, 0, dockerUnit()).
			On("systemctl is-enabled --quiet "+dockerUnitName, 0, "").
			On("sudo -n iptables -C DOCKER-USER -i incusbr0 -j ACCEPT", 0, "").
			On("sudo -n iptables -C DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT", 0, "")
	}
	return f
}

func TestDockerSkippedWhenNotRunning(t *testing.T) {
	f := host.NewFake().On("systemctl is-active --quiet docker.service", 3, "inactive\n")
	e, _ := testEnv(f)
	e.sudo = false
	if ok, detail, err := (Docker{}).Check(context.Background(), e); !ok || err != nil || detail != "Docker not running" {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
}

func TestDockerNeedsPasswordlessSudo(t *testing.T) {
	e, _ := testEnv(dockerMachine(daemonDefault, false))
	e.sudo = false
	if _, _, err := (Docker{}).Check(context.Background(), e); err == nil || !strings.Contains(err.Error(), "passwordless sudo") {
		t.Fatal(err)
	}
}

func TestDockerInstalledPassesWithoutWriting(t *testing.T) {
	f := dockerMachine(daemonDefault, true)
	e, _ := testEnv(f)
	ok, detail, err := Docker{}.Check(context.Background(), e)
	if !ok || err != nil || !strings.Contains(detail, "iptables backend") {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "tee") || strings.Contains(c, "-I ") || strings.Contains(c, "systemctl enable") {
			t.Errorf("writes: %s", c)
		}
	}
}

func TestDockerChecksIPv6RulesWhereDockerMadeTheChain(t *testing.T) {
	f := dockerMachine(daemonDefault, true).On("sudo -n ip6tables -L DOCKER-USER -n", 0, "")
	e, _ := testEnv(f)
	ok, detail, _ := Docker{}.Check(context.Background(), e)
	if ok || !strings.HasPrefix(detail, "ip6tables DOCKER-USER lacks -i incusbr0 -j ACCEPT") {
		t.Fatalf("%v %q", ok, detail)
	}
	f.On("sudo -n ip6tables -C DOCKER-USER -i incusbr0 -j ACCEPT", 0, "").
		On("sudo -n ip6tables -C DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT", 0, "")
	if ok, detail, _ := (Docker{}).Check(context.Background(), e); !ok {
		t.Fatalf("%q", detail)
	}
}

func TestDockerStoppedButEnabledStillGetsTheUnit(t *testing.T) {
	f := host.NewFake().
		On("systemctl is-active --quiet docker.service", 3, "").
		On("systemctl is-enabled --quiet docker.service", 0, "").
		On("sudo -n tee "+dockerUnitPath, 0, "").
		On("sudo -n systemctl daemon-reload", 0, "").
		On("sudo -n systemctl enable "+dockerUnitName, 0, "")
	e, _ := testEnv(f)
	if ok, detail, _ := (Docker{}).Check(context.Background(), e); ok || !strings.Contains(detail, dockerUnitPath) {
		t.Fatalf("%v %q", ok, detail)
	}
	if err := (Docker{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	// Restarting the oneshot with Docker stopped would leave it active, and the start that follows
	// would not run it again.
	if f.Ran("sudo -n systemctl restart " + dockerUnitName) {
		t.Fatalf("%v", f.Calls)
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerStoppedWithTheUnitEnabledPasses(t *testing.T) {
	f := host.NewFake().
		On("systemctl is-active --quiet docker.service", 3, "").
		On("systemctl is-active --quiet docker.socket", 0, "").
		On("cat "+dockerUnitPath, 0, dockerUnit()).
		On("systemctl is-enabled --quiet "+dockerUnitName, 0, "")
	e, _ := testEnv(f)
	ok, detail, _ := Docker{}.Check(context.Background(), e)
	if !ok || !strings.Contains(detail, "next start") {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestDockerNftablesBackendStopsCreate(t *testing.T) {
	f := dockerMachine(daemonNftables, false)
	e, _ := testEnv(f)
	ok, detail, _ := Docker{}.Check(context.Background(), e)
	if ok || !strings.Contains(detail, "nftables") {
		t.Fatalf("%v %q", ok, detail)
	}
	err := Docker{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "nftables") || !strings.Contains(err.Error(), "DOCKER-USER") {
		t.Fatal(err)
	}
	if f.Ran("sudo -n tee " + dockerUnitPath) {
		t.Fatal("wrote the unit for an nftables Docker")
	}
	dockerUntouched(t, f.Calls)
}

func TestDockerApplyInstallsTheUnitAndNeverRestartsDocker(t *testing.T) {
	f := dockerMachine(daemonDefault, false).
		On("sudo -n tee "+dockerUnitPath, 0, "").
		On("sudo -n systemctl daemon-reload", 0, "").
		On("sudo -n systemctl enable "+dockerUnitName, 0, "").
		On("sudo -n systemctl restart "+dockerUnitName, 0, "")
	e, _ := testEnv(f)
	if err := (Docker{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"sudo -n tee " + dockerUnitPath,
		"sudo -n systemctl daemon-reload",
		"sudo -n systemctl enable " + dockerUnitName,
		"sudo -n systemctl restart " + dockerUnitName,
	}
	if got := f.Calls[len(f.Calls)-len(want):]; !slices.Equal(got, want) {
		t.Fatalf("%q", f.Calls)
	}
	dockerUntouched(t, f.Calls)
}

func TestEveryRootCommandIsNonInteractive(t *testing.T) {
	f := dockerMachine(daemonDefault, false)
	e, _ := testEnv(f)
	Docker{}.Check(context.Background(), e)
	Docker{}.Apply(context.Background(), e)
	for _, c := range rootCalls(f.Calls) {
		if !strings.HasPrefix(c, "sudo -n ") {
			t.Errorf("not sudo -n: %s", c)
		}
	}
}

func TestDockerUnitIsWrittenFromStdin(t *testing.T) {
	f := dockerMachine(daemonDefault, false).On("sudo -n tee "+dockerUnitPath, 0, "")
	e, _ := testEnv(f)
	Docker{}.Apply(context.Background(), e)
	if got, ok := f.Stdins["sudo -n tee "+dockerUnitPath]; !ok || got != dockerUnit() {
		t.Fatalf("stdin %q (%v)", got, f.Calls)
	}
}

func TestDockerUnitShape(t *testing.T) {
	u := dockerUnit()
	for _, want := range []string{"After=docker.service\n", "PartOf=docker.service\n", "WantedBy=docker.service\n", "Type=oneshot\n", "RemainAfterExit=yes\n"} {
		if !strings.Contains(u, want) {
			t.Errorf("lacks %q", want)
		}
	}
	// A lone $t would be expanded by systemd before the shell sees it.
	if regexp.MustCompile(`[^$]\$t\b`).MatchString(u) {
		t.Fatalf("unescaped $t:\n%s", u)
	}
}

// execLine is the shell script of the unit's key= line, unescaped the way systemd does.
func execLine(t *testing.T, key string) string {
	t.Helper()
	for _, l := range strings.Split(dockerUnit(), "\n") {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			v = strings.TrimSuffix(strings.TrimPrefix(v, "/bin/sh -c '"), "'")
			return strings.ReplaceAll(v, "$$", "$")
		}
	}
	t.Fatalf("no %s line", key)
	return ""
}

// runUnitScript runs the unit's key= script against fake iptables and ip6tables that log their
// arguments. iptables has DOCKER-USER, and -C exits checkExit; ip6tables has no DOCKER-USER chain.
func runUnitScript(t *testing.T, key, checkExit string) []string {
	t.Helper()
	bin := t.TempDir()
	logf := filepath.Join(bin, "log")
	fake := "#!/bin/sh\necho \"$(basename \"$0\") $*\" >> " + logf + "\n" +
		"case \"$(basename \"$0\") $1\" in\n" +
		"  \"iptables -L\") exit 0 ;;\n" +
		"  \"iptables -C\") exit " + checkExit + " ;;\n" +
		"  \"ip6tables -L\") exit 1 ;;\n" +
		"esac\nexit 0\n"
	for _, n := range []string{"iptables", "ip6tables"} {
		if err := os.WriteFile(filepath.Join(bin, n), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c := exec.Command("sh", "-c", execLine(t, key))
	c.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v %s", key, err, out)
	}
	b, _ := os.ReadFile(logf)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestDockerUnitStartAddsMissingRules(t *testing.T) {
	want := []string{
		"iptables -L DOCKER-USER -n",
		"iptables -C DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
		"iptables -I DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
		"iptables -C DOCKER-USER -i incusbr0 -j ACCEPT",
		"iptables -I DOCKER-USER -i incusbr0 -j ACCEPT",
		"ip6tables -L DOCKER-USER -n",
	}
	if got := runUnitScript(t, "ExecStart", "1"); !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestDockerUnitStartAddsNothingTwice(t *testing.T) {
	for _, line := range runUnitScript(t, "ExecStart", "0") {
		if strings.Contains(line, " -I ") {
			t.Fatalf("inserted a present rule: %s", line)
		}
	}
}

func TestDockerUnitStopRemovesTheRules(t *testing.T) {
	want := []string{
		"iptables -L DOCKER-USER -n",
		"iptables -D DOCKER-USER -i incusbr0 -j ACCEPT",
		"iptables -D DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
		"ip6tables -L DOCKER-USER -n",
	}
	if got := runUnitScript(t, "ExecStop", "0"); !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestDockerBackend(t *testing.T) {
	for in, want := range map[string]string{
		daemonNftables:                    "nftables",
		`{"firewall-backend":"iptables"}`: "iptables",
		daemonDefault:                     "iptables",
		"not json":                        "iptables",
		"":                                "iptables",
	} {
		if got := dockerBackend(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./installer/internal/create/ -run 'Docker|RootCommand'`
Expected: FAIL, `undefined: Docker`.

- [ ] **Step 3: Write `docker.go`**

```go
package create

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

const (
	dockerUnitName = "flocc-docker-user.service"
	dockerUnitPath = "/etc/systemd/system/" + dockerUnitName
)

// dockerRules let the VM through the DROP policy Docker puts on FORWARD, scoped to the bridge,
// as the Incus firewall docs give them.
var dockerRules = [][]string{
	{"-i", Bridge, "-j", "ACCEPT"},
	{"-o", Bridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
}

// dockerUnit re-adds the rules after every Docker start (PartOf restarts it with Docker) and
// removes them when Docker stops. systemd expands $name in Exec lines, so the shell's variable is
// $$t. ip6tables is skipped where Docker made no IPv6 DOCKER-USER chain.
func dockerUnit() string {
	var start, stop []string
	for i := len(dockerRules) - 1; i >= 0; i-- {
		r := strings.Join(dockerRules[i], " ")
		start = append(start, "$$t -C DOCKER-USER "+r+" 2>/dev/null || $$t -I DOCKER-USER "+r)
	}
	for _, rule := range dockerRules {
		stop = append(stop, "$$t -D DOCKER-USER "+strings.Join(rule, " ")+" 2>/dev/null")
	}
	loop := "for t in iptables ip6tables; do $$t -L DOCKER-USER -n >/dev/null 2>&1 || continue; "
	return "[Unit]\n" +
		"Description=Let Incus VMs on " + Bridge + " through Docker's forward drop (flocc)\n" +
		"After=docker.service\n" +
		"PartOf=docker.service\n" +
		"\n[Service]\n" +
		"Type=oneshot\n" +
		"RemainAfterExit=yes\n" +
		"ExecStart=/bin/sh -c '" + loop + strings.Join(start, "; ") + "; done'\n" +
		"ExecStop=/bin/sh -c '" + loop + strings.Join(stop, "; ") + "; done; true'\n" +
		"\n[Install]\n" +
		"WantedBy=docker.service\n"
}

const daemonJSON = "/etc/docker/daemon.json"

// dockerBackend is the firewall-backend key of daemon.json, read from the file because docker info
// needs a running daemon. A Docker without the key uses iptables; the nftables backend arrived
// with 29.0.
func dockerBackend(daemon string) string {
	var cfg struct {
		Backend string `json:"firewall-backend"`
	}
	if json.Unmarshal([]byte(daemon), &cfg) != nil || cfg.Backend == "" {
		return "iptables"
	}
	return cfg.Backend
}

type Docker struct{}

func (Docker) Name() string { return "Docker" }

func (d Docker) backend(ctx context.Context, e *Env) string {
	return dockerBackend(e.out(ctx, cmd("cat", daemonJSON)))
}

func (d Docker) Check(ctx context.Context, e *Env) (bool, string, error) {
	running := e.ok(ctx, cmd("systemctl", "is-active", "--quiet", "docker.service"))
	// A stopped or socket-activated Docker still sets FORWARD to DROP at its next start.
	if !running && !e.ok(ctx, cmd("systemctl", "is-enabled", "--quiet", "docker.service")) &&
		!e.ok(ctx, cmd("systemctl", "is-active", "--quiet", "docker.socket")) {
		return true, "Docker not running", nil
	}
	if err := e.needRoot("Reading Docker's firewall rules"); err != nil {
		return false, "", err
	}
	backend := d.backend(ctx, e)
	if strings.HasPrefix(backend, "nftables") {
		return false, "Docker uses the nftables firewall backend", nil
	}
	if e.out(ctx, cmd("cat", dockerUnitPath)) != strings.TrimSpace(dockerUnit()) {
		return false, dockerUnitPath + " missing or out of date", nil
	}
	if !e.ok(ctx, cmd("systemctl", "is-enabled", "--quiet", dockerUnitName)) {
		return false, dockerUnitName + " not enabled", nil
	}
	if !running {
		return true, "Docker stopped; " + dockerUnitName + " adds the rules at its next start", nil
	}
	for _, tool := range []string{"iptables", "ip6tables"} {
		// Only where Docker made an IPv6 DOCKER-USER chain, as the unit does.
		if tool == "ip6tables" && !e.ok(ctx, e.root(cmd(tool, "-L", "DOCKER-USER", "-n"))) {
			continue
		}
		for _, r := range dockerRules {
			if !e.ok(ctx, e.root(cmd(tool, append([]string{"-C", "DOCKER-USER"}, r...)...))) {
				return false, tool + " DOCKER-USER lacks " + strings.Join(r, " "), nil
			}
		}
	}
	return true, "DOCKER-USER lets " + Bridge + " through (" + backend + " backend)", nil
}

func (d Docker) Apply(ctx context.Context, e *Env) error {
	if strings.HasPrefix(d.backend(ctx, e), "nftables") {
		return errors.New("Docker on " + e.Spec.Machine + " uses the nftables firewall backend, which has no DOCKER-USER chain; create supports only Docker's iptables backend")
	}
	running := e.ok(ctx, cmd("systemctl", "is-active", "--quiet", "docker.service"))
	tee := e.root(cmd("tee", dockerUnitPath))
	tee.Stdin = strings.NewReader(dockerUnit())
	if _, err := e.must(ctx, tee); err != nil {
		return err
	}
	// Only this oneshot is restarted (ExecStop, then ExecStart). docker.service never is: on
	// 2026-10-20 a Docker restart killed another session's image build on the shared laptop.
	// With Docker stopped the oneshot stays inactive: once active it would not run again at
	// Docker's start.
	steps := [][]string{{"daemon-reload"}, {"enable", dockerUnitName}}
	if running {
		steps = append(steps, []string{"restart", dockerUnitName})
	}
	for _, args := range steps {
		if _, err := e.must(ctx, e.root(cmd("systemctl", args...))); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./installer/internal/create/ -run 'Docker|RootCommand' -v`
Expected: every listed test `PASS`.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/create/docker.go installer/internal/create/docker_test.go
git commit -s -m "create: DOCKER-USER rules for incusbr0 in a oneshot tied to docker.service"
```

---

### Task 5: VM launch and egress steps

**Depends on:** Task 1.

**Files:**
- Create: `installer/internal/create/vm.go`
- Test: `installer/internal/create/vm_test.go`

**Interfaces:**
- Consumes: `Env`, `cmd`, `(e *Env).incus/out/ok/must`, `Bridge`, `Image`, `tailnetHas`, `tailnetTaken`, test helpers `testEnv`, `tsStatus`, `tsEmpty` (Task 1).
- Produces: `type VM struct{}` (name `"VM"`), `type Egress struct{}` (name `"Egress"`), both `Step`; `(e *Env) instance(ctx) (instance, bool)`; `(e *Env) ipv4(ctx) string`; `const vmPoll = 5 * time.Second`, `const vmPolls = 60`; `const egressURL = "https://pkgs.tailscale.com/"`.

Incus commands, from [Incus: How to create instances](https://linuxcontainers.org/incus/docs/main/howto/instances_create/): `incus launch images:debian/12 debian-vm --vm`, `--config limits.cpu=1 --config limits.memory=192MiB`, `--device root,size=30GiB` (the short forms `-c` and `-d` appear in the same page). `--network` attaches eth0 to `incusbr0` whatever the default profile names.

- [ ] **Step 1: Write the failing tests**

`installer/internal/create/vm_test.go`:

```go
package create

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const (
	launch     = "incus launch images:debian/13 flocc-a --vm --network incusbr0 -c boot.autostart=true -c limits.cpu=4 -c limits.memory=8GiB -d root,size=40GiB"
	vmRunning  = `{"status":"Running","config":{"boot.autostart":"true"}}`
	vmNoIP     = `{"status":"Running","network":{"lo":{"addresses":[{"family":"inet","address":"127.0.0.1","scope":"local"}]},"enp5s0":{"addresses":[]}}}`
	vmWithIP   = `{"status":"Running","network":{"lo":{"addresses":[{"family":"inet","address":"127.0.0.1","scope":"local"}]},"enp5s0":{"addresses":[{"family":"inet6","address":"fd42::5","scope":"global"},{"family":"inet","address":"10.10.0.5","scope":"global"}]}}}`
	stateQuery = "incus query /1.0/instances/flocc-a/state"
	agentUp    = "incus exec flocc-a -- true"
)

func TestVMRunningWithAnAddressIsDone(t *testing.T) {
	f := host.NewFake().On("incus query /1.0/instances/flocc-a", 0, vmRunning).On(stateQuery, 0, vmWithIP).On(agentUp, 0, "")
	e, _ := testEnv(f)
	if ok, detail, _ := (VM{}).Check(context.Background(), e); !ok || detail != "flocc-a running at 10.10.0.5" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestVMLaunchesAndWaitsForAnAddress(t *testing.T) {
	f := host.NewFake().
		On(launch, 0, "").
		OnSeq(stateQuery, host.Result{Stdout: vmNoIP}, host.Result{Stdout: vmWithIP}).
		On(agentUp, 0, "")
	e, _ := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	sleeps := 0
	e.Sleep = func(d time.Duration) { sleeps++ }
	if err := (VM{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran(launch) || sleeps != 1 {
		t.Fatalf("sleeps=%d %v", sleeps, f.Calls)
	}
}

func TestVMStopsWhenTheTailnetHasTheName(t *testing.T) {
	f := host.NewFake()
	e, _ := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsStatus)
	err := VM{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "already a node on your tailnet") {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "incus launch") {
			t.Fatalf("launched: %s", c)
		}
	}
}

func TestVMResumesAHalfMadeInstance(t *testing.T) {
	f := host.NewFake().
		On("incus query /1.0/instances/flocc-a", 0, `{"status":"Stopped","config":{}}`).
		On("incus config set flocc-a boot.autostart=true", 0, "").
		On("incus start flocc-a", 0, "").
		On(stateQuery, 0, vmWithIP).
		On(agentUp, 0, "")
	e, _ := testEnv(f)
	e.Local = host.NewFake() // a tailnet read would fail the test: the name is ours already
	if err := (VM{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("incus config set flocc-a boot.autostart=true") || !f.Ran("incus start flocc-a") || f.Ran(launch) {
		t.Fatalf("%v", f.Calls)
	}
}

func TestVMCheckWantsAutostart(t *testing.T) {
	f := host.NewFake().On("incus query /1.0/instances/flocc-a", 0, `{"status":"Running","config":{}}`)
	e, _ := testEnv(f)
	if ok, detail, _ := (VM{}).Check(context.Background(), e); ok || detail != "boot.autostart is off" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestVMWaitGivesUp(t *testing.T) {
	f := host.NewFake().
		On("incus query /1.0/instances/flocc-a", 0, vmRunning).
		On(stateQuery, 0, vmNoIP)
	e, _ := testEnv(f)
	err := VM{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "no IPv4 address after 5m0s") || !strings.Contains(err.Error(), "incus console flocc-a --show-log") {
		t.Fatal(err)
	}
}

const curlCheck = "incus exec flocc-a -- curl -fsS -o /dev/null --max-time 15 https://pkgs.tailscale.com/"

func TestEgressInstallsCurlWhenMissing(t *testing.T) {
	f := host.NewFake().
		On("incus exec flocc-a -- sh -c command -v curl", 1, "").
		On("incus exec flocc-a --env DEBIAN_FRONTEND=noninteractive -- apt-get update -q", 0, "").
		On("incus exec flocc-a --env DEBIAN_FRONTEND=noninteractive -- apt-get install -y -q curl ca-certificates", 0, "").
		On(curlCheck, 0, "")
	e, _ := testEnv(f)
	if err := (Egress{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("incus exec flocc-a --env DEBIAN_FRONTEND=noninteractive -- apt-get install -y -q curl ca-certificates") {
		t.Fatalf("%v", f.Calls)
	}
}

func TestEgressNamesTheFirewallWhenAptFails(t *testing.T) {
	f := host.NewFake().
		On("incus exec flocc-a -- sh -c command -v curl", 1, "").
		On("incus exec flocc-a --env DEBIAN_FRONTEND=noninteractive -- apt-get update -q", 100, "Could not resolve deb.debian.org")
	e, _ := testEnv(f)
	err := Egress{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "docs/vm-setup.md") {
		t.Fatal(err)
	}
}

func TestEgressCheckFailureNamesTheDocs(t *testing.T) {
	e, _ := testEnv(host.NewFake().On(curlCheck, 7, ""))
	if ok, detail, _ := (Egress{}).Check(context.Background(), e); ok || !strings.Contains(detail, "docs/vm-setup.md") {
		t.Fatalf("%v %q", ok, detail)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./installer/internal/create/ -run 'VM|Egress'`
Expected: FAIL, `undefined: VM`.

- [ ] **Step 3: Write `vm.go`**

```go
package create

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

const (
	vmPoll    = 5 * time.Second
	vmPolls   = 60
	egressURL = "https://pkgs.tailscale.com/"
)

type instance struct {
	Status string            `json:"status"`
	Config map[string]string `json:"config"`
}

func (e *Env) instance(ctx context.Context) (instance, bool) {
	var in instance
	raw := e.out(ctx, e.incus("query", "/1.0/instances/"+e.Spec.Name))
	return in, raw != "" && json.Unmarshal([]byte(raw), &in) == nil
}

// ipv4 is the VM's first global IPv4 address, or "" before DHCP. The agent reports the guest's
// own interface names (enp5s0 on Debian), so none is assumed.
func (e *Env) ipv4(ctx context.Context) string {
	var st struct {
		Network map[string]struct {
			Addresses []struct{ Family, Address, Scope string } `json:"addresses"`
		} `json:"network"`
	}
	if json.Unmarshal([]byte(e.out(ctx, e.incus("query", "/1.0/instances/"+e.Spec.Name+"/state"))), &st) != nil {
		return ""
	}
	for _, n := range slices.Sorted(maps.Keys(st.Network)) {
		for _, a := range st.Network[n].Addresses {
			if n != "lo" && a.Family == "inet" && a.Scope == "global" {
				return a.Address
			}
		}
	}
	return ""
}

type VM struct{}

func (VM) Name() string { return "VM" }

func (VM) Check(ctx context.Context, e *Env) (bool, string, error) {
	in, ok := e.instance(ctx)
	switch {
	case !ok:
		return false, "no instance " + e.Spec.Name, nil
	case in.Status != "Running":
		return false, e.Spec.Name + " is " + strings.ToLower(in.Status), nil
	case in.Config["boot.autostart"] != "true":
		return false, "boot.autostart is off", nil
	}
	ip := e.ipv4(ctx)
	if ip == "" || !e.ok(ctx, e.incus("exec", e.Spec.Name, "--", "true")) {
		return false, "no IPv4 address or agent yet", nil
	}
	return true, e.Spec.Name + " running at " + ip, nil
}

func (VM) Apply(ctx context.Context, e *Env) error {
	name := e.Spec.Name
	in, exists := e.instance(ctx)
	if !exists {
		taken, err := tailnetHas(ctx, e, name)
		if err != nil {
			return err
		}
		if taken {
			return tailnetTaken(name)
		}
		if _, err := e.must(ctx, e.incus("launch", Image, name, "--vm", "--network", Bridge,
			"-c", "boot.autostart=true",
			"-c", fmt.Sprintf("limits.cpu=%d", e.Spec.CPUs),
			"-c", fmt.Sprintf("limits.memory=%dGiB", e.Spec.MemoryGiB),
			"-d", fmt.Sprintf("root,size=%dGiB", e.Spec.DiskGiB))); err != nil {
			return err
		}
	} else {
		if in.Config["boot.autostart"] != "true" {
			if _, err := e.must(ctx, e.incus("config", "set", name, "boot.autostart=true")); err != nil {
				return err
			}
		}
		if in.Status != "Running" {
			if _, err := e.must(ctx, e.incus("start", name)); err != nil {
				return err
			}
		}
	}
	for range vmPolls {
		if e.ipv4(ctx) != "" && e.ok(ctx, e.incus("exec", name, "--", "true")) {
			return nil
		}
		e.Sleep(vmPoll)
	}
	return fmt.Errorf("%s has no IPv4 address after %s; see incus console %s --show-log", name, vmPoll*vmPolls, name)
}

type Egress struct{}

func (Egress) Name() string { return "Egress" }

func (Egress) Check(ctx context.Context, e *Env) (bool, string, error) {
	if e.ok(ctx, e.incus("exec", e.Spec.Name, "--", "curl", "-fsS", "-o", "/dev/null", "--max-time", "15", egressURL)) {
		return true, "HTTPS reaches " + egressURL, nil
	}
	return false, "no HTTPS from the VM; see \"Host create\" in docs/vm-setup.md", nil
}

func (Egress) Apply(ctx context.Context, e *Env) error {
	name := e.Spec.Name
	if !e.ok(ctx, e.incus("exec", name, "--", "sh", "-c", "command -v curl")) {
		for _, args := range [][]string{{"apt-get", "update", "-q"}, {"apt-get", "install", "-y", "-q", "curl", "ca-certificates"}} {
			if _, err := e.must(ctx, e.incus(append([]string{"exec", name, "--env", "DEBIAN_FRONTEND=noninteractive", "--"}, args...)...)); err != nil {
				return fmt.Errorf("the VM cannot install curl, so it likely has no way out; see \"Host create\" in docs/vm-setup.md: %w", err)
			}
		}
	}
	// DHCP can hand out the address before the VM's resolver answers.
	for range 6 {
		if ok, _, _ := (Egress{}).Check(ctx, e); ok {
			return nil
		}
		e.Sleep(vmPoll)
	}
	return nil
}
```

`RunSteps` re-checks after `Apply`, so an `Apply` that ends without egress is reported there with the docs pointer.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./installer/internal/create/ -run 'VM|Egress' -v`
Expected: every listed test `PASS`.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/create/vm.go installer/internal/create/vm_test.go
git commit -s -m "create: launch the Debian 13 VM with autostart and wait for HTTPS out"
```

---

### Task 6: Tailscale join step

**Depends on:** Task 1.

**Files:**
- Create: `installer/internal/create/tailscale.go`
- Test: `installer/internal/create/tailscale_test.go`

**Interfaces:**
- Consumes: `Env` (with `Log`), `RedactLog` and through it Part 2a's `host.Redact`, `Prompter`, `cmd`, `(e *Env).incus/out/ok/must`, `tailnetHas`, `tailnetTaken`, `Remote` (Task 1); `host.Cmd.Stdin`, `host.Fake.Stdins` and `host.Exec` handing `Stdin` to the process (Part 2a); test helpers `testEnv`, `fakeUI`, `tsStatus`, `tsEmpty` (Task 1).
- Produces: `type Tailnet struct{}` implementing `Step` (name `"Tailscale"`); `(e *Env) vmTailscale(ctx) tsVM`.

`tailscale up` flags, from [tailscale up](https://tailscale.com/kb/1241/tailscale-up): `--auth-key` "if it begins with `file:`, then it's a path to a file containing the auth key"; `--qr` generates a QR code for the web login URL; `--hostname` sets the MagicDNS name; `--ssh` runs the Tailscale SSH server.

- [ ] **Step 1: Write the failing tests**

`installer/internal/create/tailscale_test.go`:

```go
package create

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const (
	tsInVM    = "incus exec flocc-a -- tailscale status --json"
	prefsInVM = "incus exec flocc-a -- tailscale debug prefs"
	hasTS     = "incus exec flocc-a -- sh -c command -v tailscale"
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
		On("incus exec flocc-a -- sh -c curl -fsSL https://tailscale.com/install.sh | sh", 0, "").
		On(tsInVM, 0, `{"BackendState":"NeedsLogin"}`)
	e, out := testEnv(f)
	e.Local = host.NewFake().On("tailscale status --json", 0, tsEmpty)
	if err := (Tailnet{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("incus exec flocc-a -- sh -c curl -fsSL https://tailscale.com/install.sh | sh") || !f.Ran("interactive: "+upQR) {
		t.Fatalf("%v", f.Calls)
	}
	if !strings.Contains(out.String(), "Disable key expiry") {
		t.Fatal(out.String())
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

func TestTailnetRenamedNodeThatStaysRenamedPointsAtTheStaleNode(t *testing.T) {
	f := joined("flocc-a-1.tail1.ts.net.").On(hasTS, 0, "").On(tsSet, 0, "")
	e, _ := testEnv(f)
	err := Tailnet{}.Apply(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "already a node on your tailnet") {
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./installer/internal/create/ -run 'Tailnet|AuthKey'`
Expected: FAIL, `undefined: Tailnet`.

- [ ] **Step 3: Write `tailscale.go`**

```go
package create

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type tsVM struct {
	state, dnsName string
	ssh            bool
}

func (e *Env) vmTailscale(ctx context.Context) tsVM {
	name := e.Spec.Name
	var st struct {
		BackendState string
		Self         struct{ DNSName string }
	}
	var p struct{ RunSSH bool }
	json.Unmarshal([]byte(e.out(ctx, e.incus("exec", name, "--", "tailscale", "status", "--json"))), &st)
	json.Unmarshal([]byte(e.out(ctx, e.incus("exec", name, "--", "tailscale", "debug", "prefs"))), &p)
	return tsVM{state: st.BackendState, dnsName: st.Self.DNSName, ssh: p.RunSSH}
}

type Tailnet struct{}

func (Tailnet) Name() string { return "Tailscale" }

func (Tailnet) Check(ctx context.Context, e *Env) (bool, string, error) {
	ts := e.vmTailscale(ctx)
	label, _, _ := strings.Cut(ts.dnsName, ".")
	switch {
	case ts.state == "":
		return false, "tailscaled not running in the VM", nil
	case ts.state != "Running":
		return false, "state " + ts.state, nil
	case !strings.EqualFold(label, e.Spec.Name):
		return false, "joined as " + label + ", not " + e.Spec.Name, nil
	case !ts.ssh:
		return false, "Tailscale SSH off", nil
	}
	return true, "joined as " + strings.TrimSuffix(ts.dnsName, ".") + ", SSH on", nil
}

func (Tailnet) Apply(ctx context.Context, e *Env) error {
	name := e.Spec.Name
	if !e.ok(ctx, e.incus("exec", name, "--", "sh", "-c", "command -v tailscale")) {
		if _, err := e.must(ctx, e.incus("exec", name, "--", "sh", "-c", "curl -fsSL https://tailscale.com/install.sh | sh")); err != nil {
			return err
		}
	}
	if e.vmTailscale(ctx).state == "Running" {
		// Logged in already, renamed or without SSH; a second login would make a second node.
		if _, err := e.must(ctx, e.incus("exec", name, "--", "tailscale", "set", "--ssh", "--hostname="+name)); err != nil {
			return err
		}
		// set cannot take a name another node holds: the tailnet still has a stale node of this name.
		if label, _, _ := strings.Cut(e.vmTailscale(ctx).dnsName, "."); !strings.EqualFold(label, name) {
			return tailnetTaken(name)
		}
		return nil
	}
	taken, err := tailnetHas(ctx, e, name)
	if err != nil {
		return err
	}
	if taken {
		return tailnetTaken(name)
	}
	key, err := e.UI.Secret("tailscale_auth_key", "Tailscale auth key for "+name+" (optional)",
		"Paste a one-off auth key, or leave this empty to log in with the QR code or link that follows. The key is used once and not stored.")
	if err != nil {
		return err
	}
	// --ssh on the first up: once RunSSH is on, up refuses to run without it.
	up := []string{"exec", name, "--", "tailscale", "up", "--ssh", "--hostname=" + name}
	if key = strings.TrimSpace(key); key == "" {
		err = e.M.Interactive(ctx, e.incus(append(up, "--qr")...))
	} else {
		// Before the first command that can print it: the log gets tailscale's output as it comes.
		e.Log.Add(key)
		c := e.incus(append(up, "--auth-key=file:/dev/stdin")...)
		c.Stdin = strings.NewReader(key)
		if _, err = e.must(ctx, c); err != nil {
			err = errors.New(strings.ReplaceAll(err.Error(), key, "<auth key>"))
		}
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(e.Out, "\nTailscale key expiry: open https://login.tailscale.com/admin/machines, find %s and choose Disable key expiry. An untagged node's key otherwise expires after 180 days.\n\n", name)
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./installer/internal/create/ -run 'Tailnet|AuthKey' -v`
Expected: every listed test `PASS`.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/create/tailscale.go installer/internal/create/tailscale_test.go
git commit -s -m "create: join the VM to the tailnet by QR or a one-off key on stdin"
```

---

### Task 7: Create, snapshot and the `flocc host create` verb

**Depends on:** Tasks 1–6.

**Files:**
- Create: `installer/internal/create/install.go`
- Test: `installer/internal/create/install_test.go`
- Create: `installer/internal/cli/create.go`
- Test: `installer/internal/cli/create_test.go`
- Modify: `installer/internal/cli/host.go` (`hostCmd`: one `case "create":`)
- Modify: `installer/internal/cli/cli.go` (`usage`: one line)

**Interfaces:**
- Consumes: every step type (Tasks 2–6), `RunSteps`, `Probe`, `Remote`, `ControlOpts`, `RedactLog`, `tailnetHas`, `cmd`, test helpers `testEnv`, `fakeStep`, `tsStatus`, `tsEmpty` (Task 1); `host.Exec{Log}`; `ui.NewHuh()` (its `Secret(id, title, help string) (string, error)` satisfies `Prompter`).
- Consumes from Part 2b (final names; the `create` package sees 2b only through `Installer`, and only the adapter, the dispatch case and the usage line below name a 2b identifier), all in package `cli`:
  - `type deps struct { … }` in `installer/internal/cli/cli.go`, and `testDeps(t *testing.T) (*deps, *bytes.Buffer, *bytes.Buffer)` in `cli_test.go` (2b Task 1). The fields later 2b tasks add to `deps` stay zero in `testDeps`, so no create test reaches the real verbs.
  - `func run(ctx context.Context, d *deps, args []string) int` (2b Tasks 1 and 4; it takes `-H` out of `args` before dispatching) and the `usage` constant in `cli.go`, whose host lines end with `flocc host import -H <name> [--ssh DEST | --daytona SANDBOX]` then `flocc host run -H <name> -- <command...>`.
  - `func hostCmd(ctx context.Context, d *deps, name string, args []string) int` in `installer/internal/cli/host.go` (2b Tasks 4 and 9): `args` is the line after `host`, `args[0]` the sub-verb (`run`, `add`, `import`), `name` the `-H` value; its `switch args[0]` is where the `create` case goes. There is no `hostVerb`.
  - `func hostFile(d *deps, name string) string` in `cli/host.go` (2b Task 9): `filepath.Join(hosts.Dir(d.home), name+".toml")`, that is `<home>/.config/flocc/hosts/<name>.toml`. 2b keeps it; there is no `ship.LoadHost`.
  - `func hostAdd(ctx context.Context, d *deps, name, sshDest string) error` in `cli/host.go` (2b Task 9): `flocc host add <name> --ssh <dest>` after parsing; `sshDest == ""` keeps the file's destination or defaults to `root@<name>`. It is interactive (the repo form, the team pick) and asks on the spot for any key the desktop lacks (Linear key and agent token before the repo form, publish token and ntfy topic after the host file is saved); an empty paste returns an error wrapping `keys.ErrStopped`.
  - `func shipHost(ctx context.Context, d *deps, name string) error` in `installer/internal/cli/ship.go` (2b Task 10): a non-interactive `flocc ship -H <name>` that asks for any key still missing, calls Part 2a's `ship.Ship` and prints the summary to `d.out` with the host's name in its remedy lines; it returns nil when the only open item is the Claude login.
  - `func loginHost(ctx context.Context, d *deps, name string) error` in `cli/ship.go` (2b Task 10): `flocc login -H <name>` with no `--email`, through Part 2a's `ship.Login`, which prints the host's summary.
- Produces: `type Installer interface { HostKnown(name string) bool; AddHost(ctx, name, ssh string) error; Ship(ctx, name string) error; Login(ctx, name string) error }`; `Steps() []Step`; `Create(ctx, e *Env, inst Installer, steps []Step) error`; `waitTailnet(ctx, e) error`; `snapshot(ctx, e) error`; `ParseArgs(args []string) (Spec, error)`; `Main(args []string, inst Installer) int`; in `cli`: `type hostInstaller struct{ d *deps }`.

- [ ] **Step 1: Write the failing create tests**

`installer/internal/create/install_test.go`:

```go
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

func TestSnapshotKeepsTheOldOneWhenTheNewOneFails(t *testing.T) {
	f := host.NewFake().On(snapQuery, 0, "{}").On(snapCreate, 1, "no space left")
	e, _ := testEnv(f)
	if err := snapshot(context.Background(), e); err == nil || f.Ran(snapDelete) {
		t.Fatalf("%v %v", err, f.Calls)
	}
}

func TestSnapshotClearsALeftoverNewOne(t *testing.T) {
	const leftover = "incus query /1.0/instances/flocc-a/snapshots/installed-new"
	f := host.NewFake().On(leftover, 0, "{}").On("incus snapshot delete flocc-a installed-new", 0, "").
		On(snapCreate, 0, "").On(snapRename, 0, "")
	e, _ := testEnv(f)
	if err := snapshot(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if slices.Index(f.Calls, "incus snapshot delete flocc-a installed-new") > slices.Index(f.Calls, snapCreate) {
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./installer/internal/create/ -run 'Create|Snapshot|StepsRun|ParseArgs|Main'`
Expected: FAIL, `undefined: Create`.

- [ ] **Step 3: Write `install.go`**

```go
package create

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// Installer runs the desktop's host verbs (Part 2b) once the VM is on the tailnet.
type Installer interface {
	HostKnown(name string) bool
	AddHost(ctx context.Context, name, ssh string) error
	Ship(ctx context.Context, name string) error
	Login(ctx context.Context, name string) error
}

func Steps() []Step { return []Step{Incus{}, Firewalld{}, Docker{}, VM{}, Egress{}, Tailnet{}} }

func Create(ctx context.Context, e *Env, inst Installer, steps []Step) error {
	// Attached, so a Tailscale SSH check-mode URL for the machine shows on the terminal instead
	// of hanging a captured command; ControlPersist carries the approval through the run.
	if err := e.M.Interactive(ctx, cmd("true")); err != nil {
		return fmt.Errorf("ssh %s: %w", e.Spec.Machine, err)
	}
	if err := e.Probe(ctx); err != nil {
		return err
	}
	if err := RunSteps(ctx, e, steps); err != nil {
		return err
	}
	name := e.Spec.Name
	if err := waitTailnet(ctx, e); err != nil {
		return err
	}
	if !inst.HostKnown(name) {
		if err := inst.AddHost(ctx, name, "root@"+name); err != nil {
			return fmt.Errorf("host add: %w", err)
		}
	}
	if err := inst.Ship(ctx, name); err != nil {
		return fmt.Errorf("ship: %w", err)
	}
	if err := inst.Login(ctx, name); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	return snapshot(ctx, e)
}

const (
	tailnetPoll  = 2 * time.Second
	tailnetPolls = 30
)

// waitTailnet waits until this desktop's tailnet lists the node: host add and ship reach it by
// name right after tailscale up returns, which can be before this desktop has heard of it.
func waitTailnet(ctx context.Context, e *Env) error {
	for range tailnetPolls {
		seen, err := tailnetHas(ctx, e, e.Spec.Name)
		if err != nil {
			return err
		}
		if seen {
			return nil
		}
		e.Sleep(tailnetPoll)
	}
	return fmt.Errorf("%s joined the tailnet, but this desktop does not see it after %s; check tailscale status here, then run create again", e.Spec.Name, tailnetPoll*tailnetPolls)
}

// snapshot takes the new snapshot under a temporary name and swaps it in afterwards, so a failed
// snapshot leaves the last good installed one.
func snapshot(ctx context.Context, e *Env) error {
	name := e.Spec.Name
	has := func(snap string) bool { return e.ok(ctx, e.incus("query", "/1.0/instances/"+name+"/snapshots/"+snap)) }
	do := func(args ...string) error {
		_, err := e.must(ctx, e.incus(append([]string{"snapshot"}, args...)...))
		return err
	}
	if has("installed-new") {
		if err := do("delete", name, "installed-new"); err != nil {
			return err
		}
	}
	if err := do("create", name, "installed-new"); err != nil {
		return err
	}
	if has("installed") {
		if err := do("delete", name, "installed"); err != nil {
			return err
		}
	}
	if err := do("rename", name, "installed-new", "installed"); err != nil {
		return err
	}
	fmt.Fprintf(e.Out, "✓ %-10s %s/installed; incus snapshot restore %s installed returns to it\n", "Snapshot", name, name)
	return nil
}

const usage = "usage: flocc host create [--cpus N] [--memory GiB] [--disk GiB] <machine> <name>"

// validName fits an Incus instance name and a MagicDNS label at once.
var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

func ParseArgs(args []string) (Spec, error) {
	fs := flag.NewFlagSet("flocc host create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	s := Spec{}
	fs.IntVar(&s.CPUs, "cpus", 4, "vCPUs")
	fs.IntVar(&s.MemoryGiB, "memory", 8, "memory in GiB")
	fs.IntVar(&s.DiskGiB, "disk", 40, "root disk in GiB")
	if err := fs.Parse(args); err != nil {
		return s, fmt.Errorf("%w\n%s", err, usage)
	}
	if fs.NArg() != 2 {
		return s, errors.New(usage)
	}
	s.Machine, s.Name = fs.Arg(0), fs.Arg(1)
	switch {
	case s.Machine == "" || strings.HasPrefix(s.Machine, "-"):
		return s, fmt.Errorf("machine %q: give an ssh destination", s.Machine)
	case !validName.MatchString(s.Name):
		return s, fmt.Errorf("name %q: use 2 to 63 lowercase letters, digits and '-', starting with a letter", s.Name)
	case s.CPUs < 1 || s.MemoryGiB < 1 || s.DiskGiB < 10:
		return s, errors.New("--cpus and --memory must be at least 1, --disk at least 10")
	}
	return s, nil
}

func Main(args []string, inst Installer) int {
	spec, err := ParseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "flocc host create:", err)
		return 2
	}
	logPath, logf, err := openLog(spec.Name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "flocc host create:", err)
		return 1
	}
	defer logf.Close()
	log := NewRedactLog(logf)
	run := host.Exec{Log: log}
	e := &Env{M: Remote{R: run, Target: spec.Machine, Opts: ControlOpts()}, Local: run,
		UI: ui.NewHuh(), Out: os.Stdout, Log: log, Spec: spec, Sleep: time.Sleep}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := Create(ctx, e, inst, Steps()); err != nil {
		fmt.Fprintf(os.Stderr, "\nflocc host create: %s\n\nFull log: %s\nRun the same command again to continue.\n", err, logPath)
		return 1
	}
	return 0
}

func openLog(name string) (string, *os.File, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", nil, err
	}
	dir = filepath.Join(dir, "flocc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "create-"+name+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	return path, f, err
}
```

- [ ] **Step 4: Run the create tests to verify they pass**

Run: `go test ./installer/internal/create/ -v -run 'Create|Snapshot|StepsRun|ParseArgs|Main'`
Expected: every listed test `PASS`.

- [ ] **Step 5: Write the failing cli tests**

This file uses 2b's `testDeps`, `run`, `hostFile` and `usage`, as listed under Consumes.

`installer/internal/cli/create_test.go`:

```go
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostCreateIsADesktopVerbThatChecksItsArguments(t *testing.T) {
	d, _, _ := testDeps(t)
	if code := run(context.Background(), d, []string{"host", "create", "only-one-arg"}); code != 2 {
		t.Fatal(code)
	}
}

func TestHostInstallerKnowsAHostByItsFile(t *testing.T) {
	d, _, _ := testDeps(t)
	h := hostInstaller{d: d}
	if h.HostKnown("flocc-two") {
		t.Fatal("known before host add")
	}
	path := hostFile(d, "flocc-two")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("ssh = \"root@flocc-two\"\n"), 0o600)
	if !h.HostKnown("flocc-two") {
		t.Fatal("unknown after host add")
	}
}

func TestUsageNamesHostCreate(t *testing.T) {
	if !strings.Contains(usage, "flocc host create [--cpus N] [--memory GiB] [--disk GiB] <machine> <name>") {
		t.Fatal(usage)
	}
}
```

- [ ] **Step 6: Run them to verify they fail**

Run: `go test ./installer/internal/cli/ -run 'HostCreate|HostInstaller|UsageNamesHostCreate'`
Expected: FAIL, `undefined: hostInstaller`.

- [ ] **Step 7: Write the adapter and the dispatch**

The adapter below calls 2b's `deps`, `hostFile`, `hostAdd`, `shipHost` and `loginHost` with the signatures listed under Consumes.

`installer/internal/cli/create.go`:

```go
package cli

import (
	"context"
	"os"
)

// hostInstaller gives host create the desktop verbs it runs once the VM is on the tailnet.
type hostInstaller struct{ d *deps }

func (h hostInstaller) HostKnown(name string) bool {
	_, err := os.Stat(hostFile(h.d, name))
	return err == nil
}

func (h hostInstaller) AddHost(ctx context.Context, name, ssh string) error {
	return hostAdd(ctx, h.d, name, ssh)
}

func (h hostInstaller) Ship(ctx context.Context, name string) error { return shipHost(ctx, h.d, name) }

func (h hostInstaller) Login(ctx context.Context, name string) error {
	return loginHost(ctx, h.d, name)
}
```

The dispatch point is 2b's `hostCmd` in `installer/internal/cli/host.go`. In its `switch` on the sub-verb `args[0]`, beside `case "run":` and `case "add", "import":`, add:

```go
	case "create":
		return create.Main(args[1:], hostInstaller{d: d})
```

with `"github.com/NovusEdge/flocc/installer/internal/create"` added to the imports. `hostCmd` gets the line after `host`, so `args[1:]` is exactly `[flags] <machine> <name>`. Its `name` parameter (the `-H` value) is not used by create: the new host's name is a positional argument.

In `installer/internal/cli/cli.go`, add to 2b's `usage` constant, under the `flocc host import -H <name> [--ssh DEST | --daytona SANDBOX]` line:

```
  flocc host create [--cpus N] [--memory GiB] [--disk GiB] <machine> <name>
```

- [ ] **Step 8: Run all Go tests**

Run: `go test ./... && go vet ./... && test -z "$(gofmt -l installer deploy)"`
Expected: every package `ok`, no vet or gofmt output.

- [ ] **Step 9: Commit**

```bash
git add installer/internal/create installer/internal/cli
git commit -s -m "flocc host create: make a host as an Incus VM, then host add, ship, login and snapshot"
```

---

### Task 8: Remove the recipe wrappers, document create, release 0.6.0

**Depends on:** Task 7.

**Files:**
- Modify: `justfile`
- Create: `tests/test_just_wrappers_removed.py`
- Modify: `docs/vm-setup.md` (replace the section `## A VM on an Incus machine` that Part 1 wrote; fix leftover `just` lines anywhere in it)
- Modify: any other tracked file the new test names (expected: none after 2b, but `deploy/agent-plugins.txt` and Go or Python strings that say `just ship` or `just login` are the likely ones)
- Modify: `CHANGELOG.md`, `pyproject.toml`, `plugin/.claude-plugin/plugin.json`, `uv.lock`

**Interfaces:**
- Consumes: the flags and messages of Tasks 2–7; Part 2b's wrapper recipes, each a one-line body running `build/flocc …` or `flocc …`.
- Produces: release 0.6.0.

- [ ] **Step 1: Write the failing test**

`tests/test_just_wrappers_removed.py`:

```python
import re
import subprocess
from pathlib import Path

ROOT = Path(__file__).parent.parent
HOST_VERBS = ("ship", "login", "keys", "host", "doctor", "status", "fleet-status")
VERBS = (*HOST_VERBS, "logs", "attach", "tick", "release", "daytona-start")
CALL = re.compile(r"\bjust (" + "|".join(re.escape(v) for v in VERBS) + r")\b")
# History and dated documents keep the commands they were written with.
KEEP = ("CHANGELOG.md", ".docket/", "docs/superpowers/", "docs/research/")


def output(*argv: str) -> str:
    return subprocess.run(argv, cwd=ROOT, capture_output=True, text=True, check=True).stdout


def tracked() -> list[str]:
    return [f for f in output("git", "ls-files").splitlines() if not f.startswith(KEEP)]


def test_justfile_has_no_wrappers_for_flocc_verbs():
    assert set(output("just", "--summary").split()) & set(VERBS) == set()


def test_nothing_tells_the_reader_to_run_a_removed_recipe():
    hits = [
        f"{name}:{n}: {line.strip()}"
        for name in tracked()
        if (ROOT / name).is_file()
        for n, line in enumerate((ROOT / name).read_text(errors="ignore").splitlines(), 1)
        if CALL.search(line)
    ]
    assert hits == []
```

- [ ] **Step 2: Run it to verify it fails**

Run: `uv run pytest tests/test_just_wrappers_removed.py -q`
Expected: FAIL: `just --summary` still lists the wrapper recipes, and the second test lists every `just ship`/`just login`/… line in docs and code.

- [ ] **Step 3: Delete the wrappers and fix every hit**

In `justfile`, delete every recipe whose body runs the desktop binary (`build/flocc …` or `flocc …`): after Part 2b these are the `host` group (`ship`, `login`, `keys`, `host`, `doctor`, `status`, `fleet-status`, `logs`, `attach`, `tick`, `release`) and `daytona-start`. Delete the `runlog` variable and its comment if no remaining recipe uses it. Keep `default`, the `dev` group, the `build` group and `daytona-up`, `daytona-stop`, `daytona-list` (they wrap the Daytona CLI, not flocc).

Before deleting each recipe, note the `flocc` command line its body ran. Then, for every hit `test_nothing_tells_the_reader_to_run_a_removed_recipe` prints, replace `just <recipe> <args>` with that command line (for example `just ship root@flocc-nimble` becomes the line 2b's `ship` recipe ran, `flocc ship -H flocc-nimble`). Where the hit is a Go or Python string pinned by a test, change the test's expected string in the same edit. `just check`, `just build`, `just fmt` and the other dev recipes stay as they are. The test scans tracked files only: the owner's `~/.claude/CLAUDE.md` and issue NF-215 also name `just release` and the other recipes, and Task 9 Step 9 updates them.

- [ ] **Step 4: Replace the Incus section of `docs/vm-setup.md`**

Replace the whole section `## A VM on an Incus machine` (from its heading to the line before the next `## ` heading) with:

````markdown
## Making a host with flocc host create

`flocc host create` makes a flocc host as an Incus VM on a Linux machine you reach over SSH with
your own account, such as a laptop on the tailnet:

```bash
flocc host create [--cpus 4] [--memory 8] [--disk 40] <machine> <name>
```

`<name>` becomes the Incus instance, the Tailscale name and the flocc host name. Memory and disk
are in GiB; size a host like the existing one (`incus config show <vm> --expanded`). On the
machine your account needs `incus-admin` membership or sudo, and when firewalld or Docker runs
there, passwordless sudo: create runs every root command as `sudo -n` and stops with instructions
rather than wait for a password. This desktop must be on the tailnet.

Create checks each step before changing anything, so running the same command again continues a
host that stopped half-made. In order, it:

1. checks that Incus has the managed bridge `incusbr0`, a root pool with room for the disk, and the
   memory free, and otherwise stops with the commands to run (it never sets Incus up itself);
2. scopes firewalld, when it runs (below);
3. lets the VM through Docker's forward drop, when Docker runs or is installed and stopped (below);
4. checks that no tailnet node already has `<name>`, launches `images:debian/13` as a VM with
   `boot.autostart=true`, and waits for an IPv4 address and outbound HTTPS;
5. joins the VM to the tailnet with Tailscale SSH: scan the QR code or open the link, or paste a
   one-off auth key, which reaches the VM on stdin and is neither stored nor logged;
6. waits until this desktop's tailnet lists the node, runs `flocc host add <name> --ssh
   root@<name>` unless `hosts/<name>.toml` exists, then `flocc ship -H <name>` and
   `flocc login -H <name>`;
7. takes the Incus snapshot `installed`, replacing an older one only after the new one exists;
   `incus snapshot restore <name> installed` returns the VM to it.

The snapshot holds the host's tokens and Claude login as of that run, so after a restore run
`flocc ship -H <name>` again if keys were rotated since. On a `dir` storage pool a VM snapshot is a
full copy of the root image, which the Incus step's room check does not count.

The first SSH connection to the machine runs on your terminal, so a Tailscale SSH check-mode
re-auth URL shows there, and the approval covers the rest of the run. The log is
`~/.cache/flocc/create-<name>.log`.

### What create changes on the machine

The machine is shared with other work, so create adds rules scoped to `incusbr0` and never
restarts Docker: on 2026-10-20 a Docker restart killed another session's image build.

| What | Where | Effect |
|---|---|---|
| zone `flocc-incus` | firewalld | `incusbr0` moves into it. It allows only `dhcp` and `dns` (Incus's dnsmasq), so the VM reaches none of the machine's own services. |
| policy `flocc-incus-out` | firewalld | Forwards `flocc-incus` to the zone of the default route's interface. |
| policy `flocc-no-docker` | firewalld, when Docker made its `docker` zone | Rejects `flocc-incus` to `docker`, ahead of Docker's `docker-forwarding` policy, which accepts forwarding from every zone into Docker's bridges and published ports. |
| `flocc-docker-user.service` | `/etc/systemd/system/` | A oneshot with `After=` and `PartOf=docker.service` that adds `-i incusbr0 -j ACCEPT` and `-o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT` to `DOCKER-USER` after every Docker start (ip6tables too, where Docker made that chain). With Docker stopped it is enabled but not started, and runs at Docker's next start. |

A new zone or policy needs one `firewall-cmd --reload`, which create runs whenever the runtime or
the permanent configuration differs from the wanted one. A reload drops every firewalld change that
was never made permanent, whoever made it, so on a machine other people use, compare
`firewall-cmd --list-all-zones` with the same command under `--permanent` first and look at any
runtime-only entry other than Docker's bridge bindings. Docker with its nftables firewall backend
has no `DOCKER-USER` chain, so create stops there. Without firewalld nothing keeps the VM off the
machine's services, and create says so.

firewalld forwards by zone, so the VM can also reach any other interface in the uplink's zone. That
includes `tailscale0` when it is in that zone: traffic forwarded out through it is masqueraded as
the machine, and create's Firewalld line says when this holds. If the default route moves to an
interface in another zone (Wi-Fi to Ethernet), run create again to move the policy.

The new zone has no intra-zone forwarding, so with `br_netfilter` loaded (Docker loads it) two VMs
on `incusbr0` cannot reach each other.

To take it all off a machine (the VMs on `incusbr0` then have no way out until the bridge gets
another zone):

```bash
sudo systemctl disable --now flocc-docker-user.service
sudo rm /etc/systemd/system/flocc-docker-user.service && sudo systemctl daemon-reload
sudo firewall-cmd --permanent --delete-policy=flocc-no-docker
sudo firewall-cmd --permanent --delete-policy=flocc-incus-out
sudo firewall-cmd --permanent --zone=flocc-incus --remove-interface=incusbr0
sudo firewall-cmd --permanent --delete-zone=flocc-incus
sudo firewall-cmd --reload
```

### The VM on the tailnet

The VM joins the tailnet as you, untagged, with Tailscale SSH on. Under the default policy it can
therefore reach every device on the tailnet and, through the machine's uplink, the LAN. The rules
above keep it off the machine itself and off Docker's containers, not off the rest of the network.
Closing that means tagging the node with a policy that denies its outbound tailnet traffic, which
changes how hosts log in; that decision is open. After the join, disable key expiry for `<name>` at
https://login.tailscale.com/admin/machines (create prints this reminder).
````

- [ ] **Step 5: Release notes and version**

Set `version = "0.6.0"` in `pyproject.toml` and `"version": "0.6.0"` in `plugin/.claude-plugin/plugin.json`, then run `uv lock`. Add at the top of the releases in `CHANGELOG.md`, dated the day the release PR is cut (`date +%F`):

```markdown
## 0.6.0 (YYYY-MM-DD from date +%F)

`flocc host create`, and the end of the `just` host recipes.

- `flocc host create <machine> <name>` makes a host as an Incus VM, over SSH as your own account:
  it checks Incus, puts `incusbr0` in a firewalld zone that allows only DHCP and DNS with a forward
  policy to the uplink, lets the VM through Docker's `DOCKER-USER` chain from a oneshot unit tied to
  `docker.service` (Docker is never restarted), launches Debian 13 with autostart, joins Tailscale
  by QR code or a one-off key on stdin, runs `host add`, `ship` and `login`, and snapshots the VM
  as `installed`. Running it again continues a half-made host.
- The `just` recipes that wrapped `flocc` are gone; run `flocc` itself. `just` keeps the dev, build
  and Daytona recipes.
- zer0-day-nimble's broad firewall settings (`incusbr0` in zone `trusted`, `ip-forward-no-drop`,
  FORWARD ACCEPT) are replaced by the scoped ones.
```

Write the real date in the heading, for example `## 0.6.0 (2026-10-27)`; `test_the_plugin_and_the_package_share_one_version` checks only `## 0.6.0 `.

- [ ] **Step 6: Run everything**

Run: `just check`
Expected: lint clean; `uv run pytest -q` passes, including `tests/test_just_wrappers_removed.py` and `tests/test_plugin.py::test_the_plugin_and_the_package_share_one_version`; `go test ./...` passes. `just --list` shows only the dev, build and Daytona recipes.

- [ ] **Step 7: Commit**

```bash
git add -A justfile tests/test_just_wrappers_removed.py docs/vm-setup.md CHANGELOG.md pyproject.toml plugin/.claude-plugin/plugin.json uv.lock
git add -u
git commit -s -m "Remove the just wrappers, document host create, release 0.6.0"
```

---

### Task 9 (controller, manual): revert the laptop's broad settings and run one create

**Depends on:** Task 8 committed on the branch. Not for an implementer subagent: every step runs on
the shared laptop zer0-day-nimble or on the owner's accounts.

The laptop carries, from 2026-10-19/20: `incusbr0` in firewalld zone `trusted`,
`"ip-forward-no-drop": true` in `/etc/docker/daemon.json`, and the iptables `FORWARD` policy set to
`ACCEPT` by hand. The order below keeps the existing VM (flocc-nimble, or la-nimble if it still
runs) online: the scoped rules go in first (by create), and each broad setting is removed only
after an egress check passes, with its rollback beside it. Docker is never restarted; the
`daemon.json` change takes effect at Docker's next start, at the laptop's next boot.

- [ ] **Step 1: Ask before touching the laptop**

Tell the owner this task changes the laptop's firewall and runs one `firewall-cmd --reload`, which
drops every runtime-only firewalld change on the machine (Step 2 lists them first), and that the
Step 4 isolation probe creates and removes one Docker network, `flocc-probe`. Ask:

(a) Whether their account on zer0-day-nimble has passwordless sudo, or may get a temporary
`/etc/sudoers.d/flocc-create` for this run. Only the owner can write that file, because it needs
their password: give them this line for `sudo visudo -f /etc/sudoers.d/flocc-create`, with `USER`
replaced and the paths as `command -v` shows them on the laptop:

```
USER ALL=(root) NOPASSWD: /usr/bin/true, /usr/bin/firewall-cmd, /usr/bin/iptables, /usr/bin/ip6tables, /usr/bin/docker info *, /usr/bin/docker ps *, /usr/bin/docker network create flocc-probe, /usr/bin/docker network rm flocc-probe, /usr/bin/tee /etc/systemd/system/flocc-docker-user.service, /usr/bin/systemctl daemon-reload, /usr/bin/systemctl enable flocc-docker-user.service, /usr/bin/systemctl restart flocc-docker-user.service
```

`true` is the Probe's `sudo -n true`. The line does not cover Step 6's `daemon.json` swap or its
`nmcli` change: the owner runs those two themselves (`! ssh -t zer0-day-nimble ...`), or widens the
line for the run.

(b) Which repo the trial host `flocc-trial` maps: one scratch repo both GitHub tokens can read.
Step 3 sets `shared_team` for the trial host, so it skips issues for repos it does not map
instead of commenting on them; it would still claim a `claude` issue on the scratch repo itself,
so pick a repo no open issue targets.

Check that no build runs there, then announce to the sessions working on the laptop (and tell them
about the `flocc-probe` network):

```bash
ssh zer0-day-nimble "pgrep -af 'docker(-buildx)? (build|buildx|compose)|buildkitd|buildctl|just ' | grep -v pgrep"
```

and `ListAgents` for sessions on the laptop. `containerd`, `containerd-shim` and `docker-proxy`
always run, so the pattern names builds only; the remote shell's own command line contains the
pattern, which the `grep -v` drops. Wait while a build runs.

- [ ] **Step 2: Record the baseline (read-only)**

```bash
ssh zer0-day-nimble 'sudo -n true && echo sudo ok
  sudo -n docker info --format "{{json .FirewallBackend}}"
  grep -n firewall-backend /etc/docker/daemon.json
  sudo -n firewall-cmd --get-zone-of-interface=incusbr0
  sudo -n firewall-cmd --permanent --get-zone-of-interface=incusbr0
  sudo -n iptables -S FORWARD | head -1
  sudo -n ip6tables -S FORWARD | head -1
  sudo -n iptables -S DOCKER-USER
  sudo -n ip6tables -S DOCKER-USER
  grep -n ip-forward-no-drop /etc/docker/daemon.json
  ip -j route show default
  incus network get incusbr0 ipv4.address
  incus list -c ns4 -f csv
  incus network list -f csv
  ip -br link show type bridge
  tailscale debug prefs | grep -A3 AdvertiseRoutes
  nmcli -g GENERAL.CONNECTION device show incusbr0 2>/dev/null'
```

Expected: `sudo ok`; a backend starting `iptables`, with no `firewall-backend` line or one that says
`iptables` (create reads that key, so a line that says `nftables` stops create); `trusted` twice;
`-P FORWARD ACCEPT`; `DOCKER-USER` present with no `incusbr0` rules; the `ip-forward-no-drop` line.
Write the output down; it is the rollback reference. If the backend is `nftables`, stop and tell
the owner: create does not support it. If `nmcli` names a connection for `incusbr0`, note it for
Step 6. The last three queries before it list the other things that forward through the laptop
(other Incus networks, `virbr0` and podman bridges, advertised Tailscale routes), for Step 5.

Then list what the reload will discard. It drops every runtime-only change in firewalld, including
changes other sessions made without `--permanent`; Docker re-adds its own bindings, which Step 4
checks.

```bash
for k in zones policies; do
  diff <(ssh zer0-day-nimble sudo -n firewall-cmd --list-all-$k) \
       <(ssh zer0-day-nimble sudo -n firewall-cmd --permanent --list-all-$k) \
    && echo "$k: runtime matches permanent"
done
```

Expected differences: `incusbr0` and Docker's bridges (`docker0`, `br-*`) bound at runtime only.
Show the owner every other difference, and get a yes before Step 3 for each. Do not continue on
silence.

Then the egress probe, used after every change below (`VM` is each running VM from `incus list`):

```bash
ssh zer0-day-nimble 'for vm in $(incus list -c n -f csv status=running); do
  printf "%s v4 " "$vm"; incus exec "$vm" -- curl -4 -fsS -o /dev/null --max-time 10 https://api.github.com && echo ok || echo FAIL
  printf "%s v6 " "$vm"; incus exec "$vm" -- curl -6 -fsS -o /dev/null --max-time 10 https://deb.debian.org && echo ok || echo FAIL
done'
```

Expected: `v4 ok` for every VM. Note the v6 result as the baseline; v6 is expected only where it
was ok before.

- [ ] **Step 3: Run create for the trial host**

Seed the host file first, so the shared-team answer is on disk before any ship starts the host's
timer:

```bash
go build -o build/flocc ./installer/cmd/flocc
build/flocc host add flocc-trial --ssh root@flocc-trial
grep shared_team ~/.config/flocc/hosts/flocc-trial.toml
```

Map only the scratch repo from Step 1 and answer yes to "Do other hosts handle the unticked
repos?". The grep must print `shared_team = true`, which renders `skip_unmapped` on the host. (`host
add` reads the keys, GitHub and Linear, never the host, so it works before the VM exists.) Create
finds the file and skips its own `host add`. Then:

```bash
build/flocc host create --cpus 2 --memory 4 --disk 20 zer0-day-nimble flocc-trial
```

The trial host is smaller than flocc-nimble on purpose: VM memory is not reserved, and a full-size
trial beside flocc-nimble competes with the other sessions' builds on the laptop.

Expected: `✓ Incus`, then `• Firewalld …runtime: no zone flocc-incus…` (`fwProblems` names the
missing zone first) followed by `✓ Firewalld incusbr0 in zone flocc-incus (dhcp, dns), forwarded
only to zone <zone>`, `✓ Docker DOCKER-USER lets incusbr0 through (iptables backend)`, the VM,
Egress and Tailscale steps (scan the QR code, or paste a one-off key from
https://login.tailscale.com/admin/settings/keys), then `ship`, `login`, and `✓ Snapshot
flocc-trial/installed`. If it stops, read `~/.cache/flocc/create-flocc-trial.log`, fix, and run the
same command again; it continues where it stopped.

As soon as `login` passes, stop the trial host's timer in a second terminal, while create takes the
snapshot (ship leaves the timer off until login finishes, and login's last step starts it):

```bash
build/flocc host run -H flocc-trial -- systemctl --user stop flocc.timer
build/flocc host run -H flocc-trial -- systemctl --user is-active flocc.timer
```

Expected: the second command prints `inactive`.

Right after the Firewalld step has passed (create may still be running), run the egress probe from
Step 2 in a second terminal. If any VM's v4 is `FAIL`, press Ctrl-C in the terminal running create
(it is still in the Docker and VM steps and must not go on), then roll the zone back and stop:

```bash
ssh zer0-day-nimble 'sudo -n firewall-cmd --zone=trusted --change-interface=incusbr0'
```

(runtime only; the permanent configuration still holds the new zone for the next try).

- [ ] **Step 4: Check isolation and Docker after the reload**

```bash
ssh zer0-day-nimble 'gw=$(incus network get incusbr0 ipv4.address | cut -d/ -f1)
  sudo -n firewall-cmd --get-zones | tr " " "\n" | grep -x docker
  sudo -n iptables -S DOCKER-FORWARD | head -3
  sudo -n iptables -S DOCKER-USER
  sudo -n firewall-cmd --info-policy=flocc-incus-out
  sudo -n firewall-cmd --info-policy=flocc-no-docker
  incus exec flocc-trial -- getent hosts deb.debian.org
  published=$(sudo -n docker ps --format "{{.Ports}}" | grep -o "0.0.0.0:[0-9]*" | cut -d: -f2 | sort -un | head -2)
  echo "published ports: ${published:-none}"
  for p in $(ss -Hltn | awk "{print \$4}" | sed "s/.*://" | sort -un | grep -vx 53 | head -5) $published; do
    incus exec flocc-trial -- curl -s -o /dev/null --max-time 5 "http://$gw:$p"; rc=$?
    case $rc in 7|28) echo "port $p: blocked (curl $rc)";; *) echo "port $p: REACHED (curl $rc)";; esac
  done
  sudo -n docker network create flocc-probe >/dev/null && sudo -n iptables -S DOCKER-USER | grep -c incusbr0; sudo -n docker network rm flocc-probe >/dev/null'
```

Only curl exit 7 (refused, which firewalld's REJECT gives) and 28 (timed out, which a DROP gives)
count as blocked. Any other exit means the port answered, including a non-HTTP service that makes
curl fail with 1, 8, 52 or 56.

Expected: `docker` zone present and `DOCKER-FORWARD` rules present (Docker re-added its rules after
the reload); `DOCKER-USER` holds both `incusbr0` rules; the two policies show the zones, targets
and (for `flocc-no-docker`) a negative priority; `getent` resolves; every port `blocked` (53 is
left out: DNS is the one service the zone allows); the last line is `2` (creating a Docker network
leaves `DOCKER-USER` alone, which the spec lists as unconfirmed). Any `REACHED` port: stop, roll
back as in Step 3, and report it.

If `published ports: none`, no container publishes a port, so the part of the isolation that
`flocc-no-docker` exists for was not exercised: record Docker's published ports as untested in
Step 8. To test it, ask the owner first, then run one throwaway container that publishes a port
and rerun the probe; that adds a `docker run` to the sudoers line.

- [ ] **Step 5: Remove FORWARD ACCEPT**

Only if Step 2 showed `-P FORWARD ACCEPT`. `FORWARD DROP` cuts forwarding for everything that
Docker's rules and the new `DOCKER-USER` rules do not accept, and the egress probe covers Incus
instances only. Show the owner Step 2's other Incus networks, bridges (`virbr0`, podman) and
advertised Tailscale routes, ask whether anything else forwards through the laptop, and continue
only on a no.

```bash
ssh zer0-day-nimble 'sudo -n iptables -P FORWARD DROP'
```

and, only if Step 2 showed `-P FORWARD ACCEPT` for ip6tables and an ip6tables `DOCKER-USER` chain:

```bash
ssh zer0-day-nimble 'sudo -n ip6tables -P FORWARD DROP'
```

Run the egress probe. Expected: every VM `v4 ok`, v6 as in the baseline. Rollback on a `FAIL`:
`sudo -n iptables -P FORWARD ACCEPT` (and the ip6tables one), then stop and report.

- [ ] **Step 6: Remove ip-forward-no-drop and the trusted binding**

The file swap below and the `nmcli` change are outside Step 1's sudoers line: the owner runs them
(`! ssh -t zer0-day-nimble '...'`), or has widened the line for this run.

```bash
ssh zer0-day-nimble 'command -v jq >/dev/null || { echo "install jq first"; exit 1; }
  sudo -n cp -p /etc/docker/daemon.json /etc/docker/daemon.json.pre-flocc &&
  sudo -n jq "del(.\"ip-forward-no-drop\")" /etc/docker/daemon.json.pre-flocc | sudo -n tee /etc/docker/daemon.json.new >/dev/null &&
  sudo -n jq -e . /etc/docker/daemon.json.new >/dev/null &&
  sudo -n mv /etc/docker/daemon.json.new /etc/docker/daemon.json &&
  sudo -n chmod --reference=/etc/docker/daemon.json.pre-flocc /etc/docker/daemon.json &&
  cat /etc/docker/daemon.json'
```

Expected: the file printed without `ip-forward-no-drop`. Do not restart Docker. A daemon.json that
fails to parse would stop Docker at the next boot, which is why the new file is checked with
`jq -e` before it replaces the old one; the old one stays as `daemon.json.pre-flocc`.

Then confirm the `trusted` binding is gone from both configurations (create moved it):

```bash
ssh zer0-day-nimble 'sudo -n firewall-cmd --zone=trusted --list-interfaces; sudo -n firewall-cmd --permanent --zone=trusted --list-interfaces'
```

Expected: neither line lists `incusbr0`. If Step 2's `nmcli` named a connection for `incusbr0`,
also run `nmcli -g connection.zone connection show <connection>`; if it prints `trusted`, run
`sudo -n nmcli connection modify <connection> connection.zone flocc-incus` so NetworkManager does
not put it back.

Run the egress probe once more, then `build/flocc -H flocc-nimble doctor`. Expected: every VM
`v4 ok`; doctor with no FAIL line.

- [ ] **Step 7: Tear down the trial host**

```bash
ssh zer0-day-nimble 'incus delete --force flocc-trial'
rm ~/.config/flocc/hosts/flocc-trial.toml
```

Remove the `flocc-trial` node at https://login.tailscale.com/admin/machines. Remove the temporary
sudoers file if Step 1 added one: `ssh zer0-day-nimble sudo rm /etc/sudoers.d/flocc-create`.

`host add` created a Linear label for the scratch repo, and `claude`, `escalation` and
`needs-decision` if they were missing; those three stay. Remove the scratch repo's label only if no
other host maps the repo (`grep -l <repo> ~/.config/flocc/hosts/*.toml` prints nothing) and no issue
carries the label: ask the owner, then retire it with the linear-nimblefox label tools. Do not
touch any label an issue with `claude` already uses.

- [ ] **Step 8: Record the run**

Append to the `### What create changes on the machine` section of `docs/vm-setup.md`:

```markdown
Run on zer0-day-nimble on <date>: Incus <incus version>, firewalld <firewall-cmd --version>,
Docker <docker version --format '{{.Server.Version}}'> (<backend>). The uplink was <dev> in zone
<zone>. After create the VMs kept IPv4 egress (IPv6: <result>), the trial VM reached none of the
laptop's listening ports (Docker's published ports: <reached none | untested, no container
published a port>), Docker re-added its own rules after the firewalld reload, and
`docker network create` left `DOCKER-USER` alone. FORWARD went back to DROP and
`ip-forward-no-drop` left `daemon.json` without a Docker restart; the first boot after that still
needs the check below.
```

with the real values, plus the check for the next boot:

```markdown
After the machine's next boot: `systemctl is-active flocc-docker-user.service` shows `active`,
`sudo iptables -S DOCKER-USER` holds both `incusbr0` rules, and every VM still reaches
https://api.github.com. `sudo iptables -S FORWARD | head -1` may show either policy: Docker sets
`DROP` only when it enables forwarding itself, and Incus may enable it first at boot.
```

Commit with `git commit -s -m "Record host create on zer0-day-nimble"`. Tell the owner the next-boot
check is pending until they reboot the laptop.

- [ ] **Step 9: Update the instructions outside the repo that name the removed recipes**

Task 8's test scans tracked files only. Two places outside the repo still tell sessions to run
`just release` and the other wrappers; update both once Task 8 is merged, using the command line
each removed recipe ran (`git show <Task 8 commit>^:justfile`; for `release` it is
`build/flocc -H "$t" release "$@"`, so `just release root@la-nimble NF-…` becomes
`flocc -H la-nimble release NF-…`).

1. The owner's `~/.claude/CLAUDE.md` (the linear-agents section names `just release` and
   `just release root@la-nimble NF-…`). Find the lines with
   `grep -n 'just \(ship\|login\|keys\|host\|doctor\|status\|fleet-status\|logs\|attach\|tick\|release\|daytona-start\)' ~/.claude/CLAUDE.md`,
   show the owner the replacement lines, and edit with the Edit tool after they agree. It is their
   global file.
2. The NF-215 teammate how-to in Linear. Read it with the linear-nimblefox `get_issue`, replace each
   removed recipe with its `flocc` command line, and save it with `save_issue`. If NF-215 carries
   the `claude` label, ask the owner first.

Then `grep -rn 'just \(ship\|login\|release\)' ~/.claude --include=*.md` for other copies, and
list any further hit to the owner rather than editing it.
