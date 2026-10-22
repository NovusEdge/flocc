# Create Root Pass Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `flocc host create` runs its firewalld and Docker steps as root on the machine in one `ssh -t ... sudo flocc-setup --phase create-root`, so a password on sudo no longer stops it.

**Architecture:** The two steps move to `internal/steps` and run inside `flocc-setup` as root. A new desktop package `internal/rootpass` holds the build, stage, `ssh -t` and report pieces moved out of `cardinal/passes.go`; cardinal and create both call it. In create, one `Root` step replaces `Firewalld{}` and `Docker{}`.

**Tech Stack:** Go 1.26 (`installer/`), `host.Fake` for every test.

**Spec:** `docs/superpowers/specs/2026-10-22-create-root-pass-design.md`

## Global Constraints

- Work in `~/Projects/Personal/worktrees/flocc-create-root` on branch `feat/create-root`. Set `GOTMPDIR=~/.cache/go-tmp` (create it with `mkdir -p` once) for every `go` command; `/tmp` is nearly full.
- Never run `flocc host create`, `cardinal setup`, `setup` or `ship` for real, and never ssh to any machine. Tests only.
- Do not touch `installer/internal/ui/` or `installer/internal/dash/`; another branch owns them.
- Commit with `git commit -s`. No `Co-Authored-By` trailer, no AI attribution anywhere.
- Read files with Read; change them with Edit or Write, never `sed -i`, `cat >` or heredocs. Grep and git stay on the shell.
- Comments carry only facts the code cannot show. Move existing comments with their code; delete one that no longer holds rather than appending to it.
- Grep before writing any helper; reuse what exists (`steps.Env.Ok/Output/Must`, `host.Must`, `saved.Sanitize`, `ship.ArchFor`, `ship.LineTap`).
- `docker.service` is never restarted, in code or tests. Keep the existing test that asserts it (`dockerUntouched` in `create_test.go`) working where the Docker tests land.
- Use Monitor or `run_in_background` for any wait; never a `sleep` loop.

## Review Focus

- Ctrl-C during the pass: flocc-setup's context ends inside the firewalld rebind wait and the report carries the error; the desktop fails the Root pass row. Under a cancelled context `host.Exec` fails every command, so `systemctl is-active` reads as "not running" and a Check reads as done: the old rebind loop returned nil there, and `RunAll` could end with no error. Task 1 keeps `TestFirewalldApplyWaitsForDockerToRebindItsBridges`-style coverage and adds a cancelled-context case run through a runner that fails once the context ends; Task 2 reports `ctx.Err()` with no steps.
- A report whose Firewalld or Docker step is not done but whose `error` is empty must fail create, naming the step. Task 4 tests it.
- No TTY with a password on sudo must fail before any build or staging, with the "run create in a terminal" message. Task 4 tests it.
- A machine with neither firewalld nor Docker must never see `ssh -t`, `mktemp` or `scp`. Task 4 tests it.
- The handoff banner (`host.Cmd.Why`) names the machine and never the remote command line. Tasks 3 and 4 test it.

---

### Task 1: Firewalld and Docker steps run on the machine

**Files:**
- Create: `installer/internal/steps/create_firewalld.go` (from `installer/internal/create/firewalld.go`)
- Create: `installer/internal/steps/create_docker.go` (from `installer/internal/create/docker.go`)
- Create: `installer/internal/steps/create_root.go`
- Create: `installer/internal/steps/create_firewalld_test.go`, `installer/internal/steps/create_docker_test.go` (from the create tests)
- Modify: `installer/internal/steps/cardinal_root.go:20-31` (stage names), `installer/internal/steps/cardinal_root_test.go:190-226`, `installer/internal/cardinal/passes.go:391` and `installer/cmd/flocc-setup/cardinal.go:97` (the renamed stage helpers)
- Modify: `installer/internal/create/create.go:21-24` (`Bridge = steps.IncusBridge`)
- Leave `installer/internal/create/firewalld.go`, `docker.go` and their tests in place; Task 4 deletes them.

**Interfaces:**
- Produces: `steps.IncusBridge = "incusbr0"`; `type steps.Firewalld struct{}`, `type steps.Docker struct{}` (both `steps.Step`, names "Firewalld" and "Docker"); `func steps.CreateRootSteps() []steps.Step`; `func steps.CreateRootIdle(ctx context.Context, e *steps.Env) (idle bool, detail string)`; `func steps.ValidRootStage(dir string) bool`; `func steps.CheckRootStage(dir string, sudoUID int, files ...string) error`.

- [ ] **Step 1: Move the tests first.** Copy `create/firewalld_test.go` and `create/docker_test.go` into `steps/` as `create_firewalld_test.go` and `create_docker_test.go`, package `steps`. Rewrite each fake key without the `sudo -n ` prefix (`"sudo -n firewall-cmd --get-zones"` becomes `"firewall-cmd --get-zones"`, `"sudo -n iptables -w -C ..."` becomes `"iptables -w -C ..."`). Build the env with the package's `testEnv(t, f)` (`root_test.go:16`), which sets `Root` to a temp dir and `Sleep` to a no-op; it returns one `*Env`, where create's returns `(e, u)`.
  - Files: a fake of `sudo -n cat <file>` becomes a file under `e.Root` (`os.MkdirAll(e.Path(filepath.Dir(p)), 0o755); os.WriteFile(e.Path(p), ...)`), and a missing one (`TestDockerWithoutDaemonJSONUsesIptables`) is no file. The file needs the env, so `dockerMachine(daemon, installed)` becomes `dockerMachine(t, daemon, installed) (*host.Fake, *Env)` and writes `daemonJSON` and, when installed, the unit; `TestFirewalldStopsOnTheIptablesBackend` writes `firewalldConf`. An assertion that `sudo -n tee` ran becomes a read of `e.Path(dockerUnitPath)` equal to `dockerUnit()`; one that it did not run becomes `os.Stat` of that path failing. `TestDockerApplyInstallsTheUnitAndNeverRestartsDocker`'s last calls are the three `systemctl` ones, after it checks the file.
  - Drop the tests whose subject is gone: `TestFirewalldNeedsPasswordlessSudo`, `TestDockerNeedsPasswordlessSudo`, `TestEveryRootCommandIsNonInteractive`, `TestDockerUnitIsWrittenFromStdin`, `TestDockerReadsItsFilesAsRoot`, and the `rootCalls` loop at the end of `TestFirewalldApplyWritesPermanentThenReloadsOnce`. Drop the `e.sudo = false` lines in the "skipped when not running" tests. `TestNeedRootNamesTheFix` is in `create_test.go`; Task 4 deletes it.
  - Copy `isDocker`, `touchesDocker` and `dockerUntouched` from `create/create_test.go:69-167` into `create_docker_test.go`. Leave the originals: `vm_test.go` and `tailscale_test.go` still call `dockerUntouched`, and `TestTouchesDocker` stays with them.
- [ ] Add a cancelled-context test:

```go
// ctxRunner fails every command once ctx has ended, as host.Exec does; host.Fake ignores ctx.
type ctxRunner struct{ *host.Fake }

func (r ctxRunner) Run(ctx context.Context, c host.Cmd) (host.Result, error) {
	if err := ctx.Err(); err != nil {
		return host.Result{}, err
	}
	return r.Fake.Run(ctx, c)
}

func TestFirewalldApplyStopsWaitingWhenCancelled(t *testing.T) {
	// docker0 stays out of zone docker, so the rebind wait would run all its tries.
	f := fwDockerMachine("public", "docker").On("firewall-cmd --reload", 0, "success\n")
	e := testEnv(t, f)
	e.Run = ctxRunner{f}
	ctx, cancel := context.WithCancel(context.Background())
	e.Sleep = func(time.Duration) { cancel() }
	if err := (Firewalld{}).Apply(ctx, e); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
```

  Against the loop moved verbatim it fails: after the sleep, `dockerBridgeProblem`'s `systemctl is-active` fails, reads as Docker stopped, and Apply returns nil. Step 3 changes the loop.
- [ ] Add `CreateRootIdle` tests:

```go
func TestCreateRootIdleOnlyWithoutFirewalldAndDocker(t *testing.T) {
	idle := host.NewFake() // a key with no response exits 127, so every systemctl query fails
	if ok, detail := CreateRootIdle(context.Background(), testEnv(t, idle)); !ok ||
		!strings.Contains(detail, "firewalld not running") || !strings.Contains(detail, "Docker not running") {
		t.Fatalf("idle = %v %q", ok, detail)
	}
	for _, unit := range []string{
		"systemctl is-active --quiet firewalld.service",
		"systemctl is-active --quiet docker.service",
		"systemctl is-enabled --quiet docker.service",
		"systemctl is-active --quiet docker.socket",
	} {
		if ok, _ := CreateRootIdle(context.Background(), testEnv(t, host.NewFake().On(unit, 0, ""))); ok {
			t.Fatalf("idle with %s", unit)
		}
	}
}
```

- [ ] **Step 2: Run them to see them fail.** `cd installer && GOTMPDIR=~/.cache/go-tmp go test ./internal/steps/ -run 'Fw|Firewalld|Docker|CreateRoot'` — expect compile errors (undefined `Firewalld`, `Docker`, `CreateRootIdle`).
- [ ] **Step 3: Move the code.** Copy `create/firewalld.go` and `create/docker.go` into the two new files, package `steps`, and translate against `*steps.Env`:
  - `e.out(ctx, e.root(cmd("firewall-cmd", args...)))` → `e.Output(ctx, "firewall-cmd", args...)`; `e.ok(ctx, cmd(...))` → `e.Ok(ctx, ...)`; `e.must(ctx, e.root(cmd(...)))` → `e.Must(ctx, ...)`.
  - `xt(e, tool, args...)` returns `cmd(tool, append([]string{"-w"}, args...)...)` (no sudo); keep its comment about `-w`. Call sites use `e.Run.Run` / `e.MustCmd` as needed, or turn `xt` into `func xtArgs(args ...string) []string` and call `e.Ok(ctx, tool, xtArgs(...)...)`; pick whichever reads shorter.
  - `Firewalld.iptables`: `b, _ := os.ReadFile(e.Path(firewalldConf))`. `Docker.backend`: `b, _ := os.ReadFile(e.Path(daemonJSON))`; its comment becomes "a failed read means the file is absent, which is Docker's iptables default". Docker Check reads the unit with `os.ReadFile(e.Path(dockerUnitPath))` and compares `strings.TrimSpace` of it with `strings.TrimSpace(dockerUnit())`; drop the `tee` umask comment.
  - Docker Apply writes the unit with `os.WriteFile(e.Path(dockerUnitPath), []byte(dockerUnit()), 0o644)` after `os.MkdirAll(e.Path(filepath.Dir(dockerUnitPath)), 0o755)`, as `Egress.Apply` does (`cardinal_root.go:327-335`), and still only after the nftables and DOCKER-FORWARD refusals. Its systemctl calls run without sudo. Keep the comment about never restarting `docker.service`. Its local `steps` slice is legal inside package `steps` but reads badly; call it `calls`.
  - Remove the three `needRoot` calls (Firewalld's Check and Apply, Docker's Check). Replace `Bridge` with `IncusBridge`, `e.Spec.Machine` with the words "this machine" ("firewalld on this machine uses the iptables backend ...", "this machine has no default route", "cannot list the bridges on this machine", "Docker on this machine uses the nftables firewall backend ..."), and `e.Sleep` stays `e.Sleep`.
  - The rebind loop in Firewalld.Apply tests the context after each probe, before it trusts a clean answer, so the new cancelled-context test passes:

```go
	for range fwRebindTries {
		p, err := dockerBridgeProblem(ctx, e)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || p == "" {
			return err
		}
		e.Sleep(time.Second)
	}
```

  - Rename helpers only on a clash; there is none today (`grep -nw 'sameSet\|policyInfo\|zoneOf\|xt\|Docker\|Firewalld' internal/steps/*.go`). `steps.cmd` already exists (`step.go:152`) with create's signature.
- [ ] Write `create_root.go`:

```go
package steps

import (
	"context"
	"strings"
)

// IncusBridge is the Incus bridge create puts VMs on; create.Bridge is it.
const IncusBridge = "incusbr0"

func CreateRootSteps() []Step { return []Step{Firewalld{}, Docker{}} }

func firewalldRunning(ctx context.Context, e *Env) bool {
	return e.Ok(ctx, "systemctl", "is-active", "--quiet", "firewalld.service")
}

// dockerPresent: a stopped or socket-activated Docker still sets FORWARD to DROP at its next start.
func dockerPresent(ctx context.Context, e *Env) (running, present bool) {
	running = e.Ok(ctx, "systemctl", "is-active", "--quiet", "docker.service")
	return running, running || e.Ok(ctx, "systemctl", "is-enabled", "--quiet", "docker.service") ||
		e.Ok(ctx, "systemctl", "is-active", "--quiet", "docker.socket")
}

const (
	noFirewalld = "firewalld not running; nothing keeps the VM off this machine's services"
	noDocker    = "Docker not running"
)

// CreateRootIdle reports whether neither create root step has work, which needs no root: the
// desktop runs it over ssh to skip the sudo.
func CreateRootIdle(ctx context.Context, e *Env) (bool, string) {
	if _, docker := dockerPresent(ctx, e); firewalldRunning(ctx, e) || docker {
		return false, ""
	}
	return true, strings.Join([]string{noFirewalld, noDocker}, "; ")
}
```

  Firewalld.Check starts with `if !firewalldRunning(ctx, e) { return true, noFirewalld, nil }`; Docker.Check with `running, present := dockerPresent(ctx, e); if !present { return true, noDocker, nil }`. Move the "stopped or socket-activated" comment from `docker.go:97` onto `dockerPresent` and delete it from Check.
- [ ] Rename the stage helpers in `cardinal_root.go`: the regex becomes `^/tmp/flocc-(cardinal|create)\.[A-Za-z0-9]{6,}$`; `ValidCardinalStage` → `ValidRootStage`, `CheckCardinalStage` → `CheckRootStage`; its error says "the root pass runs from a /tmp/flocc-cardinal.XXXXXX or /tmp/flocc-create.XXXXXX stage, not %s". Update every caller (`grep -rn 'CardinalStage' installer`: `cardinal/passes.go:391`, `cmd/flocc-setup/cardinal.go:97`). Rename `TestCheckCardinalStage` to `TestCheckRootStage`; it builds real stages with `os.MkdirTemp("/tmp", "flocc-cardinal.")`, so add a good stage from `os.MkdirTemp("/tmp", "flocc-create.")` and a refused `/tmp/flocc-other.abcdef`.
- [ ] In `create/create.go`, `Bridge = steps.IncusBridge` (add the import).
- [ ] **Step 4: Run.** `GOTMPDIR=~/.cache/go-tmp go test ./internal/steps/ ./internal/create/ ./internal/cardinal/ ./cmd/flocc-setup/` — pass. (`create` still holds its own Firewalld/Docker until Task 4; both compile.)
- [ ] **Step 5: Commit.** `git add -A installer/internal/steps installer/internal/create/create.go installer/internal/cardinal installer/cmd && git commit -s -m "steps: firewalld and Docker steps that run as root on the machine"`

### Task 2: The `create-root` phase in flocc-setup

**Files:**
- Modify: `installer/cmd/flocc-setup/cardinal.go:25-30, 80-100, 147-161`
- Test: `installer/cmd/flocc-setup/cardinal_test.go`

**Interfaces:**
- Consumes: `steps.CreateRootSteps()`, `steps.CheckRootStage`, `steps.RunAll`, `steps.ReportSteps`.
- Produces: `flocc-setup --phase create-root`, run as root through sudo from a checked stage; prints one `flocc-cardinal {...}` report line (`steps.CardinalReport` with `Steps`, and `Error` on failure); exit 1 on failure.

- [ ] **Step 1: Failing tests** in `cardinal_test.go` (read it first for its pattern of calling `runCardinal` and `cardinalPhase`):

```go
func TestCreateRootIsARootPhaseWithoutAUser(t *testing.T) {
	if _, ok := cardinalPhase([]string{"--phase", "create-root"}); !ok {
		t.Fatal("create-root is not routed to runCardinal")
	}
	if checkCardinalPhase("create-root", 0) != nil || checkCardinalPhase("create-root", 1000) == nil {
		t.Fatal("create-root is not a root phase")
	}
	// Refused before anything runs (not root, or no SUDO_UID), and never as a bad --user.
	var errb strings.Builder
	if code := runCardinal([]string{"--phase", "create-root"}, nil, io.Discard, &errb); code != 2 || strings.Contains(errb.String(), "user name") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}
```

  The `runCardinal` half passes today too (the phase is unknown); the `cardinalPhase` check is what fails first. The half guards the user check: with `create-root` in `stageFiles` and today's check, the missing `--user` would be refused as `--user "" is not a user name`.

- [ ] **Step 2: Run** `GOTMPDIR=~/.cache/go-tmp go test ./cmd/flocc-setup/ -run CreateRoot` — fails.
- [ ] **Step 3: Implement.** Add `"create-root"` to `cardinalRootPhases` and `stageFiles["create-root"] = []string{"flocc-setup"}`. The user check becomes: a `--user` that is given must be valid, and `cardinal-root` and `cardinal-purge` require one:

```go
needsUser := *phase == "cardinal-root" || *phase == "cardinal-purge"
if (*user != "" || needsUser) && !saved.ValidUser(*user) {
```

  Add the case:

```go
case "create-root":
	list := steps.CreateRootSteps()
	err := steps.RunAll(ctx, env, list)
	// Under a cancelled context every check reads "not running", which is done: after a Ctrl-C
	// the report holds the cancellation and no steps, even when RunAll returned nil.
	if ctx.Err() != nil {
		return finish(steps.CardinalReport{}, cmp.Or(err, ctx.Err()))
	}
	return finish(steps.CardinalReport{Steps: steps.ReportSteps(ctx, env, list)}, err)
```

  `RunAll` under the `Scripted` UI prints its status lines and step lines to stdout, which the owner sees in the handoff, and `PickRedo` returns nil, so nothing is redone. `checkCardinalPhase` refuses a non-root euid for every phase in `cardinalRootPhases`, so `create-root` gets that for free. The cancelled branch needs a signal to test; Task 1's cancelled-context test covers the loop that would otherwise swallow it.
- [ ] **Step 4: Run** the package tests — pass.
- [ ] **Step 5: Commit** `git add installer/cmd/flocc-setup && git commit -s -m "flocc-setup: create-root phase runs create's firewall steps as root"`

### Task 3: `rootpass`, shared by cardinal and create

**Files:**
- Create: `installer/internal/rootpass/rootpass.go`, `installer/internal/rootpass/rootpass_test.go`
- Modify: `installer/internal/cardinal/passes.go` (remove `sshArgs`, `build`'s body, `reportEvents`, `reportTap`, the body of `rootPass`), `installer/internal/cardinal/commands.go` (callers: `reportEvents` at 302, 340, 362, 525; `reportTap` at 354)
- Modify: `installer/internal/host/fake.go` (two test aids, below), `installer/internal/cardinal/cardinal_test.go:450-460` (`prefixFake` gives way to them)

**Interfaces:**
- Produces:

```go
package rootpass

// SSH is cardinal's sshArgs, moved verbatim with its comment.
func SSH(opts []string, tty bool, dest, remote string) host.Cmd

// Build makes ./installer/cmd/<name> for linux/arch from the checkout at root, one step per
// binary, into a new temporary directory the caller removes.
func Build(ctx context.Context, r host.Runner, s ui.Session, root, arch string, names ...string) (string, error)

type Pass struct {
	Machine string     // the owner's ssh destination on the machine
	Opts    []string   // ssh options: the shared master's
	Prefix  string     // the stage's mktemp prefix: "/tmp/flocc-cardinal" or "/tmp/flocc-create"
	Files   []string   // local files to stage; one must be named flocc-setup
	Calls   [][]string // flocc-setup's arguments, one sudo each, in order; shell-safe words only
	Why     string     // the handoff banner
}

// Run stages p.Files and runs every call on one line in one ssh -t (see rootPass's comment, moved
// here). It returns the reports in call order, and on failure also an error naming the last
// report error and the stage's log.
func Run(ctx context.Context, r host.Runner, p Pass) ([]steps.CardinalReport, error)

// Events is cardinal's reportEvents, moved verbatim.
func Events(r steps.CardinalReport) []ui.Event

// Tap is cardinal's reportTap, moved verbatim.
func Tap(out io.Writer, rs *[]steps.CardinalReport) *ship.LineTap
```

- [ ] **Step 0: Test aids in `host.Fake`.** `Fake.Calls` keeps only `String()`, which has no `Why`, and `go build` and `scp` of a temp directory have keys no test can name (cardinal's tests wrap the fake in `prefixFake` for that). Add to `fake.go`: an `Interactives []Cmd` field that `Interactive` appends each command to, and `OnName(name string, code int, stdout string) *Fake`, which answers any command of that `Name` that has no exact response. Replace `prefixFake` in `cardinal_test.go` with `f.OnName("go", 0, "").OnName("scp", 0, "")` at its two uses (lines 420, 520) and delete the type.
- [ ] **Step 1: Failing tests** in `rootpass_test.go` with `host.Fake` (read `cardinal_test.go:158-185, 480-510` for how the fake answers `mktemp` and `Interactive`; reuse that shape). Fake keys are built with `SSH(opts, false, machine, "mktemp -d /tmp/flocc-create.XXXXXX").String()` and `SSH(opts, true, machine, line).String()`. A report line given to `OnInteractiveOut` must end in `\n`: `Tap` takes only whole lines, and `Flush` passes a partial one through untaken.
  - `TestRunStagesUnderThePrefixAndSudoesEachCall`: fake `mktemp -d /tmp/flocc-create.XXXXXX` returns `/tmp/flocc-create.Ab12Cd\n`, and the exact `scp -q <opts> <file> <machine>:/tmp/flocc-create.Ab12Cd/` exits 0 (an unanswered key exits 127 and fails `Run`); `Run` with `Calls: [][]string{{"--phase", "create-root"}}`; assert the interactive command's last arg is `chmod 755 /tmp/flocc-create.Ab12Cd/flocc-setup && sudo /tmp/flocc-create.Ab12Cd/flocc-setup --phase create-root && rm -rf /tmp/flocc-create.Ab12Cd`, its args contain `-t`, and `f.Interactives[0].Why` equals `p.Why` and does not contain `flocc-setup`.
  - `TestRunRefusesAStageOutsideThePrefix`: mktemp returns `/tmp/flocc-cardinal.Ab12Cd` while `Prefix` is `/tmp/flocc-create`; error contains `mktemp on`, and no `scp` ran.
  - `TestRunNamesTheReportErrorAndTheLog`: interactive output holds `steps.CardinalReport{Error: "Firewalld: boom"}.Line() + "\n"` and `OnInteractive` an error; `Run` returns that one report, and an error containing `Firewalld: boom` and `/tmp/flocc-create.Ab12Cd/setup.log`.
- [ ] **Step 2: Run** `GOTMPDIR=~/.cache/go-tmp go test ./internal/rootpass/` — fails to compile.
- [ ] **Step 3: Implement.** Move the code. `Run` is `rootPass` from its mktemp call (`passes.go:386`) to the returned error, with `steps.ValidRootStage(dir) && strings.HasPrefix(dir, p.Prefix+".")` as the stage test, `SSH(p.Opts, false, p.Machine, "mktemp -d "+p.Prefix+".XXXXXX")` for mktemp, `host.Cmd{Name: "scp", Args: append(append(append([]string{"-q"}, p.Opts...), p.Files...), p.Machine+":"+dir+"/")}` for the copy, and `" && sudo " + dir + "/flocc-setup " + strings.Join(call, " ")` per call. The error keeps cardinal's text: `fmt.Errorf("the root pass failed: %sits log is %s/setup.log on %s: %w", why, dir, p.Machine, err)`, where `why` is the last non-empty `saved.Sanitize(r.Error) + "; "`, as now. `Build` is `Deps.build` without `RepoRoot` and without `d.UI.Group("Build")`; `os.MkdirTemp("", "flocc-rootpass-")`.
- [ ] Rewrite cardinal on top: `sshArgs` call sites use `rootpass.SSH`; `Deps.build(ctx, arch)` keeps its RepoRoot lookup and error, calls `d.UI.Group("Build")`, then `rootpass.Build(ctx, d.Run, d.UI, root, arch, "flocc", "flocc-setup")`; `reportEvents`/`reportTap` call sites use `rootpass.Events`/`rootpass.Tap`. `rootPass` keeps its TTY refusal, writes `desktop.pub` as now, builds `Calls` from `rootCall` (`{"--phase", c.Phase, "--user", c.User}` plus `"--adopt"`), calls `rootpass.Run` with `Prefix: "/tmp/flocc-cardinal"`, `Opts: d.Mux`, `Why: "root pass: sudo on " + s.Machine + " asks for your password"`, then draws the groups and rows from the reports exactly as now and returns Run's error. Keep the "i-th report is the i-th call's" comment in cardinal.
  `Run` returns the reports it tapped together with its error, so cardinal still draws a failed pass's rows before it returns the error. `rootPass` writes `desktop.pub` into `bin` before it calls `Run`, so that write now comes before mktemp; nothing depends on the order.
- [ ] **Step 4: Run** `GOTMPDIR=~/.cache/go-tmp go test ./internal/host/ ./internal/rootpass/ ./internal/cardinal/` — pass, cardinal's tests changed only where `prefixFake` went.
- [ ] **Step 5: Commit** `git add -A installer/internal/rootpass installer/internal/cardinal installer/internal/host && git commit -s -m "rootpass: cardinal's build, stage and sudo pass, shared"`

### Task 4: Create runs the root pass

**Files:**
- Create: `installer/internal/create/root.go`, `installer/internal/create/root_test.go`
- Modify: `installer/internal/create/create.go` (remove `needRoot`, `Preconditions` returns `error`, add `Checkout` and `rootDone` to `Env`), `installer/internal/create/install.go:20-32, 211-232` (`Steps()`, `Installer.RepoRoot`, `Run`)
- Delete: `installer/internal/create/firewalld.go`, `docker.go`, `firewalld_test.go`, `docker_test.go`; in `create_test.go`, `TestNeedRootNamesTheFix` (300-309) and `rootCalls` (169-184), which nothing calls once the firewall tests go
- Modify: `installer/internal/create/install_test.go` (`fakeInst` gains `RepoRoot`; the step names at 316-319 become `Incus, Root pass, VM, Egress, Tailscale`)
- Modify: `installer/internal/cli/create.go` (`hostInstaller.RepoRoot`), `installer/internal/cli/setup_ops.go:53-58, 289-297`, `installer/internal/cli/setup.go:652-655`, `installer/internal/cli/setup_test.go` (line 62's `machineState{Sudo: true}` becomes `machineState{}`; delete `TestSetupRefusesCreateWithoutPasswordlessSudo`, 235-243), any other `Installer` fake (`grep -rn 'HostKnown(' installer --include='*_test.go'`: `create/install_test.go`, `cli/create_test.go`)

**Interfaces:**
- Consumes: `steps.CreateRootIdle`, `rootpass.Build`, `rootpass.Run`, `rootpass.Events`, `ship.ArchFor`.
- Produces: `type create.Root struct{}` (Step "Root pass"); `create.Steps()` = `Incus{}, Root{}, VM{}, Egress{}, Tailnet{}`; `Installer.RepoRoot() (string, error)`; `func create.Preconditions(ctx, m host.Runner, machine string) error`.

- [ ] **Step 1: Failing tests** in `root_test.go`, using `testEnv` from `create_test.go` (it runs machine commands on `m` directly, with `TTY` and `sudo` true and the machine `laptop`) and a `host.Fake` for `e.Local`. Set `e.Checkout = func() (string, error) { return t.TempDir(), nil }`.

```go
func TestRootSkipsThePassWithoutFirewalldOrDocker(t *testing.T) {
	m := host.NewFake()
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
```

  Also, with firewalld active, `uname -m` → `x86_64` on `m`, and on `e.Local`: `OnName("go", 0, "")` and `OnName("scp", 0, "")` (the build's `-o` and the scp source are temp paths), `rootpass.SSH(ControlOpts(), false, "laptop", "mktemp -d /tmp/flocc-create.XXXXXX").String()` answering `/tmp/flocc-create.Ab12Cd\n`, and the interactive key `rootpass.SSH(ControlOpts(), true, "laptop", "chmod 755 /tmp/flocc-create.Ab12Cd/flocc-setup && sudo /tmp/flocc-create.Ab12Cd/flocc-setup --phase create-root && rm -rf /tmp/flocc-create.Ab12Cd").String()` carrying the report's `Line()` plus `\n`:
  - `TestRootRunsOnePassAndDrawsItsSteps`: report `{"steps":[{"name":"Firewalld","detail":"incusbr0 in zone flocc-incus","done":true},{"name":"Docker","detail":"Docker not running","done":true}]}`; RunSteps succeeds; `u.Rows()` holds Firewalld and Docker done and Root pass done with `firewalld and Docker checked as root on laptop`; exactly one interactive command ran; `local.Interactives[0].Why` is `root pass: sudo on laptop may ask for your password`; `dockerUntouched` over `local.Calls` and `m.Calls`.
  - `TestRootFailsOnAStepNotDone`: report with Docker `"done":false,"detail":"iptables DOCKER-USER lacks ..."` and no error, interactive exit 0; RunSteps fails with an error containing `Docker` and `lacks`.
  - `TestRootFailsWhenThePassFails`: `OnInteractive` an error and report `{"error":"Firewalld: firewalld on this machine uses the iptables backend"}`; error contains `iptables backend` and `setup.log`.
  - Update `TestPreconditionsReportSudoAndIncusApart` (rename `TestPreconditionsReportIncus`) to the one-value return.

  The first two tests compile only once `Root`, `Env.Checkout` and `rootDone` exist, so all of them fail to compile first.
- [ ] **Step 2: Run** `GOTMPDIR=~/.cache/go-tmp go test ./internal/create/ -run 'Root|Preconditions'` — fails.
- [ ] **Step 3: Implement** `root.go`:

```go
package create

// Root runs the Firewalld and Docker steps (package steps) as root on the machine, in one sudo:
// their checks read firewalld and iptables, which only root can.
type Root struct{}

func (Root) Name() string { return "Root pass" }

func (Root) Check(ctx context.Context, e *Env) (bool, string, error) {
	if e.rootDone != "" {
		return true, e.rootDone, nil
	}
	if idle, detail := steps.CreateRootIdle(ctx, &steps.Env{Run: e.M}); idle {
		return true, detail, nil
	}
	return false, "firewalld or Docker runs on " + e.Spec.Machine + "; checking them needs root", nil
}

func (Root) Apply(ctx context.Context, e *Env) error {
	m := e.Spec.Machine
	if !e.TTY && !e.sudo {
		return fmt.Errorf("sudo on %s asks for a password; run create in a terminal", m)
	}
	uname, err := e.must(ctx, cmd("uname", "-m"))
	if err != nil {
		return err
	}
	arch, err := ship.ArchFor(uname)
	if err != nil {
		return err
	}
	root, err := e.Checkout()
	if err != nil {
		return fmt.Errorf("host create builds flocc-setup from the flocc checkout it runs in; cd into it: %w", err)
	}
	bin, err := rootpass.Build(ctx, e.Local, e.UI, root, arch, "flocc-setup")
	if err != nil {
		return err
	}
	defer os.RemoveAll(bin)
	reports, err := rootpass.Run(ctx, e.Local, rootpass.Pass{Machine: m, Opts: ControlOpts(), Prefix: "/tmp/flocc-create",
		Files: []string{filepath.Join(bin, "flocc-setup")}, Calls: [][]string{{"--phase", "create-root"}},
		Why: "root pass: sudo on " + m + " may ask for your password"})
	for _, r := range reports {
		ui.Rows(e.UI, rootpass.Events(r))
	}
	if err != nil {
		return err
	}
	if len(reports) != 1 {
		return fmt.Errorf("the root pass on %s sent %d reports, not 1", m, len(reports))
	}
	// RunAll checks every step before it applies any, so a step can end not done with no error.
	for _, s := range reports[0].Steps {
		if !s.Done {
			return fmt.Errorf("%s on %s: %s", saved.Sanitize(s.Name), m, saved.Sanitize(s.Detail))
		}
	}
	e.rootDone = "firewalld and Docker checked as root on " + m
	return nil
}
```

  Check that `steps.Env{Run: e.M}` is enough for `CreateRootIdle` (it only calls `e.Ok`). If `rootpass.Build` writes into `/tmp` via `os.MkdirTemp("")`, tests honour `TMPDIR`; leave it.
- [ ] In `create.go`: add `Checkout func() (string, error) // the flocc checkout flocc-setup is built from` and `rootDone string // the root pass's detail once it ran` to `Env`; delete `needRoot`; `Preconditions` returns `e.Probe(ctx)` alone and its comment drops the sudo sentence; update the `root` comment to say it is incus's. `Steps()` becomes `[]Step{Incus{}, Root{}, VM{}, Egress{}, Tailnet{}}`. `Installer` gains `// RepoRoot is the flocc checkout this runs in.` `RepoRoot() (string, error)`; `Run` sets `Checkout: inst.RepoRoot`. Delete `firewalld.go`, `docker.go` and their tests, then `TestNeedRootNamesTheFix` and `rootCalls` from `create_test.go`. Neither the compiler nor `go vet` reports an unused function, so grep each helper in `create_test.go` for a caller. cardinal's `machineEnv` builds a `create.Env` literal and calls `Probe`; both still compile, since the new fields are optional and `Probe` keeps setting `sudo`.
- [ ] In `cli`: `func (h hostInstaller) RepoRoot() (string, error) { return h.d.repoRoot() }` (check `deps.repoRoot`'s signature); `machineState` loses `Sudo`; `probeMachine` sets `Incus: create.Preconditions(...)`; the wizard's `case !ms.Sudo:` branch goes; fix `setup_test.go` and every `Installer` fake.
- [ ] **Step 4: Run** `GOTMPDIR=~/.cache/go-tmp go test ./internal/create/ ./internal/cli/ ./internal/cardinal/` then `GOTMPDIR=~/.cache/go-tmp go vet ./...` — pass.
- [ ] **Step 5: Commit** `git add -A installer && git commit -s -m "create: run the firewall and Docker steps in one sudo root pass"`

### Task 5: Docs

**Files:**
- Modify: `docs/book/setup/host-create.md:13-14, 22-24`, `docs/book/setup/README.md:35`, `docs/book/troubleshooting.md:62-74` (Host create), `CHANGELOG.md` (the 0.9.0 entry, line 7)

- [ ] `host-create.md`: replace the two bullets with: incus needs `incus-admin` membership or passwordless sudo, as before; when firewalld or Docker runs on the machine, create builds `flocc-setup`, stages it and runs the firewall and Docker steps as root in one `ssh -t ... sudo`, so sudo asks for the password once per run (not at all with passwordless sudo); it needs a terminal unless sudo is passwordless; a run stopped in the pass leaves `/tmp/flocc-create.XXXXXX/setup.log` on the machine. In "What create does", say steps 2 and 3 run together in that root pass, shown as the row "Root pass" with a Firewalld and a Docker row drawn from its report, and that a machine with neither skips it with no password. Grep the page for any other `sudo -n` or "passwordless" line and fix it.
- [ ] `troubleshooting.md`, Host create table: add rows for `sudo on <machine> asks for a password; run create in a terminal` (run create in a terminal, or give the account passwordless sudo) and `the root pass failed: ... its log is /tmp/flocc-create.XXXXXX/setup.log` (read that log on the machine, fix what it names, run create again). Ship's passwordless row stays.
- [ ] `setup/README.md:35`: host create no longer needs passwordless sudo; say what it needs, as above, in one sentence.
- [ ] `CHANGELOG.md` 0.9.0: one bullet: "`host create` runs its firewalld and Docker steps as root in one `sudo` on the machine, so a password on sudo is asked once instead of stopping create; the setup wizard no longer refuses such a machine."
- [ ] Commit `git add docs/book CHANGELOG.md && git commit -s -m "docs: host create's root pass"`

### Finish

- [ ] `cd installer && gofmt -l . && GOTMPDIR=~/.cache/go-tmp go vet ./... && GOTMPDIR=~/.cache/go-tmp go test -race ./...` (run in the background; it exits), then `uv run pytest -q` at the root.
