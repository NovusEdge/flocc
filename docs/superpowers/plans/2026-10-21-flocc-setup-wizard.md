# `flocc setup` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One rerunnable desktop command, `flocc setup`, that walks keys, host, old-version cleanup, cardinal and logins in order, checking before each step.

**Architecture:** A wizard in `installer/internal/cli/setup.go` calls the existing functions through a struct of function fields (`setupOps`), so its tests replace each one. The cardinal package gains exported entry points and a root pass that runs an old user's purge and cardinal's root work in one `ssh -t`. The old-user table and the root-side re-checks live in `steps`, which flocc-setup runs as root.

**Tech Stack:** Go 1.27 (`go.mod`), huh v2 forms (`charm.land/huh/v2`), go-toml v2. Tests are `go test` with `host.Fake`.

**Spec:** `docs/superpowers/specs/2026-10-21-flocc-setup-wizard-design.md`. Read it before each task.

## Global Constraints

- Work in `~/Projects/Personal/worktrees/flocc-cardinal-impl` on branch `feat/cardinal-setup` (PR #48). Commit there; do not push.
- Commit with `git commit -s`. No `Co-Authored-By` line and no AI attribution anywhere.
- Before each commit: `gofmt -l deploy installer` prints nothing, `go vet ./...` passes, `go test ./installer/...` passes.
- Read files with the Read tool and change them with Edit or Write, never `sed -i`, `cat >` or a heredoc.
- Comments carry only what the code cannot say (why, constraints, what breaks). Match the surrounding comment density. No section banners, no narration of the change.
- No new dependency.
- The existing commands (`flocc keys`, `flocc host create|add`, `flocc ship`, `flocc login`, `flocc cardinal ...`) keep working with the same flags and output.
- Every string from cardinal's side or from another user's processes is printed through `saved.Sanitize` or `saved.SanitizeBlock`.
- The wizard never removes anything without a yes, and the removal prompt defaults to no.
- Version stays 0.8.0: this lands in the unreleased 0.8.0 entry.

## Review Focus

1. A Ctrl-C at any wizard prompt (huh returns `huh.ErrUserAborted`) stops the wizard: nothing after it is asked or run, and it exits 1 with "Stopped". Pinned in Task 5.
2. `owner@zer0-day-nimble` in one host file and `zer0-day-nimble` in another are one machine: one old-version scan, one root pass. Pinned in Task 7.
3. Process lines of an old user are that user's text and may hold escape sequences; they are printed sanitized. Pinned in Task 7.
4. A cardinal saved file marked `removed` (the owner ran `flocc cardinal remove`) is not set up again without a yes, and the default is no. Pinned in Task 7.
5. An empty answer to "Which machine holds <host>?" saves nothing, so the question comes back next run. Pinned in Task 5.

## Decisions this plan makes where the spec is silent

- The root pass branches on the name: `cardinal-purge --user <name>` for a name in `steps.OldUsers` runs the old-user checks and the archive; any other name purges as before. The spec's command line has no extra flag, and the table check makes the branch safe.
- The old user's processes stop before the archive, so `tar` reads a home nothing writes to. The archive name carries the time, `/root/flocc-removed-<name>-2006-01-02-150405.tar.gz`, so a second run the same day never overwrites one.
- Ship runs without asking when a host is older than the checkout or fails a doctor check other than the login and disk. A host still failing after one ship is a failed host.
- cardinal status failures other than the login send cardinal to setup; the login failure goes to step 6. Failures setup leaves (such as "no turn for 1h") are reported with their own remedy.
- "Continue creating <name>?" and "Adopt the existing cardinal user?" default to no; "Set up cardinal on <machine>?" defaults to yes.
- An unreachable host file whose tailnet node is offline gets the remedy for both pieces (remove the node in the admin console, `rm` the file). There is no scan for other offline nodes: nothing tells which ones were flocc's.
- `unknown` and an unreported login are shown and leave the exit code at 0.

## File Structure

| File | Change |
|---|---|
| `installer/internal/hosts/hosts.go` | `Machine` field, `MachineNone`, load/save checks |
| `installer/internal/cli/host.go`, `cli/create.go` | `hostAdd` and `Installer.AddHost` carry the machine |
| `installer/internal/create/install.go` | `Run` (Main's body), `Logged`, `AddHost(..., machine)` |
| `installer/internal/create/create.go` | `Preconditions` |
| `installer/internal/host/version.go` | `Version` |
| `installer/internal/steps/old_users.go` (new) | `OldUsers`, `CheckOldUser`, `RemoveOldUser` |
| `installer/internal/steps/cardinal_root.go` | `stopUser` split out of `CardinalPurge` |
| `installer/cmd/flocc-setup/cardinal.go` | `cardinal-purge` branches to `RemoveOldUser` and reports |
| `installer/internal/cardinal/passes.go` | `rootCall`, multi-call `rootPass`, `rootCalls`, report list |
| `installer/internal/cardinal/commands.go` | `SetupOptions`, `Setup`, `Status`, `Login`, `RemoveOld`, `logged`, `readStatus` |
| `installer/internal/cli/prompt.go` | `Ask`, `Choose` |
| `installer/internal/cli/setup.go` (new) | the wizard: rows, steps, summary |
| `installer/internal/cli/setup_ops.go` (new) | `setupOps`, `newSetupOps` and the real implementations |
| `installer/internal/cli/setup_test.go` (new) | wizard tests with fake ops |
| `installer/internal/cli/cli.go`, `cli/cardinal.go` | `setup` verb, usage line, `cardinalDeps` |
| `README.md`, `docs/vm-setup.md`, `CHANGELOG.md` | docs |

---

### Task 1: Host files carry `machine`

**Files:**
- Modify: `installer/internal/hosts/hosts.go`
- Modify: `installer/internal/cli/host.go:113-128`, `installer/internal/cli/create.go:30-32`
- Test: `installer/internal/hosts/hosts_test.go`, `installer/internal/cli/host_test.go`

**Interfaces:**
- Produces: `hosts.Host.Machine string` (toml `machine`), `hosts.MachineNone = "none"`; `hostAdd(ctx, d, name, sshDest, machine string) error`; `loadOrNew` copies `reach.Machine` when set.

- [ ] **Step 1: Write the failing tests**

Append to `installer/internal/hosts/hosts_test.go` (add `os`, `path/filepath`, `strings` to its imports if missing):

```go
func TestMachineRoundTripsAndNoneIsKept(t *testing.T) {
	dir := t.TempDir()
	for _, m := range []string{"zer0-day-nimble", "owner@zer0-day-nimble", MachineNone} {
		if err := Save(dir, Host{Name: "h", SSH: "root@h", Machine: m}); err != nil {
			t.Fatal(err)
		}
		got, err := Load(dir, "h")
		if err != nil || got.Machine != m {
			t.Fatalf("%q: got %+v %v", m, got, err)
		}
	}
}

func TestMachineIsRefusedWithDaytonaAndWhenNotADestination(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, Host{Name: "d", Daytona: "sb", Machine: "box"}); err == nil {
		t.Fatal("saved machine with daytona")
	}
	if err := Save(dir, Host{Name: "s", SSH: "root@s", Machine: "-oProxyCommand=x"}); err == nil {
		t.Fatal("saved a machine ssh would read as an option")
	}
	os.WriteFile(filepath.Join(dir, "d.toml"), []byte("daytona = \"sb\"\nmachine = \"box\"\n"), 0o600)
	if _, err := Load(dir, "d"); err == nil || !strings.Contains(err.Error(), "machine") {
		t.Fatalf("load: %v", err)
	}
}
```

Append to `installer/internal/cli/host_test.go`:

```go
func TestHostAddSavesTheMachine(t *testing.T) {
	d, _, _ := addDeps(t, allKeys)
	if err := hostAdd(context.Background(), d, "h1", "root@h1", "zer0-day-nimble"); err != nil {
		t.Fatal(err)
	}
	if h := loadHost(t, d, "h1"); h.Machine != "zer0-day-nimble" || h.SSH != "root@h1" {
		t.Fatalf("%+v", h)
	}
	// A second add without a machine keeps the one saved.
	if err := hostAdd(context.Background(), d, "h1", "", ""); err != nil {
		t.Fatal(err)
	}
	if h := loadHost(t, d, "h1"); h.Machine != "zer0-day-nimble" {
		t.Fatalf("%+v", h)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/internal/hosts/ ./installer/internal/cli/ -run 'Machine'`
Expected: compile errors (`Machine` undefined, too many arguments to `hostAdd`).

- [ ] **Step 3: Implement**

In `hosts.go`, add the field after `Daytona` and the constant after the type:

```go
type Host struct {
	Name    string `toml:"-"`
	SSH     string `toml:"ssh,omitempty"`
	Daytona string `toml:"daytona,omitempty"`
	// Machine is the owner's ssh destination on the machine holding the host, for flocc setup's
	// old-version cleanup and cardinal; MachineNone when the host is on none of the owner's.
	Machine string `toml:"machine,omitempty"`
	answers.Answers
}

const MachineNone = "none"
```

Extend `check` with two cases after the existing two:

```go
	case h.Machine != "" && h.Daytona != "":
		return fmt.Errorf("%s.toml: machine is for an ssh host; a Daytona sandbox is on none of your machines", h.Name)
	case h.Machine != "" && h.Machine != MachineNone && !ValidTarget(h.Machine):
		return fmt.Errorf("%s.toml: machine %q is not an ssh destination", h.Name, h.Machine)
```

Check `answers.Answers` has no field with toml key `machine` (`grep -n machine installer/internal/answers/answers.go`); if it does, stop and report.

In `cli/host.go`, change `hostAdd` and `loadOrNew`:

```go
// hostAdd is flocc host add <name> [--ssh DEST]; host create calls it with its machine.
func hostAdd(ctx context.Context, d *deps, name, sshDest, machine string) error {
	return addHost(ctx, d, name, hosts.Host{SSH: sshDest, Machine: machine})
}

// loadOrNew is the host's file, or a new host reached as root@<name>; reach overrides either.
func loadOrNew(d *deps, name string, reach hosts.Host) (hosts.Host, error) {
	h, err := hosts.Load(hosts.Dir(d.home), name)
	if errors.Is(err, os.ErrNotExist) {
		h, err = hosts.Host{Name: name, SSH: "root@" + name}, nil
	}
	if reach.SSH != "" || reach.Daytona != "" {
		h.SSH, h.Daytona = reach.SSH, reach.Daytona
	}
	if reach.Machine != "" {
		h.Machine = reach.Machine
	}
	return h, err
}
```

In `cli/create.go`, `AddHost` keeps its two-argument form until Task 2 changes the interface; for now call `hostAdd(ctx, h.d, name, ssh, "")`. Update every other `hostAdd(` call in `installer/internal/cli/*_test.go` to pass `""` as the machine (`grep -n 'hostAdd(' installer/internal/cli`).

- [ ] **Step 4: Run the tests**

Run: `go test ./installer/internal/hosts/ ./installer/internal/cli/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/hosts installer/internal/cli
git commit -s -m "hosts: a host file names the machine that holds it"
```

---

### Task 2: create exposes `Run`, `Logged`, `Preconditions`, and writes the machine

**Files:**
- Modify: `installer/internal/create/install.go`, `installer/internal/create/create.go`
- Modify: `installer/internal/cli/create.go`
- Test: `installer/internal/create/install_test.go`, `installer/internal/create/create_test.go`

**Interfaces:**
- Consumes: `hostAdd(ctx, d, name, ssh, machine)` from Task 1.
- Produces:
  - `create.Installer.AddHost(ctx context.Context, name, ssh, machine string) error`
  - `create.Run(ctx context.Context, spec Spec, inst Installer, out io.Writer, tty bool) (logPath string, err error)`
  - `create.Logged() []string`: names with a `create-<name>.log` in `os.UserCacheDir()/flocc`
  - `create.Preconditions(ctx context.Context, m host.Runner, machine string) (sudo bool, incus error)`

- [ ] **Step 1: Write the failing tests**

In `install_test.go`, change `fakeInst.AddHost` to the new signature and record the machine (read the file first; keep its existing recording style), e.g.:

```go
func (f *fakeInst) AddHost(_ context.Context, name, ssh, machine string) error {
	f.calls = append(f.calls, "add "+name+" "+ssh+" "+machine)
	return f.addErr
}
```

Adjust the field names to the ones `fakeInst` already has, and update every expectation in the file that matches the old `add` record to include the machine (`e.Spec.Machine` of that test). Then add:

```go
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
```

In `create_test.go` add:

```go
func TestPreconditionsReportSudoAndIncusApart(t *testing.T) {
	f := host.NewFake().On("sudo -n true", 1, "").On("id -nG", 0, "owner incus-admin\n").On("incus query /1.0", 0, "{}")
	sudo, incus := Preconditions(context.Background(), f, "zer0-day-nimble")
	if sudo || incus != nil {
		t.Fatalf("sudo %v incus %v", sudo, incus)
	}
	f = host.NewFake().On("sudo -n true", 1, "").On("id -nG", 0, "owner\n")
	if _, incus := Preconditions(context.Background(), f, "zer0-day-nimble"); incus == nil || !strings.Contains(incus.Error(), "incus-admin") {
		t.Fatalf("incus %v", incus)
	}
}
```

Add any missing imports (`os`, `path/filepath`, `slices`, `strings`, `context`, `host`).

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/internal/create/`
Expected: compile errors for `Logged`, `Preconditions` and the `AddHost` signature.

- [ ] **Step 3: Implement**

In `install.go`, change the interface method and its call in `Create`:

```go
	AddHost(ctx context.Context, name, ssh, machine string) error
```

```go
		if err := inst.AddHost(ctx, name, ssh, e.Spec.Machine); err != nil {
```

Move Main's body after its checks into `Run`, and add `Logged` (imports: `path/filepath` and `strings` are already there):

```go
// Run makes the host once its arguments and the installer are checked, logging to
// ~/.cache/flocc/create-<name>.log, whose path it returns for the error message.
func Run(ctx context.Context, spec Spec, inst Installer, out io.Writer, tty bool) (string, error) {
	logPath, logf, err := openLog(spec.Name)
	if err != nil {
		return "", err
	}
	defer logf.Close()
	log := NewRedactLog(logf)
	run := host.Exec{Log: log}
	// Captured commands buffer stderr, so one that stops at a password or host-key prompt would
	// hang with nothing shown; BatchMode makes it fail instead.
	opts := append([]string{"-o", "BatchMode=yes"}, ControlOpts()...)
	e := &Env{M: Remote{R: run, Target: spec.Machine, Opts: opts}, Local: run,
		UI: ui.NewHuh(), Out: out, Log: log, Spec: spec, Sleep: time.Sleep, TTY: tty}
	err = e.connect(ctx)
	if err == nil {
		err = Create(ctx, e, inst, Steps())
	}
	return logPath, err
}

// Logged names each host a create run has logged for. A run that stopped before host add left
// its log and no host file, which flocc setup offers to continue.
func Logged() []string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "flocc", "create-*.log"))
	var names []string
	for _, p := range paths {
		names = append(names, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "create-"), ".log"))
	}
	return names
}
```

`Main` becomes:

```go
func Main(args []string, inst Installer) int {
	spec, err := ParseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "flocc host create:", err)
		return 2
	}
	// Ship and host add would hit these only after the firewall, the VM and the tailnet changed.
	err = inst.Ready()
	if err == nil {
		_, err = inst.HostKnown(spec.Name, "root@"+spec.Name)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "flocc host create:", err)
		return 1
	}
	st, _ := os.Stdin.Stat()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	logPath, err := Run(ctx, spec, inst, os.Stdout, st != nil && st.Mode()&os.ModeCharDevice != 0)
	if err != nil {
		if logPath == "" {
			fmt.Fprintln(os.Stderr, "flocc host create:", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "\nflocc host create: %s\n\nFull log: %s\nRun the same command again to continue.\n", err, logPath)
		return 1
	}
	return 0
}
```

In `create.go`, after `Probe`:

```go
// Preconditions reports what the machine allows before anything changes there: whether sudo
// works without a password, which create's firewall and Docker steps need even to check, and
// whether incus runs as an administrator (nil), else the fix. m runs commands on the machine.
func Preconditions(ctx context.Context, m host.Runner, machine string) (sudo bool, incus error) {
	e := &Env{M: m, Spec: Spec{Machine: machine}}
	incus = e.Probe(ctx)
	return e.sudo, incus
}
```

In `cli/create.go`, `AddHost` takes and passes the machine:

```go
func (h hostInstaller) AddHost(ctx context.Context, name, ssh, machine string) error {
	return hostAdd(ctx, h.d, name, ssh, machine)
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./installer/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/create installer/internal/cli
git commit -s -m "create: Run, Logged and Preconditions for flocc setup; the host file gets its machine"
```

---

### Task 3: The root pass removes an old user only after its own checks, archiving the home

**Files:**
- Create: `installer/internal/steps/old_users.go`
- Modify: `installer/internal/steps/cardinal_root.go:354-383`
- Modify: `installer/cmd/flocc-setup/cardinal.go:79-99,144-145`
- Test: `installer/internal/steps/old_users_test.go` (new)

**Interfaces:**
- Produces:
  - `steps.OldUsers []string` (`{"linear-agents"}`)
  - `steps.CheckOldUser(ctx context.Context, e *Env, user string, sudoUID int) error`
  - `steps.RemoveOldUser(ctx context.Context, e *Env, user string, sudoUID int, now time.Time) (CardinalReport, error)`
  - flocc-setup `--phase cardinal-purge --user <name>`: for a name in `OldUsers` it runs `RemoveOldUser`; otherwise `CardinalPurge`. Both print a report line with steps.

- [ ] **Step 1: Write the failing tests**

Create `installer/internal/steps/old_users_test.go`:

```go
package steps

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// oldUser is linear-agents as zer0-day-nimble had it on 2026-10-21: uid 1001, its own group,
// lingering with only its user manager's session.
func oldUser(t *testing.T) (*Env, *host.Fake) {
	f := host.NewFake().
		On("getent passwd linear-agents", 0, "linear-agents:x:1001:1001::/home/linear-agents:/usr/bin/zsh\n").
		On("id -nG linear-agents", 0, "linear-agents\n").
		On("loginctl show-user linear-agents -p Sessions --value", 0, "2\n").
		On("loginctl show-session 2 -p Class --value", 0, "manager\n").
		On("loginctl disable-linger linear-agents", 0, "").
		On("loginctl terminate-user linear-agents", 0, "").
		On("pkill -KILL -u linear-agents", 0, "").
		On("pgrep -u linear-agents", 1, "").
		On("userdel -r linear-agents", 0, "").
		On("systemctl disable --now flocc-cardinal-egress@linear-agents.service", 0, "")
	e := testEnv(t, f)
	os.MkdirAll(e.Path("/home/linear-agents"), 0o700)
	os.MkdirAll(e.Path("/etc"), 0o755)
	os.WriteFile(e.Path("/etc/login.defs"), []byte("UID_MIN\t\t\t 1000\n"), 0o644)
	return e, f
}

const archiveCall = `sh -c umask 077 && tar -czf "$1" -C /home -- "$2" sh /root/flocc-removed-linear-agents-2026-10-21-153000.tar.gz linear-agents`

func TestRemoveOldUserArchivesBeforeUserdel(t *testing.T) {
	e, f := oldUser(t)
	f.On(archiveCall, 0, "")
	r, err := RemoveOldUser(context.Background(), e, "linear-agents", 1000, time.Date(2026, 10, 21, 15, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("%v; %v", err, f.Calls)
	}
	tar, del := slices.Index(f.Calls, archiveCall), slices.Index(f.Calls, "userdel -r linear-agents")
	if tar < 0 || del < tar || slices.Index(f.Calls, "pgrep -u linear-agents") > tar {
		t.Fatalf("calls %v", f.Calls)
	}
	if len(r.Steps) != 2 || !strings.Contains(r.Steps[0].Detail, "/root/flocc-removed-linear-agents-2026-10-21-153000.tar.gz") || len(r.Notes) != 1 {
		t.Fatalf("report %+v", r)
	}
}

func TestCheckOldUserRefuses(t *testing.T) {
	cases := map[string]func(e *Env, f *host.Fake){
		"not in the table": nil,
		"below UID_MIN": func(e *Env, f *host.Fake) {
			f.On("getent passwd linear-agents", 0, "linear-agents:x:999:999::/home/linear-agents:/bin/sh\n")
		},
		"the sudo user": func(e *Env, f *host.Fake) {
			f.On("getent passwd linear-agents", 0, "linear-agents:x:1000:1000::/home/linear-agents:/bin/sh\n")
		},
		"home elsewhere": func(e *Env, f *host.Fake) {
			f.On("getent passwd linear-agents", 0, "linear-agents:x:1001:1001::/srv/la:/bin/sh\n")
		},
		"symlinked home": func(e *Env, f *host.Fake) {
			os.Remove(e.Path("/home/linear-agents"))
			os.Symlink(e.Path("/etc"), e.Path("/home/linear-agents"))
		},
		"privileged group": func(e *Env, f *host.Fake) { f.On("id -nG linear-agents", 0, "linear-agents docker\n") },
		"live session": func(e *Env, f *host.Fake) {
			f.On("loginctl show-user linear-agents -p Sessions --value", 0, "2 5\n").On("loginctl show-session 5 -p Class --value", 0, "user\n")
		},
	}
	for name, setup := range cases {
		e, f := oldUser(t)
		user := "linear-agents"
		if setup == nil {
			user = "cardinal"
		} else {
			setup(e, f)
		}
		if err := CheckOldUser(context.Background(), e, user, 1000); err == nil {
			t.Errorf("%s: passed", name)
		}
		if slices.ContainsFunc(f.Calls, func(c string) bool { return strings.HasPrefix(c, "userdel") || strings.Contains(c, "tar ") }) {
			t.Errorf("%s: deleted or archived", name)
		}
	}
	e, _ := oldUser(t)
	if err := CheckOldUser(context.Background(), e, "linear-agents", 1000); err != nil {
		t.Fatalf("the live user refused: %v", err)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/internal/steps/ -run 'OldUser'`
Expected: compile errors (`RemoveOldUser`, `CheckOldUser` undefined).

- [ ] **Step 3: Implement**

In `cardinal_root.go`, split the stop loop out of `CardinalPurge`:

```go
// stopUser ends every process of user; userdel refuses while any is left, and tmux's pane scopes
// outlive the session.
func stopUser(ctx context.Context, e *Env, user string) error {
	e.Ok(ctx, "loginctl", "disable-linger", user)
	e.Ok(ctx, "loginctl", "terminate-user", user)
	for waited := time.Duration(0); ; waited += 500 * time.Millisecond {
		e.Ok(ctx, "pkill", "-KILL", "-u", user)
		if !e.Ok(ctx, "pgrep", "-u", user) {
			return nil
		}
		if waited >= purgeWait {
			return fmt.Errorf("%s still has processes after %s; stop them and run again", user, purgeWait)
		}
		e.Sleep(500 * time.Millisecond)
	}
}

// CardinalPurge deletes the user and its home, then the egress block, which must hold until no
// process of the user is left.
func CardinalPurge(ctx context.Context, e *Env, user string) error {
	if cardinalHome(ctx, e, user) != "" {
		if err := stopUser(ctx, e, user); err != nil {
			return err
		}
		if _, err := e.Must(ctx, "userdel", "-r", user); err != nil {
			return err
		}
	}
	e.Ok(ctx, "systemctl", "disable", "--now", Egress{User: user}.unit())
	if err := os.Remove(e.Path(EgressRulesPath(user))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
```

Keep the `purgeWait` comment as it is, minus the sentence that moved onto `stopUser`. Run `go test ./installer/internal/steps/ -run Purge` to confirm the existing purge test still passes.

Create `installer/internal/steps/old_users.go`:

```go
package steps

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// OldUsers are the service users earlier flocc versions made. flocc setup offers to remove one it
// finds; the root pass removes no other name. A later rename adds its old name here.
var OldUsers = []string{"linear-agents"}

var uidMinLine = regexp.MustCompile(`(?m)^\s*UID_MIN\s+(\d+)`)

func uidMin(e *Env) int {
	b, _ := os.ReadFile(e.Path("/etc/login.defs"))
	if m := uidMinLine.FindSubmatch(b); m != nil {
		if n, err := strconv.Atoi(string(m[1])); err == nil {
			return n
		}
	}
	return 1000
}

// CheckOldUser refuses unless user is a plain account of an old install: on the list, a regular
// uid that is not the one running sudo, a real /home/<user>, no group that reaches root, and no
// session but its user manager's. The desktop checked before asking; root does not take its word.
func CheckOldUser(ctx context.Context, e *Env, user string, sudoUID int) error {
	if !slices.Contains(OldUsers, user) {
		return fmt.Errorf("%s is not a user an earlier flocc made", user)
	}
	f := strings.Split(e.Output(ctx, "getent", "passwd", user), ":")
	if len(f) < 7 {
		return fmt.Errorf("no %s user", user)
	}
	uid, err := strconv.Atoi(f[2])
	switch {
	case err != nil:
		return fmt.Errorf("%s's uid %q: %w", user, f[2], err)
	case uid < uidMin(e):
		return fmt.Errorf("%s has uid %d, below UID_MIN %d, so it is a system account", user, uid, uidMin(e))
	case uid == sudoUID:
		return fmt.Errorf("%s is the account running sudo", user)
	case f[5] != "/home/"+user:
		return fmt.Errorf("%s's home is %s, not /home/%s", user, f[5], user)
	}
	// Lstat: a symlinked home is not a directory, and tar and userdel -r would follow it.
	if st, err := os.Lstat(e.Path(f[5])); err != nil || !st.IsDir() {
		return fmt.Errorf("%s's home %s is not a directory", user, f[5])
	}
	for _, g := range strings.Fields(e.Output(ctx, "id", "-nG", user)) {
		if slices.Contains(CardinalPrivileged, g) {
			return fmt.Errorf("%s is in group %s, which reaches root; remove it by hand if it is an old install", user, g)
		}
	}
	for _, s := range strings.Fields(e.Output(ctx, "loginctl", "show-user", user, "-p", "Sessions", "--value")) {
		if c := e.Output(ctx, "loginctl", "show-session", s, "-p", "Class", "--value"); !strings.HasPrefix(c, "manager") {
			return fmt.Errorf("%s has session %s (class %q) open; end it first", user, s, c)
		}
	}
	return nil
}

// RemoveOldUser archives an old install's home to a root-only tarball, then deletes the user as
// CardinalPurge does. Its processes stop first, so tar reads a home nothing writes to.
func RemoveOldUser(ctx context.Context, e *Env, user string, sudoUID int, now time.Time) (CardinalReport, error) {
	if err := CheckOldUser(ctx, e, user, sudoUID); err != nil {
		return CardinalReport{}, err
	}
	if err := stopUser(ctx, e, user); err != nil {
		return CardinalReport{}, err
	}
	archive := "/root/flocc-removed-" + user + "-" + now.Format("2006-01-02-150405") + ".tar.gz"
	// $1 and $2 keep the names out of the script's text; umask 077 makes the archive mode 600.
	if _, err := e.Must(ctx, "sh", "-c", `umask 077 && tar -czf "$1" -C /home -- "$2"`, "sh", archive, user); err != nil {
		return CardinalReport{}, fmt.Errorf("archiving /home/%s: %w", user, err)
	}
	r := CardinalReport{Steps: []ReportStep{{Name: "Archive", Detail: archive + ", mode 600", Done: true}}}
	if err := CardinalPurge(ctx, e, user); err != nil {
		return r, err
	}
	r.Steps = append(r.Steps, ReportStep{Name: "User", Detail: user + " and its home deleted", Done: true})
	r.Notes = []string{"revoke the old install's GitHub tokens and Linear key; this removal cannot"}
	return r, nil
}
```

In `installer/cmd/flocc-setup/cardinal.go`, keep the parsed `SUDO_UID` for the purge (replace the `if staged {` block):

```go
	sudoUID := -1
	if staged {
		if sudoUID, err = strconv.Atoi(os.Getenv("SUDO_UID")); err != nil {
			return fail(2, errors.New("run the root pass through sudo; SUDO_UID names the stage's owner"))
		}
		if err := steps.CheckCardinalStage(stage, sudoUID, files...); err != nil {
			return fail(2, err)
		}
	}
```

and the purge case:

```go
	case "cardinal-purge":
		if slices.Contains(steps.OldUsers, *user) {
			return finish(steps.RemoveOldUser(ctx, env, *user, sudoUID, time.Now()))
		}
		r := steps.CardinalReport{}
		err := steps.CardinalPurge(ctx, env, *user)
		if err == nil {
			r.Steps = []steps.ReportStep{{Name: "User", Detail: *user + " and its home deleted", Done: true}}
		}
		return finish(r, err)
```

`err` is already declared above (`exe, err := os.Executable()`), so `sudoUID, err = ...` assigns to it.

- [ ] **Step 4: Run the tests**

Run: `go test ./installer/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/steps installer/cmd/flocc-setup
git commit -s -m "steps: remove an old flocc user in the root pass after its own checks, home archived first"
```

---

### Task 4: cardinal runs removals in its root pass and exports what the wizard calls

**Files:**
- Modify: `installer/internal/cardinal/passes.go:337-393`
- Modify: `installer/internal/cardinal/commands.go`
- Test: `installer/internal/cardinal/cardinal_test.go`

**Interfaces:**
- Consumes: flocc-setup's `cardinal-purge` phase from Task 3.
- Produces:
  - `type SetupOptions struct { Only []string; Key string; Adopt bool; Remove []string }`
  - `func (d Deps) Setup(ctx context.Context, s saved.Saved, o SetupOptions) error`
  - `func (d Deps) Status(ctx context.Context, s saved.Saved) (steps.CardinalStatus, error)`
  - `func (d Deps) Login(ctx context.Context, s saved.Saved, email string) error`
  - `func (d Deps) RemoveOld(ctx context.Context, machine string, users []string) error`
  - These open the install's log when `d.Run` is nil, as `Main` does, and return errors already sanitized with `Log: <path>` on the last line.

- [ ] **Step 1: Write the failing tests**

In `cardinal_test.go`, change `prefixFake` so it records and answers `scp` as well as `go` (both carry temp-dir paths):

```go
// prefixFake answers go build and scp calls, whose paths are temp directories; they are still
// recorded.
type prefixFake struct{ *host.Fake }

func (p prefixFake) Run(ctx context.Context, c host.Cmd) (host.Result, error) {
	if c.Name == "go" || c.Name == "scp" {
		p.Fake.Run(ctx, c)
		return host.Result{}, nil
	}
	return p.Fake.Run(ctx, c)
}
```

Change the two existing `d.rootPass(...)` calls in `TestRootPassRefusesWithoutATerminal` and `TestRootPassRunsOneSudoFromTheMktempStage` to pass `[]rootCall{{Phase: "cardinal-root", User: "cardinal", Adopt: true}}` (the first test's `false` adopt becomes `Adopt: false`) in place of the phase and adopt arguments. Then add:

```go
func TestRootCallsRunRemovalsFirstAndOnlyWhatHasWork(t *testing.T) {
	rm := SetupOptions{Remove: []string{"linear-agents"}, Adopt: true}
	for _, c := range []struct {
		why  string
		o    SetupOptions
		want []rootCall
	}{
		{"", SetupOptions{}, nil},
		{"", rm, []rootCall{{Phase: "cardinal-purge", User: "linear-agents"}}},
		{"lingering off", SetupOptions{Adopt: true}, []rootCall{{Phase: "cardinal-root", User: "cardinal", Adopt: true}}},
		{"lingering off", rm, []rootCall{{Phase: "cardinal-purge", User: "linear-agents"}, {Phase: "cardinal-root", User: "cardinal", Adopt: true}}},
	} {
		if got := rootCalls("cardinal", c.why, c.o); !slices.Equal(got, c.want) {
			t.Errorf("why %q remove %v: %+v", c.why, c.o.Remove, got)
		}
	}
}

func TestRootPassRunsPurgeAndRootOnOneLineAndPrintsBothReports(t *testing.T) {
	f := host.NewFake()
	d, out := testDeps(t, f)
	d.TTY = true
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "flocc-setup"), []byte("x"), 0o755)
	f.On(d.owner(card.Machine, false, "mktemp -d /tmp/flocc-cardinal.XXXXXX").String(), 0, "/tmp/flocc-cardinal.Ab12Cd\n")
	f.On(d.scp(d.Mux, card.Machine+":/tmp/flocc-cardinal.Ab12Cd/", filepath.Join(bin, "flocc-setup"), filepath.Join(bin, "desktop.pub")).String(), 0, "")
	st := "/tmp/flocc-cardinal.Ab12Cd/flocc-setup"
	line := d.owner(card.Machine, true, "chmod 755 "+st+" && sudo "+st+" --phase cardinal-purge --user linear-agents && sudo "+st+" --phase cardinal-root --user cardinal --adopt && rm -rf /tmp/flocc-cardinal.Ab12Cd").String()
	purged := steps.CardinalReport{Steps: []steps.ReportStep{{Name: "Archive", Detail: "/root/flocc-removed-linear-agents-x.tar.gz", Done: true}}}
	root := steps.CardinalReport{Steps: []steps.ReportStep{{Name: "User", Detail: "exists", Done: true}}}
	f.OnInteractiveOut(line, purged.Line()+"\n"+root.Line()+"\n")
	calls := []rootCall{{Phase: "cardinal-purge", User: "linear-agents"}, {Phase: "cardinal-root", User: "cardinal", Adopt: true}}
	if err := d.rootPass(context.Background(), card, bin, "ssh-ed25519 AAAA k", calls); err != nil {
		t.Fatalf("%v; %v", err, f.Calls)
	}
	interactive := slices.DeleteFunc(slices.Clone(f.Calls), func(c string) bool { return !strings.HasPrefix(c, "interactive: ") })
	if len(interactive) != 1 || interactive[0] != "interactive: "+line {
		t.Fatalf("calls %v", f.Calls)
	}
	if !strings.Contains(out.String(), "Removed linear-agents") || !strings.Contains(out.String(), "flocc-removed-linear-agents-x.tar.gz") || !strings.Contains(out.String(), "Root pass") {
		t.Fatalf("out %q", out)
	}
}

func TestRemoveOldRunsOnlyThePurgeWithoutTheDesktopKey(t *testing.T) {
	f := host.NewFake()
	d, _ := testDeps(t, f)
	d.TTY, d.RepoRoot = true, func() (string, error) { return "/repo", nil }
	m := card.Machine
	f.On("ssh -O check "+m, 0, "")
	f.On(d.owner(m, false, "uname -m").String(), 0, "x86_64\n")
	f.On(d.owner(m, false, "mktemp -d /tmp/flocc-cardinal.XXXXXX").String(), 0, "/tmp/flocc-cardinal.Ab12Cd\n")
	d.Run = prefixFake{f}
	if err := d.RemoveOld(context.Background(), m, []string{"linear-agents"}); err != nil {
		t.Fatalf("%v; %v", err, f.Calls)
	}
	st := "/tmp/flocc-cardinal.Ab12Cd/flocc-setup"
	want := "interactive: " + d.owner(m, true, "chmod 755 "+st+" && sudo "+st+" --phase cardinal-purge --user linear-agents && rm -rf /tmp/flocc-cardinal.Ab12Cd").String()
	if !f.Ran(want) || slices.ContainsFunc(f.Calls, func(c string) bool { return strings.Contains(c, "desktop.pub") }) {
		t.Fatalf("calls %v", f.Calls)
	}
}

func TestStatusReturnsTheParsedReport(t *testing.T) {
	f := host.NewFake()
	d, _ := testDeps(t, f)
	f.On(d.user(card, false, setupPath+" --phase cardinal-status").String(), 0, `{"schema":1,"user":"cardinal","unit":"active","login":"none"}`)
	st, err := d.Status(context.Background(), card)
	if err != nil || st.Unit != "active" || st.Login != "none" {
		t.Fatalf("%+v %v", st, err)
	}
	f.On(d.user(card, false, setupPath+" --phase cardinal-status").String(), 255, "")
	if _, err := d.Status(context.Background(), card); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("err %v", err)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/internal/cardinal/`
Expected: compile errors (`rootCall`, `rootCalls`, `SetupOptions`, `RemoveOld`, `Status` undefined).

- [ ] **Step 3: Implement**

In `passes.go`, replace `rootPass` and `reportTap`:

```go
// rootCall is one sudo of a root pass: a machine phase run for one user.
type rootCall struct {
	Phase, User string
	Adopt       bool
}

// rootCalls are a setup's sudo calls: the agreed removals of old users, then cardinal's own root
// work when an outcome is missing (why is rootOutcomes'). None means no ssh -t and no password.
func rootCalls(user, why string, o SetupOptions) []rootCall {
	var calls []rootCall
	for _, u := range o.Remove {
		calls = append(calls, rootCall{Phase: "cardinal-purge", User: u})
	}
	if why != "" {
		calls = append(calls, rootCall{Phase: "cardinal-root", User: user, Adopt: o.Adopt})
	}
	return calls
}

// rootPass runs a setup's or purge's sudo calls from a stage only the owner's mktemp could have
// made, on one line in one ssh -t: sudo's ticket for that terminal covers every call after the
// first, so the owner types one password. A failed call stops the ones after it. The stage goes
// after a success and stays, with its log, after a failure.
func (d Deps) rootPass(ctx context.Context, s saved.Saved, bin, desktopKey string, calls []rootCall) error {
	if !d.TTY {
		return errors.New("the machine's sudo asks for a password; run this in a terminal")
	}
	dir, err := host.Must(ctx, d.Run, d.owner(s.Machine, false, "mktemp -d /tmp/flocc-cardinal.XXXXXX"))
	dir = strings.TrimSpace(dir)
	if err != nil {
		return err
	}
	if !steps.ValidCardinalStage(dir) {
		return fmt.Errorf("mktemp on %s returned %q", s.Machine, dir)
	}
	files := []string{filepath.Join(bin, "flocc-setup")}
	if desktopKey != "" {
		pub := filepath.Join(bin, "desktop.pub")
		if err := os.WriteFile(pub, []byte(desktopKey+"\n"), 0o600); err != nil {
			return err
		}
		files = append(files, pub)
	}
	if _, err := host.Must(ctx, d.Run, d.scp(d.Mux, s.Machine+":"+dir+"/", files...)); err != nil {
		return err
	}
	remote := "chmod 755 " + dir + "/flocc-setup"
	for _, c := range calls {
		remote += " && sudo " + dir + "/flocc-setup --phase " + c.Phase + " --user " + c.User
		if c.Adopt {
			remote += " --adopt"
		}
	}
	remote += " && rm -rf " + dir
	var reports []steps.CardinalReport
	tap := reportTap(d.Out, &reports)
	c := d.owner(s.Machine, true, remote)
	c.Stdout = tap
	err = d.Run.Interactive(ctx, c)
	tap.Flush()
	// Each call prints one report, in order, so the i-th report is the i-th call's.
	for i, r := range reports {
		title := "Root pass"
		if i < len(calls) && calls[i].Phase == "cardinal-purge" {
			title = "Removed " + calls[i].User
		}
		if len(r.Steps) > 0 || len(r.Notes) > 0 {
			d.printSteps(title, r)
		}
	}
	if err != nil {
		return fmt.Errorf("the root pass failed; its log is %s/setup.log on %s: %w", dir, s.Machine, err)
	}
	return nil
}

// reportTap passes a pass's output through as it comes, prompts included, and keeps each
// flocc-cardinal line in rs for printSteps.
func reportTap(out io.Writer, rs *[]steps.CardinalReport) *ship.LineTap {
	return &ship.LineTap{Out: out, Marker: steps.CardinalMarker, Take: func(line string) bool {
		got, ok := steps.ParseCardinalLine(line)
		if ok {
			*rs = append(*rs, got)
		}
		return ok
	}}
}
```

`login` in `commands.go` also uses `reportTap`: change its `var r steps.CardinalReport` / `reportTap(d.Out, &r)` to `var rs []steps.CardinalReport` / `reportTap(d.Out, &rs)`; it reads nothing from `r` afterwards (check, and if it does, use the last element).

In `commands.go`:

1. Add the options type and change `setup`'s signature and root-pass block:

```go
// SetupOptions are flocc cardinal setup's flags, and the old users flocc setup agreed to remove,
// which the root pass deletes before cardinal's own root work.
type SetupOptions struct {
	Only   []string
	Key    string
	Adopt  bool
	Remove []string
}
```

```go
func (d Deps) setup(ctx context.Context, s saved.Saved, o SetupOptions) error {
	key, err := d.desktopKey(o.Key)
```

Use `o.Only` where `only` was used. Replace the `if why := d.rootOutcomes(ctx, s); why != "" { ... } else { ... }` block with:

```go
	why := d.rootOutcomes(ctx, s)
	if calls := rootCalls(s.User, why, o); len(calls) > 0 {
		reasons := []string{}
		if len(o.Remove) > 0 {
			reasons = append(reasons, "removing "+strings.Join(o.Remove, ", "))
		}
		if why != "" {
			reasons = append(reasons, why)
		}
		fmt.Fprintf(d.Out, "\nRoot pass (%s): sudo asks for your password on %s once\n", strings.Join(reasons, "; "), s.Machine)
		if err := d.rootPass(ctx, s, bin, key, calls); err != nil {
			return err
		}
		if why != "" {
			if why := d.rootOutcomes(ctx, s); why != "" {
				return fmt.Errorf("after the root pass: %s. ssh %s may be refused by the machine's sshd (AllowUsers) or the tailnet policy; setup does not change either", why, s.Dest())
			}
		}
	} else {
		fmt.Fprintf(d.Out, "\nRoot pass: nothing to do\n")
	}
```

In `Main`, call `d.setup(ctx, s, SetupOptions{Only: only, Key: *key, Adopt: *adopt})`. In `remove`, call `d.rootPass(ctx, s, bin, "", []rootCall{{Phase: "cardinal-purge", User: s.User}})`.

2. Move the log opening out of `Main` into helpers, and make `Main` use `openLog`:

```go
func openLog(home, user string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(logPath(home, user)), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(logPath(home, user), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

// logged runs f as Main runs a subcommand: with a runner that logs to the install's log unless d
// has one, and with f's error sanitized and naming the log, since it carries cardinal's output.
func (d Deps) logged(user string, f func(Deps) error) error {
	if d.Run == nil {
		lf, err := openLog(d.Home, user)
		if err != nil {
			return err
		}
		defer lf.Close()
		d.Run = host.Exec{Log: lf}
	}
	if err := f(d); err != nil {
		return fmt.Errorf("%s\nLog: %s", saved.SanitizeBlock(err.Error(), 40), logPath(d.Home, user))
	}
	return nil
}
```

`Main`'s `if d.Run == nil { ... }` block becomes:

```go
	if d.Run == nil {
		f, err := openLog(d.Home, s.User)
		if err != nil {
			fmt.Fprintln(d.Err, "flocc cardinal:", err)
			return 1
		}
		defer f.Close()
		d.Run = host.Exec{Log: f}
	}
```

3. Split `status` into a reader and the printer, keeping its output exactly:

```go
// unreachable is a status that never ran; status() names setup as its remedy.
type unreachable struct{ error }

func (d Deps) readStatus(ctx context.Context, s saved.Saved) (steps.CardinalStatus, error) {
	// Full status health-checks every MCP server and host, so it gets longer than the outcome checks.
	ctx, cancel := context.WithTimeout(ctx, 3*reachLimit)
	defer cancel()
	var st steps.CardinalStatus
	res, err := d.Run.Run(ctx, d.user(s, false, setupPath+" --phase cardinal-status"))
	if err != nil || res.Code != 0 {
		why := passOutput(res, 5)
		if err != nil {
			why = err.Error()
		}
		return st, unreachable{fmt.Errorf("%s unreachable: %s", s.Dest(), why)}
	}
	if err := json.Unmarshal([]byte(res.Stdout), &st); err != nil {
		return st, fmt.Errorf("unreadable status from %s: %v", s.Dest(), err)
	}
	if st.Schema != steps.CardinalStatusSchema {
		return st, fmt.Errorf("%s reports status schema %d; run flocc cardinal setup --user %s", s.Dest(), st.Schema, s.User)
	}
	return st, nil
}

func (d Deps) status(ctx context.Context, s saved.Saved) int {
	st, err := d.readStatus(ctx, s)
	if err != nil {
		fmt.Fprintln(d.Out, err)
		var u unreachable
		if errors.As(err, &u) {
			fmt.Fprintf(d.Out, "  remedy: flocc cardinal setup --user %s\n", s.User)
		}
		return 1
	}
	out, code := RenderStatus(st)
	fmt.Fprint(d.Out, out)
	return code
}
```

4. The exported entry points:

```go
// Setup, Status, Login and RemoveOld are flocc setup's way in: Main's subcommands without its
// flag parsing.
func (d Deps) Setup(ctx context.Context, s saved.Saved, o SetupOptions) error {
	return d.logged(s.User, func(d Deps) error { return d.setup(ctx, s, o) })
}

func (d Deps) Status(ctx context.Context, s saved.Saved) (steps.CardinalStatus, error) {
	var st steps.CardinalStatus
	err := d.logged(s.User, func(d Deps) error {
		var err error
		st, err = d.readStatus(ctx, s)
		return err
	})
	return st, err
}

func (d Deps) Login(ctx context.Context, s saved.Saved, email string) error {
	return d.logged(s.User, func(d Deps) error { return d.login(ctx, s, email) })
}

// RemoveOld runs only the purge of old users on machine, for a machine whose cardinal is healthy,
// declined or elsewhere. It needs no incus, so a machine without it can still be cleaned.
func (d Deps) RemoveOld(ctx context.Context, machine string, users []string) error {
	s := saved.Saved{Machine: machine, User: saved.DefaultUser}
	return d.logged(s.User, func(d Deps) error {
		if err := ship.Connect(ctx, d.Run, ship.Dest{SSH: machine, Opts: d.Mux}, machine, d.TTY); err != nil {
			return err
		}
		bin, err := d.machineBuild(ctx, s)
		if err != nil {
			return err
		}
		defer os.RemoveAll(bin)
		fmt.Fprintf(d.Out, "\nRoot pass (removing %s): sudo asks for your password on %s once\n", strings.Join(users, ", "), machine)
		calls := rootCalls(s.User, "", SetupOptions{Remove: users})
		return d.rootPass(ctx, s, bin, "", calls)
	})
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./installer/...`
Expected: PASS, including the existing `TestMainRemoveMarksTheSavedFileRemoved` with the new `prefixFake`.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/cardinal
git commit -s -m "cardinal: removals share the root pass's one ssh -t; Setup, Status, Login and RemoveOld for flocc setup"
```

---

### Task 5: The wizard: runner, checkout, keys, the first host and each host's machine

**Files:**
- Create: `installer/internal/cli/setup.go`, `installer/internal/cli/setup_ops.go`, `installer/internal/cli/setup_test.go`
- Modify: `installer/internal/cli/prompt.go`, `installer/internal/cli/host_test.go` (the `hostScript` fake), `installer/internal/cli/cli.go`, `installer/internal/host/version.go`

**Interfaces:**
- Consumes: `hosts.Host.Machine`, `hosts.MachineNone` (Task 1); `create.Run`, `create.Logged`, `create.Preconditions`, `create.ParseArgs` (Task 2).
- Produces (later tasks extend these):
  - `prompter` gains `Ask(title, help string) (string, error)` and `Choose(title string, options []string) (int, error)`.
  - `host.Version(s string) ([3]int, bool)`.
  - `setupOps` struct of funcs; `newSetupOps(d *deps) setupOps`; `runSetup(ctx, d, ops) int`; `setupCmd(ctx, d, args) int`.
  - `wizard` with `row(state rowState, name, detail string)`, `fail(name string, err error)`, `probe(machine string) machineState`, `stopped bool`, `failed map[string]bool`, `version [3]int`.
  - Test helpers in `setup_test.go`: `opsLog`, `doneOps(l *opsLog) setupOps`, `setupScript`, `setupDeps(t, answers) (*deps, *bytes.Buffer, *setupScript)`, `inOrder(t, calls, want...)`. Later tasks add defaults for their new fields to `doneOps`, so every earlier test keeps compiling and running.

- [ ] **Step 1: Write the failing tests**

Create `installer/internal/cli/setup_test.go`:

```go
package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"charm.land/huh/v2"

	"github.com/NovusEdge/flocc/installer/internal/create"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
)

type opsLog struct{ calls []string }

func (l *opsLog) add(format string, a ...any) { l.calls = append(l.calls, fmt.Sprintf(format, a...)) }

func (l *opsLog) has(prefix string) bool {
	return slices.ContainsFunc(l.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

// inOrder fails unless want appear in calls in this order; other calls may come between.
func inOrder(t *testing.T, calls []string, want ...string) {
	t.Helper()
	i := 0
	for _, c := range calls {
		if i < len(want) && c == want[i] {
			i++
		}
	}
	if i < len(want) {
		t.Fatalf("missing %q in order; calls:\n  %s", want[i], strings.Join(calls, "\n  "))
	}
}

var nimble = hosts.Host{Name: "flocc-nimble", SSH: "root@flocc-nimble", Machine: "zer0-day-nimble"}

// doneOps is a desktop with nothing left to do: one host on zer0-day-nimble, every key stored.
func doneOps(l *opsLog) setupOps {
	return setupOps{
		checkout:   func() (string, string, error) { return "/repo", "0.8.0", nil },
		fillKeys:   func(_ context.Context, s []keys.Slot) error { l.add("keys %v", s); return nil },
		checkKeys:  func(context.Context) []string { return nil },
		hosts:      func() []hosts.Host { return []hosts.Host{nimble} },
		saveHost:   func(h hosts.Host) error { l.add("save %s machine=%s", h.Name, h.Machine); return nil },
		createLogs: func() []string { return nil },
		machine: func(_ context.Context, m string) machineState {
			l.add("machine %s", m)
			return machineState{Sudo: true}
		},
		create: func(_ context.Context, s create.Spec) error { l.add("create %s %s", s.Machine, s.Name); return nil },
		add:    func(_ context.Context, name string, r hosts.Host) error { l.add("add %s %s", name, r.Target()); return nil },
		ship:   func(_ context.Context, name string) error { l.add("ship %s", name); return nil },
	}
}

// setupScript answers by title prefix: a bool for Confirm, a string for Ask, an int for Choose.
// An unscripted Confirm takes its default, so a test can see what the default is; an unscripted
// Ask is left empty. The other prompter methods are nil and panic: the wizard must not call them.
type setupScript struct {
	prompter
	answers map[string]any
	err     error // returned by the prompt whose title starts with errAt
	errAt   string
	asked   []string
}

func (s *setupScript) answer(title string) (any, error) {
	s.asked = append(s.asked, title)
	if s.errAt != "" && strings.HasPrefix(title, s.errAt) {
		return nil, s.err
	}
	for p, v := range s.answers {
		if strings.HasPrefix(title, p) {
			return v, nil
		}
	}
	return nil, nil
}

func (s *setupScript) Confirm(title string, def bool) (bool, error) {
	v, err := s.answer(title)
	if v == nil || err != nil {
		return def, err
	}
	return v.(bool), nil
}

func (s *setupScript) Ask(title, _ string) (string, error) {
	v, err := s.answer(title)
	if v == nil || err != nil {
		return "", err
	}
	return v.(string), nil
}

func (s *setupScript) Choose(title string, _ []string) (int, error) {
	v, err := s.answer(title)
	if err != nil {
		return 0, err
	}
	if v == nil {
		return 0, fmt.Errorf("unscripted choice %q", title)
	}
	return v.(int), nil
}

func setupDeps(t *testing.T, answers map[string]any) (*deps, *bytes.Buffer, *setupScript) {
	t.Helper()
	d, out, _ := testDeps(t)
	p := &setupScript{answers: answers}
	d.prompt, d.tty = p, true
	return d, out, p
}

func TestSetupNeedsATerminal(t *testing.T) {
	d, _, _ := setupDeps(t, nil)
	d.tty = false
	if code := setupCmd(context.Background(), d, nil); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestSetupStopsWithoutTheCheckout(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.checkout = func() (string, string, error) { return "", "", errors.New("not a git checkout") }
	d, out, _ := setupDeps(t, nil)
	if code := runSetup(context.Background(), d, ops); code != 1 || l.has("keys") || !strings.Contains(out.String(), "not a git checkout") {
		t.Fatalf("exit %d calls %v out %q", code, l.calls, out)
	}
}

func TestSetupCreatesTheFirstHostBetweenTheKeyHalves(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	made := false
	ops.hosts = func() []hosts.Host {
		if made {
			return []hosts.Host{nimble}
		}
		return nil
	}
	ops.create = func(_ context.Context, s create.Spec) error {
		l.add("create %s %s", s.Machine, s.Name)
		made = true
		return nil
	}
	d, _, p := setupDeps(t, map[string]any{"No hosts yet": 0, "Host name": "flocc-nimble", "Machine to make": "zer0-day-nimble"})
	runSetup(context.Background(), d, ops)
	inOrder(t, l.calls,
		fmt.Sprintf("keys %v", []keys.Slot{keys.Linear, keys.Agent}),
		"machine zer0-day-nimble",
		"create zer0-day-nimble flocc-nimble",
		fmt.Sprintf("keys %v", []keys.Slot{keys.Publish, keys.Ntfy}))
	if slices.ContainsFunc(p.asked, func(q string) bool { return strings.HasPrefix(q, "Which machine holds") }) {
		t.Fatal("asked the machine of a host create just made on one")
	}
}

func TestSetupRefusesCreateWithoutPasswordlessSudo(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.hosts = func() []hosts.Host { return nil }
	ops.machine = func(context.Context, string) machineState { return machineState{Sudo: false} }
	d, out, _ := setupDeps(t, map[string]any{"No hosts yet": 0, "Host name": "flocc-nimble", "Machine to make": "zer0-day-nimble"})
	if code := runSetup(context.Background(), d, ops); code != 1 || l.has("create") || !strings.Contains(out.String(), "passwordless sudo") {
		t.Fatalf("exit %d calls %v out %q", code, l.calls, out)
	}
}

func TestSetupOffersToContinueAStoppedCreate(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.hosts = func() []hosts.Host { return nil }
	ops.createLogs = func() []string { return []string{"flocc-nimble"} }
	d, _, p := setupDeps(t, map[string]any{"Continue creating flocc-nimble": true, "Machine to make": "zer0-day-nimble"})
	runSetup(context.Background(), d, ops)
	if !l.has("create zer0-day-nimble flocc-nimble") || slices.ContainsFunc(p.asked, func(q string) bool { return strings.HasPrefix(q, "No hosts yet") }) {
		t.Fatalf("calls %v asked %v", l.calls, p.asked)
	}
	// Unanswered, the offer defaults to no and the wizard asks create-or-add.
	l = opsLog{}
	d, _, p = setupDeps(t, map[string]any{"No hosts yet": 1, "Host name": "box", "How to reach": "root@box"})
	runSetup(context.Background(), d, ops)
	if l.has("create") || !slices.ContainsFunc(p.asked, func(q string) bool { return strings.HasPrefix(q, "No hosts yet") }) {
		t.Fatalf("calls %v asked %v", l.calls, p.asked)
	}
}

func TestSetupAddsAnExistingHostAndShipsIt(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.hosts = func() []hosts.Host { return nil }
	d, _, _ := setupDeps(t, map[string]any{"No hosts yet": 1, "Host name": "box", "How to reach": "root@box"})
	runSetup(context.Background(), d, ops)
	inOrder(t, l.calls, "add box root@box", "ship box")
}

func TestSetupAsksAHostsMachineOnceAndSavesNoneButNotAnEmptyAnswer(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.hosts = func() []hosts.Host {
		return []hosts.Host{{Name: "a", SSH: "root@a"}, {Name: "b", SSH: "root@b"}, {Name: "sb", Daytona: "sb"}, nimble}
	}
	d, _, p := setupDeps(t, map[string]any{"Which machine holds a?": "none"})
	runSetup(context.Background(), d, ops)
	if !l.has("save a machine=none") || l.has("save b") || l.has("save sb") || l.has("save flocc-nimble") {
		t.Fatalf("calls %v", l.calls)
	}
	n := 0
	for _, q := range p.asked {
		if strings.HasPrefix(q, "Which machine holds") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("asked %v", p.asked)
	}
}

func TestSetupStopsAtACancelledPrompt(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.hosts = func() []hosts.Host { return []hosts.Host{{Name: "a", SSH: "root@a"}} }
	d, out, p := setupDeps(t, nil)
	p.errAt, p.err = "Which machine holds", huh.ErrUserAborted
	if code := runSetup(context.Background(), d, ops); code != 1 || l.has(fmt.Sprintf("keys %v", []keys.Slot{keys.Publish, keys.Ntfy})) || !strings.Contains(out.String(), "Stopped") {
		t.Fatalf("exit %d calls %v out %q", code, l.calls, out)
	}
}
```

Add `Ask` and `Choose` to the `hostScript` fake in `host_test.go` so it still satisfies `prompter`:

```go
func (s *hostScript) Ask(title, _ string) (string, error) {
	s.t.Helper()
	s.t.Fatalf("unscripted question: %q", title)
	return "", nil
}

func (s *hostScript) Choose(title string, _ []string) (int, error) {
	s.t.Helper()
	s.t.Fatalf("unscripted choice: %q", title)
	return 0, nil
}
```

Add a test for `host.Version` to `installer/internal/host/host_test.go`:

```go
func TestVersionTakesTheFirstTriple(t *testing.T) {
	if v, ok := Version("flocc 0.8.0"); !ok || v != [3]int{0, 8, 0} {
		t.Fatalf("%v %v", v, ok)
	}
	if _, ok := Version("flocc dev"); ok {
		t.Fatal("parsed a version from none")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/internal/cli/ ./installer/internal/host/`
Expected: compile errors (`setupOps`, `runSetup`, `setupCmd`, `machineState`, `Version` undefined).

- [ ] **Step 3: Implement**

`installer/internal/host/version.go`:

```go
// Version is the first x.y.z in s.
func Version(s string) ([3]int, bool) {
	m := semver.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := range 3 {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

// AtLeast reports whether the first x.y.z in s is at least min.
func AtLeast(s string, min [3]int) bool {
	v, ok := Version(s)
	if !ok {
		return false
	}
	for i := range 3 {
		if v[i] != min[i] {
			return v[i] > min[i]
		}
	}
	return true
}
```

`prompt.go`, extend the interface and `huhPrompt`:

```go
type prompter interface {
	keys.Prompt
	Repos(a *answers.Answers, found []answers.Repo, defaults func(url string) (base string, draft bool)) error
	Team(teams []linearapi.Team) (linearapi.Team, error)
	Note(title, body string)
	// Ask reads one line with echo on; "" when it is left empty.
	Ask(title, help string) (string, error)
	// Choose returns the index of the option picked.
	Choose(title string, options []string) (int, error)
}
```

```go
func (p huhPrompt) Ask(title, help string) (string, error) {
	if !p.IsTerminal() {
		return "", errNoTerminal
	}
	var v string
	err := ui.Form(huh.NewGroup(huh.NewInput().Title(title).Description(help).Value(&v))).Run()
	return v, err
}

func (p huhPrompt) Choose(title string, options []string) (int, error) {
	if !p.IsTerminal() {
		return 0, errNoTerminal
	}
	opts := make([]huh.Option[int], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o, i)
	}
	var i int
	err := ui.Form(huh.NewGroup(huh.NewSelect[int]().Title(title).Options(opts...).Value(&i))).Run()
	return i, err
}
```

Create `installer/internal/cli/setup_ops.go`:

```go
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/NovusEdge/flocc/installer/internal/create"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/ship"
)

// setupOps are the existing functions flocc setup calls, as fields so its tests can replace each.
type setupOps struct {
	checkout   func() (root, version string, err error)
	fillKeys   func(ctx context.Context, slots []keys.Slot) error
	checkKeys  func(ctx context.Context) []string
	hosts      func() []hosts.Host
	saveHost   func(h hosts.Host) error
	createLogs func() []string
	machine    func(ctx context.Context, machine string) machineState
	create     func(ctx context.Context, spec create.Spec) error
	add        func(ctx context.Context, name string, reach hosts.Host) error
	ship       func(ctx context.Context, name string) error
}

// machineState is what a machine allows. Err is set when ssh could not reach it.
type machineState struct {
	Err   error
	Sudo  bool  // sudo -n works there
	Incus error // nil when incus runs as an administrator, else the fix
}

func newSetupOps(d *deps) setupOps {
	mux, _ := ship.SSHMux()
	return setupOps{
		checkout:   func() (string, string, error) { return checkoutVersion(d) },
		fillKeys:   func(ctx context.Context, s []keys.Slot) error { return keys.Fill(ctx, keysEnv(d), mapped(listHosts(d)), s) },
		checkKeys:  func(ctx context.Context) []string { return keys.Check(ctx, keysEnv(d), mapped(listHosts(d))) },
		hosts:      func() []hosts.Host { return listHosts(d) },
		saveHost:   func(h hosts.Host) error { return hosts.Save(hosts.Dir(d.home), h) },
		createLogs: create.Logged,
		machine:    func(ctx context.Context, m string) machineState { return probeMachine(ctx, d, mux, m) },
		create:     func(ctx context.Context, s create.Spec) error { return runCreate(ctx, d, s) },
		add:        func(ctx context.Context, name string, reach hosts.Host) error { return addHost(ctx, d, name, reach) },
		ship:       func(ctx context.Context, name string) error { return shipHost(ctx, d, name) },
	}
}

var pyVersion = regexp.MustCompile(`(?m)^version = "([^"]+)"`)

// checkoutVersion is the flocc checkout this runs in, which ship, create and cardinal build from,
// and the version its pyproject.toml declares.
func checkoutVersion(d *deps) (string, string, error) {
	root, err := d.repoRoot()
	if err != nil {
		return "", "", fmt.Errorf("flocc setup builds from the flocc checkout it runs in; cd into it: %w", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "pyproject.toml"))
	if err != nil {
		return root, "", err
	}
	m := pyVersion.FindSubmatch(b)
	if m == nil {
		return root, "", fmt.Errorf("%s/pyproject.toml declares no version", root)
	}
	return root, string(m[1]), nil
}

// owner runs commands as the owner on machine, as host create's captured commands do.
func owner(d *deps, mux []string, machine string) create.Remote {
	return create.Remote{R: d.run, Target: machine, Opts: append([]string{"-o", "BatchMode=yes"}, mux...)}
}

// probeMachine opens the machine's shared connection on this terminal, so a host-key question or
// a Tailscale approval can be answered, then reads what the machine allows.
func probeMachine(ctx context.Context, d *deps, mux []string, machine string) machineState {
	if err := ship.Connect(ctx, d.run, ship.Dest{SSH: machine, Opts: mux}, machine, d.tty); err != nil {
		return machineState{Err: err}
	}
	sudo, incus := create.Preconditions(ctx, owner(d, mux, machine), machine)
	return machineState{Sudo: sudo, Incus: incus}
}

// runCreate is flocc host create <machine> <name> after its argument checks.
func runCreate(ctx context.Context, d *deps, spec create.Spec) error {
	inst := hostInstaller{d: d}
	if err := inst.Ready(); err != nil {
		return err
	}
	if _, err := inst.HostKnown(spec.Name, "root@"+spec.Name); err != nil {
		return err
	}
	logPath, err := create.Run(ctx, spec, inst, d.out, d.tty)
	if err != nil && logPath != "" {
		return fmt.Errorf("%w\nFull log: %s", err, logPath)
	}
	return err
}
```

Create `installer/internal/cli/setup.go`:

```go
package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"charm.land/huh/v2"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/create"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
)

type rowState int

const (
	rowDone rowState = iota
	// rowSkipped is a step left as it is with nothing wrong: the owner said no, or there is nothing
	// the wizard could act on. It does not fail the run.
	rowSkipped
	rowPending // left to do; the detail says what
	rowFailed
)

type setupRow struct {
	state        rowState
	name, detail string
}

type wizard struct {
	ctx     context.Context
	d       *deps
	ops     setupOps
	rows    []setupRow
	version [3]int
	stopped bool                    // a prompt was cancelled: nothing more is asked or run
	failed  map[string]bool         // by saved.HostPart: machines whose host failed, which steps 4-6 skip
	probed  map[string]machineState // by saved.HostPart
}

func setupCmd(ctx context.Context, d *deps, args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(d.err, "usage: flocc setup")
		return 2
	}
	if !d.tty {
		fmt.Fprintln(d.err, "flocc setup asks as it goes; run it in a terminal")
		return 2
	}
	return runSetup(ctx, d, newSetupOps(d))
}

func runSetup(ctx context.Context, d *deps, ops setupOps) int {
	w := &wizard{ctx: ctx, d: d, ops: ops, failed: map[string]bool{}, probed: map[string]machineState{}}
	w.run()
	return w.summary()
}

func (w *wizard) run() {
	if !w.checkout() || !w.fillKeys([]keys.Slot{keys.Linear, keys.Agent}, false) {
		return
	}
	list := w.hostFiles()
	if w.stopped || len(list) == 0 {
		return
	}
	// The publish token's page lists the mapped repos, so it comes after the host.
	w.fillKeys([]keys.Slot{keys.Publish, keys.Ntfy}, true)
}

func (w *wizard) row(state rowState, name, detail string) {
	w.rows = append(w.rows, setupRow{state, name, detail})
}

// fail records err as name's failure. A cancelled prompt or an empty paste stops the whole run.
func (w *wizard) fail(name string, err error) {
	if errors.Is(err, huh.ErrUserAborted) || errors.Is(err, context.Canceled) || errors.Is(err, keys.ErrStopped) {
		w.stopped = true
	}
	w.row(rowFailed, name, err.Error())
}

func (w *wizard) summary() int {
	o := w.d.o()
	marks := map[rowState]string{rowDone: o.OK("✓"), rowSkipped: "·", rowPending: o.Warn("…"), rowFailed: o.Fail("✗")}
	fmt.Fprintln(w.d.out, "\nflocc setup")
	code := 0
	for _, r := range w.rows {
		o.Line("%s %-13s %s", marks[r.state], r.name, r.detail)
		if r.state >= rowPending {
			code = 1
		}
	}
	if w.stopped {
		o.Line("Stopped. Run flocc setup again to go on.")
		code = 1
	}
	return code
}

func (w *wizard) checkout() bool {
	root, v, err := w.ops.checkout()
	if err != nil {
		w.row(rowFailed, "checkout", err.Error())
		return false
	}
	ver, ok := host.Version(v)
	if !ok {
		w.row(rowFailed, "checkout", fmt.Sprintf("%s: version %q is not x.y.z", root, v))
		return false
	}
	w.version = ver
	w.row(rowDone, "checkout", root+" at "+v)
	return true
}

// fillKeys asks for the keys in slots the store lacks; with check it then checks every key
// against what the host files map.
func (w *wizard) fillKeys(slots []keys.Slot, check bool) bool {
	if err := w.ops.fillKeys(w.ctx, slots); err != nil {
		w.fail("keys", err)
		return false
	}
	var titles []string
	for _, s := range slots {
		titles = append(titles, s.Title())
	}
	if check {
		if p := w.ops.checkKeys(w.ctx); len(p) > 0 {
			w.row(rowPending, "keys", strings.Join(p, "\n"))
			return true
		}
	}
	w.row(rowDone, "keys", strings.Join(titles, ", "))
	return true
}

// probe reads a machine once per run.
func (w *wizard) probe(machine string) machineState {
	k := saved.HostPart(machine)
	if s, ok := w.probed[k]; ok {
		return s
	}
	s := w.ops.machine(w.ctx, machine)
	w.probed[k] = s
	return s
}

// hostFiles is every host file, after making the first host when there is none and asking each
// ssh host without a machine which one holds it.
func (w *wizard) hostFiles() []hosts.Host {
	list := w.ops.hosts()
	if len(list) == 0 {
		if !w.firstHost() {
			return nil
		}
		list = w.ops.hosts()
	}
	for i := range list {
		if w.stopped {
			return nil
		}
		w.placeHost(&list[i])
	}
	return list
}

func (w *wizard) firstHost() bool {
	// A create that stopped before host add left its log and no host file.
	for _, name := range w.ops.createLogs() {
		again, err := w.d.prompt.Confirm("Continue creating "+name+"? A host create for it stopped before the host was added", false)
		if err != nil {
			w.fail("host", err)
			return false
		}
		if again {
			return w.createHost(name)
		}
	}
	i, err := w.d.prompt.Choose("No hosts yet", []string{
		"Create one as an Incus VM on a machine of yours",
		"Add one that already runs flocc, or will after ship (ssh or Daytona)",
	})
	if err != nil {
		w.fail("host", err)
		return false
	}
	if i == 1 {
		return w.addExisting()
	}
	name, err := w.d.prompt.Ask("Host name", "2 to 63 lowercase letters, digits and '-'; also its Incus and Tailscale name")
	if err != nil {
		w.fail("host", err)
		return false
	}
	return w.createHost(strings.TrimSpace(name))
}

func (w *wizard) createHost(name string) bool {
	m, err := w.d.prompt.Ask("Machine to make "+name+" on", "the ssh destination of your account there, such as you@box")
	if err != nil {
		w.fail("host", err)
		return false
	}
	spec, err := create.ParseArgs([]string{strings.TrimSpace(m), name})
	if err != nil {
		w.row(rowFailed, "host", err.Error())
		return false
	}
	ms := w.probe(spec.Machine)
	switch {
	case ms.Err != nil:
		w.row(rowFailed, "host", ms.Err.Error())
		return false
	case !ms.Sudo:
		w.row(rowFailed, "host", fmt.Sprintf("host create needs passwordless sudo on %s: its firewall and Docker steps run sudo -n even to check. "+
			"Add a file in /etc/sudoers.d/ there with visudo, or add a host made another way", spec.Machine))
		return false
	case ms.Incus != nil:
		w.row(rowFailed, "host", ms.Incus.Error())
		return false
	}
	if err := w.ops.create(w.ctx, spec); err != nil {
		w.fail("host", fmt.Errorf("create %s: %w", name, err))
		return false
	}
	w.row(rowDone, "host", "created "+name+" on "+spec.Machine)
	return true
}

func (w *wizard) addExisting() bool {
	name, err := w.d.prompt.Ask("Host name", "what flocc calls it, and its file name")
	if err != nil {
		w.fail("host", err)
		return false
	}
	reach, err := w.d.prompt.Ask("How to reach it", "an ssh destination such as root@box, or daytona:<sandbox>")
	if err != nil {
		w.fail("host", err)
		return false
	}
	name, reach = strings.TrimSpace(name), strings.TrimSpace(reach)
	var h hosts.Host
	if sb, ok := strings.CutPrefix(reach, "daytona:"); ok {
		h.Daytona = sb
	} else {
		h.SSH = reach
	}
	if err := w.ops.add(w.ctx, name, h); err != nil {
		w.fail("host", fmt.Errorf("host add %s: %w", name, err))
		return false
	}
	if err := w.ops.ship(w.ctx, name); err != nil {
		w.fail("host", fmt.Errorf("ship %s: %w", name, err))
		return false
	}
	w.row(rowDone, "host", "added and shipped "+name)
	return true
}

// placeHost asks once which machine holds an ssh host and saves the answer, none included. An
// empty answer saves nothing, so it is asked again next run.
func (w *wizard) placeHost(h *hosts.Host) {
	if h.Machine != "" || h.Daytona != "" {
		return
	}
	m, err := w.d.prompt.Ask("Which machine holds "+h.Name+"?",
		"the ssh destination of your account there, such as you@box; none for a host not on one of your machines")
	if err != nil {
		w.fail(h.Name, err)
		return
	}
	m = strings.TrimSpace(m)
	switch {
	case m == "":
		return
	case m != hosts.MachineNone && !hosts.ValidTarget(m):
		w.row(rowPending, h.Name, fmt.Sprintf("%q is not an ssh destination; asked again next run", m))
		return
	}
	h.Machine = m
	if err := w.ops.saveHost(*h); err != nil {
		w.row(rowFailed, h.Name, "saving its machine: "+err.Error())
	}
}

// machineOf is the machine holding h, or "" when it is on none of the owner's or not known yet.
func machineOf(h hosts.Host) string {
	if h.Machine == hosts.MachineNone {
		return ""
	}
	return h.Machine
}

// machinesOf is each machine holding a host, in host order, once: owner@box and box are one.
func machinesOf(list []hosts.Host) []string {
	var out []string
	for _, h := range list {
		m := machineOf(h)
		if m != "" && !slices.ContainsFunc(out, func(o string) bool { return saved.HostPart(o) == saved.HostPart(m) }) {
			out = append(out, m)
		}
	}
	return out
}
```

`machinesOf` is used from Task 7 on; if `go vet` complains it is unused in this task, leave it (vet does not flag unused functions; the compiler does not either).

In `cli.go`, add the verb and its usage line:

```go
	case "setup":
		return setupCmd(ctx, d, rest[1:])
```

and in `usage`, as the first desktop command:

```
  flocc setup                        everything below in order: keys, a host, old versions, cardinal, logins
```

- [ ] **Step 4: Run the tests**

Run: `go test ./installer/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/cli installer/internal/host
git commit -s -m "cli: flocc setup, from the checkout and keys to the first host and each host's machine"
```

---

### Task 6: Step 2 reads each host and repairs what ship can

**Files:**
- Modify: `installer/internal/cli/setup.go`, `installer/internal/cli/setup_ops.go`, `installer/internal/cli/setup_test.go`

**Interfaces:**
- Consumes: Task 5's `wizard`, `setupOps`, `doneOps`, `host.AtLeast`.
- Produces:
  - `setupOps.readHost func(ctx context.Context, h hosts.Host) hostState`, `setupOps.offline func(ctx context.Context, name string) bool`
  - `type hostState struct { Err, Version string; Login *string; Doctor []string; LowDisk string }`
  - `type checkedHost struct { h hosts.Host; st hostState }`; `(w *wizard) checkHosts(list []hosts.Host) []checkedHost`
  - `doctorFails(out string) (fails []string, disk string)`; `tailnetOffline(ctx, r host.Runner, name string) bool`

- [ ] **Step 1: Write the failing tests**

In `setup_test.go`, add to `doneOps`:

```go
		readHost: func(_ context.Context, h hosts.Host) hostState {
			l.add("read %s", h.Name)
			full := "full"
			return hostState{Version: "0.8.0", Login: &full}
		},
		offline: func(context.Context, string) bool { return false },
```

Then add:

```go
func TestSetupShipsAnOldOrFailingHostAndReportsTheRest(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	none := "none"
	ops.hosts = func() []hosts.Host {
		var list []hosts.Host
		for _, n := range []string{"old", "broken", "nologin", "full-disk", "gone"} {
			list = append(list, hosts.Host{Name: n, SSH: "root@" + n, Machine: hosts.MachineNone})
		}
		return list
	}
	reads := map[string][]hostState{
		"old":       {{Version: "0.7.0"}, {Version: "0.8.0"}},
		"broken":    {{Version: "0.8.0", Doctor: []string{"tmux: no server"}}, {Version: "0.8.0"}},
		"nologin":   {{Version: "0.8.0", Login: &none}},
		"full-disk": {{Version: "0.8.0", LowDisk: "disk: 1.0 GB free"}},
		"gone":      {{Err: "ssh: connect timed out"}},
	}
	ops.readHost = func(_ context.Context, h hosts.Host) hostState {
		l.add("read %s", h.Name)
		st := reads[h.Name][0]
		if len(reads[h.Name]) > 1 {
			reads[h.Name] = reads[h.Name][1:]
		}
		return st
	}
	ops.offline = func(_ context.Context, name string) bool { return name == "gone" }
	d, out, _ := setupDeps(t, nil)
	code := runSetup(context.Background(), d, ops)
	if code != 1 || !l.has("ship old") || !l.has("ship broken") || l.has("ship nologin") || l.has("ship full-disk") || l.has("ship gone") {
		t.Fatalf("exit %d calls %v", code, l.calls)
	}
	for _, want := range []string{"admin/machines", "gone.toml", "disk: 1.0 GB free"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in %q", want, out)
		}
	}
}

func TestSetupFailsAHostStillFailingAfterShip(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.readHost = func(_ context.Context, h hosts.Host) hostState {
		l.add("read %s", h.Name)
		return hostState{Version: "0.8.0", Doctor: []string{"repo flocc: cannot fetch"}}
	}
	d, out, _ := setupDeps(t, nil)
	if code := runSetup(context.Background(), d, ops); code != 1 || strings.Count(strings.Join(l.calls, "\n"), "ship flocc-nimble") != 1 || !strings.Contains(out.String(), "cannot fetch") {
		t.Fatalf("exit %d calls %v out %q", code, l.calls, out)
	}
}

func TestDoctorFailsLeavesTheLoginAndDiskOutOfShip(t *testing.T) {
	out := "ok    git: /usr/bin/git\nFAIL  claude login: none; run flocc login\nFAIL  disk: 1.0 GB free under /w, below min_free_disk_gb 5\n" +
		"FAIL  tmux: no server; systemctl --user start flocc-tmux.service\n15 checks, 3 failed\n"
	fails, disk := doctorFails(out)
	if !slices.Equal(fails, []string{"tmux: no server; systemctl --user start flocc-tmux.service"}) || !strings.HasPrefix(disk, "disk: 1.0 GB free") {
		t.Fatalf("%q %q", fails, disk)
	}
}

func TestTailnetOfflineFindsThePeerByName(t *testing.T) {
	f := host.NewFake().On("tailscale status --json", 0,
		`{"Peer":{"k1":{"HostName":"la-nimble","DNSName":"la-nimble.tail.ts.net.","Online":false},"k2":{"HostName":"flocc-nimble","DNSName":"flocc-nimble.tail.ts.net.","Online":true}}}`)
	if !tailnetOffline(context.Background(), f, "la-nimble") || tailnetOffline(context.Background(), f, "flocc-nimble") || tailnetOffline(context.Background(), f, "nope") {
		t.Fatal("wrong")
	}
}
```

Add `"github.com/NovusEdge/flocc/installer/internal/host"` to the test imports.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/internal/cli/ -run 'Setup|Doctor|Tailnet'`
Expected: compile errors (`hostState`, `readHost`, `doctorFails`, `tailnetOffline` undefined).

- [ ] **Step 3: Implement**

Add to `setupOps`:

```go
	readHost func(ctx context.Context, h hosts.Host) hostState
	offline  func(ctx context.Context, name string) bool
```

and to `newSetupOps`:

```go
		readHost: func(ctx context.Context, h hosts.Host) hostState { return readHost(ctx, d, h) },
		offline:  func(ctx context.Context, name string) bool { return tailnetOffline(ctx, d.run, name) },
```

In `setup_ops.go` (add imports `encoding/json`, `strings`, `github.com/NovusEdge/flocc/installer/internal/host`):

```go
// hostState is what flocc setup reads from a host. Err is set when it could not read the host,
// which is reported and never acted on.
type hostState struct {
	Err     string
	Version string   // flocc --version without "flocc "; "" when flocc is not installed there
	Login   *string  // claude_login from status --json; nil when the host did not ask
	Doctor  []string // failing doctor checks ship can repair
	LowDisk string   // doctor's failing disk check
}

// readHost reads flocc --version, status --json and doctor on h as the service user.
func readHost(ctx context.Context, d *deps, h hosts.Host) hostState {
	if err := connectFirst(ctx, d, h); err != nil {
		return hostState{Err: err.Error()}
	}
	dest, err := d.resolve(ctx, h.Target())
	if err != nil {
		return hostState{Err: err.Error()}
	}
	capture := func(args ...string) (host.Result, error) {
		return ship.Capture(ctx, d.run, dest, ship.FloccLine(args), nil)
	}
	why := func(what string, res host.Result, err error) hostState {
		if err != nil {
			return hostState{Err: what + ": " + err.Error()}
		}
		return hostState{Err: fmt.Sprintf("%s: exit %d: %s", what, res.Code, strings.TrimSpace(host.Tail(res.Out(), 1)))}
	}
	res, err := capture("--version")
	switch {
	case err == nil && res.Code == 127:
		return hostState{} // sh found no flocc: ship installs it
	case err != nil || res.Code != 0:
		return why("flocc --version", res, err)
	}
	st := hostState{Version: strings.TrimPrefix(strings.TrimSpace(res.Stdout), "flocc ")}
	if res, err = capture("status", "--json"); err != nil || res.Code != 0 {
		return why("flocc status", res, err)
	}
	var rep ship.Report
	if err := json.Unmarshal([]byte(res.Stdout), &rep); err != nil {
		return hostState{Err: "unreadable status: " + err.Error()}
	}
	st.Login = rep.ClaudeLogin
	// doctor exits 1 when a check fails; anything higher is ssh or the host's shell.
	if res, err = capture("doctor"); err != nil || res.Code > 1 {
		return why("flocc doctor", res, err)
	}
	st.Doctor, st.LowDisk = doctorFails(res.Stdout)
	return st
}

// doctorFails splits doctor's FAIL lines. The login is step 6's and low disk is the owner's to
// free, so neither is a reason to ship.
func doctorFails(out string) (fails []string, disk string) {
	for _, l := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(l, "FAIL  ")
		if !ok {
			continue
		}
		switch name, _, _ := strings.Cut(rest, ": "); name {
		case "claude login":
		case "disk":
			disk = rest
		default:
			fails = append(fails, rest)
		}
	}
	return fails, disk
}

// tailnetOffline reports whether this desktop's tailnet lists name as a peer that is offline.
func tailnetOffline(ctx context.Context, r host.Runner, name string) bool {
	out, err := host.Must(ctx, r, host.Cmd{Name: "tailscale", Args: []string{"status", "--json"}})
	if err != nil {
		return false
	}
	var st struct {
		Peer map[string]struct {
			HostName, DNSName string
			Online            bool
		}
	}
	if json.Unmarshal([]byte(out), &st) != nil {
		return false
	}
	for _, p := range st.Peer {
		label, _, _ := strings.Cut(p.DNSName, ".")
		if strings.EqualFold(label, name) || strings.EqualFold(p.HostName, name) {
			return !p.Online
		}
	}
	return false
}
```

Check `host.Tail` exists with signature `Tail(s string, n int) string` (`grep -n 'func Tail' installer/internal/host`); it is used by `ship/fleet.go` the same way.

In `setup.go`, add the step and call it in `run` between `hostFiles` and the second key half:

```go
type checkedHost struct {
	h  hosts.Host
	st hostState
}

// checkHosts reads every host, ships the ones ship can repair and marks each machine whose host
// failed, which steps 4-6 then skip. It returns the hosts that did not fail.
func (w *wizard) checkHosts(list []hosts.Host) []checkedHost {
	var ok []checkedHost
	for _, h := range list {
		if w.stopped {
			break
		}
		if st, good := w.checkHost(h); good {
			ok = append(ok, checkedHost{h, st})
		} else if m := machineOf(h); m != "" {
			w.failed[saved.HostPart(m)] = true
		}
	}
	return ok
}

func (w *wizard) checkHost(h hosts.Host) (hostState, bool) {
	st := w.ops.readHost(w.ctx, h)
	if st.Err != "" {
		detail := "could not read it: " + st.Err
		if h.SSH != "" && w.ops.offline(w.ctx, saved.HostPart(h.SSH)) {
			detail += "\nits tailnet node is offline; if the host is gone, remove the node at https://login.tailscale.com/admin/machines and rm " + hostFile(w.d, h.Name)
		}
		w.row(rowFailed, h.Name, detail)
		return st, false
	}
	if why := w.shipWhy(st); why != "" {
		w.d.o().Line("%s: %s; shipping the checkout", h.Name, why)
		if err := w.ops.ship(w.ctx, h.Name); err != nil {
			w.fail(h.Name, fmt.Errorf("ship: %w", err))
			return st, false
		}
		if st = w.ops.readHost(w.ctx, h); st.Err != "" {
			w.row(rowFailed, h.Name, "after ship, could not read it: "+st.Err)
			return st, false
		}
		if why := w.shipWhy(st); why != "" {
			w.row(rowFailed, h.Name, "still, after ship: "+why)
			return st, false
		}
	}
	if st.LowDisk != "" {
		w.row(rowPending, h.Name, st.LowDisk+"\nfree space there; flocc does not delete worktrees for you")
		return st, true
	}
	w.row(rowDone, h.Name, "flocc "+st.Version+", doctor ok")
	return st, true
}

// shipWhy is why ship should run on a host, "" when it need not.
func (w *wizard) shipWhy(st hostState) string {
	switch {
	case st.Version == "":
		return "flocc is not installed there"
	case !host.AtLeast(st.Version, w.version):
		return "flocc " + st.Version + " is older than the checkout"
	case len(st.Doctor) > 0:
		return "doctor fails " + strings.Join(st.Doctor, "; ")
	}
	return ""
}
```

`run` becomes:

```go
func (w *wizard) run() {
	if !w.checkout() || !w.fillKeys([]keys.Slot{keys.Linear, keys.Agent}, false) {
		return
	}
	list := w.hostFiles()
	if w.stopped || len(list) == 0 {
		return
	}
	w.checkHosts(list)
	if w.stopped {
		return
	}
	// The publish token's page lists the mapped repos, so it comes after the host.
	w.fillKeys([]keys.Slot{keys.Publish, keys.Ntfy}, true)
}
```

Task 8 keeps `checkHosts`' result for the logins; here it is discarded.

- [ ] **Step 4: Run the tests**

Run: `go test ./installer/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/cli
git commit -s -m "cli: flocc setup reads each host and ships the ones ship can repair"
```

---

### Task 7: Steps 4 and 5: old versions and cardinal, with one root pass per machine

**Files:**
- Modify: `installer/internal/cli/setup.go`, `installer/internal/cli/setup_ops.go`, `installer/internal/cli/setup_test.go`, `installer/internal/cli/cardinal.go`

**Interfaces:**
- Consumes: `steps.OldUsers` (Task 3); `cardinal.SetupOptions`, `Deps.Setup`, `Deps.Status`, `Deps.RemoveOld` (Task 4); `machinesOf`, `probe`, `failed` (Tasks 5-6).
- Produces:
  - `setupOps` fields `oldUsers func(ctx, machine string) []oldUser`, `hasUser func(ctx, machine, user string) bool`, `cardinal func() (saved.Saved, bool, error)`, `cardStatus func(ctx, s saved.Saved) (steps.CardinalStatus, error)`, `cardSetup func(ctx, s saved.Saved, o cardinal.SetupOptions) error`, `removeOld func(ctx, machine string, users []string) error`
  - `type oldUser struct { Name string; Lingering bool; Since string; Procs []string }`; `findOld(ctx, m host.Runner, names []string) []oldUser`
  - `(w *wizard) oldVersions(machines []string) map[string][]string` (keyed by `saved.HostPart`), `(w *wizard) cardinal(machines []string, remove map[string][]string) *saved.Saved`, `(w *wizard) removeRest(machines []string, remove map[string][]string)`, field `w.cardLogin string`
  - `(d *deps) cardinalDeps() cardinal.Deps`

- [ ] **Step 1: Write the failing tests**

Add to `doneOps` (imports: `cardinal`, `saved`, `steps`):

```go
		oldUsers: func(_ context.Context, m string) []oldUser { l.add("old %s", m); return nil },
		hasUser:  func(context.Context, string, string) bool { return false },
		cardinal: func() (saved.Saved, bool, error) {
			return saved.Saved{Machine: "zer0-day-nimble", User: "cardinal", RenewHour: 4}, true, nil
		},
		cardStatus: func(context.Context, saved.Saved) (steps.CardinalStatus, error) {
			l.add("cardinal status")
			return steps.CardinalStatus{Unit: "active", Login: "full"}, nil
		},
		cardSetup: func(_ context.Context, s saved.Saved, o cardinal.SetupOptions) error {
			l.add("cardinal setup %s adopt=%v remove=%v", s.Machine, o.Adopt, o.Remove)
			return nil
		},
		removeOld: func(_ context.Context, m string, users []string) error { l.add("remove %s %v", m, users); return nil },
```

Then:

```go
// withOld gives zer0-day-nimble the linear-agents user until a removal runs.
func withOld(l *opsLog, ops *setupOps) {
	gone := false
	ops.oldUsers = func(_ context.Context, m string) []oldUser {
		l.add("old %s", m)
		if gone {
			return nil
		}
		return []oldUser{{Name: "linear-agents", Lingering: true, Since: "Tue 2026-10-20 01:41:28 EEST",
			Procs: []string{"/usr/lib/systemd/systemd --user", "/usr/bin/tmux -L linear-agents -D", "claude --print \x1b[31mred"}}}
	}
	setup, remove := ops.cardSetup, ops.removeOld
	ops.cardSetup = func(ctx context.Context, s saved.Saved, o cardinal.SetupOptions) error {
		gone = gone || len(o.Remove) > 0
		return setup(ctx, s, o)
	}
	ops.removeOld = func(ctx context.Context, m string, u []string) error {
		gone = true
		return remove(ctx, m, u)
	}
}

func TestSetupShowsAnOldUserAndKeepsItByDefault(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	withOld(&l, &ops)
	d, out, _ := setupDeps(t, nil)
	runSetup(context.Background(), d, ops)
	for _, want := range []string{"linear-agents user on zer0-day-nimble", "lingering", "tmux -L linear-agents", "may still be polling Linear"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in %q", want, out)
		}
	}
	if strings.Contains(out.String(), "\x1b[31m") {
		t.Error("printed the old user's escape sequence")
	}
	if l.has("remove") || l.has("cardinal setup") {
		t.Fatalf("removed without a yes: %v", l.calls)
	}
}

func TestSetupRemovesInCardinalsRootPassWhenSetupRuns(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	withOld(&l, &ops)
	ops.cardStatus = func(context.Context, saved.Saved) (steps.CardinalStatus, error) {
		l.add("cardinal status")
		return steps.CardinalStatus{Unit: "failed", Login: "full"}, nil
	}
	d, _, _ := setupDeps(t, map[string]any{"Remove the linear-agents user": true})
	runSetup(context.Background(), d, ops)
	inOrder(t, l.calls, "cardinal setup zer0-day-nimble adopt=true remove=[linear-agents]", "old zer0-day-nimble")
	if l.has("remove ") {
		t.Fatalf("a second root pass: %v", l.calls)
	}
}

func TestSetupRemovesAloneBesideAHealthyCardinal(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	withOld(&l, &ops)
	d, out, _ := setupDeps(t, map[string]any{"Remove the linear-agents user": true})
	if code := runSetup(context.Background(), d, ops); code != 0 {
		t.Fatalf("exit %d %q", code, out)
	}
	if !l.has("remove zer0-day-nimble [linear-agents]") || l.has("cardinal setup") || !strings.Contains(out.String(), "Revoke") {
		t.Fatalf("calls %v out %q", l.calls, out)
	}
}

func TestSetupNoOldUserMeansNoRootWork(t *testing.T) {
	var l opsLog
	d, _, _ := setupDeps(t, nil)
	if code := runSetup(context.Background(), d, doneOps(&l)); code != 0 || l.has("remove") || l.has("cardinal setup") {
		t.Fatalf("exit %d calls %v", code, l.calls)
	}
}

func TestSetupCardinalWhenNoneIsSaved(t *testing.T) {
	cases := []struct {
		name    string
		hasUser bool
		answers map[string]any
		want    string // "" when setup must not run
		code    int
	}{
		{"set up by default", false, nil, "cardinal setup zer0-day-nimble adopt=false remove=[]", 0},
		{"declined", false, map[string]any{"Set up cardinal": false}, "", 0},
		{"adopt", true, map[string]any{"Adopt the existing cardinal user": true}, "cardinal setup zer0-day-nimble adopt=true remove=[]", 0},
		{"adopt defaults to no", true, nil, "", 0},
	}
	for _, c := range cases {
		var l opsLog
		ops := doneOps(&l)
		ops.cardinal = func() (saved.Saved, bool, error) { return saved.Saved{}, false, nil }
		ops.hasUser = func(_ context.Context, m, u string) bool { return c.hasUser && u == "cardinal" }
		d, _, _ := setupDeps(t, c.answers)
		code := runSetup(context.Background(), d, ops)
		if code != c.code || (c.want == "") == l.has("cardinal setup") || (c.want != "" && !l.has(c.want)) {
			t.Errorf("%s: exit %d calls %v", c.name, code, l.calls)
		}
	}
}

func TestSetupDoesNotQuietlyRebuildARemovedCardinal(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.cardinal = func() (saved.Saved, bool, error) {
		return saved.Saved{Machine: "zer0-day-nimble", User: "cardinal", Removed: true}, true, nil
	}
	d, _, p := setupDeps(t, nil)
	if code := runSetup(context.Background(), d, ops); code != 0 || l.has("cardinal setup") ||
		!slices.ContainsFunc(p.asked, func(q string) bool { return strings.HasPrefix(q, "Set up cardinal on zer0-day-nimble again") }) {
		t.Fatalf("exit %d calls %v asked %v", code, l.calls, p.asked)
	}
}

func TestSetupWithoutIncusFailsCardinalButStillRemoves(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	withOld(&l, &ops)
	ops.cardStatus = func(context.Context, saved.Saved) (steps.CardinalStatus, error) {
		return steps.CardinalStatus{}, errors.New("cardinal@zer0-day-nimble unreachable")
	}
	ops.machine = func(context.Context, string) machineState {
		return machineState{Incus: errors.New("cannot run incus as an administrator; add it to the incus-admin group")}
	}
	d, out, _ := setupDeps(t, map[string]any{"Remove the linear-agents user": true})
	if code := runSetup(context.Background(), d, ops); code != 1 || l.has("cardinal setup") || !l.has("remove zer0-day-nimble [linear-agents]") || !strings.Contains(out.String(), "incus-admin") {
		t.Fatalf("exit %d calls %v out %q", code, l.calls, out)
	}
}

func TestSetupTreatsOwnerAtMachineAndMachineAsOne(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.hosts = func() []hosts.Host {
		return []hosts.Host{nimble, {Name: "two", SSH: "root@two", Machine: "owner@zer0-day-nimble"}}
	}
	d, _, _ := setupDeps(t, nil)
	runSetup(context.Background(), d, ops)
	if strings.Count(strings.Join(l.calls, "\n"), "old ") != 1 {
		t.Fatalf("calls %v", l.calls)
	}
}

func TestSetupSkipsOldVersionsAndCardinalOnAMachineWhoseHostFailed(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	ops.hosts = func() []hosts.Host {
		return []hosts.Host{nimble, {Name: "other", SSH: "root@other", Machine: "box"}}
	}
	ops.readHost = func(_ context.Context, h hosts.Host) hostState {
		if h.Name == "flocc-nimble" {
			return hostState{Err: "ssh: timed out"}
		}
		return hostState{Version: "0.8.0"}
	}
	d, _, _ := setupDeps(t, nil)
	runSetup(context.Background(), d, ops)
	if l.has("old zer0-day-nimble") || !l.has("old box") || l.has("cardinal status") {
		t.Fatalf("calls %v", l.calls)
	}
}

func TestFindOldReadsTheUserWithoutSudo(t *testing.T) {
	f := host.NewFake().
		On("id -u linear-agents", 0, "1001\n").
		On("loginctl show-user linear-agents -p Linger --value", 0, "yes\n").
		On("loginctl show-user linear-agents -p Timestamp --value", 0, "Tue 2026-10-20 01:41:28 EEST\n").
		On("ps -u linear-agents -o args=", 0, "/usr/lib/systemd/systemd --user\n/usr/bin/tmux -L linear-agents -D\n")
	got := findOld(context.Background(), f, []string{"linear-agents", "nobody-else"})
	if len(got) != 1 || !got[0].Lingering || got[0].Since == "" || len(got[0].Procs) != 2 {
		t.Fatalf("%+v", got)
	}
	if slices.ContainsFunc(f.Calls, func(c string) bool { return strings.Contains(c, "sudo") }) {
		t.Fatal("used sudo")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/internal/cli/ -run 'Setup|FindOld'`
Expected: compile errors for the new fields and `oldUser`, `findOld`.

- [ ] **Step 3: Implement**

In `cli/cardinal.go`, build the deps once for both callers:

```go
func (d *deps) cardinalDeps() cardinal.Deps {
	mux, _ := ship.SSHMux()
	return cardinal.Deps{Out: d.out, Err: d.err, Home: d.home, TTY: d.tty, Open: d.open,
		RepoRoot: d.repoRoot, Tailnet: tailnetIPs, Mux: mux}
}

func (d *deps) cardinal(ctx context.Context, args []string) int {
	return cardinal.Main(ctx, d.cardinalDeps(), args)
}
```

Add the fields to `setupOps`:

```go
	oldUsers   func(ctx context.Context, machine string) []oldUser
	hasUser    func(ctx context.Context, machine, user string) bool
	cardinal   func() (saved.Saved, bool, error)
	cardStatus func(ctx context.Context, s saved.Saved) (steps.CardinalStatus, error)
	cardSetup  func(ctx context.Context, s saved.Saved, o cardinal.SetupOptions) error
	removeOld  func(ctx context.Context, machine string, users []string) error
```

and to `newSetupOps` (after `mux`, add `cd := d.cardinalDeps()`):

```go
		oldUsers: func(ctx context.Context, m string) []oldUser { return findOld(ctx, owner(d, mux, m), steps.OldUsers) },
		hasUser: func(ctx context.Context, m, user string) bool {
			r, err := owner(d, mux, m).Run(ctx, host.Cmd{Name: "id", Args: []string{"-u", user}})
			return err == nil && r.Code == 0
		},
		cardinal: func() (saved.Saved, bool, error) {
			s, err := saved.Load(saved.Dir(d.home), saved.DefaultUser)
			if errors.Is(err, os.ErrNotExist) {
				return s, false, nil
			}
			return s, err == nil, err
		},
		cardStatus: cd.Status,
		cardSetup:  cd.Setup,
		removeOld:  cd.RemoveOld,
```

`setup_ops.go` also gets (imports `errors`, `cardinal`, `saved`, `steps`):

```go
// oldUser is an earlier flocc's service user as the owner sees it without sudo.
type oldUser struct {
	Name      string
	Lingering bool
	Since     string   // when its user manager started
	Procs     []string // each process's command line, as that user wrote it
}

// findOld looks for each name on the machine m runs commands on.
func findOld(ctx context.Context, m host.Runner, names []string) []oldUser {
	out := func(name string, args ...string) (string, bool) {
		r, err := m.Run(ctx, host.Cmd{Name: name, Args: args})
		return strings.TrimSpace(r.Stdout), err == nil && r.Code == 0
	}
	var found []oldUser
	for _, n := range names {
		if _, ok := out("id", "-u", n); !ok {
			continue
		}
		u := oldUser{Name: n}
		linger, _ := out("loginctl", "show-user", n, "-p", "Linger", "--value")
		u.Lingering = linger == "yes"
		u.Since, _ = out("loginctl", "show-user", n, "-p", "Timestamp", "--value")
		if procs, _ := out("ps", "-u", n, "-o", "args="); procs != "" {
			u.Procs = strings.Split(procs, "\n")
		}
		found = append(found, u)
	}
	return found
}
```

In `setup.go` (imports `cardinal`, `steps`), add the field `cardLogin string // cardinal's login from its last status` to `wizard`, and:

```go
// oldVersions shows each old user found on a machine and asks whether to remove it, default no.
// The removals it returns run in the machine's root pass.
func (w *wizard) oldVersions(machines []string) map[string][]string {
	remove := map[string][]string{}
	for _, m := range machines {
		k := saved.HostPart(m)
		if w.failed[k] {
			w.row(rowPending, "old versions", k+": skipped, a host on it failed")
			continue
		}
		found := w.ops.oldUsers(w.ctx, m)
		if len(found) == 0 {
			w.row(rowDone, "old versions", "none on "+k)
			continue
		}
		for _, u := range found {
			w.showOld(k, u)
			yes, err := w.d.prompt.Confirm("Remove the "+u.Name+" user on "+k+"? Its home is archived first", false)
			if err != nil {
				w.fail("old versions", err)
				return nil
			}
			if yes {
				remove[k] = append(remove[k], u.Name)
			} else {
				w.row(rowSkipped, "old versions", u.Name+" on "+k+" kept")
			}
		}
	}
	return remove
}

// showOld prints what was found. The process lines are the old user's text, so they are sanitized.
func (w *wizard) showOld(machine string, u oldUser) {
	o := w.d.o()
	facts := []string{fmt.Sprintf("%d processes", len(u.Procs))}
	if u.Lingering {
		facts = append(facts, "lingering")
	}
	if u.Since != "" {
		facts = append(facts, "user manager since "+saved.Sanitize(u.Since))
	}
	o.Line("%s user on %s (%s), from an earlier flocc:", u.Name, machine, strings.Join(facts, ", "))
	for _, p := range u.Procs {
		o.Line("  %s", saved.Sanitize(p))
	}
	if slices.ContainsFunc(u.Procs, polls) {
		o.WarnLine("It runs claude or a flocc tick, so the old install may still be polling Linear.")
	}
}

// polls reports whether a process of an old install may still be taking Linear issues.
func polls(args string) bool {
	return strings.Contains(args, "claude") || strings.Contains(args, "linear-agents run") || strings.Contains(args, "flocc run")
}

// setupFailures are the status failures setup repairs; the login is step 6's.
func setupFailures(st steps.CardinalStatus) []string {
	st.Login = "full"
	return st.Failures()
}

// cardinal sets up or repairs cardinal on its one machine: the saved install's, else the first
// host's. A setup also runs that machine's agreed removals in its root pass, and takes them out of
// remove. It returns the install to log in, or nil.
func (w *wizard) cardinal(machines []string, remove map[string][]string) *saved.Saved {
	cs, have, err := w.ops.cardinal()
	if err != nil {
		w.row(rowFailed, "cardinal", err.Error())
		return nil
	}
	if !have {
		if len(machines) == 0 {
			w.row(rowSkipped, "cardinal", "no host is on a machine of yours")
			return nil
		}
		cs = saved.Saved{Machine: machines[0], User: saved.DefaultUser, RenewHour: 4}
	}
	k := saved.HostPart(cs.Machine)
	if w.failed[k] {
		w.row(rowPending, "cardinal", "skipped: a host on "+k+" failed")
		return nil
	}
	if have && !cs.Removed {
		st, err := w.ops.cardStatus(w.ctx, cs)
		if err == nil && len(setupFailures(st)) == 0 {
			w.row(rowDone, "cardinal", "status ok on "+k)
			w.cardLogin = st.Login
			return &cs
		}
		return w.setupCardinal(cs, true, remove)
	}
	ms := w.probe(cs.Machine)
	if ms.Err != nil {
		w.row(rowFailed, "cardinal", ms.Err.Error())
		return nil
	}
	var q string
	var def, adopt bool
	switch {
	case have:
		q, adopt = "Set up cardinal on "+k+" again? flocc cardinal remove took it down", true
	case w.ops.hasUser(w.ctx, cs.Machine, cs.User):
		q, adopt = "Adopt the existing "+cs.User+" user on "+k+"? No saved file names it, so it looks like a setup by hand", true
	default:
		q, def = "Set up cardinal on "+k+"?", true
	}
	yes, err := w.d.prompt.Confirm(q, def)
	if err != nil {
		w.fail("cardinal", err)
		return nil
	}
	if !yes {
		w.row(rowSkipped, "cardinal", "not set up on "+k+"; asked again next run")
		return nil
	}
	return w.setupCardinal(cs, adopt, remove)
}

func (w *wizard) setupCardinal(cs saved.Saved, adopt bool, remove map[string][]string) *saved.Saved {
	k := saved.HostPart(cs.Machine)
	ms := w.probe(cs.Machine)
	switch {
	case ms.Err != nil:
		w.row(rowFailed, "cardinal", ms.Err.Error())
		return nil
	case ms.Incus != nil:
		w.row(rowFailed, "cardinal", ms.Incus.Error())
		return nil
	}
	rm := remove[k]
	delete(remove, k)
	err := w.ops.cardSetup(w.ctx, cs, cardinal.SetupOptions{Adopt: adopt, Remove: rm})
	w.removed(cs.Machine, rm, err)
	if err != nil {
		w.fail("cardinal", fmt.Errorf("setup: %w", err))
		return nil
	}
	st, err := w.ops.cardStatus(w.ctx, cs)
	switch {
	case err != nil:
		w.row(rowFailed, "cardinal", "after setup: "+err.Error())
		return nil
	case len(setupFailures(st)) > 0:
		w.row(rowPending, "cardinal", strings.Join(setupFailures(st), "\n"))
	default:
		w.row(rowDone, "cardinal", "set up on "+k)
	}
	w.cardLogin = st.Login
	return &cs
}

// removeRest runs the removals no cardinal setup took, one purge-only root pass per machine.
func (w *wizard) removeRest(machines []string, remove map[string][]string) {
	for _, m := range machines {
		if users := remove[saved.HostPart(m)]; len(users) > 0 {
			w.removed(m, users, w.ops.removeOld(w.ctx, m, users))
		}
	}
}

// removed looks for each agreed user again, as it was found, and reports what the root pass did.
func (w *wizard) removed(m string, users []string, err error) {
	if len(users) == 0 {
		return
	}
	k := saved.HostPart(m)
	still := w.ops.oldUsers(w.ctx, m)
	for _, u := range users {
		if !slices.ContainsFunc(still, func(o oldUser) bool { return o.Name == u }) {
			w.row(rowDone, "old versions", "removed "+u+" on "+k+"; its home is archived under /root there. "+
				"Revoke the old install's GitHub tokens and Linear key yourself")
			continue
		}
		why := "the root pass did not remove it"
		if err != nil {
			why = err.Error()
		}
		w.row(rowFailed, "old versions", u+" on "+k+": "+why)
	}
}
```

`run` gains, after the second key half:

```go
	if w.stopped {
		return
	}
	machines := machinesOf(list)
	remove := w.oldVersions(machines)
	if w.stopped {
		return
	}
	w.cardinal(machines, remove)
	if w.stopped {
		return
	}
	w.removeRest(machines, remove)
```

`machinesOf(list)` must use the host list after `placeHost` saved machines, which `hostFiles` returns.

- [ ] **Step 4: Run the tests**

Run: `go test ./installer/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/cli
git commit -s -m "cli: flocc setup offers old-version removal and cardinal, one root pass per machine"
```

---

### Task 8: Step 6, logins, and the whole run

**Files:**
- Modify: `installer/internal/cli/setup.go`, `installer/internal/cli/setup_ops.go`, `installer/internal/cli/setup_test.go`

**Interfaces:**
- Consumes: `checkHosts`' `[]checkedHost` (Task 6); `w.cardinal`'s `*saved.Saved` and `w.cardLogin` (Task 7); `loginWith` (existing); `cardinal.Deps.Login` (Task 4).
- Produces: `setupOps.login func(ctx, name, email string) error`, `setupOps.cardLogin func(ctx, s saved.Saved, email string) error`; `(w *wizard) logins(checked []checkedHost, cs *saved.Saved)`.

- [ ] **Step 1: Write the failing tests**

Add to `doneOps`:

```go
		login:     func(_ context.Context, name, email string) error { l.add("login %s %s", name, email); return nil },
		cardLogin: func(_ context.Context, _ saved.Saved, email string) error { l.add("cardinal login %s", email); return nil },
```

Then:

```go
func TestSetupOnAFreshDesktopRunsEveryStepInOrder(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	made := false
	ops.hosts = func() []hosts.Host {
		if made {
			return []hosts.Host{nimble}
		}
		return nil
	}
	ops.create = func(_ context.Context, s create.Spec) error {
		l.add("create %s %s", s.Machine, s.Name)
		made = true
		return nil
	}
	ops.cardinal = func() (saved.Saved, bool, error) { return saved.Saved{}, false, nil }
	ops.cardStatus = func(context.Context, saved.Saved) (steps.CardinalStatus, error) {
		l.add("cardinal status")
		return steps.CardinalStatus{Unit: "active", Login: "none"}, nil
	}
	d, _, _ := setupDeps(t, map[string]any{"No hosts yet": 0, "Host name": "flocc-nimble", "Machine to make": "zer0-day-nimble",
		"claude.ai pool account for cardinal": "pool@example.com"})
	if code := runSetup(context.Background(), d, ops); code != 0 {
		t.Fatalf("exit %d; calls %v", code, l.calls)
	}
	inOrder(t, l.calls,
		fmt.Sprintf("keys %v", []keys.Slot{keys.Linear, keys.Agent}),
		"create zer0-day-nimble flocc-nimble",
		"read flocc-nimble",
		fmt.Sprintf("keys %v", []keys.Slot{keys.Publish, keys.Ntfy}),
		"old zer0-day-nimble",
		"cardinal setup zer0-day-nimble adopt=false remove=[]",
		"cardinal status",
		"cardinal login pool@example.com")
}

func TestSetupOnAFinishedDesktopOnlyReads(t *testing.T) {
	var l opsLog
	d, out, p := setupDeps(t, nil)
	if code := runSetup(context.Background(), d, doneOps(&l)); code != 0 {
		t.Fatalf("exit %d %q", code, out)
	}
	for _, c := range l.calls {
		if !strings.HasPrefix(c, "keys ") && !strings.HasPrefix(c, "read ") && !strings.HasPrefix(c, "old ") && c != "cardinal status" {
			t.Errorf("changed something: %q", c)
		}
	}
	if len(p.asked) > 0 {
		t.Errorf("asked %v", p.asked)
	}
}

func TestSetupLogsInWhatIsNotFullAndOnlyReportsUnknown(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	none, unknown := "none", "unknown"
	ops.hosts = func() []hosts.Host {
		return []hosts.Host{nimble, {Name: "b", SSH: "root@b", Machine: "zer0-day-nimble"}, {Name: "c", SSH: "root@c", Machine: "zer0-day-nimble"}}
	}
	ops.readHost = func(_ context.Context, h hosts.Host) hostState {
		switch h.Name {
		case "b":
			return hostState{Version: "0.8.0", Login: &none}
		case "c":
			return hostState{Version: "0.8.0", Login: &unknown}
		}
		return hostState{Version: "0.8.0"}
	}
	d, _, _ := setupDeps(t, map[string]any{"claude.ai pool account for b": "pool@example.com"})
	if code := runSetup(context.Background(), d, ops); code != 0 {
		t.Fatalf("exit %d calls %v", code, l.calls)
	}
	if !l.has("login b pool@example.com") || l.has("login c") || l.has("login flocc-nimble") || l.has("cardinal login") {
		t.Fatalf("calls %v", l.calls)
	}
}

func TestSetupRefusesALoginAddressThatIsNotOne(t *testing.T) {
	var l opsLog
	ops := doneOps(&l)
	none := "none"
	ops.readHost = func(context.Context, hosts.Host) hostState { return hostState{Version: "0.8.0", Login: &none} }
	d, _, _ := setupDeps(t, map[string]any{"claude.ai pool account for flocc-nimble": "me; rm -rf ~"})
	if code := runSetup(context.Background(), d, ops); code != 1 || l.has("login ") {
		t.Fatalf("exit %d calls %v", code, l.calls)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/internal/cli/ -run Setup`
Expected: compile errors for `login` and `cardLogin`.

- [ ] **Step 3: Implement**

Add to `setupOps`:

```go
	login     func(ctx context.Context, name, email string) error
	cardLogin func(ctx context.Context, s saved.Saved, email string) error
```

and to `newSetupOps`:

```go
		login:     func(ctx context.Context, name, email string) error { return loginWith(ctx, d, name, email) },
		cardLogin: cd.Login,
```

In `setup.go` (import `ship`):

```go
// logins signs in each host and cardinal whose login is not full. unknown is a host that could not
// read its login, which is no evidence of a lapse, so it is only shown.
func (w *wizard) logins(checked []checkedHost, cs *saved.Saved) {
	for _, c := range checked {
		if w.stopped {
			return
		}
		if m := machineOf(c.h); m != "" && w.failed[saved.HostPart(m)] {
			continue
		}
		name := c.h.Name
		w.login(name, c.st.Login, func(email string) error { return w.ops.login(w.ctx, name, email) })
	}
	if cs != nil && !w.stopped {
		w.login("cardinal", &w.cardLogin, func(email string) error { return w.ops.cardLogin(w.ctx, *cs, email) })
	}
}

func (w *wizard) login(name string, state *string, do func(email string) error) {
	switch {
	case state == nil:
		w.row(rowSkipped, "login", name+": the host does not report its login")
		return
	case *state == "full":
		w.row(rowDone, "login", name+": full")
		return
	case *state == "unknown":
		w.row(rowSkipped, "login", name+": unknown, it could not read its login; not acted on")
		return
	}
	email, err := w.d.prompt.Ask("claude.ai pool account for "+name+" (login is "+saved.Sanitize(*state)+")", "empty lets the sign-in page choose")
	if err != nil {
		w.fail("login", err)
		return
	}
	// The address goes into a remote command line.
	if email = strings.TrimSpace(email); email != "" && !ship.ValidEmail(email) {
		w.row(rowPending, "login", fmt.Sprintf("%s: %q is not an email address", name, email))
		return
	}
	if err := do(email); err != nil {
		w.row(rowFailed, "login", name+": "+err.Error())
		return
	}
	w.row(rowDone, "login", name+": signed in")
}
```

`run` in full:

```go
func (w *wizard) run() {
	if !w.checkout() || !w.fillKeys([]keys.Slot{keys.Linear, keys.Agent}, false) {
		return
	}
	list := w.hostFiles()
	if w.stopped || len(list) == 0 {
		return
	}
	checked := w.checkHosts(list)
	if w.stopped {
		return
	}
	// The publish token's page lists the mapped repos, so it comes after the host.
	w.fillKeys([]keys.Slot{keys.Publish, keys.Ntfy}, true)
	if w.stopped {
		return
	}
	machines := machinesOf(list)
	remove := w.oldVersions(machines)
	if w.stopped {
		return
	}
	cs := w.cardinal(machines, remove)
	if w.stopped {
		return
	}
	w.removeRest(machines, remove)
	w.logins(checked, cs)
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./installer/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/cli
git commit -s -m "cli: flocc setup signs in what is not logged in"
```

---

### Task 9: Docs and changelog

**Files:**
- Modify: `README.md:64-66`, `docs/vm-setup.md` (sections "The desktop command" and "cardinal"), `CHANGELOG.md` (0.8.0 entry)

- [ ] **Step 1: Write the docs**

`README.md`: in the "Current design" paragraph, add before the sentence starting "On the desktop, `flocc keys`": "`flocc setup` runs the steps below in order and can be run again to repair what is missing."

`docs/vm-setup.md`: add a section `## flocc setup` before `## The desktop command` with, in this order:

- One paragraph: what it runs (keys, host create or add, keys again, old versions, cardinal, logins), that each step checks first, that a rerun shows what is done and repairs the rest, and that it needs a terminal (exits 2 without one).
- A code block with `flocc setup`, run inside the checkout.
- A table with columns "Step", "Checks", "Does": checkout; keys (Linear key and agent token); host (no host file: create or add; a stopped create's log offers to continue; each host: `flocc --version`, `status --json`, `doctor`; older than the checkout or a doctor failure other than login and disk: ship; low disk and unreachable: reported); keys (publish token, ntfy topic, then a check); old versions (`id`, `loginctl`, `ps` without sudo; shown, default no; removed in the root pass after its own checks, home archived to `/root/flocc-removed-<user>-<date-time>.tar.gz`); cardinal (one machine: the saved one, else the first host's; status ok: nothing; failing: setup; a `cardinal` user without a saved file: adopt, default no; none: asked, default yes); logins (`full`: done; `unknown`: shown; otherwise `flocc login` or `flocc cardinal login` with the pool account).
- One paragraph: host create needs passwordless sudo on the machine; a machine whose sudo asks for a password can hold cardinal and run the cleanup, and the removal and cardinal's root work share one `ssh -t`, so sudo asks once.
- One paragraph: which machine a host is on (`machine` in the host file, asked once, `none` allowed, refused with `daytona`).
- One line: exit codes 0, 1, 2 as in the spec.

In the `## cardinal` section, replace the comment on `flocc cardinal setup zer0-day-nimble  # the first run on the hand setup needs --adopt` with `# or flocc setup, which asks to adopt the hand setup`.

`CHANGELOG.md`, in `## 0.8.0`, change the summary line to "`flocc cardinal`, the always-on assistant's setup command, and `flocc setup`, one command for the whole setup." and add a bullet:

```markdown
- `flocc setup` walks the keys, a host (create, add, or ship to repair), the old `linear-agents`
  user from the hand-setup era, cardinal and the logins, in order and safe to rerun. An old user is
  removed only on a yes, after the root pass re-checks it, with its home archived under `/root`;
  that removal and cardinal's root work share one sudo password. Host files gain `machine`.
```

Read each file before editing it and keep its voice. Run the de-slopify skill over the new section.

- [ ] **Step 2: Check**

Run: `just check`
Expected: lint and tests pass.

- [ ] **Step 3: Commit**

```bash
git add README.md docs/vm-setup.md CHANGELOG.md
git commit -s -m "docs: flocc setup"
```

---

## After the tasks: live run (controller with the owner, not a subagent)

The owner runs this in a real terminal on the desktop, from the worktree:

1. `go build -o /tmp/flocc-next ./installer/cmd/flocc` and `cd ~/Projects/Personal/worktrees/flocc-cardinal-impl && /tmp/flocc-next setup`.
2. Expect: checkout and keys done; flocc-nimble read, asked once for its machine (`zer0-day-nimble`) unless the file has it; the `linear-agents` user shown with its systemd and tmux processes, default no; answer yes; cardinal has no saved file and a `cardinal` user, so "Adopt" is asked; answer yes; one sudo password prompt for the purge and cardinal-root; the summary names the archive and the token reminder; cardinal login offered if not full.
3. Check: `id linear-agents` fails on zer0-day-nimble; `sudo ls -l /root/flocc-removed-linear-agents-*.tar.gz` shows mode 600; `/tmp/flocc-next cardinal status` passes; a second `/tmp/flocc-next setup` changes nothing and exits 0.
4. Then merge #48.
