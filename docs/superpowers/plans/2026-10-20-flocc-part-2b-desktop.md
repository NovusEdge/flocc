# flocc Part 2b: desktop CLI and credentials (0.5.0) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One `flocc` command on the desktop: host commands run over `-H`, keys are made once, checked when pasted and kept in the OS keyring, each host's answers live in `~/.config/flocc/hosts/<name>.toml`, and ship and login run from the desktop; released as 0.5.0.

**Architecture:** The Go module gets two `main` packages, `installer/cmd/flocc` (desktop) and `installer/cmd/flocc-setup` (the staged installer, moved from `installer/main.go`), over shared internal packages. New packages: `internal/cli` (verb dispatch, `-H`, `host`, `keys`, `ship`, `login`), `internal/keys` (secret store and the fill flow), `internal/githubapi` and `internal/linearapi` (small API clients that hold the side-effect-free key validators), and `internal/replay` (serves recorded HTTP responses to tests). Part 1's `internal/hosts` grows the host's answers; there is no second host store. Ship and login are Part 2a's `ship.Ship` and `ship.Login`, called as they are; the desktop prints the ship summary with the host's name in it. QR codes are Part 2a's `qr.Write`. The Python host CLI gains `logs` and `attach`, refuses the desktop verbs, and its `status --json` reports the Claude login and token expiries. This plan is written against the code as it stands after Part 1 and Part 2a: every old name maps through the spec's Part 1 Names table (`src/linear_agents/` → `src/flocc/`, `linear-agents` CLI → `flocc`, `la-setup` → `flocc-setup`, `LINEAR_AGENTS_*` → `FLOCC_*`, module `github.com/NovusEdge/linear-agents` → `github.com/NovusEdge/flocc`, service user and `~/.config/linear-agents` → `flocc` and `~/.config/flocc`, `linear-agents.service` → `flocc.service`, tmux socket `-L flocc`).

**Tech Stack:** Go 1.27 (`charm.land/huh/v2`, `charm.land/lipgloss/v2`, `github.com/pelletier/go-toml/v2`, Part 2a's `github.com/mdp/qrterminal/v4` behind `installer/internal/qr`, plus the keyring dependency chosen in Task 5), Python 3.13 with uv, pytest and ruff.

**Spec:** `docs/superpowers/specs/2026-10-20-flocc-design.md`, section "Part 2b: desktop CLI and credentials" (and the Part 1 Names table, the Part 1 "Old names still read" section, and the Part 2a sections this part consumes).

## Global Constraints

- `flocc -H <name> <host command line>` runs that line on the host as the service user, each argument quoted on its own, with `ssh -t` only when stdin is a terminal.
- Without `-H`: `$FLOCC_HOST`; then the only host; then, for a command naming an issue key, the host whose `status --json` holds that claim.
- `-H all` is allowed for read-only commands (`status`, `doctor`, `logs`).
- SSH uses `ControlMaster`/`ControlPersist`, so a Tailscale check-mode approval covers a whole run.
- Desktop-only verbs: `ship`, `login`, `keys`, `host`. The host CLI answers them with "that is a desktop command".
- A bare `flocc` prints help and never reaches the root phase.
- Secrets live in the OS keyring when there is one (Secret Service, macOS Keychain), otherwise in `~/.config/flocc/secrets.toml`, mode 600.
- `flocc keys` reads pastes with echo off and never takes a key as a flag. No key appears in any argv, log line or shell history.
- Validators have no side effects: the Linear key reads the viewer and team, and proves it can write by setting a mapped digest issue's title to the title it already has; each GitHub token reads every repo in `hosts/*.toml`; the zero-SHA ref probe must fail for the agent token and pass for the publish token; a `POST /pulls` naming a head that does not exist must return 422, not 403, for the publish token.
- A missing key is asked for where it is needed: `host add` asks for the Linear key and the agent token before it reads repos and teams, and for the publish token and ntfy topic once the host file is saved; `ship` asks for any key still missing. Nothing stops with "run flocc keys". `host import` takes each key the desktop lacks from the host's own secret files, through the same checks as a paste.
- A stored token that misses a repo is fixed on GitHub by editing the token's repository access (its value stays the same), then `flocc keys check`. Only a paste that fails is asked for again.
- GitHub token pages are the fine-grained token page with name, description, owner, permissions and `expires_in=366` filled in; the publish token's page opens only after the agent token is accepted. The agent token asks for `contents`, `pull_requests`, `issues`, `statuses` and `actions`, all `read`: fine-grained tokens have no Checks permission and cannot call the Checks API, and Actions read is what shows workflow runs and their logs (docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens, the URL parameter table and the limitations list; docs.github.com/en/rest/authentication/permissions-required-for-fine-grained-personal-access-tokens, which lists no Checks category; both read 2026-10-20).
- The ntfy topic QR code is `ntfy://ntfy.sh/<topic>?display=flocc`, with the https URL beside it, then one test push with "did it arrive?".
- `hosts/<name>.toml` is the only source of a host's answers; an `--interactive` ship copies the host's answers back to it.
- The `just` recipes become one-line wrappers over `flocc`; they are removed in 0.6.0, not here.
- 0.5.0 removes Part 1's two old-name readers (the `Linear-Agents:` trailer and the old bot prefix), the old-host refusal and the old hosts file conversion. It is tagged only after la-nimble is deleted (Part 1 Task 15).
- Host files are Part 1's package `installer/internal/hosts` (`hosts.Host`, `Dir`, `Load`, `List`, `Save`). Package `cli` never names a local variable `hosts`.
- Comments carry only facts the code cannot show. Commits use `git commit -s`, with no `Co-Authored-By` line and no AI attribution.
- Steps that touch a real host or the owner's accounts are manual steps for the controller, never for an implementer subagent.

## Review Focus

1. A desktop with no usable Secret Service (SSH session without D-Bus, locked or absent keyring): the store must choose the file store within seconds and say where secrets went, never hang. Task 5 pins this with a probe that blocks.
2. A paste carrying a trailing newline, surrounding spaces or `NAME=value`: trimmed or refused with a reason before any network call. Task 8 pins this.
3. Mapped repos spread over two GitHub owners: one fine-grained token has one resource owner, so the page cannot be prefilled with an owner and the owner must be told to make the token for the owner that holds the repos. Task 8 pins this (no `target_name`, a note naming both owners).
4. One host unreachable during `keys rotate` or `-H all`: the others still run, the unreachable one is named, the exit status is non-zero. Tasks 4 and 11 pin this.
5. An issue key that no host holds, or that two hosts both report: an error naming the hosts and asking for `-H`, never a guess. Task 4 pins this.
6. A secret sent or read over SSH (`pushKeys`, `host import` reading the host's key files): every such call passes `-T`, so a `RequestTTY force` in the owner's ssh config cannot echo the value or hold `cat` short of EOF. Tasks 4, 9 and 11 pin this.

## Unconfirmed, checked by the controller

| Premise | Where it is checked | If it is wrong |
|---|---|---|
| GitHub checks the caller's permission before it validates `head` on `POST /pulls` (from the spec) | Task 6 Step 9 | The owner decides before merge whether to drop the pull-request probe |
| `gh pr checks` (GraphQL `statusCheckRollup`) shows a private repo's Actions results to a fine-grained token with `actions` and `statuses` read. The docs say fine-grained tokens cannot call the Checks API, so the check runs may be missing from it. | Task 14 Step 6 | Agents read CI with `gh run list --branch <branch>` and `gh run view --log-failed`, which Actions read covers by the docs; the controller adds that line to the agent instructions |
| Linear refuses `issueUpdate` from a key without write access, and setting a title to the value it already has adds no entry to the issue's history (only `updatedAt` moves) | Task 7 Step 7 | If history shows an entry, keep the probe (it names the change as flocc's) and say so in docs/vm-setup.md; if a read-only key is not refused, drop the probe and have the Linear page help say "Full access" only |
| `https://linear.app/settings/account/security` opens the page where personal API keys are made | Task 14 Step 6 | Use the URL the page has, or `https://linear.app/settings` with the menu path in the help text |

The write probe sets the title to itself rather than sending an empty `input`: whether Linear checks the key's scope before it rejects or skips an empty update is not documented, so an empty update could pass a read-only key. A title written back is a real write, which the scope check must approve.

## Execution order

| Task | Depends on | Can run in parallel with |
|---|---|---|
| 1 Binary split and `flocc` skeleton | none | 2, 5, 6 |
| 2 Python host CLI: desktop verbs, `logs`, `attach` | none | 1 to 11 |
| 3 Host files carry the answers | none | 2, 5, 6, 7 |
| 4 Host commands over `-H` | 1, 3 | 2, 5, 6, 7 |
| 5 Secret store | none | 1, 2, 3, 4, 6, 7 |
| 6 GitHub client and token validators | none | 1, 2, 3, 4, 5 |
| 7 Linear client and key validator | 6 | 2, 3, 4, 5 |
| 8 `flocc keys` | 4, 5, 6, 7 | 2 |
| 9 `flocc host add` and `host import` | 8 | 2 |
| 10 `flocc ship` and `flocc login` | 9 | 2, 11 |
| 11 `flocc keys rotate` | 9 | 2, 10, 12, 13 |
| 12 Login and token expiry in status | 2, 4, 10 | 11 |
| 13 Remove the old-name readers | 12; la-nimble deleted before its commit is released | 11 |
| 14 Docs, changelog, 0.5.0, verification | all; la-nimble deleted | none |

Tasks listed as parallel share no files. Tasks 1 and 3 both change `installer/internal/ship/ship.go`, so they do not run together. Tasks 4, 8, 9 and 10 all change `installer/internal/cli/cli.go`, so they run in that order.

---

### Task 1: Binary split and the `flocc` skeleton

**Depends on:** nothing in this plan (Parts 1 and 2a merged).

Today one binary is both the desktop tool (`ship`, `host`, `fleet`) and the staged installer, and run with no arguments it starts the root phase. This task splits it: `installer/cmd/flocc-setup` is the installer alone, `installer/cmd/flocc` is the desktop command, and the desktop verbs that exist today move to `flocc` unchanged so every `just` recipe keeps working. Ship also stops reading a prebuilt installer from `build/`: it cross-compiles `flocc-setup` into the stage itself, so `flocc ship` from a PATH never ships a stale installer next to a newer bundle.

**Files:**
- Move: `installer/main.go` → `installer/cmd/flocc-setup/main.go`
- Move: `installer/main_test.go` → `installer/cmd/flocc-setup/main_test.go`
- Create: `installer/cmd/flocc/main.go`
- Create: `installer/internal/cli/cli.go`
- Create: `installer/internal/cli/cli_test.go`
- Modify: `installer/internal/ship/ship.go` (`stageFiles`, usage text)
- Modify: `installer/internal/ship/ship_test.go`
- Modify: `justfile` (build recipes and every recipe that ran `build/flocc-setup`)

**Interfaces:**
- Consumes (Part 2a): `ship.Main(args []string) int`, `ship.LoginMain(args []string) int`, `ship.HostMain(args []string) int`, `ship.FleetMain(args []string, out io.Writer) int`; `stageFiles(ctx, r, root, dir, arch, configDir string, private func(string) (bool, error)) error`; the `stageOK` runner and `shipRoot` helper in `ship_test.go`; `installer/main.go`'s `run`, whose `switch args[0]` dispatches `host`, `fleet`, `ship` and `login` to the `ship` package.
- Produces: package `cli` with `func Main(args []string) int`, `type deps struct { out, err io.Writer; home string; getenv func(string) string }`, `func run(ctx context.Context, d *deps, args []string) int`, and the `usage` constant. Later tasks add fields to `deps` and cases to the `switch` in `run`. `installer/cmd/flocc/main.go` calls `cli.Main`. `stageFiles` builds `<dir>/flocc-setup` with `go build` instead of copying `build/flocc-setup-linux-<arch>`.

- [ ] **Step 1: Move the installer's main package**

```bash
mkdir -p installer/cmd/flocc-setup
git mv installer/main.go installer/cmd/flocc-setup/main.go
git mv installer/main_test.go installer/cmd/flocc-setup/main_test.go
```

In `installer/cmd/flocc-setup/main.go`, delete the block at the top of `run` that dispatches the desktop subcommands (the `if len(args) > 0 { switch args[0] { case "host": … case "login": … } }` block Part 2a wrote). Drop the `ship` import if nothing else in the file uses it (`go build` names it).

Run: `go build ./installer/... && go test ./installer/cmd/flocc-setup/`
Expected: builds; `ok  github.com/NovusEdge/flocc/installer/cmd/flocc-setup`.

- [ ] **Step 2: Write the failing tests for the desktop skeleton**

`installer/internal/cli/cli_test.go`:

```go
package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func testDeps(t *testing.T) (*deps, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errb bytes.Buffer
	return &deps{out: &out, err: &errb, home: t.TempDir(), getenv: func(string) string { return "" }}, &out, &errb
}

func TestBareFloccPrintsHelpAndFails(t *testing.T) {
	d, out, errb := testDeps(t)
	if code := run(context.Background(), d, nil); code != 2 || out.Len() != 0 || !strings.Contains(errb.String(), "Desktop commands:") {
		t.Fatalf("%d %q %q", code, out, errb)
	}
}

func TestHelpGoesToStdout(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		d, out, _ := testDeps(t)
		if code := run(context.Background(), d, []string{arg}); code != 0 || !strings.Contains(out.String(), "Desktop commands:") {
			t.Fatalf("%s: %d %q", arg, code, out)
		}
	}
}
```

Add to `installer/internal/ship/ship_test.go` a runner that keeps whole commands, and a test that ship builds the installer for the host's architecture:

```go
// cmdLog keeps every command whole, Dir and Env included, which the Fake's string keys drop.
type cmdLog struct {
	*host.Fake
	cmds []host.Cmd
}

func (c *cmdLog) Run(ctx context.Context, cmd host.Cmd) (host.Result, error) {
	c.cmds = append(c.cmds, cmd)
	return c.Fake.Run(ctx, cmd)
}

func TestStageFilesBuildsTheInstallerForTheHostArch(t *testing.T) {
	root, dir, cfg := t.TempDir(), t.TempDir(), t.TempDir()
	writeJSON(t, filepath.Join(cfg, "settings.json"), map[string]any{})
	writeJSON(t, filepath.Join(cfg, "plugins/known_marketplaces.json"), map[string]any{})
	f := &cmdLog{Fake: host.NewFake().
		On("git -C "+root+" config user.name", 0, "Ada L\n").
		On("git -C "+root+" config user.email", 0, "ada@example.test\n").
		On("go build -o "+dir+"/flocc-setup ./installer/cmd/flocc-setup", 0, "").
		On("git -C "+root+" bundle create "+dir+"/flocc.bundle HEAD", 0, "")}
	if err := stageFiles(context.Background(), f, root, dir, "arm64", cfg, func(string) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(f.cmds, func(c host.Cmd) bool { return c.Name == "go" })
	if i < 0 {
		t.Fatalf("no go build: %v", f.Calls)
	}
	build := f.cmds[i]
	for _, want := range []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64"} {
		if !slices.Contains(build.Env, want) {
			t.Fatalf("%s missing from %q", want, build.Env)
		}
	}
	if build.Dir != root {
		t.Fatalf("built in %q, not the repo root", build.Dir)
	}
}
```

In the same file, update the code that wrote a prebuilt binary:

- `shipRoot`: delete the `os.MkdirAll(... "build" ...)` and `os.WriteFile(... "flocc-setup-linux-amd64" ...)` lines.
- `stageOK.Run`: accept the build the way it accepts git, by adding `case "go": return host.Result{}, nil` to its `switch c.Name`.
- `TestStageFilesArchivesPrivateMarketplaces`: delete the same two lines and add `On("go build -o "+dir+"/flocc-setup ./installer/cmd/flocc-setup", 0, "").` to the Fake chain.

- [ ] **Step 3: Run the tests to see them fail**

Run: `go test ./installer/internal/cli/ ./installer/internal/ship/`
Expected: FAIL: `cli` does not compile (`undefined: deps`, `undefined: run`); `TestStageFilesBuildsTheInstallerForTheHostArch` fails with `no go build` or a read error for `build/flocc-setup-linux-arm64`.

- [ ] **Step 4: Build the installer in the stage**

In `installer/internal/ship/ship.go`, `stageFiles`, replace the block that reads `build/flocc-setup-linux-<arch>` and writes `<dir>/flocc-setup` with:

```go
	// Built here, not read from build/, so a stale installer never ships next to a newer bundle.
	if _, err := host.Must(ctx, r, host.Cmd{Name: "go", Dir: root,
		Args: []string{"build", "-o", filepath.Join(dir, "flocc-setup"), "./installer/cmd/flocc-setup"},
		Env:  []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=" + arch}}); err != nil {
		return err
	}
```

- [ ] **Step 5: Write the desktop skeleton**

`installer/internal/cli/cli.go`:

```go
// Package cli is the desktop flocc command. Desktop verbs run here; any other command line runs
// on a host as the service user.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/NovusEdge/flocc/installer/internal/ship"
)

const usage = `flocc runs Claude sessions on Linear issues. This is its desktop command.

Desktop commands:
  flocc keys                         make, check and store the Linear key, GitHub tokens and ntfy topic
  flocc keys check                   check every stored key again
  flocc keys rotate github|linear    replace keys and push them to every host
  flocc host add <name> [--ssh DEST | --daytona SANDBOX]
  flocc host import -H <name> [--ssh DEST | --daytona SANDBOX]
  flocc host run -H <name> -- <command...>
  flocc ship -H <name> [--interactive]    run it inside the flocc checkout: it ships that commit
  flocc login -H <name> [--email ADDR]

Host commands run on the host as the service user:
  flocc [-H <name>] <command> [args...]   e.g. flocc -H flocc-nimble doctor, flocc release NF-12
  Without -H: $FLOCC_HOST, then the only host, then the host holding the issue named.
  -H all runs status, doctor or logs on every host.
`

type deps struct {
	out, err io.Writer
	home     string
	getenv   func(string) string
}

func Main(args []string) int {
	home, _ := os.UserHomeDir()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return run(ctx, &deps{out: os.Stdout, err: os.Stderr, home: home, getenv: os.Getenv}, args)
}

func run(ctx context.Context, d *deps, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(d.err, usage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(d.out, usage)
		return 0
	case "ship":
		return ship.Main(args[1:])
	case "login":
		return ship.LoginMain(args[1:])
	case "fleet":
		return ship.FleetMain(args[1:], d.out)
	case "host":
		return ship.HostMain(args[1:])
	}
	fmt.Fprintf(d.err, "flocc: unknown command %q; flocc help lists them\n", args[0])
	return 2
}
```

`ship`, `login`, `fleet` and `host` keep Part 2a's argument forms (an SSH target, not a host name) until Tasks 4 and 10 replace them.

`ctx` is unused until Task 4; keep the parameter so later tasks do not change the signature (Go allows unused parameters).

`installer/cmd/flocc/main.go`:

```go
package main

import (
	"os"

	"github.com/NovusEdge/flocc/installer/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:])) }
```

- [ ] **Step 6: Point the recipes at the new binaries**

In `justfile`, replace the `build-setup` and `build-desktop` recipes with one recipe:

```just
# Build the desktop flocc command into build/
[group('build')]
build:
    mkdir -p build
    go build -o build/flocc ./installer/cmd/flocc
```

In every recipe that depended on `build-setup` or `build-desktop`, change the dependency to `build` and the binary from `build/flocc-setup` to `build/flocc`. The commands after the binary name stay as they are (`ship`, `login`, `host`, `fleet`).

- [ ] **Step 7: Run the tests to see them pass**

Run: `go test ./installer/... && go vet ./... && just build && build/flocc; echo "exit $?"`
Expected: all packages `ok`; `build/flocc` prints the usage to stderr and `exit 2`.

- [ ] **Step 8: Commit**

```bash
git add -A installer justfile
git commit -s -m "Split the desktop flocc command from the staged flocc-setup installer"
```

---

### Task 2: Python host CLI: desktop verbs, `logs`, `attach`

**Depends on:** nothing in this plan.

The host CLI must answer the desktop verbs with a refusal, and gains the two commands the `just logs` and `just attach` recipes ran as raw shell, so `flocc -H <host> logs -f` and `flocc -H <host> attach NF-12` work through the same grammar as every other host command. The desktop takes `-H` out of the command line, so no host command may define it.

**Files:**
- Modify: `src/flocc/cli.py`
- Modify: `src/flocc/status.py` (rename `_detect_mode` to `detect_mode`)
- Test: `tests/test_cli.py`

**Interfaces:**
- Consumes: `flocc.sessions.SOCKET` (`"flocc"` after Part 1), `flocc.cli._config`, `flocc.cli.build_parser`, `flocc.cli.main`.
- Produces: `flocc.cli.DESKTOP_VERBS = ("ship", "login", "keys", "host")`; host commands `flocc logs [-n LINES] [-f]` and `flocc attach <session>`; `flocc.status.detect_mode() -> str`.

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_cli.py` (add `import argparse` and `import os` to its imports, and `build_parser` to the `from flocc.cli import ...` line):

```python
@pytest.mark.parametrize(
    "argv",
    [["ship"], ["keys", "rotate", "github"], ["host", "add", "x"], ["--config", "c.toml", "login"]],
)
def test_desktop_verbs_are_refused_on_a_host(argv, capsys):
    assert main(argv) == 2
    assert "that is a desktop command" in capsys.readouterr().err


def test_no_host_command_takes_dash_h():
    # The desktop removes -H from the command line before it reaches the host.
    parser = build_parser()
    parsers = [parser]
    for action in parser._actions:
        if isinstance(action, argparse._SubParsersAction):
            parsers += action.choices.values()
    for p in parsers:
        for action in p._actions:
            assert not {"-H", "--host"} & set(action.option_strings), p.prog


class Exec(Exception):
    pass


@pytest.fixture
def host_cfg(tmp_path, monkeypatch):
    path = tmp_path / "c.toml"
    path.write_text(EXAMPLE.read_text().replace("~/.local/state/flocc", str(tmp_path / "state")))

    def fake_exec(file, argv):
        raise Exec(argv)

    monkeypatch.setattr(os, "execvp", fake_exec)
    return path


def test_logs_reads_the_journal_on_a_systemd_host(host_cfg, monkeypatch):
    monkeypatch.setattr("flocc.status.detect_mode", lambda: "systemd")
    with pytest.raises(Exec) as exc:
        main(["--config", str(host_cfg), "logs", "-n", "50", "-f"])
    assert exc.value.args[0] == [
        "journalctl", "--user", "-u", "flocc", "--no-pager", "-f", "-n", "50"
    ]


def test_logs_tails_run_log_on_a_loop_host(host_cfg, tmp_path, monkeypatch):
    monkeypatch.setattr("flocc.status.detect_mode", lambda: "loop")
    with pytest.raises(Exec) as exc:
        main(["--config", str(host_cfg), "logs"])
    assert exc.value.args[0] == ["tail", "-n", "100", str(tmp_path / "state" / "run.log")]


def test_attach_is_read_only_on_the_service_socket(host_cfg):
    with pytest.raises(Exec) as exc:
        main(["--config", str(host_cfg), "attach", "NF-12-review"])
    assert exc.value.args[0] == ["tmux", "-L", "flocc", "attach", "-r", "-t", "NF-12-review"]
```

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_cli.py -k "desktop or dash_h or logs or attach"`
Expected: FAIL: the desktop-verb cases exit 2 with argparse's `invalid choice` text instead of "that is a desktop command"; `logs` and `attach` are `invalid choice`.

- [ ] **Step 3: Implement**

In `src/flocc/status.py`, rename `_detect_mode` to `detect_mode` (definition and its one call in `collect`).

In `src/flocc/cli.py`, add after `cmd_status`:

```python
def cmd_logs(args) -> int:
    from flocc.status import detect_mode

    cfg, _ = _config(args)
    follow = ["-f"] if args.follow else []
    lines = ["-n", str(args.lines)]
    if detect_mode() == "systemd":
        argv = ["journalctl", "--user", "-u", "flocc", "--no-pager", *follow, *lines]
    else:
        argv = ["tail", *follow, *lines, str(cfg.state_root / "run.log")]
    os.execvp(argv[0], argv)


def cmd_attach(args) -> int:
    from flocc.sessions import SOCKET

    argv = ["tmux", "-L", SOCKET, "attach", "-r", "-t", args.session]
    os.execvp(argv[0], argv)
```

Add `"logs": cmd_logs,` and `"attach": cmd_attach,` to `COMMANDS`. In `build_parser`, after the `status` parser:

```python
    logs = add("logs", "Print the service log: the journal on systemd hosts, run.log otherwise")
    logs.add_argument("-n", dest="lines", type=int, default=100, help="lines to print")
    logs.add_argument("-f", "--follow", action="store_true", help="keep printing new lines")
    attach = add("attach", "Attach read-only to an agent's tmux session")
    attach.add_argument("session", help="session name, e.g. NF-12 or NF-12-review")
```

Add above `main`:

```python
DESKTOP_VERBS = ("ship", "login", "keys", "host")


def _command(argv: list[str]) -> str | None:
    it = iter(argv)
    for arg in it:
        if arg == "--config":
            next(it, None)
        elif not arg.startswith("-"):
            return arg
    return None
```

and make `main` refuse them before parsing (argparse would report `invalid choice`, or choke on the `-H` that follows):

```python
def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    if (verb := _command(argv)) in DESKTOP_VERBS:
        print(f"flocc {verb}: that is a desktop command; run it on your desktop", file=sys.stderr)
        return 2
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
        return COMMANDS[args.command](args)
    except SystemExit as exc:
        return int(exc.code or 0)
```

`cmd_logs` and `cmd_attach` keep the `-> int` annotation the `COMMANDS` table expects; `os.execvp` does not return.

- [ ] **Step 4: Run the tests to see them pass**

Run: `uv run pytest -q && uv run ruff check . && uv run ruff format --check .`
Expected: all pass, no lint findings.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/cli.py src/flocc/status.py tests/test_cli.py
git commit -s -m "Host CLI: refuse desktop verbs, add logs and attach"
```

---

### Task 3: Host files carry the answers

**Depends on:** nothing in this plan.

Part 1 made package `installer/internal/hosts`, whose `Host` holds only `ssh` or `daytona`. In 2b the host file is the only source of a host's answers, so `hosts.Host` embeds `answers.Answers` (never the secrets). This task extends that package; it adds no second host store. It also tightens what a host file may hold: the destination reaches remote command lines, so it must pass the same rule as a ship target, and `all` cannot be a host name because `-H all` means every host. The rule moves from `ship` into `hosts`, because `ship` imports `hosts` and the reverse import would be a cycle.

**Files:**
- Modify: `installer/internal/hosts/hosts.go`
- Test: `installer/internal/hosts/hosts_test.go`
- Modify: `installer/internal/ship/ship.go` (`ValidTarget` calls `hosts.ValidTarget`; `targetRE` goes)

**Interfaces:**
- Consumes (Part 1 Task 6): `hosts.Host{Name, SSH, Daytona}`, `(Host).Target`, `hosts.Dir`, `hosts.Load`, `hosts.List(dir) ([]Host, error)`, `hosts.Save`, `hosts.ConvertOld`, `checkName`, the test helper `write(t, path, body)`. (Part 2a): `ship.ValidTarget`, `targetRE`, `answers.Answers`.
- Produces: `type hosts.Host struct { Name string; SSH string; Daytona string; answers.Answers }` (TOML keys `ssh`, `daytona`, then the answers' own keys, flattened); `hosts.ValidTarget(t string) bool`; `hosts.Load` clears `Secrets`, refuses a destination `ValidTarget` refuses, and wraps `os.ErrNotExist` for a missing file; `hosts.Save` drops `Secrets`, refuses what `Load` would refuse, and writes through a temp file and a rename; `checkName` refuses `all`. `hosts.List` keeps its `([]Host, error)` contract: callers print each line of the error as a note, as Part 1's `discover` does. `ship.ValidTarget` stays, as a call to `hosts.ValidTarget`.

- [ ] **Step 1: Write the failing tests**

Append to `installer/internal/hosts/hosts_test.go` (add `"github.com/NovusEdge/flocc/installer/internal/answers"` and `"github.com/pelletier/go-toml/v2"` to its imports):

```go
func TestHostFileKeepsTheAnswersFlatAndNeverASecret(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hosts")
	h := Host{Name: "flocc-nimble", SSH: "root@flocc-nimble", Answers: answers.Answers{
		TeamKey: "NF", DigestIssue: "NF-186", NtfyTopic: "flocc-x",
		Repos:   []answers.Repo{{Name: "nimblefox", URL: "https://github.com/o/nimblefox", BaseBranch: "main", Check: "just check", Draft: true}},
		Secrets: &answers.Secrets{LinearKey: "lin_api_s3cret"},
	}}
	if err := Save(dir, h); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "flocc-nimble.toml")
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "s3cret") || strings.Contains(string(b), "secrets") {
		t.Fatalf("a secret reached the host file:\n%s", b)
	}
	// go-toml flattens the embedded Answers, so the file reads like a host's own answers.toml.
	var raw map[string]any
	if err := toml.Unmarshal(b, &raw); err != nil || raw["team_key"] != "NF" || raw["ssh"] != "root@flocc-nimble" || raw["Answers"] != nil {
		t.Fatalf("%v %v\n%s", raw, err, b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	got, err := Load(dir, "flocc-nimble")
	if err != nil || got.Target() != "root@flocc-nimble" || got.TeamKey != "NF" || got.Repos[0].Name != "nimblefox" || got.Secrets != nil {
		t.Fatalf("%+v %v", got, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".host-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestLoadRefusesWhatSSHOrTheShellWouldMisread(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "none.toml"), "team_key = \"NF\"\n")
	write(t, filepath.Join(dir, "shell.toml"), "ssh = \"h;id\"\n")
	write(t, filepath.Join(dir, "subst.toml"), "daytona = \"$(id)\"\n")
	write(t, filepath.Join(dir, "all.toml"), "ssh = \"root@all\"\n")
	write(t, filepath.Join(dir, "secret.toml"), "ssh = \"root@x\"\n[secrets]\nlinear_key = \"lin_api_s3cret\"\n")
	for _, name := range []string{"none", "shell", "subst", "all", "../x"} {
		if _, err := Load(dir, name); err == nil {
			t.Errorf("%s loaded", name)
		}
	}
	if h, err := Load(dir, "secret"); err != nil || h.Secrets != nil {
		t.Fatalf("%+v %v", h, err)
	}
	if _, err := Load(dir, "absent"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	for _, h := range []Host{{Name: "all", SSH: "root@all"}, {Name: "x", SSH: "h;id"}, {Name: "x"}} {
		if err := Save(dir, h); err == nil {
			t.Errorf("%+v saved", h)
		}
	}
}

func TestValidTarget(t *testing.T) {
	for _, ok := range []string{"root@flocc-nimble", "daytona:flocc-smoke", "box.tailnet.ts.net"} {
		if !ValidTarget(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "-oProxyCommand=x", "h;rm -rf ~", "a b", "$(id)", "h`id`"} {
		if ValidTarget(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
```

Part 1's `TestListReturnsGoodHostsByNameAndNamesTheBadOnes` already pins that `List` returns Daytona hosts as well as SSH ones, and `TestDiscoverSkipsDaytonaWithoutItsConfigAndKeepsTheHostsDir` that the fleet keeps only the SSH ones.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/internal/hosts/`
Expected: FAIL to build, `unknown field Answers in struct literal`, `undefined: ValidTarget`.

- [ ] **Step 3: Implement**

In `installer/internal/hosts/hosts.go`, add `"github.com/NovusEdge/flocc/installer/internal/answers"` to the imports and replace `Host`, `checkName`, `Load` and `Save` with:

```go
// Host is one host file: how to reach the host, and the answers ship sends it, so the file is the
// only place they live. It sets exactly one of SSH and Daytona and never holds a secret: those
// come from the desktop's key store at ship time.
type Host struct {
	Name    string `toml:"-"`
	SSH     string `toml:"ssh,omitempty"`
	Daytona string `toml:"daytona,omitempty"`
	answers.Answers
}

var targetRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:-]*$`)

// ValidTarget reports whether t is safe as an ssh destination: no leading dash, which ssh would
// read as an option, and no shell metacharacters, since the target reaches remote command lines.
func ValidTarget(t string) bool { return targetRE.MatchString(t) }

func checkName(name string) error {
	switch {
	case !validName.MatchString(name):
		return fmt.Errorf("host name %q: start with a letter or digit, then use letters, digits, '.', '_' or '-'", name)
	case name == "all":
		return errors.New("host name all: -H all means every host, so no host can be called all")
	}
	return nil
}

func check(h Host) error {
	switch {
	case (h.SSH == "") == (h.Daytona == ""):
		return fmt.Errorf("%s.toml: set exactly one of ssh and daytona", h.Name)
	case !ValidTarget(h.Target()):
		return fmt.Errorf("%s.toml: %q is not an ssh destination or daytona sandbox", h.Name, h.Target())
	}
	return nil
}

func Load(dir, name string) (Host, error) {
	if err := checkName(name); err != nil {
		return Host{}, err
	}
	b, err := os.ReadFile(filepath.Join(dir, name+".toml"))
	if err != nil {
		return Host{}, err
	}
	var h Host
	if err := toml.Unmarshal(b, &h); err != nil {
		return Host{}, fmt.Errorf("%s.toml: %w", name, err)
	}
	h.Name, h.Secrets = name, nil
	if err := check(h); err != nil {
		return Host{}, err
	}
	return h, nil
}

func Save(dir string, h Host) error {
	if err := checkName(h.Name); err != nil {
		return err
	}
	if err := check(h); err != nil {
		return err
	}
	h.Secrets = nil
	b, err := toml.Marshal(h)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Written beside it and renamed: List reads *.toml, so it never sees a half-written host.
	// CreateTemp makes the file mode 600.
	tmp, err := os.CreateTemp(dir, ".host-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, h.Name+".toml"))
}
```

`validName`, `Target`, `Dir`, `List` and `ConvertOld` stay. In `ConvertOld`, replace the `strings.HasPrefix(t, "-")` test with `!ValidTarget(t)` and its message with `fmt.Sprintf("dropped %q: not an ssh destination", t)`, so a line `Save` would now refuse is dropped instead of stopping the conversion.

In `installer/internal/ship/ship.go`, delete `targetRE` and make `ValidTarget` call the moved rule (drop the `regexp` import if nothing else uses it):

```go
// ValidTarget is hosts.ValidTarget: ship and the host files take the same targets.
func ValidTarget(t string) bool { return hosts.ValidTarget(t) }
```

with `"github.com/NovusEdge/flocc/installer/internal/hosts"` added to ship.go's imports.

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test ./installer/internal/hosts/ ./installer/internal/ship/ && go vet ./installer/...`
Expected: `ok` for both; Part 1's `TestSaveLoadAndTarget`, `TestListReturnsGoodHostsByNameAndNamesTheBadOnes` and `TestConvertOldWritesOneFilePerHostOnce`, and Part 2a's `TestValidTarget` in `ship`, still pass.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/hosts installer/internal/ship/ship.go
git commit -s -m "Host files carry the host's answers; one target rule for host files and ship"
```

---

### Task 4: Host commands over `-H`

**Depends on:** Tasks 1 and 3.

Any command line that is not a desktop verb runs on a host as the service user, as `flocc` followed by each argument quoted on its own. This task adds the host choice (`-H`, `$FLOCC_HOST`, the only host, the host holding the issue named), `-H all` for the read-only commands, `flocc host run` for a raw command, and SSH connection sharing. The `just` recipes that ran raw shell on a host become wrappers.

**Files:**
- Create: `installer/internal/cli/remote.go`, `installer/internal/cli/host.go`
- Create: `installer/internal/cli/remote_test.go`
- Modify: `installer/internal/cli/cli.go`
- Modify: `installer/internal/ship/host.go` (`FloccLine`, `OnHost`, `Capture`; `HostMain` and `hostRun` go), `installer/internal/ship/host_test.go`
- Modify: `installer/internal/ship/daytona.go` (`Resolve`, `sshMux`)
- Modify: `installer/internal/ship/fleet.go` (`Statuses`, `destCache.sshOpts`), `installer/internal/ship/fleet_test.go`
- Modify: `installer/internal/ship/login.go` (rename `exitCode` to `ExitCode`), `installer/internal/ship/ship.go` (the same rename; `connect` calls `Resolve`)
- Modify: `installer/internal/ship/ship_test.go` (`TestResolveSharesOneConnectionPerHost`)
- Modify: `justfile` (host recipes)

**Interfaces:**
- Consumes: Task 1 (`deps`, `run`, `usage`), Task 3 (`hosts.Host`, `hosts.Dir`, `hosts.List`, `hosts.Save`), Part 2a (`ship.Dest`, `sshCmd`, `sshNoTTY`, `asServiceUser`, `shQuote`, `scrub`, `exitCode`, `connect`, `resolveTarget`, `realWaiter`, `host.Cmd.Stdin`), the fleet's `collect`, `newDestCache`, `destCache`, `FleetHost`, `Row`, `Report`.
- Produces:
  - `ship.Resolve(ctx context.Context, target string) (ship.Dest, error)`: the Part 2a target resolution plus `-o ControlMaster=auto -o ControlPath=<home>/.ssh/flocc-%C -o ControlPersist=10m`. Part 2a's `connect` calls it, so `ship.Ship` and `ship.Login` share connections too.
  - `ship.FloccLine(args []string) []string`; `ship.OnHost(ctx, r host.Runner, dest Dest, command []string, tty bool) error`; `ship.Capture(ctx, r host.Runner, dest Dest, command []string, stdin io.Reader) (host.Result, error)` (built with `sshNoTTY` and `BatchMode=yes`); `ship.Statuses(ctx, r host.Runner, hs []hosts.Host) []Row` (SSH hosts share connections too); `ship.ExitCode(err error, what string) int`.
  - In `cli`: `deps` gains `run host.Runner`, `tty bool`, `resolve func(context.Context, string) (ship.Dest, error)`, `fleet func(args []string) int`; `splitHost(args []string) (name string, rest []string, err error)`; `listHosts(d *deps) []hosts.Host` (prints each problem file as a note); `pickHost(ctx, d *deps, name string, args []string) (hosts.Host, error)`; `readOnly = []string{"status", "doctor", "logs"}`; `hostCmd(ctx context.Context, d *deps, name string, args []string) int` in `cli/host.go` (args is the line after `host`, `args[0]` the sub-verb, `name` the `-H` value; Part 3 adds `case "create"` to its switch). `run` now strips `-H` before dispatching, so later verbs receive it as `name`.

- [ ] **Step 1: Write the failing ship tests**

In `installer/internal/ship/host_test.go`, replace `TestHostRunAttachesATerminalAndScrubsTheToken` with:

```go
func TestOnHostAttachesATerminalOnlyWhenAskedAndScrubsTheToken(t *testing.T) {
	remote := asServiceUser([]string{"tmux", "ls"})
	line := "ssh -o StrictHostKeyChecking=accept-new -t tok@h " + remote
	f := host.NewFake().OnInteractive(line, errors.New("ssh tok@h failed"))
	dest := Dest{SSH: "tok@h", Token: "tok", Opts: []string{"-o", "StrictHostKeyChecking=accept-new"}}
	err := OnHost(context.Background(), f, dest, []string{"tmux", "ls"}, true)
	if !f.Ran("interactive: " + line) {
		t.Fatalf("%v", f.Calls)
	}
	if err == nil || strings.Contains(err.Error(), "tok@") || !strings.Contains(err.Error(), "<token>@h") {
		t.Fatal(err)
	}
	OnHost(context.Background(), f, Dest{SSH: "h"}, []string{"tmux", "ls"}, false)
	if !f.Ran("interactive: ssh h " + remote) {
		t.Fatalf("%v", f.Calls)
	}
}

// -T, because a RequestTTY setting in the owner's ssh config would give cat a pty, which echoes
// the secret on stdin into the output and never passes EOF.
func TestCaptureSendsStdinWithoutATerminalAndScrubsOutput(t *testing.T) {
	remote := asServiceUser([]string{"cat"})
	line := "ssh -o BatchMode=yes -T tok@h " + remote
	f := host.NewFake()
	f.Responses[line] = host.Result{Stderr: "warning tok@h", Code: 3}
	res, err := Capture(context.Background(), f, Dest{SSH: "tok@h", Token: "tok"}, []string{"cat"}, strings.NewReader("in"))
	if err != nil || res.Code != 3 || f.Stdins[line] != "in" || strings.Contains(res.Stderr, "tok@") {
		t.Fatalf("%+v %v %v", res, err, f.Stdins)
	}
}

func TestFloccLineQuotesEachArgument(t *testing.T) {
	got := shWords(t, strings.Join(FloccLine([]string{"escalate", "--summary", "it's $HOME and `id`"}), " "))
	if strings.Join(got, "|") != "flocc|escalate|--summary|it's $HOME and `id`" {
		t.Fatalf("%q", got)
	}
}
```

Append to `installer/internal/ship/ship_test.go`:

```go
func TestResolveSharesOneConnectionPerHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	d, err := Resolve(context.Background(), "root@h")
	if err != nil || d.SSH != "root@h" {
		t.Fatalf("%+v %v", d, err)
	}
	opts := strings.Join(d.Opts, " ")
	for _, want := range []string{"ControlMaster=auto", "ControlPersist=10m", "ControlPath=" + filepath.Join(home, ".ssh", "flocc-%C")} {
		if !strings.Contains(opts, want) {
			t.Errorf("%s missing from %q", want, opts)
		}
	}
	if st, err := os.Stat(filepath.Join(home, ".ssh")); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("%v %v", st, err)
	}
}
```

Append to `installer/internal/ship/fleet_test.go` (it imports `hosts` since Part 1):

```go
// destCache hands an ssh host its destination without minting one, so the sharing must come
// from the cache itself.
func TestStatusesSharesConnectionsForSSHHosts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := host.NewFake()
	Statuses(context.Background(), f, []hosts.Host{{Name: "a", SSH: "root@a"}})
	if len(f.Calls) != 1 || !strings.Contains(f.Calls[0], "ControlMaster=auto") || !strings.Contains(f.Calls[0], "BatchMode=yes") {
		t.Fatalf("%v", f.Calls)
	}
}
```

- [ ] **Step 2: Write the failing cli tests**

`installer/internal/cli/remote_test.go`:

```go
package cli

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/ship"
)

// fakeSSH answers ssh by destination: Run returns out[dest], and a destination it does not know
// fails as an unreachable host. files answers a remote command containing "cat <path>" with
// files[path] on a reachable host, and exit 1 when path is not in it. Commands are kept whole,
// with the stdin each Run got.
type fakeSSH struct {
	mu          sync.Mutex
	out         map[string]string
	files       map[string]string
	runs        []host.Cmd
	stdins      []string
	interactive []host.Cmd
}

func (f *fakeSSH) Run(_ context.Context, c host.Cmd) (host.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, c)
	in := ""
	if c.Stdin != nil {
		b, _ := io.ReadAll(c.Stdin)
		in = string(b)
	}
	f.stdins = append(f.stdins, in)
	dest := c.Args[len(c.Args)-2]
	out, ok := f.out[dest]
	if !ok {
		return host.Result{Stderr: "ssh: connect to host " + dest + ": No route to host", Code: 255}, nil
	}
	if f.files != nil {
		if _, path, found := strings.Cut(c.Args[len(c.Args)-1], "cat "); found {
			path = strings.Trim(path, `'"`)
			body, ok := f.files[path]
			if !ok {
				return host.Result{Stderr: "cat: " + path + ": No such file or directory", Code: 1}, nil
			}
			return host.Result{Stdout: body}, nil
		}
	}
	return host.Result{Stdout: out}, nil
}

func (f *fakeSSH) Interactive(_ context.Context, c host.Cmd) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interactive = append(f.interactive, c)
	return nil
}

// hostDeps makes one host file per name, reached as root@<name>. HOME is the test's, because
// ship.Statuses makes ~/.ssh for its connection sharing.
func hostDeps(t *testing.T, names ...string) (*deps, *fakeSSH, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	d, out, errb := testDeps(t)
	t.Setenv("HOME", d.home)
	for _, n := range names {
		if err := hosts.Save(hosts.Dir(d.home), hosts.Host{Name: n, SSH: "root@" + n}); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeSSH{out: map[string]string{}}
	d.run = f
	d.resolve = func(_ context.Context, target string) (ship.Dest, error) { return ship.Dest{SSH: target}, nil }
	d.fleet = func([]string) int { t.Fatal("fleet view called"); return 0 }
	return d, f, out, errb
}

// shWords has sh split line into words, as the ssh user's shell does.
func shWords(t *testing.T, line string) []string {
	t.Helper()
	out, err := exec.Command("sh", "-c", "for a in "+line+`; do printf '%s\0' "$a"; done`).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
}

func TestSplitHostStopsAtDoubleDash(t *testing.T) {
	name, rest, err := splitHost([]string{"release", "-H", "a", "NF-1", "--", "-H", "x"})
	if err != nil || name != "a" || strings.Join(rest, " ") != "release NF-1 -- -H x" {
		t.Fatalf("%q %q %v", name, rest, err)
	}
	if name, _, _ := splitHost([]string{"--host=b", "status"}); name != "b" {
		t.Fatal(name)
	}
	if _, _, err := splitHost([]string{"-H", "a", "-H", "b", "status"}); err == nil {
		t.Fatal("two -H accepted")
	}
	if _, _, err := splitHost([]string{"status", "-H"}); err == nil {
		t.Fatal("-H without a name accepted")
	}
}

func TestHostChoiceOrder(t *testing.T) {
	d, _, _, _ := hostDeps(t, "a", "b")
	ctx := context.Background()
	if h, err := pickHost(ctx, d, "b", nil); err != nil || h.Name != "b" {
		t.Fatalf("%+v %v", h, err)
	}
	d.getenv = func(k string) string { return map[string]string{"FLOCC_HOST": "a"}[k] }
	if h, err := pickHost(ctx, d, "", []string{"status"}); err != nil || h.Name != "a" {
		t.Fatalf("%+v %v", h, err)
	}
	if _, err := pickHost(ctx, d, "c", nil); err == nil || !strings.Contains(err.Error(), "flocc host add c") {
		t.Fatal(err)
	}
	if _, err := pickHost(ctx, d, "all", []string{"release", "NF-1"}); err == nil || !strings.Contains(err.Error(), "-H all runs only") {
		t.Fatal(err)
	}
	d.getenv = func(string) string { return "" }
	if _, err := pickHost(ctx, d, "", []string{"status"}); err == nil || !strings.Contains(err.Error(), "a, b") {
		t.Fatal(err)
	}
	one, _, _, _ := hostDeps(t, "solo")
	if h, err := pickHost(ctx, one, "", []string{"tick"}); err != nil || h.Name != "solo" {
		t.Fatalf("%+v %v", h, err)
	}
	none, _, _, _ := hostDeps(t)
	if _, err := pickHost(ctx, none, "", []string{"tick"}); err == nil || !strings.Contains(err.Error(), "flocc host add") {
		t.Fatal(err)
	}
}

const holdsNF12 = `{"host":"b","claims":[{"issue":"NF-12","state":"blocked","phase":"impl"}]}`

func TestAnIssueKeyPicksTheHostHoldingTheClaim(t *testing.T) {
	d, f, _, _ := hostDeps(t, "a", "b")
	f.out["root@a"] = `{"host":"a","claims":[]}`
	f.out["root@b"] = holdsNF12
	if code := run(context.Background(), d, []string{"release", "NF-12", "--keep-worktree"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(f.interactive) != 1 || f.interactive[0].Args[len(f.interactive[0].Args)-2] != "root@b" {
		t.Fatalf("%v", f.interactive)
	}
}

func TestAnIssueKeyNoHostOrTwoHostsHoldIsAnError(t *testing.T) {
	d, f, _, errb := hostDeps(t, "a", "b", "c")
	f.out["root@a"] = `{"host":"a","claims":[]}`
	f.out["root@b"] = `{"host":"b","claims":[]}`
	if code := run(context.Background(), d, []string{"release", "NF-12"}); code == 0 || len(f.interactive) != 0 {
		t.Fatalf("exit %d %v", code, f.interactive)
	}
	if msg := errb.String(); !strings.Contains(msg, "no reachable host holds NF-12") || !strings.Contains(msg, "unreachable: c") || !strings.Contains(msg, "-H") {
		t.Fatal(msg)
	}
	f.out["root@a"], f.out["root@b"], f.out["root@c"] = holdsNF12, holdsNF12, holdsNF12
	errb.Reset()
	if code := run(context.Background(), d, []string{"release", "NF-12"}); code == 0 || !strings.Contains(errb.String(), "a, b and c") {
		t.Fatalf("exit %d %q", code, errb.String())
	}
}

func TestHostCommandArgumentsArriveIntact(t *testing.T) {
	d, f, _, _ := hostDeps(t, "a")
	args := []string{"escalate", "--summary", "it's $HOME and `id`", "--action", "two  words"}
	if code := run(context.Background(), d, append([]string{"-H", "a"}, args...)); code != 0 {
		t.Fatalf("exit %d", code)
	}
	c := f.interactive[0]
	words := shWords(t, c.Args[len(c.Args)-1])
	script := strings.TrimPrefix(words[len(words)-1], "cd && ")
	out, err := exec.Command("sh", "-c", `flocc() { printf '%s\n' "$@"; }; `+script).Output()
	if err != nil || string(out) != strings.Join(args, "\n")+"\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestSSHGetsATerminalOnlyWhenStdinIsOne(t *testing.T) {
	for _, tty := range []bool{false, true} {
		d, f, _, _ := hostDeps(t, "a")
		d.tty = tty
		run(context.Background(), d, []string{"doctor"})
		if got := strings.Contains(f.interactive[0].String(), "ssh -t "); got != tty {
			t.Errorf("tty=%v: %s", tty, f.interactive[0])
		}
	}
}

func TestDashHAllRunsOnlyReadOnlyCommands(t *testing.T) {
	for _, args := range [][]string{{"release", "NF-1"}, {"tick"}, {"logs", "-f"}, {"logs", "--follow"}} {
		d, f, _, errb := hostDeps(t, "a")
		if code := run(context.Background(), d, append([]string{"-H", "all"}, args...)); code != 2 || len(f.runs)+len(f.interactive) != 0 {
			t.Errorf("%v: exit %d, ran %v %v", args, code, f.runs, f.interactive)
		}
		if errb.Len() == 0 {
			t.Errorf("%v: no reason given", args)
		}
	}
}

func TestDashHAllStatusIsTheFleetView(t *testing.T) {
	d, _, _, _ := hostDeps(t, "a")
	var got []string
	d.fleet = func(args []string) int { got = args; return 0 }
	if code := run(context.Background(), d, []string{"status", "-H", "all", "--watch"}); code != 0 || strings.Join(got, " ") != "--watch" {
		t.Fatalf("exit %d args %q", code, got)
	}
}

func TestDashHAllDoctorReportsEveryHostAndTheUnreachableOne(t *testing.T) {
	d, f, out, _ := hostDeps(t, "a", "b")
	f.out["root@a"] = "ok    config: /x\n"
	code := run(context.Background(), d, []string{"-H", "all", "doctor"})
	o := out.String()
	if code == 0 || !strings.Contains(o, "== a ==\nok    config: /x\n") || !strings.Contains(o, "== b ==") || !strings.Contains(o, "No route to host") {
		t.Fatalf("exit %d\n%s", code, o)
	}
	for _, c := range f.runs {
		if strings.Contains(c.String(), " -t ") {
			t.Errorf("-H all asked for a terminal: %s", c)
		}
	}
}

func TestHostRunPassesARawCommand(t *testing.T) {
	d, f, _, _ := hostDeps(t, "a")
	if code := run(context.Background(), d, []string{"host", "run", "-H", "a", "--", "tmux", "-L", "flocc", "ls"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	words := shWords(t, f.interactive[0].Args[len(f.interactive[0].Args)-1])
	if script := words[len(words)-1]; script != "cd && tmux -L flocc ls" {
		t.Fatalf("%q", script)
	}
}
```

In `cli_test.go`'s `testDeps`, leave the new fields zero: the tests above set them through `hostDeps`.

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./installer/internal/ship/ ./installer/internal/cli/`
Expected: FAIL to build: `undefined: OnHost`, `undefined: Capture`, `undefined: Resolve`, `undefined: splitHost`, `unknown field run in struct literal`.

- [ ] **Step 4: Implement the ship side**

In `installer/internal/ship/daytona.go`, add:

```go
// Resolve is resolveTarget with SSH connection sharing.
func Resolve(ctx context.Context, target string) (Dest, error) {
	mux, err := sshMux()
	if err != nil {
		return Dest{}, err
	}
	home, _ := os.UserHomeDir()
	d, err := resolveTarget(ctx, target, filepath.Join(home, ".config/daytona/config.json"), &http.Client{Timeout: 30 * time.Second}, realWaiter(os.Stderr))
	if err != nil {
		return d, err
	}
	d.Opts = append(d.Opts, mux...)
	return d, nil
}

// sshMux makes every ssh to one host in the next ten minutes reuse the first connection, so one
// Tailscale check-mode approval covers a whole run. %C is a hash of the connection, which keeps
// the socket path inside the 104 bytes macOS allows.
func sshMux() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + filepath.Join(dir, "flocc-%C"), "-o", "ControlPersist=10m"}, nil
}
```

(add `"path/filepath"` to daytona.go's imports if missing). In `ship.go`, in Part 2a's `connect(ctx, target string, secrets ...string)`, replace the `resolveTarget(…)` call with `Resolve(ctx, target)` (keep its redaction of `secrets` and the token), so `Ship` and `Login` share the connection the way `-H` commands do; the `home` local and the `net/http` and `time` imports go if nothing else in ship.go uses them. In `ship.go`, `login.go` and `host.go`, rename `exitCode` to `ExitCode` (its definition in `login.go` and every call). In `installer/internal/ship/host.go`, delete `HostMain` and `hostRun` and add:

```go
// FloccLine is the host's own flocc with each argument quoted on its own, so spaces, quotes and $
// reach the host CLI exactly as typed on the desktop.
func FloccLine(args []string) []string {
	line := []string{"flocc"}
	for _, a := range args {
		line = append(line, shQuote(a))
	}
	return line
}

// OnHost runs command, shell words, on the host as the service user with this terminal's
// streams; tty asks ssh for a terminal.
func OnHost(ctx context.Context, r host.Runner, dest Dest, command []string, tty bool) error {
	return scrub(r.Interactive(ctx, sshCmd(dest, tty, asServiceUser(command))), dest.Token)
}

// Capture runs command on the host as the service user without a terminal and returns what it
// printed; stdin, when set, is the command's input. Callers send and read secrets with it, so it
// forbids a pty (sshNoTTY). BatchMode makes ssh fail instead of waiting at a prompt nobody sees.
func Capture(ctx context.Context, r host.Runner, dest Dest, command []string, stdin io.Reader) (host.Result, error) {
	d := dest
	d.Opts = append([]string{"-o", "BatchMode=yes"}, dest.Opts...)
	c := sshNoTTY(d, asServiceUser(command))
	c.Stdin = stdin
	res, err := r.Run(ctx, c)
	if dest.Token != "" {
		res.Stdout = strings.ReplaceAll(res.Stdout, dest.Token, "<token>")
		res.Stderr = strings.ReplaceAll(res.Stderr, dest.Token, "<token>")
	}
	return res, scrub(err, dest.Token)
}
```

Fix `host.go`'s imports (`io`, `strings`, `context` and `host` are used; drop what is not). In `installer/internal/ship/fleet.go`, give `destCache` a field for the ssh hosts it does not mint for:

```go
type destCache struct {
	mu      sync.Mutex
	m       map[string]cachedDest
	now     func() time.Time
	mint    func(context.Context, FleetHost) (Dest, error)
	sshOpts []string // given to every ssh host, which resolve returns without minting
}
```

make `resolve`'s first branch `return Dest{SSH: h.Target, Opts: c.sshOpts}, nil` (`fetch` copies `Opts` before it appends, so the hosts never share a backing array), and add:

```go
// Statuses runs flocc status --json on every host in parallel, as the fleet view does.
func Statuses(ctx context.Context, r host.Runner, hs []hosts.Host) []Row {
	fhs := make([]FleetHost, len(hs))
	for i, h := range hs {
		fhs[i] = FleetHost{Name: h.Name, Target: h.Target(), Daytona: h.Daytona != ""}
	}
	cache := newDestCache(func(ctx context.Context, h FleetHost) (Dest, error) { return Resolve(ctx, h.Target) })
	// No sharing is still a working lookup, so a missing ~/.ssh does not stop it.
	cache.sshOpts, _ = sshMux()
	return collect(ctx, r, cache, fhs)
}
```

- [ ] **Step 5: Implement the cli side**

`installer/internal/cli/remote.go`:

```go
package cli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/ship"
)

// readOnly are the host commands -H all may run: they change nothing on any host.
var readOnly = []string{"status", "doctor", "logs"}

var issueKey = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]+$`)

// splitHost takes -H and --host out of args. Everything after "--" is left alone, so a raw
// command for host run keeps its own flags.
func splitHost(args []string) (name string, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return name, append(rest, args[i:]...), nil
		}
		var v string
		switch {
		case a == "-H" || a == "--host":
			if i+1 == len(args) {
				return "", nil, fmt.Errorf("%s needs a host name", a)
			}
			i++
			v = args[i]
		case strings.HasPrefix(a, "-H="), strings.HasPrefix(a, "--host="):
			_, v, _ = strings.Cut(a, "=")
		default:
			rest = append(rest, a)
			continue
		}
		if name != "" {
			return "", nil, errors.New("-H given twice")
		}
		name = v
	}
	return name, rest, nil
}

// listHosts is every host file that loads; each one that does not is printed as a note, so one
// bad file hides no other host.
func listHosts(d *deps) []hosts.Host {
	all, err := hosts.List(hosts.Dir(d.home))
	if err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			fmt.Fprintln(d.err, "hosts: "+line)
		}
	}
	return all
}

func names(hs []hosts.Host) []string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Name)
	}
	return out
}

// and joins names the way the messages read: "a", "a and b", "a, b and c".
func and(ns []string) string {
	if len(ns) < 2 {
		return strings.Join(ns, "")
	}
	return strings.Join(ns[:len(ns)-1], ", ") + " and " + ns[len(ns)-1]
}

// pickHost: -H, then $FLOCC_HOST, then the only host, then the host holding the issue named.
func pickHost(ctx context.Context, d *deps, name string, args []string) (hosts.Host, error) {
	if name == "all" {
		return hosts.Host{}, fmt.Errorf("-H all runs only %s; name one host", strings.Join(readOnly, ", "))
	}
	all := listHosts(d)
	if name == "" {
		name = d.getenv("FLOCC_HOST")
	}
	if name != "" {
		if i := slices.IndexFunc(all, func(h hosts.Host) bool { return h.Name == name }); i >= 0 {
			return all[i], nil
		}
		return hosts.Host{}, fmt.Errorf("no host named %s; flocc host add %s", name, name)
	}
	switch len(all) {
	case 0:
		return hosts.Host{}, errors.New("no hosts yet; flocc host add <name>")
	case 1:
		return all[0], nil
	}
	i := slices.IndexFunc(args, issueKey.MatchString)
	if i < 0 {
		return hosts.Host{}, fmt.Errorf("name a host with -H: %s", strings.Join(names(all), ", "))
	}
	key := args[i]
	var holders []hosts.Host
	var unreachable []string
	for j, row := range ship.Statuses(ctx, d.run, all) {
		if row.Report == nil {
			unreachable = append(unreachable, all[j].Name)
			continue
		}
		if slices.ContainsFunc(row.Report.Claims, func(c ship.Claim) bool { return c.Issue == key }) {
			holders = append(holders, all[j])
		}
	}
	switch len(holders) {
	case 1:
		return holders[0], nil
	case 0:
		msg := fmt.Sprintf("no reachable host holds %s", key)
		if len(unreachable) > 0 {
			msg += " (unreachable: " + strings.Join(unreachable, ", ") + ")"
		}
		return hosts.Host{}, fmt.Errorf("%s; name one with -H: %s", msg, strings.Join(names(all), ", "))
	}
	return hosts.Host{}, fmt.Errorf("%s is claimed on %s; name one with -H", key, and(names(holders)))
}

func onHost(ctx context.Context, d *deps, name string, args []string) int {
	h, err := pickHost(ctx, d, name, args)
	if err != nil {
		fmt.Fprintln(d.err, "flocc:", err)
		return 2
	}
	dest, err := d.resolve(ctx, h.Target())
	if err == nil {
		err = ship.OnHost(ctx, d.run, dest, ship.FloccLine(args), d.tty)
	}
	return ship.ExitCode(err, "flocc")
}

// onAll runs a read-only command on every host at once and prints each host's output whole.
func onAll(ctx context.Context, d *deps, args []string) int {
	switch {
	case !slices.Contains(readOnly, args[0]):
		fmt.Fprintf(d.err, "flocc: -H all runs only %s; name one host for %s\n", strings.Join(readOnly, ", "), args[0])
		return 2
	case args[0] == "logs" && (slices.Contains(args, "-f") || slices.Contains(args, "--follow")):
		fmt.Fprintln(d.err, "flocc: logs -f follows one host; name it with -H")
		return 2
	case args[0] == "status":
		return d.fleet(args[1:])
	}
	all := listHosts(d)
	if len(all) == 0 {
		fmt.Fprintln(d.err, "flocc: no hosts yet; flocc host add <name>")
		return 2
	}
	results := make([]host.Result, len(all))
	var wg sync.WaitGroup
	for i, h := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dest, err := d.resolve(ctx, h.Target())
			if err == nil {
				results[i], err = ship.Capture(ctx, d.run, dest, ship.FloccLine(args), nil)
			}
			if err != nil {
				results[i] = host.Result{Stderr: err.Error(), Code: 255}
			}
		}()
	}
	wg.Wait()
	code := 0
	for i, h := range all {
		out := results[i].Out()
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		fmt.Fprintf(d.out, "== %s ==\n%s", h.Name, out)
		if results[i].Code != 0 {
			fmt.Fprintf(d.out, "(exit %d)\n", results[i].Code)
			code = 1
		}
	}
	return code
}
```

`installer/internal/cli/host.go`:

```go
package cli

import (
	"context"
	"fmt"

	"github.com/NovusEdge/flocc/installer/internal/ship"
)

// hostCmd is flocc host <sub>: args is the line after "host", name the -H value.
func hostCmd(ctx context.Context, d *deps, name string, args []string) int {
	if len(args) > 0 && args[0] == "run" {
		return hostRun(ctx, d, name, args[1:])
	}
	fmt.Fprint(d.err, usage)
	return 2
}

// hostRun is flocc host run -H <name> -- <command...>: a raw command, not the host's flocc.
func hostRun(ctx context.Context, d *deps, name string, args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(d.err, "usage: flocc host run -H <name> -- <command...>")
		return 2
	}
	h, err := pickHost(ctx, d, name, nil)
	if err != nil {
		fmt.Fprintln(d.err, "flocc:", err)
		return 2
	}
	dest, err := d.resolve(ctx, h.Target())
	if err == nil {
		err = ship.OnHost(ctx, d.run, dest, args, d.tty)
	}
	return ship.ExitCode(err, "flocc")
}
```

In `installer/internal/cli/cli.go`, extend `deps` and `Main`, and replace `run`:

```go
type deps struct {
	out, err io.Writer
	home     string
	getenv   func(string) string
	run      host.Runner
	tty      bool // stdin is a terminal, so ssh gets -t
	resolve  func(context.Context, string) (ship.Dest, error)
	fleet    func(args []string) int
}

func Main(args []string) int {
	home, _ := os.UserHomeDir()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	st, _ := os.Stdin.Stat()
	d := &deps{out: os.Stdout, err: os.Stderr, home: home, getenv: os.Getenv,
		run: host.Exec{}, tty: st != nil && st.Mode()&os.ModeCharDevice != 0, resolve: ship.Resolve,
		fleet: func(args []string) int { return ship.FleetMain(args, os.Stdout) }}
	return run(ctx, d, args)
}

func run(ctx context.Context, d *deps, args []string) int {
	name, rest, err := splitHost(args)
	if err != nil {
		fmt.Fprintln(d.err, "flocc:", err)
		return 2
	}
	if len(rest) == 0 {
		fmt.Fprint(d.err, usage)
		return 2
	}
	switch rest[0] {
	case "help", "-h", "--help":
		fmt.Fprint(d.out, usage)
		return 0
	case "ship":
		return ship.Main(rest[1:])
	case "login":
		return ship.LoginMain(rest[1:])
	case "host":
		return hostCmd(ctx, d, name, rest[1:])
	}
	if name == "all" {
		return onAll(ctx, d, rest)
	}
	return onHost(ctx, d, name, rest)
}
```

Add `"github.com/NovusEdge/flocc/installer/internal/host"` to `cli.go`'s imports. The `fleet` case is gone: `flocc status -H all` is the fleet view now.

- [ ] **Step 6: Make the host recipes wrappers**

In `justfile`, delete the `runlog` variable and its comment, and replace the bodies of these recipes (their `[group]` and comment lines stay; each recipe's first parameter is now a host name from `~/.config/flocc/hosts/`):

```just
[positional-arguments]
host name *cmd: build
    t=$1; shift; build/flocc host run -H "$t" -- "$@"

doctor name: build
    build/flocc -H {{name}} doctor

status name: build
    build/flocc -H {{name}} status

fleet-status *flags: build
    build/flocc status -H all {{flags}}

logs name: build
    build/flocc -H {{name}} logs -f

attach name session: build
    build/flocc -H {{name}} attach {{session}}

tick name: build
    build/flocc -H {{name}} tick

[positional-arguments]
release name key *flags: build
    t=$1; shift; build/flocc -H "$t" release "$@"
```

Change the comment of `host` to `# Run a raw command on the host as flocc, with the service PATH`.

- [ ] **Step 7: Run the tests to see them pass**

Run: `go test -race ./installer/... && go vet ./... && just --list >/dev/null`
Expected: `ok` for every package; `just --list` parses.

- [ ] **Step 8: Commit**

```bash
git add installer/internal/cli installer/internal/ship justfile
git commit -s -m "Run host commands from the desktop with -H, -H all and the issue's host"
```

---

### Task 5: Secret store

**Depends on:** nothing in this plan.

Keys live in the OS keyring when one answers, otherwise in `~/.config/flocc/secrets.toml` with mode 600. An existing `secrets.toml` always wins, so keys never end up split between two stores when a keyring appears later. The keyring library blocks forever while a locked keyring waits for an unlock prompt nobody can see (a desktop reached over SSH), so every keyring call runs under a time limit.

Dependency choice (researched 2026-10-20):
- Chosen: `github.com/zalando/go-keyring` v0.2.8. Pure Go on Linux (Secret Service over D-Bus, no cgo, so `CGO_ENABLED=0` builds keep working); on macOS it drives `/usr/bin/security -i` and writes the secret to its stdin, never to argv; it ships an in-memory mock (`keyring.MockInit`) for tests.
- Rejected: `github.com/99designs/keyring` (and its `byteness` fork): its macOS Keychain backend needs cgo, and its extra file/pass/KWallet backends bring their own passphrase prompts we would have to suppress.

**Files:**
- Create: `installer/internal/keys/store.go`
- Create: `installer/internal/keys/store_test.go`
- Modify: `installer/internal/steps/secrets.go`, `installer/internal/steps/service_test.go` (export `CleanSecret`)
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: `steps.cleanSecret` (renamed here to `steps.CleanSecret(v string) (string, error)`).
- Produces: `keys.Slot` with constants `keys.Linear` (`"linear_key"`), `keys.Agent` (`"agent_token"`), `keys.Publish` (`"publish_token"`), `keys.Ntfy` (`"ntfy_topic"`); `keys.Slots []Slot` in fill order; `(Slot).Title() string`; `keys.Store` interface `{ Get(Slot) (string, error); Set(Slot, string) error; Where() string }` where `Get` returns `""` and no error for a key never set; `keys.Open(home string, notify io.Writer) Store`; `keys.CheckPrefix(s Slot, v string) error`.

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/zalando/go-keyring@v0.2.8`
Expected: `go.mod` gains `github.com/zalando/go-keyring v0.2.8` (and `godbus/dbus/v5`, `danieljoos/wincred` as indirect).

- [ ] **Step 2: Export the paste cleaner**

In `installer/internal/steps/secrets.go` rename `cleanSecret` to `CleanSecret` (definition and its two callers in the file); in `installer/internal/steps/service_test.go` rename the two calls. The doc comment stays: `// CleanSecret strips what a paste adds and refuses what doctor would reject.`

Run: `go test ./installer/internal/steps/`
Expected: `ok`.

- [ ] **Step 3: Write the failing tests**

`installer/internal/keys/store_test.go`:

```go
package keys

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// No test may reach the real keyring of the machine running it.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

func TestFileStoreRoundTripsWithMode600(t *testing.T) {
	home := t.TempDir()
	s := fileStore{path: filepath.Join(home, ".config/flocc/secrets.toml")}
	if v, err := s.Get(Agent); err != nil || v != "" {
		t.Fatalf("%q %v", v, err)
	}
	if err := s.Set(Agent, "github_pat_a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(Linear, "lin_api_b"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get(Agent); v != "github_pat_a" {
		t.Fatal(v)
	}
	st, _ := os.Stat(s.path)
	dir, _ := os.Stat(filepath.Dir(s.path))
	if st.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
		t.Fatalf("%o %o", st.Mode().Perm(), dir.Mode().Perm())
	}
}

func TestOpenUsesTheKeyringWhenItAnswers(t *testing.T) {
	var notes bytes.Buffer
	s := Open(t.TempDir(), &notes)
	if s.Where() != "the OS keyring" || notes.Len() != 0 {
		t.Fatalf("%s %q", s.Where(), notes.String())
	}
	if v, err := s.Get(Publish); err != nil || v != "" {
		t.Fatalf("%q %v", v, err)
	}
	if err := s.Set(Publish, "github_pat_p"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get(Publish); v != "github_pat_p" {
		t.Fatal(v)
	}
}

func TestOpenKeepsAnExistingSecretsFile(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config/flocc/secrets.toml")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("ntfy_topic = 'flocc-x'\n"), 0o600)
	s := Open(home, &bytes.Buffer{})
	if s.Where() != path {
		t.Fatal(s.Where())
	}
	if v, _ := s.Get(Ntfy); v != "flocc-x" {
		t.Fatal(v)
	}
}

func TestOpenFallsBackWhenTheKeyringHangs(t *testing.T) {
	get, wait, limit := keyringGet, probeWait, probeLimit
	t.Cleanup(func() { keyringGet, probeWait, probeLimit = get, wait, limit })
	keyringGet = func(string, string) (string, error) { select {} }
	probeWait, probeLimit = 5*time.Millisecond, 50*time.Millisecond
	home := t.TempDir()
	var notes bytes.Buffer
	start := time.Now()
	s := Open(home, &notes)
	if time.Since(start) > time.Second {
		t.Fatal("Open waited on a hung keyring")
	}
	want := filepath.Join(home, ".config/flocc/secrets.toml")
	if s.Where() != want || !strings.Contains(notes.String(), want) || !strings.Contains(notes.String(), "unlock it if it asks") {
		t.Fatalf("%s %q", s.Where(), notes.String())
	}
}

func TestCheckPrefixRefusesAKeyInTheWrongSlot(t *testing.T) {
	for _, c := range []struct {
		slot  Slot
		value string
		want  string
	}{
		{Agent, "lin_api_x", "Linear API key"},
		{Linear, "github_pat_x", "GitHub token"},
		{Publish, "ghp_x", "classic"},
		{Linear, "abc", "starts with lin_api_"},
		{Agent, "abc", "starts with github_pat_"},
	} {
		if err := CheckPrefix(c.slot, c.value); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %q: %v", c.slot, c.value, err)
		}
	}
	for slot, ok := range map[Slot]string{Linear: "lin_api_x", Agent: "github_pat_x", Publish: "github_pat_y"} {
		if err := CheckPrefix(slot, ok); err != nil {
			t.Errorf("%s: %v", slot, err)
		}
	}
}
```

- [ ] **Step 4: Run them to see them fail**

Run: `go test ./installer/internal/keys/`
Expected: FAIL to compile: `undefined: fileStore`, `undefined: Open`, `undefined: CheckPrefix`.

- [ ] **Step 5: Implement**

`installer/internal/keys/store.go`:

```go
// Package keys holds the owner's keys on the desktop and the flow that makes and checks them.
package keys

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/zalando/go-keyring"
)

type Slot string

const (
	Linear  Slot = "linear_key"
	Agent   Slot = "agent_token"
	Publish Slot = "publish_token"
	Ntfy    Slot = "ntfy_topic"
)

// Slots is the fill order: the publish token's page opens only after the agent token passes.
var Slots = []Slot{Linear, Agent, Publish, Ntfy}

func (s Slot) Title() string {
	switch s {
	case Linear:
		return "Linear API key"
	case Agent:
		return "GitHub agent token (read-only)"
	case Publish:
		return "GitHub publish token"
	}
	return "ntfy topic"
}

type Store interface {
	Get(Slot) (string, error)
	Set(Slot, string) error
	Where() string
}

const service = "flocc"

var (
	// keyringGet is a var so a test can make the keyring hang.
	keyringGet = keyring.Get
	probeWait  = 2 * time.Second
	probeLimit = 30 * time.Second
	opLimit    = time.Minute

	errKeyringTimeout = errors.New("the OS keyring did not answer; unlock it and run again")
)

// Open picks the store. An existing secrets.toml wins, so keys never split between two stores;
// otherwise the OS keyring when it answers; otherwise a new secrets.toml.
func Open(home string, notify io.Writer) Store {
	path := filepath.Join(home, ".config", "flocc", "secrets.toml")
	if _, err := os.Stat(path); err == nil {
		return fileStore{path: path}
	}
	err := probe(notify)
	if err == nil {
		return ringStore{}
	}
	fmt.Fprintf(notify, "No OS keyring answered (%v); keys go in %s, mode 600.\n", err, path)
	return fileStore{path: path}
}

// probe reads a name nobody stores. A locked Secret Service shows an unlock prompt and blocks
// until it is answered, so the owner is told after probeWait and the probe gives up at probeLimit.
func probe(notify io.Writer) error {
	done := make(chan error, 1)
	go func() {
		_, err := keyringGet(service, "probe")
		if errors.Is(err, keyring.ErrNotFound) {
			err = nil
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(probeWait):
		fmt.Fprintln(notify, "Waiting for the OS keyring; unlock it if it asks.")
	}
	select {
	case err := <-done:
		return err
	case <-time.After(probeLimit - probeWait):
		return errKeyringTimeout
	}
}

type ringStore struct{}

func (ringStore) Where() string { return "the OS keyring" }

func (ringStore) Get(s Slot) (string, error) {
	v, err := limited(func() (string, error) { return keyringGet(service, string(s)) })
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return v, err
}

func (ringStore) Set(s Slot, v string) error {
	_, err := limited(func() (string, error) { return "", keyring.Set(service, string(s), v) })
	return err
}

func limited(f func() (string, error)) (string, error) {
	type result struct {
		v   string
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := f()
		done <- result{v, err}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-time.After(opLimit):
		return "", errKeyringTimeout
	}
}

type fileStore struct{ path string }

func (f fileStore) Where() string { return f.path }

func (f fileStore) load() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, toml.Unmarshal(b, &m)
}

func (f fileStore) Get(s Slot) (string, error) {
	m, err := f.load()
	return m[string(s)], err
}

func (f fileStore) Set(s Slot, v string) error {
	m, err := f.load()
	if err != nil {
		return err
	}
	m[string(s)] = v
	b, err := toml.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	// Written beside it and renamed, so a failed write never leaves half the keys.
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".secrets-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path)
}

// CheckPrefix refuses a paste that is plainly the wrong kind of key, before any network call.
// The two GitHub tokens share a prefix; the live check tells them apart.
func CheckPrefix(s Slot, v string) error {
	switch {
	case strings.HasPrefix(v, "lin_api_") && s != Linear:
		return fmt.Errorf("that is a Linear API key; this is the %s", s.Title())
	case strings.HasPrefix(v, "github_pat_") && s == Linear:
		return errors.New("that is a GitHub token; this is the Linear API key")
	case strings.HasPrefix(v, "ghp_") || strings.HasPrefix(v, "gho_"):
		return errors.New("that is a classic GitHub token; make a fine-grained one")
	case s == Linear && !strings.HasPrefix(v, "lin_api_"):
		return errors.New("a Linear personal API key starts with lin_api_")
	case (s == Agent || s == Publish) && !strings.HasPrefix(v, "github_pat_"):
		return errors.New("a fine-grained GitHub token starts with github_pat_")
	}
	return nil
}
```

`os.CreateTemp` creates the file with mode 600, so the rename leaves `secrets.toml` at 600.

- [ ] **Step 6: Run the tests to see them pass**

Run: `go test ./installer/internal/keys/ ./installer/internal/steps/ && go vet ./installer/... && CGO_ENABLED=0 go build ./installer/...`
Expected: `ok` for both packages; the cgo-free build succeeds.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum installer/internal/keys installer/internal/steps/secrets.go installer/internal/steps/service_test.go
git commit -s -m "Desktop secret store: OS keyring with a time limit, else secrets.toml"
```

---

### Task 6: GitHub client and token validators

**Depends on:** nothing in this plan.

The desktop checks a GitHub token the moment it is pasted. Every check reads, or sends one of two requests GitHub must refuse: a ref at the all-zero SHA (the Go port of `tokens.can_write_contents`) and a pull request from a head branch that cannot exist. The tests replay responses in the raw form `curl --http1.1 -si` prints, so a recording from the real API drops in unchanged; this task ships hand-made recordings in that form and a manual step re-records them.

**Files:**
- Create: `installer/internal/replay/replay.go`
- Create: `installer/internal/githubapi/githubapi.go`
- Create: `installer/internal/githubapi/check.go`
- Create: `installer/internal/githubapi/githubapi_test.go`
- Create: `installer/internal/githubapi/testdata/*.http` (listed in Step 1)
- Create: `.gitattributes`

**Interfaces:**
- Consumes: `steps.ParseRepos(b []byte) (repos []answers.Repo, maybeEmpty, dropped []string, err error)` (existing).
- Produces:
  - `replay.Route{Method, Path, Body, Auth, File string}`, `replay.New(t *testing.T, dir string, routes ...replay.Route) *replay.Server`, `(*replay.Server).Requests []replay.Request` with `replay.Request{Method, Path, Auth, Body string}`.
  - `githubapi.DefaultBase = "https://api.github.com"`, `githubapi.Client{Base, Token string; HTTP *http.Client}`, `githubapi.New(token string) *Client`, `githubapi.ErrBadCredentials`, `githubapi.ErrNotVisible`.
  - `(*Client).Expiry(ctx) (time.Time, error)` (zero time: never expires), `(*Client).Repo(ctx, slug string) (githubapi.Repo, error)` with `Repo{DefaultBranch string; Private bool}`, `(*Client).CanWriteContents(ctx, slug string) (bool, error)`, `(*Client).CanOpenPulls(ctx, slug, base string) (bool, error)`, `(*Client).ListRepos(ctx) ([]byte, error)`.
  - `githubapi.Target{Slug, Base string}`, `githubapi.TokenCheck{Expires time.Time; Problems []string}`, `githubapi.CheckAgent(ctx, c *Client, repos []Target) (TokenCheck, error)`, `githubapi.CheckPublish(ctx, c *Client, repos []Target) (TokenCheck, error)`. The error means the token itself was refused (or GitHub could not be reached); `Problems` lists repos the token cannot serve, one line each, with the fix.

- [ ] **Step 1: Add the recordings**

`.gitattributes` (recordings carry CRLF header lines when captured by curl; git must not rewrite them):

```
*.http -text
```

Create these files under `installer/internal/githubapi/testdata/`. Each is a status line, headers, a blank line and the body, with no `Content-Length` (the replay server reads the body to the end of the file).

`user-ok.http`:

```
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
GitHub-Authentication-Token-Expiration: 2027-10-21 08:00:00 UTC
X-GitHub-Media-Type: github.v3; format=json

{"login":"NovusEdge","id":1,"type":"User"}
```

`user-never.http`:

```
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8

{"login":"NovusEdge","id":1,"type":"User"}
```

`bad-credentials.http`:

```
HTTP/1.1 401 Unauthorized
Content-Type: application/json; charset=utf-8

{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest","status":"401"}
```

`repo-ok.http`:

```
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8

{"id":2,"name":"a","full_name":"o/a","private":true,"default_branch":"main","owner":{"login":"o"}}
```

`not-found.http`:

```
HTTP/1.1 404 Not Found
Content-Type: application/json; charset=utf-8

{"message":"Not Found","documentation_url":"https://docs.github.com/rest/repos/repos#get-a-repository","status":"404"}
```

`refs-forbidden.http`:

```
HTTP/1.1 403 Forbidden
Content-Type: application/json; charset=utf-8

{"message":"Resource not accessible by personal access token","documentation_url":"https://docs.github.com/rest/git/refs#create-a-reference","status":"403"}
```

`refs-unprocessable.http`:

```
HTTP/1.1 422 Unprocessable Entity
Content-Type: application/json; charset=utf-8

{"message":"Object does not exist","documentation_url":"https://docs.github.com/rest/git/refs#create-a-reference","status":"422"}
```

`pulls-unprocessable.http`:

```
HTTP/1.1 422 Unprocessable Entity
Content-Type: application/json; charset=utf-8

{"message":"Validation Failed","errors":[{"resource":"PullRequest","field":"head","code":"invalid"}],"documentation_url":"https://docs.github.com/rest/pulls/pulls#create-a-pull-request","status":"422"}
```

`pulls-forbidden.http`:

```
HTTP/1.1 403 Forbidden
Content-Type: application/json; charset=utf-8

{"message":"Resource not accessible by personal access token","documentation_url":"https://docs.github.com/rest/pulls/pulls#create-a-pull-request","status":"403"}
```

`repos-page1.http`:

```
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
Link: <https://api.github.com/user/repos?per_page=100&affiliation=owner,collaborator,organization_member&page=2>; rel="next", <https://api.github.com/user/repos?per_page=100&affiliation=owner,collaborator,organization_member&page=2>; rel="last"

[{"name":"a","html_url":"https://github.com/o/a","default_branch":"main","private":true,"size":10,"owner":{"login":"o"}}]
```

`repos-page2.http`:

```
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
Link: <https://api.github.com/user/repos?per_page=100&affiliation=owner,collaborator,organization_member&page=1>; rel="prev", <https://api.github.com/user/repos?per_page=100&affiliation=owner,collaborator,organization_member&page=1>; rel="first"

[{"name":"b","html_url":"https://github.com/o/b","default_branch":"trunk","private":false,"size":3,"owner":{"login":"o"}}]
```

- [ ] **Step 2: Write the replay server**

`installer/internal/replay/replay.go`:

```go
// Package replay serves recorded HTTP responses to tests. A recording is the raw output of
// `curl --http1.1 -si`, so one captured from the real API drops in unchanged.
package replay

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Route answers requests with this method and request URI (query included) with File. When
// Body is set the request body must also contain it: GraphQL sends every query to one path. When
// Auth is set the Authorization header must equal it, so two tokens can get different answers.
type Route struct {
	Method, Path, Body, Auth, File string
}

type Request struct {
	Method, Path, Auth, Body string
}

type Server struct {
	*httptest.Server
	mu       sync.Mutex
	Requests []Request
}

// New fails the test on any request no route matches; the first matching route wins.
func New(t *testing.T, dir string, routes ...Route) *Server {
	t.Helper()
	s := &Server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req := Request{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), string(body)}
		s.mu.Lock()
		s.Requests = append(s.Requests, req)
		s.mu.Unlock()
		for _, rt := range routes {
			if rt.Method != req.Method || rt.Path != req.Path || !strings.Contains(req.Body, rt.Body) ||
				rt.Auth != "" && rt.Auth != req.Auth {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, rt.File))
			if err != nil {
				t.Errorf("replay: %v", err)
				w.WriteHeader(599)
				return
			}
			resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), r)
			if err != nil {
				t.Errorf("replay: %s: %v", rt.File, err)
				w.WriteHeader(599)
				return
			}
			for k, vs := range resp.Header {
				if k == "Content-Length" || k == "Transfer-Encoding" {
					continue
				}
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
			return
		}
		t.Errorf("replay: no recording for %s %s %s", req.Method, req.Path, req.Body)
		w.WriteHeader(599)
	}))
	t.Cleanup(s.Close)
	return s
}
```

- [ ] **Step 3: Write the failing tests**

`installer/internal/githubapi/githubapi_test.go`:

```go
package githubapi

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/replay"
	"github.com/NovusEdge/flocc/installer/internal/steps"
)

func get(path, file string) replay.Route  { return replay.Route{Method: "GET", Path: path, File: file} }
func post(path, file string) replay.Route { return replay.Route{Method: "POST", Path: path, File: file} }

func serve(t *testing.T, routes ...replay.Route) (*replay.Server, *Client) {
	s := replay.New(t, "testdata", routes...)
	return s, &Client{Base: s.URL, Token: "github_pat_test", HTTP: s.Client()}
}

var repoA = []Target{{Slug: "o/a", Base: "main"}}

func TestExpiryReadsTheTokenHeader(t *testing.T) {
	_, c := serve(t, get("/user", "user-ok.http"))
	got, err := c.Expiry(context.Background())
	if want := time.Date(2027, 10, 21, 8, 0, 0, 0, time.UTC); err != nil || !got.Equal(want) {
		t.Fatalf("%v %v", got, err)
	}
}

func TestATokenWithoutExpiryHasTheZeroTime(t *testing.T) {
	_, c := serve(t, get("/user", "user-never.http"))
	if got, err := c.Expiry(context.Background()); err != nil || !got.IsZero() {
		t.Fatalf("%v %v", got, err)
	}
}

func TestAnExpiredTokenIsRefusedOutright(t *testing.T) {
	_, c := serve(t, get("/user", "bad-credentials.http"))
	if _, err := CheckAgent(context.Background(), c, repoA); !errors.Is(err, ErrBadCredentials) {
		t.Fatal(err)
	}
}

func TestCheckAgentAcceptsAReadOnlyToken(t *testing.T) {
	s, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/a", "repo-ok.http"),
		post("/repos/o/a/git/refs", "refs-forbidden.http"))
	got, err := CheckAgent(context.Background(), c, repoA)
	if err != nil || len(got.Problems) != 0 || got.Expires.Year() != 2027 {
		t.Fatalf("%+v %v", got, err)
	}
	if s.Requests[0].Auth != "Bearer github_pat_test" {
		t.Fatalf("%+v", s.Requests[0])
	}
}

func TestCheckAgentRefusesATokenThatCanPush(t *testing.T) {
	_, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/a", "repo-ok.http"),
		post("/repos/o/a/git/refs", "refs-unprocessable.http"))
	got, err := CheckAgent(context.Background(), c, repoA)
	if err != nil || len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "o/a: the token can push") {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCheckNamesARepoTheTokenCannotRead(t *testing.T) {
	_, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/b", "not-found.http"))
	got, err := CheckAgent(context.Background(), c, []Target{{Slug: "o/b", Base: "main"}})
	if err != nil || len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "o/b: the token cannot read it") {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCheckPublishAcceptsPushAndPullRequests(t *testing.T) {
	_, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/a", "repo-ok.http"),
		post("/repos/o/a/git/refs", "refs-unprocessable.http"), post("/repos/o/a/pulls", "pulls-unprocessable.http"))
	if got, err := CheckPublish(context.Background(), c, repoA); err != nil || len(got.Problems) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCheckPublishRefusesATokenThatCannotPush(t *testing.T) {
	_, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/a", "repo-ok.http"),
		post("/repos/o/a/git/refs", "refs-forbidden.http"), post("/repos/o/a/pulls", "pulls-unprocessable.http"))
	got, err := CheckPublish(context.Background(), c, repoA)
	if err != nil || len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "cannot push") {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCheckPublishRefusesATokenThatCannotOpenPullRequests(t *testing.T) {
	_, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/a", "repo-ok.http"),
		post("/repos/o/a/git/refs", "refs-unprocessable.http"), post("/repos/o/a/pulls", "pulls-forbidden.http"))
	got, err := CheckPublish(context.Background(), c, repoA)
	if err != nil || len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "cannot open pull requests") {
		t.Fatalf("%+v %v", got, err)
	}
}

// The probes are the only writes a check sends, and GitHub must refuse both: no ref can point at
// the zero SHA, and no pull request can come from a branch that does not exist.
func TestChecksSendNothingButReadsAndTheTwoProbes(t *testing.T) {
	s, c := serve(t, get("/user", "user-ok.http"), get("/repos/o/a", "repo-ok.http"),
		post("/repos/o/a/git/refs", "refs-unprocessable.http"), post("/repos/o/a/pulls", "pulls-unprocessable.http"))
	if _, err := CheckPublish(context.Background(), c, repoA); err != nil {
		t.Fatal(err)
	}
	for _, r := range s.Requests {
		switch {
		case r.Method == "GET":
		case strings.HasSuffix(r.Path, "/git/refs"):
			var body struct{ SHA string }
			json.Unmarshal([]byte(r.Body), &body)
			if body.SHA != strings.Repeat("0", 40) {
				t.Fatalf("ref probe with a real SHA: %s", r.Body)
			}
		case strings.HasSuffix(r.Path, "/pulls"):
			var body struct{ Head string }
			json.Unmarshal([]byte(r.Body), &body)
			if body.Head != probeHead {
				t.Fatalf("pull probe from a real branch: %s", r.Body)
			}
		default:
			t.Fatalf("unexpected write %s %s", r.Method, r.Path)
		}
	}
}

func TestListReposFollowsTheNextPageOnTheSameServer(t *testing.T) {
	q := "/user/repos?per_page=100&affiliation=owner,collaborator,organization_member"
	_, c := serve(t, get(q, "repos-page1.http"), get(q+"&page=2", "repos-page2.http"))
	raw, err := c.ListRepos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	repos, _, _, err := steps.ParseRepos(raw)
	names := []string{}
	for _, r := range repos {
		names = append(names, r.Name)
	}
	if err != nil || !slices.Equal(names, []string{"a", "b"}) || repos[1].BaseBranch != "trunk" {
		t.Fatalf("%+v %v", repos, err)
	}
}
```

- [ ] **Step 4: Run them to see them fail**

Run: `go test ./installer/internal/githubapi/`
Expected: FAIL to compile: `undefined: Client`, `undefined: CheckAgent`, `undefined: probeHead`.

- [ ] **Step 5: Write the client**

`installer/internal/githubapi/githubapi.go`:

```go
// Package githubapi is the part of GitHub's REST API the desktop uses: token checks that change
// nothing, and repo discovery.
package githubapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const DefaultBase = "https://api.github.com"

var (
	ErrBadCredentials = errors.New("GitHub rejected the token: it expired, was revoked or is mistyped")
	ErrNotVisible     = errors.New("not visible to this token")
)

type Client struct {
	Base, Token string
	HTTP        *http.Client
}

func New(token string) *Client {
	return &Client{Base: DefaultBase, Token: token, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

type Repo struct {
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, []byte, error) {
	var in io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		in = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, in)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return resp, b, ErrBadCredentials
	}
	return resp, b, nil
}

func apiError(resp *http.Response, body []byte) error {
	var e struct{ Message string }
	json.Unmarshal(body, &e)
	return fmt.Errorf("GitHub %s %s: %d %s", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, e.Message)
}

// Expiry is when the token expires; the zero time for a token that never does.
func (c *Client) Expiry(ctx context.Context) (time.Time, error) {
	resp, b, err := c.do(ctx, "GET", "/user", nil)
	if err != nil {
		return time.Time{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, apiError(resp, b)
	}
	h := resp.Header.Get("GitHub-Authentication-Token-Expiration")
	if h == "" {
		return time.Time{}, nil
	}
	return time.Parse("2006-01-02 15:04:05 MST", h)
}

func (c *Client) Repo(ctx context.Context, slug string) (Repo, error) {
	resp, b, err := c.do(ctx, "GET", "/repos/"+slug, nil)
	if err != nil {
		return Repo{}, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		var r Repo
		return r, json.Unmarshal(b, &r)
	case http.StatusNotFound:
		return Repo{}, fmt.Errorf("%s: %w", slug, ErrNotVisible)
	}
	return Repo{}, apiError(resp, b)
}

// CanWriteContents reports whether the token may push to slug, asked without changing
// anything. A ref at the all-zero SHA can never be created, and GitHub checks permission
// before the object, so 422 means the write itself was allowed. A refusal is 403, or 404 when
// the token can read the repo but not write it, the same answer as for a repo it cannot see.
func (c *Client) CanWriteContents(ctx context.Context, slug string) (bool, error) {
	resp, b, err := c.do(ctx, "POST", "/repos/"+slug+"/git/refs",
		map[string]string{"ref": "refs/heads/flocc-doctor-probe", "sha": strings.Repeat("0", 40)})
	if err != nil {
		return false, err
	}
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return false, nil
	case resp.StatusCode == http.StatusUnprocessableEntity:
		return true, nil
	case resp.StatusCode == http.StatusNotFound:
		_, err := c.Repo(ctx, slug)
		return false, err
	case resp.StatusCode < 300:
		return false, fmt.Errorf("GitHub accepted a ref at the zero SHA in %s", slug)
	}
	return false, apiError(resp, b)
}

// probeHead is a branch no repo has, so the pull request probe can never open one.
const probeHead = "flocc-probe-no-such-branch"

// CanOpenPulls asks for a pull request from probeHead. GitHub checks the caller's permission
// before it validates the head, so 422 means opening one is allowed and 403 means it is not.
func (c *Client) CanOpenPulls(ctx context.Context, slug, base string) (bool, error) {
	resp, b, err := c.do(ctx, "POST", "/repos/"+slug+"/pulls",
		map[string]string{"title": "flocc permission probe", "head": probeHead, "base": base})
	if err != nil {
		return false, err
	}
	switch {
	case resp.StatusCode == http.StatusUnprocessableEntity:
		return true, nil
	case resp.StatusCode == http.StatusForbidden:
		return false, nil
	case resp.StatusCode == http.StatusNotFound:
		_, err := c.Repo(ctx, slug)
		return false, err
	case resp.StatusCode < 300:
		return false, fmt.Errorf("GitHub opened a pull request in %s from %s; close it", slug, probeHead)
	}
	return false, apiError(resp, b)
}

var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// ListRepos returns every page of /user/repos concatenated, the form steps.ParseRepos reads.
// Only the path and query of each next link are kept, so every page comes from c.Base.
func (c *Client) ListRepos(ctx context.Context) ([]byte, error) {
	path := "/user/repos?per_page=100&affiliation=owner,collaborator,organization_member"
	var all []byte
	for path != "" {
		resp, b, err := c.do(ctx, "GET", path, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, apiError(resp, b)
		}
		all = append(all, b...)
		path = ""
		if m := nextLink.FindStringSubmatch(resp.Header.Get("Link")); m != nil {
			if u, err := url.Parse(m[1]); err == nil {
				path = u.RequestURI()
			}
		}
	}
	return all, nil
}
```

- [ ] **Step 6: Write the checks**

`installer/internal/githubapi/check.go`:

```go
package githubapi

import (
	"context"
	"errors"
	"time"
)

type Target struct{ Slug, Base string }

type TokenCheck struct {
	Expires  time.Time // zero: never expires
	Problems []string
}

// CheckAgent passes a token that reads every repo and can push to none: agents run gh with it.
func CheckAgent(ctx context.Context, c *Client, repos []Target) (TokenCheck, error) {
	return check(ctx, c, repos, func(t Target) (string, error) {
		writes, err := c.CanWriteContents(ctx, t.Slug)
		if err != nil || !writes {
			return "", err
		}
		return t.Slug + ": the token can push; set Contents to Read-only", nil
	})
}

// CheckPublish passes a token that can push to and open pull requests in every repo.
func CheckPublish(ctx context.Context, c *Client, repos []Target) (TokenCheck, error) {
	return check(ctx, c, repos, func(t Target) (string, error) {
		writes, err := c.CanWriteContents(ctx, t.Slug)
		if err != nil {
			return "", err
		}
		if !writes {
			return t.Slug + ": the token cannot push; set Contents to Read and write", nil
		}
		pulls, err := c.CanOpenPulls(ctx, t.Slug, t.Base)
		if err != nil || pulls {
			return "", err
		}
		return t.Slug + ": the token cannot open pull requests; set Pull requests to Read and write", nil
	})
}

func check(ctx context.Context, c *Client, repos []Target, probe func(Target) (string, error)) (TokenCheck, error) {
	var out TokenCheck
	exp, err := c.Expiry(ctx)
	if err != nil {
		return out, err
	}
	out.Expires = exp
	for _, t := range repos {
		if _, err := c.Repo(ctx, t.Slug); errors.Is(err, ErrNotVisible) {
			out.Problems = append(out.Problems, t.Slug+": the token cannot read it; add it under Repository access")
			continue
		} else if err != nil {
			return out, err
		}
		problem, err := probe(t)
		if err != nil {
			return out, err
		}
		if problem != "" {
			out.Problems = append(out.Problems, problem)
		}
	}
	return out, nil
}
```

- [ ] **Step 7: Run the tests to see them pass**

Run: `go test ./installer/internal/githubapi/ ./installer/internal/replay/ && go vet ./installer/...`
Expected: `ok` for `githubapi`; `replay` has no test files.

- [ ] **Step 8: Commit**

```bash
git add .gitattributes installer/internal/replay installer/internal/githubapi
git commit -s -m "GitHub client with side-effect-free token checks"
```

- [ ] **Step 9 (manual, controller only): re-record against the real API**

This confirms the spec's open point that GitHub checks permission before it validates `head` on `POST /pulls`. Run in an interactive shell on the desktop with a real agent token (`A`) and publish token (`P`) for a repo the owner maps (`o/r`); `read -rs` keeps the tokens out of history and argv:

```bash
read -rs A; read -rs P
cd installer/internal/githubapi/testdata
gh_() { curl --http1.1 -si -H @<(printf 'Authorization: Bearer %s\nAccept: application/vnd.github+json\n' "$1") "${@:2}"; }
gh_ "$A" -X POST -d '{"ref":"refs/heads/flocc-doctor-probe","sha":"0000000000000000000000000000000000000000"}' https://api.github.com/repos/o/r/git/refs > refs-forbidden.http
gh_ "$P" -X POST -d '{"ref":"refs/heads/flocc-doctor-probe","sha":"0000000000000000000000000000000000000000"}' https://api.github.com/repos/o/r/git/refs > refs-unprocessable.http
gh_ "$P" -X POST -d '{"title":"flocc permission probe","head":"flocc-probe-no-such-branch","base":"main"}' https://api.github.com/repos/o/r/pulls > pulls-unprocessable.http
gh_ "$A" -X POST -d '{"title":"flocc permission probe","head":"flocc-probe-no-such-branch","base":"main"}' https://api.github.com/repos/o/r/pulls > pulls-forbidden.http
gh_ "x" https://api.github.com/user > bad-credentials.http
unset A P
```

`user-ok.http` and `user-never.http` stay hand-made: the tests assert the expiry date written in them. For the five files just written, `head -1` must show 403, 422, 422, 403 and 401 in that order (a 404 for `refs-forbidden.http` is also a refusal and the check handles it, but then copy the file to a new name and add a test for it). If `pulls-forbidden.http` is 422 instead of 403, GitHub validates the head first, the pull request check cannot tell the tokens apart, and the owner decides before merge whether to drop it. In every file written, delete the `Content-Length` and `Transfer-Encoding` lines: curl prints the body already decoded, which `http.ReadResponse` cannot read under a `chunked` header, and an edited body no longer matches its length. The recordings become public with the repo, so replace the private owner and repo names in them (`o/r` and its owner) with `o` and `a`, which the tests use. Run `go test ./installer/internal/githubapi/` again, then commit the recordings with `git commit -s -m "Re-record GitHub responses"`.

---

### Task 7: Linear client and key validator

**Depends on:** Task 6 (the replay server).

The Linear key is checked by reading the viewer and its teams, then by one write that changes nothing: each mapped digest issue's title is set to the title it already has. A key made with read-only permissions passes every read, so without the write it would pass here and fail on the service's first claim on every host. `host add` (Task 9) also needs the team's open digest issue, issue creation and label creation, so the client carries those calls too; the check and the issue lookup are exercised against recordings here, and Task 9 drives the rest against a fake.

**Files:**
- Create: `installer/internal/linearapi/linearapi.go`
- Create: `installer/internal/linearapi/linearapi_test.go`
- Create: `installer/internal/linearapi/testdata/viewer-ok.http`, `viewer-other-team.http`, `bad-key.http`, `issue-ok.http`, `issue-not-found.http`, `update-ok.http`, `update-forbidden.http`

**Interfaces:**
- Consumes: `replay.New`, `replay.Route` (Task 6).
- Produces: `linearapi.DefaultURL = "https://api.linear.app/graphql"`, `linearapi.Client{URL, Key string; HTTP *http.Client}`, `linearapi.New(key string) *Client`, `linearapi.ErrNotFound`, `linearapi.Team{ID, Key, Name string}`, `linearapi.Viewer{Name, Email string; Teams []Team}`, `linearapi.Issue{ID, Key, Title, StateType string}` with `(Issue).Open() bool`, and methods `Viewer(ctx) (Viewer, error)`, `Issue(ctx, key string) (Issue, error)` (an issue Linear does not have wraps `ErrNotFound`), `SetTitle(ctx, id, title string) error`, `FindOpenIssue(ctx, teamKey, title string) (Issue, bool, error)`, `CreateIssue(ctx, teamID, title, description string) (Issue, error)`, `MissingLabels(ctx, teamKey string, names []string) ([]string, error)`, `CreateLabel(ctx, teamID, name string) error`; `linearapi.CheckKey(ctx, c *Client, teams []string) (Viewer, error)`; `linearapi.CanWrite(ctx, c *Client, issueKey string) error`.

- [ ] **Step 1: Add the recordings**

`installer/internal/linearapi/testdata/viewer-ok.http`:

```
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8

{"data":{"viewer":{"name":"Ada Lovelace","email":"ada@example.test"},"teams":{"nodes":[{"id":"team-nf","key":"NF","name":"Nimblefox"}]}}}
```

`viewer-other-team.http`:

```
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8

{"data":{"viewer":{"name":"Ada Lovelace","email":"ada@example.test"},"teams":{"nodes":[{"id":"team-xy","key":"XY","name":"Elsewhere"}]}}}
```

`bad-key.http`:

```
HTTP/1.1 400 Bad Request
Content-Type: application/json; charset=utf-8

{"errors":[{"message":"Authentication required, not authenticated","extensions":{"type":"authentication error","code":"AUTHENTICATION_ERROR","statusCode":401,"userPresentableMessage":"You need to authenticate to access this operation."}}]}
```

`issue-ok.http`:

```
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8

{"data":{"issue":{"id":"issue-186","identifier":"NF-186","title":"flocc digest","state":{"type":"started"}}}}
```

`issue-not-found.http`:

```
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8

{"data":null,"errors":[{"message":"Entity not found: Issue","extensions":{"type":"invalid input","code":"INPUT_ERROR","statusCode":400,"userPresentableMessage":"Could not find referenced Issue."}}]}
```

`update-ok.http`:

```
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8

{"data":{"issueUpdate":{"success":true}}}
```

`update-forbidden.http` (hand-made; Step 7 replaces it with Linear's own answer to a read-only key):

```
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8

{"data":null,"errors":[{"message":"Invalid scope: `write` required","extensions":{"type":"forbidden","code":"FORBIDDEN","statusCode":403,"userPresentableMessage":"You do not have permission to perform this action."}}]}
```

- [ ] **Step 2: Write the failing tests**

`installer/internal/linearapi/linearapi_test.go`:

```go
package linearapi

import (
	"context"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/replay"
)

func serve(t *testing.T, file string) (*replay.Server, *Client) {
	s := replay.New(t, "testdata", replay.Route{Method: "POST", Path: "/graphql", Body: "viewer", File: file})
	return s, &Client{URL: s.URL + "/graphql", Key: "lin_api_test", HTTP: s.Client()}
}

func TestCheckKeyAcceptsAKeyThatSeesTheTeam(t *testing.T) {
	s, c := serve(t, "viewer-ok.http")
	v, err := CheckKey(context.Background(), c, []string{"NF"})
	if err != nil || v.Name != "Ada Lovelace" || len(v.Teams) != 1 || v.Teams[0].ID != "team-nf" {
		t.Fatalf("%+v %v", v, err)
	}
	// Personal API keys go in the header bare, without "Bearer".
	if s.Requests[0].Auth != "lin_api_test" {
		t.Fatalf("%+v", s.Requests[0])
	}
}

func TestCheckKeyWithNoHostsNeedsOnlyATeam(t *testing.T) {
	_, c := serve(t, "viewer-other-team.http")
	if _, err := CheckKey(context.Background(), c, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCheckKeyRefusesAKeyForAnotherTeam(t *testing.T) {
	_, c := serve(t, "viewer-other-team.http")
	_, err := CheckKey(context.Background(), c, []string{"NF"})
	if err == nil || !strings.Contains(err.Error(), "cannot see team NF") || !strings.Contains(err.Error(), "XY") {
		t.Fatal(err)
	}
}

func TestCheckKeyRefusesARejectedKey(t *testing.T) {
	_, c := serve(t, "bad-key.http")
	_, err := CheckKey(context.Background(), c, []string{"NF"})
	if err == nil || !strings.Contains(err.Error(), "Authentication required") {
		t.Fatal(err)
	}
}

func writeServer(t *testing.T, update string) (*replay.Server, *Client) {
	s := replay.New(t, "testdata",
		replay.Route{Method: "POST", Path: "/graphql", Body: "issue(id", File: "issue-ok.http"},
		replay.Route{Method: "POST", Path: "/graphql", Body: "issueUpdate", File: update})
	return s, &Client{URL: s.URL + "/graphql", Key: "lin_api_test", HTTP: s.Client()}
}

// The probe writes back the title it read, so the issue is left as it was.
func TestCanWriteSetsTheTitleItRead(t *testing.T) {
	s, c := writeServer(t, "update-ok.http")
	if err := CanWrite(context.Background(), c, "NF-186"); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Variables struct{ ID, Title string }
	}
	json.Unmarshal([]byte(s.Requests[1].Body), &body)
	if body.Variables.ID != "issue-186" || body.Variables.Title != "flocc digest" {
		t.Fatalf("%s", s.Requests[1].Body)
	}
}

func TestCanWriteRefusesAReadOnlyKey(t *testing.T) {
	_, c := writeServer(t, "update-forbidden.http")
	err := CanWrite(context.Background(), c, "NF-186")
	if err == nil || !strings.Contains(err.Error(), "cannot write") || !strings.Contains(err.Error(), "Full access") {
		t.Fatal(err)
	}
}

func TestAnIssueLinearDoesNotHaveIsErrNotFound(t *testing.T) {
	s := replay.New(t, "testdata", replay.Route{Method: "POST", Path: "/graphql", Body: "issue(id", File: "issue-not-found.http"})
	c := &Client{URL: s.URL + "/graphql", Key: "lin_api_test", HTTP: s.Client()}
	if _, err := c.Issue(context.Background(), "NF-9999"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
```

Add `"encoding/json"` and `"errors"` to the test file's imports.

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./installer/internal/linearapi/`
Expected: FAIL to compile: `undefined: Client`, `undefined: CheckKey`, `undefined: CanWrite`, `undefined: ErrNotFound`.

- [ ] **Step 4: Implement**

`installer/internal/linearapi/linearapi.go`:

```go
// Package linearapi is the part of Linear's GraphQL API the desktop uses: checking a key, and
// the digest issue and labels host add makes.
package linearapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

const DefaultURL = "https://api.linear.app/graphql"

var ErrNotFound = errors.New("Linear has no such issue")

type Client struct {
	URL, Key string
	HTTP     *http.Client
}

func New(key string) *Client {
	return &Client{URL: DefaultURL, Key: key, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

type Team struct{ ID, Key, Name string }

type Viewer struct {
	Name, Email string
	Teams       []Team
}

type Issue struct{ ID, Key, Title, StateType string }

func (i Issue) Open() bool { return i.StateType != "completed" && i.StateType != "canceled" }

// do posts one query. Linear reports a bad key and rate limits as HTTP 400 with a JSON error
// list, so the body decides, not the status.
func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var env struct {
		Data   json.RawMessage
		Errors []struct{ Message string }
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("Linear HTTP %d: unreadable reply", resp.StatusCode)
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, len(env.Errors))
		for i, e := range env.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("Linear: %s", strings.Join(msgs, "; "))
	}
	return json.Unmarshal(env.Data, out)
}

type issueNode struct {
	ID, Identifier, Title string
	State                 struct{ Type string }
}

func (n issueNode) issue() Issue {
	return Issue{ID: n.ID, Key: n.Identifier, Title: n.Title, StateType: n.State.Type}
}

const issueFields = "id identifier title state { type }"

func (c *Client) Viewer(ctx context.Context) (Viewer, error) {
	var out struct {
		Viewer struct{ Name, Email string }
		Teams  struct{ Nodes []Team }
	}
	err := c.do(ctx, "query { viewer { name email } teams(first: 100) { nodes { id key name } } }", nil, &out)
	return Viewer{Name: out.Viewer.Name, Email: out.Viewer.Email, Teams: out.Teams.Nodes}, err
}

func (c *Client) Issue(ctx context.Context, key string) (Issue, error) {
	var out struct{ Issue issueNode }
	err := c.do(ctx, "query($id: String!) { issue(id: $id) { "+issueFields+" } }", map[string]any{"id": key}, &out)
	// Linear reports a missing issue only in the message text.
	if err != nil && strings.Contains(err.Error(), "Entity not found") {
		err = fmt.Errorf("%s: %w", key, ErrNotFound)
	}
	return out.Issue.issue(), err
}

func (c *Client) SetTitle(ctx context.Context, id, title string) error {
	var out struct{ IssueUpdate struct{ Success bool } }
	err := c.do(ctx, `mutation($id: String!, $title: String!) {
		issueUpdate(id: $id, input: { title: $title }) { success } }`,
		map[string]any{"id": id, "title": title}, &out)
	if err == nil && !out.IssueUpdate.Success {
		err = errors.New("Linear did not apply the update")
	}
	return err
}

// FindOpenIssue finds a team issue with exactly this title that is not completed or canceled.
func (c *Client) FindOpenIssue(ctx context.Context, teamKey, title string) (Issue, bool, error) {
	var out struct{ Issues struct{ Nodes []issueNode } }
	err := c.do(ctx, `query($team: String!, $title: String!) { issues(first: 10, filter: {
		team: { key: { eq: $team } }, title: { eq: $title },
		state: { type: { nin: ["completed", "canceled"] } } }) { nodes { `+issueFields+` } } }`,
		map[string]any{"team": teamKey, "title": title}, &out)
	if err != nil || len(out.Issues.Nodes) == 0 {
		return Issue{}, false, err
	}
	return out.Issues.Nodes[0].issue(), true, nil
}

func (c *Client) CreateIssue(ctx context.Context, teamID, title, description string) (Issue, error) {
	var out struct{ IssueCreate struct{ Issue issueNode } }
	err := c.do(ctx, `mutation($team: String!, $title: String!, $desc: String!) {
		issueCreate(input: { teamId: $team, title: $title, description: $desc }) { issue { `+issueFields+` } } }`,
		map[string]any{"team": teamID, "title": title, "desc": description}, &out)
	return out.IssueCreate.Issue.issue(), err
}

// MissingLabels is the Go form of linear.missing_labels on the host: a name counts when a
// workspace label or one of this team's has it.
func (c *Client) MissingLabels(ctx context.Context, teamKey string, names []string) ([]string, error) {
	usable := map[string]bool{}
	// Chunked so a long name list cannot push a label past the 250-node page.
	for i := 0; i < max(len(names), 1); i += 100 {
		var out struct {
			Teams       struct{ Nodes []struct{ ID string } }
			IssueLabels struct {
				Nodes []struct {
					Name string
					Team *struct{ Key string }
				}
			}
		}
		err := c.do(ctx, `query($key: String!, $names: [String!]) {
			teams(filter: { key: { eq: $key } }) { nodes { id } }
			issueLabels(first: 250, filter: { name: { in: $names } }) { nodes { name team { key } } } }`,
			map[string]any{"key": teamKey, "names": names[i:min(i+100, len(names))]}, &out)
		if err != nil {
			return nil, err
		}
		if len(out.Teams.Nodes) == 0 {
			return nil, fmt.Errorf("team %s does not exist or the key cannot see it", teamKey)
		}
		for _, n := range out.IssueLabels.Nodes {
			if n.Team == nil || n.Team.Key == teamKey {
				usable[n.Name] = true
			}
		}
	}
	var missing []string
	for _, n := range names {
		if !usable[n] {
			missing = append(missing, n)
		}
	}
	return missing, nil
}

func (c *Client) CreateLabel(ctx context.Context, teamID, name string) error {
	var out struct{ IssueLabelCreate struct{ Success bool } }
	err := c.do(ctx, `mutation($team: String!, $name: String!) {
		issueLabelCreate(input: { teamId: $team, name: $name }) { success } }`,
		map[string]any{"team": teamID, "name": name}, &out)
	if err == nil && !out.IssueLabelCreate.Success {
		err = fmt.Errorf("Linear did not create label %s", name)
	}
	return err
}

// CanWrite proves the key may write by setting issueKey's title to the title it already has; only
// updatedAt moves. A key made with read-only permissions passes every read, then fails on the
// service's first claim.
func CanWrite(ctx context.Context, c *Client, issueKey string) error {
	is, err := c.Issue(ctx, issueKey)
	if err != nil {
		return err
	}
	if err := c.SetTitle(ctx, is.ID, is.Title); err != nil {
		return fmt.Errorf("the key cannot write to %s (%v); make one with Full access", issueKey, err)
	}
	return nil
}

// CheckKey reads the viewer and its teams; every team key in teams must be visible, and with
// none named any team will do. CanWrite proves write access.
func CheckKey(ctx context.Context, c *Client, teams []string) (Viewer, error) {
	v, err := c.Viewer(ctx)
	if err != nil {
		return v, err
	}
	var seen []string
	for _, t := range v.Teams {
		seen = append(seen, t.Key)
	}
	if len(seen) == 0 {
		return v, fmt.Errorf("the key sees no Linear team")
	}
	for _, want := range teams {
		if !slices.Contains(seen, want) {
			return v, fmt.Errorf("the key cannot see team %s (it sees %s); make one that can", want, strings.Join(seen, ", "))
		}
	}
	return v, nil
}
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go test ./installer/internal/linearapi/ && go vet ./installer/...`
Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
git add installer/internal/linearapi
git commit -s -m "Linear client with a key check whose only write changes nothing"
```

- [ ] **Step 7 (manual, controller only): re-record against the real API**

This also settles two of the plan's Unconfirmed premises: that Linear refuses `issueUpdate` from a read-only key, and that a title written back unchanged adds nothing to the issue's history. With the real Linear key (`L`), and a second key `R` the owner makes for this step only with Read permission alone, in an interactive shell on the desktop:

```bash
read -rs L; read -rs R
cd installer/internal/linearapi/testdata
lin() { curl --http1.1 -si -H @<(printf 'Authorization: %s\nContent-Type: application/json\n' "$1") -d "$2" https://api.linear.app/graphql; }
q='{"query":"query { viewer { name email } teams(first: 100) { nodes { id key name } } }"}'
lin "$L" "$q" > viewer-ok.http
lin lin_api_invalid "$q" > bad-key.http
lin "$L" '{"query":"query { issue(id: \"NF-186\") { id identifier title state { type } } }"}' > issue-ok.http
lin "$L" '{"query":"query { issue(id: \"NF-999999\") { id identifier title state { type } } }"}' > issue-not-found.http
title=$(sed -n 's/.*"title":"\([^"]*\)".*/\1/p' issue-ok.http); id=$(sed -n 's/.*"id":"\([^"]*\)".*/\1/p' issue-ok.http)
upd="{\"query\":\"mutation { issueUpdate(id: \\\"$id\\\", input: { title: \\\"$title\\\" }) { success } }\"}"
lin "$L" "$upd" > update-ok.http
lin "$R" "$upd" > update-forbidden.http
unset L R
```

Pass: `update-ok.http` holds `"success":true`; `update-forbidden.http` holds an error and no `"success":true` (if it succeeded, the read-only key can write and the probe cannot tell the keys apart: stop and take the Unconfirmed table's fallback); NF-186's history in Linear shows no new entry from the update. Revoke `R` in Linear.

Then, in every recording, delete the `Content-Length` and `Transfer-Encoding` lines (curl prints the body already decoded, which `http.ReadResponse` cannot read under a `chunked` header); in `viewer-ok.http` replace the owner's name and email with `Ada Lovelace` and `ada@example.test` and the NF team's id with `team-nf`; in `issue-ok.http` replace the issue's id with `issue-186` and its title with `flocc digest` (the tests assert those). The recordings become public with the repo, so they must hold no private name. Run `go test ./installer/internal/linearapi/`; commit with `git commit -s -m "Re-record Linear responses"`.

---

### Task 8: `flocc keys`

**Depends on:** Tasks 4, 5, 6 and 7.

`flocc keys` fills each missing key in turn: it opens the page where the key is made (browser, URL and terminal QR code), reads the paste with echo off, checks it by prefix and then live, and stores it. The publish token's page opens only after the agent token passed. The ntfy topic is generated, shown as a QR code for the Android app (Part 2a's `qr.Write`), and confirmed with one test push. `flocc keys check` re-runs every live check. `keys.Fill` takes the slots to fill, so `host add` and `ship` (Tasks 9 and 10) ask for a missing key on the spot with the same prompt and checks.

GitHub's token page takes `name`, `description`, `target_name` (the resource owner), `expires_in` (1 to 366) and one parameter per permission (`contents=read`), per the URL parameter table in docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens (read 2026-10-20). Fine-grained tokens have no Checks permission: docs.github.com/en/rest/authentication/permissions-required-for-fine-grained-personal-access-tokens lists none, and the first page lists "calling the Checks API" among what fine-grained tokens cannot do. The agent token therefore asks for `statuses=read` (commit statuses) and `actions=read` (workflow runs and their logs, which is where GitHub Actions results live). Whether `gh pr checks` shows Actions results to such a token is in the Unconfirmed table. Repository selection cannot be prefilled, so the repos to tick are printed beside the URL. The installer's own help text in `steps/secrets.go`, which still says "Checks", changes here to match.

**Files:**
- Create: `installer/internal/keys/fill.go`, `installer/internal/keys/prompt.go`
- Create: `installer/internal/keys/fill_test.go`
- Create: `installer/internal/cli/keys.go`, `installer/internal/cli/keys_test.go`
- Modify: `installer/internal/cli/cli.go` (`deps`, `Main`, `run`)
- Modify: `installer/internal/steps/secrets.go` (the Linear and agent token help text)

**Interfaces:**
- Consumes: Task 5 (`keys.Store`, `keys.Slot`, `keys.Slots`, `keys.CheckPrefix`, `keys.Open`, `steps.CleanSecret`), Task 6 (`githubapi.Client`, `githubapi.Target`, `githubapi.CheckAgent`, `githubapi.CheckPublish`, `githubapi.DefaultBase`, `replay`, the recordings in `installer/internal/githubapi/testdata`), Task 7 (`linearapi.Client`, `linearapi.CheckKey`, `linearapi.CanWrite`, `linearapi.DefaultURL`, the recordings in `installer/internal/linearapi/testdata`), Task 3 (`hosts.Host`), Task 4 (`deps`, `run`, `listHosts`), Part 2a (`ship.OpenURL`, `qr.Write`, `answers.RandomTopic`).
- Produces:
  - `keys.Prompt` interface `{ Secret(title, help string) (string, error); Confirm(title string, def bool) (bool, error) }`; `keys.Huh` (huh forms, secrets with echo off).
  - `keys.Mapped{Teams []string; Repos []githubapi.Target; Digests []string}`; `keys.Env{Store Store; Prompt Prompt; Out io.Writer; Open func(url string); QR func(text string); HTTP *http.Client; GitHubAPI, LinearAPI, NtfyBase string; NewTopic func() string}`.
  - `keys.Fill(ctx, e Env, m Mapped, slots []Slot) error` (asks only for the slots in `slots` that the store lacks, in `Slots` order), `keys.ErrStopped`, `keys.Check(ctx, e Env, m Mapped) []string` (one line per problem; prints an `ok` line per good key), `keys.Accept(ctx, e Env, m Mapped, s Slot, raw string) (value, note string, err error)`, `keys.Page(s Slot, m Mapped) (url, help string)`.
  - In `cli`: `deps` gains `store func() keys.Store` (opened on first use, so host commands never touch the keyring), `prompt keys.Prompt`, `open func(string)`, `qr func(string)`, `http *http.Client`, `githubAPI, linearAPI, ntfyBase string`, `newTopic func() string`; `keysCmd(ctx, d *deps, args []string) int`; `keysEnv(d *deps) keys.Env`; `mapped(hs []hosts.Host) keys.Mapped`.

- [ ] **Step 1: Write the failing tests**

`installer/internal/keys/fill_test.go`:

```go
package keys

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/githubapi"
	"github.com/NovusEdge/flocc/installer/internal/replay"
)

// logStore keeps keys in memory and logs each Set into the fixture's event list, beside the
// pages opened, so a test can read their order.
type logStore struct {
	m      map[Slot]string
	events *[]string
}

func (s logStore) Get(k Slot) (string, error) { return s.m[k], nil }
func (logStore) Where() string                 { return "memory" }

func (s logStore) Set(k Slot, v string) error {
	s.m[k] = v
	*s.events = append(*s.events, "set "+string(k))
	return nil
}

// script answers the prompts in order; an exhausted list gives an empty paste or the default.
type script struct {
	secrets  []string
	confirms []bool
}

func (s *script) Secret(string, string) (string, error) {
	if len(s.secrets) == 0 {
		return "", nil
	}
	v := s.secrets[0]
	s.secrets = s.secrets[1:]
	return v, nil
}

func (s *script) Confirm(_ string, def bool) (bool, error) {
	if len(s.confirms) == 0 {
		return def, nil
	}
	v := s.confirms[0]
	s.confirms = s.confirms[1:]
	return v, nil
}

type fixture struct {
	env    Env
	store  logStore
	events []string
	out    bytes.Buffer
	qr     []string
	mu     sync.Mutex
	pushes []*http.Request
}

func newFixture(t *testing.T, gh, lin *replay.Server, p *script) *fixture {
	t.Helper()
	f := &fixture{}
	f.store = logStore{m: map[Slot]string{}, events: &f.events}
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.pushes = append(f.pushes, r)
		f.mu.Unlock()
	}))
	t.Cleanup(ntfy.Close)
	f.env = Env{Store: f.store, Prompt: p, Out: &f.out, HTTP: http.DefaultClient, NtfyBase: ntfy.URL,
		Open:     func(u string) { f.events = append(f.events, "open "+u) },
		QR:       func(s string) { f.qr = append(f.qr, s) },
		NewTopic: func() string { return "flocc-test" }}
	if gh != nil {
		f.env.GitHubAPI = gh.URL
	}
	if lin != nil {
		f.env.LinearAPI = lin.URL + "/graphql"
	}
	return f
}

func ghServer(t *testing.T, routes ...replay.Route) *replay.Server {
	return replay.New(t, "../githubapi/testdata", routes...)
}

// linServer answers the viewer query with file; more routes answer the write probe.
func linServer(t *testing.T, file string, more ...replay.Route) *replay.Server {
	routes := append([]replay.Route{{Method: "POST", Path: "/graphql", Body: "viewer", File: file}}, more...)
	return replay.New(t, "../linearapi/testdata", routes...)
}

var (
	userOK = replay.Route{Method: "GET", Path: "/user", File: "user-ok.http"}
	repoA  = replay.Route{Method: "GET", Path: "/repos/o/a", File: "repo-ok.http"}
	onlyA  = Mapped{Repos: []githubapi.Target{{Slug: "o/a", Base: "main"}}}
)

func TestFillAsksForEachKeyInOrderAndStoresIt(t *testing.T) {
	f := newFixture(t, ghServer(t, userOK), linServer(t, "viewer-ok.http"),
		&script{secrets: []string{"lin_api_k", "github_pat_agent", "github_pat_publish"}})
	if err := Fill(context.Background(), f.env, Mapped{}, Slots); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, e := range f.events {
		order = append(order, strings.SplitN(e, "?", 2)[0])
	}
	want := []string{
		"open https://linear.app/settings/account/security", "set linear_key",
		"open https://github.com/settings/personal-access-tokens/new", "set agent_token",
		"open https://github.com/settings/personal-access-tokens/new", "set publish_token",
		"set ntfy_topic",
	}
	if strings.Join(order, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%q", order)
	}
	if f.store.m[Publish] != "github_pat_publish" || f.store.m[Ntfy] != "flocc-test" {
		t.Fatalf("%v", f.store.m)
	}
	for _, secret := range []string{"lin_api_k", "github_pat_agent", "github_pat_publish"} {
		if strings.Contains(f.out.String(), secret) {
			t.Errorf("%s printed", secret)
		}
	}
}

func TestAKeyInTheWrongSlotIsRefusedBeforeAnyRequest(t *testing.T) {
	gh := ghServer(t, userOK)
	f := newFixture(t, gh, nil, &script{secrets: []string{"lin_api_x", "github_pat_agent"}})
	f.store.m[Linear] = "lin_api_k"
	if err := Fill(context.Background(), f.env, Mapped{}, Slots); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if f.store.m[Agent] != "github_pat_agent" || f.store.m[Publish] != "" || !strings.Contains(f.out.String(), "that is a Linear API key") {
		t.Fatalf("%v\n%s", f.store.m, f.out.String())
	}
	for _, r := range gh.Requests {
		if r.Auth != "Bearer github_pat_agent" {
			t.Errorf("the wrong-slot paste reached GitHub: %+v", r)
		}
	}
}

func TestAPasteIsTrimmedAndNameEqualsValueIsRefused(t *testing.T) {
	gh := ghServer(t, userOK)
	f := newFixture(t, gh, nil, &script{secrets: []string{"AGENT_TOKEN=github_pat_a", "  github_pat_a \n"}})
	f.store.m[Linear] = "lin_api_k"
	Fill(context.Background(), f.env, Mapped{}, Slots)
	if f.store.m[Agent] != "github_pat_a" || !strings.Contains(f.out.String(), "NAME=value") {
		t.Fatalf("%q\n%s", f.store.m[Agent], f.out.String())
	}
	// The refused paste never reached GitHub: every request carries the trimmed value.
	if len(gh.Requests) == 0 {
		t.Fatal("the trimmed paste was not checked")
	}
	for _, r := range gh.Requests {
		if r.Auth != "Bearer github_pat_a" {
			t.Errorf("%+v", r)
		}
	}
}

func TestAnAgentTokenThatCanPushIsRefusedAndAskedForAgain(t *testing.T) {
	gh := ghServer(t, userOK, repoA,
		replay.Route{Method: "POST", Path: "/repos/o/a/git/refs", Auth: "Bearer github_pat_push", File: "refs-unprocessable.http"},
		replay.Route{Method: "POST", Path: "/repos/o/a/git/refs", Auth: "Bearer github_pat_read", File: "refs-forbidden.http"})
	f := newFixture(t, gh, nil, &script{secrets: []string{"github_pat_push", "github_pat_read"}})
	f.store.m[Linear] = "lin_api_k"
	Fill(context.Background(), f.env, onlyA, Slots)
	if f.store.m[Agent] != "github_pat_read" || !strings.Contains(f.out.String(), "o/a: the token can push") ||
		!strings.Contains(f.out.String(), "paste it again") {
		t.Fatalf("%q\n%s", f.store.m[Agent], f.out.String())
	}
}

func TestFillAsksOnlyForTheSlotsNamed(t *testing.T) {
	f := newFixture(t, nil, nil, &script{secrets: []string{"lin_api_k"}})
	f.store.m[Linear] = "lin_api_k"
	if err := Fill(context.Background(), f.env, Mapped{}, []Slot{Linear}); err != nil || len(f.events) != 0 {
		t.Fatalf("%v %v", err, f.events)
	}
}

func TestAReadOnlyLinearKeyIsRefused(t *testing.T) {
	lin := linServer(t, "viewer-ok.http",
		replay.Route{Method: "POST", Path: "/graphql", Body: "issue(id", File: "issue-ok.http"},
		replay.Route{Method: "POST", Path: "/graphql", Body: "issueUpdate", File: "update-forbidden.http"})
	f := newFixture(t, nil, lin, &script{})
	_, _, err := Accept(context.Background(), f.env, Mapped{Teams: []string{"NF"}, Digests: []string{"NF-186"}}, Linear, "lin_api_ro")
	if err == nil || !strings.Contains(err.Error(), "cannot write to NF-186") {
		t.Fatal(err)
	}
}

func TestNtfyShowsTheAppLinkAndResendsUntilThePushArrives(t *testing.T) {
	f := newFixture(t, nil, nil, &script{confirms: []bool{false, true, true}})
	f.store.m[Linear], f.store.m[Agent], f.store.m[Publish] = "lin_api_k", "github_pat_a", "github_pat_p"
	if err := Fill(context.Background(), f.env, Mapped{}, Slots); err != nil {
		t.Fatal(err)
	}
	if len(f.qr) != 1 || f.qr[0] != "ntfy://ntfy.sh/flocc-test?display=flocc" || !strings.Contains(f.out.String(), "https://ntfy.sh/flocc-test") {
		t.Fatalf("%q\n%s", f.qr, f.out.String())
	}
	if len(f.pushes) != 2 || f.pushes[0].URL.Path != "/flocc-test" || f.pushes[0].Header.Get("Priority") != "5" || f.pushes[0].Header.Get("Title") != "flocc" {
		t.Fatalf("%d pushes", len(f.pushes))
	}
}

func TestGitHubPagesArePrefilled(t *testing.T) {
	m := Mapped{Repos: []githubapi.Target{{Slug: "o/a"}, {Slug: "o/b"}}}
	raw, help := Page(Agent, m)
	u, _ := url.Parse(raw)
	for k, v := range map[string]string{"name": "flocc agent", "expires_in": "366", "target_name": "o",
		"contents": "read", "pull_requests": "read", "issues": "read", "statuses": "read", "actions": "read"} {
		if got := u.Query().Get(k); got != v {
			t.Errorf("%s=%q", k, got)
		}
	}
	if u.Query().Has("checks") {
		t.Error("fine-grained tokens have no checks permission")
	}
	if !strings.Contains(help, "o/a, o/b") {
		t.Fatal(help)
	}
	raw, _ = Page(Publish, m)
	u, _ = url.Parse(raw)
	if q := u.Query(); q.Get("contents") != "write" || q.Get("pull_requests") != "write" || q.Has("issues") {
		t.Fatal(raw)
	}
}

func TestAGitHubPageForTwoOwnersNamesBoth(t *testing.T) {
	raw, help := Page(Agent, Mapped{Repos: []githubapi.Target{{Slug: "o/a"}, {Slug: "p/b"}}})
	u, _ := url.Parse(raw)
	if u.Query().Has("target_name") || !strings.Contains(help, "o and p") || !strings.Contains(help, "one owner") {
		t.Fatalf("%s\n%s", raw, help)
	}
}

func TestCheckNamesEveryProblem(t *testing.T) {
	gh := ghServer(t, userOK, repoA, replay.Route{Method: "POST", Path: "/repos/o/a/git/refs", File: "refs-unprocessable.http"})
	f := newFixture(t, gh, linServer(t, "bad-key.http"), &script{})
	f.store.m[Linear], f.store.m[Agent] = "lin_api_k", "github_pat_push"
	got := strings.Join(Check(context.Background(), f.env, onlyA), "\n")
	for _, want := range []string{"Authentication required", "o/a: the token can push", "GitHub publish token: missing", "ntfy topic: missing"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from:\n%s", want, got)
		}
	}
	// A stored token is edited on GitHub, which keeps its value; there is no prompt to paste into.
	if !strings.Contains(got, `open "flocc agent", Edit`) || !strings.Contains(got, "the token stays the same") ||
		!strings.Contains(got, "flocc keys check") || strings.Contains(got, "paste it again") {
		t.Errorf("wrong remedy:\n%s", got)
	}
}
```

`installer/internal/cli/keys_test.go`:

```go
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
)

func TestKeysRefusesAKeyOnTheCommandLine(t *testing.T) {
	d, _, errb := testDeps(t)
	for _, arg := range []string{"github_pat_x", "lin_api_x", "ghp_x"} {
		if code := run(context.Background(), d, []string{"keys", arg}); code != 2 || !strings.Contains(errb.String(), "shell history") {
			t.Fatalf("%s: exit %d %q", arg, code, errb.String())
		}
	}
}

func TestMappedJoinsTheHostsTeamsReposAndDigests(t *testing.T) {
	r := func(u, base string) answers.Repo { return answers.Repo{URL: u, BaseBranch: base} }
	m := mapped([]hosts.Host{
		{Name: "a", Answers: answers.Answers{TeamKey: "NF", DigestIssue: "NF-7", Repos: []answers.Repo{r("https://github.com/O/A.git", "main")}}},
		{Name: "b", Answers: answers.Answers{TeamKey: "NF", DigestIssue: "NF-7", Repos: []answers.Repo{r("https://github.com/o/a", "main"), r("https://github.com/o/b", "trunk")}}},
	})
	if strings.Join(m.Teams, ",") != "NF" || len(m.Repos) != 2 || m.Repos[0].Slug != "o/a" || m.Repos[1].Base != "trunk" ||
		strings.Join(m.Digests, ",") != "NF-7" {
		t.Fatalf("%+v", m)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/internal/keys/ ./installer/internal/cli/`
Expected: FAIL to build: `undefined: Fill`, `undefined: Page`, `undefined: mapped`.

- [ ] **Step 3: Write the fill flow**

`installer/internal/keys/fill.go`:

```go
package keys

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/githubapi"
	"github.com/NovusEdge/flocc/installer/internal/linearapi"
	"github.com/NovusEdge/flocc/installer/internal/steps"
)

type Prompt interface {
	// Secret reads a paste with echo off; "" when it is left empty.
	Secret(title, help string) (string, error)
	Confirm(title string, def bool) (bool, error)
}

// Mapped is what the host files map, which the keys are checked against. The Linear key's write
// probe runs on the first digest issue Linear still has.
type Mapped struct {
	Teams   []string
	Repos   []githubapi.Target
	Digests []string
}

type Env struct {
	Store  Store
	Prompt Prompt
	Out    io.Writer
	Open   func(url string) // the browser, the URL and a QR code
	QR     func(text string)
	HTTP   *http.Client
	// The API bases; githubapi.DefaultBase, linearapi.DefaultURL and https://ntfy.sh outside tests.
	GitHubAPI, LinearAPI, NtfyBase string
	NewTopic                       func() string
}

var ErrStopped = errors.New("stopped at an empty paste")

// Fill asks for each key in slots that the store lacks, in Slots order, and stores each one only
// once it passes its checks.
func Fill(ctx context.Context, e Env, m Mapped, slots []Slot) error {
	for _, s := range Slots {
		if !slices.Contains(slots, s) {
			continue
		}
		v, err := e.Store.Get(s)
		if err != nil {
			return err
		}
		if v != "" {
			continue
		}
		if s == Ntfy {
			err = fillNtfy(ctx, e)
		} else {
			err = fillKey(ctx, e, m, s)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func fillKey(ctx context.Context, e Env, m Mapped, s Slot) error {
	stored, err := askKey(ctx, e, m, s, "Paste it here; nothing shows while you paste. Leave it empty to stop.")
	if err == nil && !stored {
		err = ErrStopped
	}
	return err
}

// askKey opens s's page and asks until a paste passes its checks and is stored. An empty paste
// stores nothing and returns false.
func askKey(ctx context.Context, e Env, m Mapped, s Slot, help string) (bool, error) {
	u, page := Page(s, m)
	fmt.Fprintf(e.Out, "\n%s\n%s\n%s\n", s.Title(), page, u)
	e.Open(u)
	for {
		raw, err := e.Prompt.Secret(s.Title(), help)
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(raw) == "" {
			return false, nil
		}
		v, note, err := Accept(ctx, e, m, s, raw)
		if err != nil {
			fmt.Fprintf(e.Out, "Not stored: %v\n", err)
			continue
		}
		if err := e.Store.Set(s, v); err != nil {
			return false, err
		}
		fmt.Fprintf(e.Out, "Stored in %s: %s\n", e.Store.Where(), note)
		return true, nil
	}
}

// Accept cleans a paste and checks it, by prefix and then live, without storing it.
func Accept(ctx context.Context, e Env, m Mapped, s Slot, raw string) (value, note string, err error) {
	v, err := steps.CleanSecret(raw)
	if err == nil {
		err = CheckPrefix(s, v)
	}
	if err == nil {
		note, err = checkLive(ctx, e, m, s, v)
	}
	if _, ok := err.(repoProblems); ok {
		err = fmt.Errorf("%w\nChange the token on GitHub (its repositories and permissions can be edited) and paste it again.", err)
	}
	return v, note, err
}

// repoProblems are the repos a working token cannot serve, one line each. The fix differs: a
// paste is asked for again, a stored token is edited on GitHub.
type repoProblems []string

func (p repoProblems) Error() string { return strings.Join(p, "\n") }

func checkLive(ctx context.Context, e Env, m Mapped, s Slot, v string) (string, error) {
	if s == Linear {
		lin := &linearapi.Client{URL: e.LinearAPI, Key: v, HTTP: e.HTTP}
		viewer, err := linearapi.CheckKey(ctx, lin, m.Teams)
		if err != nil {
			return "", err
		}
		// One write proves the key's scope. A digest Linear no longer has is skipped: that is the
		// host file's problem, which host add fixes. With no host yet there is no digest to write
		// to, and host add's own check runs the probe later.
		for _, key := range m.Digests {
			err := linearapi.CanWrite(ctx, lin, key)
			if errors.Is(err, linearapi.ErrNotFound) {
				continue
			}
			if err != nil {
				return "", err
			}
			break
		}
		var teams []string
		for _, t := range viewer.Teams {
			teams = append(teams, t.Key)
		}
		return fmt.Sprintf("%s, teams %s", viewer.Name, strings.Join(teams, ", ")), nil
	}
	check := githubapi.CheckAgent
	if s == Publish {
		check = githubapi.CheckPublish
	}
	res, err := check(ctx, &githubapi.Client{Base: e.GitHubAPI, Token: v, HTTP: e.HTTP}, m.Repos)
	if err != nil {
		return "", err
	}
	if len(res.Problems) > 0 {
		return "", repoProblems(res.Problems)
	}
	if res.Expires.IsZero() {
		return "never expires", nil
	}
	return "expires " + res.Expires.Format("2006-01-02"), nil
}

// Check runs the live checks on every stored key. It prints an ok line per good key and returns
// one line per problem.
func Check(ctx context.Context, e Env, m Mapped) []string {
	var problems []string
	for _, s := range []Slot{Linear, Agent, Publish} {
		v, err := e.Store.Get(s)
		if err == nil && v == "" {
			err = errors.New("missing; run flocc keys")
		}
		var note string
		if err == nil {
			note, err = checkLive(ctx, e, m, s, v)
		}
		if _, ok := err.(repoProblems); ok {
			// Editing a fine-grained token keeps its value, so nothing needs pasting.
			err = fmt.Errorf("%w\nOn github.com/settings/personal-access-tokens open %q, Edit, and fix the above under "+
				"Repository access and Permissions (the token stays the same), then run flocc keys check.", err, tokenName[s])
		}
		if err != nil {
			problems = append(problems, s.Title()+": "+err.Error())
			continue
		}
		fmt.Fprintf(e.Out, "ok    %s: %s\n", s.Title(), note)
	}
	if t, err := e.Store.Get(Ntfy); err != nil || t == "" {
		problems = append(problems, "ntfy topic: missing; run flocc keys")
	}
	return problems
}

// tokenName is the name each GitHub token's page fills in, which the owner finds it by later.
var tokenName = map[Slot]string{Agent: "flocc agent", Publish: "flocc publish"}

// Page is where a key is made and what to do there. GitHub prefills everything but the repos.
func Page(s Slot, m Mapped) (string, string) {
	if s == Linear {
		// The same path as the installer's help text in steps/secrets.go.
		help := "Linear → Settings → Account → Security & access → New API key. Name it flocc and give it Full access: the service writes comments and moves issues."
		if len(m.Teams) > 0 {
			help += " It must see team " + strings.Join(m.Teams, ", ") + "."
		}
		return "https://linear.app/settings/account/security", help
	}
	q := url.Values{"expires_in": {"366"}, "name": {tokenName[s]}}
	if s == Agent {
		q.Set("description", "Read-only. flocc's agents run gh with it.")
		// No checks: fine-grained tokens have no such permission. Actions results are read
		// through actions (runs and logs) and statuses.
		for _, p := range []string{"contents", "pull_requests", "issues", "statuses", "actions"} {
			q.Set(p, "read")
		}
	} else {
		q.Set("description", "flocc's service pushes branches and opens pull requests with it.")
		q.Set("contents", "write")
		q.Set("pull_requests", "write")
	}
	var owners, slugs []string
	for _, r := range m.Repos {
		slugs = append(slugs, r.Slug)
		if o, _, _ := strings.Cut(r.Slug, "/"); !slices.Contains(owners, o) {
			owners = append(owners, o)
		}
	}
	help := "Repository access: Only select repositories, then tick the repos you will map; flocc host add checks them again."
	if len(slugs) > 0 {
		help = "Repository access: Only select repositories, then tick " + strings.Join(slugs, ", ") + "."
	}
	switch len(owners) {
	case 1:
		q.Set("target_name", owners[0])
	case 0:
	default:
		help += " These repos belong to " + strings.Join(owners[:len(owners)-1], ", ") + " and " + owners[len(owners)-1] +
			", but a fine-grained token serves one owner: pick the owner as Resource owner, and map only that owner's repos."
	}
	return "https://github.com/settings/personal-access-tokens/new?" + q.Encode(), help
}

func fillNtfy(ctx context.Context, e Env) error {
	topic := e.NewTopic()
	if err := e.Store.Set(Ntfy, topic); err != nil {
		return err
	}
	web := "https://ntfy.sh/" + topic
	fmt.Fprintf(e.Out, "\nntfy topic\nScan this with the ntfy app on Android, or subscribe to %s in the app.\n", web)
	e.QR("ntfy://ntfy.sh/" + topic + "?display=flocc")
	for {
		if err := push(ctx, e, topic); err != nil {
			return err
		}
		arrived, err := e.Prompt.Confirm("A test push went to "+web+". Did it arrive?", true)
		if err != nil || arrived {
			return err
		}
		again, err := e.Prompt.Confirm("Subscribe to "+web+" in the ntfy app. Send another test push?", true)
		if err != nil {
			return err
		}
		if !again {
			fmt.Fprintf(e.Out, "Kept the topic; subscribe to %s before the first ship.\n", web)
			return nil
		}
	}
}

func push(ctx context.Context, e Env, topic string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", e.NtfyBase+"/"+topic, strings.NewReader("test push from flocc keys"))
	if err != nil {
		return err
	}
	req.Header.Set("Title", "flocc")
	req.Header.Set("Priority", "5")
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy answered the test push with %s", resp.Status)
	}
	return nil
}
```

`installer/internal/keys/prompt.go`:

```go
package keys

import "charm.land/huh/v2"

// Huh asks on the terminal. A paste is read with echo off, so it never shows on screen or in the
// scrollback, and never reaches shell history.
type Huh struct{}

func (Huh) Secret(title, help string) (string, error) {
	var v string
	err := huh.NewForm(huh.NewGroup(huh.NewInput().Title(title).Description(help).
		EchoMode(huh.EchoModeNone).Value(&v))).Run()
	return v, err
}

func (Huh) Confirm(title string, def bool) (bool, error) {
	v := def
	err := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title(title).Value(&v))).Run()
	return v, err
}
```

- [ ] **Step 4: Add the verb**

`installer/internal/cli/keys.go`:

```go
package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/githubapi"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
)

func keysEnv(d *deps) keys.Env {
	return keys.Env{Store: d.store(), Prompt: d.prompt, Out: d.out, Open: d.open, QR: d.qr, HTTP: d.http,
		GitHubAPI: d.githubAPI, LinearAPI: d.linearAPI, NtfyBase: d.ntfyBase, NewTopic: d.newTopic}
}

// mapped is every team, repo and digest issue the host files map, each once.
func mapped(hs []hosts.Host) keys.Mapped {
	var m keys.Mapped
	for _, h := range hs {
		if h.TeamKey != "" && !slices.Contains(m.Teams, h.TeamKey) {
			m.Teams = append(m.Teams, h.TeamKey)
		}
		if h.DigestIssue != "" && !slices.Contains(m.Digests, h.DigestIssue) {
			m.Digests = append(m.Digests, h.DigestIssue)
		}
		for _, r := range h.Repos {
			slug := strings.TrimPrefix(answers.NormalizeURL(r.URL), "https://github.com/")
			if !slices.ContainsFunc(m.Repos, func(t githubapi.Target) bool { return t.Slug == slug }) {
				m.Repos = append(m.Repos, githubapi.Target{Slug: slug, Base: r.BaseBranch})
			}
		}
	}
	return m
}

func looksLikeKey(s string) bool {
	return slices.ContainsFunc([]string{"lin_api_", "github_pat_", "ghp_", "gho_"}, func(p string) bool { return strings.HasPrefix(s, p) })
}

// report prints the problems keys.Check found and returns the exit status.
func report(d *deps, problems []string) int {
	for _, p := range problems {
		fmt.Fprintln(d.err, "FAIL  "+p)
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}

func keysCmd(ctx context.Context, d *deps, args []string) int {
	if slices.ContainsFunc(args, looksLikeKey) {
		fmt.Fprintln(d.err, "flocc keys: a key on the command line stays in your shell history; run flocc keys and paste it at the prompt")
		return 2
	}
	m := mapped(listHosts(d))
	switch {
	case len(args) == 0:
		e := keysEnv(d)
		if err := keys.Fill(ctx, e, m, keys.Slots); err != nil {
			fmt.Fprintf(d.err, "flocc keys: %v; run flocc keys again to go on\n", err)
			return 1
		}
		return report(d, keys.Check(ctx, e, m))
	case len(args) == 1 && args[0] == "check":
		e := keysEnv(d)
		// A desktop whose keyring once failed stays on secrets.toml; this shows it.
		fmt.Fprintf(d.out, "Keys are in %s.\n", e.Store.Where())
		return report(d, keys.Check(ctx, e, m))
	}
	fmt.Fprint(d.err, usage)
	return 2
}
```

In `cli.go`, add the fields to `deps`:

```go
	store     func() keys.Store // opened on first use: host commands never wait on the keyring
	prompt    keys.Prompt
	open      func(url string)
	qr        func(text string)
	http      *http.Client
	githubAPI string
	linearAPI string
	ntfyBase  string
	newTopic  func() string
```

set them in `Main` (add `net/http`, `sync`, `time` and the `answers`, `githubapi`, `keys`, `linearapi`, `qr` packages to the imports):

```go
	d.store = sync.OnceValue(func() keys.Store { return keys.Open(home, os.Stderr) })
	d.prompt = keys.Huh{}
	d.open = ship.OpenURL
	d.qr = func(text string) {
		if err := qr.Write(os.Stdout, text); err != nil {
			fmt.Fprintln(os.Stderr, "no QR code:", err)
		}
	}
	d.http = &http.Client{Timeout: 30 * time.Second}
	d.githubAPI, d.linearAPI, d.ntfyBase = githubapi.DefaultBase, linearapi.DefaultURL, "https://ntfy.sh"
	d.newTopic = answers.RandomTopic
```

and add `case "keys": return keysCmd(ctx, d, rest[1:])` to `run`'s switch.

In `installer/internal/steps/secrets.go`, make the installer's help text say what the desktop's pages say: the Linear entry's help becomes `"Linear → Settings → Account → Security & access → New API key, with Full access. Limit it to team NF."`, and in the agent token's help replace `Contents, Pull requests, Checks, Issues` with `Contents, Pull requests, Issues, Commit statuses, Actions` (fine-grained tokens have no Checks permission). No test pins either string.

- [ ] **Step 5: Run the tests to see them pass**

Run: `go test -race ./installer/... && go vet ./... && CGO_ENABLED=0 go build ./installer/...`
Expected: `ok` for every package.

- [ ] **Step 6: Commit**

```bash
git add installer/internal/keys installer/internal/cli installer/internal/steps/secrets.go
git commit -s -m "flocc keys: make, check and store each key once"
```

---

### Task 9: `flocc host add` and `flocc host import`

**Depends on:** Task 8 (and through it 3, 4, 6, 7).

`host add` writes a host file from what the keys can see: the repos the agent token reads (offered ticked, through the repo form the installer already has), the Linear team (picked without asking when the key sees one), the digest issue (an existing host's open one on the same team, else an open issue titled `flocc digest`, else a new one), and the missing labels (`claude`, `escalation`, `needs-decision`, one per repo). It asks for the Linear key and the agent token first if the store lacks them, and for the publish token and the ntfy topic once the host file is saved, so the publish token's page lists the repos just mapped. It then re-runs the key checks against every host's repos; the Linear check now writes the digest issue's title back to itself, which proves the key can write. `host import` builds the file from a host's own saved answers, and takes each key the desktop lacks from the host's own secret files, through the same checks as a paste, so moving a host to the desktop store never needs new tokens.

**Files:**
- Modify: `installer/internal/cli/host.go`
- Create: `installer/internal/cli/prompt.go`
- Create: `installer/internal/cli/host_test.go`
- Modify: `installer/internal/cli/cli.go` (`deps.prompt` becomes a `prompter`; `Main`)
- Modify: `installer/internal/ui/huh.go` (extract `RepoForm`)
- Modify: `installer/internal/steps/secrets.go` (`SecretFile`), `installer/internal/steps/service_test.go`

**Interfaces:**
- Consumes: Task 3 (`hosts.Host`, `hosts.Load`, `hosts.Save`, `hosts.Dir`), Task 4 (`hostCmd`, `hostRun`, `listHosts`, `ship.Capture`, `deps.resolve`, `deps.run`, `fakeSSH` with `files`, `hostDeps`), Task 6 (`githubapi.Client`, `ListRepos`, `Repo`), Task 7 (`linearapi.Client`, `Viewer`, `Issue`, `(Issue).Open`, `ErrNotFound`, `FindOpenIssue`, `CreateIssue`, `MissingLabels`, `CreateLabel`, `Team`), Task 8 (`keys.Fill`, `keys.Accept`, `keys.Check`, `keysEnv`, `mapped`, `keys.Huh`, `keys.Prompt`, `deps.store`), Part 2a (`answers.Parse`), `steps.ParseRepos`, `steps.MergeRepos`, `steps.secretSpecs`, `answers.Exclude`, `answers.SameURL`.
- Produces: `ui.RepoForm(a *answers.Answers, found []answers.Repo, defaults func(url string) (base string, draft bool)) error`; `cli.prompter` (`keys.Prompt` plus `Repos(a *answers.Answers, found []answers.Repo, defaults func(string) (string, bool)) error`, `Team(teams []linearapi.Team) (linearapi.Team, error)`, `Note(title, body string)`); `steps.SecretFile(id string) string` (`"gh-agent.token"` for `"agent_token"`, `""` for an unknown id); `hostFile(d *deps, name string) string` (`filepath.Join(hosts.Dir(d.home), name+".toml")`); `hostAdd(ctx context.Context, d *deps, name, sshDest string) error` (`sshDest` "" keeps the file's or defaults to `root@<name>`); `addHost(ctx, d *deps, name string, reach hosts.Host) error`; `importHost(ctx, d *deps, name string, reach hosts.Host) error`; `const digestTitle = "flocc digest"`; test helpers `hostScript` (with `secrets` and `err`), `writeSecrets`, `allKeys`, `fakeLinear`, `fakeGitHub`, `addDeps`.

- [ ] **Step 1: Extract the repo form**

In `installer/internal/ui/huh.go`, move everything in `ConfigForm` from `start := 0` to the end of the function into a new exported function, and make `ConfigForm` end with `return RepoForm(a, found, defaults)`:

```go
// RepoForm ticks the repos agents may work on from found, then asks for more by URL. With
// found empty it asks for every repo by URL.
func RepoForm(a *answers.Answers, found []answers.Repo, defaults func(url string) (base string, draft bool)) error {
	start := 0
	// … the moved lines, unchanged
}
```

Run: `go build ./installer/... && go test ./installer/internal/ui/ ./installer/internal/steps/`
Expected: builds; `ok` (or `no test files` for `ui`).

- [ ] **Step 2: Name the host's secret files**

Append to `installer/internal/steps/service_test.go`:

```go
func TestSecretFileNamesTheHostsFiles(t *testing.T) {
	for id, file := range map[string]string{"linear_key": "linear.key", "agent_token": "gh-agent.token", "publish_token": "gh-publish.token", "x": ""} {
		if got := SecretFile(id); got != file {
			t.Errorf("%s: %q", id, got)
		}
	}
}
```

Append to `installer/internal/steps/secrets.go`:

```go
// SecretFile is the file under ~/.config/flocc holding the secret with this answers id; "" for
// an unknown id.
func SecretFile(id string) string {
	for _, s := range secretSpecs {
		if s.id == id {
			return s.file
		}
	}
	return ""
}
```

Run: `go test ./installer/internal/steps/`
Expected: `ok`.

- [ ] **Step 3: Write the failing tests**

`installer/internal/cli/host_test.go`:

```go
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/linearapi"
	"github.com/NovusEdge/flocc/installer/internal/ship"
)

// hostScript ticks every repo offered, picks the first team and pastes secrets in order. With
// secrets used up it returns err, as a Ctrl-C at the prompt does, or else an empty paste.
type hostScript struct {
	secrets   []string
	err       error
	teamAsked bool
	notes     []string
}

func (s *hostScript) Confirm(_ string, def bool) (bool, error) { return def, nil }
func (s *hostScript) Note(title, _ string)                     { s.notes = append(s.notes, title) }

func (s *hostScript) Secret(string, string) (string, error) {
	if len(s.secrets) == 0 {
		return "", s.err
	}
	v := s.secrets[0]
	s.secrets = s.secrets[1:]
	return v, nil
}

func (s *hostScript) Repos(a *answers.Answers, found []answers.Repo, _ func(string) (string, bool)) error {
	a.Repos = found
	return nil
}

func (s *hostScript) Team(ts []linearapi.Team) (linearapi.Team, error) {
	s.teamAsked = true
	return ts[0], nil
}

// writeSecrets makes the file store, so no test reaches the machine's keyring.
func writeSecrets(t *testing.T, home, body string) {
	t.Helper()
	p := filepath.Join(home, ".config/flocc/secrets.toml")
	os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const allKeys = "linear_key = 'lin_api_k'\nagent_token = 'github_pat_agent'\npublish_token = 'github_pat_publish'\nntfy_topic = 'flocc-t'\n"

// fakeLinear keeps labels and issues, so a second host add finds what the first one made.
// issueErr, when set, is the error every issue lookup gets, as a rate limit or outage gives.
type fakeLinear struct {
	mu                                         sync.Mutex
	labels                                     []string
	issues                                     map[string]string // key -> title
	issueErr                                   string
	labelCreates, issueCreates, finds, updates int
}

func (f *fakeLinear) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var req struct {
		Query     string
		Variables map[string]any
	}
	json.NewDecoder(r.Body).Decode(&req)
	q, v := req.Query, req.Variables
	issue := func(key string) map[string]any {
		return map[string]any{"id": "id-" + key, "identifier": key, "title": f.issues[key], "state": map[string]any{"type": "unstarted"}}
	}
	var data any
	switch {
	case strings.Contains(q, "issueUpdate"):
		// The write probe: the title must come back unchanged.
		if title := f.issues[strings.TrimPrefix(v["id"].(string), "id-")]; v["title"] != title {
			http.Error(w, "the probe changed the title", 400)
			return
		}
		f.updates++
		data = map[string]any{"issueUpdate": map[string]any{"success": true}}
	case strings.Contains(q, "issueLabelCreate"):
		f.labels = append(f.labels, v["name"].(string))
		f.labelCreates++
		data = map[string]any{"issueLabelCreate": map[string]any{"success": true}}
	case strings.Contains(q, "issueCreate"):
		key := fmt.Sprintf("NF-%d", 100+len(f.issues))
		f.issues[key] = v["title"].(string)
		f.issueCreates++
		data = map[string]any{"issueCreate": map[string]any{"issue": issue(key)}}
	case strings.Contains(q, "issueLabels"):
		var nodes []any
		for _, n := range v["names"].([]any) {
			if slices.Contains(f.labels, n.(string)) {
				nodes = append(nodes, map[string]any{"name": n, "team": map[string]any{"key": "NF"}})
			}
		}
		data = map[string]any{"teams": map[string]any{"nodes": []any{map[string]any{"id": "team-nf"}}},
			"issueLabels": map[string]any{"nodes": nodes}}
	case strings.Contains(q, "issues(first"):
		f.finds++
		var nodes []any
		for k, title := range f.issues {
			if title == v["title"] {
				nodes = append(nodes, issue(k))
			}
		}
		data = map[string]any{"issues": map[string]any{"nodes": nodes}}
	case strings.Contains(q, "issue(id"):
		key := v["id"].(string)
		msg := f.issueErr
		if _, ok := f.issues[key]; !ok && msg == "" {
			msg = "Entity not found: Issue"
		}
		if msg != "" {
			json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": msg}}})
			return
		}
		data = map[string]any{"issue": issue(key)}
	case strings.Contains(q, "viewer"):
		data = map[string]any{"viewer": map[string]any{"name": "Ada", "email": "ada@example.test"},
			"teams": map[string]any{"nodes": []any{map[string]any{"id": "team-nf", "key": "NF", "name": "Nimblefox"}}}}
	default:
		http.Error(w, "unexpected query", 400)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// fakeGitHub lists repo o/a; only github_pat_publish may push and open pull requests.
func fakeGitHub(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publish := r.Header.Get("Authorization") == "Bearer github_pat_publish"
		switch {
		case r.URL.Path == "/user":
			w.Write([]byte(`{"login":"o"}`))
		case r.URL.Path == "/user/repos":
			w.Write([]byte(`[{"name":"a","html_url":"https://github.com/o/a","default_branch":"main","private":true,"size":3,"owner":{"login":"o"}}]`))
		case r.Method == "GET" && r.URL.Path == "/repos/o/a":
			w.Write([]byte(`{"default_branch":"main","private":true}`))
		case publish && (strings.HasSuffix(r.URL.Path, "/git/refs") || strings.HasSuffix(r.URL.Path, "/pulls")):
			w.WriteHeader(422)
		case strings.HasSuffix(r.URL.Path, "/git/refs"):
			w.WriteHeader(403)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func addDeps(t *testing.T, secrets string) (*deps, *fakeLinear, *hostScript) {
	t.Helper()
	d, _, _ := testDeps(t)
	lin := &fakeLinear{issues: map[string]string{}}
	ls := httptest.NewServer(lin)
	t.Cleanup(ls.Close)
	writeSecrets(t, d.home, secrets)
	p := &hostScript{}
	d.store = func() keys.Store { return keys.Open(d.home, io.Discard) }
	d.prompt = p
	d.http = http.DefaultClient
	d.githubAPI, d.linearAPI = fakeGitHub(t).URL, ls.URL
	ntfy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(ntfy.Close)
	d.ntfyBase, d.newTopic = ntfy.URL, func() string { return "flocc-t" }
	d.open, d.qr = func(string) {}, func(string) {}
	return d, lin, p
}

func loadHost(t *testing.T, d *deps, name string) hosts.Host {
	t.Helper()
	h, err := hosts.Load(hosts.Dir(d.home), name)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHostAddMakesTheDigestAndLabelsOnce(t *testing.T) {
	d, lin, p := addDeps(t, allKeys)
	for range 2 {
		if err := hostAdd(context.Background(), d, "h1", ""); err != nil {
			t.Fatal(err)
		}
	}
	if lin.labelCreates != 4 || lin.issueCreates != 1 || p.teamAsked {
		t.Fatalf("labels %d issues %d team asked %v", lin.labelCreates, lin.issueCreates, p.teamAsked)
	}
	if !slices.Equal(lin.labels, []string{"claude", "escalation", "needs-decision", "a"}) || lin.issues["NF-100"] != digestTitle {
		t.Fatalf("%v %v", lin.labels, lin.issues)
	}
	// The key check after each add proved the Linear key can write, on the digest.
	if lin.updates != 2 {
		t.Fatalf("write probes %d", lin.updates)
	}
	h := loadHost(t, d, "h1")
	if h.SSH != "root@h1" || h.Hostname != "h1" || h.TeamKey != "NF" || h.DigestIssue != "NF-100" ||
		len(h.Repos) != 1 || h.Repos[0].URL != "https://github.com/o/a" {
		t.Fatalf("%+v", h)
	}
	if hostFile(d, "h1") != filepath.Join(d.home, ".config/flocc/hosts/h1.toml") {
		t.Fatal(hostFile(d, "h1"))
	}
}

func TestHostAddSharesTheDigestOfAHostOnTheSameTeam(t *testing.T) {
	d, lin, _ := addDeps(t, allKeys)
	lin.issues["NF-7"] = "digest"
	b := hosts.Host{Name: "b", SSH: "root@b", Answers: answers.Answers{TeamKey: "NF", DigestIssue: "NF-7"}}
	if err := hosts.Save(hosts.Dir(d.home), b); err != nil {
		t.Fatal(err)
	}
	if err := hostAdd(context.Background(), d, "h1", "root@10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	h := loadHost(t, d, "h1")
	if h.DigestIssue != "NF-7" || h.SSH != "root@10.0.0.5" || lin.issueCreates != 0 || lin.finds != 0 {
		t.Fatalf("%+v creates %d finds %d", h, lin.issueCreates, lin.finds)
	}
}

// A blip at Linear must not look like a missing digest: an imported digest is not titled
// "flocc digest", so falling through would make a second one and rewrite the host file.
func TestADigestLookupErrorIsNotAMissingDigest(t *testing.T) {
	d, lin, _ := addDeps(t, allKeys)
	lin.issueErr = "Rate limit exceeded"
	h1 := hosts.Host{Name: "h1", SSH: "root@h1", Answers: answers.Answers{TeamKey: "NF", DigestIssue: "NF-186"}}
	if err := hosts.Save(hosts.Dir(d.home), h1); err != nil {
		t.Fatal(err)
	}
	err := hostAdd(context.Background(), d, "h1", "")
	if err == nil || !strings.Contains(err.Error(), "Rate limit") || lin.issueCreates != 0 || lin.finds != 0 {
		t.Fatalf("%v creates %d finds %d", err, lin.issueCreates, lin.finds)
	}
	if h := loadHost(t, d, "h1"); h.DigestIssue != "NF-186" {
		t.Fatalf("%+v", h)
	}
}

func TestHostAddSkipsADigestAnotherHostNamesButLinearNoLongerHas(t *testing.T) {
	d, lin, _ := addDeps(t, allKeys)
	b := hosts.Host{Name: "b", SSH: "root@b", Answers: answers.Answers{TeamKey: "NF", DigestIssue: "NF-7"}}
	if err := hosts.Save(hosts.Dir(d.home), b); err != nil {
		t.Fatal(err)
	}
	// NF-7 is unknown to fakeLinear, so Linear answers "Entity not found", as for a deleted issue.
	if err := hostAdd(context.Background(), d, "h1", ""); err != nil {
		t.Fatal(err)
	}
	if h := loadHost(t, d, "h1"); h.DigestIssue == "NF-7" || lin.issueCreates != 1 {
		t.Fatalf("%+v creates %d", h, lin.issueCreates)
	}
}

// A fresh desktop: host add asks for each key where it needs it, instead of stopping.
func TestHostAddAsksForAMissingKeyOnTheSpot(t *testing.T) {
	d, lin, p := addDeps(t, "")
	p.secrets = []string{"lin_api_k", "github_pat_agent", "github_pat_publish"}
	if err := hostAdd(context.Background(), d, "h1", ""); err != nil {
		t.Fatal(err)
	}
	st := d.store()
	for slot, want := range map[keys.Slot]string{keys.Linear: "lin_api_k", keys.Agent: "github_pat_agent", keys.Publish: "github_pat_publish", keys.Ntfy: "flocc-t"} {
		if v, _ := st.Get(slot); v != want {
			t.Errorf("%s: %q", slot, v)
		}
	}
	if h := loadHost(t, d, "h1"); h.DigestIssue != "NF-100" || lin.labelCreates != 4 {
		t.Fatalf("%+v labels %d", h, lin.labelCreates)
	}
}

func TestHostAddStopsAtAnEmptyPasteBeforeWritingAnything(t *testing.T) {
	d, lin, _ := addDeps(t, "")
	err := hostAdd(context.Background(), d, "h1", "")
	if !errors.Is(err, keys.ErrStopped) || lin.labelCreates != 0 {
		t.Fatal(err)
	}
	if _, err := hosts.Load(hosts.Dir(d.home), "h1"); err == nil {
		t.Fatal("a host file was written without keys")
	}
}

func TestHostAddSaysWhichKeyMissesTheNewRepo(t *testing.T) {
	d, _, _ := addDeps(t, strings.Replace(allKeys, "github_pat_publish", "github_pat_weak", 1))
	err := hostAdd(context.Background(), d, "h1", "")
	if err == nil || !strings.Contains(err.Error(), "o/a: the token cannot push") || !strings.Contains(err.Error(), "the token stays the same") {
		t.Fatal(err)
	}
	loadHost(t, d, "h1") // saved even though a key does not cover it yet
}

const hostAnswers = "team_key = \"NF\"\ndigest_issue = \"NF-186\"\nntfy_topic = \"flocc-host\"\n\n" +
	"[[repos]]\nname = \"a\"\nurl = \"https://github.com/o/a\"\nbase_branch = \"main\"\ncheck = \"just check\"\n"

func importDeps(t *testing.T) (*deps, *fakeSSH, *fakeLinear) {
	t.Helper()
	d, lin, _ := addDeps(t, "")
	lin.issues["NF-186"] = "Digest"
	f := &fakeSSH{out: map[string]string{"root@h1": ""}, files: map[string]string{".config/flocc/answers.toml": hostAnswers}}
	d.run = f
	d.resolve = func(_ context.Context, target string) (ship.Dest, error) { return ship.Dest{SSH: target}, nil }
	return d, f, lin
}

func TestHostImportCopiesTheHostsAnswersAndKeepsItsTopic(t *testing.T) {
	d, f, _ := importDeps(t)
	if code := run(context.Background(), d, []string{"host", "import", "-H", "h1"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	h := loadHost(t, d, "h1")
	if h.TeamKey != "NF" || h.DigestIssue != "NF-186" || h.NtfyTopic != "" || len(h.Repos) != 1 {
		t.Fatalf("%+v", h)
	}
	if topic, _ := d.store().Get(keys.Ntfy); topic != "flocc-host" {
		t.Fatalf("topic %q", topic)
	}
	words := shWords(t, f.runs[0].Args[len(f.runs[0].Args)-1])
	if script := words[len(words)-1]; script != "cd && cat .config/flocc/answers.toml" {
		t.Fatalf("%q", script)
	}
}

// Moving a host onto the desktop store needs no new tokens: each key comes through the same
// checks as a paste, and one that fails them is left for flocc keys.
func TestHostImportTakesTheHostsKeysThroughThePasteChecks(t *testing.T) {
	d, f, lin := importDeps(t)
	f.files[".config/flocc/linear.key"] = "lin_api_k\n"
	f.files[".config/flocc/gh-agent.token"] = "github_pat_agent\n"
	f.files[".config/flocc/gh-publish.token"] = "github_pat_weak\n"
	if code := run(context.Background(), d, []string{"host", "import", "-H", "h1"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	st := d.store()
	if v, _ := st.Get(keys.Linear); v != "lin_api_k" {
		t.Fatalf("linear %q", v)
	}
	if v, _ := st.Get(keys.Agent); v != "github_pat_agent" {
		t.Fatalf("agent %q", v)
	}
	if v, _ := st.Get(keys.Publish); v != "" {
		t.Fatalf("a publish token that cannot push was taken: %q", v)
	}
	if lin.updates != 1 {
		t.Fatalf("the Linear key's write probe ran %d times", lin.updates)
	}
	out := d.out.(*bytes.Buffer).String()
	if !strings.Contains(out, "Took the GitHub agent token (read-only) from h1") || !strings.Contains(out, "o/a: the token cannot push") {
		t.Fatal(out)
	}
	for _, secret := range []string{"lin_api_k", "github_pat_agent", "github_pat_weak"} {
		if strings.Contains(out, secret) {
			t.Errorf("%s printed", secret)
		}
	}
	// Each key file is read without a terminal, so a forced pty cannot echo it.
	for _, c := range f.runs {
		if !slices.Contains(c.Args, "-T") {
			t.Errorf("no -T: %s", c)
		}
	}
}
```

Add `"bytes"` and `"errors"` to the test file's imports.

- [ ] **Step 4: Run them to see them fail**

Run: `go test ./installer/internal/cli/`
Expected: FAIL to build: `undefined: hostAdd`, `undefined: hostFile`, `undefined: digestTitle`, and `d.prompt = p` does not compile until `prompter` exists.

- [ ] **Step 5: Implement**

`installer/internal/cli/prompt.go`:

```go
package cli

import (
	"charm.land/huh/v2"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/linearapi"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// prompter is everything the desktop asks the owner.
type prompter interface {
	keys.Prompt
	Repos(a *answers.Answers, found []answers.Repo, defaults func(url string) (base string, draft bool)) error
	Team(teams []linearapi.Team) (linearapi.Team, error)
	Note(title, body string)
}

type huhPrompt struct{ keys.Huh }

func (huhPrompt) Repos(a *answers.Answers, found []answers.Repo, defaults func(string) (string, bool)) error {
	return ui.RepoForm(a, found, defaults)
}

func (huhPrompt) Team(teams []linearapi.Team) (linearapi.Team, error) {
	opts := make([]huh.Option[int], len(teams))
	for i, t := range teams {
		opts[i] = huh.NewOption(t.Key+"  "+t.Name, i)
	}
	var i int
	err := huh.NewForm(huh.NewGroup(huh.NewSelect[int]().Title("Linear team for this host").Options(opts...).Value(&i))).Run()
	return teams[i], err
}

func (huhPrompt) Note(title, body string) { ui.NewHuh().Note(title, body) }
```

In `cli.go`, change the field to `prompt prompter` and in `Main` set `d.prompt = huhPrompt{}`.

In `installer/internal/cli/host.go`, replace `hostCmd` and add the rest (imports: `context`, `errors`, `fmt`, `os`, `path/filepath`, `slices`, `strings`, and the `answers`, `githubapi`, `hosts`, `keys`, `linearapi`, `ship`, `steps` packages):

```go
// hostCmd is flocc host <sub>: args is the line after "host", name the -H value.
func hostCmd(ctx context.Context, d *deps, name string, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(d.err, usage)
		return 2
	}
	switch args[0] {
	case "run":
		return hostRun(ctx, d, name, args[1:])
	case "add", "import":
		reach, rest, err := reachFlags(args[1:])
		if err == nil && args[0] == "add" {
			if len(rest) != 1 {
				err = errors.New("usage: flocc host add <name> [--ssh DEST | --daytona SANDBOX]")
			} else {
				err = addHost(ctx, d, rest[0], reach)
			}
		} else if err == nil {
			if name == "" && len(rest) == 1 {
				name = rest[0]
			}
			if name == "" {
				err = errors.New("usage: flocc host import -H <name> [--ssh DEST | --daytona SANDBOX]")
			} else {
				err = importHost(ctx, d, name, reach)
			}
		}
		if err != nil {
			fmt.Fprintf(d.err, "flocc host %s: %v\n", args[0], err)
			return 1
		}
		return 0
	}
	fmt.Fprint(d.err, usage)
	return 2
}

// reachFlags takes --ssh and --daytona wherever they sit: the host name comes first, and the
// flag package stops at it.
func reachFlags(args []string) (hosts.Host, []string, error) {
	var h hosts.Host
	var rest []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--ssh", "--daytona":
			if i+1 == len(args) {
				return h, nil, fmt.Errorf("%s needs a value", a)
			}
			i++
			if a == "--ssh" {
				h.SSH = args[i]
			} else {
				h.Daytona = args[i]
			}
		default:
			rest = append(rest, a)
		}
	}
	if h.SSH != "" && h.Daytona != "" {
		return h, nil, errors.New("give --ssh or --daytona, not both")
	}
	return h, rest, nil
}

func hostFile(d *deps, name string) string {
	return filepath.Join(hosts.Dir(d.home), name+".toml")
}

// hostAdd is flocc host add <name> [--ssh DEST]; Part 3's host create calls it.
func hostAdd(ctx context.Context, d *deps, name, sshDest string) error {
	return addHost(ctx, d, name, hosts.Host{SSH: sshDest})
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
	return h, err
}

const digestTitle = "flocc digest"

const digestBody = "flocc hosts post their daily digest here: tier 3 reports and token warnings. Pin this issue."

func addHost(ctx context.Context, d *deps, name string, reach hosts.Host) error {
	h, err := loadOrNew(d, name, reach)
	if err != nil {
		return err
	}
	if h.Hostname == "" {
		h.Hostname = name
	}
	// The repos and teams come from what these two keys can see, so they are asked for first.
	e := keysEnv(d)
	if err := keys.Fill(ctx, e, mapped(listHosts(d)), []keys.Slot{keys.Linear, keys.Agent}); err != nil {
		return err
	}
	st := d.store()
	lk, err := st.Get(keys.Linear)
	if err != nil {
		return err
	}
	ag, err := st.Get(keys.Agent)
	if err != nil {
		return err
	}
	gh := &githubapi.Client{Base: d.githubAPI, Token: ag, HTTP: d.http}
	lin := &linearapi.Client{URL: d.linearAPI, Key: lk, HTTP: d.http}
	if err := pickRepos(ctx, d, gh, &h); err != nil {
		return err
	}
	team, err := pickTeam(ctx, d, lin, &h)
	if err != nil {
		return err
	}
	if err := ensureDigest(ctx, d, lin, team, &h); err != nil {
		return err
	}
	if err := ensureLabels(ctx, d, lin, team, h); err != nil {
		return err
	}
	if err := hosts.Save(hosts.Dir(d.home), h); err != nil {
		return err
	}
	fmt.Fprintf(d.out, "Saved %s.\n", hostFile(d, name))
	// Asked for after the save, so the publish token's page lists the repos just mapped.
	m := mapped(listHosts(d))
	if err := keys.Fill(ctx, e, m, []keys.Slot{keys.Publish, keys.Ntfy}); err != nil {
		return fmt.Errorf("the host is saved; %w", err)
	}
	if problems := keys.Check(ctx, e, m); len(problems) > 0 {
		return fmt.Errorf("the host is saved, but the keys do not cover it yet:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

func pickRepos(ctx context.Context, d *deps, gh *githubapi.Client, h *hosts.Host) error {
	raw, err := gh.ListRepos(ctx)
	if err != nil {
		return err
	}
	repos, maybeEmpty, dropped, err := steps.ParseRepos(raw)
	if err != nil {
		return err
	}
	if len(dropped) > 0 {
		d.prompt.Note("Some owners are not listed",
			"Not listed: repos of "+strings.Join(dropped, ", ")+", which the agent token can see only publicly. Add any you want by URL.")
	}
	found, skipped := steps.MergeRepos(repos, h.Repos)
	if len(skipped) > 0 {
		d.prompt.Note("Skipped repos whose name another repo already has", strings.Join(skipped, "\n"))
	}
	// Left unticked unless already mapped: cloning an empty repo fails.
	for _, u := range maybeEmpty {
		if !slices.ContainsFunc(h.Repos, func(r answers.Repo) bool { return answers.SameURL(r.URL, u) }) {
			h.ExcludedRepos = answers.Exclude(h.ExcludedRepos, u)
		}
	}
	defaults := func(u string) (string, bool) {
		r, err := gh.Repo(ctx, strings.TrimPrefix(answers.NormalizeURL(u), "https://github.com/"))
		if err != nil || r.DefaultBranch == "" {
			return "main", true
		}
		// Private repos of a GitHub Free organization cannot have draft PRs.
		return r.DefaultBranch, !r.Private
	}
	return d.prompt.Repos(&h.Answers, found, defaults)
}

func pickTeam(ctx context.Context, d *deps, lin *linearapi.Client, h *hosts.Host) (linearapi.Team, error) {
	v, err := lin.Viewer(ctx)
	if err != nil {
		return linearapi.Team{}, err
	}
	if i := slices.IndexFunc(v.Teams, func(t linearapi.Team) bool { return t.Key == h.TeamKey }); i >= 0 {
		return v.Teams[i], nil
	}
	var t linearapi.Team
	switch len(v.Teams) {
	case 0:
		return t, errors.New("the Linear key sees no team")
	case 1:
		t = v.Teams[0]
	default:
		if t, err = d.prompt.Team(v.Teams); err != nil {
			return t, err
		}
	}
	h.TeamKey = t.Key
	return t, nil
}

// openIssue reports whether key is an open issue: false with no error when Linear has no such
// issue or it is closed. Any other error comes back, so a blip at Linear never replaces a working
// digest: an imported one is not titled "flocc digest", and falling through would make a second.
func openIssue(ctx context.Context, lin *linearapi.Client, key string) (bool, error) {
	is, err := lin.Issue(ctx, key)
	if errors.Is(err, linearapi.ErrNotFound) {
		return false, nil
	}
	return err == nil && is.Open(), err
}

// ensureDigest keeps an open digest issue, else takes the open one another host on the team
// uses, else finds or creates the team's open "flocc digest".
func ensureDigest(ctx context.Context, d *deps, lin *linearapi.Client, team linearapi.Team, h *hosts.Host) error {
	if h.DigestIssue != "" {
		if ok, err := openIssue(ctx, lin, h.DigestIssue); err != nil || ok {
			return err
		}
	}
	for _, o := range listHosts(d) {
		if o.Name == h.Name || o.TeamKey != team.Key || o.DigestIssue == "" || o.DigestIssue == h.DigestIssue {
			continue
		}
		ok, err := openIssue(ctx, lin, o.DigestIssue)
		if err != nil {
			return err
		}
		if ok {
			h.DigestIssue = o.DigestIssue
			return nil
		}
	}
	is, ok, err := lin.FindOpenIssue(ctx, team.Key, digestTitle)
	if err != nil {
		return err
	}
	if !ok {
		if is, err = lin.CreateIssue(ctx, team.ID, digestTitle, digestBody); err != nil {
			return err
		}
		fmt.Fprintf(d.out, "Created %s, the digest issue; pin it in Linear.\n", is.Key)
	}
	h.DigestIssue = is.Key
	return nil
}

func ensureLabels(ctx context.Context, d *deps, lin *linearapi.Client, team linearapi.Team, h hosts.Host) error {
	names := []string{"claude", "escalation", "needs-decision"}
	for _, r := range h.Repos {
		names = append(names, r.Name)
	}
	missing, err := lin.MissingLabels(ctx, team.Key, names)
	if err != nil {
		return err
	}
	for _, n := range missing {
		if err := lin.CreateLabel(ctx, team.ID, n); err != nil {
			return err
		}
		fmt.Fprintf(d.out, "Created label %s in %s.\n", n, team.Key)
	}
	return nil
}

// importHost copies a host's saved answers into its host file: for a host shipped before host
// files held answers, and after an --interactive ship. The host's ntfy topic becomes the
// desktop's when the desktop has none, so a subscription the owner already has keeps working,
// and so does each key the desktop lacks: GitHub shows a token only once, so without this a
// host moved to the desktop store would need new ones.
func importHost(ctx context.Context, d *deps, name string, reach hosts.Host) error {
	h, err := loadOrNew(d, name, reach)
	if err != nil {
		return err
	}
	dest, err := d.resolve(ctx, h.Target())
	if err != nil {
		return err
	}
	res, err := ship.Capture(ctx, d.run, dest, []string{"cat", ".config/flocc/answers.toml"}, nil)
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("reading the host's answers: %s", strings.TrimSpace(res.Out()))
	}
	a, err := answers.Parse([]byte(res.Stdout))
	if err != nil {
		return err
	}
	st := d.store()
	if cur, err := st.Get(keys.Ntfy); err == nil && cur == "" && a.NtfyTopic != "" {
		if err := st.Set(keys.Ntfy, a.NtfyTopic); err != nil {
			return err
		}
		fmt.Fprintln(d.out, "Kept the host's ntfy topic as the desktop's.")
	}
	h.Answers = *a
	h.NtfyTopic, h.Secrets = "", nil
	if err := hosts.Save(hosts.Dir(d.home), h); err != nil {
		return err
	}
	fmt.Fprintf(d.out, "Imported %s's answers into %s.\n", name, hostFile(d, name))
	return adoptKeys(ctx, d, h, dest)
}

// adoptKeys reads each key the store lacks from the host's secret file as the service user and
// stores it once it passes the checks a paste gets. A key that fails them is left for flocc keys.
// The values reach neither argv nor output: Capture forbids a pty, and only the slot's title and
// the check's note are printed.
func adoptKeys(ctx context.Context, d *deps, h hosts.Host, dest ship.Dest) error {
	e := keysEnv(d)
	m := mapped(listHosts(d))
	for _, s := range []keys.Slot{keys.Linear, keys.Agent, keys.Publish} {
		cur, err := e.Store.Get(s)
		if err != nil {
			return err
		}
		if cur != "" {
			continue
		}
		res, err := ship.Capture(ctx, d.run, dest, []string{"cat", ".config/flocc/" + steps.SecretFile(string(s))}, nil)
		if err != nil {
			return err
		}
		if res.Code != 0 {
			continue // the host never had it; flocc keys asks for it
		}
		v, note, err := keys.Accept(ctx, e, m, s, res.Stdout)
		if err != nil {
			fmt.Fprintf(d.out, "Did not take the %s from %s:\n%v\nflocc keys asks for a new one.\n", s.Title(), h.Name, err)
			continue
		}
		if err := e.Store.Set(s, v); err != nil {
			return err
		}
		fmt.Fprintf(d.out, "Took the %s from %s (%s).\n", s.Title(), h.Name, note)
	}
	return nil
}
```

`host.go` keeps `hostRun` from Task 4.

- [ ] **Step 6: Run the tests to see them pass**

Run: `go test -race ./installer/... && go vet ./...`
Expected: `ok` for every package.

- [ ] **Step 7: Commit**

```bash
git add installer/internal/cli installer/internal/ui/huh.go installer/internal/steps/secrets.go installer/internal/steps/service_test.go
git commit -s -m "flocc host add and import: a host file from what the keys can see, asking for a missing key on the spot"
```

---

### Task 10: `flocc ship` and `flocc login`

**Depends on:** Task 9.

`flocc ship -H <name>` asks for any key the store still lacks, builds the answers document from the host file and the stored keys, and hands it to Part 2a's `ship.Ship`, which sends it on stdin. `Ship` returns the host's summary without printing it; the desktop sets the summary's `Target` to the host name, so its remedy line reads `flocc login -H flocc-nimble` and not `-H root@flocc-nimble`, and prints it. `--interactive` runs the host's own forms over `ssh -t` and then copies the host's answers back into the host file. `flocc login -H <name>` runs Part 2a's `ship.Login`, which prints the host's summary itself. The remedy lines the host and the installer print change from `just ship`/`just login <target>` to the `flocc` commands, and the host's Config step stops sending an ntfy test push on every non-interactive ship, since `flocc keys` confirmed the topic once.

**Files:**
- Create: `installer/internal/cli/ship.go`, `installer/internal/cli/ship_test.go`
- Modify: `installer/internal/cli/cli.go` (`deps`, `Main`, `run`)
- Modify: `installer/internal/ship/ship.go` (delete `Main` and `openAnswers`, and with `Main` the `--stage-only` and `--answers FILE` flags), `installer/internal/ship/login.go` (delete `LoginMain`, add `ValidEmail`), `installer/internal/ship/ship_test.go` (delete `TestMainRefusesInteractiveWithAnswers` and `TestOpenAnswersReadsStdinForDashAndAFileOtherwise`), `installer/internal/ship/login_test.go` (drop the `LoginMain` lines from `TestLoginRefusesAnUnsafeTargetAndEmail`; add `TestValidEmail`)
- Modify: `installer/internal/steps/config.go` (test push only when interactive), `installer/internal/steps/config_test.go`
- Modify (remedy lines): `installer/internal/steps/run.go`, `installer/internal/steps/claude.go`, `installer/internal/steps/summary.go`, `installer/internal/steps/run_test.go`, `installer/internal/steps/summary_test.go`, `installer/cmd/flocc-setup/main.go`, `src/flocc/tokens.py`, `tests/test_tokens.py`, `tests/test_doctor.py`, `deploy/agent-plugins.txt`
- Modify: `justfile` (`ship`, `login`)

**Interfaces:**
- Consumes: Part 2a (`ship.Ship(ctx context.Context, o ship.Options, answers io.Reader) (ship.Summary, error)`, `ship.Options{Target string; Interactive bool; Out io.Writer}`, `ship.Summary` with its `Target` field and `Print(w io.Writer)`, `ship.Login(ctx context.Context, o ship.LoginOptions) error`, `ship.LoginOptions{Target, Email string; Out io.Writer}`, `emailRE`, `answers.Parse`, `steps.Env.SkipInteractive`, `testEnv`); Task 3 (`hosts.Host`, `hosts.Save`, `hosts.Load`); Task 4 (`pickHost`, `listHosts`, `hostDeps`, `fakeSSH`); Task 8 (`deps.store`, `keys.Fill`, `keys.Slots`, `keysEnv`, `mapped`); Task 9 (`importHost`, `hostScript`, `writeSecrets`, `allKeys`, `fakeGitHub`).
- Produces: `shipHost(ctx context.Context, d *deps, name string) error` and `loginHost(ctx context.Context, d *deps, name string) error` (Part 3 calls both); `shipWith(ctx, d *deps, name string, interactive bool) error`; `loginWith(ctx, d *deps, name, email string) error`; `answersDoc(st keys.Store, h hosts.Host) ([]byte, error)`; `ship.ValidEmail(s string) bool`; `deps` gains `repoRoot func() (string, error)`, `doShip func(context.Context, ship.Options, io.Reader) (ship.Summary, error)`, `doLogin func(context.Context, ship.LoginOptions) error`.

- [ ] **Step 1: Write the failing tests**

`installer/internal/cli/ship_test.go`:

```go
package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/ship"
)

// shipCall is what the fake Ship got: the answers document is read, as Ship reads it.
type shipCall struct {
	o   ship.Options
	doc []byte
	noAnswers bool // the answers reader was nil
}

func shipDeps(t *testing.T, secrets string) (*deps, *fakeSSH, *[]shipCall) {
	t.Helper()
	d, f, _, _ := hostDeps(t, "a")
	writeSecrets(t, d.home, secrets)
	d.store = func() keys.Store { return keys.Open(d.home, io.Discard) }
	d.http, d.githubAPI, d.open = http.DefaultClient, fakeGitHub(t).URL, func(string) {}
	d.prompt = &hostScript{}
	h := hosts.Host{Name: "a", SSH: "root@a", Answers: answers.Answers{TeamKey: "NF", DigestIssue: "NF-1",
		Repos: []answers.Repo{{Name: "a", URL: "https://github.com/o/a", BaseBranch: "main", Check: "just check"}}}}
	if err := hosts.Save(hosts.Dir(d.home), h); err != nil {
		t.Fatal(err)
	}
	var calls []shipCall
	d.repoRoot = func() (string, error) { return "/repo", nil }
	d.doShip = func(_ context.Context, o ship.Options, in io.Reader) (ship.Summary, error) {
		c := shipCall{o: o, noAnswers: in == nil}
		if in != nil {
			c.doc, _ = io.ReadAll(in)
		}
		calls = append(calls, c)
		return ship.Summary{Version: "flocc 0.5.0", Login: "none", LoginPending: true}, nil
	}
	return d, f, &calls
}

func TestShipSendsTheHostFileAndTheKeysOnStdinAndPrintsTheSummary(t *testing.T) {
	d, _, calls := shipDeps(t, allKeys)
	if code := run(context.Background(), d, []string{"ship", "-H", "a"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	c := (*calls)[0]
	if c.o.Target != "root@a" || c.o.Interactive || c.o.Out != d.out || c.noAnswers {
		t.Fatalf("%+v", c)
	}
	a, err := answers.Parse(c.doc)
	if err != nil || a.TeamKey != "NF" || a.NtfyTopic != "flocc-t" || a.Secrets.LinearKey != "lin_api_k" ||
		a.Secrets.AgentToken != "github_pat_agent" || a.Secrets.PublishToken != "github_pat_publish" {
		t.Fatalf("%+v %v", a, err)
	}
	// The summary names the host, not its ssh destination, in the command it asks for.
	if out := d.out.(*bytes.Buffer).String(); !strings.Contains(out, "flocc 0.5.0") || !strings.Contains(out, "flocc login -H a") ||
		strings.Contains(out, "root@a") {
		t.Fatal(out)
	}
}

func TestShipAsksForAMissingKeyThenShips(t *testing.T) {
	d, _, calls := shipDeps(t, strings.Replace(allKeys, "publish_token = 'github_pat_publish'\n", "", 1))
	d.prompt = &hostScript{secrets: []string{"github_pat_publish"}}
	if err := shipHost(context.Background(), d, "a"); err != nil {
		t.Fatal(err)
	}
	a, _ := answers.Parse((*calls)[0].doc)
	if a == nil || a.Secrets.PublishToken != "github_pat_publish" {
		t.Fatalf("%+v", a)
	}
}

func TestShipStopsBeforeShippingWhenAKeyIsNotGivenOrTheHostHasNoAnswers(t *testing.T) {
	d, _, calls := shipDeps(t, strings.Replace(allKeys, "publish_token = 'github_pat_publish'\n", "", 1))
	if err := shipHost(context.Background(), d, "a"); err == nil || !strings.Contains(err.Error(), "empty paste") {
		t.Fatal(err)
	}
	hosts.Save(hosts.Dir(d.home), hosts.Host{Name: "b", SSH: "root@b"})
	writeSecrets(t, d.home, allKeys)
	if err := shipHost(context.Background(), d, "b"); err == nil || !strings.Contains(err.Error(), "flocc host add b") {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("shipped anyway: %+v", *calls)
	}
}

func TestInteractiveShipSendsNoAnswersAndCopiesTheHostsBack(t *testing.T) {
	d, f, calls := shipDeps(t, allKeys)
	f.out["root@a"] = "team_key = \"NF\"\ndigest_issue = \"NF-9\"\n\n[[repos]]\nname = \"a\"\nurl = \"https://github.com/o/a\"\nbase_branch = \"main\"\ncheck = \"just check\"\n"
	if code := run(context.Background(), d, []string{"ship", "-H", "a", "--interactive"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if c := (*calls)[0]; !c.o.Interactive || !c.noAnswers {
		t.Fatalf("%+v", c)
	}
	if h, _ := hosts.Load(hosts.Dir(d.home), "a"); h.DigestIssue != "NF-9" {
		t.Fatalf("%+v", h)
	}
}

func TestLoginRunsOnTheChosenHost(t *testing.T) {
	d, _, _ := shipDeps(t, allKeys)
	var got ship.LoginOptions
	d.doLogin = func(_ context.Context, o ship.LoginOptions) error {
		got = o
		return nil
	}
	if code := run(context.Background(), d, []string{"login", "-H", "a", "--email", "o@example.test"}); code != 0 ||
		got.Target != "root@a" || got.Email != "o@example.test" || got.Out != d.out {
		t.Fatalf("exit %d %+v", code, got)
	}
	if code := run(context.Background(), d, []string{"login", "-H", "a", "--email", "a@b;id"}); code != 2 {
		t.Fatalf("an unsafe email: exit %d", code)
	}
}
```

Append to `installer/internal/steps/config_test.go`:

```go
// gitOK accepts git, whose arguments hold the test's temp root.
type gitOK struct{ *host.Fake }

func (g gitOK) Run(ctx context.Context, c host.Cmd) (host.Result, error) {
	if c.Name == "git" {
		g.Fake.Calls = append(g.Fake.Calls, c.String())
		return host.Result{}, nil
	}
	return g.Fake.Run(ctx, c)
}

// The desktop's flocc keys confirmed the topic; a ship from it must not push again.
func TestConfigApplyWithAnswersOnStdinSendsNoTestPush(t *testing.T) {
	f := host.NewFake()
	e := testEnv(t, f)
	e.Run = gitOK{f}
	e.SkipInteractive = true
	e.Answers.TeamKey, e.Answers.DigestIssue, e.Answers.NtfyTopic = "NF", "NF-1", "flocc-t"
	e.Answers.Repos = []answers.Repo{{Name: "r", URL: "https://github.com/o/r", BaseBranch: "main", Check: "just check"}}
	e.UI = &ui.Scripted{A: e.Answers, Out: io.Discard}
	os.MkdirAll(e.HomePath("src/r/.git"), 0o755)
	if err := (Config{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "curl") {
			t.Fatalf("test push sent: %s", c)
		}
	}
}
```

In `installer/internal/ship/login_test.go`, delete the `LoginMain` call and its check at the end of Part 2a's `TestLoginRefusesAnUnsafeTargetAndEmail` (the `Login` cases stay), and add:

```go
func TestValidEmail(t *testing.T) {
	if !ValidEmail("o@example.test") || ValidEmail("a@b;id") || ValidEmail("$(id)@x") {
		t.Fatal("wrong verdict")
	}
}
```

and delete `TestMainRefusesInteractiveWithAnswers` and `TestOpenAnswersReadsStdinForDashAndAFileOtherwise` from `ship_test.go`.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/internal/cli/ ./installer/internal/steps/ ./installer/internal/ship/`
Expected: FAIL to build (`undefined: shipHost`, `unknown field repoRoot`, `undefined: ValidEmail`); once it builds, `TestConfigApplyWithAnswersOnStdinSendsNoTestPush` fails on the `curl` call.

- [ ] **Step 3: Implement the verbs**

`installer/internal/cli/ship.go`:

```go
package cli

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/pelletier/go-toml/v2"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/ship"
)

func shipCmd(ctx context.Context, d *deps, name string, args []string) int {
	fs := flag.NewFlagSet("flocc ship", flag.ContinueOnError)
	fs.SetOutput(d.err)
	interactive := fs.Bool("interactive", false, "answer the host's own forms over ssh -t, then copy its answers into the host file")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(d.err, "usage: flocc ship -H <name> [--interactive]")
		return 2
	}
	return ship.ExitCode(shipWith(ctx, d, name, *interactive), "ship")
}

// shipHost ships without a terminal on the host; Part 3's host create calls it. A run whose only
// open item is the Claude login exits 0 on the host, so it returns nil.
func shipHost(ctx context.Context, d *deps, name string) error { return shipWith(ctx, d, name, false) }

func shipWith(ctx context.Context, d *deps, name string, interactive bool) error {
	h, err := pickHost(ctx, d, name, nil)
	if err != nil {
		return err
	}
	// Ship reads the root itself; asked here first for the friendlier error.
	if _, err := d.repoRoot(); err != nil {
		return fmt.Errorf("flocc ship ships the flocc checkout it runs in; cd into it: %w", err)
	}
	// An io.Reader holding a nil *bytes.Reader is not nil, and Ship refuses answers with --interactive.
	var in io.Reader
	if !interactive {
		if h.TeamKey == "" || h.DigestIssue == "" || len(h.Repos) == 0 {
			return fmt.Errorf("%s has no answers yet; run flocc host add %s", h.Name, h.Name)
		}
		if err := keys.Fill(ctx, keysEnv(d), mapped(listHosts(d)), keys.Slots); err != nil {
			return err
		}
		doc, err := answersDoc(d.store(), h)
		if err != nil {
			return err
		}
		in = bytes.NewReader(doc)
	}
	s, err := d.doShip(ctx, ship.Options{Target: h.Target(), Interactive: interactive, Out: d.out}, in)
	// The host cannot know the name the desktop calls it, which the remedy lines use.
	s.Target = h.Name
	s.Print(d.out)
	if err != nil {
		return err
	}
	if interactive {
		return importHost(ctx, d, h.Name, hosts.Host{})
	}
	return nil
}

// answersDoc is what ship sends on stdin: the host file's answers with the stored keys. Part
// 2a's Ship redacts the document's secrets from its log.
func answersDoc(st keys.Store, h hosts.Host) ([]byte, error) {
	v := map[keys.Slot]string{}
	for _, s := range keys.Slots {
		got, err := st.Get(s)
		if err != nil {
			return nil, err
		}
		if got == "" {
			return nil, fmt.Errorf("the %s is missing", s.Title())
		}
		v[s] = got
	}
	a := h.Answers
	a.NtfyTopic = v[keys.Ntfy]
	a.Secrets = &answers.Secrets{LinearKey: v[keys.Linear], AgentToken: v[keys.Agent], PublishToken: v[keys.Publish]}
	return toml.Marshal(a)
}

func loginCmd(ctx context.Context, d *deps, name string, args []string) int {
	fs := flag.NewFlagSet("flocc login", flag.ContinueOnError)
	fs.SetOutput(d.err)
	email := fs.String("email", "", "the claude.ai account to sign in as")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(d.err, "usage: flocc login -H <name> [--email ADDR]")
		return 2
	}
	// The address goes into the remote command line, which sudo -i hands to a login shell.
	if *email != "" && !ship.ValidEmail(*email) {
		fmt.Fprintf(d.err, "flocc login: %q is not an email address\n", *email)
		return 2
	}
	return ship.ExitCode(loginWith(ctx, d, name, *email), "login")
}

// loginHost is flocc login -H <name> with no --email; Part 3's host create calls it.
func loginHost(ctx context.Context, d *deps, name string) error { return loginWith(ctx, d, name, "") }

// loginWith runs Part 2a's Login, which opens the sign-in URL here and prints the host's summary.
func loginWith(ctx context.Context, d *deps, name, email string) error {
	h, err := pickHost(ctx, d, name, nil)
	if err != nil {
		return err
	}
	return d.doLogin(ctx, ship.LoginOptions{Target: h.Target(), Email: email, Out: d.out})
}
```

In `cli.go`, add to `deps`:

```go
	repoRoot func() (string, error)
	doShip   func(context.Context, ship.Options, io.Reader) (ship.Summary, error)
	doLogin  func(context.Context, ship.LoginOptions) error
```

set them in `Main` (add `os/exec` and `strings` to the imports):

```go
	d.repoRoot = func() (string, error) {
		out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		return strings.TrimSpace(string(out)), err
	}
	d.doShip, d.doLogin = ship.Ship, ship.Login
```

and in `run` replace the `ship` and `login` cases with `case "ship": return shipCmd(ctx, d, name, rest[1:])` and `case "login": return loginCmd(ctx, d, name, rest[1:])`.

Neither path goes through `d.resolve`: `Ship` and `Login` resolve the target in Part 2a's `connect`, which calls `Resolve` since Task 4, so they share the SSH connection too. Their runner's log redacts the Daytona token and, for `Ship`, the answers document's secrets (Part 2a Task 6).

In `installer/internal/ship/ship.go` delete `Main` and `openAnswers`, and in `login.go` delete `LoginMain` (the desktop's entry point is `cli` now, and `flocc-setup` stopped dispatching them in Task 1). `Main` took `--stage-only DIR` and `--answers FILE|-`; neither has a desktop form, and the changelog says so (Task 14). Then add to `login.go`:

```go
func ValidEmail(s string) bool { return emailRE.MatchString(s) }
```

Drop imports that become unused (`flag`, `net/http`, `time`, `path/filepath` where nothing else uses them; `go vet` names them).

- [ ] **Step 4: Stop the per-ship test push**

In `installer/internal/steps/config.go`, `Config.Apply`, wrap the ntfy block (the `curl` call, the `Confirm` and the "did not arrive" error) in:

```go
	// With answers on stdin the desktop's flocc keys already saw this topic's test push arrive.
	if !e.SkipInteractive {
		ntfy := "https://ntfy.sh/" + a.NtfyTopic
		// … the existing curl, Confirm and error, unchanged
	}
```

- [ ] **Step 5: Point the remedy lines at flocc**

Run: `git grep -n -E "just (ship|login)" -- installer src tests deploy`

Change each hit as follows (comments too), so the list is empty afterwards:

| File | Old | New |
|---|---|---|
| `installer/internal/steps/run.go` | `fix them and run just ship again` | `fix them and run flocc ship -H <host> again` |
| `installer/internal/steps/run.go` | `until just login runs` | `until flocc login runs` |
| `installer/internal/steps/claude.go` | `leaves it to just login` / `; just login does it after ship` | `leaves it to flocc login` / `; flocc login -H <host> does it after ship` |
| `installer/internal/steps/summary.go` | `"; on the desktop run: just login " + cmp.Or(target, "<host>")`, `which just login fixes` | `"; on the desktop run: flocc login -H " + cmp.Or(target, "<host>")`, `which flocc login fixes` |
| `installer/internal/steps/summary_test.go` | `just login root@h`, `just login <host>` | `flocc login -H root@h`, `flocc login -H <host>` |
| `installer/internal/steps/run_test.go` | `on the desktop, run just login <ssh target of this host>` | `on the desktop, run flocc login -H <this host>` |
| `installer/cmd/flocc-setup/main.go` | `run just ship again to restart`, `just ship runs it with sudo`, `just ship stages`, `Run just login again to finish.`, `Run just ship again to continue.` | `run flocc ship -H <host> again to restart`, `flocc ship runs it with sudo`, `flocc ship stages`, `Run flocc login -H <host> again to finish.`, `Run flocc ship -H <host> again to continue.` |
| `src/flocc/tokens.py` | `LOGIN_REMEDY = "on the desktop, run just login <ssh target of this host>"`, `action = "On the desktop, run just login <ssh target of this host>"` | `"on the desktop, run flocc login -H <this host>"`, `"On the desktop, run flocc login -H <this host>"` |
| `tests/test_tokens.py` | `assert "just login" in esc.action` | `assert "flocc login -H" in esc.action` |
| `tests/test_doctor.py` | `"token-only (oauth_token); on the desktop, run just login"` | `"token-only (oauth_token); on the desktop, run flocc login -H"` |
| `deploy/agent-plugins.txt` | `` `just ship` installs only these `` | `` `flocc ship` installs only these `` |

The summary prints the host's name in place of `<host>` because `shipWith` sets `Summary.Target` to it on the desktop (Part 2a removed the host's `--target`). `flocc login`'s summary comes from Part 2a's `Login`, which leaves `Target` empty, so a login that is still pending afterwards prints `flocc login -H <host>`.

- [ ] **Step 6: Make the ship and login recipes wrappers**

In `justfile`:

```just
ship name *flags: build
    build/flocc ship -H {{name}} {{flags}}

login name *flags: build
    build/flocc login -H {{name}} {{flags}}
```

keeping their `[group]` and comment lines, with the comments rewritten as `# Set up or upgrade a host from its host file and the stored keys (--interactive: the host's own forms)` and `# Log the host in to Claude, accept Remote Control and start its timer (--email picks the account)`. In `daytona-start`, change `just ship daytona:{{name}}` to `just ship {{name}}`: `daytona:x` would now reach `-H` and fail the host-name check. The sandbox then needs a host file, so add to the recipe's comment "after flocc host add {{name}} --daytona {{name}}".

- [ ] **Step 7: Run everything**

Run: `just check && git grep -n -E "just (ship|login)" -- installer src tests deploy; echo "grep exit $?"`
Expected: `just check` passes; the grep prints nothing and `grep exit 1`.

- [ ] **Step 8: Commit**

```bash
git add installer src tests deploy justfile
git commit -s -m "flocc ship and flocc login from host files and stored keys; remedy lines name flocc"
```

---

### Task 11: `flocc keys rotate`

**Depends on:** Task 9. Runs in parallel with Tasks 10, 12 and 13.

`flocc keys rotate github` reopens both GitHub token pages in turn (an empty paste keeps the current token), checks and stores each new token, then writes it into every host's secret file. Each value travels on the SSH session's stdin, never in a command line, over `ssh -T` so a forced pty cannot echo it. Hosts it could not reach are listed and the exit status is 1. Running sessions read `GH_TOKEN` when they start, so the old token must stay valid until they end, and the command says so. `rotate linear` does the same for the Linear key, whose check includes the write probe on a digest issue (Task 7), so a read-only key never reaches a host. A rotate stopped partway (Ctrl-C at the second paste) still pushes the keys it stored, so the desktop and the hosts never hold different keys. The token expiries `flocc status -H all` shows come from each host's daily token check, so the command says the new expiry shows after that check.

**Files:**
- Modify: `installer/internal/keys/fill.go` (`Replace`), `installer/internal/keys/fill_test.go`
- Modify: `installer/internal/cli/keys.go` (`rotate`, `pushKeys`, the `rotate` case)
- Create: `installer/internal/cli/rotate_test.go`

**Interfaces:**
- Consumes: Task 8 (`askKey`, `keys.Env`, `keysEnv`, `mapped`, `keysCmd`, the `fixture`/`script` test helpers), Task 4 (`ship.Capture`, `listHosts`, `hostDeps`, `fakeSSH` with `stdins`), Task 9 (`steps.SecretFile`, `hostScript` with `secrets` and `err`, `writeSecrets`, `allKeys`, `fakeGitHub`).
- Produces: `keys.Replace(ctx, e Env, m Mapped, slots []Slot) ([]Slot, error)` (the slots it changed, also when it returns an error); `rotate(ctx, d *deps, which string) int`; `pushKeys(ctx, d *deps, h hosts.Host, st keys.Store, slots []keys.Slot) error`.

- [ ] **Step 1: Write the failing tests**

Append to `installer/internal/keys/fill_test.go`:

```go
func TestReplaceKeepsAKeyLeftEmptyAndStoresANewOne(t *testing.T) {
	f := newFixture(t, ghServer(t, userOK), nil, &script{secrets: []string{"github_pat_new", ""}})
	f.store.m[Agent], f.store.m[Publish] = "github_pat_old", "github_pat_pub"
	changed, err := Replace(context.Background(), f.env, Mapped{}, []Slot{Agent, Publish})
	if err != nil || len(changed) != 1 || changed[0] != Agent {
		t.Fatalf("%v %v", changed, err)
	}
	if f.store.m[Agent] != "github_pat_new" || f.store.m[Publish] != "github_pat_pub" {
		t.Fatalf("%v", f.store.m)
	}
}
```

`installer/internal/cli/rotate_test.go`:

```go
package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/keys"
)

func rotateDeps(t *testing.T, p *hostScript) (*deps, *fakeSSH) {
	t.Helper()
	d, f, _, _ := hostDeps(t, "a", "b")
	writeSecrets(t, d.home, allKeys)
	d.store = func() keys.Store { return keys.Open(d.home, io.Discard) }
	d.prompt = p
	d.open = func(string) {}
	d.http = http.DefaultClient
	d.githubAPI = fakeGitHub(t).URL
	f.out["root@a"] = ""
	return d, f
}

// pushedTo is the stdin of the push of file to dest, and whether there was one.
func pushedTo(f *fakeSSH, dest, file string) (string, bool) {
	for i, c := range f.runs {
		if c.Args[len(c.Args)-2] == dest && strings.Contains(c.String(), file) {
			return f.stdins[i], true
		}
	}
	return "", false
}

func TestRotatePushesTheNewTokenOnStdinAndNamesAnUnreachableHost(t *testing.T) {
	d, f := rotateDeps(t, &hostScript{secrets: []string{"github_pat_agent2", ""}})
	if code := run(context.Background(), d, []string{"keys", "rotate", "github"}); code != 1 {
		t.Fatalf("exit %d\n%s\n%s", code, d.out, d.err)
	}
	if v, _ := d.store().Get(keys.Agent); v != "github_pat_agent2" {
		t.Fatal(v)
	}
	for _, c := range f.runs {
		if strings.Contains(c.String(), "github_pat_agent2") {
			t.Fatalf("the token is in a command line: %s", c)
		}
		// With RequestTTY force in the owner's ssh config, a pty would echo the token.
		if !slices.Contains(c.Args, "-T") {
			t.Fatalf("no -T: %s", c)
		}
	}
	if in, ok := pushedTo(f, "root@a", "gh-agent.token"); !ok || in != "github_pat_agent2\n" {
		t.Fatalf("runs %v stdins %q", f.runs, f.stdins)
	}
	if _, ok := pushedTo(f, "root@a", "gh-publish.token"); ok {
		t.Fatal("the publish token was kept, so it must not be pushed")
	}
	out, errb := d.out.(*bytes.Buffer).String(), d.err.(*bytes.Buffer).String()
	if !strings.Contains(out, "Pushed to a") || !strings.Contains(out, "Do not revoke the old token") ||
		!strings.Contains(out, "daily token check") || !strings.Contains(errb, "b: ") {
		t.Fatalf("%s\n%s", out, errb)
	}
}

// A rotate stopped at the second prompt still pushes the token it stored, so the desktop and the
// hosts never hold different agent tokens.
func TestAStoppedRotatePushesWhatItStored(t *testing.T) {
	d, f := rotateDeps(t, &hostScript{secrets: []string{"github_pat_agent2"}, err: errors.New("interrupted")})
	if code := run(context.Background(), d, []string{"keys", "rotate", "github"}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if in, ok := pushedTo(f, "root@a", "gh-agent.token"); !ok || in != "github_pat_agent2\n" {
		t.Fatalf("runs %v", f.runs)
	}
	if !strings.Contains(d.err.(*bytes.Buffer).String(), "interrupted") {
		t.Fatal(d.err)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/internal/keys/ ./installer/internal/cli/`
Expected: FAIL to build: `undefined: Replace`; `rotate` reaches the usage text and exits 2.

- [ ] **Step 3: Implement**

Append to `installer/internal/keys/fill.go`:

```go
// Replace asks for a new value of each slot in turn, checked as Fill checks it, and returns the
// slots it changed, also with an error, so the caller can push what was stored. An empty paste
// keeps the current value.
func Replace(ctx context.Context, e Env, m Mapped, slots []Slot) ([]Slot, error) {
	var changed []Slot
	for _, s := range slots {
		stored, err := askKey(ctx, e, m, s, "Paste the new one; nothing shows while you paste. Leave it empty to keep the current one.")
		if err != nil {
			return changed, err
		}
		if stored {
			changed = append(changed, s)
		}
	}
	return changed, nil
}
```

In `installer/internal/cli/keys.go`, add the `rotate` case to `keysCmd`'s switch, before the usage fallback:

```go
	case len(args) == 2 && args[0] == "rotate":
		return rotate(ctx, d, args[1])
```

and add (with `ship` and `steps` added to the imports):

```go
func rotate(ctx context.Context, d *deps, which string) int {
	var slots []keys.Slot
	switch which {
	case "github":
		slots = []keys.Slot{keys.Agent, keys.Publish}
	case "linear":
		slots = []keys.Slot{keys.Linear}
	default:
		fmt.Fprintln(d.err, "usage: flocc keys rotate github|linear")
		return 2
	}
	all := listHosts(d)
	changed, err := keys.Replace(ctx, keysEnv(d), mapped(all), slots)
	code := 0
	if err != nil {
		// Pushed anyway below: what was stored must reach the hosts too.
		fmt.Fprintln(d.err, "flocc keys rotate:", err)
		code = 1
	}
	if len(changed) == 0 {
		if err == nil {
			fmt.Fprintln(d.out, "Nothing changed.")
		}
		return code
	}
	var failed []string
	for _, h := range all {
		if err := pushKeys(ctx, d, h, d.store(), changed); err != nil {
			failed = append(failed, h.Name+": "+err.Error())
			continue
		}
		fmt.Fprintf(d.out, "Pushed to %s.\n", h.Name)
	}
	if which == "github" {
		fmt.Fprintln(d.out, "Do not revoke the old token until the sessions running now end: each read GH_TOKEN when it started.")
		fmt.Fprintln(d.out, "flocc status -H all shows the new expiry after each host's next daily token check.")
	}
	if len(failed) > 0 {
		fmt.Fprintf(d.err, "Not pushed; once these are up, run flocc keys rotate %s again or flocc ship -H <host>:\n  %s\n", which, strings.Join(failed, "\n  "))
		return 1
	}
	return code
}

// pushKeys writes each changed key into the host's secret file as the service user. The value
// goes on stdin through Capture, which forbids a pty, and the file is written beside the old one
// and renamed over it, mode 600.
func pushKeys(ctx context.Context, d *deps, h hosts.Host, st keys.Store, slots []keys.Slot) error {
	dest, err := d.resolve(ctx, h.Target())
	if err != nil {
		return err
	}
	for _, s := range slots {
		v, err := st.Get(s)
		if err != nil {
			return err
		}
		f := ".config/flocc/" + steps.SecretFile(string(s))
		script := []string{"umask 077 && cat > " + f + ".new && mv " + f + ".new " + f}
		res, err := ship.Capture(ctx, d.run, dest, script, strings.NewReader(v+"\n"))
		if err != nil {
			return err
		}
		if res.Code != 0 {
			return fmt.Errorf("exit %d: %s", res.Code, strings.TrimSpace(res.Out()))
		}
	}
	return nil
}
```

Add `rotate github|linear` is already in `usage` (Task 1).

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test -race ./installer/... && go vet ./...`
Expected: `ok` for every package.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/keys installer/internal/cli
git commit -s -m "flocc keys rotate: replace a key and push it to every host on stdin"
```

---

### Task 12: The Claude login and token expiries in status

**Depends on:** Tasks 2, 4 and 10.

`flocc status -H all` adds a LOGIN and a TOKENS column per host. The host reports both in `status --json`: the login from `tokens.claude_login` (Part 2a), and each GitHub token's expiry as the tick's daily token check last saw it, kept in the ledger's meta table so `status` still calls neither Linear nor GitHub. The single-host `flocc status` prints them too.

**Files:**
- Modify: `src/flocc/tokens.py` (`check_tokens` records expiries), `src/flocc/status.py` (`collect`, `_read_ledger`), `src/flocc/cli.py` (`cmd_status`)
- Test: `tests/test_tokens.py`, `tests/test_status.py`
- Modify: `installer/internal/ship/fleet.go` (`Report`, `loginCell`, `tokensCell`, `render`), `installer/internal/ship/fleet_test.go`

**Interfaces:**
- Consumes: Part 2a (`tokens.claude_login(exe, run) -> str | None`, the `tick_lock` key in the report), Task 2 (`detect_mode`), Task 4 (the fleet view behind `flocc status -H all`), Task 10 (`tokens.py` as it changed there).
- Produces: ledger meta keys `token_expiry:agent` and `token_expiry:publish` holding a Unix time in whole seconds, `never` or `rejected`; `status --json` keys `claude_login` (`"full"`, `"token-only (<method>)"`, `"none"` or null when not asked) and `token_expiry` (`{"agent": "<value>", ...}`, only the tokens checked so far); `collect(..., claude: str | None = None)`; Go `Report.Now`, `Report.ClaudeLogin *string`, `Report.TokenExpiry map[string]string`.

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_tokens.py`:

```python
def test_check_tokens_records_each_expiry_for_status(rt):
    def expiry(token):
        if token == "p":
            raise TokenRejected("401")
        return NOW + 10 * DAY

    check_tokens(rt, expiry)
    assert ledger.meta_get(rt.con, "token_expiry:agent") == str(int(NOW + 10 * DAY))
    assert ledger.meta_get(rt.con, "token_expiry:publish") == "rejected"
    rt.con.execute("DELETE FROM meta WHERE key = 'token_check_date'")
    check_tokens(rt, lambda token: None)
    assert ledger.meta_get(rt.con, "token_expiry:agent") == "never"
```

In `tests/test_status.py`, add `import subprocess`; change `_report`'s `base` to also hold `"now": 0.0, "claude_login": None, "token_expiry": {}`; change every `lambda cfg, sessions: …` in `test_human_status_prints_tick_usage_and_disk` to `lambda cfg, sessions, **kw: …`; then append:

```python
def test_status_reports_the_claude_login_and_token_expiries(cfg):
    con = ledger.connect(cfg.ledger_path)
    ledger.meta_set(con, "token_expiry:agent", "1900000000")
    ledger.meta_set(con, "token_expiry:publish", "never")
    con.close()

    def run(argv, **kw):
        assert argv == ["/opt/claude", "auth", "status"]
        return subprocess.CompletedProcess(argv, 0, '{"loggedIn": true, "authMethod": "claude.ai"}', "")

    report = collect(cfg, FakeSessions(), now=1000.0, mode="loop", run=run, claude="/opt/claude")
    assert report["token_expiry"] == {"agent": "1900000000", "publish": "never"}
    assert report["claude_login"] == "full"
    assert collect(cfg, FakeSessions(), now=1000.0, mode="loop")["claude_login"] is None


def test_human_status_prints_the_login_and_expiries(cfg, tmp_path, monkeypatch, capsys):
    report = _report(now=1_800_000_000.0, claude_login="none",
                     token_expiry={"agent": str(1_800_000_000 + 5 * 86400), "publish": "rejected"})
    monkeypatch.setattr("flocc.status.collect", lambda cfg, sessions, **kw: report)
    assert main(["--config", str(tmp_path / "c.toml"), "status"]) == 0
    out = capsys.readouterr().out
    assert "claude login none" in out
    assert "github agent token expires 2027-01-20 (5 days)" in out
    assert "github publish token rejected by GitHub" in out
```

Append to `installer/internal/ship/fleet_test.go`:

```go
func TestLoginAndTokenCellsFlagWhatWillStopSessions(t *testing.T) {
	const red, yellow = "\x1b[31m", "\x1b[33m"
	for _, tc := range []struct {
		json, login, tokens, color string
	}{
		{`{"now":0,"claude_login":"full","token_expiry":{"agent":"2592000","publish":"never"}}`, "full", "agent 30d", ""},
		{`{"now":0,"claude_login":"none","token_expiry":{"agent":"864000","publish":"5000000"}}`, "none", "agent 10d", yellow},
		{`{"now":0,"claude_login":"full","token_expiry":{"agent":"never","publish":"86400"}}`, "full", "publish 1d", red},
		{`{"now":0,"claude_login":"full","token_expiry":{"agent":"rejected"}}`, "full", "agent rejected", red},
		{`{"now":0,"token_expiry":{"agent":"never","publish":"never"}}`, "-", "never", ""},
		{`{"now":0}`, "-", "-", ""},
	} {
		var rep Report
		if err := json.Unmarshal([]byte(tc.json), &rep); err != nil {
			t.Fatal(err)
		}
		if got := plainText(loginCell(&rep)); got != tc.login {
			t.Errorf("%s: login %q", tc.json, got)
		}
		cell := tokensCell(&rep)
		if got := plainText(cell); got != tc.tokens || tc.color != "" && !strings.Contains(cell, tc.color) {
			t.Errorf("%s: tokens %q %q", tc.json, got, cell)
		}
	}
	none := Report{ClaudeLogin: new(string)}
	*none.ClaudeLogin = "token-only (oauth_token)"
	if !strings.Contains(loginCell(&none), red) {
		t.Error("a login without Remote Control is not styled as bad")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_tokens.py tests/test_status.py; go test ./installer/internal/ship/`
Expected: Python: `assert None == '...'` for the meta keys and `KeyError: 'token_expiry'`; Go: `undefined: loginCell`, `undefined: tokensCell`.

- [ ] **Step 3: Implement the host side**

In `src/flocc/tokens.py`, `check_tokens`: in the `except TokenRejected:` branch add `ledger.meta_set(rt.con, f"token_expiry:{name}", "rejected")` before `_page(...)`, and right after the `except (OSError, ValueError)` block add:

```python
        # status reads this; it must not ask GitHub itself.
        ledger.meta_set(rt.con, f"token_expiry:{name}", "never" if expires is None else str(int(expires)))
```

In `src/flocc/status.py`, add `from flocc.tokens import claude_login`, replace `_live_claims` with:

```python
def _read_ledger(path: Path) -> tuple[list[ledger.Claim], dict[str, str], str | None]:
    # Not ledger.connect: that creates the file, switches the journal mode and runs the schema.
    # mode=ro still creates the -shm and -wal files beside a WAL ledger, but never changes the
    # ledger file itself.
    if not path.exists():
        return [], {}, None
    try:
        con = sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=2)
        con.row_factory = sqlite3.Row
        try:
            expiry = {
                name: value
                for name in ("agent", "publish")
                if (value := ledger.meta_get(con, f"token_expiry:{name}")) is not None
            }
            return ledger.live_claims(con), expiry, None
        finally:
            con.close()
    except sqlite3.Error as exc:
        return [], {}, str(exc)
```

and in `collect` add the parameter `claude: str | None = None`, replace `claims, ledger_error = _live_claims(cfg.ledger_path)` with `claims, expiry, ledger_error = _read_ledger(cfg.ledger_path)`, and add to the returned dict:

```python
        "claude_login": None if claude is None else claude_login(claude, run),
        "token_expiry": expiry,
```

In `src/flocc/cli.py`, add `import shutil`; in `cmd_status` call `collect(cfg, Sessions(), claude=shutil.which("claude"))`, and before the `last_tick` lines print:

```python
    if report["claude_login"]:
        print(f"  claude login {report['claude_login']}")
    for name, value in report["token_expiry"].items():
        print(f"  github {name} token {_expiry(value, report['now'])}")
```

with, beside `_age`:

```python
def _expiry(value: str, now: float) -> str:
    if value == "never":
        return "never expires"
    if value == "rejected":
        return "rejected by GitHub"
    at = float(value)
    day = time.strftime("%Y-%m-%d", time.gmtime(at))
    return f"expires {day} ({int((at - now) // 86400)} days)"
```

- [ ] **Step 4: Implement the columns**

In `installer/internal/ship/fleet.go`, add to `Report`:

```go
	Now         float64           `json:"now"`
	ClaudeLogin *string           `json:"claude_login"` // nil: not asked
	TokenExpiry map[string]string `json:"token_expiry"` // Unix seconds, "never" or "rejected"
```

add (with `"math"` and `"strconv"` imported):

```go
// loginCell is bad unless the login is full: only a claude.ai login opens Remote Control.
func loginCell(rep *Report) string {
	switch {
	case rep.ClaudeLogin == nil:
		return cell("-", dim)
	case *rep.ClaudeLogin == "full":
		return "full"
	}
	return cell(*rep.ClaudeLogin, bad)
}

// tokensCell shows the GitHub token that expires first, with the host's own clock from the
// report, at the tick's thresholds: a digest line under 14 days, a page under 3.
func tokensCell(rep *Report) string {
	if len(rep.TokenExpiry) == 0 {
		return cell("-", dim)
	}
	soonest, days := "", math.MaxFloat64
	for _, name := range []string{"agent", "publish"} {
		v, ok := rep.TokenExpiry[name]
		switch {
		case !ok || v == "never":
		case v == "rejected":
			return cell(name+" rejected", bad)
		default:
			at, err := strconv.ParseFloat(v, 64)
			if d := (at - rep.Now) / 86400; err == nil && d < days {
				soonest, days = name, d
			}
		}
	}
	if soonest == "" {
		return "never"
	}
	text := fmt.Sprintf("%s %dd", soonest, int(days))
	switch {
	case days < 3:
		return cell(text, bad)
	case days < 14:
		return cell(text, warn)
	}
	return text
}
```

In `render`, add `"LOGIN", "TOKENS"` to the host table's headers after `"DISK FREE"`, add `d, d` after the six `d` cells of the unreachable row, and add `loginCell(rep), tokensCell(rep)` after `diskCell(rep)` in the reachable row.

- [ ] **Step 5: Run the tests to see them pass**

Run: `just check`
Expected: passes.

- [ ] **Step 6: Commit**

```bash
git add src/flocc tests installer/internal/ship/fleet.go installer/internal/ship/fleet_test.go
git commit -s -m "Status reports the Claude login and token expiries; flocc status -H all shows them per host"
```

---

### Task 13: Remove the old-name readers

**Depends on:** Task 12 (and through it every task that edits `ship.go`, `fleet.go` or the ship tests).

The spec removes Part 1's two readers in 0.5.0: the `Linear-Agents:` trailer in branch recovery and the publisher's untrailed check, and the old bot prefix in the poller. Part 1 kept every old name in two files, `src/flocc/legacy.py` and `installer/internal/ship/legacy.go`; the second also holds the old-host refusal (`refuseLegacy`, `legacyUser`) and the old hosts file's path (`legacyHostsFile`), and the conversion itself is `hosts.ConvertOld`. All of it goes, with its callers and tests. After this, `tests/test_names.py` exempts no code file.

> **Precondition (controller):** the old-host refusal is what stops a ship onto a host still running the old service, so the commit of this task may be merged, but 0.5.0 is not tagged or released, until la-nimble is deleted (Part 1 Task 15). Check `incus list la-nimble` on zer0-day-nimble (empty) and `tailscale status | grep la-nimble` (nothing) before Task 14 Step 5.

**Files:**
- Delete: `src/flocc/legacy.py`, `installer/internal/ship/legacy.go`
- Modify: `src/flocc/worktree.py`, `src/flocc/publisher.py`, `src/flocc/poller.py`
- Modify: `tests/test_worktree.py`, `tests/test_publisher.py`, `tests/test_poller.py`, `tests/test_names.py`
- Modify: `installer/internal/ship/ship.go` (`shipTo`), `installer/internal/ship/fleet.go` (`FleetMain`)
- Modify: `installer/internal/hosts/hosts.go` (delete `ConvertOld`), `installer/internal/hosts/hosts_test.go`
- Modify: `installer/internal/ship/ship_test.go`

**Interfaces:**
- Consumes (Part 1): `flocc.legacy.TRAILER`, `flocc.legacy.BOT_PREFIX`, `flocc.config.TRAILER`, `recover_tests`, `legacyUser`, `refuseLegacy`, `legacyHostsFile`, `hosts.ConvertOld` and `FleetMain`'s call of it; Part 1's tests `test_commits_with_either_trailer_count_as_trailed`, `test_comments_with_the_old_bot_prefix_are_not_forwarded`, `test_recover_tests_takes_the_first_run_of_test_commits`, `TestShipRefusesAHostThatRunsTheOldService`, `TestConvertOldWritesOneFilePerHostOnce`.
- Produces: no reader of an old name anywhere; `COMPAT` gone from `tests/test_names.py`.

- [ ] **Step 1: Make the name test strict**

In `tests/test_names.py`, delete the `COMPAT` line and its comment, and set `EXEMPT = {"CHANGELOG.md", "tests/test_names.py"}`.

Run: `uv run pytest -q tests/test_names.py`
Expected: FAIL, listing the lines of `src/flocc/legacy.py` and `installer/internal/ship/legacy.go`.

- [ ] **Step 2: Remove the Python reader**

- `git rm src/flocc/legacy.py`.
- `src/flocc/worktree.py`: drop `from flocc import legacy`; in `recover_tests` replace the `wanted = {…}` set and its test with a single trailer:

```python
    wanted = f"{TRAILER}: {key} test"
    run = []
    # Only the run that starts the branch: the implementer can write a test trailer itself.
    for record in log.split("\x1e"):
        sha, _, trailers = record.strip().partition("\x1f")
        if not sha:
            continue
        if wanted not in {line.strip() for line in trailers.splitlines()}:
            break
        run.append(sha)
```

- `src/flocc/publisher.py`: drop `from flocc import legacy` and the `f"--grep=^{legacy.TRAILER}: ",` argument.
- `src/flocc/poller.py`: `from flocc import ledger, legacy` becomes `from flocc import ledger`, and `comment.body.startswith((BOT_PREFIX, legacy.BOT_PREFIX))` becomes `comment.body.startswith(BOT_PREFIX)`.
- `tests/test_publisher.py`: delete `test_commits_with_either_trailer_count_as_trailed` and the `from flocc import legacy` import.
- `tests/test_poller.py`: delete `test_comments_with_the_old_bot_prefix_are_not_forwarded`; `from flocc import ledger, legacy` becomes `from flocc import ledger`.
- `tests/test_worktree.py`: in `test_recover_tests_takes_the_first_run_of_test_commits` change `f"{legacy.TRAILER}: NF-3 test"` to `f"{TRAILER}: NF-3 test"`; drop `from flocc import legacy`.

- [ ] **Step 3: Remove the Go reader**

- `git rm installer/internal/ship/legacy.go`.
- `ship.go`, `shipTo`: delete the `if err := refuseLegacy(ctx, r, dest); err != nil { return err }` block.
- `fleet.go`, `FleetMain`: delete the `if msg, err := hosts.ConvertOld(filepath.Join(home, legacyHostsFile), hostsDir); … }` block (keep `hostsDir := hosts.Dir(home)`), and change the no-hosts note to `"no hosts: log in with daytona login, or run flocc host add <name>"`.
- `installer/internal/hosts/hosts.go`: delete `ConvertOld` and its comment; in `hosts_test.go` delete `TestConvertOldWritesOneFilePerHostOnce`.
- Delete every test that uses a removed name:

Run: `grep -rn -E "ConvertOld|legacyHostsFile|refuseLegacy|legacyUser" installer`
Expected before the deletions: hits in `ship_test.go` (`TestShipRefusesAHostThatRunsTheOldService`) and in what the two bullets above delete; delete that test function. Then the same grep prints nothing. Remove imports the deletions leave unused (`go vet` names them).

- [ ] **Step 4: Run everything**

Run: `just check && grep -rn -i -E "linear[-_ ]agents" src installer tests deploy; echo "grep exit $?"`
Expected: `just check` passes (with `test_no_tracked_file_uses_the_old_names` now strict); the grep prints nothing and `grep exit 1`.

- [ ] **Step 5: Commit**

```bash
git add -A src/flocc tests installer/internal/ship installer/internal/hosts
git commit -s -m "Stop reading la-nimble's trailer, bot prefix and hosts file

0.5.0 removes the readers Part 1 kept in legacy.py, legacy.go and hosts.ConvertOld, with the
old-host refusal. It is released only once la-nimble is deleted."
```

---

### Task 14: Docs, changelog and 0.5.0, then verification on real hosts

**Depends on:** every other task. Steps 1 to 4 are for an implementer; Step 5 waits on the precondition below; Step 6 is the controller's.

> **Precondition for Step 5 (controller):** la-nimble is deleted (Part 1 Task 15). 0.5.0 drops the refusal of a host that still runs the old service (Task 13), so it must not be tagged or released while la-nimble exists. Check `incus list la-nimble` on zer0-day-nimble prints no instance and `tailscale status` lists no la-nimble.

**Files:**
- Modify: `docs/vm-setup.md`, `README.md`, `CHANGELOG.md`, `pyproject.toml`, `plugin/.claude-plugin/plugin.json`, `uv.lock`
- Modify (controller, after merge): `docs/superpowers/specs/2026-10-20-flocc-design.md` ("Unconfirmed")

**Interfaces:**
- Consumes: every task above; Part 2a's `## Claude login` section and Part 1's `### Fleet view` and `## Hosts from before 0.3.0` sections of `docs/vm-setup.md`.
- Produces: release 0.5.0.

- [ ] **Step 1: Document the desktop command**

In `docs/vm-setup.md`, insert after the opening section (before `## Hosts`):

````markdown
## The desktop command

Everything on the desktop goes through `flocc`, built from this repo:

```bash
go install ./installer/cmd/flocc     # puts flocc in ~/go/bin
```

Run `flocc ship` from inside the flocc checkout: ship sends that
checkout's commit and builds the installer from it, so it needs the checkout and Go on the
desktop.

A new host takes these commands, in this order:

```bash
flocc keys                 # once per desktop: the Linear key, both GitHub tokens, the ntfy topic
flocc host add flocc-nimble
flocc ship -H flocc-nimble
flocc login -H flocc-nimble
```

`flocc keys` first is the usual order, but not required: `host add` and `ship` ask for any key
the desktop does not have yet, with the same prompt and checks.

A host that already runs flocc (set up before 0.5.0, or from another desktop) is imported first,
so the desktop takes its keys and its ntfy topic instead of making new ones:

```bash
flocc host import -H flocc-nimble   # its answers, plus each key the desktop lacks
flocc keys                          # asks only for what the host did not have
```

Run `flocc keys` before the import and it mints a new ntfy topic; the next ship then moves the
host off the topic your phone follows.

`flocc keys` asks for each key it does not have yet. For each it opens the page where the key is
made, with a QR code for a phone; GitHub's token page arrives with the name, description, owner,
permissions and the longest expiry (366 days) filled in, and the repos to tick are printed beside
it. Paste the key at the prompt: nothing shows while you paste, and a key is never taken as a
command-line argument, so none lands in shell history. Each paste is checked before it is stored,
first by its prefix (a Linear key in a GitHub slot is refused at once), then against the APIs
without changing anything: the Linear key must see the hosts' teams and be able to write (it
sets a digest issue's title to the title it already has, so make the key with Full access), the
agent token must read every mapped repo and must not be able to push, and the publish token must
be able to push and open pull requests. The agent token asks for Contents, Pull requests, Issues,
Commit statuses and Actions, all read-only; fine-grained tokens have no Checks permission, and
Actions read is what shows workflow runs and their logs. The publish token's page opens only after
the agent token passed. Last comes the ntfy topic: scan its QR code with the ntfy app and confirm
the test push. Keys live in the OS keyring (Secret Service, macOS Keychain); without one they go
in `~/.config/flocc/secrets.toml`, mode 600, and once that file exists it is used even after a
keyring appears. `flocc keys check` runs every check again.

When a stored token does not cover a repo, typically the publish token after `host add` mapped a
new repo, nothing needs pasting: on github.com/settings/personal-access-tokens open the token
("flocc agent" or "flocc publish"), Edit, add the repo under Repository access (the token's value
stays the same), then run `flocc keys check`.

`flocc host add <name> [--ssh DEST | --daytona SANDBOX]` writes
`~/.config/flocc/hosts/<name>.toml`, the only place a host's answers live: the repos the agent
token can read (offered ticked), the Linear team (picked for you when the key sees one), the
digest issue (another host's on the same team, else an open issue titled `flocc digest`, else a
new one to pin), and the labels `claude`, `escalation`, `needs-decision` and one per repo, created
when missing. It then checks the keys again against every host's repos and says what a token
lacks. The SSH destination defaults to `root@<name>`; `all` cannot be a host name. `flocc host
import -H <name>` builds the file from a host's own saved answers instead, and takes each key the
desktop lacks from the host's secret files, checked as a paste is; `flocc ship -H <name>
--interactive` runs the host's own forms and copies its answers back.

`flocc keys rotate github` reopens both token pages (an empty paste keeps a token), checks the new
ones, writes them into every host's secret files over SSH, and lists the hosts it could not
reach. Do not revoke the old token until the sessions running now have ended: each reads
`GH_TOKEN` when it starts. `flocc status -H all` shows the new expiry after each host's next
daily token check. `flocc keys rotate linear` does the same for the Linear key.

Any other command runs on a host as `flocc`, with the host's own grammar, so every remedy line a
host prints works on the desktop with `-H` added:

```bash
flocc -H flocc-nimble doctor
flocc release NF-12 --keep-worktree   # the host holding NF-12
flocc -H flocc-nimble logs -f
flocc -H flocc-nimble attach NF-12-review
flocc status -H all                   # every host, with its Claude login and token expiries
flocc host run -H flocc-nimble -- tmux -L flocc ls
```

Without `-H` the host is `$FLOCC_HOST`, then the only host, then, for a command naming an issue
key, the host whose `flocc status --json` holds that claim. `-H` is taken from anywhere before
`--`, so an argument that is literally `-H` goes after `--`. `-H all` runs only `status`, `doctor`
and `logs` (without `-f`). SSH, `flocc ship` and `flocc login` included, shares one connection per
host for ten minutes, so a Tailscale
check-mode approval covers a whole run. `ship`, `login`, `keys` and `host` are desktop commands;
the host's `flocc` refuses them. The `just` host recipes still work and run these commands; they
go in 0.6.0.
````

Then, in the same file:

- In Part 2a's `## Claude login` section, change `just login <target> [--email you@example.com]` to `flocc login -H <name> [--email you@example.com]`.
- In the paragraphs Part 2a wrote about `just ship <target> --answers <file>`: replace the first sentence with "`flocc ship -H <name>` runs without a terminal: it sends the host file's answers and the stored keys as one answers document on stdin over one SSH session, and nothing prompts; the ntfy topic's test push happened once, in `flocc keys`." and keep the rest of that paragraph; replace "`just ship <target> --interactive`" with "`flocc ship -H <name> --interactive`" (that mode still sends the test push and asks).
- In `### Fleet view`, replace `just fleet-status` with `flocc status -H all` (twice); replace the sentence introducing the host files, its `toml` example block and Part 1's paragraph about converting the old hosts file with "plus every host in `~/.config/flocc/hosts/` reached over SSH (`flocc host add` writes them; Daytona hosts come from the Daytona API)."; and after the sentence listing the colored cells add "LOGIN is the host's Claude login, red unless it is a full claude.ai login; TOKENS is the GitHub token that expires first, yellow under 14 days and red under 3 days or when GitHub rejected it."
- In `## Hosts from before 0.3.0`, delete the paragraph saying `just ship` refuses such a host (0.5.0 removed that check) and keep the paragraph about branches continuing on a new host.
- Replace every other `just ship <target>`, `just status`, `just logs`, `just attach` and `just host` in the file with the `flocc` command its recipe runs now.

Run: `grep -n -E "just (ship|login|status|logs|attach|host|fleet-status|doctor|tick|release)|qrencode|la-setup" docs/vm-setup.md`
Expected: no output.

- [ ] **Step 2: Point the README at the desktop command**

In `README.md`, section `## Current design`, after the sentence Part 1 added, add: "On the desktop, `flocc keys`, `flocc host add`, `flocc ship` and `flocc login` set a host up; `docs/vm-setup.md` explains them."

- [ ] **Step 3: Bump the version and add the changelog entry**

Set `version = "0.5.0"` in `pyproject.toml` and `"version": "0.5.0"` in `plugin/.claude-plugin/plugin.json`, then run `uv lock`. Add above `## 0.4.0` in `CHANGELOG.md`, with the date from `date +%F`:

```markdown
## 0.5.0 (<the date from date +%F>)

One `flocc` command on the desktop; keys are made once, checked when pasted and reused.

- `flocc` (`go install ./installer/cmd/flocc`) is the desktop command. Host commands run as
  `flocc -H <host> <command>`, with the host picked from `$FLOCC_HOST`, the only host or the issue
  named; `-H all` runs `status`, `doctor` and `logs`. The installer is its own binary,
  `flocc-setup`, built by ship for the host's architecture.
- `flocc keys` opens each key's page with a QR code, prefills GitHub's token page, reads pastes
  with echo off, checks each key by prefix and against the APIs without side effects, and keeps
  them in the OS keyring (else `~/.config/flocc/secrets.toml`, mode 600). The ntfy topic is shown
  as a QR code for the app and confirmed once. `flocc keys rotate github|linear` pushes new keys
  to every host.
- `flocc host add` writes `~/.config/flocc/hosts/<name>.toml` with the host's answers, creating
  the digest issue and the labels when missing; `host add` and `ship` ask for a key the desktop
  lacks on the spot. `flocc host import` reads the answers from a host and takes each key the
  desktop lacks from it, checked as a paste is. `flocc ship -H` and `flocc login -H` replace
  `just ship` and `just login`, whose recipes now run them; run `flocc ship` inside the flocc
  checkout. `flocc-setup ship` is gone, and with it `--stage-only DIR` and `--answers FILE`.
- The agent token asks for Actions and Commit statuses read instead of Checks, which
  fine-grained tokens do not have. The Linear key must be able to write; its check sets a
  digest issue's title to the title it has.
- `flocc status -H all` shows each host's Claude login and token expiries. The host CLI gains
  `logs` and `attach` and refuses the desktop commands. `all` can no longer be a host name.
- Ship no longer sends an ntfy test push on every run.
- `Linear-Agents:` trailers, `🤖 linear-agents:` comments and the old hosts file are no longer
  read, and ship no longer checks for a host from before 0.3.0.
```

The changelog is exempt from the name test, so the old names may appear there.

- [ ] **Step 4: Run the full check**

Run: `just check`
Expected: passes, including `test_the_plugin_and_the_package_share_one_version` and `test_no_tracked_file_uses_the_old_names`.

- [ ] **Step 5: Commit (after the precondition above holds)**

```bash
git add docs/vm-setup.md README.md CHANGELOG.md pyproject.toml plugin/.claude-plugin/plugin.json uv.lock
git commit -s -m "Release 0.5.0: the desktop flocc command and its keys"
```

- [ ] **Step 6 (manual, controller with the owner): verify on the desktop and flocc-nimble**

Not for an implementer: these use the owner's accounts and real hosts. Record each result in the PR description.

1. `go install ./installer/cmd/flocc && flocc; echo "exit $?"` prints the help and `exit 2`.
2. `flocc host import -H flocc-nimble` writes `~/.config/flocc/hosts/flocc-nimble.toml` with its repos, team and digest, keeps its ntfy topic, and prints "Took the …" for the Linear key and both GitHub tokens, with their expiries. Pass: no key value appears in the output; `flocc keys check` prints three `ok` lines and no FAIL (the Linear line proves the write probe passed on the digest issue).
3. `flocc keys`. Pass: it asks for nothing (every key came from the import); `history | tail -5` shows no key. Then open `https://linear.app/settings/account/security` and confirm it is the page where personal API keys are made; if it is not, change `keys.Page` and the help text in `steps/secrets.go` to what it is, in a follow-up commit. Open the agent token's prefilled URL (`flocc keys` prints it when the slot is empty; or build it from `keys.Page`) and confirm the page ticks Contents, Pull requests, Issues, Commit statuses and Actions as read-only, with 366 days and the name filled in.
4. The fresh-paste path, with new keys the owner makes for the rotation: `flocc keys rotate github` then `flocc keys rotate linear`. Pass: a GitHub token pasted in the Linear slot is refused at once; each new key is stored with its expiry; "Pushed to flocc-nimble." and the revoke warning; then `flocc -H flocc-nimble doctor` shows no FAIL line. Revoke the old tokens and key once `flocc status` shows no running session that started before the push.
5. `flocc status -H all`. Pass: flocc-nimble's row shows LOGIN `full` and TOKENS with the new expiry (after the next daily token check; until then the old one).
5b. Actions results with the agent token: on flocc-nimble, as the service user, `GH_TOKEN=$(cat ~/.config/flocc/gh-agent.token) gh pr checks <a PR with a finished workflow> -R <owner>/<repo>` and `gh run list -R <owner>/<repo> -L 3`. Record whether `gh pr checks` lists the workflow's checks. If it does not and `gh run list` works, take the Unconfirmed table's fallback (agents read CI with `gh run list --branch` and `gh run view --log-failed`) and note it in the PR.
6. `flocc -H flocc-nimble logs -n 5`, `flocc logs -n 5` (only host), and `flocc release NF-999` (no such claim: the host's own "no claim" error, exit 1).
7. `flocc ship -H flocc-nimble; echo "exit $?"`. Pass: no prompt, no ntfy push, the summary with `claude login full` and `exit 0`.
8. A fresh Daytona host: `just daytona-up flocc-smoke`, `flocc host add flocc-smoke --daytona flocc-smoke` (on a desktop store without a publish token, this asks for one after the repo form, and its page lists the repo just mapped) mapping one repo no issue carries a label for, answering yes to "Do other hosts handle the unticked repos?", then `flocc ship -H flocc-smoke` (summary ends with `flocc login -H flocc-smoke`, exit 0) and `flocc login -H flocc-smoke` (the sign-in QR code shows without `qrencode`). Then `just daytona-stop flocc-smoke`, delete `~/.config/flocc/hosts/flocc-smoke.toml`, and delete the smoke repo's label in Linear if `host add` created it.
9. If Task 6's manual step confirmed that `POST /pulls` returns 422 for the publish token and 403 for the agent token, move the spec's Unconfirmed line "GitHub checks the caller's permission before validating `head` on `POST /pulls`" out of "Unconfirmed" with the date; do the same for the Linear write probe (Task 7 Step 7) and the `gh pr checks` result (5b), and commit the spec with `git commit -s`.
