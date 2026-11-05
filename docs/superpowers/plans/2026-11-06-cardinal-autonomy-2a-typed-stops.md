# Cardinal autonomy 2a: typed stops — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every stop has a cause and a type in code, events carry both, the service retries a `publish-failed` block once by itself, cardinal's triage decides from the type, and the gate's `fix` alias and the `cardinal_fix` cap key are gone.

**Architecture:** A new table module, `src/flocc/causes.py`, maps each stop cause to one of four types (`transient`, `agent`, `owner`, `repo`); `control.RESUMABLE` is derived from it. `_block` looks its cause up before it kills the session and passes the type on its `Notice`, and `Notifier.send` writes the cause and type into two new nullable `events` columns, which the JSON line carries as `"cause"` and `"type"`. A new poller step, `retry_transient`, sets a claim blocked with `publish-failed` back to running once, 30 minutes after the block, with one publish try left. Two ledger migrations add the columns and fold `cardinal_fix:KEY` into `cardinal_resume:KEY`.

**Tech Stack:** Python 3, sqlite3, pytest via `uv run pytest`, Markdown skills for cardinal.

**Spec:** `docs/superpowers/specs/2026-11-06-cardinal-autonomy-2-design.md`, section 1 ("Typed stops" and "`fix` alias") and its testing lines. Section 2 (resume on comment) is another plan; this one does not touch `forward_comments`, the comment scan, or `_finish_phase`'s review-fix and rebase branches.

## Global Constraints

- Types are exactly `transient`, `agent`, `owner`, `repo`. Causes and types, verbatim from the spec's table: `publish-failed` transient; `crash`, `idle`, `timed-out`, `check-failed`, `no-changes` agent; `needs-owner`, `check-cap`, `tests-touched`, `no-tests`, `branch-moved`, `rebase-conflict` owner; `base-broken` repo.
- `RESUMABLE` is the `agent` causes plus `needs-owner`.
- An unknown cause raises in `_block` before the session is killed.
- `check-cap` is any check-failed block with `fix_attempts >= check_fix_rounds`.
- The `events` columns are `cause` and `type`, nullable. Old rows and non-stop events read null. The JSON line carries `"cause"` and `"type"` next to `"kind"`. `detail` keeps its current text, `"{cause}: "` prefix included.
- Transient retry: 30 minutes by `blocked.md`'s mtime, once per claim (meta `transient_retry:KEY`), sets only `state=running`, sets `publish_failures:KEY` to `PUBLISH_TRIES - 1`, runs next to `restart_timed_out` with its hold and `still_wanted` checks, comments on the issue. A second `publish-failed` after the retry is typed `owner`. `_start` clears `transient_retry:KEY`.
- The gate refuses `fix` as an unknown verb. `control._CAPS` and `_start` drop `cardinal_fix`.
- Commits: `git commit -s`, no `Co-Authored-By` line and no AI attribution, subject 72 characters or fewer.
- While iterating run only the touched tests: `uv run pytest tests/<file>::<name> -q`. Run the full check once at the end: `PATH="$(dirname $(uvx --from shellcheck-py which shellcheck)):$PATH" just check`.
- `just lint` runs `ruff format --check`, `ruff check` and `pyrefly check`. Before each commit run `uv run ruff format` and `uv run ruff check` on the files you touched; the code here is written to 100 columns, but let the formatter decide.
- Comments carry only facts the code cannot show; match the file's density. No narration, no ticket references.
- Reuse existing helpers (`control.stop_cause`, `poller.still_wanted`, `agentfs.read_text`, `ledger.meta_get/meta_set`); grep before writing a new one.
- Work only in `/home/novusedge/Projects/Personal/worktrees/flocc-typed-stops` on branch `feat/typed-stops`. Use absolute paths; every command below runs from that directory.

## Decisions taken in this plan

- **`Notice` gains `stop_type` only, not `cause`.** The spec says `Notice` gains `cause` and `stop_type`, but `Notice.cause` already exists and holds the dedupe key `"NF-1:publish-failed"`. The stop's cause is that key's last segment, which `Notifier.send` already derives for the detail. So `send` emits `cause=<that segment>` when `stop_type` is set, and null otherwise. A second cause field would duplicate it under a colliding name.
- **The `Event` attribute is `stop_type`**, the column and the JSON key are `type`, so Python code never shadows the builtin.
- **The same-failure block keeps `kind="ended"`** at the last round. It now writes `check-cap` there, typed `owner`, which is what triage acts on. Only the round-cap block is `kind="cap"`, as today.
- **The retry unlinks `blocked.md`** when it sets the claim running. A publish that then succeeds must not leave a stale "why it stopped" for `run-summary`; a second failure writes a new one.
- **The retry skips a claim without the session's `done.json`.** The publish reads it again; without it, `supervise` would treat the claim as a dead session and relaunch a paid one. It also skips admission: the retry starts a check and a push, not a session.
- **The first `publish-failed` notice says the service tries once more in 30 minutes.** The owner sees that action text on the issue, so it must not tell them to fix the token first. After the retry, the action is today's text.
- **`causes.RESUMABLE` lives in `causes.py`**, and `control` imports it, so the table and the set cannot drift.
- **The `cardinal_fix` fold is a ledger migration**, so it runs on every host at the first connect after deploy, and the spec's "same migration" holds everywhere. It copies a non-empty `cardinal_fix:KEY` into `cardinal_resume:KEY` unless that key already holds a value, then deletes every `cardinal_fix:` key. The host query in Task 5 is read-only and is the owner's or controller's step.
- **Migration numbering.** Task 2 appends migration 6 (index 5) and Task 5 appends migration 7 (index 6). Section 2 changes no schema. If another PR appends a migration first, append after it and update the version numbers in the tests; never edit a shipped entry.
- **CHANGELOG.** The Unreleased bullet that introduced `resume` says `fix` stays as an alias for one release. That release has not been cut, so the sentence is removed rather than contradicted by a new bullet.

## Review Focus

1. A `publish-failed` block already older than 30 minutes when this deploys is retried on the first tick. That is intended: the retry is one check and one push. Task 4's mtime test pins that age alone decides it.
2. A claim the owner reruns after its retry must get its own retry. `_start` clears `transient_retry:KEY`. Task 4 extends the fresh-claim test to pin it.
3. A blocked `publish-failed` claim whose `done.json` is gone must stay blocked, not become a running claim with no session. Task 4 pins it.
4. Events written before the migration, and events printed by a follower process still running the old code, carry no cause or type. Triage must fall back to `kind`. Task 2 pins the null JSON fields; Task 6 pins the skill's null rule.
5. A typo'd cause in a future `_block` call must fail without killing the live session or changing the claim. Task 3 pins it, and `test_causes.py` pins that the table and the source agree.

---

### Task 1: The cause table, and `RESUMABLE` from it

**Files:**
- Create: `src/flocc/causes.py`
- Modify: `src/flocc/control.py:23-25` (the `RESUMABLE` literal and its comment), `:106-107` (the comment above the `fix_attempts` guard)
- Test: `tests/test_causes.py` (new)

**Interfaces:**
- Produces: `causes.TRANSIENT`, `causes.AGENT`, `causes.OWNER`, `causes.REPO` (str constants), `causes.TYPES: frozenset[str]`, `causes.CAUSES: dict[str, str]`, `causes.type_of(cause: str) -> str` (raises `ValueError("unknown stop cause '...'")`), `causes.RESUMABLE: frozenset[str]`. `control.RESUMABLE` is the same object.

- [ ] **Step 1: Write the failing tests**

Create `tests/test_causes.py`:

```python
import pytest

from flocc import causes, control


def test_every_cause_has_one_of_the_four_types():
    assert causes.TYPES == {"transient", "agent", "owner", "repo"}
    assert set(causes.CAUSES.values()) <= causes.TYPES


def test_the_table_matches_the_spec():
    assert causes.CAUSES == {
        "publish-failed": "transient",
        "crash": "agent",
        "idle": "agent",
        "timed-out": "agent",
        "check-failed": "agent",
        "no-changes": "agent",
        "needs-owner": "owner",
        "check-cap": "owner",
        "tests-touched": "owner",
        "no-tests": "owner",
        "branch-moved": "owner",
        "rebase-conflict": "owner",
        "base-broken": "repo",
    }


def test_resumable_is_the_agent_stops_and_needs_owner():
    agent = {c for c, t in causes.CAUSES.items() if t == causes.AGENT}
    assert control.RESUMABLE == agent | {"needs-owner"}
    assert control.RESUMABLE == {
        "needs-owner",
        "check-failed",
        "no-changes",
        "idle",
        "crash",
        "timed-out",
    }


def test_an_unknown_cause_is_refused():
    assert causes.type_of("crash") == "agent"
    with pytest.raises(ValueError, match="unknown stop cause 'crashed'"):
        causes.type_of("crashed")
```

- [ ] **Step 2: Run them to verify they fail**

Run: `uv run pytest tests/test_causes.py -q`
Expected: FAIL with `ImportError: cannot import name 'causes' from 'flocc'`.

- [ ] **Step 3: Implement**

Create `src/flocc/causes.py`:

```python
"""Why a run stopped, and who acts on it: the service (transient), cardinal (agent), the owner,
or nobody until the repo's base is fixed (repo)."""

TRANSIENT, AGENT, OWNER, REPO = "transient", "agent", "owner", "repo"
TYPES = frozenset({TRANSIENT, AGENT, OWNER, REPO})

CAUSES = {
    # A second failure, after the service's one retry, is the owner's (supervisor._block).
    "publish-failed": TRANSIENT,
    # supervise already relaunched the session once, so this is its second death.
    "crash": AGENT,
    "idle": AGENT,
    "timed-out": AGENT,
    # The same failure twice with fix rounds left; at the last round it is check-cap.
    "check-failed": AGENT,
    "no-changes": AGENT,
    # cardinal resumes it only when the issue text settles every question.
    "needs-owner": OWNER,
    "check-cap": OWNER,
    "tests-touched": OWNER,
    "no-tests": OWNER,
    "branch-moved": OWNER,
    "rebase-conflict": OWNER,
    "base-broken": REPO,
}

RESUMABLE = frozenset({c for c, t in CAUSES.items() if t == AGENT} | {"needs-owner"})


def type_of(cause: str) -> str:
    try:
        return CAUSES[cause]
    except KeyError:
        raise ValueError(f"unknown stop cause {cause!r}") from None
```

In `src/flocc/control.py`, add the import after `from flocc.agentblock import settings_from_json`:

```python
from flocc.causes import RESUMABLE
```

and delete these lines (currently `:23-25`):

```python
# The stops a note can redirect. tests-touched guards the tester's work against the executor,
# the publish, branch and rebase stops need the host or the owner, and no-tests needs criteria.
RESUMABLE = frozenset({"needs-owner", "check-failed", "no-changes", "idle", "crash", "timed-out"})
```

In `resume`, replace the comment above `if cause == "check-failed" and claim.fix_attempts >= cap:` (currently `:106`):

```python
    # The same-failure block and the cap block share the cause; only the rounds tell them apart.
```

with:

```python
    # Blocks at the cap write check-cap now; one written before that still says check-failed.
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_causes.py tests/test_control.py -q`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/causes.py src/flocc/control.py tests/test_causes.py
git commit -s -m "feat(causes): one table of stop causes and their types"
```

---

### Task 2: `cause` and `type` on events, notices and the JSON line

**Files:**
- Modify: `src/flocc/ledger.py:54-78` (`_MIGRATIONS`: append one entry)
- Modify: `src/flocc/events.py` (`_COLUMNS`, `Event`, `emit`, `line`)
- Modify: `src/flocc/notice.py` (`Notice` field, `Notifier.send`)
- Modify: `src/flocc/canary.py:31` (the `base-broken` emit in `mark`)
- Test: `tests/test_ledger.py` (`test_a_fresh_ledger_is_at_the_latest_version`, new helper and test), `tests/test_events.py`, `tests/test_notice.py`, `tests/test_canary.py`

**Interfaces:**
- Consumes: `causes.type_of`, `causes.REPO` (Task 1).
- Produces: `events.emit(con, kind, now, *, issue_key=None, repo=None, detail="", cause: str | None = None, stop_type: str | None = None) -> int`. `Event` gains `cause: str | None = None` and `stop_type: str | None = None` as its last two fields. `Notice` gains `stop_type: str | None = None` as its last field; when it is set, `Notifier.send` emits `cause=<last segment of notice.cause>` and `stop_type=notice.stop_type`. The ledger is at version 6.

- [ ] **Step 1: Write the failing tests**

In `tests/test_ledger.py`, change the column set in `test_a_fresh_ledger_is_at_the_latest_version` to include the two new columns:

```python
    assert {
        "id",
        "kind",
        "issue_key",
        "repo",
        "detail",
        "at",
        "printed_at",
        "acked_at",
        "prints",
        "cause",
        "type",
    } == (_columns(con, "events"))
```

and add after `test_a_second_connection_finds_the_ledger_migrated`:

```python
def _ledger_at(path, version):
    raw = sqlite3.connect(path)
    raw.executescript(ledger._SCHEMA)
    for statements in ledger._MIGRATIONS[:version]:
        for sql in statements:
            raw.execute(sql)
    raw.execute(f"PRAGMA user_version = {version}")
    raw.commit()
    return raw


def test_events_written_before_the_stop_columns_read_null(tmp_path):
    path = tmp_path / "old.sqlite"
    raw = _ledger_at(path, 5)
    raw.execute(
        "INSERT INTO events (kind, issue_key, detail, at)"
        " VALUES ('ended', 'NF-1', 'crash: x', 1.0)"
    )
    raw.commit()
    raw.close()
    con = ledger.connect(path)
    [e] = events.unacked(con)
    assert (e.kind, e.detail, e.cause, e.stop_type) == ("ended", "crash: x", None, None)
```

Add `events` to the flocc import at the top of `tests/test_ledger.py` (it imports `from flocc import ledger` today; make it `from flocc import events, ledger`), and keep `import sqlite3`, which `_v0_ledger` already uses.

Append to `tests/test_events.py`:

```python
def test_a_stop_event_carries_its_cause_and_type_next_to_the_kind(con):
    events.emit(
        con, "ended", 1.0, issue_key="NF-1", detail="crash: x", cause="crash", stop_type="agent"
    )
    events.emit(con, "daily", 2.0, detail="2026-11-06")
    stop, daily = events.unacked(con)
    assert (stop.cause, stop.stop_type, stop.detail) == ("crash", "agent", "crash: x")
    assert (daily.cause, daily.stop_type) == (None, None)
    rows = json.loads(events.line([stop, daily], 3.0))["events"]
    assert list(rows[0])[:4] == ["id", "kind", "cause", "type"]
    assert (rows[0]["cause"], rows[0]["type"]) == ("crash", "agent")
    assert (rows[1]["cause"], rows[1]["type"]) == (None, None)
```

Append to `tests/test_notice.py`:

```python
def test_a_stop_notice_emits_its_cause_and_type_and_keeps_the_detail(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    Notifier(con, FakeLinear(), lambda: 5.0).send(
        Notice(
            "NF-1:publish-failed",
            "Publishing failed 3 times in a row",
            issue_key="NF-1",
            event="ended",
            stop_type="transient",
        ),
        ISSUE,
    )
    [e] = events.unacked(con)
    assert (e.cause, e.stop_type) == ("publish-failed", "transient")
    assert e.detail == "publish-failed: Publishing failed 3 times in a row"


def test_a_notice_without_a_stop_type_emits_no_cause(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    Notifier(con, FakeLinear(), lambda: 5.0).send(
        Notice("NF-1:supervisor-error", "boom", issue_key="NF-1", event="service-error"), ISSUE
    )
    [e] = events.unacked(con)
    assert (e.cause, e.stop_type) == (None, None)
```

Append to `tests/test_canary.py`:

```python
def test_a_base_broken_event_is_typed_repo(tmp_path):
    c = con(tmp_path)
    canary.mark(c, "web", "abc123def456", 100.0, "boom", check="just check")
    [e] = events.unacked(c)
    assert (e.kind, e.cause, e.stop_type) == ("base-broken", "base-broken", "repo")
    assert e.detail == "`just check` fails at abc123def"
```

- [ ] **Step 2: Run them to verify they fail**

Run: `uv run pytest tests/test_ledger.py tests/test_events.py::test_a_stop_event_carries_its_cause_and_type_next_to_the_kind tests/test_notice.py tests/test_canary.py::test_a_base_broken_event_is_typed_repo -q`
Expected: FAIL. The column set lacks `cause` and `type`, `emit()` gets an unexpected keyword argument `cause`, and `Notice.__init__()` gets an unexpected keyword argument `stop_type`.

- [ ] **Step 3: Implement**

In `src/flocc/ledger.py`, append one entry to `_MIGRATIONS`, after `("ALTER TABLE events ADD COLUMN prints INTEGER NOT NULL DEFAULT 0",),`:

```python
    ("ALTER TABLE events ADD COLUMN cause TEXT", "ALTER TABLE events ADD COLUMN type TEXT"),
```

In `src/flocc/events.py`, replace `_COLUMNS`:

```python
_COLUMNS = "id, kind, issue_key, repo, detail, at, printed_at, acked_at, prints, cause, type"
```

add two fields at the end of `Event`, after `prints: int`:

```python
    # A stop's cause and its causes.TYPES entry; None for other events and older rows.
    cause: str | None = None
    stop_type: str | None = None
```

replace `emit`:

```python
def emit(
    con,
    kind: str,
    now: float,
    *,
    issue_key: str | None = None,
    repo: str | None = None,
    detail: str = "",
    cause: str | None = None,
    stop_type: str | None = None,
) -> int:
    if kind not in KINDS:
        raise ValueError(f"unknown event kind {kind!r}")
    with ledger.transaction(con):
        cur = con.execute(
            "INSERT INTO events (kind, issue_key, repo, detail, at, cause, type)"
            " VALUES (?, ?, ?, ?, ?, ?, ?)",
            (kind, issue_key, repo, detail[:DETAIL_CHARS], now, cause, stop_type),
        )
    return int(cur.lastrowid or 0)
```

and in `line`, add the two keys right after `"kind": e.kind,`:

```python
                    "cause": e.cause,
                    "type": e.stop_type,
```

In `src/flocc/notice.py`, add a field to `Notice` after `detail: str = ""`:

```python
    # The stop's causes.TYPES entry; set by supervisor._block only, which also makes the cause's
    # last segment the event's cause.
    stop_type: str | None = None
```

and replace the `events.emit(...)` call in `Notifier.send`:

```python
            events.emit(
                self.con,
                notice.event,
                self.now(),
                issue_key=notice.issue_key,
                detail=notice.detail or f"{cause}: {notice.summary}",
                cause=cause if notice.stop_type else None,
                stop_type=notice.stop_type,
            )
```

In `src/flocc/canary.py`, add `causes` to the import (`from flocc import causes, events, ledger`) and replace the emit in `mark`:

```python
        events.emit(
            con,
            "base-broken",
            now,
            repo=repo,
            detail=f"{what} fails at {sha[:9]}",
            cause="base-broken",
            stop_type=causes.type_of("base-broken"),
        )
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_ledger.py tests/test_events.py tests/test_notice.py tests/test_canary.py tests/test_status.py -q`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/ledger.py src/flocc/events.py src/flocc/notice.py src/flocc/canary.py tests/test_ledger.py tests/test_events.py tests/test_notice.py tests/test_canary.py
git commit -s -m "feat(events): stop cause and type columns on events and the JSON line"
```

---

### Task 3: `_block` types its stop; `check-cap`; the timeout's cause

**Files:**
- Modify: `src/flocc/supervisor.py` (imports; a new constant near `RESUME_NOTE`; `_block` at `:152-163`; `_publish_failed` at `:202-208`; the two check-failed `_block` calls in `_finish_phase` at `:581-588` and `:622-629`; `_timed_out` at `:819-821`)
- Test: `tests/test_supervisor.py`, `tests/test_causes.py`

**Interfaces:**
- Consumes: `causes.type_of`, `causes.OWNER` (Task 1); `Notice(stop_type=...)` and `events.emit(cause=..., stop_type=...)` (Task 2).
- Produces: `supervisor.TRANSIENT_RETRY = "transient_retry"` (the meta key prefix; Task 4 sets it). `_block` keeps its signature. Every notice it sends has `stop_type` set.

- [ ] **Step 1: Write the failing tests**

In `tests/test_supervisor.py`, change the import line `from flocc.supervisor import BLOCKED, RESUME_NOTE, LaunchError, supervise` to:

```python
from flocc.supervisor import (
    BLOCKED,
    FAILURE_HASH,
    PUBLISH_TRIES,
    RESUME_NOTE,
    LaunchError,
    _block,
    _digest,
    supervise,
)
```

Replace `test_a_used_fix_cap_is_a_cap_event`:

```python
def test_a_used_fix_cap_is_a_cap_event_typed_owner(rt):
    add(rt, phase="fix", fix_attempts=rt.cfg.check_fix_rounds)
    rt.sessions.alive_names.add("NF-1")
    rt.publish_fn = lambda claim, title, summary, **kw: PublishResult("check_failed", "E boom")
    done(rt, "fixed")
    supervise(rt, current(rt))
    sent = rt.notifier.sent[-1]
    assert current(rt).state == "blocked" and sent.event == "cap"
    assert (sent.cause, sent.stop_type) == ("NF-1:check-cap", "owner")
    assert (state_dir(rt.cfg, "NF-1") / BLOCKED).read_text().startswith("check-cap\n")
```

Add after it:

```python
def test_the_same_failure_at_the_last_round_is_check_cap(rt):
    add(rt, phase="fix", fix_attempts=rt.cfg.check_fix_rounds)
    rt.sessions.alive_names.add("NF-1")
    (state_dir(rt.cfg, "NF-1") / FAILURE_HASH).write_text(_digest("E1 boom"))
    rt.publish_fn = lambda claim, title, summary, **kw: PublishResult("check_failed", "E1 boom")
    done(rt, "fixed")
    supervise(rt, current(rt))
    sent = rt.notifier.sent[-1]
    assert "the same way twice" in sent.summary
    assert (sent.cause, sent.stop_type) == ("NF-1:check-cap", "owner")


def test_an_unknown_stop_cause_raises_before_the_kill(rt):
    add(rt, phase="impl")
    rt.sessions.alive_names.add("NF-1")
    with pytest.raises(ValueError, match="unknown stop cause"):
        _block(rt, current(rt), "crashed", "summary", "action")
    assert rt.sessions.killed == [] and current(rt).state == "running"
    assert rt.notifier.sent == [] and not (state_dir(rt.cfg, "NF-1") / BLOCKED).exists()


def test_a_publish_failure_after_the_service_s_retry_is_the_owner_s(rt):
    def rejected(c, t, s):
        raise subprocess.CalledProcessError(128, ["git", "push"], stderr="fatal: denied")

    rt.publish_fn = rejected
    add(rt, phase="impl", tester_sha="sha-tests")
    ledger.meta_set(rt.con, "transient_retry:NF-1", "1")
    ledger.meta_set(rt.con, "publish_failures:NF-1", str(PUBLISH_TRIES - 1))
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).state == "blocked"
    [esc] = rt.notifier.sent
    assert (esc.cause, esc.stop_type) == ("NF-1:publish-failed", "owner")
    assert "tries once more" not in esc.action
```

In `test_the_same_check_failure_twice_blocks_without_another_round`, append (the block there is at `fix_attempts == 1`, under the default 2 rounds):

```python
    assert (rt.notifier.sent[-1].cause, rt.notifier.sent[-1].stop_type) == (
        "NF-1:check-failed",
        "agent",
    )
```

In `test_publish_that_keeps_failing_blocks`, after `assert esc.cause == "NF-1:publish-failed" and "Authentication failed" in esc.summary`, add:

```python
    assert esc.stop_type == "transient" and "tries once more in 30 minutes" in esc.action
```

In `test_a_timed_out_run_is_an_ended_event_and_a_comment`, after `assert e.kind == "ended" and e.detail.startswith("timed out")`, add:

```python
    assert (e.cause, e.stop_type) == ("timed-out", "agent")
```

In `test_a_block_is_an_ended_event_and_writes_why`, after the first assert, add:

```python
    assert rt.notifier.sent[0].stop_type == "owner"
```

Append to `tests/test_causes.py`:

```python
import ast
from pathlib import Path

from flocc import supervisor


def _block_causes() -> set[str]:
    """Every string literal in the cause argument of a _block call in supervisor.py."""
    tree = ast.parse(Path(supervisor.__file__).read_text())
    found = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Call) and getattr(node.func, "id", None) == "_block":
            found |= {
                n.value
                for n in ast.walk(node.args[2])
                if isinstance(n, ast.Constant) and isinstance(n.value, str)
            }
    return found


def test_every_block_cause_in_the_source_is_in_the_table():
    # timed-out is emitted by _timed_out and base-broken by canary.mark, not through _block.
    assert _block_causes() == set(causes.CAUSES) - {"timed-out", "base-broken"}
```

Move the new imports (`ast`, `Path`, `supervisor`) to the top of the file with the existing ones, so the file's imports read:

```python
import ast
from pathlib import Path

import pytest

from flocc import causes, control, supervisor
```

- [ ] **Step 2: Run them to verify they fail**

Run: `uv run pytest tests/test_supervisor.py tests/test_causes.py -q`
Expected: FAIL. `stop_type` is `None` on every notice, `test_an_unknown_stop_cause_raises_before_the_kill` sees no exception, and `_block_causes()` lacks `check-cap`.

- [ ] **Step 3: Implement**

In `src/flocc/supervisor.py`, add to the imports after `from flocc.agentblock import settings_from_json`:

```python
from flocc.causes import OWNER, type_of
```

Add after the `RESUME_NOTE = "resume-note.md"` line and its comment:

```python
# Meta key: the service has already retried this claim's publish-failed block once.
TRANSIENT_RETRY = "transient_retry"
```

Replace `_block`:

```python
def _block(
    rt, claim: Claim, cause: str, summary: str, action: str, kind: str = "ended", detail: str = ""
) -> None:
    key = claim.issue_key
    # Before the kill: an unknown cause leaves the session and the claim as they were.
    stop_type = type_of(cause)
    if cause == "publish-failed" and ledger.meta_get(rt.con, f"{TRANSIENT_RETRY}:{key}"):
        stop_type = OWNER
    rt.sessions.kill(session_name(key, claim.phase))
    ledger.meta_set(rt.con, f"{CHECK_RUNNING}:{key}", "")
    notice = Notice(
        f"{key}:{cause}",
        summary,
        action,
        issue_key=key,
        event=kind,
        detail=detail,
        stop_type=stop_type,
    )
    d = state_dir(rt.cfg, key)
    d.mkdir(parents=True, exist_ok=True)
    (d / BLOCKED).write_text(f"{cause}\n\n{notice.text()}\n")
    rt.notifier.send(notice, rt.linear.issue(key))
    ledger.update_claim(rt.con, key, state="blocked")
```

In `_publish_failed`, replace the final `_block(...)` call (the `"publish-failed"` one) with:

```python
    action = f"Fix the push or the gh token, then run flocc release --keep-worktree {key}"
    if not ledger.meta_get(rt.con, f"{TRANSIENT_RETRY}:{key}"):
        action = f"The service tries once more in 30 minutes. If that fails too: {action}"
    _block(
        rt,
        claim,
        "publish-failed",
        f"Publishing failed {fails} times in a row: {exc}\n{detail[-1500:]}".strip(),
        action,
    )
```

(The `ledger.meta_set(rt.con, counter, "0")` line after it stays.)

In `_finish_phase`, in the same-failure branch (the `_block` whose summary starts `"The check failed the same way twice; ..."`), replace the cause argument `"check-failed",` with:

```python
            "check-cap" if claim.fix_attempts >= rt.cfg.check_fix_rounds else "check-failed",
```

In the round-cap branch (the `_block` whose summary is `f"The check still fails after {rounds}:\n{result.detail}"` and that passes `kind="cap"`), replace `"check-failed",` with:

```python
            "check-cap",
```

In `_timed_out`, replace the `events.emit(...)` call:

```python
    events.emit(
        rt.con,
        "ended",
        rt.now(),
        issue_key=key,
        repo=claim.repo,
        detail=f"timed out: {line}",
        cause="timed-out",
        stop_type=type_of("timed-out"),
    )
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_supervisor.py tests/test_causes.py tests/test_control.py tests/test_rebase.py -q`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/supervisor.py tests/test_supervisor.py tests/test_causes.py
git commit -s -m "feat(supervisor): type every stop, block at the last round as check-cap"
```

---

### Task 4: The service retries a `publish-failed` block once

**Files:**
- Modify: `src/flocc/poller.py` (imports; a new constant beside `RERUN_COMMENTS_S`; a new function `retry_transient` after `restart_timed_out`; the meta clear list in `_start` at `:386-395`)
- Modify: `src/flocc/tick.py:16-22` (import) and `:215` (call)
- Test: `tests/test_poller.py`

**Interfaces:**
- Consumes: `supervisor.TRANSIENT_RETRY`, `supervisor.PUBLISH_TRIES`, `supervisor.BLOCKED` (Task 3 and existing); `control.stop_cause(rt, claim) -> str`; `poller.still_wanted(rt, key) -> bool`; `agentfs.read_text(directory, name) -> str | None`.
- Produces: `poller.TRANSIENT_RETRY_S = 30 * 60`, `poller.retry_transient(rt) -> None`.

- [ ] **Step 1: Write the failing tests**

In `tests/test_poller.py`, add `import os` to the standard-library imports at the top, add `retry_transient` to the `from flocc.poller import (...)` list, and change `from flocc.supervisor import LaunchError, supervise` to:

```python
from flocc.supervisor import BLOCKED, PUBLISH_TRIES, LaunchError, supervise
```

Also add `TRANSIENT_RETRY_S` to the `from flocc.poller import (...)` list.

In `test_a_fresh_claim_forgets_the_last_run_s_rebase_and_base_hold`, add `"transient_retry",` to `kept_keys` (Task 5 removes `"cardinal_fix"` from the same tuple):

```python
    kept_keys = (
        "rebase",
        "rebase_tried",
        "base_held",
        "base_released",
        "cardinal_fix",
        "cardinal_resume",
        "transient_retry",
    )
```

Add after `test_a_blocked_claim_is_not_restarted`:

```python
NOW = 100_000.0


def claim_of(rt, key="NF-5"):
    c = ledger.get_claim(rt.con, key)
    assert c is not None
    return c


def publish_blocked(rt, key="NF-5", age=TRANSIENT_RETRY_S, cause="publish-failed", done=True):
    rt.now = lambda: NOW
    finished(rt, key, state="blocked")
    ledger.update_claim(rt.con, key, phase="impl", started_at=NOW - 7200)
    d = state_dir(rt.cfg, key)
    d.mkdir(parents=True, exist_ok=True)
    (d / BLOCKED).write_text(f"{cause}\n\nPublishing failed 3 times in a row\n")
    os.utime(d / BLOCKED, (NOW - age, NOW - age))
    if done:
        (d / "done.json").write_text(json.dumps({"summary": "Did it.", "at": NOW - 3600}))
    rt.linear.state_types = lambda keys: {k: "started" for k in keys}
    return d


def test_a_publish_failure_is_retried_once_after_thirty_minutes(rt):
    d = publish_blocked(rt)
    retry_transient(rt)
    c = claim_of(rt)
    assert (c.state, c.phase, c.started_at, c.restarts) == ("running", "impl", NOW - 7200, 0)
    assert ledger.meta_get(rt.con, "publish_failures:NF-5") == str(PUBLISH_TRIES - 1)
    assert ledger.meta_get(rt.con, "transient_retry:NF-5") == "1"
    assert not (d / BLOCKED).exists()
    [(_, text)] = rt.linear.comments
    assert "trying once more" in text
    # Blocked again: no second retry.
    ledger.update_claim(rt.con, "NF-5", state="blocked")
    (d / BLOCKED).write_text("publish-failed\n\nagain\n")
    os.utime(d / BLOCKED, (NOW - TRANSIENT_RETRY_S, NOW - TRANSIENT_RETRY_S))
    retry_transient(rt)
    assert claim_of(rt).state == "blocked"


def test_the_retry_makes_one_more_try_then_the_stop_is_the_owner_s(rt):
    publish_blocked(rt)
    retry_transient(rt)

    def rejected(claim, title, summary):
        raise subprocess.CalledProcessError(128, ["git", "push"], stderr="fatal: denied")

    rt.publish_fn = rejected
    supervise(rt, claim_of(rt))
    assert claim_of(rt).state == "blocked"
    assert rt.notifier.sent[-1].stop_type == "owner"


def test_a_publish_failure_is_not_retried_before_thirty_minutes(rt):
    publish_blocked(rt, age=TRANSIENT_RETRY_S - 60)
    retry_transient(rt)
    assert claim_of(rt).state == "blocked"
    assert not ledger.meta_get(rt.con, "transient_retry:NF-5") and rt.linear.comments == []


@pytest.mark.parametrize("why", ["held", "parked", "unlabelled", "other-cause", "no-done"])
def test_no_retry_for_a_held_unwanted_or_other_stop(rt, why):
    publish_blocked(
        rt,
        cause="branch-moved" if why == "other-cause" else "publish-failed",
        done=why != "no-done",
    )
    if why == "held":
        ledger.add_hold(rt.con, "NF-5", "owner said wait", 1.0)
    if why == "parked":
        rt.linear.state_types = lambda keys: {k: "unstarted" for k in keys}
    if why == "unlabelled":
        rt.linear.issue = lambda key: issue(key, labels=("backend",))
    retry_transient(rt)
    assert claim_of(rt).state == "blocked"
    assert not ledger.meta_get(rt.con, "transient_retry:NF-5")
```

- [ ] **Step 2: Run them to verify they fail**

Run: `uv run pytest tests/test_poller.py -q -k "retr or fresh_claim_forgets"`
Expected: FAIL with `ImportError: cannot import name 'retry_transient' from 'flocc.poller'`.

- [ ] **Step 3: Implement**

In `src/flocc/poller.py`:

Change `from flocc import canary, events, host, ledger` to `from flocc import agentfs, canary, events, host, ledger`. Change `from flocc.context import state_dir, write_context` to `from flocc.context import agent_dir, state_dir, write_context`. Add after `from flocc.agentblock import (...)`:

```python
from flocc.control import stop_cause
```

Change `from flocc.supervisor import BLOCKED, LAST_SUMMARY, QUESTIONS, RESUME_NOTE, launch` to:

```python
from flocc.supervisor import (
    BLOCKED,
    LAST_SUMMARY,
    PUBLISH_TRIES,
    QUESTIONS,
    RESUME_NOTE,
    TRANSIENT_RETRY,
    launch,
)
```

Add after `RERUN_COMMENTS_S = 7 * 24 * 3600`:

```python
TRANSIENT_RETRY_S = 30 * 60
```

Add after `restart_timed_out`:

```python
def retry_transient(rt) -> None:
    """Sets a claim blocked on publish-failed running again, once, TRANSIENT_RETRY_S after the
    block: the next tick finds its done.json and publishes again, check included."""
    holds = ledger.issue_holds(rt.con)
    for claim in ledger.live_claims(rt.con):
        key = claim.issue_key
        once = f"{TRANSIENT_RETRY}:{key}"
        if claim.state != "blocked" or key in holds or ledger.meta_get(rt.con, once):
            continue
        if stop_cause(rt, claim) != "publish-failed":
            continue
        blocked = state_dir(rt.cfg, key) / BLOCKED
        try:
            if rt.now() - blocked.stat().st_mtime < TRANSIENT_RETRY_S:
                continue
            # Without the session's done.json the running claim would read as a dead session,
            # and supervise would relaunch it.
            if agentfs.read_text(agent_dir(rt.cfg, key), "done.json") is None:
                continue
            if not still_wanted(rt, key):
                continue
        except (OSError, LinearError, KeyError) as exc:
            print(f"retry: {key} not checked: {exc}", file=sys.stderr)
            continue
        # Recorded first, like the timeout restart: a tick that dies below must not retry again.
        ledger.meta_set(rt.con, once, "1")
        # One try left, not PUBLISH_TRIES: each try runs the repo's full check again.
        ledger.meta_set(rt.con, f"publish_failures:{key}", str(PUBLISH_TRIES - 1))
        # The state alone: a new started_at would make the session's done.json read as stale.
        ledger.update_claim(rt.con, key, state="running")
        blocked.unlink(missing_ok=True)
        try:
            rt.linear.comment(
                claim.issue_id, "publishing failed earlier; trying once more, check included"
            )
        except LinearError as exc:
            print(f"retry: {key} not commented: {exc}", file=sys.stderr)
```

In `_start`, replace the tuple of kept meta keys:

```python
    for kept in (
        "rebase",
        "rebase_tried",
        "base_held",
        "base_released",
        "cardinal_fix",
        "cardinal_resume",
        "host_wait",
        TRANSIENT_RETRY,
    ):
```

In `src/flocc/tick.py`, add `retry_transient,` to the `from flocc.poller import (...)` list (after `restart_timed_out,`), and call it right after `restart_timed_out(rt)`:

```python
        restart_timed_out(rt)
        retry_transient(rt)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_poller.py tests/test_rebase.py -q`
Expected: PASS. (`tests/test_poller.py` also runs `tick`, which now calls `retry_transient`.)

- [ ] **Step 5: Commit**

```bash
git add src/flocc/poller.py src/flocc/tick.py tests/test_poller.py
git commit -s -m "feat(poller): retry a publish-failed block once after 30 minutes"
```

---

### Task 5: Remove the `fix` alias and the `cardinal_fix` cap key

**Files:**
- Modify: `src/flocc/cardinalgate.py` (the `resume` case in `argv_for`)
- Modify: `src/flocc/control.py:17-20` (`_CAPS` and its comment), `:111` (the cap check in `resume`)
- Modify: `src/flocc/poller.py` (`"cardinal_fix"` in `_start`'s kept list)
- Modify: `src/flocc/ledger.py` (`_MIGRATIONS`: append one entry)
- Test: `tests/test_cardinalgate.py`, `tests/test_control.py`, `tests/test_poller.py`, `tests/test_ledger.py`

**Interfaces:**
- Consumes: `_ledger_at(path, version)` in `tests/test_ledger.py` (Task 2).
- Produces: the ledger at version 7. `control._CAPS` no longer exists; `resume` checks `cardinal_resume:KEY` only.

- [ ] **Step 1: Write the failing tests**

In `tests/test_cardinalgate.py`, replace `test_a_note_with_shell_text_stays_one_argument` and its parametrize decorator:

```python
def test_a_note_with_shell_text_stays_one_argument():
    note = "fix $(rm -rf ~); it's `x`\n-- and --config=/etc/x"
    quoted = "'" + note.replace("'", "'\\''") + "'"
    assert argv_for(f"resume NF-3 --note {quoted}") == ["resume", "NF-3", f"--note={note}"]
```

and add this entry to the refused-lines parametrize list that follows it, after `"run-summary NF-12 NF-13",`:

```python
        "fix NF-3 --note 'the old alias'",
```

In `tests/test_control.py`, replace `test_a_run_the_old_fix_verb_marked_is_still_capped`:

```python
def test_the_old_fix_key_no_longer_caps(rt, tmp_path):
    blocked(rt, tmp_path)
    ledger.meta_set(rt.con, "cardinal_fix:NF-1", "1")
    assert resume(rt, "NF-1", "note").state == "running"
```

In `tests/test_poller.py`, remove `"cardinal_fix",` from `kept_keys` in `test_a_fresh_claim_forgets_the_last_run_s_rebase_and_base_hold`.

In `tests/test_ledger.py`, add after `test_events_written_before_the_stop_columns_read_null`:

```python
def test_cardinal_fix_marks_become_resume_marks(tmp_path):
    path = tmp_path / "old.sqlite"
    raw = _ledger_at(path, 6)
    raw.executemany(
        "INSERT INTO meta (key, value) VALUES (?, ?)",
        [
            ("cardinal_fix:NF-1", "1"),
            ("cardinal_fix:NF-2", ""),
            ("cardinal_fix:NF-3", "1"),
            ("cardinal_resume:NF-3", ""),
            ("cardinal_resume:NF-4", "1"),
            ("cardinal_fixed:NF-5", "x"),
        ],
    )
    raw.commit()
    raw.close()
    con = ledger.connect(path)
    keys = ("NF-1", "NF-2", "NF-3", "NF-4")
    got = {k: ledger.meta_get(con, f"cardinal_resume:{k}") for k in keys}
    assert got == {"NF-1": "1", "NF-2": None, "NF-3": "1", "NF-4": "1"}
    left = con.execute("SELECT key FROM meta WHERE key GLOB 'cardinal_fix:*'").fetchall()
    assert left == [] and ledger.meta_get(con, "cardinal_fixed:NF-5") == "x"
```

- [ ] **Step 2: Run them to verify they fail**

Run: `uv run pytest tests/test_cardinalgate.py tests/test_control.py::test_the_old_fix_key_no_longer_caps tests/test_poller.py::test_a_fresh_claim_forgets_the_last_run_s_rebase_and_base_hold tests/test_ledger.py::test_cardinal_fix_marks_become_resume_marks -q`
Expected: FAIL. The gate still maps `fix`, `resume` refuses with "already resumed", and the migration test finds `cardinal_fix:` keys left. The `_start` test passes already; it guards the next step.

- [ ] **Step 3: Implement**

In `src/flocc/cardinalgate.py`, replace:

```python
        case (("resume" | "fix"), [key, "--note", note]):
            # fix is the verb resume replaced; cardinal's VM and the executor deploy separately.
            return ["resume", _key(key), f"--note={_text(note, 'note')}"]
```

with:

```python
        case "resume", [key, "--note", note]:
            return [verb, _key(key), f"--note={_text(note, 'note')}"]
```

In `src/flocc/control.py`, replace:

```python
# One cardinal resume per claim; poller._start clears both keys with the claim's other per-run
# meta. cardinal_fix is what the fix verb set before resume replaced it.
CARDINAL_RESUME = "cardinal_resume"
_CAPS = (CARDINAL_RESUME, "cardinal_fix")
```

with:

```python
# One cardinal resume per claim; poller._start clears it with the claim's other per-run meta.
CARDINAL_RESUME = "cardinal_resume"
```

and in `resume`, replace `if any(ledger.meta_get(rt.con, f"{k}:{key}") for k in _CAPS):` with:

```python
    if ledger.meta_get(rt.con, f"{CARDINAL_RESUME}:{key}"):
```

In `src/flocc/poller.py`, remove `"cardinal_fix",` from the kept tuple in `_start`.

In `src/flocc/ledger.py`, append one entry to `_MIGRATIONS`, after the `events` cause/type entry from Task 2:

```python
    # The fix verb's cap key, folded into resume's: an empty resume value is one _start cleared.
    (
        """INSERT INTO meta (key, value)
SELECT 'cardinal_resume:' || substr(key, 14), value FROM meta
WHERE key GLOB 'cardinal_fix:*' AND value != ''
ON CONFLICT(key) DO UPDATE SET value = excluded.value WHERE meta.value = ''""",
        "DELETE FROM meta WHERE key GLOB 'cardinal_fix:*'",
    ),
```

(`substr(key, 14)` skips the 13 characters of `cardinal_fix:`. `GLOB` is used because `LIKE` reads `_` as a wildcard. The `WHERE` on the `SELECT` is required: without one, SQLite cannot parse `ON CONFLICT` after `INSERT ... SELECT`.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_cardinalgate.py tests/test_control.py tests/test_poller.py tests/test_ledger.py -q`
Expected: PASS. Then confirm nothing else names the old key or verb:

Run: `git grep -n "cardinal_fix\|\"fix\"" -- src tests`
Expected: matches only in `tests/test_ledger.py` (the migration test) and `src/flocc/ledger.py` (the migration), plus `phase="fix"`-style phase names, which are unrelated.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/cardinalgate.py src/flocc/control.py src/flocc/poller.py src/flocc/ledger.py tests/test_cardinalgate.py tests/test_control.py tests/test_poller.py tests/test_ledger.py
git commit -s -m "feat(cardinal): drop the fix alias, fold cardinal_fix into resume"
```

- [ ] **Step 6: Host check (owner or controller only; the implementer does not run it)**

Before the merge, read each host's ledger for `cardinal_fix:` keys, read-only. For `flocc-nimble`, and the same for every other host the desktop serves:

```bash
flocc -H flocc-nimble config get ledger_path
ssh root@flocc-nimble sudo -u flocc python3 - <<'EOF'
import os, sqlite3
path = os.path.expanduser("~flocc/.local/state/flocc/ledger.sqlite")  # the path printed above
con = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
for row in con.execute(
    "SELECT key, value FROM meta"
    " WHERE key GLOB 'cardinal_fix:*' OR key GLOB 'cardinal_resume:*' ORDER BY key"
):
    print(*row)
EOF
```

Expected before deploy: any `cardinal_fix:KEY 1` row is a claim whose resume cap the migration carries over. After deploy and one tick, the same query prints no `cardinal_fix:` row, and each of those keys has `cardinal_resume:KEY 1`.

---

### Task 6: Triage decides from the type

**Files:**
- Modify: `deploy/cardinal/plugin/skills/triage/SKILL.md` (lines 6-10, and 22-48)
- Test: `tests/test_cardinal_plugin.py` (`test_triage_resumes_and_answers_with_the_resume_verb`, and a new test)

**Interfaces:**
- Consumes: the event JSON keys `"cause"` and `"type"` (Task 2), the types from Task 1, the retry from Task 4.

- [ ] **Step 1: Write the failing tests**

In `tests/test_cardinal_plugin.py`, replace `test_triage_resumes_and_answers_with_the_resume_verb`:

```python
def test_triage_resumes_and_answers_with_the_resume_verb():
    text = (PLUGIN / "skills" / "triage" / "SKILL.md").read_text()
    assert f"{EXECUTOR} resume KEY --note" in text and f"{EXECUTOR} fix" not in text
    assert "`needs-owner`" in text and "acceptance criteria" in text


def test_triage_decides_on_the_stop_type_and_on_the_kind_when_it_is_null():
    text = " ".join((PLUGIN / "skills" / "triage" / "SKILL.md").read_text().split())
    assert "`cause`, `type`" in text
    for stop_type in ("agent", "owner", "transient", "repo"):
        assert f"`type` `{stop_type}`" in text
    assert "`type` null: decide by `kind`" in text
    # The type replaces the prose list of causes.
    owner_stops = ("tests-touched", "branch-moved", "publish-failed", "rebase-conflict", "no-tests")
    for cause in owner_stops:
        assert cause not in text
    assert f"{EXECUTOR} amend-tests KEY" in text and "`tests-amended`" in text
```

- [ ] **Step 2: Run them to verify they fail**

Run: `uv run pytest tests/test_cardinal_plugin.py -q -k triage`
Expected: FAIL on `assert "`cause`, `type`" in text`.

- [ ] **Step 3: Implement**

In `deploy/cardinal/plugin/skills/triage/SKILL.md`, replace the first paragraph (lines 6-10, from `A line `{"events": [...]}`` to `again and do nothing else.`) with:

```markdown
A line `{"events": [...]}` lists events the executor VM holds until you acknowledge them. Each
event has `id`, `kind`, `cause`, `type`, `issue`, `repo`, `detail` and `age_s`. A stopped run
carries the stop's `cause` and its `type`; every other event has null in both. Handle every event
in the line, then acknowledge all of them with one call:
`/opt/cardinal/plugin/bin/executor ack ID ID ...`. An event you already handled may come again
after 30 minutes or a reconnect; acknowledge it again and do nothing else.
```

Replace the bullets from `- `ended`, detail starting `done`` through the `- `base-broken`` bullet (lines 22-48) with:

```markdown
A stopped run: decide by its `type`, and read the run summary for what happened.

- `type` `agent`: resume when you can say what the next attempt does differently:
  `/opt/cardinal/plugin/bin/executor resume KEY --note '<what to do and where, one paragraph>'`.
  You get one resume per run. When you cannot say what would change, tell the owner in at most
  three lines: the issue, why it stopped, the one thing they need to do.
- `type` `owner`: tell the owner in at most three lines: the issue, why it stopped, the one thing
  they need to do. For `kind` `cap`, the check still fails after every fix round: say what fails,
  from the run summary, and start no fixer. Two exceptions:
  - `kind` `needs-owner`: an agent stopped with questions, and the run summary lists them. Answer
    only when the issue description, or a spec it names, settles every question and the answer
    stays inside the acceptance criteria: `/opt/cardinal/plugin/bin/executor resume KEY --note
    '<the answers, one per question, with where each comes from>'`. A question about scope, the
    criteria, credentials, cost or a system outside the repo is the owner's: tell them in at most
    three lines, quoting it.
  - The PR needing the amended-tests label (`tests-amended`): don't ask the owner. Run
    `/opt/cardinal/plugin/bin/executor amend-tests KEY`, then resume with a note that the label is
    on the PR and it should finish with `flocc done`. If resume refuses, tell the owner in one
    line that the label is on and `flocc release KEY` reruns it.
- `type` `transient`: do nothing. The service tries once more 30 minutes after the stop, and a
  second failure comes back typed `owner`.
- `type` `repo` (`kind` `base-broken`): tell the owner the repo and commit whose check fails; that
  repo's issues wait.

Any other event has `type` null: decide by `kind`.

- `ended`, detail starting `done`: tell the owner in one line: the issue, the PR link, and the
  review's verdict if the summary has one. When the detail says `nothing to do`, give its reason
  instead of a PR link.
- `ended` blocked or timed out, `needs-owner`, `cap` or `base-broken` with `type` null: the
  service sent it before it typed its stops. Handle `needs-owner` and `cap` as under `owner`,
  `base-broken` as under `repo`, and a blocked or timed-out `ended` as under `agent`. `resume`
  refuses a stop that is the owner's; then tell the owner.
- A running session asks for the amended-tests label (it reaches you only through the owner):
  run `/opt/cardinal/plugin/bin/executor amend-tests KEY`; the verb tells the session.
```

The bullets from `- `bad-sequence`` to the end of the file stay as they are.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_cardinal_plugin.py -q`
Expected: PASS, including `test_the_triage_skill_covers_every_event_kind_and_the_link`.

- [ ] **Step 5: Commit**

```bash
git add deploy/cardinal/plugin/skills/triage/SKILL.md tests/test_cardinal_plugin.py
git commit -s -m "feat(cardinal): triage decides a stop from its type"
```

---

### Task 7: The book, the changelog, and the full check

**Files:**
- Modify: `docs/book/cardinal.md:36-42` (the events paragraph), `:48` (the gate's verb list)
- Modify: `docs/book/commands.md:49` (`resume` row), `:66` (`events` row)
- Modify: `docs/book/architecture.md:72-83` (the Failures table and the paragraph after it)
- Modify: `CHANGELOG.md` (Unreleased)

- [ ] **Step 1: Edit the book**

In `docs/book/cardinal.md`, after the sentence ending `Routine starts, fixers under the cap, reviews and rebases emit nothing.` (line 42), add on the next line of the same paragraph:

```markdown
A stopped run's event also carries the stop's `cause` and its `type`: `agent` (cardinal may
resume it), `owner`, `transient` (the service retries it once) or `repo` (the repo's base is
broken). Other events carry null in both.
```

and replace `` `resume` (and `fix`, an alias of `resume` for one release), `hold`, `release`, `cleanup`, `` (line 48) with:

```markdown
`resume`, `hold`, `release`, `cleanup`,
```

In `docs/book/commands.md`, replace the `resume` row (line 49) with:

```markdown
| `resume <key> --note TEXT` | Restart a blocked or timed-out run once, on its branch, with the note: the answer to a `needs-owner` stop, or direction after a stop typed `agent` (`check-failed` with rounds left, `no-changes`, `idle`, `crash` or a timeout). Refused for other stops |
```

and replace the `events` row (line 66) with:

```markdown
| `events [--follow] [--hello]` | Print unacknowledged events as JSON lines of at most 50. A stopped run's event carries its `cause` and `type`; other events carry null. `--follow` keeps printing: a batch closes 2 minutes after its first event, and an unacknowledged event is printed again every 30 minutes, at most 3 times per connection, and on every new connection |
```

In `docs/book/architecture.md`, in the Failures table, replace the `The check fails` row's last sentence `After the last one fails, the claim blocks and the service comments on the issue` with `After the last one fails, the claim blocks as `check-cap` and the service comments on the issue`, and add a row after it:

```markdown
| Publishing fails three ticks in a row | The claim blocks as `publish-failed`. 30 minutes later the service sets it running once more, for one more check and push. A second failure is yours |
```

Replace the paragraph after the table (`A blocked or timed-out issue keeps its claim until you release it. ...`) with:

```markdown
Every stop has a cause, and each cause has a type that says who acts: `transient` (the service retries), `agent` (cardinal may resume), `owner` or `repo` (the base is broken). The table is `src/flocc/causes.py`. A blocked or timed-out issue keeps its claim until you release it. Release moves it back to Todo, and the next tick continues on the kept branch.
```

- [ ] **Step 2: Edit the changelog**

In `CHANGELOG.md`, in the `resume` bullet under Unreleased, delete the sentence `` `fix` still works as an alias for one release. `` (the bullet now ends `... and how many ended done.`). Then add a bullet right after that one:

```markdown
- Every stop has a cause and a type. cardinal's events carry both as `cause` and `type`
  (`agent`, `owner`, `transient` or `repo`; null for events that are not stops), and triage
  decides from the type. A check that still fails at the last fix round blocks as `check-cap`. A
  `publish-failed` block is retried once by the service 30 minutes later, check included; a
  second failure is the owner's. The gate no longer accepts `fix`; use `resume`.
```

- [ ] **Step 3: Run the full check**

Run: `PATH="$(dirname $(uvx --from shellcheck-py which shellcheck)):$PATH" just check`
Expected: lint and the whole test suite pass. Fix any failure in the task that owns the code, and rerun.

- [ ] **Step 4: Commit**

```bash
git add docs/book/cardinal.md docs/book/commands.md docs/book/architecture.md CHANGELOG.md
git commit -s -m "docs: typed stops, the publish retry, no fix alias"
```
