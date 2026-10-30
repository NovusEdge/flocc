# Executor Core, Plan 1: Spike, Ledger Migration and Removals

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove the plugin monitor that the new cardinal depends on, migrate the ledger for events and holds, and delete cardinal 0.9–0.12, A2A, escalation and ntfy, the permission broker, grants, the gate, path claims, and the escalate, coordinate and handoff skills, leaving every kept path (publish, tests-amended, rebase, canary, tokens, comment steering) working and both test suites green.

**Architecture:** The ledger gets a `PRAGMA user_version` migration list; each removal task that drops a column appends its own migration. Escalation's call sites collapse onto one small `Notifier` in a new `notice.py` that comments on the issue or writes the digest; plan 3 turns that one choke point into events. The idle and background markers the Stop hook writes move out of `gate.py` into a new `idle.py`; `AMENDED` moves into `publisher.py`; `pr_url` moves from `a2atick.py` into `publisher.py`. Everything else is deletion plus caller edits.

**Tech Stack:** Python 3.13 stdlib (sqlite3 3.35+ for `ALTER TABLE ... DROP COLUMN`), pytest, ruff, pyrefly; Go 1.27 for the desktop installer; uv for the lock file.

**Spec:** `docs/superpowers/specs/2026-10-29-executor-core-design.md` ("Order of work" steps 0–3)

## Global Constraints

- Each task is one PR into the feature branch `feat/executor-core` (create it from `spec/executor-core` before Task 1). Each task ends with its targeted tests green; Task 10 runs `just check` once.
- Commits use `git commit -s`. No `Co-Authored-By` trailer, no AI attribution line.
- Read files with the Read tool, change them with Edit or Write. No `sed -i`, no heredoc or `cat >` into repo files.
- Comments only carry facts the code cannot show (why, what breaks, which outside constraint). Delete a comment that only made sense for removed code; do not append corrections to it.
- No new dependency. `pyproject.toml` loses the `a2a` extra and its dev deps; `uv.lock` is regenerated with `uv lock`, never edited by hand.
- flocc-nimble stays stopped for the whole plan (spec step 1). Nothing in this plan starts the service, adds the `claude` label to an issue, or touches a live Linear issue.
- Kept for plan 2 and plan 3, and must not be removed or renamed here: `publisher.publish` and its tester-file guard and `tests-amended` path; `rebase.step`; `tokens.py`; `canary.py`; `poller.forward_comments` (owner and `steer_authors` replies into a live session); `flocc release`, `flocc canary`, `flocc done`, `flocc review-done`, `flocc tests-amended`, `flocc commit`, `flocc stop`, `flocc session-start`, `flocc statusline`, `flocc press`, `flocc peek`, `flocc say`, `flocc attach`; the commit-msg trailer hook; the idle and stall notices in `supervisor.py`; the digest; auto mode with `permissions.toml` (`Config.permission_allow`, `Config.permission_environment`, `judge_model`, `judge_max_per_run`, `installer/internal/steps/permissions.go`), which plan 2 replaces with `bypassPermissions`.
- Run Python tests with `uv run pytest -q <paths>` and Go tests with `go test ./<pkg>/...`. Lint a touched Python file with `uv run ruff check <file> && uv run ruff format --check <file>`.

## Interfaces plan 1 leaves for plans 2 and 3

| Interface | Where after plan 1 | Used by |
|---|---|---|
| `ledger.connect(path)` runs `_migrate`; `_MIGRATIONS` is a tuple of statement tuples, `PRAGMA user_version` = count applied | `src/flocc/ledger.py` | Plan 3 appends migrations for event helpers if the columns change |
| `events(id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, issue_key TEXT, repo TEXT, detail TEXT NOT NULL DEFAULT '', at REAL NOT NULL, printed_at REAL, acked_at REAL)` and `holds(issue_key TEXT PRIMARY KEY, reason TEXT NOT NULL, at REAL NOT NULL)` | ledger, migration 1 | Plan 3 (`flocc events`, `ack`, `hold`) |
| `notice.Notice(cause: str, summary: str, action: str = "", issue_key: str \| None = None)` and `notice.Notifier(con, linear, now).send(notice: Notice, issue: Issue \| None = None) -> None` on `Runtime.notifier` | `src/flocc/notice.py` | Plan 3 makes `send` also write an event; every service-side owner notice already goes through it |
| `publisher.AMENDED = "tests-amended.json"`, `publisher.read_amended(state)` | `src/flocc/publisher.py` | Plan 2 (optional tester guard), `flocc tests-amended` |
| `publisher.pr_url(rt, claim) -> str \| None` | `src/flocc/publisher.py` | `rebase.step`, plan 3 `run-summary` |
| `idle.note_idle(state, payload, now)`, `idle.note_background(state, payload)`, `idle.has_background(state)`, `idle.clear_idle(state)`, `idle.BACKGROUND` | `src/flocc/idle.py` | `flocc stop` (plan 2 adds the refuse-once Stop hook here), `supervisor` |
| `supervisor._block(rt, claim, cause, summary, action)` | `src/flocc/supervisor.py` | Plan 3 emits `ended` here |
| `cli.cmd_hook` pre-commit refuses commits from the review session; commit-msg adds the trailer | `src/flocc/cli.py`, `worktree.install_hook` | Plan 2 reviewer archetype |
| `canary.mark`, `canary.hold_reason`, `ledger.held_keys` | unchanged | Plan 3 `base-broken` |
| `tokens.check_tokens`, `tokens.check_claude_login` send through `rt.notifier` | `src/flocc/tokens.py` | Plan 3 `service-error` |

## Where this plan departs from the spec, and why

- **The pre-commit hook stays, with only the review-session refusal.** d174 says the pre-commit hook goes; the spec's Removed table says only its claim check goes. The review refusal is the one thing keeping a reviewer from committing until plan 2's reviewer archetype and guard hook exist, and dropping it later costs one line.
- **The installer's permissions step and `permissions.toml` stay until plan 2.** The spec lists `steps/permissions{,_test}.go` under Removed, but its allow rules are what auto mode runs on. They go in the same PR that switches sessions to `bypassPermissions`.
- **The 60% handoff restart goes here, not in step 4.** The spec puts it under the smaller supervisor (step 4) but removes the handoff skill in step 3; the restart sends the session to that skill. `handoff.md` itself stays: the tester still writes it for the implementer, and `flocc context` prints it.
- **Question tracking goes with the gate.** The gate's `AskUserQuestion` hook is what writes `asking`; without it `_check_question`, the wall-clock credit and the reply hold read a file nobody writes. Until plan 2's guard denies `AskUserQuestion`, an open question shows up as a stall notice.
- **Escalation becomes `notice.py`, not nothing.** Blocked runs, failed prepares, idle and stall notices, token and login warnings and repeated tick failures still need to reach the owner between plan 1 and plan 3. They become an issue comment (or a digest line when there is no issue) with no tiers, labels or pushes.
- **Retired config keys are ignored with a warning.** `ntfy_url`, `context_restart_pct` and a repo's `shared_paths` are dropped at load with one stderr line, so a host's existing `config.toml` keeps loading after the upgrade.
- **Agent-block keys `paths` and `allow` are refused as unknown keys.** An issue that still has them gets the existing "Skipping: agent block: unknown keys" comment, so the owner sees the change instead of having the lines silently ignored.

## Review Focus

1. **`flocc status` on a host whose ledger the new code has not migrated yet** (upgrade shipped, no tick run): `status` opens the ledger read-only and must still list the claims, though the `paths` and `escalation_offset` columns are still there. Pinned in Task 1 (`test_a_column_the_code_no_longer_knows_is_ignored_on_read`).
2. **Two processes opening an old ledger at once** (the tick and a session's pre-commit hook): the migration must run once and neither process may fail with "duplicate column" or "no such column". Pinned in Task 1 (`test_a_second_connection_finds_the_ledger_migrated`), and by the re-read of `user_version` inside `BEGIN IMMEDIATE`.
3. **A host `config.toml` that still has `ntfy_url`, `context_restart_pct` or `shared_paths`**: the tick must keep loading it. Pinned in Task 6 (`test_retired_top_level_keys_are_ignored_with_a_warning`) and Task 7 (`test_a_retired_repo_key_is_ignored_with_a_warning`).
4. **An issue description that still has `paths:` or `allow:` in its agent block**: the poller must skip it with one comment, not crash the tick. Pinned in Task 5 (`test_allow_is_now_an_unknown_key`) and Task 7 (`test_paths_is_now_an_unknown_key`).
5. **A blocked run after escalation is gone**: the owner must still get one comment on the issue naming the cause and the action, and the claim must still become `blocked`. Pinned in Task 6 (`test_block_comments_on_the_issue_without_labels`).

---

### Task 0: Spike the plugin monitor on the desktop

No code ships from this task, and nothing from it is committed. It checks the one Claude Code feature the new cardinal stands on (spec step 0). The owner runs it, or an agent sets it up and the owner reads the phone and the Claude app.

**Exit criterion: if any of checks 1–4 fails, stop here. Do not start Task 1. Report the failed check, with what was seen, to the owner: the design returns to them.** Check 5 is recorded, not a stop.

**Files (outside the repo, not committed):**
- Create: `~/monitor-spike/.claude-plugin/plugin.json`
- Create: `~/monitor-spike/monitors/monitors.json`
- Create: `~/monitor-spike/heartbeat.sh`

**Interfaces:**
- Consumes: nothing.
- Produces: a yes/no with notes for each check below, recorded with the docket skill as a claim and told to the owner.

- [ ] **Step 1: Check the Claude Code version**

Run: `claude --version`
Expected: `2.1.290` or later (idle compaction, spec "Compaction"). If lower, update Claude Code first.

- [ ] **Step 2: Write the plugin**

`~/monitor-spike/.claude-plugin/plugin.json`:

```json
{
  "name": "monitor-spike",
  "version": "0.0.1",
  "description": "Throwaway: does a when=always plugin monitor wake an idle Remote Control session"
}
```

`~/monitor-spike/monitors/monitors.json` (the format from code.claude.com/docs/en/plugins/components.md#monitors; `when: "always"` starts it at session start and on plugin reload):

```json
[
  {
    "name": "wake",
    "command": "touch \"$HOME/monitor-spike/wake.log\"; tail -n0 -F \"$HOME/monitor-spike/wake.log\"",
    "description": "Spike: lines appended to wake.log",
    "when": "always"
  },
  {
    "name": "heartbeat",
    "command": "\"${CLAUDE_PLUGIN_ROOT}\"/heartbeat.sh",
    "description": "Spike: one line per hour, then exits after 3 hours",
    "when": "always"
  }
]
```

`~/monitor-spike/heartbeat.sh`:

```sh
#!/bin/sh
# Prints once an hour for three hours, then exits, so the spike also sees what an exit does.
for n in 1 2 3; do
  sleep 3600
  echo "heartbeat $n at $(date +%H:%M)"
done
echo "heartbeat exiting"
```

Run: `chmod +x ~/monitor-spike/heartbeat.sh && claude plugin validate ~/monitor-spike`
Expected: `Validation passed` (warnings about a missing `author` are fine).

- [ ] **Step 3: Start an idle interactive Remote Control session**

Run on the desktop (not on flocc-nimble):

```bash
tmux new-session -d -s monitor-spike -c ~/monitor-spike \
  claude --plugin-dir ~/monitor-spike --model sonnet \
  --remote-control monitor-spike --name monitor-spike \
  "You are a spike. When a monitor notification arrives, reply with one line quoting it and the time. Otherwise do nothing."
```

Note the time. Open the Claude app on the phone.

- [ ] **Step 4: Run the checks**

| # | Check | How | Pass when |
|---|---|---|---|
| 1 | A printed line wakes the idle session | After the first reply, wait 5 minutes idle, then `echo "wake 1" >> ~/monitor-spike/wake.log` | The session takes a turn quoting `wake 1` without any typed input (`tmux attach -r -t monitor-spike` to see it) |
| 2 | The wake is visible in the Claude app | Look at the `monitor-spike` session in the app after check 1 | The turn caused by `wake 1` shows in the app's view of the session |
| 3 | It keeps running for hours | Leave the session idle for 3 hours, then `echo "wake 2" >> ~/monitor-spike/wake.log` | The session wakes on `wake 2`; `pgrep -af 'tail -n0 -F'` still shows the tail process |
| 4 | What an exiting command does | Watch for the three `heartbeat` lines and `heartbeat exiting` over the same 3 hours | Each line woke the session. Record whether the session saw the exit, whether Claude Code restarted the monitor, and whether `/reload-plugins` restarts it. Pass means the exit did not end or break the session; cardinal's loop reconnects on its own either way |
| 5 | Phone notification per new Remote Control session | Start a second session the same way with `--remote-control monitor-spike-2 --name monitor-spike-2`, then a third | Record whether the phone got a notification for each new session (spec "Testing": a live run must show none) |

- [ ] **Step 5: Record and clean up**

Record each check's result and notes as one docket claim (docket skill), then tell the owner the results in plain words. Then:

```bash
tmux kill-session -t monitor-spike; tmux kill-session -t monitor-spike-2; tmux kill-session -t monitor-spike-3
rm -rf ~/monitor-spike
```

---

### Task 1: Drain flocc-nimble and migrate the ledger

Spec step 1. The drain is an owner step on live infrastructure; the implementing agent does only Steps 3–8 and must not run the drain commands.

**Files:**
- Modify: `src/flocc/ledger.py:1-104` (schema comment, `_MIGRATIONS`, `_migrate`, `connect`, `_from_row`)
- Test: `tests/test_ledger.py`

**Interfaces:**
- Consumes: nothing.
- Produces: `ledger._MIGRATIONS: tuple[tuple[str, ...], ...]` (Tasks 6 and 7 append one entry each); `ledger._SCHEMA` frozen as version 0; tables `events` and `holds` (see "Interfaces plan 1 leaves"); `_from_row` ignores columns `Claim` has no field for.

- [ ] **Step 1 (owner): Confirm flocc-nimble is stopped**

```bash
ssh root@flocc-nimble 'sudo -u flocc XDG_RUNTIME_DIR=/run/user/$(id -u flocc) systemctl --user is-active flocc.timer flocc.service'
```

Expected: `inactive` twice. If the timer is active, stop it the same way with `systemctl --user stop flocc.timer` before going on.

- [ ] **Step 2 (owner): Release every live claim**

```bash
flocc -H all status
```

For each issue listed under flocc-nimble, run `flocc release <KEY>` on the desktop. Expected afterwards: `flocc -H all status` shows `no live claims` for flocc-nimble. Releasing moves each issue back to Todo; with the timer stopped nothing restarts it.

- [ ] **Step 3: Write the failing tests**

Append to `tests/test_ledger.py` (add `import sqlite3` at the top, beside `import pytest`):

```python
V0_CLAIM = (
    "INSERT INTO claims (issue_key, issue_id, repo, worktree, branch, phase, state, paths,"
    " depends_on, settings_json, started_at, wall_clock_s)"
    " VALUES ('NF-9', 'id-9', 'backend', '/wt/NF-9', 'agent/nf-9', 'impl', 'running',"
    " '[\"src/**\"]', '[]', '{}', 1.0, 3600)"
)


def _v0_ledger(path):
    raw = sqlite3.connect(path)
    raw.executescript(ledger._SCHEMA)
    raw.execute(V0_CLAIM)
    raw.commit()
    raw.close()


def _columns(con, table):
    return {r[1] for r in con.execute(f"PRAGMA table_info({table})")}


def test_a_fresh_ledger_is_at_the_latest_version(con):
    assert con.execute("PRAGMA user_version").fetchone()[0] == len(ledger._MIGRATIONS)
    assert {"id", "kind", "issue_key", "repo", "detail", "at", "printed_at", "acked_at"} == (
        _columns(con, "events")
    )
    assert {"issue_key", "reason", "at"} == _columns(con, "holds")


def test_an_old_ledger_migrates_and_keeps_its_claims(tmp_path):
    path = tmp_path / "old.sqlite"
    _v0_ledger(path)
    con = ledger.connect(path)
    assert con.execute("PRAGMA user_version").fetchone()[0] == len(ledger._MIGRATIONS)
    claim = ledger.get_claim(con, "NF-9")
    assert claim is not None and claim.phase == "impl" and claim.wall_clock_s == 3600
    assert "events" in {r[0] for r in con.execute("SELECT name FROM sqlite_master")}


def test_a_second_connection_finds_the_ledger_migrated(tmp_path):
    path = tmp_path / "old.sqlite"
    _v0_ledger(path)
    first, second = ledger.connect(path), ledger.connect(path)
    assert second.execute("PRAGMA user_version").fetchone()[0] == len(ledger._MIGRATIONS)
    assert ledger.get_claim(first, "NF-9") is not None


def test_a_column_the_code_no_longer_knows_is_ignored_on_read(con):
    con.execute("ALTER TABLE claims ADD COLUMN retired TEXT")
    ledger.insert_claim(con, make())
    claim = ledger.get_claim(con, "NF-1")
    assert claim is not None and not hasattr(claim, "retired")
```

- [ ] **Step 4: Run them to see them fail**

Run: `uv run pytest -q tests/test_ledger.py`
Expected: FAIL; `_MIGRATIONS` does not exist, and `get_claim` raises `TypeError: Claim.__init__() got an unexpected keyword argument 'retired'`.

- [ ] **Step 5: Implement**

In `src/flocc/ledger.py`, put a comment above `_SCHEMA` and add the migrations after it (after the closing `"""` of `_SCHEMA`, before `_JSON_FIELDS`):

```python
# Version 0. A ledger moves forward through _MIGRATIONS only; PRAGMA user_version counts the
# entries applied. Never edit an entry once it has shipped: append a new one.
_SCHEMA = """
```

```python
_MIGRATIONS: tuple[tuple[str, ...], ...] = (
    (
        """CREATE TABLE events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,
  issue_key TEXT,
  repo TEXT,
  detail TEXT NOT NULL DEFAULT '',
  at REAL NOT NULL,
  printed_at REAL,
  acked_at REAL
)""",
        """CREATE TABLE holds (
  issue_key TEXT PRIMARY KEY,
  reason TEXT NOT NULL,
  at REAL NOT NULL
)""",
    ),
)
```

Replace `connect` and add `_migrate` below it:

```python
def connect(path: Path | str) -> sqlite3.Connection:
    # Autocommit plus explicit BEGIN IMMEDIATE: the tick and the pre-commit hook write from
    # separate processes, and a deferred transaction can deadlock upgrading to a write lock.
    con = sqlite3.connect(path, timeout=10, isolation_level=None)
    con.row_factory = sqlite3.Row
    con.execute("PRAGMA journal_mode=WAL")
    con.execute("PRAGMA busy_timeout=10000")
    con.executescript(_SCHEMA)
    _migrate(con)
    return con


def _version(con: sqlite3.Connection) -> int:
    return con.execute("PRAGMA user_version").fetchone()[0]


def _migrate(con: sqlite3.Connection) -> None:
    if _version(con) >= len(_MIGRATIONS):
        return
    # Read again under the write lock: the tick and a session's hook can open an old ledger at
    # the same moment, and the second must not apply a migration the first just applied.
    with transaction(con):
        start = _version(con)
        for n, statements in enumerate(_MIGRATIONS[start:], start + 1):
            for sql in statements:
                con.execute(sql)
            con.execute(f"PRAGMA user_version = {n}")
```

`transaction` is defined below `connect`; move the `transaction` function above `connect` so the module reads top-down.

Replace `_from_row`:

```python
_CLAIM_FIELDS = frozenset(f.name for f in fields(Claim))


def _from_row(row: sqlite3.Row) -> Claim:
    # status reads the ledger read-only, so before the first tick after an upgrade it still sees
    # columns a migration has yet to drop.
    data = {k: v for k, v in dict(row).items() if k in _CLAIM_FIELDS}
    for key in _JSON_FIELDS:
        data[key] = json.loads(data[key])
    return Claim(**data)
```

Place `_CLAIM_FIELDS` right after the `Claim` dataclass.

- [ ] **Step 6: Run the ledger and status tests**

Run: `uv run pytest -q tests/test_ledger.py tests/test_status.py`
Expected: PASS.

- [ ] **Step 7: Run everything that opens a ledger**

Run: `uv run pytest -q`
Expected: PASS (no test depends on the ledger having no `events` table).

- [ ] **Step 8: Commit**

```bash
uv run ruff check src/flocc/ledger.py tests/test_ledger.py && uv run ruff format --check src/flocc/ledger.py tests/test_ledger.py
git add src/flocc/ledger.py tests/test_ledger.py
git commit -s -m "ledger: user_version migrations; add events and holds"
```

---

### Task 2: Purge cardinal 0.9–0.12

Spec step 2. Go only, plus `deploy/cardinal/` and one exemption in `tests/test_names.py`. Several files named `cardinal*` also hold code that `flocc host create`, setup's old-users pass (d126), `ui`, `dash` and `ship` use; that code moves out first (Steps 2–5), then the cardinal code is deleted (Step 6). Line numbers are at commit `f6f2353`; find each edit by its quoted text if lines moved.

**Files:**
- Create: `installer/internal/safe/safe.go`, `installer/internal/safe/safe_test.go`, `installer/internal/steps/rootreport.go`, `installer/internal/steps/rootreport_test.go`, `installer/cmd/flocc-setup/rootphases.go`, `installer/cmd/flocc-setup/rootphases_test.go`
- Delete: `installer/cmd/flocc-setup/cardinal.go`, `installer/cmd/flocc-setup/cardinal_test.go`, `installer/internal/cardinal/` (all of it, `saved/` included), `installer/internal/cli/cardinal.go`, `installer/internal/cli/cardinal_test.go`, `installer/internal/dash/cardinal.go`, `installer/internal/dash/testdata/cardinal-80x24.golden`, `installer/internal/steps/cardinal_{doc,files,host,login,root,status,user}.go` and their `_test.go` files (14 files), `deploy/cardinal/` (4 files)
- Modify: `deploy/embed.go`, `installer/cmd/flocc-setup/main.go`, `installer/internal/rootpass/rootpass.go`, `installer/internal/hosts/hosts.go`, `installer/internal/cli/{cli,cli_test,setup,setup_ops,setup_test,tailnet}.go`, `installer/internal/dash/{model,keys,issue,fleet,hostscreen,pager,layout,output,titles,model_test,fleet_test,hostscreen_test,review_test}.go`, `installer/internal/dash/testdata/fleet-{80x24,140x40}.golden`, `installer/internal/ship/{fleet,fleet_test,source_test}.go`, `installer/internal/create/root.go`, `installer/internal/ui/wire.go`, `installer/internal/steps/{old_users,old_users_test,login,claude}.go`, `tests/test_names.py`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces: package `safe` with `Sanitize`, `SanitizeBlock`, `HostPart` (the bodies of `cardinal/saved`'s functions, unchanged); in `steps`: `RootMarker`, `ReportStep`, `RootReport` (was `CardinalReport`, without `Key`, `Login`, `HostKey`, `Installed`), `ParseRootLine`, `ReportSteps`, `ValidRootStage`, `CheckRootStage`, `PrivilegedGroups` (was `CardinalPrivileged`), `PurgeUser` (was `CardinalPurge`), `writeAtomic`; flocc-setup root phases `create-root` and `old-user-remove`; `cli.removeOld(ctx, d, mux, machine, users) error`; `steps.OldUsers = []string{OldName, "cardinal"}`.

- [ ] **Step 1 (owner): Take cardinal's key off the hosts while the old code still exists**

`flocc cardinal remove --purge` runs the `cardinal-host-remove` phase on every flocc host: it removes cardinal's line from `/root/.ssh/authorized_keys`, `/etc/ssh/sshd_config.d/cardinal.conf`, `/etc/sysctl.d/60-flocc-cardinal.conf` and `/var/lib/flocc-cardinal`. Nothing after this task can. From a checkout of `main` on the desktop:

```bash
flocc cardinal remove --purge
```

Expected: it reports each host cleaned. Confirm on flocc-nimble: `ssh root@flocc-nimble 'grep -c cardinal /root/.ssh/authorized_keys; ls /etc/ssh/sshd_config.d/'` prints `0` and no `cardinal.conf`. The laptop's `cardinal` user goes later through `flocc setup`'s old-users pass (Step 9).

- [ ] **Step 2: Move the sanitizers to `installer/internal/safe`**

Create `installer/internal/safe/safe.go` with `package safe` and the bodies of `Sanitize`, `SanitizeBlock`, `HostPart` and the `maxField` constant copied unchanged from `installer/internal/cardinal/saved/saved.go` (with the imports they need). Create `installer/internal/safe/safe_test.go` with `TestSanitizeStripsEscapesAndCuts` copied from `saved/saved_test.go:32-37`, `package safe`.

In each of these files replace the import `"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"` with `"github.com/NovusEdge/flocc/installer/internal/safe"` and every `saved.Sanitize`/`saved.SanitizeBlock`/`saved.HostPart` with `safe.…`: `ui/wire.go`, `dash/hostscreen.go`, `dash/pager.go`, `dash/layout.go`, `dash/fleet.go`, `dash/output.go`, `dash/issue.go`, `dash/titles.go`, `dash/model.go`, `create/root.go`, `rootpass/rootpass.go`, `cli/tailnet.go`, `cli/setup.go`, `cli/setup_ops.go`, `ship/fleet.go`, `cli/ship.go` (all under `installer/internal/`; Task 3 later deletes `cli/ship.go`'s only use).

Run: `go build ./... && go test ./installer/internal/safe/`
Expected: PASS.

- [ ] **Step 3: Move the root-pass report and purge helpers to `steps/rootreport.go`**

Create `installer/internal/steps/rootreport.go` holding, moved from the cardinal files:
- from `cardinal_doc.go:12-80`: `CardinalMarker` renamed `RootMarker`, `ReportStep`, `CardinalReport` renamed `RootReport` with its `Key`, `Login`, `HostKey` and `Installed` fields removed, its `JSON` and `Line` methods, `ParseCardinalLine` renamed `ParseRootLine`, `ReportSteps`;
- from `cardinal_root.go:20-72`: `rootStageName`, `ValidRootStage`, `CheckRootStage`, `CardinalPrivileged` renamed `PrivilegedGroups`, `cardinalHome` renamed `oldHome`;
- from `cardinal_root.go:355-391`: `purgeWait`, `stopUser`, `CardinalPurge` renamed `PurgeUser`. In `PurgeUser` replace `Egress{User: user}.unit()` with `"flocc-cardinal-egress@"+user+".service"` and `EgressRulesPath(user)` with `"/etc/flocc-cardinal/"+user+".nft"`; that cleanup stays, it removes 0.12's nft rules;
- from `cardinal_user.go:35-56`: `writeAtomic` (used by `steps/login.go` and `steps/claude.go`).

Change the stage regex from `` `^/tmp/flocc-(cardinal|create)\.[A-Za-z0-9]{6,}$` `` to `` `^/tmp/flocc-(create|remove)\.[A-Za-z0-9]{6,}$` `` and the error text beside it to name `flocc-create` and `flocc-remove`.

Update the callers of the renamed names: `rootpass/rootpass.go` (lines 102, 108, 120, 143, 163-165), `rootpass/rootpass_test.go:99`, `create/root_test.go:84,110,121`, `steps/old_users.go:60,74-90` (and its line 73 comment "as CardinalPurge does" → "as PurgeUser does").

Create `installer/internal/steps/rootreport_test.go` from `cardinal_doc_test.go:8-17` (drop `Key` and `Login` from the literal), `cardinal_root_test.go:190-230` (`TestCheckRootStage`, with the `"flocc-cardinal."` prefixes changed to `"flocc-remove."` and line 223's path to `/var/tmp/flocc-remove.abcdef`) and `cardinal_root_test.go:232-253` (the purge test, with its `withCardinal`/`cardinalEnv` helpers from lines 16-29 inlined and `EgressRulesPath(u)` replaced by the literal path).

Run: `go build ./... && go test ./installer/internal/steps/ ./installer/internal/rootpass/ ./installer/internal/create/`
Expected: PASS (the cardinal files still compile against the renamed names only if you also update their uses; if they do not compile, do Step 6's deletion of the `steps/cardinal_*` files now and come back).

- [ ] **Step 4: Replace flocc-setup's cardinal phases with two root phases**

Create `installer/cmd/flocc-setup/rootphases.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/steps"
)

// rootPhases run as root inside one sudo pass that the desktop starts over ssh.
var rootPhases = []string{"create-root", "old-user-remove"}
```

Then move into it, from `cardinal.go`: the `--phase` parser at lines 35-51 renamed `rootPhase(args []string) (string, bool)` returning `slices.Contains(rootPhases, v)`; `createRootReport` (lines 63-73) unchanged; and a `runRootPhase(args []string, stdout, stderr io.Writer) int` built from `runCardinal` that: parses `--phase` and `--user`; refuses unless `os.Geteuid() == 0`; for `old-user-remove` requires `slices.Contains(steps.OldUsers, *user)` (in place of `saved.ValidUser`); reads `SUDO_UID` and runs `steps.CheckRootStage(stage, sudoUID, "flocc-setup")`; opens the setup log and builds the `Env` as lines 126-130 do, without `Home`; sets `syscall.Umask(0o022)`; and ends with

```go
	finish := func(r steps.RootReport) int {
		fmt.Fprint(stdout, "\n"+r.Line())
		if r.Error != "" {
			return 1
		}
		return 0
	}
	switch *phase {
	case "create-root":
		return finish(createRootReport(ctx, env))
	default:
		return finish(steps.RemoveOldUser(ctx, env, *user, sudoUID, time.Now()))
	}
```

Keep the exit-code rule `runCardinal` used if it differs from the one above; copy it.

In `installer/cmd/flocc-setup/main.go:153-155` replace:

```go
	if _, ok := cardinalPhase(args); ok {
		return runCardinal(args, os.Stdin, os.Stdout, os.Stderr)
	}
```

with:

```go
	if _, ok := rootPhase(args); ok {
		return runRootPhase(args, os.Stdout, os.Stderr)
	}
```

Create `rootphases_test.go` from `cardinal_test.go:17-48` (`TestCreateRootIsARootPhaseWithoutAUser` renamed to the new functions with the `nil` stdin argument dropped, `endedRunner`, `TestCreateRootReportAfterInterruptHoldsNoSteps`), and add:

```go
func TestOldUserRemoveIsARootPhase(t *testing.T) {
	if p, ok := rootPhase([]string{"--phase", "old-user-remove", "--user", "cardinal"}); !ok || p != "old-user-remove" {
		t.Fatalf("got %q %v", p, ok)
	}
	if _, ok := rootPhase([]string{"--phase", "cardinal-purge"}); ok {
		t.Fatal("cardinal-purge is gone")
	}
}
```

Delete `installer/cmd/flocc-setup/cardinal.go` and `cardinal_test.go`.

Run: `go test ./installer/cmd/flocc-setup/`
Expected: PASS.

- [ ] **Step 5: Give setup its own old-users root pass**

`cardinal.Deps.RemoveOld` was the d126 removal. Add to `installer/internal/cli/setup_ops.go` (imports `rootpass` and `ui` added; `errors` kept):

```go
// removeOld purges each agreed old user on machine in one root pass.
func removeOld(ctx context.Context, d *deps, mux []string, machine string, users []string) error {
	if !d.tty {
		return errors.New("the machine's sudo asks for a password; run this in a terminal")
	}
	if err := ship.Connect(ctx, d.run, ship.Dest{SSH: machine, Opts: mux}, machine, d.tty); err != nil {
		return err
	}
	m, err := host.Must(ctx, owner(d, mux, machine), host.Cmd{Name: "uname", Args: []string{"-m"}})
	if err != nil {
		return err
	}
	arch, err := ship.ArchFor(m)
	if err != nil {
		return err
	}
	root, err := d.repoRoot()
	if err != nil {
		return fmt.Errorf("removing old users builds flocc-setup from the flocc checkout it runs in; cd into it: %w", err)
	}
	bin, err := rootpass.Build(ctx, d.run, d.ui, root, arch, "flocc-setup")
	if err != nil {
		return err
	}
	defer os.RemoveAll(bin)
	d.ui.Note("Root pass", "removing "+strings.Join(users, ", ")+"\nsudo asks for your password on "+machine+" once")
	calls := make([][]string, len(users))
	for i, u := range users {
		calls[i] = []string{"--phase", "old-user-remove", "--user", u}
	}
	reports, err := rootpass.Run(ctx, d.run, rootpass.Pass{Machine: machine, Opts: mux, Prefix: "/tmp/flocc-remove",
		Files: []string{filepath.Join(bin, "flocc-setup")}, Calls: calls, Why: "root pass: sudo on " + machine + " asks for your password"})
	for i, r := range reports {
		title := "Removed " + users[i]
		if r.Error != "" {
			title = "Removing " + users[i] + " failed"
		}
		if len(r.Steps) > 0 || len(r.Notes) > 0 {
			d.ui.Group(title)
			ui.Rows(d.ui, rootpass.Events(r))
		}
	}
	return err
}
```

Check `ship.Connect`, `ship.ArchFor`, `rootpass.Build`, `rootpass.Run`, `rootpass.Pass` and `rootpass.Events` against `installer/internal/cardinal/passes.go:321-368`, which this replaces, and match their real signatures.

- [ ] **Step 6: Delete the cardinal code and fix its callers**

```bash
git rm -r installer/internal/cardinal deploy/cardinal installer/internal/cli/cardinal.go installer/internal/cli/cardinal_test.go installer/internal/dash/cardinal.go installer/internal/dash/testdata/cardinal-80x24.golden
git rm installer/internal/steps/cardinal_{doc,files,host,login,root,status,user}.go installer/internal/steps/cardinal_{doc,files,host,login,root,status,user}_test.go
```

Then make these edits:

`deploy/embed.go:16-19`: delete

```go
//go:embed cardinal
var Cardinal embed.FS

var CardinalUnits = []string{"cardinal.service", "cardinal-renew.service", "cardinal-renew.timer"}
```

`rootpass/rootpass.go`: the package doc (lines 2-3) now says flocc host create and setup's old-users removal use the root pass (drop the sentence about cardinal importing create); line 91's comment names `"/tmp/flocc-create" or "/tmp/flocc-remove"`; line 162's `flocc-cardinal line` comment names the root report line. `rootpass_test.go:81` may keep its `"/tmp/flocc-cardinal.Ab12Cd\n"` (any non-matching prefix works).

`hosts/hosts.go:23-24`: `// old-version cleanup and cardinal; MachineNone when the host is on none of the owner's.` → `// old-version cleanup; MachineNone when the host is on none of the owner's.`

`cli/cli.go`: line 34 drop `, cardinal` from `keys, a host, old versions, cardinal, logins`; delete the usage lines 44-45 (`flocc cardinal setup|login|...` and its continuation); delete `case "cardinal": return len(rest) > 1 && rest[1] == "attach"` (168-169), `case "cardinal": return len(rest) > 1 && (rest[1] == "setup" || rest[1] == "login")` (242-243) and the `case "cardinal":` dispatch block ending `return d.cardinal(ctx, rest[1:])` (330-336). `cli_test.go:208,209,212,215`: delete the four `{"cardinal", …}` cases.

`cli/setup_ops.go`: delete the `cardinal` import; delete the struct fields `findUser`, `cardinal`, `cardStatus`, `cardSetup`, `cardLogin` (lines 44-47, 50) and `cd := d.cardinalDeps()` (61); in the literal at 93-111 replace the `findUser`, `cardinal`, `cardStatus`, `cardSetup`, `removeOld`, `login`, `cardLogin` entries with:

```go
		removeOld: func(ctx context.Context, m string, users []string) error { return removeOld(ctx, d, mux, m, users) },
		login:     func(ctx context.Context, name, email string) error { return loginWith(ctx, d, name, email) },
```

(keep the existing `login` closure body if it differs from this one; only the cardinal entries go). Line 265's comment drops `and cardinal`.

`cli/setup.go`: delete the `cardinal` import and the `cardLogin string` field (line 43); line 60 becomes `d.ui.Plan([]string{"Checkout", "Keys", "Tailnet", "Hosts", "Old versions", "Logins"})`; lines 89-94 become

```go
	w.removeRest(machines, remove)
	w.logins(checked)
```

`func (w *wizard) logins(checked []checkedHost, cs *saved.Saved) {` becomes `func (w *wizard) logins(checked []checkedHost) {` with its lines 111-113 (`if cs != nil && !w.stopped { w.login("cardinal", ...) }`) deleted and line 97's comment dropping `and cardinal`; delete `setupFailures`, `cardinal` and `setupCardinal` (236-345); line 347's comment becomes `// removeRest runs the agreed removals, one purge-only root pass per machine.`

`cli/setup_test.go`: delete the `cardinal`, `saved` and `steps` imports; in `doneOps` delete the `findUser`, `cardinal`, `cardStatus`, `cardSetup`, `cardLogin` entries (77-88, 91-94); in `withOld` delete the `cardSetup` wrap (448-452) so line 448 reads `remove := ops.removeOld`; delete `"Cardinal"` from `want` (383); line 786's string becomes `"Checkout\nKeys\nTailnet\nHosts\nOld versions\nLogins"`; drop `|| l.has("cardinal setup")` at 473, 502, 510, 573, `|| l.has("cardinal status")` at 605, `&& c != "cardinal status"` at 736, `|| l.has("cardinal login")` at 765; delete `TestSetupRemovesInCardinalsRootPassWhenSetupRuns`, `TestSetupCardinalWhenNoneIsSaved`, `TestSetupDoesNotQuietlyRebuildARemovedCardinal`, `TestSetupAfterASetupOfARemovedCardinalChecksStatusWithoutAsking`; rename `TestSetupRemovesAloneBesideAHealthyCardinal` to `TestSetupRemovesAnOldUser` and `TestSetupWithoutIncusFailsCardinalButStillRemoves` to `TestSetupWithoutIncusStillRemoves` (deleting its lines 566-568); in `TestSetupOnAFreshDesktopRunsEveryStepInOrder` delete lines 708-712, the `"claude.ai pool account for cardinal": …` answer (714) and the three cardinal `inOrder` entries (724-726).

`dash/model.go`: delete the `sync` import, `Cardinals(context.Context) ([]ship.CardinalRow, []string)` from the source interface (30), `and cardinal attach` from line 39's comment, the `cards` fields (69, 101), `cardinalScreen` (82), `cardSel int` (123), `m.cardSel = …` (275) and `case cardinalScreen: return m.cardinalView()` (326-327); the fetch closure (159-174) becomes

```go
	return func() tea.Msg {
		hs, notes := src.Discover(ctx)
		res := Fetch(ctx, hs, src.Status)
		return fetched{results: res, notes: notes, at: time.Now()}
	}
```

and line 256 `m.cards, m.notes = f.cards, f.notes` becomes `m.notes = f.notes`.

`dash/keys.go`: delete the `cardinalScreen` key case (57-58), `case cardinalPane` in `opens` (137-138) and in `move` (162-163), `cardinalScreen` in `bar` (177-178); `panes()` becomes `func (m *Model) panes() []focus { return []focus{hostsPane, issuesPane} }` with its comment saying the hosts and issues panes, in that order.

`dash/issue.go:33-36`: delete `case cardinalPane: if len(m.cards) > 0 { m.screen = cardinalScreen }`.

`dash/fleet.go`: delete `cardinalPane` (22), `cardW = 34` (26), `cardLines := m.cardLines()` (87), `if len(m.cards) > 0 { needC = len(cardLines) + 2 }` (92-94) and `cardLines()` (194-223); lines 88 and 95-115 reduce to

```go
	needH := len(hostLines) + 2
	capTop := max(bh/2, 3)
	topH := min(needH, capTop)
	top := hostPane(iw, topH)
```

keeping whatever the old block did with `top` after that.

`dash/model_test.go:84`: delete `func (*fakeSource) Cardinals(…)`. `dash/fleet_test.go`: delete the `saved` and `steps` imports, `cardRow` (28-31), line 39 (`f.cards = …`), `TestCardinalPaneRows`, lines 85-89 of `TestArrowsMoveOnlyTheFocusedPane`, and lines 46-53 of `TestTabCyclesHostsCardinalIssues`, renamed `TestTabCyclesHostsAndIssues`. `dash/hostscreen_test.go`: delete `TestCardinalScreen`, `TestCardinalAttachArgs`, `TestCardinalGolden`. `dash/review_test.go`: delete the `saved` import, `TestCardinalAttachesTheSelectedInstall`, `TestCardinalAttachWithoutInstallsDoesNothing`.

`ship/fleet.go` (the CARDINAL table of `status -H all`): delete the `steps` import; delete `CardinalRow`, `fetchCardinal`, `collectCardinals`, `discoverCardinals`, `cardinalTable` (253-351); `func fleetTables(rows []Row, cards []CardinalRow) (tables []ui.Table, failures []string) {` → `func fleetTables(rows []Row) (tables []ui.Table, failures []string) {` with its `if len(cards) > 0 {…}` block (618-622) deleted and its comment (553-554) no longer mentioning cardinal; `func draw(s ui.Session, rows []Row, cards []CardinalRow, notes []string) {` / `tables, failures := fleetTables(rows, cards)` → `func draw(s ui.Session, rows []Row, notes []string) {` / `tables, failures := fleetTables(rows)`; drop `saved.Dir(home)` and the `cardinalDir` field and parameter from `runFleet`, `newSource` and the source struct (693, 699, 705-706, 722); delete `Source.Cardinals` (742-746); in `runFleet` delete lines 783-790 and 792 (the cardinal goroutine and `<-done`) and call `draw(s, rows, notes)`.

`ship/fleet_test.go`: delete the `saved` and `steps` imports, `cardinalFake` and its three tests (300-352); `render` takes `(rows []Row, notes []string)` and calls `fleetTables(rows)`; fix its callers (258, 294 dropping the `"CARDINAL"` check, 484 `draw(ui.New(rec), rows, []string{…})`, 520-523 dropping `, nil`). `ship/source_test.go:30,64`: `newSource("", t.TempDir(), t.TempDir())` → `newSource("", t.TempDir())`.

- [ ] **Step 7: Let setup's old-users pass remove the `cardinal` user**

`installer/internal/steps/old_users.go:9-11`: replace

```go
// OldUsers are the service users earlier flocc versions made. flocc setup offers to remove one it
// finds; the root pass removes no other name. A later rename adds its old name here.
var OldUsers = []string{OldName}
```

with

```go
// OldUsers are the users earlier flocc versions made: the service user before the rename, and
// cardinal 0.9-0.12's. flocc setup offers to remove one it finds; the root pass removes no other name.
var OldUsers = []string{OldName, "cardinal"}
```

`installer/internal/steps/old_users_test.go:78`: the "not in the table" case uses `user = "cardinal"`; change it to `user = "nobody-flocc"`.

`installer/internal/steps/claude.go:17`: nothing (Task 5 does not touch it; leave the version floor).

`tests/test_names.py`: delete `"installer/internal/cardinal/cardinal_test.go",` from `EXEMPT`.

- [ ] **Step 8: Regenerate the dash goldens and run the Go suite**

```bash
gofmt -w installer deploy
go build ./... && go vet ./...
go test ./installer/internal/dash -run Golden -update
git diff --stat installer/internal/dash/testdata
go test ./...
uv run pytest -q tests/test_names.py
grep -rn -i 'cardinal' installer deploy --include='*.go' | grep -v 'flocc-cardinal-egress@\|/etc/flocc-cardinal/\|"cardinal"\|cardinal 0.9-0.12'
```

Expected: only `fleet-80x24.golden` and `fleet-140x40.golden` change; all tests PASS; the last grep prints nothing (the remaining hits are the purge paths and the `OldUsers` entry).

- [ ] **Step 9 (owner, after merge): Remove the laptop's `cardinal` user**

Run `flocc setup` on the desktop. In "Old versions" it offers to remove the `cardinal` user on zer0-day-nimble; answer yes. Expected: `Removed cardinal`, and `/root/flocc-removed-cardinal-<ts>.tar.gz` exists on the laptop (it holds cardinal's old private key; delete it once nothing in it is needed). Check first that no peer session or build uses the laptop's daemons.

- [ ] **Step 10: Commit**

```bash
git add -A installer deploy tests/test_names.py
git commit -s -m "Purge cardinal 0.9-0.12 from the desktop installer"
```

---

### Task 3: Remove A2A and the coordinate skill

The A2A server, client, MCP tools, inbox and tick step, the hooks that deliver mail, `flocc-a2a.service`, and the desktop's token minting and tailnet grant. `pr_url` moves into `publisher.py` for `rebase.step`.

**Files:**
- Delete: `src/flocc/{a2aclient,a2aconf,a2aserver,a2atick,mcp_a2a,inbox,delivery}.py`, `tests/test_{a2aclient,a2aserver,a2atick,mcp_a2a,inbox,delivery}.py`, `deploy/flocc-a2a.service`, `plugin/skills/coordinate/`, `installer/internal/cli/a2a.go`, `installer/internal/cli/a2a_test.go`, `installer/internal/steps/a2a.go`, `installer/internal/steps/a2a_test.go`
- Modify: `src/flocc/{publisher,rebase,tick,cli,sessions,status,runtime,context}.py`, `plugin/hooks/hooks.json`, `plugin/skills/implementer/SKILL.md`, `pyproject.toml`, `uv.lock`
- Modify (Go): `deploy/embed.go`, `deploy/embed_test.go`, `installer/internal/steps/list.go`, `installer/internal/steps/repo.go:50`, `installer/internal/cli/{ship,ship_test,create}.go`, `installer/internal/keys/store.go`, `installer/internal/answers/{answers,answers_test}.go`, `installer/internal/tailnet/{policy,policy_test}.go`, `installer/internal/cli/tailnet_test.go`
- Test: `tests/test_rebase.py`, `tests/test_sessions.py`, `tests/test_status.py`, `tests/test_plugin.py`, `tests/test_context.py`

**Interfaces:**
- Consumes: `cli.py` as it stands after Task 2 (Python untouched by Task 2).
- Produces: `publisher.pr_url(rt, claim: Claim) -> str | None`; `cli.py` imports `from flocc import gate, ledger, permission, usage`; `claude_argv` passes only the issue's own MCP servers; the status report has no `a2a` key; `steps.ServiceSteps()` returns `[]Step{Repo{}, Identity{}, Claude{}, ClaudeLogin{}, Secrets{}, Permissions{}, Tools{}, Config{}, Run{}}`.

- [ ] **Step 1 (owner): Stop the A2A service on flocc-nimble**

```bash
ssh root@flocc-nimble 'sudo -u flocc XDG_RUNTIME_DIR=/run/user/$(id -u flocc) systemctl --user disable --now flocc-a2a.service; rm -f ~flocc/.config/systemd/user/flocc-a2a.service ~flocc/.config/flocc/a2a.toml'
rm -f ~/.config/flocc/a2a.toml
```

Nothing after this task stops or removes them. The keyring slots `a2a_owner` and `a2a_host:<name>` on the desktop can be left; nothing reads them.

- [ ] **Step 2: Move `pr_url` into the publisher and point the rebase at it**

Add to `src/flocc/publisher.py` (import `state_dir` from `flocc.context` at the top):

```python
def pr_url(rt, claim: Claim) -> str | None:
    path = state_dir(rt.cfg, claim.issue_key) / "pr-url"
    url = path.read_text().strip() if path.exists() else ""
    return url if url and url != "unknown" else None
```

Check there is no import cycle first: `grep -n '^from\|^import' src/flocc/context.py` must not import `publisher`.

In `src/flocc/rebase.py` replace `from flocc.a2atick import pr_url` with `from flocc.publisher import pr_url`. In `tests/test_rebase.py` replace any `flocc.a2atick` reference the same way (`grep -n a2atick tests/test_rebase.py`).

Run: `uv run pytest -q tests/test_rebase.py`
Expected: PASS.

- [ ] **Step 3: Delete the A2A modules and their tests**

```bash
git rm src/flocc/a2aclient.py src/flocc/a2aconf.py src/flocc/a2aserver.py src/flocc/a2atick.py src/flocc/mcp_a2a.py src/flocc/inbox.py src/flocc/delivery.py
git rm tests/test_a2aclient.py tests/test_a2aserver.py tests/test_a2atick.py tests/test_mcp_a2a.py tests/test_inbox.py tests/test_delivery.py
git rm deploy/flocc-a2a.service
git rm -r plugin/skills/coordinate
```

- [ ] **Step 4: Fix the Python callers**

`src/flocc/tick.py`: change `from flocc import a2atick, ledger, rebase` to `from flocc import ledger, rebase`, and delete:

```python
        # After poll, so an issue started this tick counts as live for the record comments.
        a2atick.step(rt)
```

`src/flocc/cli.py`:
- `from flocc import gate, inbox, ledger, permission, usage` → `from flocc import gate, ledger, permission, usage`.
- Delete the whole `cmd_inbox`, `cmd_a2a` and `cmd_mcp_a2a` functions, and the `_context_out` helper (only `cmd_inbox` and `cmd_stop` used it).
- In `cmd_stop`, delete `from flocc import delivery` and the whole block:

```python
    if (state / inbox.MARKER).exists() and not delivery.ending(state):
        try:
            text = delivery.take(state, time.time())
        except Exception as exc:  # noqa: BLE001
            print(f"flocc stop delivered nothing: {exc}", file=sys.stderr)
            text = ""
        if text:
            # Continues the turn; Claude Code's stop_hook_active protection bounds the loop.
            _context_out("Stop", text)
            return 0
```

  and change its comment `# The inbox and note_idle still run when this fails, so the supervisor sees the session idle.` to `# note_idle still runs when this fails, so the supervisor sees the session idle.`
- In `cmd_context`, replace

```python
    print(
        f"## Peers in this repo\n\n{names}. The flocc-a2a tool `agents` lists every host's"
        " issues; message one with `send`.\n"
    )
```

  with `print(f"## Peers in this repo\n\n{names}.\n")`. (Task 7 deletes the peers block.)
- In `cmd_status`, delete `if report.get("a2a"): print(f"  a2a service {report['a2a']}")` (two lines).
- In `COMMANDS`, delete `"inbox": cmd_inbox,`, `"a2a": cmd_a2a,`, `"mcp-a2a": cmd_mcp_a2a,`.
- In `build_parser`, delete the `a2a = add("a2a", ...)` line and its three `a2a.add_argument` lines, `add("mcp-a2a", "Run the flocc-a2a MCP server on stdio")` and `add("inbox", "Hook: hand this session its pending A2A messages")`, and change the `stop` help to `"Hook (Stop): answered-question check, then record the idle stop"`.

`src/flocc/sessions.py`: delete the `A2A_TOOLS` tuple; in `claude_argv` delete the `# Through --mcp-config: ...` comment and the `servers["flocc-a2a"] = {...}` assignment, and change `"allow": [*cfg.permission_allow, *A2A_TOOLS]` to `"allow": [*cfg.permission_allow]`. Keep the comment above `argv += ["--mcp-config", ...]`.

`src/flocc/status.py`: delete `_a2a_state` and the report entry `"a2a": _a2a_state(run) if mode == "systemd" else None,`.

`src/flocc/runtime.py`: the `pr_view` comment becomes `# The PR's state, head and mergeability; None in tests that never reach the rebase step.`

`src/flocc/context.py`: in `_COMMANDS`, delete the line `- the flocc-a2a tools \`agents\`, \`send\`, \`notify\` and \`replies\` reach agents on every host; see the coordinate skill.`

`plugin/hooks/hooks.json`: delete the inbox commands, so that `SessionStart` holds only `{ "type": "command", "command": "flocc session-start" }`, `UserPromptSubmit` holds only the `rm -f "$FLOCC_STATE/idle"` command, and the matcher-less `PostToolUse` entry (the `inbox-pending` one) is gone. Check: `python3 -m json.tool plugin/hooks/hooks.json >/dev/null && ! grep -n inbox plugin/hooks/hooks.json`.

`plugin/skills/implementer/SKILL.md`: delete the section

```markdown
## Changes other issues use

After changing something other issues use (an API, a shared test helper, a schema, CI), call the
`notify` tool for the repo with the changed paths, so live agents hear of it before they trip on
it.
```

`pyproject.toml`: delete

```toml
[project.optional-dependencies]
# Only flocc a2a serve imports these; the tick, the hooks and the MCP server stay stdlib.
a2a = ["a2a-sdk[http-server]>=1.2.3,<1.3", "uvicorn>=0.54,<1"]
```

and the two dev-group lines `"a2a-sdk[http-server]>=1.2.3,<1.3",` and `"uvicorn>=0.54,<1",`. Then run `uv lock` (regenerates `uv.lock`) and `uv sync`.

- [ ] **Step 5: Update the Python tests**

`tests/test_sessions.py`: delete `A2A_TOOLS,` from the import, the `a2a_server` helper, `test_no_rules_leave_auto_mode_alone_and_allow_only_the_a2a_tools` and `test_sessions_get_the_a2a_server_and_its_tools`; in `test_argv_wraps_claude_in_a_clean_env` change the expected `--mcp-config` value to `{"mcpServers": {"sentry": CFG.mcp_servers["sentry"]}}`; in `test_the_hosts_rules_and_environment_reach_the_session` change `assert s["permissions"]["allow"] == ["Bash(uv sync --frozen)", *A2A_TOOLS]` to `assert s["permissions"]["allow"] == ["Bash(uv sync --frozen)"]`; add:

```python
def test_no_rules_leave_auto_mode_alone_and_allow_nothing_extra():
    s = settings_of(argv_with())
    assert "autoMode" not in s and s["permissions"]["allow"] == []
    assert json.loads(argv_with()[-1]) == {"mcpServers": {}}
```

`tests/test_status.py`: in `test_systemd_tick_reads_the_service_timestamps` delete the two `report["a2a"]`/`["a2a"] is None` asserts, and in `FakeSystemctl.__call__` simplify the assert to `assert argv[:3] == ["systemctl", "--user", "show"]` and the return to `return type("R", (), {"stdout": self.stdout})()`.

`tests/test_plugin.py::test_hooks`: delete the `pending = ...` line and the four lines that use it (`post = ...`, the two `post` asserts) and change:

```python
    prompt = [h["command"] for e in hooks["UserPromptSubmit"] for h in e["hooks"]]
    assert prompt == [clear, pending]
```

to `assert [h["command"] for e in hooks["UserPromptSubmit"] for h in e["hooks"]] == [clear]`, and the last assert to `assert starts == ["flocc session-start"]`.

`tests/test_context.py::test_context_lists_issue_and_commands`: delete the needles `"flocc-a2a tools",` and `"coordinate skill",`.

Run:

```bash
uv run pytest -q tests/test_rebase.py tests/test_sessions.py tests/test_status.py tests/test_plugin.py tests/test_context.py tests/test_cli.py tests/test_run_loop.py tests/test_process.py
uv run pyrefly check
grep -rn -i 'a2a\|inbox\|delivery\|mcp-a2a' src tests plugin pyproject.toml
```

Expected: PASS; the grep prints nothing.

- [ ] **Step 6: Remove A2A from the desktop installer**

`deploy/embed.go`: line 7 `//go:embed flocc.service flocc.timer flocc-tmux.service flocc-a2a.service config.example.toml` → `//go:embed flocc.service flocc.timer flocc-tmux.service config.example.toml`; delete the `A2AUnit` comment and `const A2AUnit = "flocc-a2a.service"` (12-14). `deploy/embed_test.go:6`: `append(Units, A2AUnit, "config.example.toml")` → `append(Units, "config.example.toml")`.

`installer/internal/steps/list.go`: `ServiceSteps` returns `[]Step{Repo{}, Identity{}, Claude{}, ClaudeLogin{}, Secrets{}, Permissions{}, Tools{}, Config{}, Run{}}` (drop `A2A{}` only; the Permissions comment above stays). `git rm installer/internal/steps/a2a.go installer/internal/steps/a2a_test.go`.

`installer/internal/steps/repo.go:50`: `"uv", "tool", "install", "--force", "--editable", ".[a2a]"` → `"uv", "tool", "install", "--force", "--editable", "."`.

`git rm installer/internal/cli/a2a.go installer/internal/cli/a2a_test.go`.

`installer/internal/cli/ship.go`: delete the imports `net/netip`, `strings` and `safe` (their only uses are in the A2A block; `go vet` confirms); replace lines 56-62 (from `ips := peerAddrs(ctx, d)` through `doc, err := answersDoc(d.store(), h, listHosts(d), d.home, daytonaKey(ctx, d, h), ips)`) with

```go
		doc, err := answersDoc(d.store(), h, daytonaKey(ctx, d, h))
```

change `func answersDoc(st keys.Store, h hosts.Host, all []hosts.Host, home, tsKey string, ips map[string]string) ([]byte, error) {` to `func answersDoc(st keys.Store, h hosts.Host, tsKey string) ([]byte, error) {`, and delete inside it:

```go
	per, desk, err := a2aFiles(st, all, ips)
	if err != nil {
		return nil, err
	}
	if f, ok := per[h.Name]; ok {
		a.Secrets.A2A = &f
	}
	if err := writeDesktopA2A(home, desk); err != nil {
		return nil, err
	}
```

`installer/internal/cli/ship_test.go`: delete the A2A and desktop-file asserts (68-73), `a2aTodos` (85-93), `TestShipWarnsWhenAHostHasNoTailnetAddressAndShipsAnyway` and `TestShipDoesNotWarnWhenTheAddressIsKnown`; drop imports `go vet` reports unused.

`installer/internal/cli/create.go:66-78`: replace the doc comment and `Ship` method with

```go
func (h hostInstaller) Ship(ctx context.Context, name string) error { return shipHost(ctx, h.d, name) }
```

`installer/internal/keys/store.go`: delete `A2AOwner`, `A2AHost`, `EnsureToken` (32-48) and the `crypto/rand` and `encoding/hex` imports.

`installer/internal/answers/answers.go`: delete the `A2A *A2AFile` field (106), `A2AFile` and `A2APeer` (109-121) and the `if s.A2A != nil {…}` block in `Values` (134-139). `answers_test.go`: delete `TestSecretsValuesIncludeEveryA2AToken`.

`installer/internal/tailnet/policy.go`: delete `needA2A` (34), `a2aPort` (36), `isA2A` and its comment (57-61), the `hasA2A` field (156), the `if !f.hasA2A { m = append(m, needA2A) }` block in `Check` (209-211) and the `if !f.hasA2A { … }` block in `Merge` (289-295); lines 179-181 become

```go
			f.tagSource = f.tagSource || slices.Contains(r.Src, Tag)
```

Keep the `a+":7443"` entry in the deny test at line 311: it denies a port nothing serves now, and an existing policy keeps passing. A policy that still holds the old tag-to-tag grant now fails `Check` with the existing "remove it" message; that is the intended way the owner learns to delete it.

`installer/internal/tailnet/policy_test.go`: drop `"A2A"` from the three expected lists (26-29); delete `a2aLess`, `withGrant` if unused afterwards, `TestCheckWantsTheA2AGrant`, `TestTheA2AGrantIsNotATagSource`, the wildcard/extra-port test at line 108 and `TestAnAclWithTheA2AShapeIsNotTheA2AGrant`; rename `TestMergeAddsTheA2AGrantAndDenies7443` to `TestMergeDenies7443` and delete its `needA2A` and grant-count asserts (129-133), keeping the `desktop:7443`/`laptop:7443` loop; fix the grant count asserted near line 263. `installer/internal/cli/tailnet_test.go:35`: delete the `"grants":[…tcp:7443…]` entry from the fixture policy.

Run:

```bash
gofmt -w installer deploy
go build ./... && go vet ./...
go test ./deploy/ ./installer/internal/steps/ ./installer/internal/cli/ ./installer/internal/keys/ ./installer/internal/answers/ ./installer/internal/tailnet/
grep -rn -i 'a2a' installer deploy
```

Expected: PASS; the grep prints nothing.

- [ ] **Step 7: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests
git add -A src tests plugin deploy installer pyproject.toml uv.lock
git commit -s -m "Remove A2A: server, MCP tools, inbox hooks, tick step and ship's tokens"
```

---

### Task 4: Remove the permission broker

`flocc permission` (the `PermissionDenied`/`PermissionRequest` hook that logs and denies prompts) and `flocc permissions report`. The allow rules in `permissions.toml` and auto mode stay until plan 2.

**Files:**
- Delete: `src/flocc/permission.py`, `tests/test_permission.py`
- Modify: `src/flocc/cli.py` (import, `cmd_permission`, `cmd_permissions`, `COMMANDS`, parser)
- Modify: `plugin/hooks/hooks.json`, `tests/test_plugin.py::test_hooks`

**Interfaces:**
- Consumes: Task 3's `cli.py` import line `from flocc import gate, ledger, permission, usage`.
- Produces: `cli.py` imports `from flocc import gate, ledger, usage`.

- [ ] **Step 1: Change the hooks test to expect no permission hooks**

In `tests/test_plugin.py::test_hooks`, replace:

```python
    for event in ("PermissionDenied", "PermissionRequest"):
        (entry,) = hooks[event]
        assert "matcher" not in entry
        assert [h["command"] for h in entry["hooks"]] == ["flocc permission"]
        # Step 1's judge may use its whole 45 s; the default hook timeout would cut it off.
        assert entry["hooks"][0]["timeout"] == 60
```

with:

```python
    assert "PermissionDenied" not in hooks and "PermissionRequest" not in hooks
```

Run: `uv run pytest -q tests/test_plugin.py::test_hooks`
Expected: FAIL on the new assert.

- [ ] **Step 2: Delete the hook entries**

In `plugin/hooks/hooks.json`, delete these lines:

```json
    "PermissionDenied": [
      { "hooks": [ { "type": "command", "command": "flocc permission", "timeout": 60 } ] }
    ],
    "PermissionRequest": [
      { "hooks": [ { "type": "command", "command": "flocc permission", "timeout": 60 } ] }
    ],
```

- [ ] **Step 3: Delete the module, its test and its commands**

```bash
git rm src/flocc/permission.py tests/test_permission.py
```

In `src/flocc/cli.py`:
- Change `from flocc import gate, ledger, permission, usage` to `from flocc import gate, ledger, usage`.
- Delete the whole `def cmd_permission(args) -> int:` function and the whole `def cmd_permissions(args) -> int:` function.
- In `COMMANDS`, delete `"permission": cmd_permission,` and `"permissions": cmd_permissions,`.
- In `build_parser`, delete:

```python
    add("permission", "Hook: log an auto-mode block or prompt")
```

and:

```python
    perms = add("permissions", "Summarise the auto-mode blocks and prompts sessions met")
    perms.add_argument("action", choices=["report"], help="what to show")
    perms.add_argument("--days", type=int, default=7, help="how many days back")
```

- [ ] **Step 4: Run the tests**

Run: `uv run pytest -q tests/test_plugin.py tests/test_cli.py && uv run pyrefly check`
Expected: PASS; pyrefly reports no missing import.

- [ ] **Step 5: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests
git add -A src/flocc/cli.py src/flocc/permission.py tests/test_permission.py tests/test_plugin.py plugin/hooks/hooks.json
git commit -s -m "Remove the permission broker hook and its report"
```

---

### Task 5: Remove the gate, grants, launch budgets and question tracking

`gate.py` and `grants.py` go. What the Stop hook still needs (the idle and background markers) moves to a new `idle.py`; `AMENDED` moves to `publisher.py`. With the gate gone nothing writes `asking`, `launches.jsonl` or `asked-seconds`, so every reader of them goes too. The catalogue agents in `plugin/agents/` stay, without budgets.

**Files:**
- Create: `src/flocc/idle.py`, `tests/test_idle.py`
- Delete: `src/flocc/gate.py`, `src/flocc/grants.py`, `tests/test_gate.py`, `tests/test_grants.py`
- Modify: `src/flocc/publisher.py:10,` (`AMENDED`), `src/flocc/cli.py`, `src/flocc/supervisor.py`, `src/flocc/poller.py`, `src/flocc/sessions.py`, `src/flocc/status.py`, `src/flocc/agentblock.py`, `src/flocc/context.py`
- Modify: `plugin/hooks/hooks.json`, `plugin/skills/implementer/SKILL.md`, `plugin/skills/tester/SKILL.md`
- Modify (Go): `installer/internal/ship/fleet.go:93-94`, `installer/internal/ship/fleet_test.go:516-517`, `installer/internal/dash/issue.go:156`, `installer/internal/dash/model_test.go:51-52` and its `claim(` callers, `installer/internal/dash/issue_test.go:46`, `installer/internal/dash/testdata/issue-80x24.golden`, `installer/internal/dash/testdata/issue-140x40.golden`
- Test: `tests/test_cli.py`, `tests/test_supervisor.py`, `tests/test_poller.py`, `tests/test_sessions.py`, `tests/test_status.py`, `tests/test_agentblock.py`, `tests/test_plugin.py`, `tests/test_context.py`

**Interfaces:**
- Consumes: Task 4's `cli.py` import `from flocc import gate, ledger, usage`.
- Produces: `idle.note_idle(state: Path, payload: dict, now: float) -> None` writing `idle` as `{"at": float, "message": str}`; `idle.note_background(state: Path, payload: dict) -> None`; `idle.has_background(state: Path) -> bool`; `idle.clear_idle(state: Path) -> None`; `idle.BACKGROUND = "background"`; `idle.IDLE_MESSAGE_CHARS = 600`; `publisher.AMENDED = "tests-amended.json"`; `supervisor.launch(rt, claim, phase, extra="")` (no `started_at` parameter); `sessions.claude_argv(cfg, name, settings, prompt, env, claude_exe, exe, disabled_plugins=(), remote_control=True)` (no `grants` parameter); status claim rows without `launches`/`launch_limit`.

- [ ] **Step 1: Write `tests/test_idle.py`**

The idle-marker tests move from `tests/test_gate.py`, minus the `waiting_on_owner` ones:

```python
import json

import pytest

from flocc.idle import clear_idle, has_background, note_background, note_idle


def _assistant(*blocks):
    return json.dumps({"type": "assistant", "message": {"content": list(blocks)}})


def test_idle_message_skips_assistant_lines_without_text(tmp_path):
    transcript = tmp_path / "t.jsonl"
    transcript.write_text(
        "\n".join(
            [
                _assistant({"type": "text", "text": "waiting on you"}),
                _assistant({"type": "tool_use", "name": "Bash"}),
                _assistant({"type": "thinking", "thinking": "hm"}),
            ]
        )
    )
    note_idle(tmp_path, {"transcript_path": str(transcript)}, 5.0)
    assert json.loads((tmp_path / "idle").read_text()) == {"at": 5.0, "message": "waiting on you"}


@pytest.mark.parametrize(
    "content",
    [None, b"\xff\xfe", json.dumps({"type": "assistant", "message": "text"}), "[1]"],
    ids=["missing", "undecodable", "message-not-a-dict", "line-not-an-object"],
)
def test_idle_marker_is_written_when_the_transcript_cannot_be_read(tmp_path, content):
    transcript = tmp_path / "t.jsonl"
    if isinstance(content, bytes):
        transcript.write_bytes(content)
    elif content is not None:
        transcript.write_text(content)
    note_idle(tmp_path, {"transcript_path": str(transcript)}, 5.0)
    marker = json.loads((tmp_path / "idle").read_text())
    assert marker["at"] == 5.0 and marker["message"] == ""


def test_background_marker_follows_the_last_stop(tmp_path):
    note_background(tmp_path, {"background_tasks": [{"type": "shell"}]})
    assert has_background(tmp_path)
    note_background(tmp_path, {"background_tasks": []})
    assert not has_background(tmp_path)


def test_clear_idle_removes_the_marker_and_its_notice_mark(tmp_path):
    (tmp_path / "idle").write_text("{}")
    (tmp_path / "idle-escalated").write_text("1.0")
    clear_idle(tmp_path)
    assert not (tmp_path / "idle").exists() and not (tmp_path / "idle-escalated").exists()
```

Run: `uv run pytest -q tests/test_idle.py`
Expected: FAIL with `ModuleNotFoundError: No module named 'flocc.idle'`.

- [ ] **Step 2: Create `src/flocc/idle.py`**

```python
import json
import os
from pathlib import Path

IDLE_MESSAGE_CHARS = 600
BACKGROUND = "background"


def _transcript_text(path: str) -> str:
    for line in reversed(Path(path).read_text().splitlines()):
        try:
            entry = json.loads(line)
        except ValueError:
            continue
        if not isinstance(entry, dict) or entry.get("type") != "assistant":
            continue
        content = (entry.get("message") or {}).get("content")
        if isinstance(content, str):
            text = content
        else:
            blocks = content if isinstance(content, list) else []
            texts = (
                b.get("text") for b in blocks if isinstance(b, dict) and b.get("type") == "text"
            )
            text = "\n".join(t for t in texts if isinstance(t, str))
        # A line holding only tool calls says nothing; the turn's last words are further back.
        if text:
            return text
    return ""


def note_idle(state: Path, payload: dict, now: float) -> None:
    message = payload.get("last_assistant_message")
    if not isinstance(message, str) or not message:
        message = ""
        transcript = payload.get("transcript_path")
        if isinstance(transcript, str) and transcript:
            # The marker matters more than the message: write it whatever the transcript holds.
            try:
                message = _transcript_text(transcript)
            except (OSError, ValueError, AttributeError):
                pass
    state.mkdir(parents=True, exist_ok=True)
    # Written whole by rename: the supervisor polls this file.
    tmp = state / "idle.tmp"
    tmp.write_text(json.dumps({"at": now, "message": message[:IDLE_MESSAGE_CHARS]}))
    os.replace(tmp, state / "idle")


def note_background(state: Path, payload: dict) -> None:
    """Records the background tasks a turn ended with; the supervisor treats them as work."""
    kinds = [
        str(t.get("type", "")) for t in payload.get("background_tasks") or [] if isinstance(t, dict)
    ]
    path = state / BACKGROUND
    if not kinds:
        path.unlink(missing_ok=True)
        return
    tmp = state / "background.tmp"
    tmp.write_text(json.dumps(kinds))
    os.replace(tmp, path)


def has_background(state: Path) -> bool:
    try:
        return bool(json.loads((state / BACKGROUND).read_text()))
    except (OSError, ValueError):
        return False


def clear_idle(state: Path) -> None:
    for name in ("idle", "idle-escalated"):
        (state / name).unlink(missing_ok=True)
```

Run: `uv run pytest -q tests/test_idle.py`
Expected: PASS.

- [ ] **Step 3: Move `AMENDED` into the publisher**

In `src/flocc/publisher.py`, delete `from flocc.gate import AMENDED` and add below the imports:

```python
AMENDED = "tests-amended.json"
```

In `src/flocc/cli.py::cmd_tests_amended`, change `path = state / gate.AMENDED` to:

```python
    from flocc.publisher import AMENDED

    path = state / AMENDED
```

(put the import beside the existing `from flocc.worktree import changed_files` at the top of the function).

- [ ] **Step 4: Cut the gate and grants out of `cli.py`**

- Change `from flocc import gate, ledger, usage` to `from flocc import idle, ledger, usage`.
- Delete `from flocc.grants import for_phase`.
- Delete the whole `def cmd_gate(args) -> int:` function.
- In `cmd_idle`, change `gate.note_idle(` to `idle.note_idle(` and the comment `# Exit 2 on Stop blocks the stop and keeps the session running; see cmd_gate.` to `# Exit 2 on Stop blocks the stop and keeps the session running.`
- Replace `cmd_stop` with:

```python
def cmd_stop(args) -> int:
    # Exit 2 on Stop blocks the stop; bookkeeping failures exit 1 as cmd_idle does.
    try:
        state = _state_dir()
        payload = _hook_input()
    except Exception as exc:  # noqa: BLE001
        print(f"flocc stop could not read its input: {exc}", file=sys.stderr)
        return 1
    # note_idle still runs when this fails, so the supervisor sees the session idle.
    failed = 0
    try:
        idle.note_background(state, payload)
    except Exception as exc:  # noqa: BLE001
        print(f"flocc stop could not record the background tasks: {exc}", file=sys.stderr)
        failed = 1
    try:
        idle.note_idle(state, payload, time.time())
    except Exception as exc:  # noqa: BLE001
        print(f"flocc stop could not record the stop: {exc}", file=sys.stderr)
        return 1
    return failed
```

- In `_session_bookkeeping`, delete the `else:` branch and its three lines:

```python
    else:
        # Background agents survive compaction, so only a real start resets the running count.
        run = os.environ.get("FLOCC_RUN")
        (state / gate.LAST_STOP).touch()
        gate.record_session(state, run, time.time(), os.environ.get("FLOCC_PHASE"))
```

- In `cmd_context`, delete the grants block:

```python
    g = for_phase(state / "grants.json", os.environ.get("FLOCC_PHASE", ""))
    items = [f"launches {g.launches}"] if g.launches is not None else []
    items += [f"agent {a}" for a in g.agents] + [f"workflow {w}" for w in g.workflows]
    items += [f"fix-rounds {g.fix_rounds}"] if g.fix_rounds is not None else []
    print(f"## Grants\n\n{', '.join(items) or 'none'}.\n")
```

- In `cmd_status`, replace:

```python
        line = f"  {c['issue']}  {c['phase']}  {c['state']}  ctx {_pct(c['context_pct'])}  {alive}"
        print(f"{line}  launches {c['launches']}/{c['launch_limit']}")
```

with:

```python
        print(f"  {c['issue']}  {c['phase']}  {c['state']}  ctx {_pct(c['context_pct'])}  {alive}")
```

- In `COMMANDS`, delete `"gate": cmd_gate,`.
- In `build_parser`, delete:

```python
    gate = add("gate", "Hook: allow or refuse a subagent or workflow launch, track questions")
    which = gate.add_mutually_exclusive_group()
    which.add_argument("--stopped", action="store_true", help="SubagentStop: an agent finished")
    which.add_argument("--answered", action="store_true", help="PostToolUse: a question answered")
```

and change the `stop` parser line to `add("stop", "Hook (Stop): record background tasks and the idle stop")`.

- [ ] **Step 5: Cut them out of the supervisor**

In `src/flocc/supervisor.py`:
- Delete `from flocc import grants as grants_mod` and the whole `from flocc.gate import (...)` block; add `from flocc.idle import BACKGROUND, clear_idle, has_background`.
- Delete `QUESTION_WAIT_S = 7200`.
- Change the signature `def launch(rt, claim: Claim, phase: str, extra: str = "", started_at: float | None = None) -> None:` to `def launch(rt, claim: Claim, phase: str, extra: str = "") -> None:`, delete `start = claim.started_at if started_at is None else started_at`, and in the `clean_env({...})` dict delete the two lines:

```python
            # Handoffs and crash relaunches of one phase share its launch budget.
            "FLOCC_RUN": run_id(phase, start),
```

- Replace:

```python
    settings = settings_from_json(claim.settings_json)
    if settings.allow:
        grants_mod.set_block(d / "grants.json", grants_mod.parse_allow(settings.allow))
    granted = grants_mod.for_phase(d / "grants.json", phase)
    env[LAUNCH_GRANTS_ENV] = ",".join(sorted(grants_mod.launch_flags(granted)))
```

with `settings = settings_from_json(claim.settings_json)`, and delete `grants=granted,` from the `claude_argv(...)` call.
- Delete `clear_question(d)` and the three lines:

```python
    # A new phase or claim starts a new clock; a restart of the same session keeps its credit.
    if started_at is not None:
        (d / ASKED).unlink(missing_ok=True)
```

- In every `launch(rt, claim, ..., started_at=start)` call in `supervisor.py`, delete the `, started_at=start` argument (five calls: in `_start_review`, `_finish_phase` test→impl, `_start_review_fix`, `start_rebase`, and the check-fix round). Leave `started_at=start` in the `ledger.update_claim(...)` calls alone. Check with `grep -n 'launch(rt' src/flocc/supervisor.py src/flocc/poller.py`: no `started_at` may remain on a `launch(` line.
- Delete the whole `_asked_seconds` function and the whole `_check_question` function, and the call `_check_question(rt, claim, d, name)` at the end of `supervise`.
- In `_check_idle`, replace:

```python
    if not idle.exists() or (d / "asking").exists():
        return
    try:
        raw = json.loads(idle.read_text())
        at, message = float(raw["at"]), str(raw.get("message", ""))
        waiting = raw.get("waiting_on_owner") is True
    except (OSError, ValueError, KeyError, TypeError):
        return
    escalated = d / "idle-escalated"
    if waiting or (escalated.exists() and escalated.read_text() == str(at)):
        return
```

with:

```python
    if not idle.exists():
        return
    try:
        raw = json.loads(idle.read_text())
        at, message = float(raw["at"]), str(raw.get("message", ""))
    except (OSError, ValueError, KeyError, TypeError):
        return
    escalated = d / "idle-escalated"
    if escalated.exists() and escalated.read_text() == str(at):
        return
```

and delete:

```python
    if running_agents(d, run_id(claim.phase, claim.started_at), claim.phase):
        return
```

from both `_check_idle` and `_check_stall`.
- In `_check_stall`, change `if (d / "idle").exists() or (d / "asking").exists():` to `if (d / "idle").exists():`, and in its opening comment change `Ended turns and open questions have their own checks.` to `Ended turns have their own check.`
- In `supervise`, replace:

```python
    elapsed = rt.now() - claim.started_at
    if claim.phase == "review":
        limit, held = rt.cfg.review_wall_clock_s, False
    else:
        limit = claim.wall_clock_s + _asked_seconds(d)
        # A session waiting on an answer burns no tokens. The owner is paged QUESTION_WAIT_S after
        # the question and gets as long again to answer; measured from the limit instead, a
        # question asked near it would be paged and killed in the same tick. A torn marker holds
        # nothing, or every tick would read it as a fresh question.
        held = False
        if (d / "asking").exists():
            try:
                asked_at = float(json.loads((d / "asking").read_text())["at"])
            except (OSError, ValueError, KeyError, TypeError):
                asked_at = float("-inf")
            held = rt.now() - asked_at <= 2 * QUESTION_WAIT_S
    if elapsed > limit and not held:
```

with:

```python
    elapsed = rt.now() - claim.started_at
    limit = rt.cfg.review_wall_clock_s if claim.phase == "review" else claim.wall_clock_s
    if elapsed > limit:
```

- In the handoff condition in `supervise`, delete the line `not (d / "asking").exists()` and its `and`, and the sentence `Not while a question is open: the keystrokes could select an option in its dialog.` from the comment above it (Task 8 removes the rest of that block).

- [ ] **Step 6: Cut them out of the poller, sessions, status, agent block and context**

`src/flocc/poller.py`:
- Delete `from flocc.grants import GrantError, add_comment, allow_lines, parse_allow`.
- Delete the whole `_apply_grants` function, and in `forward_comments` delete the two lines `# After delivery: ...` comment and `_apply_grants(rt, claim, comment.body)`.
- In `forward_comments`, replace:

```python
                # A dead session is relaunched by the next tick's supervisor, which delivers
                # this reply and the ones after it. While a question is open, typing into the
                # pane could select an option in the dialog, so the reply waits for the answer.
                if (
                    not rt.sessions.alive(name)
                    or (state_dir(rt.cfg, claim.issue_key) / "asking").exists()
                ):
                    break
```

with:

```python
                # A dead session is relaunched by the next tick's supervisor, which delivers
                # this reply and the ones after it.
                if not rt.sessions.alive(name):
                    break
```

- In `_start`, replace the stale list and its comment:

```python
    # A previous claim's grants, scratchpads, compaction and question belong to that run; the
    # agent block's grants are written again at launch.
    for stale in (
        "grants.json",
        "scratchpads",
        "compacted",
        "asking",
        "asking-escalated",
        "idle",
        "idle-escalated",
        "asked-seconds",
    ):
```

with:

```python
    # A previous claim's scratchpads, compaction and idle marker belong to that run.
    for stale in ("scratchpads", "compacted", "idle", "idle-escalated"):
```

`src/flocc/sessions.py`:
- Delete `from flocc.grants import Grants`, `_GENERAL_DENY = "Agent(general-purpose)"` and `NO_GRANTS = Grants()`.
- In `_DENY`, change the comment `# A second claude process is a session the gate and the usage caps never see.` to `# A second claude process is a session the usage caps never see.`, and add `"Agent(general-purpose)",` as the last entry of the list.
- In `claude_argv`, delete the parameter `grants: Grants = NO_GRANTS,`, replace:

```python
    deny = list(_DENY)
    if "general-purpose" not in grants.agents:
        deny.append(_GENERAL_DENY)
    permissions: dict = {"deny": deny, "allow": [*cfg.permission_allow, *A2A_TOOLS]}
```

with (after Task 3 removed `A2A_TOOLS` the last line already reads `"allow": [*cfg.permission_allow]`):

```python
    permissions: dict = {"deny": list(_DENY), "allow": [*cfg.permission_allow]}
```

and change `"disableWorkflows": not grants.workflows,` to `"disableWorkflows": True,`.

`src/flocc/status.py`:
- Delete `from flocc.gate import BUDGETS, launches_used, run_id` and `from flocc.grants import for_phase`.
- In `_claim_row`, delete the four lines from `run = run_id(claim.phase, claim.started_at)` through `limit = granted if granted is not None else (budget.launches if budget else 0)`, and the two dict entries `"launches": launches_used(state_dir, run),` and `"launch_limit": limit,`.

`src/flocc/agentblock.py`:
- Delete `from flocc.grants import GrantError, parse_allow`.
- Change `_KEYS = {"model", "effort", "wall_clock", "paths", "depends_on", "mcp", "allow"}` to `_KEYS = {"model", "effort", "wall_clock", "paths", "depends_on", "mcp"}`.
- Delete the `allow: str | None = None` field from `IssueSettings`, the block from `allow = values.get("allow") or None` through `raise BlockError(f"agent block: {exc}") from exc`, `allow=allow,` in the `parse_settings` return, and `allow=raw.get("allow"),` in `settings_from_json`. A stored claim's `settings_json` may still hold `"allow"`; `settings_from_json` reads keys by name, so it is ignored.

`src/flocc/context.py`: in `_COMMANDS`, delete the line:

```
- "Grants" below lists what you may do beyond the defaults; the gate refuses anything else.
```

- [ ] **Step 7: Remove the gate hooks**

In `plugin/hooks/hooks.json`, delete the gated `PreToolUse` entry:

```json
      {
        "matcher": "Agent|Task|Workflow|AskUserQuestion",
        "hooks": [
          { "type": "command", "command": "flocc gate", "timeout": 10, "onFailure": "block" }
        ]
      },
```

the whole `"PostToolUse"` and `"PostToolUseFailure"` keys (after Task 3 each holds only the `AskUserQuestion` / `flocc gate --answered` entry), and the whole `"SubagentStop"` key with its entry. Fix the trailing comma after the `"Stop"` entry so the file stays valid JSON. Check: `python3 -m json.tool plugin/hooks/hooks.json >/dev/null`.

The file then reads:

```json
{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          { "type": "command", "command": "flocc session-start" }
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [
          { "type": "command", "command": "[ -z \"$FLOCC_STATE\" ] || rm -f \"$FLOCC_STATE/idle\"" }
        ]
      }
    ],
    "PreToolUse": [
      {
        "hooks": [ { "type": "command", "command": "[ -z \"$FLOCC_STATE\" ] || rm -f \"$FLOCC_STATE/idle\"" } ]
      }
    ],
    "Stop": [
      { "hooks": [ { "type": "command", "command": "flocc stop" } ] }
    ]
  }
}
```

Write it with exactly this content if the deletions leave anything else.

- [ ] **Step 8: Strip the gate and grants from the skills**

`plugin/skills/implementer/SKILL.md`:
- Delete the bullet that starts `- Run \`flocc context\` and read "Grants" before planning;` (three lines).
- Replace the bullet that starts `- Plan first: write \`plan.md\`` (eight lines, through `itself is too large, and propose the split.`) with:

```markdown
- Plan first: write `plan.md` in your state directory with numbered tasks, the paths each task
  touches, and its tests. Tasks that share a path go in different waves; a wave holds at most four
  tasks. Keep the plan small: one plan review, one implementer per task, one reviewer per wave,
  one fix and its re-review. Merge small tasks or do them inline rather than adding agents.
```

- Replace the sentence `A second round needs a \`fix-rounds\` grant;
  without one, list the findings left after the first round in your \`flocc done\` summary and
  finish; the service's reviewer reads the PR.` with `List the findings left after that round in your \`flocc done\` summary and finish; the service's reviewer reads the PR.`
- Replace the bullet that starts `- Start general-purpose agents and workflows only when "Grants" lists them.` (five lines) with:

```markdown
- Start only the `flocc:` agents named here. Never start general-purpose agents, workflows or
  teammates, never pass `model`, `effort`, `isolation`, `name` or `team_name` when starting an
  agent, and never run `claude` from the shell; the session settings block these.
```

`plugin/skills/tester/SKILL.md`: replace `with today's date in the task; you have two launches. Start no
  other agents.` with `with today's date in the task. Start no other agents.`

- [ ] **Step 9: Update the Python tests**

Delete `tests/test_gate.py` and `tests/test_grants.py` (`git rm`).

`tests/test_plugin.py`:
- Replace `from flocc.gate import CATALOGUE` with nothing, and in `test_every_catalogue_agent_has_a_file_with_its_model_and_effort` replace `assert set(CATALOGUE) == SPEC.keys()` with `assert {p.stem for p in (PLUGIN / "agents").glob("*.md")} == SPEC.keys()`.
- Replace the body of `test_hooks` with:

```python
def test_hooks():
    hooks = json.loads((PLUGIN / "hooks" / "hooks.json").read_text())["hooks"]
    assert set(hooks) == {"SessionStart", "UserPromptSubmit", "PreToolUse", "Stop"}
    assert [h["command"] for h in hooks["Stop"][0]["hooks"]] == ["flocc stop"]
    # The idle marker means "the last event was a Stop", so any later activity removes it.
    clear = '[ -z "$FLOCC_STATE" ] || rm -f "$FLOCC_STATE/idle"'
    assert [h["command"] for e in hooks["PreToolUse"] for h in e["hooks"]] == [clear]
    assert [h["command"] for e in hooks["UserPromptSubmit"] for h in e["hooks"]] == [clear]
    starts = [h["command"] for e in hooks["SessionStart"] for h in e["hooks"]]
    assert starts == ["flocc session-start"]
```

`tests/test_cli.py`:
- Delete `from flocc.grants import Grants, set_block`.
- Rename the helper `gate_env` to `session_env` everywhere in the file (Edit with `replace_all`), and delete its two lines `monkeypatch.setenv("FLOCC_RUN", f"{phase}@1")` and the `FLOCC_LAUNCH_GRANTS` comment plus `monkeypatch.delenv("FLOCC_LAUNCH_GRANTS", raising=False)`.
- Delete these tests: `test_gate_command_denies_with_hook_json`, `test_gate_allows_a_flag_grant_only_for_the_session_launched_with_it`, `test_a_question_is_never_blocked_by_bookkeeping`, `test_gate_refuses_with_exit_2_when_it_breaks`, `test_gate_without_a_state_dir_refuses`, `test_gate_records_and_clears_a_question`, `test_gate_answered_credits_the_time_the_question_was_open`, `test_bookkeeping_failures_never_exit_2` (with its `@pytest.mark.parametrize("flag", ...)` line), `test_stopped_records_the_agent_type_and_phase`, `test_gate_refuses_input_it_cannot_read` (with its parametrize line), `test_gate_refuses_when_it_cannot_print_the_denial`, `test_context_prints_the_phase_grants`.
- In `test_session_start_on_compaction`, delete the last two asserts (`launches.jsonl` and `last-stop`).
- Replace `test_session_start_on_startup_records_a_session` and `test_session_start_prints_the_context_when_bookkeeping_fails` with:

```python
def test_session_start_on_startup_prints_the_context(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setenv("FLOCC_LEDGER", str(tmp_path / "l.sqlite"))
    (tmp_path / "context.md").write_text("CTX")
    monkeypatch.setattr("sys.stdin", io.StringIO('{"source": "startup"}'))
    assert main(["session-start"]) == 0
    assert "CTX" in capsys.readouterr().out
    assert not (tmp_path / "launches.jsonl").exists()


def test_session_start_prints_the_context_when_bookkeeping_fails(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setenv("FLOCC_LEDGER", str(tmp_path / "l.sqlite"))
    (tmp_path / "context.md").write_text("CTX")
    (tmp_path / "scratchpads").mkdir()
    payload = {"source": "startup", "scratchpad_dir": "/tmp/claude-1000/x/s1/scratchpad"}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    assert main(["session-start"]) == 0
    captured = capsys.readouterr()
    assert "CTX" in captured.out and "bookkeeping failed" in captured.err
```

- In `test_idle_bookkeeping_failure_exits_1_never_2`, change `"flocc.gate.note_idle"` to `"flocc.idle.note_idle"`.
- Run `uv run ruff check --fix tests/test_cli.py` to drop imports the deleted tests left unused.

`tests/test_supervisor.py`:
- Delete `from flocc.gate import record_session, record_stop, run_gate, run_id` and `from flocc.grants import Grants, add_comment, for_phase`; change `from flocc.supervisor import QUESTION_WAIT_S, LaunchError, supervise` to `from flocc.supervisor import LaunchError, supervise`.
- Delete these tests (with any `@pytest.mark.parametrize` line directly above them): `test_an_open_question_holds_off_the_wall_clock`, `test_the_time_a_question_was_open_is_credited_to_the_wall_clock`, `test_a_question_left_open_forever_times_out_after_the_wait`, `test_a_torn_question_marker_does_not_hold_off_the_wall_clock`, `test_an_open_question_never_holds_off_the_review_limit`, `test_phase_sessions_share_one_run_id_across_relaunches`, `test_launch_records_block_grants_without_beating_comment_grants`, `test_a_session_waiting_on_background_agents_is_left_alone`, `test_a_session_that_already_escalated_and_waits_is_left_alone`, `test_an_idle_session_with_an_open_question_is_left_to_the_question_check`, `test_a_still_screen_while_background_agents_run_is_not_a_stall`, `test_a_stale_question_is_escalated_once`, `test_no_handoff_is_requested_while_a_question_is_open`, `test_a_tester_question_escalation_points_at_the_pane`, `test_an_implementer_question_escalation_points_at_the_app`, `test_a_fresh_question_is_not_escalated`, `test_launch_applies_block_grants_to_the_session`, `test_launch_tells_the_gate_which_grants_the_session_started_with`, `test_launch_without_flag_grants_sets_an_empty_value`, `test_a_malformed_question_file_is_ignored`.
- Change `@pytest.mark.parametrize("marker", ["idle", "asking"])` (above `test_an_ended_turn_or_open_question_is_not_a_stall`) to `@pytest.mark.parametrize("marker", ["idle"])`, and rename the test to `test_an_ended_turn_is_not_a_stall`.
- In `test_relaunch_clears_compaction_and_question_markers`, change `names = ("compacted", "asking", "asking-escalated", "idle", "idle-escalated")` to `names = ("compacted", "idle", "idle-escalated")` and rename the test to `test_relaunch_clears_compaction_and_idle_markers`.
- Replace `test_review_and_fix_launches_carry_their_own_run_id_and_remote_control` with:

```python
def test_only_fix_launches_get_remote_control(rt):
    rt.publish_fn = lambda c, t, s: PublishResult("check_failed", "E1 boom")
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))  # check fails: fix
    argv = rt.sessions.started[-1][1]
    assert current(rt).phase == "fix" and "--remote-control" in argv
    assert "FLOCC_RUN" not in env_of(argv)
    rt.publish_fn = lambda c, t, s: PublishResult("opened", url="https://pr/1")
    done(rt)
    supervise(rt, current(rt))  # PR opens: review
    argv = rt.sessions.started[-1][1]
    assert current(rt).phase == "review" and "--remote-control" not in argv
```

- Run `uv run ruff check --fix tests/test_supervisor.py`.

`tests/test_poller.py`:
- Delete the grants import line (`from flocc.grants import Grants, for_phase, set_block`).
- Delete `test_a_reply_is_held_while_a_question_is_open_and_delivered_after`, `test_an_open_question_does_not_hold_other_claims`, `test_owner_and_steer_author_allow_comments_grant_for_the_running_phase`, `test_bad_allow_comment_is_answered_once_and_keeps_grants`, `test_grants_wait_for_a_live_session`.
- Replace `test_a_new_claim_starts_without_the_last_claims_state` with:

```python
def test_a_new_claim_starts_without_the_last_claims_state(rt):
    d = state_dir(rt.cfg, "NF-9")
    d.mkdir(parents=True)
    names = ("scratchpads", "compacted", "idle", "tests-amended.json")
    for name in names:
        (d / name).write_text("x")
    rt.linear.ready = [issue()]
    poll(rt)
    for name in names:
        assert not (d / name).exists()
```

`tests/test_sessions.py`:
- Delete `from flocc.grants import Grants` and `test_grants_lift_the_matching_launch_flags`.

`tests/test_status.py`: delete `test_claims_carry_launches_against_budget` and the `run_gate` import.

`tests/test_agentblock.py`: replace `test_a_huge_allow_number_is_a_block_error` and `test_allow_key_is_kept_raw_and_validated` with:

```python
def test_allow_is_now_an_unknown_key():
    with pytest.raises(BlockError, match="unknown keys"):
        parse_settings("```agent\nallow: launches 4\n```")
```

`tests/test_context.py`: nothing yet (Task 6 and Task 7 change its needle list).

- [ ] **Step 10: Drop launches from the desktop's status decode and the dashboard**

`installer/internal/ship/fleet.go`: delete the two fields

```go
	Launches    int            `json:"launches"`
	LaunchLimit int            `json:"launch_limit"`
```

then run `gofmt -w installer/internal/ship/fleet.go` (field alignment changes).

`installer/internal/ship/fleet_test.go:516-517`: in the JSON literal delete `"launches":1,"launch_limit":2,`, and change the condition to `if c := current.Claims[0]; len(c.Activity) != 1 || c.Activity[0].Text != "reading" {`.

`installer/internal/dash/issue.go:156`: replace

```go
		m.label(7, "session", fmt.Sprintf("%s%scontext %s%slaunches %d/%d", session, theme.Sep, ctx, theme.Sep, c.Launches, c.LaunchLimit))
```

with

```go
		m.label(7, "session", fmt.Sprintf("%s%scontext %s", session, theme.Sep, ctx))
```

`installer/internal/dash/model_test.go:51-52`: change the helper to

```go
func claim(issue, phase, state string, pct float64, upd float64, act ...string) ship.Claim {
	c := ship.Claim{Issue: issue, Phase: phase, State: state, ContextPct: f64(pct),
```

keeping the rest of that struct literal as it is, and remove the fifth argument from every `claim(` call in `installer/internal/dash/*_test.go` (find them with `grep -n 'claim("' installer/internal/dash/*_test.go`; e.g. `claim("NF-240", "fix", "blocked", 7, 0, 86400, ...)` becomes `claim("NF-240", "fix", "blocked", 7, 86400, ...)`).

`installer/internal/dash/issue_test.go:46`: change `"session  none · context 7% · launches 0/2",` to `"session  none · context 7%",`.

Regenerate the goldens and check the diff is only the dropped `· launches 0/2`:

```bash
go test ./installer/internal/dash/ -run Golden -update
git diff --stat installer/internal/dash/testdata
git diff installer/internal/dash/testdata | grep '^[-+] ' | grep -v 'launches 0/2' | grep -v 'context 7%'
```

Expected: the last command prints nothing.

- [ ] **Step 11: Run the tests**

```bash
uv run pytest -q tests/test_idle.py tests/test_cli.py tests/test_supervisor.py tests/test_poller.py tests/test_sessions.py tests/test_status.py tests/test_agentblock.py tests/test_plugin.py tests/test_publisher.py tests/test_context.py
uv run pyrefly check
go test ./installer/internal/ship/... ./installer/internal/dash/...
grep -rn 'flocc.gate\|flocc.grants\|from flocc import.*gate\|FLOCC_RUN\|FLOCC_LAUNCH_GRANTS\|asked-seconds\|"asking"' src tests
```

Expected: tests PASS; pyrefly clean; the grep prints nothing.

- [ ] **Step 12: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests && gofmt -l installer
git add -A src tests plugin installer/internal/ship installer/internal/dash
git commit -s -m "Remove the subagent gate, grants, launch budgets and question tracking"
```

---

### Task 6: Replace escalation and ntfy with a plain notice

`escalation.py`, `flocc escalate`, the `escalations.jsonl` route, tiers, the `escalation`/`needs-decision` labels, the shared escalation issue, ntfy pushes and the escalate skill go. What the service itself reports goes through a new 30-line `notice.py`: a comment on the issue, or a digest line when there is no issue.

**Files:**
- Create: `src/flocc/notice.py`, `tests/test_notice.py`
- Delete: `src/flocc/escalation.py`, `tests/test_escalation.py`, `plugin/skills/escalate/`, `plugin/evals/escalate-irreversible/`, `plugin/evals/escalate-reversible/`, `plugin/evals/escalate-shared-cause/`
- Modify: `src/flocc/{ledger,tick,poller,supervisor,tokens,cli,doctor,config,linear,context}.py`, `deploy/config.example.toml`, `plugin/skills/{implementer,tester,reviewer}/SKILL.md`, `tests/fakes.py`
- Modify (Go): `installer/internal/keys/{store,envfile,fill}.go` and tests, `installer/internal/cli/{cli,keys,setup,host,ship}.go` and tests, `installer/internal/answers/answers.go`, `installer/internal/answers/topic_test.go` (delete), `installer/cmd/flocc-setup/main.go`, `installer/internal/ui/huh.go`, `installer/internal/steps/config.go`, `installer/internal/steps/config_test.go`, `installer/internal/steps/testdata/config{,.skip}.golden.toml`, `installer/internal/hosts/hosts_test.go`, `installer/testdata/e2e-answers.toml`
- Test: `tests/test_{ledger,poller,supervisor,tokens,cli,doctor,config,installer_config,linear,context,rebase}.py`

**Interfaces:**
- Consumes: `ledger._MIGRATIONS` from Task 1 (one entry).
- Produces: `notice.Notice(cause: str, summary: str, action: str = "", issue_key: str | None = None)` with `.text() -> str`; `notice.Notifier(con, linear, now)` with `.send(notice: Notice, issue: Issue | None = None) -> None`; `ledger.DEDUPE_S = 24 * 3600`; `ledger._MIGRATIONS` with a second entry dropping `claims.escalation_offset`; `Claim` without `escalation_offset`; `Config` without `ntfy_url`; `config._RETIRED = ("ntfy_url",)`.

- [ ] **Step 1: Write the failing tests**

`tests/test_notice.py`:

```python
from flocc import ledger
from flocc.notice import Notice, Notifier
from tests.fakes import ISSUE, FakeLinear


def test_a_notice_with_an_issue_is_one_comment_and_no_label(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    linear = FakeLinear()
    Notifier(con, linear, lambda: 5.0).send(
        Notice("NF-1:crash", "The session died twice.", "Release it", issue_key="NF-1"), ISSUE
    )
    assert linear.comments == ["The session died twice.\nAction: Release it"]
    assert ledger.pending_digest(con) == []


def test_a_notice_without_an_issue_goes_to_the_digest(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    linear = FakeLinear()
    Notifier(con, linear, lambda: 5.0).send(Notice("claude-login", "Login lapsed."))
    assert linear.comments == []
    assert [line for _, _, line in ledger.pending_digest(con)] == ["Login lapsed."]
```

Add to `tests/test_ledger.py`:

```python
def test_the_escalation_offset_column_is_gone(tmp_path):
    path = tmp_path / "old.sqlite"
    _v0_ledger(path)
    con = ledger.connect(path)
    assert "escalation_offset" not in _columns(con, "claims")
    assert ledger.get_claim(con, "NF-9") is not None
```

Add to `tests/test_config.py`:

```python
def test_retired_top_level_keys_are_ignored_with_a_warning(tmp_path, capsys):
    p = tmp_path / "config.toml"
    kept = [ln for ln in EXAMPLE.read_text().splitlines() if not ln.startswith("ntfy_url")]
    p.write_text('ntfy_url = "https://ntfy.sh/x"\n' + "\n".join(kept) + "\n")
    assert load(p).team_key == "NF"
    assert "ntfy_url is no longer used" in capsys.readouterr().err
```

Add to `tests/test_supervisor.py`:

```python
def test_block_comments_on_the_issue_without_labels(rt):
    from flocc.notice import Notifier

    rt.notifier = Notifier(rt.con, rt.linear, rt.now)
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    rt.git_head = lambda path: "sha-base"
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).state == "blocked"
    assert rt.linear.comments == [
        "The tester finished without committing tests.\n"
        "Action: Add detail to the acceptance criteria, then run flocc release NF-1"
    ]
```

Run: `uv run pytest -q tests/test_notice.py tests/test_ledger.py tests/test_supervisor.py::test_block_comments_on_the_issue_without_labels`
Expected: FAIL with `ModuleNotFoundError: No module named 'flocc.notice'` and the column still present.

- [ ] **Step 2: Create `src/flocc/notice.py`, the ledger constant and migration**

```python
import sys
from collections.abc import Callable
from dataclasses import dataclass

from flocc import ledger
from flocc.linear import Issue


@dataclass(frozen=True)
class Notice:
    cause: str
    summary: str
    action: str = ""
    issue_key: str | None = None

    def text(self) -> str:
        return f"{self.summary}\nAction: {self.action}" if self.action else self.summary


class Notifier:
    """Tells the owner about a problem: on its issue when it has one, else in the daily digest."""

    def __init__(self, con, linear, now: Callable[[], float]):
        self.con, self.linear, self.now = con, linear, now

    def send(self, notice: Notice, issue: Issue | None = None) -> None:
        print(f"{notice.issue_key or '-'} {notice.cause}: {notice.summary}", file=sys.stderr)
        if issue is not None:
            self.linear.comment(issue.id, notice.text())
        else:
            ledger.add_digest(self.con, notice.issue_key, notice.text(), self.now())
```

In `src/flocc/ledger.py`: add `DEDUPE_S = 24 * 3600` directly above `def alert_seen`; append a second entry to `_MIGRATIONS`:

```python
    ("ALTER TABLE claims DROP COLUMN escalation_offset",),
```

leave `escalation_offset INTEGER NOT NULL DEFAULT 0` in `_SCHEMA` (version 0 stays as shipped; the migration drops it); delete the `escalation_offset: int = 0` field from `Claim`.

`tests/fakes.py`: replace `FakeNotifier.send` with

```python
    def send(self, notice, issue=None):
        self.sent.append(notice)
```

- [ ] **Step 3: Rewire every caller**

`src/flocc/tick.py`:
- `from flocc.escalation import Escalation, Notifier, ntfy_push` → `from flocc.notice import Notice, Notifier`.
- Delete `route_escalations,` from the poller import and `failed |= route_escalations(rt)` from `tick`.
- In `build_runtime`: `notifier = Notifier(cfg, con, linear, now=time.time)` → `notifier = Notifier(con, linear, now=time.time)`.
- Replace `_page_after_repeats` and `_page_unbuilt` with:

```python
def _page_after_repeats(con, notifier, counter, cause, what, exc: Exception, action: str):
    fails = int(ledger.meta_get(con, counter) or 0) + 1
    ledger.meta_set(con, counter, str(fails))
    now = time.time()
    if fails < FAILS_BEFORE_PAGE or ledger.alert_seen(con, cause, ledger.DEDUPE_S, now):
        return
    summary = f"{what} has failed {fails} ticks in a row: {exc}"
    notifier.send(Notice(cause, summary, action))
    ledger.record_alert(con, cause, 0, None, now)


def _page_unbuilt(cfg: Config, exc: Exception) -> None:
    # The notice must not need what failed: with no issue it only writes the digest, so it gets
    # no Linear client. Without a ledger to count in, the journal is all there is.
    try:
        con = ledger.connect(cfg.ledger_path)
    except Exception as err:  # noqa: BLE001
        print(f"tick: failure not counted, the ledger did not open: {err}", file=sys.stderr)
        return
    _page_tick_failure(con, Notifier(con, None, now=time.time), exc)
```

- Replace the supervisor-error send in `tick` with:

```python
                cause = f"{claim.issue_key}:supervisor-error"
                if not ledger.alert_seen(rt.con, cause, ledger.DEDUPE_S, rt.now()):
                    rt.notifier.send(
                        Notice(
                            cause,
                            "The supervisor failed on this issue; see journalctl -u flocc.",
                            "Read the log",
                            issue_key=claim.issue_key,
                        )
                    )
                    ledger.record_alert(rt.con, cause, 0, claim.issue_key, rt.now())
```

`src/flocc/poller.py`:
- `from flocc.escalation import DEDUPE_S, Escalation, scoped` → `from flocc.notice import Notice`; use `ledger.DEDUPE_S` where `DEDUPE_S` appeared.
- In `_start`, the main-broken send becomes:

```python
            # Marked only once the notice is out: a mark holds every issue before _start, so one
            # set ahead of a failed send would hold them with nobody told.
            rt.notifier.send(
                Notice(
                    f"{repo}:main-broken",
                    f"./setup fails on a clean checkout of {repo}'s {base_branch} at {base[:9]},"
                    f" so its issues are held: {exc}{detail}",
                    f"Fix ./setup on {base_branch} or on the host. Issues resume after a new"
                    f" commit on {base_branch}, after 24 hours, or once flocc canary {repo} passes",
                    issue_key=issue.key,
                ),
                issue,
            )
```

  and the prepare-failure block becomes:

```python
        # A broken ./setup fails on every poll, and every send comments again.
        if ledger.alert_seen(rt.con, cause, ledger.DEDUPE_S, rt.now()):
            return
        rt.notifier.send(
            Notice(
                cause,
                f"Preparing the worktree failed: {exc}{detail}",
                "Fix the repo or ./setup",
                issue_key=issue.key,
            ),
            issue,
        )
        ledger.record_alert(rt.con, cause, 2, issue.key, rt.now())
        return
```

- In `_start`, delete the `Claim(...)` arguments `escalation_offset=_size(d / "escalations.jsonl"),` and the two comment lines above it; delete the `_size`, `_new_lines` and `route_escalations` functions. Change `forward_comments`'s docstring to `"""Returns the Linear failures by issue key; one deleted issue must not stop the others."""`.
- In `release_claim`, the comment above `clear_issue_alerts` becomes `# Alert dedupe is keyed to the old claim; a rerun that fails again for the same cause must still be told.`

`src/flocc/supervisor.py`:
- `from flocc.escalation import DEDUPE_S, Escalation, scoped` → `from flocc.notice import Notice`; `DEDUPE_S` in `start_rebase` → `ledger.DEDUPE_S`.
- `_block` becomes:

```python
def _block(rt, claim: Claim, cause: str, summary: str, action: str) -> None:
    rt.sessions.kill(session_name(claim.issue_key, claim.phase))
    key = claim.issue_key
    rt.notifier.send(Notice(f"{key}:{cause}", summary, action, issue_key=key), rt.linear.issue(key))
    ledger.update_claim(rt.con, key, state="blocked")
```

- In `_check_idle` replace the `esc = scoped(Escalation(2, "idle", …, waiting=True), claim.issue_key)` expression with

```python
    esc = Notice(
        f"{claim.issue_key}:idle",
        f"{name} has been idle for {minutes} min without finishing:"
        f" {message or 'no last message'}",
        "Reply on this issue to steer it, or release it with"
        f" flocc release --keep-worktree {claim.issue_key}",
        issue_key=claim.issue_key,
    )
```

  and in `_check_stall` replace `esc = scoped(Escalation(2, "stalled", …, waiting=True), key)` with

```python
    esc = Notice(
        f"{key}:stalled",
        f"{name} has shown the same screen for {minutes} min in the middle of a turn,"
        f" most likely a dialog waiting for an answer:\n```\n{tail}\n```",
        f"See it with flocc peek {key} and answer with flocc press {key} <keys>"
        f" (e.g. flocc press {key} Down Enter), or release it with"
        f" flocc release --keep-worktree {key}",
        issue_key=key,
    )
```

  The `rt.notifier.send(esc, rt.linear.issue(...), None)` lines after them drop the `, None`.
- In `_review_skipped`, the comment `# A review is advice on an open PR; losing it must neither block the claim nor page anyone.` becomes `# A review is advice on an open PR; losing it must not block the claim.`

`src/flocc/tokens.py`:
- `from flocc.escalation import Escalation` → `from flocc.notice import Notice`.
- `_page` body: `rt.notifier.send(Notice(f"gh-token-{name}", summary, f"Make a new {name} token and write it to {path}"))` (drop the `action` and `esc` locals).
- `check_claude_login`: `rt.notifier.send(Notice("claude-login", summary, action))`; its comment's `pages once` → `reports once`.

`src/flocc/cli.py`: delete `from flocc.escalation import Escalation, validate`, the whole `cmd_escalate` function, `"escalate": cmd_escalate,` from `COMMANDS`, and the `esc = add("escalate", …)` parser line with its eight `esc.add_argument` lines. In `cmd_hook`, the refusal text `message that session or escalate` stays until Task 7 deletes it.

`src/flocc/doctor.py`: delete `_LABELS = ("escalation", "needs-decision")` and `ntfy_check` with its call in `collect_results`; in `linear_checks`, `names = [cfg.trigger_label, *_LABELS]` → `names = [cfg.trigger_label]`.

`src/flocc/config.py`:
- Delete the `ntfy_url: str` field. The `digest_issue` comment becomes `# Required: wall-clock stops and problems with no issue reach the owner only through it.`
- Add above `_PATH_KEYS`: `_RETIRED = ("ntfy_url",)`.
- In `load`, after the `digest_issue` check, add:

```python
    # Hosts keep their config.toml across upgrades, so a key a release removed must not stop the tick.
    for key in _RETIRED:
        if raw.pop(key, None) is not None:
            print(f"{path}: {key} is no longer used; delete it", file=sys.stderr)
```

`src/flocc/linear.py`: delete `add_label` and `create_issue`; in `missing_labels` the comment `# The same rule add_label applies: a workspace label or one of this team's.` becomes `# A workspace label, or one of this team's.`

`src/flocc/context.py`: in `_COMMANDS` replace

```
approval, or ask for design sign-off: nobody is at this session to approve it. Ask only about a
decision the spec leaves open, through the escalate skill.
```

with

```
approval, or ask for design sign-off: nobody is at this session to approve it. Where the spec
leaves a choice open, take its default; what needs the owner goes in your `flocc done` summary
under a line that starts with "Needs the owner".
```

and delete the line ``- `flocc escalate ...` when you need the owner; see the escalate skill.``

`deploy/config.example.toml`: delete `ntfy_url = "https://ntfy.sh/replace-with-a-long-random-topic"`; change `# Escalate to the owner when a session has ended its turn and stayed quiet this many seconds.` to `# Comment on the issue when a session has ended its turn and stayed quiet this many seconds.` and `# Escalate when a session's screen has not changed for this many seconds in the middle of a turn.` to `# Comment on the issue when a session's screen has not changed this many seconds mid-turn.`

Skills:
- `git rm -r plugin/skills/escalate plugin/evals/escalate-irreversible plugin/evals/escalate-reversible plugin/evals/escalate-shared-cause` (then `rmdir plugin/evals` if empty).
- `plugin/skills/implementer/SKILL.md`: delete the bullet starting `- When a peer's claim blocks your commit` (five lines, through the `flocc escalate --tier 2 --cause claim-held:…` line); replace the bullet `- Nobody reads your session's pane. Never end a turn waiting for the owner in plain text: to wait
  on the owner, ask with \`AskUserQuestion\` (see the escalate skill).` with

```markdown
- Do not ask the owner anything, in the pane or with `AskUserQuestion`; nobody answers. Where the
  spec leaves a choice open, take its default or the smallest choice that meets the criteria. Put
  anything that needs the owner in your `flocc done` summary under a line that starts with
  `Needs the owner`.
```

  and in the first bullet's `A rejected claim names the issue that holds it; message that session or escalate.` leave the text for Task 7, which deletes the bullet.
- `plugin/skills/tester/SKILL.md`: delete the bullet starting `- When a peer's claim blocks your commit` (four lines); replace the bullet `- Nobody reads your session's pane. Never end a turn waiting for the owner in plain text: to wait
  on the owner, run \`flocc escalate --tier 2 ... --waiting\` (see the escalate skill).` with the same four-line bullet as the implementer's.
- `plugin/skills/reviewer/SKILL.md`: `- Findings go only in \`review.md\`. Do not run \`flocc escalate\`; the PR is already open and
  the owner reads the review.` → `- Findings go only in \`review.md\`; the PR is already open and the owner reads the review.`

- [ ] **Step 4: Update the Python tests**

- `git rm tests/test_escalation.py`.
- `tests/test_poller.py`: delete `from flocc.escalation import Notifier` and `route_escalations` from the poller import; delete `PollLinear.add_label`; delete `test_escalations_sent_before_a_failure_are_not_resent`, `test_route_escalations_reads_only_new_lines`, `test_review_session_escalations_go_to_the_digest_only`; in `test_rerun_ignores_files_from_the_previous_claim` delete the `old = …` and `escalations.jsonl` lines, `route_escalations(rt)` and `assert rt.notifier.sent == []`; in `test_prepare_failure_comments_once_with_stderr` use `rt.notifier = Notifier(rt.con, rt.linear, rt.now)` with `from flocc.notice import Notifier`; delete every `e.tier`/`esc.tier`/`esc.scope` clause (`assert all(e.tier == 2 for e in rt.notifier.sent)` lines go entirely; `assert esc.tier == 1 and esc.issue_key == "NF-9" and …` → `assert esc.issue_key == "NF-9" and …`; `assert esc.tier == 2 and esc.cause == "NF-9:prepare"` → `assert esc.cause == "NF-9:prepare"`; `assert (esc.tier, esc.scope) == (1, "global") and "tmux: no server" in esc.summary` → `assert esc.issue_key is None and "tmux: no server" in esc.summary`; `[page] = [e for e in rt.notifier.sent if e.tier == 1]` → `[page] = [e for e in rt.notifier.sent if e.cause == "linear-api"]`); every `def down(esc, issue, team_id):` / `def flaky(esc, issue, team_id):` replacement for `rt.notifier.send` becomes `(notice, issue=None)`. The tick-failure tests (`test_tick_pages_once_*`) call the notifier once per 24 h now; if one asserts a second page in the same run, change it to assert one.
- `tests/test_supervisor.py`: `assert rt.notifier.sent[-1].waiting is True` → delete the line; `assert esc.tier == 2 and esc.waiting and esc.issue_key == "NF-1"` → `assert esc.issue_key == "NF-1"`; `assert esc.tier == 2 and esc.waiting and esc.cause == "NF-1:stalled"` → `assert esc.cause == "NF-1:stalled"`; `def flaky(esc, issue, team_id):` → `def flaky(notice, issue=None):`.
- `tests/test_tokens.py`: `assert (esc.tier, esc.scope, esc.cause) == (1, "global", "gh-token-agent")` → `assert esc.cause == "gh-token-agent"`; same for `"claude-login"`.
- `tests/test_doctor.py`: delete `test_ntfy_check_warns_on_placeholder`; in `test_linear_checks` use `missing=["claude"]` and `"claude" in labels.detail`, and expect `fake.label_calls == [[tmp_cfg.trigger_label], list(tmp_cfg.repos)]`; in the JSON and plain-output tests replace the sample check name `"ntfy"` with `"disk"` (both the `Result(...)` and the expected text).
- `tests/test_config.py::test_example_loads_with_defaults`: no `ntfy_url` assert exists; leave it.
- `tests/test_installer_config.py`: delete `assert cfg.ntfy_url == "https://ntfy.sh/flocc-test"`.
- `tests/test_linear.py`: delete the `add_label` tests (`test_add_label_skips_other_teams_labels`, `test_add_label_swaps_siblings_in_an_exclusive_group` with its parametrize, `test_add_label_keeps_siblings_in_a_multi_select_group`, `test_add_top_level_label_only_adds`, `test_add_label_already_on_the_issue_but_not_in_the_snapshot_is_a_noop`, `test_add_label_already_present_is_a_noop`, `test_add_label_missing_label_raises`) and any `create_issue` test; keep `test_missing_labels_counts_team_and_workspace_labels`.
- `tests/test_context.py::test_context_lists_issue_and_commands`: delete the needle `"flocc escalate",`.
- `tests/test_rebase.py:94` (`pr.notifier.sent[-1].summary`) keeps working; leave it.

Run:

```bash
uv run pytest -q tests/test_notice.py tests/test_ledger.py tests/test_poller.py tests/test_supervisor.py tests/test_tokens.py tests/test_doctor.py tests/test_config.py tests/test_linear.py tests/test_context.py tests/test_rebase.py tests/test_cli.py
uv run pyrefly check
grep -rn -i 'escalat\|ntfy\|needs-decision\|Escalation\|tier [0-9]' src tests plugin deploy/config.example.toml | grep -v 'idle_escalate_s\|stall_escalate_s\|idle-escalated\|STALL_ESCALATED\|stall-escalated'
```

Expected: PASS; the grep prints nothing. (`idle_escalate_s`, `stall_escalate_s` and the `*-escalated` marker names stay: renaming config keys would break host configs for no gain.)

- [ ] **Step 5: Remove ntfy and the escalation labels from the desktop installer**

`installer/internal/keys/store.go`: delete `Ntfy Slot = "ntfy_topic"` (25); `var Slots = []Slot{Linear, Agent, Publish, Ntfy}` → `var Slots = []Slot{Linear, Agent, Publish}`; `var fillOrder = []Slot{Linear, Agent, Publish, Tailscale, Ntfy}` → `var fillOrder = []Slot{Linear, Agent, Publish, Tailscale}`; the fall-through `return "ntfy topic"` (68) → `return string(s)`.

`installer/internal/keys/envfile.go:27`: delete `Ntfy: "FLOCC_NTFY_TOPIC",`.

`installer/internal/keys/fill.go`: delete the `bytes` and `qr` imports; the `Env` fields comment and line (43-46) become

```go
	// The API bases; githubapi.DefaultBase and linearapi.DefaultURL outside tests. An empty
	// TailscaleAPI is https://api.tailscale.com.
	GitHubAPI, LinearAPI, TailscaleAPI string
```

(`NewTopic` goes); `if s == Ntfy { err = fillNtfy(ctx, e) } else { err = fillKey(ctx, e, m, s) }` (65-69) → `err = fillKey(ctx, e, m, s)`; the `if s == Ntfy { stored, err = takeNtfy(…) } else { stored, err = takeKey(…) }` block (225-230) → keep `var stored bool` and write `stored, err = takeKey(ctx, e, m, s, t)`; delete `takeNtfy` (284-299), the `Ntfy: "sending a test push",` entry (365), the "Check ntfy topic" block in `Check` (495-501), and `fillNtfy` and `push` (570-627).

Keys tests: `fill_test.go` — drop the ntfy server, `NtfyBase:` and `NewTopic:` (98-106), `"set ntfy_topic",` (146), `|| f.store.m[Ntfy] != "flocc-test"` (151), the `"Check ntfy topic"` entry (186), `f.store.m[Ntfy]` and `"t"` (329), change `len(got) != 5` to `len(got) != 4` (342), delete 348-350 and `TestNtfyShowsTheAppLinkAndResendsUntilThePushArrives`, drop `"ntfy topic: missing"` (554); `take_test.go:101-130`: delete the two ntfy tests; `envfile_test.go`: switch `FLOCC_NTFY_TOPIC`/`Ntfy` to `FLOCC_LINEAR_API_KEY`/`Linear` in the tests at 33, 66-68, 93, 111-129; `store_test.go:64-69,78`: `ntfy_topic = 'flocc-x'`/`Get(Ntfy)` → `linear_key`/`Get(Linear)` (check the slot's TOML name in `store.go`).

`installer/internal/cli/cli.go`: line 35 `make, check and store the Linear key, GitHub tokens and ntfy topic` → `make, check and store the Linear key and GitHub tokens`; delete the `ntfyBase string` and `newTopic func() string` fields (79-80); lines 99-100 become `d.githubAPI, d.linearAPI = githubapi.DefaultBase, linearapi.DefaultURL`; drop the `answers` import if `go vet` reports it unused.

`installer/internal/cli/keys.go:20-21`: `GitHubAPI: d.githubAPI, LinearAPI: d.linearAPI, NtfyBase: d.ntfyBase, NewTopic: d.newTopic}` → `GitHubAPI: d.githubAPI, LinearAPI: d.linearAPI}`.

`installer/internal/cli/setup.go:81`: `[]keys.Slot{keys.Publish, keys.Ntfy}` → `[]keys.Slot{keys.Publish}`; same in `setup_test.go:195,229,291,722`.

`installer/internal/cli/host.go`: lines 220-221 become `st.Detail("checking the publish token")` and `if err := keys.Fill(ctx, e, m, []keys.Slot{keys.Publish}); err != nil {`; drop the ntfy sentence from the comment at 368-369; replace the import block at 405-419 (from `detail := …` through `h.NtfyTopic, h.Secrets = "", nil`) with

```go
		h.Answers = *a
		h.Secrets = nil
		return "imported into " + hostFile(d, name), hosts.Save(hosts.Dir(d.home), h)
```

(keep the function's real return statement if it differs; only the topic handling goes); line 346 `names := []string{"claude", "escalation", "needs-decision"}` → `names := []string{"claude"}`. `host_test.go`: drop `ntfy_topic = 'flocc-t'\n` from `allKeys` (105); delete the ntfy server and `d.ntfyBase`/`d.newTopic` (245-247); drop `"Check ntfy topic=done", ` (298); delete `p.confirms = []bool{true} // the ntfy test push arrived` (429); drop `keys.Ntfy: "flocc-t"` (434), `|| h.NtfyTopic != ""` (515) and lines 518-519; delete `TestHostImportSaysWhenTheDesktopsTopicWillReplaceTheHosts`; line 271's expected labels become `[]string{"claude", "a"}`. `envkeys_test.go:113,131`: switch `FLOCC_NTFY_TOPIC` to `FLOCC_LINEAR_API_KEY`.

`installer/internal/cli/ship.go:127`: delete `a.NtfyTopic = v[keys.Ntfy]`; `ship_test.go:64`: drop `a.NtfyTopic != "flocc-t" ||`.

`installer/internal/answers/answers.go`: delete the `NtfyTopic` field (156) and `ntfyTopic`, `ntfyURLPrefix`, `NormalizeTopic`, `RandomTopic`, `OverlaySaved` (219-260) with the imports only they used; in `Overlay` (262-273) drop the topic handling and the `{&a.NtfyTopic, topic}` entry, keeping the `error` return. `git rm installer/internal/answers/topic_test.go`.

`installer/cmd/flocc-setup/main.go:229-233`: replace

```go
		if saved, err := answers.Load(env.AnswersPath); err == nil {
			if err := a.OverlaySaved(saved); err != nil {
				fmt.Fprintln(stderr, "flocc-setup: the saved ntfy topic in", env.AnswersPath, "is invalid and will be asked again:", err)
			}
		}
```

with

```go
		if saved, err := answers.Load(env.AnswersPath); err == nil {
			_ = a.Overlay(saved)
		}
```

`installer/internal/ui/huh.go:91-111`: delete `suggested`, the ntfy `huh.NewInput()` (95-100) and the normalize block (104-111); the form keeps the team key and digest issue.

`installer/internal/steps/config.go`: delete `m["ntfy_url"] = "https://ntfy.sh/" + a.NtfyTopic` (32), the `RandomTopic` fallback (163-166) and the curl test push and its confirm (185-198). `config_test.go:21,155`: drop the `NtfyTopic` assignments. `hosts/hosts_test.go:100`: drop `NtfyTopic: "flocc-x"`. `installer/testdata/e2e-answers.toml:3`: delete the `ntfy_topic` line. Regenerate the config goldens: `go test ./installer/internal/steps -run RenderConfig -update`, then `git diff installer/internal/steps/testdata` shows only the `ntfy_url` line removed.

The doctor-check fixtures named `"ntfy"` in `dash/testdata/host-*.golden`, `dash/hostscreen_test.go:19,84` and `cli/remote_test.go:374,400` are sample data for any check name; leave them.

Run:

```bash
gofmt -w installer
go build ./... && go vet ./...
go test ./installer/internal/keys/ ./installer/internal/cli/ ./installer/internal/answers/ ./installer/internal/steps/ ./installer/internal/hosts/ ./installer/internal/ui/... ./installer/cmd/flocc-setup/
uv run pytest -q tests/test_installer_config.py
grep -rn -i 'ntfy\|NtfyTopic\|needs-decision\|"escalation"' installer --include='*.go' | grep -v '_test.go'
```

Expected: PASS; the grep prints nothing.

- [ ] **Step 6: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests
git add -A src tests plugin deploy installer
git commit -s -m "Replace escalation tiers and ntfy with issue comments and the digest"
```

---

### Task 7: Remove path claims

`flocc claim`, claimed paths on the claim and in the agent block, `shared_paths`, the admission overlap check and the pre-commit claim check go. The pre-commit hook stays with only its review-session refusal (see "Where this plan departs").

**Files:**
- Modify: `src/flocc/{ledger,admission,agentblock,config,poller,context,cli,sessions}.py`, `deploy/config.example.toml`, `plugin/skills/{implementer,tester}/SKILL.md`
- Test: `tests/test_{ledger,admission,agentblock,config,cli,worktree,poller,context}.py` and every test that calls `make(` with a paths tuple

**Interfaces:**
- Consumes: `ledger._MIGRATIONS` with two entries (Tasks 1 and 6).
- Produces: `ledger._MIGRATIONS` third entry `("ALTER TABLE claims DROP COLUMN paths",)`; `Claim` without `paths`; `IssueSettings` without `paths`; `RepoConfig` without `shared_paths`; `tests.test_ledger.make(key="NF-1", state="running", worktree=None, repo="backend")`; `cli.cmd_hook` pre-commit returns 1 only for a review-phase claim.

- [ ] **Step 1: Write the failing tests**

`tests/test_ledger.py`:

```python
def test_the_paths_column_is_gone(tmp_path):
    path = tmp_path / "old.sqlite"
    _v0_ledger(path)
    con = ledger.connect(path)
    assert "paths" not in _columns(con, "claims")
    claim = ledger.get_claim(con, "NF-9")
    assert claim is not None and claim.depends_on == []
```

`tests/test_agentblock.py`:

```python
def test_paths_is_now_an_unknown_key():
    with pytest.raises(BlockError, match="unknown keys"):
        parse_settings("```agent\npaths: src/**\n```")
```

`tests/test_config.py` (replacing `test_shared_paths_load_per_repo`):

```python
def test_a_retired_repo_key_is_ignored_with_a_warning(tmp_path, capsys):
    p = tmp_path / "config.toml"
    table = "[repos.backend]"
    p.write_text(EXAMPLE.read_text().replace(table, f'{table}\nshared_paths = ["docs/log.md"]'))
    assert load(p).repos["backend"].base_branch == "main"
    assert "repos.backend.shared_paths is no longer used" in capsys.readouterr().err
```

`tests/test_cli.py`:

```python
def test_commit_hook_allows_any_file_outside_the_review(claims_env, origin, monkeypatch):
    ledger.insert_claim(claims_env, make("NF-1", worktree=str(origin.resolve())))
    ledger.insert_claim(claims_env, make("NF-2", worktree="/wt/NF-2"))
    monkeypatch.chdir(origin)
    (origin / "server.py").write_text("x\n")
    git("add", "server.py", cwd=origin)
    assert main(["hook", "pre-commit"]) == 0
```

(use the same `claims_env`/`origin`/`git` names the neighbouring commit-hook tests in `tests/test_cli.py` use; copy the setup of `test_commit_in_a_review_worktree_is_rejected` if they differ.)

Run: `uv run pytest -q tests/test_ledger.py::test_the_paths_column_is_gone tests/test_agentblock.py::test_paths_is_now_an_unknown_key tests/test_config.py::test_a_retired_repo_key_is_ignored_with_a_warning`
Expected: FAIL.

- [ ] **Step 2: Drop paths from the ledger, admission and agent block**

`src/flocc/ledger.py`:
- Append to `_MIGRATIONS`: `("ALTER TABLE claims DROP COLUMN paths",),`
- Delete the `paths: list[str]` field from `Claim`; `_JSON_FIELDS = ("paths", "depends_on")` → `_JSON_FIELDS = ("depends_on",)`.
- Delete `in_repo`, `is_shared`, `unshared`, `add_paths`, `_literal_prefix`, `globs_overlap`, `conflicting_claims` and `file_conflicts`; change `from pathlib import Path, PurePosixPath` to `from pathlib import Path`.
- In `connect`, the comment's `the tick and the pre-commit hook write from separate processes` → `the tick, flocc release and flocc canary write from separate processes`.

`src/flocc/admission.py`: `from flocc.ledger import Claim, conflicting_claims, in_repo, unshared` → `from flocc.ledger import Claim`; delete the five lines from `shared = cfg.repos[repo].shared_paths if repo in cfg.repos else ()` through `return Decision(False, f"paths claimed: {owners}")`; the comment `# A claim held on a broken base runs no session; it still holds its paths below.` → `# A claim held on a broken base runs no session.`

`src/flocc/agentblock.py`: `_KEYS = {"model", "effort", "wall_clock", "paths", "depends_on", "mcp"}` → `_KEYS = {"model", "effort", "wall_clock", "depends_on", "mcp"}`; delete the `paths: tuple[str, ...] = ()` field, `paths=_list(values.get("paths", "")),` and `paths=tuple(raw.get("paths", ())),`; the comment `# Silently running with defaults would drop the paths and depends_on the owner wrote.` → `# Silently running with defaults would drop the depends_on the owner wrote.`; the `_list` comment `# Split on commas, so brace globs like src/{a,b}/** are unsupported; write two globs.` → `# Split on commas.`

`src/flocc/config.py`: delete the `shared_paths` field and its comment from `RepoConfig`; replace `_repo` with:

```python
def _repo(config_path: Path, name: str, table: dict) -> RepoConfig:
    if table.pop("shared_paths", None) is not None:
        print(
            f"{config_path}: repos.{name}.shared_paths is no longer used; delete it",
            file=sys.stderr,
        )
    # Unpacked so a misspelt repo key fails like a misspelt top-level one.
    return RepoConfig(path=Path(table.pop("path")).expanduser(), **table)
```

`src/flocc/poller.py::_start`: delete `paths=ledger.unshared(list(settings.paths), cfg.repos[repo].shared_paths),`.

`src/flocc/context.py`: in `write_context` delete `paths = ", ".join(claim.paths) if claim.paths else "none yet"` and change the `## Claim` line to `f"## Claim\n\nBranch \`{claim.branch}\`.\n\n"`; in `_COMMANDS` delete ``- `flocc claim '<glob>'` before editing files outside your claimed paths.`` and change ``- `flocc context` to see this text again with the current peer list.`` to ``- `flocc context` to see this text again.``

`src/flocc/sessions.py`: the `_DENY` comment `# --no-verify skips the pre-commit claim check and the commit-msg trailer.` → `# --no-verify skips the pre-commit review guard and the commit-msg trailer.`

- [ ] **Step 3: Cut the claim commands and the pre-commit claim check**

`src/flocc/cli.py`:
- `from pathlib import Path, PurePosixPath` → `from pathlib import Path`.
- Replace `cmd_hook` with:

```python
def cmd_hook(args) -> int:
    from flocc.worktree import git

    if args.name == "commit-msg":
        return _commit_msg(args.message_file, not args.no_trailer)
    top = Path(git("rev-parse", "--show-toplevel", cwd=Path.cwd()))
    mine = ledger.claim_for_worktree(_ledger(args), str(top))
    if mine is not None and mine.phase == "review":
        print("flocc: the review session is read-only; do not commit", file=sys.stderr)
        return 1
    return 0
```

- Delete `_shared_paths` and `cmd_claim`.
- In `cmd_context`, delete the peers block (`from flocc.sessions import session_name`, the `# Peers come from the live ledger…` comment, `live = …`, `mine = …`, `peers = …`, `names = …` and the `print(f"## Peers in this repo\n\n{names}.\n")`).
- Delete `"claim": cmd_claim,` from `COMMANDS` and the parser lines `claim = add("claim", "Claim file globs for this issue")` and `claim.add_argument("globs", nargs="+", help="file globs to claim")`; `add("context", "Print the issue's claims and context")` → `add("context", "Print the issue's context")`.

`deploy/config.example.toml`: delete

```toml
# Files every issue edits, such as an append-only log. Sessions cannot claim them, and the commit
# hook never refuses them over a peer's claim. Exact paths or globs, relative to the repo root.
# shared_paths = ["docs/verification.md"]
```

`plugin/skills/implementer/SKILL.md`: delete the bullet starting `- Claim the files you will edit before editing them:` (four lines, through `file the repo lists as shared is edited without a claim.`). `plugin/skills/tester/SKILL.md`: delete the bullet starting `- Claim any file you edit outside your claimed paths first:` (three lines).

- [ ] **Step 4: Update the tests**

- `tests/test_ledger.py`: `def make(key="NF-1", paths=("src/a/**",), state="running", worktree=None, repo="backend"):` → `def make(key="NF-1", state="running", worktree=None, repo="backend"):` and delete `paths=list(paths),` from the `Claim(...)`; in `test_insert_get_update_live` change `ledger.update_claim(con, "NF-1", phase="impl", paths=["src/a/**", "src/b.py"])` to `ledger.update_claim(con, "NF-1", phase="impl")` and `assert got.phase == "impl" and got.paths == [...]` to `assert got.phase == "impl"`; delete `test_globs_overlap_cases` (with its parametrize), `test_conflicts_exclude_self`, `test_file_under_a_claimed_directory_conflicts`, `test_add_paths_extends_a_claim_or_reports_the_owner`, `test_add_paths_ignores_claims_in_other_repos`, `test_is_shared_matches_exact_names_and_globs`.
- Every other `make(` call: remove the positional paths argument. Find them with `grep -rn 'make("NF-[0-9]*", *(' tests` (e.g. `make("NF-1", ())` → `make("NF-1")`, `make("NF-2", ("server/x.py",), repo="web", worktree="/wt/NF-2")` → `make("NF-2", repo="web", worktree="/wt/NF-2")`, `make("NF-1", (), state="blocked")` → `make("NF-1", state="blocked")`). After the edit the grep prints nothing.
- `tests/test_admission.py`: delete `test_path_conflict`, `test_a_shared_path_in_the_agent_block_does_not_hold_a_start`, `test_a_claim_in_another_repo_does_not_hold_a_start`.
- `tests/test_agentblock.py`: in `FULL` delete the line `paths: [src/billing/**, tests/billing/**]`; in `test_full_block` delete `paths=("src/billing/**", "tests/billing/**"),`.
- `tests/test_config.py`: delete `test_shared_paths_load_per_repo` (replaced in Step 1).
- `tests/test_cli.py`: delete `test_claim_refuses_a_whole_tree`, `test_claim_refuses_a_glob_that_matches_a_directory`, `test_claim_refuses_a_glob_outside_the_worktree`, `test_claim_skips_a_shared_path`, `test_claim_ignores_a_peer_in_another_repo`, `test_commit_hook_still_refuses_a_peer_claim_in_the_same_repo`, `test_commit_hook_ignores_what_the_merged_side_brought_in`, `test_commit_hook_still_checks_what_the_branch_itself_edited_before_the_merge`; keep `test_commit_in_a_review_worktree_is_rejected`.
- `tests/test_poller.py`: delete `test_a_shared_path_in_the_agent_block_is_not_recorded_as_a_claim`.
- `tests/test_worktree.py`: delete `test_claim_cli`; rename `test_hook_blocks_files_claimed_by_others` to `test_hook_blocks_a_commit_from_the_review_session`; replace `_check_hook_blocks` with:

```python
def _check_hook_blocks(origin, tmp_path, root):
    path, _, base = prepare_worktree(RepoConfig(origin), root, "NF-3")
    db = tmp_path / "l.sqlite"
    con = ledger.connect(db)
    review = make("NF-3", worktree=str(path))
    review.phase = "review"
    ledger.insert_claim(con, review)
    cfg = tmp_path / "c.toml"
    cfg.write_text(
        EXAMPLE.read_text().replace(
            'ledger_path = "~/.local/state/flocc/ledger.sqlite"', f'ledger_path = "{db}"'
        )
    )
    exe = shutil.which("flocc")
    assert exe
    install_hook(path, tmp_path / "hooks", exe, cfg)
    (path / "ok.txt").write_text("fine\n")
    git("add", "ok.txt", cwd=path)
    blocked = subprocess.run(
        ["git", "commit", "-qm", "x"], cwd=path, capture_output=True, text=True, check=False
    )
    assert blocked.returncode != 0 and "read-only" in blocked.stderr
    ledger.update_claim(con, "NF-3", phase="impl")
    git("commit", "-qm", "ok", cwd=path)
    assert changed_files(path, base, "HEAD") == {"ok.txt"}
```

  (`shutil.which("flocc")` needs the package installed in the venv, as the test already assumes.)
- `tests/test_context.py`: in `test_context_lists_issue_and_commands` delete the needle `"flocc claim",`; rename `test_context_cli_prints_context_live_peers_and_handoff` to `test_context_cli_prints_context_and_handoff`, change its claims loop to `for claim in (make("NF-1"), make("NF-2"), replace(make("NF-3"), repo="web")):`, and its last assert to `assert "CTX" in out and "NOTE" in out and "NF-2" not in out`.

Run:

```bash
uv run pytest -q tests/test_ledger.py tests/test_admission.py tests/test_agentblock.py tests/test_config.py tests/test_cli.py tests/test_worktree.py tests/test_poller.py tests/test_context.py tests/test_status.py tests/test_supervisor.py tests/test_rebase.py
uv run pyrefly check
grep -rn 'shared_paths\|\.paths\b\|flocc claim\|cmd_claim\|conflicting_claims\|file_conflicts\|add_paths' src tests plugin deploy
```

Expected: PASS; the grep prints nothing.

- [ ] **Step 5: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests
git add -A src tests plugin deploy
git commit -s -m "Remove path claims and the pre-commit claim check"
```

---

### Task 8: Remove the handoff skill and the 60% context restart

d175. The handoff skill, `flocc handoff-ready`, the supervisor's context check and `context_restart_pct` go; sessions run on Claude Code's own auto-compact. `handoff.md` stays as the tester's note to the implementer, printed by `flocc context`.

**Files:**
- Delete: `plugin/skills/handoff/`
- Modify: `src/flocc/{supervisor,cli,config,poller,context}.py`, `deploy/config.example.toml`, `plugin/.claude-plugin/plugin.json`, `tests/bin/claude`
- Test: `tests/test_supervisor.py`, `tests/test_cli.py`, `tests/test_context.py`, `tests/test_config.py`, `tests/test_process.py`

**Interfaces:**
- Consumes: `config._RETIRED = ("ntfy_url",)` from Task 6.
- Produces: `config._RETIRED = ("ntfy_url", "context_restart_pct")`; `supervisor._MARKERS = ("done.json",)`; no `handoff-ready` command.

- [ ] **Step 1: Write the failing test**

Add to `tests/test_config.py`:

```python
def test_context_restart_pct_is_retired(tmp_path, capsys):
    p = tmp_path / "config.toml"
    p.write_text("context_restart_pct = 60.0\n" + EXAMPLE.read_text())
    cfg = load(p)
    assert not hasattr(cfg, "context_restart_pct")
    assert "context_restart_pct is no longer used" in capsys.readouterr().err
```

Run: `uv run pytest -q tests/test_config.py::test_context_restart_pct_is_retired`
Expected: FAIL (the field still loads).

- [ ] **Step 2: Remove the restart**

`src/flocc/supervisor.py`:
- Delete `HANDOFF_ASK` (four lines) and change `_MARKERS = ("done.json", "handoff-ready", "handoff-requested")` to `_MARKERS = ("done.json",)`.
- `from flocc.usage import context_pct, latest_usage, read_status` → `from flocc.usage import latest_usage`.
- In `launch`, the comment `# After everything above that can raise. The previous session's markers would be taken for the new one's, and its context reading would ask the new session to hand off at once.` (two lines) becomes `# After everything above that can raise: the previous session's markers would be taken for the new one's.`, and `"compacted",` goes from the leftovers tuple.
- In `supervise`, delete

```python
    if (d / "handoff-ready").exists():
        rt.sessions.kill(name)
        _relaunch(rt, claim)
        return
```

  and the whole block from `pct = context_pct(read_status(d) or {})` through `requested.touch()` (with its comment).

`src/flocc/cli.py`: delete `cmd_handoff_ready`, `"handoff-ready": cmd_handoff_ready,` and `add("handoff-ready", "Mark handoff.md ready")`; in `_session_bookkeeping` delete the compaction branch

```python
    if payload.get("source") == "compact":
        # status.json still holds the pre-compaction reading; the supervisor would hand off at once.
        (state / "compacted").touch()
        (state / "status.json").unlink(missing_ok=True)
```

and change the `session-start` help to `"Hook: record the scratchpad, print the context"`.

`src/flocc/config.py`: delete `context_restart_pct: float = 60.0`; `_RETIRED = ("ntfy_url",)` → `_RETIRED = ("ntfy_url", "context_restart_pct")`.

`src/flocc/poller.py::_start`: replace

```python
    # The previous claim's notes would be read as this session's handoff, and would satisfy
    # handoff-ready before the new session wrote its own. Kept for the owner.
```

with `# The previous claim's tester notes would be read as this claim's. Kept for the owner.`, and drop `"compacted"` from the stale list (`for stale in ("scratchpads", "idle", "idle-escalated"):`, comment `# A previous claim's scratchpads and idle marker belong to that run.`).

`src/flocc/context.py`: in `_COMMANDS` delete ``- `flocc handoff-ready` after writing handoff.md when asked to hand off.``

`deploy/config.example.toml`: delete

```toml
# Restart a session when its context passes this pct.
# context_restart_pct = 60.0
```

`plugin/.claude-plugin/plugin.json`: `"description": "Tester and implementer workflow, escalation rubric and handoffs for flocc sessions"` → `"description": "Tester, implementer and reviewer skills for flocc sessions"`.

`git rm -r plugin/skills/handoff`.

`tests/bin/claude`: delete the `handoff)` case (three lines).

- [ ] **Step 3: Update the tests**

`tests/test_supervisor.py`:
- Delete `test_high_context_requests_handoff_once`, `test_handoff_ready_restarts_same_phase_with_a_clean_slate`, `test_no_handoff_before_the_first_compaction`.
- In `test_failed_start_puts_the_markers_back` delete `(d / "handoff-requested").touch()` and change `assert (d / "done.json").read_text() == before and (d / "handoff-requested").exists()` to `assert (d / "done.json").read_text() == before`.
- Replace the body of `test_fix_session_keeps_the_check_output_across_restarts` with:

```python
def test_fix_session_keeps_the_check_output_across_restarts(rt):
    rt.publish_fn = lambda c, t, s: PublishResult("check_failed", "E1 boom")
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))  # check fails: fix starts
    rt.sessions.alive_names.clear()
    supervise(rt, current(rt))  # the fix session died: relaunched once
    assert len(rt.sessions.started) == 2
    assert any("E1 boom" in arg for arg in rt.sessions.started[-1][1])
```

- In `test_relaunch_clears_compaction_and_idle_markers` use `names = ("idle", "idle-escalated")` and rename it `test_relaunch_clears_the_idle_markers`.

`tests/test_cli.py`: replace `test_session_start_on_compaction` with

```python
def test_session_start_on_compaction_lists_the_scratchpad(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setenv("FLOCC_LEDGER", str(tmp_path / "l.sqlite"))
    payload = {"source": "compact", "scratchpad_dir": "/tmp/claude-1000/x/s1/scratchpad"}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    assert main(["session-start"]) == 0
    assert (tmp_path / "scratchpads").read_text().splitlines() == [payload["scratchpad_dir"]]
    assert not (tmp_path / "compacted").exists()
```

`tests/test_context.py`: replace `test_done_and_handoff_ready` with

```python
def test_done_writes_the_summary(tmp_path, monkeypatch):
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path))
    assert main(["done", "--summary", "tests written"]) == 0
    assert json.loads((tmp_path / "done.json").read_text())["summary"] == "tests written"
```

`tests/test_config.py::test_example_loads_with_defaults`: delete `assert cfg.context_restart_pct == 60.0`.

`tests/test_process.py`: replace lines 54-63 (from `(state / "fake-mode").write_text("handoff")` through `assert current(rt).restarts == 1`) with:

```python
    (state / "fake-mode").write_text("crash")
    launch(rt, c, "test")
    wait_for(lambda: not sessions.alive("NF-1-test"))
    (state / "fake-mode").write_text("done")
    supervise(rt, current(rt))  # dead: the one restart
    assert current(rt).restarts == 1
```

Run:

```bash
uv run pytest -q tests/test_supervisor.py tests/test_cli.py tests/test_context.py tests/test_config.py tests/test_process.py tests/test_plugin.py tests/test_poller.py
grep -rn 'handoff-ready\|handoff-requested\|HANDOFF_ASK\|context_restart_pct\|"compacted"\|handoff skill' src tests plugin deploy | grep -v '_RETIRED'
```

Expected: PASS; the grep prints only `config.py`'s `_RETIRED` line (already filtered) and nothing else.

- [ ] **Step 4: Check the skills say nothing removed**

```bash
grep -rn -i 'escalat\|flocc claim\|grant\|gate\|launch budget\|AskUserQuestion\|handoff-ready\|notify\|a2a\|peer' plugin/skills plugin/agents
```

Expected hits, and only these: the implementer and tester bullet telling the agent not to use `AskUserQuestion`. Fix any other hit by deleting the sentence that names the removed feature.

- [ ] **Step 5: Commit**

```bash
uv run ruff check src tests && uv run ruff format --check src tests
git add -A src tests plugin deploy
git commit -s -m "Remove the handoff skill and the 60% context restart"
```

---

### Task 9: Docs and CHANGELOG

Every edit below quotes the current text verbatim. Apply each one with the Edit tool. "Delete
from X through Y" means delete both lines and everything between them. Where a step says "and the
blank line after it", also delete one blank line so that headings stay separated by exactly one
blank line.

**Files:**
- Delete: `docs/book/cardinal.md`
- Modify: `docs/book/SUMMARY.md`, `docs/book/README.md`, `README.md`, `docs/book/architecture.md`, `docs/book/commands.md`, `docs/book/getting-started.md`, `docs/book/keys.md`, `docs/book/tailnet.md`, `docs/book/troubleshooting.md`, `docs/book/setup/README.md`, `docs/book/setup/running-a-host.md`, `CHANGELOG.md`

`docs/book/setup/host-create.md` needs no change.

**Interfaces:**
- Consumes: the behaviour Tasks 2–8 leave (no new code).
- Produces: nothing code reads, except `CHANGELOG.md` still holding `## 0.12.0 ` for `tests/test_plugin.py::test_the_plugin_and_the_package_share_one_version`.

- [ ] **Step 1: Delete the cardinal page and its contents entry**

```bash
git rm docs/book/cardinal.md
```

In `docs/book/SUMMARY.md`, delete this line:

```
* [cardinal](cardinal.md)
```

- [ ] **Step 2: Book introduction and repo README**

`docs/book/README.md`, delete this table row:

```
| cardinal | One machine, optional | An always-on Claude session that watches the fleet and tells you what needs you |
```

`README.md`:

```
The documentation is a book in [`docs/book/`](docs/book/README.md), published with GitBook. It covers setup, keys, the tailnet, every command, cardinal, troubleshooting and the architecture.
```

becomes

```
The documentation is a book in [`docs/book/`](docs/book/README.md), published with GitBook. It covers setup, keys, the tailnet, every command, troubleshooting and the architecture.
```

- [ ] **Step 3: architecture.md**

Programs table:

```
| `flocc` desktop command (`installer/`) | Go | Your desktop | Hosts and their answers, keys, shipping the service, the dashboard, `host create`, cardinal's setup |
| `flocc` service (`src/flocc`) | Python, standard library only | Each host | Polling, worktrees, sessions, publishing, escalation |
```

becomes

```
| `flocc` desktop command (`installer/`) | Go | Your desktop | Hosts and their answers, keys, shipping the service, the dashboard, `host create` |
| `flocc` service (`src/flocc`) | Python, standard library only | Each host | Polling, worktrees, sessions, publishing, the digest |
```

Components table, these six rows:

```
| Supervisor | Restarts sessions from their handoff note, enforces wall-clock limits, cleans up crashed issues |
| Publisher | Runs the check, guards the tester's files, pushes, opens the draft PR, comments on Linear |
| Notifier | Routes escalations, dedupes them, sends ntfy pushes, applies Linear labels, posts the daily digest |
| Claims ledger | SQLite in WAL mode: issue, session, claimed paths, dependencies, state |
| Pre-commit hook | Rejects commits that touch paths another live issue has claimed |
| Agent plugin | The tester, implementer and reviewer skills, the escalation rubric, coordination rules and handoff hooks |
```

become

```
| Supervisor | Restarts a crashed session once, enforces wall-clock limits, cleans up crashed issues |
| Publisher | Runs the check, guards the tester's files, pushes, opens the draft PR, comments on Linear |
| Digest | Posts the daily digest comment, which also carries problems no issue owns: a failed tick, an expiring token, a lapsed Claude login |
| Claims ledger | SQLite in WAL mode: issue, session, dependencies, state |
| Pre-commit hook | Rejects commits made by the review session |
| Agent plugin | The tester, implementer and reviewer skills and the hooks sessions run |
```

Admit step:

```
2. **Admit.** A new issue starts only if usage is below the thresholds, fewer than `session_cap` sessions run, its claimed paths do not overlap a live claim and its `depends_on` issues are done.
```

becomes

```
2. **Admit.** A new issue starts only if usage is below the thresholds, fewer than `session_cap` sessions run and its `depends_on` issues are done.
```

Implement step:

```
5. **Implement.** A fresh session works from the issue and the handoff note until the tests pass. Past 60% context it writes a handoff note, and the supervisor restarts it from that note under the same Remote Control name.
```

becomes

```
5. **Implement.** A fresh session works from the issue and the handoff note until the tests pass.
```

Failures table, these three rows:

```
| Session process gone while its claim is live | Restart once from the latest handoff note. On a second failure, `needs-decision` and a tier 2 escalation |
```

```
| The check fails | The same check runs on the base branch. If it fails the same way there, the repo is held as main-broken and no fix round starts. Otherwise up to `check_fix_rounds` (default 2) fresh implementer sessions, each with the failure output. After the last one fails, `needs-decision` |
```

```
| Agent needs a decision | It escalates with a default and a deadline and continues on the default, unless the decision is irreversible |
```

become, in the same places,

```
| Session process gone while its claim is live | Restart once. On a second failure the claim blocks and the service comments on the issue |
```

```
| The check fails | The same check runs on the base branch. If it fails the same way there, the repo is held as main-broken and no fix round starts. Otherwise up to `check_fix_rounds` (default 2) fresh implementer sessions, each with the failure output. After the last one fails, the claim blocks and the service comments on the issue |
```

```
| Agent needs a decision | It takes the spec's default and lists what needs the owner under "Needs the owner" in its summary. An idle session gets an idle notice on the issue |
```

(The `Wall-clock limit reached` row stays as it is.)

Coordination section, delete these two bullets:

```
- Claimed paths are checked at admission and again by the pre-commit hook. A file is claimed when it or a directory above it matches a claimed glob. An issue with no `paths` claims nothing up front, and its agent claims paths as it learns them with `flocc claim`.
```

```
- Agents message their peers with `SendMessage`. `flocc context` lists each agent's current peers in the same repository from the ledger.
```

A2A section: delete from

```
## A2A
```

through

```
The tick adds two kinds of message. When a PR opens or merges, each issue Linear lists as blocked by it gets a handoff naming the PR and its files, and a merge also notifies the repository's other live agents. The tick also drops pending messages whose issue has closed. See the [A2A design](../superpowers/specs/2026-10-24-a2a-layer-design.md) for the reasons behind these choices.
```

and the blank line after it, so `## Access and trust` follows the Coordination section's last bullet after one blank line.

cardinal section at the end of the file: delete the blank line before it and from

```
## cardinal
```

through

```
[cardinal](cardinal.md) is a separate session outside the VMs. It reads the fleet through the same `flocc -H all status` you run, and Linear through its MCP server, and reports to you in the Claude app. It has no more access to hosts than the key limited to the Incus bridge gives it, and the tailnet is blocked from its user.
```

The file now ends with `{% endhint %}` and one newline.

- [ ] **Step 4: commands.md**

Desktop commands table:

```
| `flocc setup` | Everything in order: keys, a host, old versions, cardinal, logins. See [Set up](setup/README.md#flocc-setup) |
| `flocc keys` | Make, check and store the Linear key, GitHub tokens, ntfy topic and the optional Tailscale client. See [Keys](keys.md) |
```

becomes

```
| `flocc setup` | Everything in order: keys, a host, old versions, logins. See [Set up](setup/README.md#flocc-setup) |
| `flocc keys` | Make, check and store the Linear key, GitHub tokens and the optional Tailscale client. See [Keys](keys.md) |
```

Delete this row:

```
| `flocc cardinal setup\|login\|status\|attach\|restart\|remove` | The always-on assistant. See [cardinal](cardinal.md) |
```

Host commands table: delete these four rows:

```
| `permissions report [--days N]` | Count the auto-mode blocks and prompts sessions met. See [Run a host](setup/running-a-host.md#auto-mode-blocks) |
```

```
| `a2a serve [--bind ADDR] [--port N]` | Serve the host's A2A endpoint. It binds the address `tailscale ip -4` prints, port 7443, unless given. Run by `flocc-a2a.service` |
| `inbox` | Hook for agent sessions: hand the session its pending A2A messages as additional context. Prints nothing and exits 0 on failure, so the call it rides on is not blocked |
```

```
| `mcp-a2a` | Run the `flocc-a2a` MCP server on stdio. It offers the tools `agents`, `send`, `notify` and `replies`. Host sessions get it from the plugin; desktop sessions add it once, see [Getting started](getting-started.md#6-let-your-desktop-sessions-reach-the-agents) |
```

and replace these two rows:

```
| `escalate`, `claim`, `commit`, `done`, `review-done`, `handoff-ready`, `context` | Used by agent sessions, not by you |
```

```
| `stop` | Hook for agent sessions at the end of a turn: record an answered question, deliver pending messages and continue the turn, or else record the idle stop |
```

with

```
| `commit`, `done`, `review-done`, `context` | Used by agent sessions, not by you |
```

```
| `stop` | Hook for agent sessions at the end of a turn: record its background tasks and the idle stop |
```

Choosing the host, delete this bullet:

```
- `flocc cardinal` takes no `-H`. Limit its setup with `--only <host>`.
```

Display table:

```
| full | Alternate screen: a rail of the command's sections, the current section's steps with their live output, a progress bar. Prompts open as a modal over the dimmed screen | `setup`, `host create`, `ship`, `login`, `cardinal setup`, `cardinal login`, and the dashboard |
| inline | Running steps and any prompt below the scrollback. Each step moves up into scrollback when it ends | Short commands: `keys`, `keys check`, `cardinal restart`, one-shot `status` |
```

becomes

```
| full | Alternate screen: a rail of the command's sections, the current section's steps with their live output, a progress bar. Prompts open as a modal over the dimmed screen | `setup`, `host create`, `ship`, `login`, and the dashboard |
| inline | Running steps and any prompt below the scrollback. Each step moves up into scrollback when it ends | Short commands: `keys`, `keys check`, one-shot `status` |
```

```
A pipe, `NO_COLOR` or `TERM=dumb` gives plain whatever is set. Commands that hand the terminal to a child (forwarded host commands, `host run`, `cardinal attach`) always run plain. A full-screen run on a terminal smaller than 60 columns by 20 rows switches to inline. Below 100 columns the rail of sections sits above the steps instead of beside them.
```

becomes

```
A pipe, `NO_COLOR` or `TERM=dumb` gives plain whatever is set. Commands that hand the terminal to a child (forwarded host commands and `host run`) always run plain. A full-screen run on a terminal smaller than 60 columns by 20 rows switches to inline. Below 100 columns the rail of sections sits above the steps instead of beside them.
```

Dashboard:

```
Bare `flocc`, or `flocc status --watch`, on a terminal. The fleet screen has four panes:
```

becomes

```
Bare `flocc`, or `flocc status --watch`, on a terminal. The fleet screen has three panes:
```

Delete these two bullets:

```
- cardinal: per saved install, its unit, session age, last turn and login. Shown only when an install is saved.
```

```
- cardinal: the table `flocc cardinal status` prints.
```

Keys table:

```
| tab | fleet | Move between the hosts, cardinal and issues panes |
```

becomes

```
| tab | fleet | Move between the hosts and issues panes |
```

Delete this row:

```
| c | cardinal screen | Attach read-only to the selected install |
```

```
`a`, `s` and `c` hand the terminal to the child, and so does `r` after its `y`. Each returns to the dashboard when the child exits.
```

becomes

```
`a` and `s` hand the terminal to the child, and so does `r` after its `y`. Each returns to the dashboard when the child exits.
```

Files table, delete the last row:

```
| `~/.cache/flocc/cardinal-<user>.log` | The log of a cardinal command |
```

- [ ] **Step 5: getting-started.md**

Setup list:

```
5. Asks for the GitHub publish token and the ntfy topic.
6. Offers to remove old installs it finds on the machine.
7. Sets up [cardinal](cardinal.md), if you want it.
8. Signs in to Claude on each host.
```

becomes

```
5. Asks for the GitHub publish token.
6. Offers to remove old installs it finds on the machine.
7. Signs in to Claude on each host.
```

In the example description, delete this line inside the `agent` block:

```
paths: [src/export/**]
```

Agent block table, delete these two rows:

```
| `paths` | File globs the issue claims up front, so two issues do not edit the same files |
```

```
| `allow` | Extra launch grants. See [Run a host](setup/running-a-host.md#subagents-in-agent-sessions) |
```

Section 6: delete from

```
## 6. Let your desktop sessions reach the agents
```

through

```
Your sessions can then list the running agents with `agents`, message one with `send`, tell a repository's agents about a shared change with `notify`, and read answers with `replies`. A message from you reaches an agent at its next tool call or turn end, and wakes a session that is idle at its prompt. See [Architecture](architecture.md#a2a).
```

and the blank line after it, so `## Run an issue again` follows section 5's last paragraph after one blank line.

- [ ] **Step 6: keys.md**

```
flocc holds five keys on your desktop. `flocc keys` makes, checks and stores them, and `flocc ship` sends them to each host.
```

becomes

```
flocc holds four keys on your desktop. `flocc keys` makes, checks and stores them, and `flocc ship` sends them to each host.
```

Delete this table row:

```
| ntfy topic | `ntfy_topic` | Push notifications | `FLOCC_NTFY_TOPIC` |
```

In the `.env` example, delete this line:

```
FLOCC_NTFY_TOPIC=flocc-a-long-random-topic
```

Delete this bullet:

```
- A changed ntfy topic is stored. It reaches a host on that host's next `flocc ship`.
```

Delete from

```
### ntfy topic
```

through

```
flocc generates a random topic and stores it. It shows a QR code for the ntfy app on Android, with the `https://ntfy.sh/<topic>` address beside it, and sends one test push. Answer whether it arrived. If it did not, subscribe in the app and flocc sends another.
```

and the blank line after it, so `### Tailscale OAuth client` follows the publish-token paragraph after one blank line.

- [ ] **Step 7: tailnet.md**

Task 3 removed the tag-to-tag grant but kept the `a+":7443"` deny entry in the policy's `tests` (`grep -n 7443 installer/internal/tailnet/policy.go` prints that one line), so the port lists in the `tests` text and JSON stay as they are.

Policy checks:

```
- No grant or ACL with the tag as a source, except the A2A grant below. The tag must be able to reach nothing else.
```

becomes

```
- No grant or ACL with the tag as a source. The tag must be able to reach nothing.
```

Delete this bullet:

```
- A grant from the tag to itself on `tcp:7443` and nothing else, so hosts can reach each other's A2A endpoints.
```

Merge table, delete this row:

```
| `grants` | A grant from the tag to itself on `tcp:7443` |
```

Old-policy section:

```
### A policy from before 0.11.0

Setup adds the A2A grant when it is missing, so paste the merged file as above. The `tests` entry that denies the tag your devices is kept as you pasted it, so add `<device>:7443` to its `deny` list for each device by hand. Without it, nothing checks that a host cannot reach a device on the A2A port.
```

becomes

```
### A policy from 0.11.0 or 0.12.0

Those versions added a grant from the tag to itself on `tcp:7443`, for A2A between hosts. Setup now reports it as a grant with the tag as its source and does not rewrite it. Delete it from your policy and paste the policy again. The `<device>:7443` entries in the `tests` deny list stay.
```

- [ ] **Step 8: troubleshooting.md**

Not-picked-up list, items 6 and 7:

```
6. The `agent` block, if any, parses. An unknown key, a `wall_clock` that is not like `90m` or `2h`, a missing closing fence or an `mcp` server the host does not configure gets a "Skipping:" comment.
7. Admission allows it: the host is under `session_cap`, usage is below `five_hour_max_pct` and `weekly_max_pct`, free disk is above `min_free_disk_gb`, its `paths` do not overlap a live claim, and every `depends_on` issue is done. `flocc -H <host> logs` prints `held, <reason>` for an issue that waits.
```

become

```
6. The `agent` block, if any, parses. An unknown key, a `wall_clock` that is not like `90m` or `2h`, a missing closing fence or an `mcp` server the host does not configure gets a "Skipping:" comment. `allow` and `paths` are unknown keys now; delete those lines from older issues.
7. Admission allows it: the host is under `session_cap`, usage is below `five_hour_max_pct` and `weekly_max_pct`, free disk is above `min_free_disk_gb`, and every `depends_on` issue is done. `flocc -H <host> logs` prints `held, <reason>` for an issue that waits.
```

```
A failed `./setup` or a worktree that is not clean after `./setup` fails the start of the issue with a tier 2 escalation. Gitignore everything `./setup` and the check write.
```

becomes

```
A failed `./setup` or a worktree that is not clean after `./setup` fails the start of the issue, and the service comments on the issue. Gitignore everything `./setup` and the check write.
```

Setup and ship table:

```
| A running session still has old plugins or settings | Sessions keep what they launched with. The upgrade reaches them at their next launch or handoff |
```

becomes

```
| A running session still has old plugins or settings | Sessions keep what they launched with. The upgrade reaches them at their next launch |
```

Claude login table:

```
| One push says the login lapsed | The tick pages once, and again only after a login has been seen. Run `flocc login -H <name>` |
```

becomes

```
| The digest issue says the login lapsed | The tick reports it once, and again only after a login has been seen. Run `flocc login -H <name>` |
```

End of file: delete the blank line before `## cardinal` and everything from

```
## cardinal
```

through the last line of the file

```
The subagent gate refuses launches that exceed the phase's budget or are not in `plugin/agents/`. The refusal names the rule. Raise it with an `allow:` line. See [Subagents in agent sessions](setup/running-a-host.md#subagents-in-agent-sessions).
```

This removes the cardinal table and the `## An agent says a launch was refused` section. The file now ends with the Disk paragraph (`A full disk breaks SSH and the installer. ...`) and one newline.

- [ ] **Step 9: setup/README.md**

Setup steps table:

```
| Keys | The publish token and the ntfy topic | Takes them from the environment or `.env`, asks for what is missing, then checks them |
```

becomes

```
| Keys | The publish token | Takes it from the environment or `.env`, asks for it if missing, then checks it |
```

Delete this row:

```
| Cardinal | One machine: the saved one, else the first host's | Status ok: nothing. Failing: setup. A `cardinal` user without a saved file: asks to adopt, default no. No cardinal: asks, default yes |
```

```
| Logins | Each host's and cardinal's login | `full` is done and `unknown` is shown. Anything else runs `flocc login` or `flocc cardinal login` with the pool account you give |
```

becomes

```
| Logins | Each host's login | `full` is done and `unknown` is shown. Anything else runs `flocc login` with the pool account you give |
```

```
Host create runs its firewalld and Docker steps as root in one `ssh -t ... sudo`, so a machine whose sudo asks for a password works from a terminal, and sudo asks once. Cardinal's removal and root work share one `ssh -t` the same way.
```

becomes

```
Host create runs its firewalld and Docker steps as root in one `ssh -t ... sudo`, so a machine whose sudo asks for a password works from a terminal, and sudo asks once. Removing an old user's install runs as one root pass the same way.
```

Add a host list:

```
- The labels `claude`, `escalation`, `needs-decision` and one per repository, created when missing.
```

becomes

```
- The label `claude` and one label per repository, created when missing.
```

Import section:

```
A host shipped before 0.5.0, or from another desktop, has no answers in a host file, only an SSH destination. `flocc ship` refuses it with a pointer to the import. Import it first, so the desktop takes its answers, its keys and its ntfy topic instead of making new ones:
```

becomes

```
A host shipped before 0.5.0, or from another desktop, has no answers in a host file, only an SSH destination. `flocc ship` refuses it with a pointer to the import. Import it first, so the desktop takes its answers and its keys instead of making new ones:
```

Delete the hint after the import code block, from

```
{% hint style="warning" %}
Run the import before `flocc keys`. `flocc keys` first mints a new ntfy topic, and the next ship then moves the host off the topic your phone follows.
```

through the `{% endhint %}` that closes it, and the blank line after it.

Answers document example, delete this line:

```
ntfy_topic = "flocc-a-long-random-topic"
```

```
With `--interactive`, ship runs over `ssh -t` and asks on the host's terminal instead: the Tailscale login, the three secrets, the config form and the ntfy test push. It sends no answers document.
```

becomes

```
With `--interactive`, ship runs over `ssh -t` and asks on the host's terminal instead: the Tailscale login, the three secrets and the config form. It sends no answers document.
```

Upgrade a host:

```
Run `flocc ship -H <name>` again. It ships the current commit as a git bundle, since the host has no credential for the repository. A session already running keeps the plugin, hooks and settings it was launched with; the upgrade reaches it at its next launch or handoff.
```

becomes

```
Run `flocc ship -H <name>` again. It ships the current commit as a git bundle, since the host has no credential for the repository. A session already running keeps the plugin, hooks and settings it was launched with; the upgrade reaches it at its next launch.
```

Claude login:

```
When the login lapses, the next tick sends one tier 1 push. It pages again only after a login has been seen in between. Shipping to a host whose login has lapsed leaves its already-enabled timer on, so ticks keep running and the one page stands until you run `flocc login -H <name>`.
```

becomes

```
When the login lapses, the next tick reports it once on the digest issue and in the service log. It reports again only after a login has been seen in between. Shipping to a host whose login has lapsed leaves its already-enabled timer on, so ticks keep running until you run `flocc login -H <name>`.
```

What flocc cannot do for you:

```
- **Pin the digest issue in Linear.** `flocc host add` creates the digest issue and the labels `claude`, `escalation`, `needs-decision` and one per mapped repository when they are missing, but `host import` does not. A host file written by import or by hand needs them in the team already. A repository's label is named exactly like its `[repos.<name>]` key; a repository without one is mapped but gets no issues. Doctor fails on a missing `claude`, `escalation` or `needs-decision` label or digest issue and warns about missing repository labels.
```

becomes

```
- **Pin the digest issue in Linear.** `flocc host add` creates the digest issue, the `claude` label and one label per mapped repository when they are missing, but `host import` does not. A host file written by import or by hand needs them in the team already. A repository's label is named exactly like its `[repos.<name>]` key; a repository without one is mapped but gets no issues. Doctor fails on a missing `claude` label or digest issue and warns about missing repository labels.
```

- [ ] **Step 10: setup/running-a-host.md**

```
This page covers what you tune after a host is shipped: how it ticks, its config, disk use, several hosts at once, auto-mode blocks and the limits on subagents.
```

becomes

```
This page covers what you tune after a host is shipped: how it ticks, its config, disk use, several hosts at once and auto-mode blocks.
```

Config table, delete this row:

```
| `context_restart_pct` | 60.0 | A session past this context use writes a handoff note and restarts |
```

```
Each `[repos.<name>]` table maps a repository. It takes `path`, `base_branch`, `draft`, `commit_trailer` and `shared_paths`. A private repository of a GitHub Free organization cannot have draft PRs, so set `draft = false` there.
```

becomes

```
Each `[repos.<name>]` table maps a repository. It takes `path`, `base_branch`, `draft` and `commit_trailer`. A private repository of a GitHub Free organization cannot have draft PRs, so set `draft = false` there.
```

Auto-mode blocks:

```
Host sessions run in auto mode. When its classifier blocks a command, the agent hears why and works around it or gives up. Nothing waits for you. After three blocks in a row Claude Code shows a prompt, which denies itself after two minutes. Every block and prompt is logged per issue in `~/.local/state/flocc/<KEY>/permissions.jsonl`.

`flocc -H <host> permissions report` counts them. Turn what you see into the host file's `[permissions]` table, then run `flocc ship -H <host>`. Sessions pick it up at their next launch.
```

becomes

```
Host sessions run in auto mode. When its classifier blocks a command, the agent hears why and works around it or gives up. Nothing waits for you. After three blocks in a row Claude Code shows a prompt, which denies itself after two minutes.

To let a command through, add it to the host file's `[permissions]` table, then run `flocc ship -H <host>`. Sessions pick it up at their next launch.
```

Replace everything from

```
## Subagents in agent sessions
```

through

```
Answer a tier 2 question in a Linear comment, which the service forwards to the session, or directly through Remote Control.
```

(both the subagent section and `## Questions and escalations`, with its tier table) with

```
## When a run needs you

The service comments on the issue when a run blocks, when `./setup` fails at prepare, and when an idle or stall notice goes unanswered. Problems that belong to no issue, such as a failed tick, an expiring GitHub token or a lapsed Claude login, go to the digest issue and the service log. Nothing is pushed to your phone.

A session does not ask you questions. Where the spec leaves a choice open it takes the default, and it lists what needs you under "Needs the owner" in its `flocc done` summary, which goes into the PR body. To steer a running session, comment on the issue; the service types the comment into the session.
```

Keep one blank line between that last paragraph and `## Upgrade notes`.

Upgrade notes, insert this bullet as the first item of the list (above the `digest_issue` bullet):

```
- `ntfy_url`, `context_restart_pct` and a repo's `shared_paths` are ignored with a warning; delete them from `config.toml`. An `agent` block with an `allow:` or `paths:` line is skipped with a "Skipping:" comment until the line is removed. The `escalation` and `needs-decision` labels are no longer used; delete them in Linear if you like.
```

Scratch-issue checklist:

```
4. Exercise escalation. In a session run `flocc escalate --tier 1 --cause smoke --summary test --action ignore`, then tick. You should get one ntfy push and the `escalation` label. Repeat the command and tick again: you should get a second comment and no second push.
5. Exercise a rerun: block a second issue, then run `flocc release ENG-13`. The issue should be back in Todo, and the next tick restarts it on the kept branch.
```

becomes

```
4. Exercise a rerun: block a second issue, then run `flocc release ENG-13`. The issue should be back in Todo, and the next tick restarts it on the kept branch.
```

- [ ] **Step 11: CHANGELOG.md**

Insert this between the header paragraph (ending `` `v<version>`. ``) and `## 0.12.0 (2026-10-25)`, with one blank line above and below:

```markdown
## Unreleased

flocc drops cardinal, A2A and the escalation path built in 0.9 to 0.12. A run that needs you says
so on its Linear issue.

- cardinal as built in 0.9 to 0.12 is gone: `flocc cardinal` and its subcommands, cardinal's step
  in `flocc setup`, the dashboard's cardinal pane and `c` key, the CARDINAL table in
  `flocc status -H all` and `~/.cache/flocc/cardinal-<user>.log`. `flocc setup` offers to remove
  the old `cardinal` user.
- A2A is gone: `flocc-a2a.service`, `flocc a2a serve`, `flocc mcp-a2a` and its tools, the
  `flocc inbox` hook, and the token minting and peer lists in `flocc ship`, which no longer writes
  `~/.config/flocc/a2a.toml`. Remove the server from desktop sessions with
  `claude mcp remove --scope user flocc-a2a`, and the tailnet grant from `tag:flocc-host` to
  itself on `tcp:7443`, which setup now reports.
- Escalation is gone: `flocc escalate`, the escalate skill, the tiers, ntfy pushes and the ntfy
  topic in `flocc keys`. flocc no longer creates the `escalation` and `needs-decision` labels and
  doctor no longer requires them. The service comments on the issue when a run blocks, prepare
  fails, or an idle or stall notice goes unanswered. A failed tick, an expiring token and a lapsed
  Claude login go to the digest issue and the journal.
- The subagent gate and its launch budgets are gone, with `flocc gate`, `allow:` grants in the
  agent block and in comments, and `launches N/M` in status and the dashboard. An `allow:` line in
  the agent block is now an unknown key, and the issue is skipped with a comment.
- The permission hook and `flocc permissions report` are gone. Auto mode and the host file's
  `[permissions]` allow rules stay.
- Path claims are gone: `flocc claim`, the pre-commit hook's claim check and the agent block's
  `paths:` key, which is now refused as unknown. Repo config drops `shared_paths`. The pre-commit
  hook still refuses commits from the review session.
- The coordinate and handoff skills and the restart at 60% context are gone, with
  `flocc handoff-ready` and `context_restart_pct`.
- Open `AskUserQuestion` questions are no longer tracked: they do not extend the wall clock, and
  the skills tell agents not to ask.
- `ntfy_url`, `context_restart_pct` and `shared_paths` in `config.toml` are ignored with a warning
  if still present.
- The ledger gains `events` and `holds` tables and drops the claim's `paths` and
  `escalation_offset` columns on its first open by the new version.
```

- [ ] **Step 12: Check for leftovers**

```bash
grep -rn -i 'a2a\|cardinal\|escalat\|ntfy\|flocc claim\|handoff-ready\|permissions report\|grant\|needs-decision' docs/book README.md
```

Expected hits, and only these:

- `docs/book/tailnet.md`: "a policy that grants the tag no destinations", "No grant or ACL with the tag as a source", "No grant or ACL from `*`. Tailscale's default grant from `*`", the `"grants": [` key in the generated JSON, "Narrowed to `autogroup:member` in grants and ACLs", "A grant or ACL with the tag as its source is reported", and the new `### A policy from 0.11.0 or 0.12.0` paragraph. All are the tailnet policy's own `grants`, not flocc grants.
- `docs/book/architecture.md`: "your policy grants that tag no destinations".
- `docs/book/setup/running-a-host.md`: the new upgrade-note bullet, and `idle_escalate_s`/`stall_escalate_s` rows in the config table if present.

Any other hit is a missed passage: fix it in the file's voice. Also check that no link points at a deleted page or anchor:

```bash
grep -rn 'cardinal.md\|#a2a\|#subagents-in-agent-sessions\|#6-let-your-desktop' docs/book README.md
```

Expected: no output.

- [ ] **Step 13: Commit**

```bash
uv run pytest -q tests/test_plugin.py tests/test_names.py
git add -A docs/book README.md CHANGELOG.md
git commit -s -m "docs: drop removed features from the book"
```

---

### Task 10: Full verification

- [ ] **Step 1: Run the whole check once**

```bash
just check
```

Expected: ruff, pyrefly, shellcheck, gofmt and `go vet` clean; `uv run pytest -q` and `go test ./...` PASS.

- [ ] **Step 2: Sweep for leftovers across the tree**

```bash
git ls-files | grep -i -E 'a2a|cardinal|escalat|gate\.py|grants\.py|permission\.py|inbox|delivery|handoff/|coordinate/'
grep -rn -E 'flocc (escalate|claim|gate|permission|inbox|a2a|mcp-a2a|handoff-ready|cardinal)' src tests plugin installer deploy setup justfile docs/book README.md
```

Expected: the first command lists only dated files under `docs/superpowers/` (old specs and plans stay as history); the second prints nothing.

- [ ] **Step 3: Ask the docket skill to retire the superseded decisions**

With the docket skill, retire each decision on a removed feature (among them d68 AskUserQuestion pushes, d86 the supervisor's prompt judgement, d89 cardinal's owner queue, d160 question credit; d101 and d162 are already superseded), pointing at d171–d177, and correct d155 (the stall notice now comments on the issue instead of escalating). Run `docket context --file` on each deleted module's path to find the rest. Commit the ledger with this task:

```bash
git add .docket
git commit -s -m "docket: retire decisions on removed features"
```

