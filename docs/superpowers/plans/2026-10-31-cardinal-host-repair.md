# Cardinal Host Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** flocc notices when the executor host runs short of `/tmp` or memory or the kernel kills a process, wakes cardinal with a `host-pressure` event, starts and relaunches nothing until the host recovers, and gives cardinal fixed repair verbs plus one owner-approved shell for anything they miss.

**Architecture:** A new `flocc.host` module measures `/tmp`, `MemAvailable` and the OOM counter on each tick before `supervise`, keeps one episode in `meta`, emits `host-pressure` once per break and sends each running session a recovery message when the episode closes. Admission refuses starts during an episode, and `supervise` defers a finished step (before its publish runs the check) and a dead session's relaunch without spending a fix round or a restart. Cardinal gets four gate verbs (`host`, `cleanup --now`, `stop-tests KEY`, `shell CMD`) backed by new `flocc-agent-run` subcommands, a separate `bin/agent-shell` that the managed settings make ask the owner, and a managed `only-executor` PreToolUse hook that keeps `default` mode from running anything else on cardinal's VM.

**Tech Stack:** Python 3.13 stdlib (`os.statvfs`, `/proc`, `subprocess`, `signal`, sqlite3 `meta`), pytest, ruff, pyrefly; POSIX sh and stdlib Python 3 on the cardinal VM; Go installer (`deploy` embed, `steps.AgentAccess`, `create.CardinalBase`); tmux, rootless podman; Claude Code managed settings and Remote Control.

**Spec:** `docs/superpowers/specs/2026-10-31-cardinal-host-repair-design.md`

## Global Constraints

- Work on the branch the executor gives you, in a worktree under the repo's worktrees directory on disk, never under `/tmp`.
- Commits use `git commit -s`, Conventional Commits subjects, and no `Co-Authored-By` trailer or other AI attribution.
- The full check is `just check`. shellcheck may be missing: run it as `uvx --from shellcheck-py shellcheck`. Run the tests a task touches while iterating and `just check` once, in Task 10.
- Comments only for facts the code can't show. Match the file's comment density. No comment names this plan, a task or a ticket.
- Reuse existing helpers; grep before writing a new one (`agentrun._older`, `_KEY`, `_scratch_root`, `container_env`, `under_home`, `session_name`, `ledger.meta_get/meta_set`, `events.emit`, `summary._capped`, `cli._config`, `cli._issue_key`).
- Read files with the Read tool and change them with Edit or Write.
- `/tmp` opens an episode above `tmp_max_pct` (default 80) and closes it below 0.9 × `tmp_max_pct`.
- Memory opens an episode when `MemAvailable` is below `min_available_mem_gb` (default 1.0) and closes it above 1.1 × `min_available_mem_gb`. Memory is compared in GiB (2**30 bytes), the unit the event detail prints.
- An OOM kill opens an episode when `oom_kill` in `/proc/vmstat` rose since the last tick, and the episode can close only on a tick with no new kill. The last value lives in `meta`. The first reading only sets the baseline; a value lower than the stored one means a reboot and resets it. Neither emits.
- Both thresholds are `Config` fields next to `min_free_disk_gb`.
- The event kind is `host-pressure`, a member of `events.KINDS`. Its detail names each value over its limit, for example `/tmp 93% (2.7/2.9 GiB); MemAvailable 0.6 GiB; 2 OOM kills`. One event per break, as `canary.mark` does, plus one more for each new OOM kill during an open episode.
- Host pressure wakes cardinal within about five minutes: one 3-minute tick plus up to `events.BATCH_S` (120 s).
- During an episode `admission.admit` refuses new starts with the reason in the decision, and `supervise` launches nothing: no fix round, review, crash relaunch or next step. A deferred step keeps its fix rounds and restarts.
- When the episode closes, each running session gets one message through `send`: `The host has recovered; you may run the test suite again.`
- `stop-tests` sends: `flocc stopped your test run because the host is under memory or /tmp pressure. Do not rerun the suite until flocc says the host has recovered.`
- `cleanup --now`: scratchpad age 10 minutes (600 s) instead of `SCRATCH_AGE_S` (a day); `max_age_s` stays 7 days; a scratchpad a kept claim lists is kept; pytest runs of any age go. The daily pass removes only pytest runs older than a day. Both skip symlinks such as `pytest-current` and any run whose `.lock` names a live pid. The result lists what was removed and the bytes freed. `--now` waits up to 60 s for the tick lock and reports that it is busy if it can't get it.
- `shell`: the command goes to `bash -c` as one argument, runs as `flocc-agent` with a 120-second timeout, output capped at 8,000 characters, each call recorded in `meta` with its time, command and exit status, and `digest-data` lists the day's calls. The gate caps the command at `NOTE_CHARS` (2000).
- `bin/executor` refuses `shell` as its first argument before ssh runs. Only `bin/agent-shell` sends the gate verb `shell`, and no `allow` rule covers it.
- The managed settings on the cardinal VM are exactly:
  ```json
  {
    "permissions": {
      "defaultMode": "default",
      "allow": ["Bash(/opt/cardinal/plugin/bin/executor:*)", "Skill"],
      "ask": ["Bash(/opt/cardinal/plugin/bin/agent-shell:*)"],
      "deny": ["Read", "Glob", "Grep", "WebFetch", "WebSearch", "Edit", "Write", "NotebookEdit", "mcp__*"],
      "disableBypassPermissionsMode": "disable"
    },
    "hooks": {
      "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "/opt/cardinal/plugin/bin/only-executor"}]}]
    }
  }
  ```
- `only-executor` exits 2 for any Bash command whose first word is not exactly `/opt/cardinal/plugin/bin/executor` or `/opt/cardinal/plugin/bin/agent-shell`, for any command with `;`, `&`, `|`, `<`, `>`, `(`, `)` or a newline outside quotes, and for `$` or `` ` `` anywhere but inside single quotes (where bash would expand them). `cardinal.service` drops `--permission-mode dontAsk`.
- No root verbs, no per-session cgroups, no change to nimblefox, and agent sessions keep `/tmp` as their temp directory. Cardinal does not `hold` for host pressure.
- `flocc host` is a desktop verb (`cli.DESKTOP_VERBS`), which a host refuses. The gate's `host` verb therefore runs the new host command `flocc host-report`.
- Waits on a live system use the Monitor tool (`ToolSearch("select:Monitor")` first), never a foreground `sleep` loop.

## Review Focus

These failure modes follow from the spec, but none of its listed tests would catch them. Each has a test in the task that owns the code.

1. **The `host` verb dies at the desktop-verb refusal.** `argv_for("host") == ["host"]` looks right, but `cli.main` refuses `host` as a desktop command, so cardinal's first repair step always fails. Task 5: `test_the_host_verb_reaches_a_host_command_not_the_desktop_one`.
2. **The deferral sits at the `launch` call, after the publish.** `_finish_phase` publishes before it decides on a fix round or a review, and the publish runs the repo's check: the test suite the episode is about, rerun on every tick of the episode. Task 3: `test_a_finished_step_waits_out_host_pressure` asserts the publish is never called.
3. **A session the OOM killer took during the episode is blocked as "died twice" afterwards.** Deferring the relaunch without remembering why still spends the restart when the episode ends, and the next death blocks the run. Task 3: `test_a_session_that_dies_during_an_episode_is_relaunched_after_it_without_a_restart`.
4. **The same OOM count emits again on every tick of an open episode.** If the baseline is written only when an event goes out, an unchanged counter keeps reading as new. Task 2: `test_a_new_kill_during_an_episode_emits_again_and_an_old_one_does_not`.
5. **One failed recovery `send` stops the rest or keeps the episode open.** A session can end between `alive` and `send`. If `AgentError` escapes, the other sessions are never told and the tick fails. Task 4: `test_a_send_that_fails_does_not_cost_the_other_sessions_theirs`.

## Where the plan settles what the spec leaves open

- The spec lists seven supervisor launch sites. The plan gates two branches of `supervise` instead (Task 3's table), because four of the sites run only after `_finish_phase` has published, and the publish runs the check.
- A run whose session dies during an episode gets its next relaunch free (`host_wait`). The wall clock keeps running through the wait; the spec names only fix rounds and restarts.
- The hook checks raw text, so it also refuses `fix --note` and `hold --reason` text holding `;`, `&`, `<`, `>` or a line break, and `agent-shell` commands with a pipe. The triage skill tells cardinal so.
- In `default` mode, a tool that is neither allowed nor denied asks the owner, where `dontAsk` denied it. The managed settings stay exactly as the spec gives them; the live checks in Task 11 watch for stray prompts.
- Podman's own working directory for a container is a path inside it, so containers are placed under a worktree by label only (compose's `com.docker.compose.project.working_dir`).

---

### Task 1: Measure the host, and the two thresholds

**Files:**
- Create: `src/flocc/host.py`
- Modify: `src/flocc/config.py:71` (fields after `min_free_disk_gb`), `src/flocc/config.py:181-183` (range checks)
- Modify: `deploy/config.example.toml:11-12`
- Test: `tests/test_host.py` (create), `tests/test_config.py:9-30,61-69`

**Interfaces:**
- Produces: `host.GIB = 2**30`; `host.Reading(tmp_used: int, tmp_size: int, mem_available: int, oom_kills: int)`, frozen, with property `tmp_pct -> float`; `host.read(tmp: Path = Path("/tmp"), proc: Path = Path("/proc")) -> Reading`; `host.mem_available(meminfo: str) -> int` (bytes); `host.oom_kills(vmstat: str) -> int`; `host.size_text(n: int) -> str`; `Config.tmp_max_pct: float = 80.0`; `Config.min_available_mem_gb: float = 1.0`.

- [ ] **Step 1: Write the failing tests**

Create `tests/test_host.py`:

```python
import os

import pytest

from flocc import host
from flocc.host import GIB, Reading
from flocc.host import read as real_read

MEMINFO = "MemTotal:        6082560 kB\nMemFree:          204800 kB\nMemAvailable:     629145 kB\n"
VMSTAT = "nr_free_pages 51200\noom_kill 2\nnuma_hit 1\n"


def test_mem_available_is_in_bytes():
    assert host.mem_available(MEMINFO) == 629145 * 1024


def test_a_meminfo_without_mem_available_is_an_error():
    with pytest.raises(ValueError):
        host.mem_available("MemTotal: 1 kB\n")


def test_oom_kills_reads_the_counter_and_is_zero_without_one():
    assert host.oom_kills(VMSTAT) == 2
    assert host.oom_kills("nr_free_pages 1\n") == 0


def test_read_measures_tmp_and_proc(tmp_path):
    proc = tmp_path / "proc"
    proc.mkdir()
    (proc / "meminfo").write_text(MEMINFO)
    (proc / "vmstat").write_text(VMSTAT)
    r = real_read(tmp_path, proc)
    st = os.statvfs(tmp_path)
    assert r.tmp_size == st.f_blocks * st.f_frsize and 0 <= r.tmp_used <= r.tmp_size
    assert (r.mem_available, r.oom_kills) == (629145 * 1024, 2)


def test_tmp_pct_and_an_empty_filesystem():
    assert Reading(3, 4, 0, 0).tmp_pct == 75.0
    assert Reading(0, 0, 0, 0).tmp_pct == 0.0


def test_size_text_switches_to_gib_at_one_gib():
    assert host.size_text(3 * GIB // 2) == "1.5 GiB"
    assert host.size_text(5 * 2**20) == "5 MiB"
```

`real_read` is bound at import: from Task 2 on, `conftest.py` replaces `flocc.host.read` for every test.

In `tests/test_config.py`, add to `test_example_loads_with_defaults` after the `min_free_disk_gb` line:

```python
    assert cfg.tmp_max_pct == 80.0
    assert cfg.min_available_mem_gb == 1.0
```

and extend the parametrize list of `test_out_of_range_number_is_an_error`:

```python
    [
        "digest_hour = 24",
        "session_cap = 0",
        "five_hour_max_pct = 150.0",
        "weekly_max_pct = -1.0",
        "tmp_max_pct = 101.0",
        "min_available_mem_gb = -1.0",
    ],
```

- [ ] **Step 2: Run them and see them fail**

Run: `uv run pytest tests/test_host.py tests/test_config.py -q`
Expected: `ModuleNotFoundError: No module named 'flocc.host'` for `test_host.py`, and `AttributeError: 'Config' object has no attribute 'tmp_max_pct'` in `test_config.py`.

- [ ] **Step 3: Implement**

Create `src/flocc/host.py`:

```python
import os
from dataclasses import dataclass
from pathlib import Path

GIB = 2**30


@dataclass(frozen=True)
class Reading:
    tmp_used: int
    tmp_size: int
    mem_available: int
    oom_kills: int

    @property
    def tmp_pct(self) -> float:
        return 100 * self.tmp_used / self.tmp_size if self.tmp_size else 0.0


def mem_available(meminfo: str) -> int:
    for line in meminfo.splitlines():
        if line.startswith("MemAvailable:"):
            return int(line.split()[1]) * 1024
    raise ValueError("no MemAvailable line in /proc/meminfo")


def oom_kills(vmstat: str) -> int:
    for line in vmstat.splitlines():
        name, _, value = line.partition(" ")
        if name == "oom_kill":
            return int(value)
    # Kernels before 4.13 keep no counter.
    return 0


def read(tmp: Path = Path("/tmp"), proc: Path = Path("/proc")) -> Reading:
    st = os.statvfs(tmp)
    return Reading(
        tmp_used=(st.f_blocks - st.f_bfree) * st.f_frsize,
        tmp_size=st.f_blocks * st.f_frsize,
        mem_available=mem_available((proc / "meminfo").read_text()),
        oom_kills=oom_kills((proc / "vmstat").read_text()),
    )


def size_text(n: int) -> str:
    return f"{n / GIB:.1f} GiB" if n >= GIB else f"{n / 2**20:.0f} MiB"
```

In `src/flocc/config.py`, after `min_free_disk_gb: float = 2.0` (line 71):

```python
    # flocc.host opens an episode past either: starts and relaunches wait, and cardinal is told.
    tmp_max_pct: float = 80.0
    min_available_mem_gb: float = 1.0
```

Replace lines 181-183 with:

```python
    for key in ("five_hour_max_pct", "weekly_max_pct", "tmp_max_pct"):
        if not 0 <= getattr(cfg, key) <= 100:
            raise ValueError(f"{path}: {key} {getattr(cfg, key)} is not in 0-100")
    if cfg.min_available_mem_gb < 0:
        raise ValueError(f"{path}: min_available_mem_gb {cfg.min_available_mem_gb} must be >= 0")
```

In `deploy/config.example.toml`, after line 12 (`# min_free_disk_gb = 2.0`):

```toml
# Starts and relaunches wait, and cardinal is told, while /tmp is fuller than this percent or
# memory available is below this many GiB.
# tmp_max_pct = 80.0
# min_available_mem_gb = 1.0
```

- [ ] **Step 4: Run them and see them pass**

Run: `uv run pytest tests/test_host.py tests/test_config.py -q`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/host.py src/flocc/config.py deploy/config.example.toml tests/test_host.py tests/test_config.py
git commit -s -m "feat(host): measure /tmp, memory and OOM kills, with their thresholds"
```

---

### Task 2: Episodes and the `host-pressure` event in the tick

**Files:**
- Modify: `src/flocc/host.py` (add episode state and `check`)
- Modify: `src/flocc/events.py:8`
- Modify: `src/flocc/tick.py:10` (import), `src/flocc/tick.py:176-177` (call before the supervise loop)
- Modify: `tests/conftest.py:1-19` (autouse calm reading)
- Test: `tests/test_host.py`, `tests/test_poller.py` (tick tests, after `test_tick_linear_error_on_one_claim_still_runs_the_rest`)

**Interfaces:**
- Consumes: Task 1's `Reading`, `read`, `GIB`; `events.emit`; `ledger.meta_get/meta_set`.
- Produces: `events.KINDS` gains `"host-pressure"`; `host.EPISODE = "host_episode"`; `host.OOM_SEEN = "host_oom_kills"`; `host.CLOSE_TMP = 0.9`; `host.CLOSE_MEM = 1.1`; `host.open_episode(con, now: float, detail: str) -> None`; `host.pressure_detail(con) -> str` ("" when no episode is open); `host.under_pressure(con) -> bool`; `host.check(rt, reading: Reading | None = None) -> None` (uses `rt.cfg`, `rt.con`, `rt.now`, and from Task 4 `rt.sessions`). `tests.conftest.CALM`.

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_host.py` (add `import types` and `from flocc import events, ledger` and `from tests.fakes import FakeSessions` to its imports):

```python
SIZE = 3 * GIB


def reading(tmp_pct=10.0, mem_gb=4.0, ooms=0) -> Reading:
    return Reading(int(SIZE * tmp_pct / 100), SIZE, int(mem_gb * GIB), ooms)


def runtime(cfg):
    return types.SimpleNamespace(
        cfg=cfg, con=ledger.connect(cfg.ledger_path), now=lambda: 100.0, sessions=FakeSessions()
    )


def details(rt) -> list[str]:
    return [e.detail for e in events.unacked(rt.con) if e.kind == "host-pressure"]


def test_an_episode_opens_once_and_stays_open_between_the_thresholds(tmp_cfg):
    rt = runtime(tmp_cfg)
    host.check(rt, reading(93))
    host.check(rt, reading(93))
    host.check(rt, reading(75))
    assert details(rt) == ["/tmp 93% (2.8/3.0 GiB)"]
    assert host.under_pressure(rt.con)
    assert host.pressure_detail(rt.con) == "/tmp 93% (2.8/3.0 GiB)"


def test_an_episode_closes_once_every_value_is_past_its_close_threshold(tmp_cfg):
    rt = runtime(tmp_cfg)
    host.check(rt, reading(93, mem_gb=0.6))
    host.check(rt, reading(71, mem_gb=1.05))
    assert host.under_pressure(rt.con)
    host.check(rt, reading(71, mem_gb=1.2))
    assert not host.under_pressure(rt.con) and host.pressure_detail(rt.con) == ""
    host.check(rt, reading(93))
    assert len(details(rt)) == 2


def test_the_detail_names_every_value_over_its_limit(tmp_cfg):
    rt = runtime(tmp_cfg)
    host.check(rt, reading(ooms=3))
    host.check(rt, reading(93, mem_gb=0.6, ooms=5))
    assert details(rt) == ["/tmp 93% (2.8/3.0 GiB); MemAvailable 0.6 GiB; 2 OOM kills"]


def test_the_first_reading_and_a_counter_drop_only_set_the_baseline(tmp_cfg):
    rt = runtime(tmp_cfg)
    host.check(rt, reading(ooms=7))
    host.check(rt, reading(ooms=1))
    assert details(rt) == [] and not host.under_pressure(rt.con)
    assert ledger.meta_get(rt.con, host.OOM_SEEN) == "1"
    host.check(rt, reading(ooms=2))
    assert details(rt) == ["1 OOM kill"]


def test_a_new_kill_during_an_episode_emits_again_and_an_old_one_does_not(tmp_cfg):
    rt = runtime(tmp_cfg)
    host.check(rt, reading(ooms=5))
    host.check(rt, reading(93, ooms=5))
    host.check(rt, reading(93, ooms=6))
    host.check(rt, reading(93, ooms=6))
    host.check(rt, reading(93, ooms=6))
    assert details(rt) == ["/tmp 93% (2.8/3.0 GiB)", "/tmp 93% (2.8/3.0 GiB); 1 OOM kill"]


def test_an_oom_only_episode_closes_on_the_first_tick_without_a_new_kill(tmp_cfg):
    rt = runtime(tmp_cfg)
    host.check(rt, reading(ooms=1))
    host.check(rt, reading(ooms=2))
    assert host.under_pressure(rt.con)
    host.check(rt, reading(ooms=2))
    assert not host.under_pressure(rt.con)
```

Append to `tests/test_poller.py` (add `from flocc import host` and `from flocc.host import Reading` to its imports):

```python
FULL_TMP = Reading(29 * 2**30 // 10, 3 * 2**30, 4 * 2**30, 0)


def test_the_tick_opens_a_host_episode_before_it_supervises(rt, tmp_path, monkeypatch):
    ledger.insert_claim(rt.con, make("NF-1"))
    seen = []
    monkeypatch.setattr("flocc.host.read", lambda: FULL_TMP)
    monkeypatch.setattr("flocc.tick.build_runtime", lambda cfg, path: rt)
    monkeypatch.setattr(
        "flocc.tick.supervise", lambda rt, claim: seen.append(host.under_pressure(rt.con))
    )
    monkeypatch.setattr("flocc.tick.poll", lambda rt: None)
    tick(rt.cfg, tmp_path / "c.toml")
    assert seen == [True]
    assert [e.kind for e in events.unacked(rt.con) if e.kind == "host-pressure"] == ["host-pressure"]


def test_a_host_that_cannot_be_measured_does_not_stop_the_tick(rt, tmp_path, monkeypatch):
    def unreadable():
        raise OSError("no /proc/meminfo")

    monkeypatch.setattr("flocc.host.read", unreadable)
    monkeypatch.setattr("flocc.tick.build_runtime", lambda cfg, path: rt)
    monkeypatch.setattr("flocc.tick.poll", lambda rt: None)
    assert tick(rt.cfg, tmp_path / "c.toml") == 0
```

(`events` is already imported in `test_poller.py`; add it to the `from flocc import ...` line if it is not.)

- [ ] **Step 2: Run them and see them fail**

Run: `uv run pytest tests/test_host.py tests/test_poller.py -q -k "episode or kill or baseline or detail or measured"`
Expected: `AttributeError: module 'flocc.host' has no attribute 'check'`, and the tick tests fail with no `host-pressure` event.

- [ ] **Step 3: Implement**

In `src/flocc/events.py`, replace line 8:

```python
KINDS = frozenset(
    {"ended", "cap", "base-broken", "bad-sequence", "service-error", "daily", "host-pressure"}
)
```

In `src/flocc/host.py`, add `import json` at the top and `from flocc import events, ledger` below the stdlib imports, then append:

```python
EPISODE = "host_episode"
OOM_SEEN = "host_oom_kills"
# A value sitting at its limit would otherwise open and close an episode every tick.
CLOSE_TMP = 0.9
CLOSE_MEM = 1.1


def _episode(con) -> dict | None:
    raw = ledger.meta_get(con, EPISODE)
    try:
        value = json.loads(raw) if raw else None
    except ValueError:
        return None
    return value if isinstance(value, dict) else None


def open_episode(con, now: float, detail: str) -> None:
    ledger.meta_set(con, EPISODE, json.dumps({"at": now, "detail": detail}))


def pressure_detail(con) -> str:
    """The open episode's detail; "" when the host is not under pressure."""
    episode = _episode(con)
    if episode is None:
        return ""
    return str(episode.get("detail") or "host under pressure")


def under_pressure(con) -> bool:
    return bool(pressure_detail(con))


def _over(r: Reading, cfg, new_ooms: int) -> list[str]:
    parts = []
    if r.tmp_pct > cfg.tmp_max_pct:
        parts.append(f"/tmp {r.tmp_pct:.0f}% ({r.tmp_used / GIB:.1f}/{r.tmp_size / GIB:.1f} GiB)")
    if r.mem_available < cfg.min_available_mem_gb * GIB:
        parts.append(f"MemAvailable {r.mem_available / GIB:.1f} GiB")
    if new_ooms:
        parts.append(f"{new_ooms} OOM kill{'s' * (new_ooms != 1)}")
    return parts


def _recovered(r: Reading, cfg) -> bool:
    return (
        r.tmp_pct < CLOSE_TMP * cfg.tmp_max_pct
        and r.mem_available > CLOSE_MEM * cfg.min_available_mem_gb * GIB
    )


def check(rt, reading: Reading | None = None) -> None:
    """Opens an episode with a host-pressure event when a value crosses its limit, and closes it
    once every value is back past its close threshold."""
    r = reading or read()
    con, now = rt.con, rt.now()
    seen = ledger.meta_get(con, OOM_SEEN)
    # The counter restarts at boot, so a drop means a reboot. A drop and the first reading only
    # set the baseline.
    new_ooms = r.oom_kills - int(seen) if seen and r.oom_kills >= int(seen) else 0
    over = _over(r, rt.cfg, new_ooms)
    episode = _episode(con)
    # A kill during an open episode is an event of its own: the repair did not hold.
    if over and (episode is None or new_ooms):
        detail = "; ".join(over)
        # Before the marks: a tick that dies in between emits again, and cardinal acks the
        # duplicate away, where a lost event is never seen.
        events.emit(con, "host-pressure", now, detail=detail)
        open_episode(con, now if episode is None else float(episode.get("at", now)), detail)
    elif episode is not None and _recovered(r, rt.cfg):
        ledger.meta_set(con, EPISODE, "")
    ledger.meta_set(con, OOM_SEEN, str(r.oom_kills))
```

In `src/flocc/tick.py`, change line 10 to `from flocc import host, ledger, rebase` and replace lines 176-177:

```python
    try:
        # Before supervise, so nothing it would launch starts into an episode this tick opens.
        try:
            host.check(rt)
        except (OSError, ValueError):
            traceback.print_exc()
        for claim in ledger.live_claims(rt.con):
```

In `tests/conftest.py`, add `from flocc.host import Reading` to the imports, and after the `no_token_check` fixture:

```python
CALM = Reading(tmp_used=0, tmp_size=2**30, mem_available=8 * 2**30, oom_kills=0)


@pytest.fixture(autouse=True)
def calm_host(monkeypatch):
    # The tick reads the real /tmp and /proc otherwise, and a full /tmp on the machine running
    # the suite would open an episode and hold every start the tests expect.
    monkeypatch.setattr("flocc.host.read", lambda: CALM)
```

- [ ] **Step 4: Run them and see them pass**

Run: `uv run pytest tests/test_host.py tests/test_poller.py tests/test_events.py -q`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/host.py src/flocc/events.py src/flocc/tick.py tests/conftest.py tests/test_host.py tests/test_poller.py
git commit -s -m "feat(host): open a host-pressure episode on each tick before supervise"
```

---

### Task 3: The start gate, and supervisor deferral without spending budget

**Files:**
- Modify: `src/flocc/admission.py:17-29`
- Modify: `src/flocc/poller.py:8` (import), `src/flocc/poller.py:258-269` (pass the episode), `src/flocc/poller.py:350` (clear `host_wait` on a fresh claim)
- Modify: `src/flocc/control.py:5` (import), `src/flocc/control.py:35-50` (`admit_rerun`)
- Modify: `src/flocc/supervisor.py:10` (import), after `src/flocc/supervisor.py:44` (constant), `src/flocc/supervisor.py:760-784` (`supervise`)
- Test: `tests/test_admission.py`, `tests/test_poller.py`, `tests/test_control.py`, `tests/test_supervisor.py`

**Interfaces:**
- Consumes: `host.pressure_detail`, `host.under_pressure`, `host.open_episode`, `host.EPISODE`.
- Produces: `admit(..., held: frozenset[str] = frozenset(), host_pressure: str = "") -> Decision`, refusing with reason `f"host under pressure: {host_pressure}"`; `supervisor.HOST_WAIT = "host_wait"` (meta key prefix, `f"{HOST_WAIT}:{key}"`).

How each launch site the spec lists is covered:

| Spec site | Path | Gate |
|---|---|---|
| `supervisor.py:256` review, `:346` review fix, `:461` next step, `:556` fix round | reached only through `_finish_phase` from `supervise`'s fresh `done.json` branch | `under_pressure` before `_finish_phase`, so the publish and its check do not run either |
| `:205` and `:782` crash relaunch | `supervise`'s dead-session branch | `under_pressure` before the restart checks; `host_wait` makes the later relaunch free |
| `:384` rebase | `rebase.step` calls `admit_rerun` first | admission |
| `control.start_fix`, `poller.py:258` | `admit_rerun` and `admit` | admission |

- [ ] **Step 1: Write the failing tests**

`tests/test_admission.py`, append:

```python
def test_host_pressure_holds_the_start():
    d = admit("NF-9", "backend", DEFAULT, [], OK, {}, CFG, 1100, None, host_pressure="/tmp 93%")
    assert not d.ok and d.reason == "host under pressure: /tmp 93%"
```

`tests/test_poller.py`, after `test_low_disk_holds_a_start`:

```python
def test_host_pressure_holds_a_start(rt, capsys):
    host.open_episode(rt.con, 1.0, "MemAvailable 0.6 GiB")
    rt.linear.ready = [issue()]
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is None and rt.linear.started == []
    assert "host under pressure: MemAvailable 0.6 GiB" in capsys.readouterr().err
```

`tests/test_control.py`, add `from flocc import host, ledger` (replacing `from flocc import ledger`) and append:

```python
def test_fix_is_refused_while_the_host_is_under_pressure(rt, tmp_path):
    blocked(rt, tmp_path)
    host.open_episode(rt.con, 1.0, "/tmp 93% (2.8/3.0 GiB)")
    with pytest.raises(Refused, match="not now: host under pressure"):
        start_fix(rt, "NF-1", "note")
    assert rt.sessions.started == []
```

`tests/test_supervisor.py`, change the import to `from flocc import canary, events, host, ledger` and append:

```python
def pressure(rt):
    host.open_episode(rt.con, 50.0, "/tmp 93% (2.8/3.0 GiB)")


def recover(rt):
    ledger.meta_set(rt.con, host.EPISODE, "")


@pytest.mark.parametrize(
    ("phase", "result", "after"),
    [
        ("test", None, "impl"),
        ("impl", PublishResult("opened", url="https://pr/1"), "review"),
        ("impl", PublishResult("check_failed", "E boom"), "fix"),
    ],
)
def test_a_finished_step_waits_out_host_pressure(rt, phase, result, after):
    add(rt, phase=phase, tester_sha="sha-tests")
    published = []

    def publish(claim, title, summary, **kw):
        published.append(claim.issue_key)
        return result

    rt.publish_fn = publish
    done(rt, "step done")
    pressure(rt)
    for _ in range(2):
        supervise(rt, current(rt))
    c = current(rt)
    assert (c.state, c.phase, c.fix_attempts, c.restarts) == ("running", phase, 0, 0)
    assert rt.sessions.started == [] and published == []
    recover(rt)
    supervise(rt, current(rt))
    assert current(rt).phase == after and len(rt.sessions.started) == 1


def test_a_review_asking_for_changes_waits_out_host_pressure(rt):
    review_session(rt, CHANGES)
    done(rt, "review")
    pressure(rt)
    supervise(rt, current(rt))
    assert current(rt).phase == "review" and rt.pr_comments == []
    recover(rt)
    supervise(rt, current(rt))
    assert (current(rt).phase, current(rt).fix_attempts) == ("fix", 0)


def test_a_session_that_dies_during_an_episode_is_relaunched_after_it_without_a_restart(rt):
    add(rt, phase="impl", restarts=1)
    pressure(rt)
    supervise(rt, current(rt))
    c = current(rt)
    assert (c.state, c.restarts) == ("running", 1) and rt.sessions.started == []
    recover(rt)
    supervise(rt, current(rt))
    c = current(rt)
    assert (c.state, c.restarts) == ("running", 1) and rt.sessions.started[0][0] == "NF-1"
    rt.sessions.alive_names.clear()
    supervise(rt, current(rt))
    assert current(rt).state == "blocked"
```

- [ ] **Step 2: Run them and see them fail**

Run: `uv run pytest tests/test_admission.py tests/test_poller.py tests/test_control.py tests/test_supervisor.py -q -k "pressure or episode"`
Expected: `TypeError: admit() got an unexpected keyword argument 'host_pressure'`, a start in the poller test, no `Refused` in the control test, and launches under pressure in the supervisor tests.

- [ ] **Step 3: Implement**

`src/flocc/admission.py`, extend the signature and put the check first:

```python
    held: frozenset[str] = frozenset(),
    host_pressure: str = "",
) -> Decision:
    if host_pressure:
        return Decision(False, f"host under pressure: {host_pressure}")
    if free_disk_gb is not None and free_disk_gb < cfg.min_free_disk_gb:
```

`src/flocc/poller.py`: line 8 becomes `from flocc import canary, events, host, ledger`; the `admit(...)` call at lines 258-269 gains a last argument after `ledger.held_keys(rt.con),`:

```python
            host.pressure_detail(rt.con),
```

and line 350 becomes:

```python
    for kept in ("rebase", "rebase_tried", "base_held", "base_released", "cardinal_fix", "host_wait"):
```

(ruff format will wrap it; keep the tuple's order.)

`src/flocc/control.py`: line 5 becomes `from flocc import host, ledger`, and `admit_rerun`'s call gains a last argument after `ledger.held_keys(rt.con),`:

```python
        host.pressure_detail(rt.con),
```

`src/flocc/supervisor.py`: line 10 becomes `from flocc import agentfs, canary, events, host, ledger`. After `BLOCKED = "blocked.md"` (line 44):

```python
# Set when a session is found dead during a host episode: it most likely died of the episode,
# so its relaunch afterwards costs no restart.
HOST_WAIT = "host_wait"
```

In `supervise`, replace the fresh-`done.json` block (lines 760-765):

```python
        if not stale:
            rt.sessions.kill(name)
            if claim.phase != "review":
                (d / LAST_SUMMARY).write_text(summary)
            # Before _finish_phase: its publish runs the repo's check, the load an episode is about.
            if host.under_pressure(rt.con):
                return
            _finish_phase(rt, claim, summary)
            return
```

and the dead-session block (lines 769-784):

```python
    if not rt.sessions.alive(name):
        waited = f"{HOST_WAIT}:{key}"
        if host.under_pressure(rt.con):
            ledger.meta_set(rt.con, waited, "1")
            return
        if ledger.meta_get(rt.con, waited):
            _relaunch(rt, claim)
            ledger.meta_set(rt.con, waited, "")
            return
        if claim.restarts >= 1 and claim.phase == "review":
            _review_skipped(rt, claim, f"the session died twice ({name})")
            return
        if claim.restarts >= 1:
            _block(
                rt,
                claim,
                "crash",
                "The session died twice.",
                f"Check the worktree, then run flocc release --keep-worktree {key}",
            )
            return
        _relaunch(rt, claim)
        ledger.update_claim(rt.con, key, restarts=claim.restarts + 1)
        return
```

- [ ] **Step 4: Run them and see them pass**

Run: `uv run pytest tests/test_admission.py tests/test_poller.py tests/test_control.py tests/test_supervisor.py tests/test_rebase.py -q`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/admission.py src/flocc/poller.py src/flocc/control.py src/flocc/supervisor.py tests/test_admission.py tests/test_poller.py tests/test_control.py tests/test_supervisor.py
git commit -s -m "feat(host): hold starts and launches during a host episode without spending budget"
```

---

### Task 4: Tell running sessions when the host recovers

**Files:**
- Modify: `src/flocc/host.py` (imports, `RECOVERED`, `_tell_recovered`, the close branch of `check`)
- Test: `tests/test_host.py`

**Interfaces:**
- Consumes: `rt.sessions.alive(name)`, `rt.sessions.send(name, text)` (`AgentSessions.send`, the existing path); `sessions.session_name`; `ledger.live_claims`.
- Produces: `host.RECOVERED = "The host has recovered; you may run the test suite again."`.

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_host.py` (add `from flocc.agent import AgentError` and `from tests.test_ledger import make` to its imports):

```python
def claim(rt, key, phase, state="running"):
    c = make(key, state=state)
    c.phase = phase
    ledger.insert_claim(rt.con, c)


def test_closing_the_episode_tells_each_running_session_once(tmp_cfg):
    rt = runtime(tmp_cfg)
    claim(rt, "NF-1", "impl")
    claim(rt, "NF-2", "review")
    claim(rt, "NF-3", "impl", state="blocked")
    claim(rt, "NF-4", "fix")
    rt.sessions.alive_names = {"NF-1", "NF-2-review", "NF-3"}
    host.check(rt, reading(93))
    assert rt.sessions.sent == []
    host.check(rt, reading(10))
    host.check(rt, reading(10))
    assert sorted(rt.sessions.sent) == [("NF-1", host.RECOVERED), ("NF-2-review", host.RECOVERED)]


def test_a_send_that_fails_does_not_cost_the_other_sessions_theirs(tmp_cfg, capsys):
    rt = runtime(tmp_cfg)
    claim(rt, "NF-1", "impl")
    claim(rt, "NF-2", "impl")
    rt.sessions.alive_names = {"NF-1", "NF-2"}
    sent = []

    def send(name, text):
        if name == "NF-1":
            raise AgentError("flocc-agent-run send exited 1: can't find session")
        sent.append(name)

    rt.sessions.send = send
    host.check(rt, reading(93))
    host.check(rt, reading(10))
    assert sent == ["NF-2"] and not host.under_pressure(rt.con)
    assert "NF-1: recovery message not sent" in capsys.readouterr().err
```

- [ ] **Step 2: Run them and see them fail**

Run: `uv run pytest tests/test_host.py -q -k "tells or send"`
Expected: `AttributeError: module 'flocc.host' has no attribute 'RECOVERED'`.

- [ ] **Step 3: Implement**

In `src/flocc/host.py` add `import subprocess` and `import sys` to the stdlib imports and `from flocc.sessions import session_name` below `from flocc import events, ledger`. Add after `CLOSE_MEM`:

```python
RECOVERED = "The host has recovered; you may run the test suite again."
```

Add before `check`:

```python
def _tell_recovered(rt) -> None:
    for claim in ledger.live_claims(rt.con):
        if claim.state != "running":
            continue
        name = session_name(claim.issue_key, claim.phase)
        # A session can end between the two calls; the others still get their message.
        try:
            if rt.sessions.alive(name):
                rt.sessions.send(name, RECOVERED)
        except (subprocess.SubprocessError, OSError) as exc:
            print(f"{claim.issue_key}: recovery message not sent: {exc}", file=sys.stderr)
```

and in `check`, the close branch becomes:

```python
    elif episode is not None and _recovered(r, rt.cfg):
        # Before the messages, so starts resume even if every send fails.
        ledger.meta_set(con, EPISODE, "")
        _tell_recovered(rt)
```

- [ ] **Step 4: Run them and see them pass**

Run: `uv run pytest tests/test_host.py tests/test_poller.py -q`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/host.py tests/test_host.py
git commit -s -m "feat(host): tell each running session when the host recovers"
```

---

### Task 5: The `host` report: `flocc-agent-run usage` and the gate verb

**Files:**
- Modify: `src/flocc/agentrun.py:4-11` (imports), `:28-41` (`SUBCOMMANDS`), after `:306` (helpers), before `:547` (`usage`), `:547-556` (`HANDLERS`)
- Modify: `deploy/flocc-agent-run:7`
- Modify: `src/flocc/agent.py` (method after `sweep`, line 130)
- Modify: `src/flocc/host.py` (`report_text`)
- Modify: `src/flocc/cli.py` (new `cmd_host_report` after `cmd_cleanup` at line 439, `COMMANDS` at 738-765, parser at 829)
- Modify: `src/flocc/cardinalgate.py:47-66`
- Test: `tests/test_agentrun.py`, `tests/test_host.py`, `tests/test_cli.py`, `tests/test_cardinalgate.py`

**Interfaces:**
- Consumes: `agentrun.under_home`, `container_env`, `_KEY`; `host.read`, `host.size_text`, `host.pressure_detail`, `host.GIB`.
- Produces (agentrun, reused by Tasks 6 and 7): `_tmp_root() -> Path`; `_pytest_root() -> Path`; `_du(path: Path) -> int`; `_procs() -> dict[int, tuple[int, list[str]]]` (pid to parent pid and argv); `_path_issue(path: Path, root: Path) -> str | None`; `_cwd_issue(pid: int, root: Path) -> str | None`; `_lock_pid(d: Path) -> int | None`; `_containers() -> list[dict]` (`podman ps -a --format json`); `_container_issue(container: dict, root: Path) -> str | None`; `_container_name(c: dict) -> str`; `usage(req: {"worktree_root": str}) -> {"largest": [{"path", "bytes", "issue"}], "issues": {KEY: bytes}, "containers": [{"name", "memory", "issue"}]}`. `Agent.usage() -> dict`. `host.report_text(cfg, r: Reading, usage: dict, episode: str) -> str`. CLI `flocc host-report`. Gate: `host` maps to `["host-report"]`.

- [ ] **Step 1: Write the failing tests**

`tests/test_agentrun.py`, add `import sys` and `import time` to the imports and append:

```python
def _agent_home(tmp_path, monkeypatch):
    home = tmp_path / "agent"
    (home / "worktrees").mkdir(parents=True)
    monkeypatch.setenv("HOME", str(home))
    tmp = tmp_path / "tmp"
    tmp.mkdir()
    monkeypatch.setattr("flocc.agentrun._tmp_root", lambda: tmp)
    return home, tmp


def test_usage_names_the_issue_behind_a_pytest_run_and_its_memory(tmp_path, monkeypatch):
    home, tmp = _agent_home(tmp_path, monkeypatch)
    monkeypatch.setattr("flocc.agentrun.container_env", dict)
    wt = home / "worktrees" / "NF-5"
    wt.mkdir()
    run = agentrun._pytest_root() / "pytest-7"
    run.mkdir(parents=True)
    (run / "copy").write_bytes(b"x" * 300_000)
    (tmp / "small").write_bytes(b"x" * 10)
    proc = subprocess.Popen(["sleep", "30"], cwd=wt)
    try:
        (run / ".lock").write_text(str(proc.pid))
        out = agentrun.usage({"worktree_root": str(home / "worktrees")})
    finally:
        proc.kill()
        proc.wait()
    first = out["largest"][0]
    assert first["path"] == str(run) and first["issue"] == "NF-5" and first["bytes"] >= 300_000
    assert {e["path"] for e in out["largest"]} == {str(run), str(tmp / "small")}
    assert out["issues"]["NF-5"] > 0 and out["containers"] == []


def test_usage_lists_containers_with_their_memory_and_issue(tmp_path, monkeypatch):
    home, _ = _agent_home(tmp_path, monkeypatch)
    root = (home / "worktrees").resolve()
    monkeypatch.setattr("flocc.agentrun.container_env", lambda: {"DOCKER_HOST": "unix:///s"})
    monkeypatch.setattr("flocc.agentrun.shutil.which", lambda name: "/usr/bin/podman")
    label = "com.docker.compose.project.working_dir"
    ps = [
        {"Id": "abc123def456" + "0" * 52, "Names": ["nf-db-1"], "Labels": {label: str(root / "NF-5")}},
        {"Id": "f" * 64, "Names": ["other"], "Labels": {}},
    ]

    def run(argv, **kw):
        if argv[:2] == ["podman", "ps"]:
            return subprocess.CompletedProcess(argv, 0, json.dumps(ps), "")
        return subprocess.CompletedProcess(argv, 0, "abc123def456\t120MB / 5.8GB\n", "")

    monkeypatch.setattr("flocc.agentrun.subprocess.run", run)
    out = agentrun.usage({"worktree_root": str(home / "worktrees")})
    assert out["containers"] == [
        {"name": "nf-db-1", "memory": "120MB / 5.8GB", "issue": "NF-5"},
        {"name": "other", "memory": "not running", "issue": None},
    ]
```

`tests/test_host.py`, append:

```python
USAGE = {
    "largest": [
        {"path": "/tmp/pytest-of-flocc-agent/pytest-7", "bytes": 2 * GIB, "issue": "NF-5"},
        {"path": "/tmp/claude-1001", "bytes": 5 * 2**20, "issue": None},
    ],
    "issues": {"NF-5": 3 * GIB // 2, "NF-6": GIB // 4},
    "containers": [{"name": "nf-db-1", "memory": "120MB / 5.8GB", "issue": "NF-5"}],
}


def test_the_report_names_the_values_their_limits_and_the_owners(tmp_cfg):
    r = Reading(int(SIZE * 0.93), SIZE, int(0.6 * GIB), 4)
    text = host.report_text(tmp_cfg, r, USAGE, "/tmp 93% (2.8/3.0 GiB)")
    for part in (
        "/tmp 93% used (2.8 GiB of 3.0 GiB); limit 80%",
        "MemAvailable 0.6 GiB; limit 1 GiB",
        "OOM kills since boot: 4",
        "Episode open: /tmp 93% (2.8/3.0 GiB)",
        "- 2.0 GiB /tmp/pytest-of-flocc-agent/pytest-7 (NF-5)",
        "- 5 MiB /tmp/claude-1001\n",
        "- NF-5: 1.5 GiB\n- NF-6: 256 MiB",
        "- nf-db-1: 120MB / 5.8GB (NF-5)",
    ):
        assert part in text


def test_an_empty_report_says_none(tmp_cfg):
    text = host.report_text(
        tmp_cfg, reading(), {"largest": [], "issues": {}, "containers": []}, ""
    )
    assert "Episode: none open" in text and text.count("- none") == 3
```

`tests/test_cli.py`, append:

```python
def test_host_report_prints_the_reading_and_flocc_agent_s_part(host_cfg, monkeypatch, capsys):
    load(host_cfg).ledger_path.parent.mkdir(parents=True, exist_ok=True)
    usage = {"largest": [], "issues": {"NF-5": 2**30}, "containers": []}
    monkeypatch.setattr("flocc.agent.Agent.usage", lambda self: usage)
    assert main(["--config", str(host_cfg), "host-report"]) == 0
    out = capsys.readouterr().out
    assert "/tmp 0% used" in out and "- NF-5: 1.0 GiB" in out and "Episode: none open" in out
```

`tests/test_cardinalgate.py`: add `("host", ["host-report"]),` to the `test_the_allowed_lines` list and `"host --json",` to `test_anything_else_is_refused`, then append:

```python
def test_the_host_verb_reaches_a_host_command_not_the_desktop_one(monkeypatch, capsys):
    from flocc import cli

    monkeypatch.setenv("SSH_ORIGINAL_COMMAND", "host")
    monkeypatch.setitem(cli.COMMANDS, "host-report", lambda args: 0)
    assert cardinalgate.main() == 0
    assert "desktop command" not in capsys.readouterr().err
```

- [ ] **Step 2: Run them and see them fail**

Run: `uv run pytest tests/test_agentrun.py tests/test_host.py tests/test_cli.py tests/test_cardinalgate.py -q -k "usage or report or host"`
Expected: `AttributeError: module 'flocc.agentrun' has no attribute '_tmp_root'`, no `report_text`, `invalid choice: 'host-report'`, and the gate refusing `host`.

- [ ] **Step 3: Implement**

`src/flocc/agentrun.py`: add `import pwd` to the stdlib imports. In `SUBCOMMANDS`, add `"usage",` after `"sweep",`. After `scratch_path` (line 306):

```python
def _tmp_root() -> Path:
    return Path("/tmp")


def _pytest_root() -> Path:
    return _tmp_root() / f"pytest-of-{pwd.getpwuid(os.getuid()).pw_name}"


def _du(path: Path) -> int:
    """Bytes allocated under path, links not followed; on a tmpfs these are the pages it holds."""
    try:
        total = path.lstat().st_blocks * 512
    except OSError:
        return 0
    for top, dirs, files in os.walk(path):
        for name in dirs + files:
            try:
                total += os.lstat(os.path.join(top, name)).st_blocks * 512
            except OSError:
                pass
    return total


def _procs() -> dict[int, tuple[int, list[str]]]:
    found: dict[int, tuple[int, list[str]]] = {}
    for d in Path("/proc").iterdir():
        if not d.name.isdigit():
            continue
        try:
            stat = (d / "stat").read_text()
            raw = (d / "cmdline").read_bytes()
        except OSError:
            continue
        # The command name, in parentheses, may itself hold spaces and parentheses.
        ppid = int(stat.rsplit(")", 1)[1].split()[1])
        found[int(d.name)] = (ppid, [a.decode(errors="replace") for a in raw.split(b"\0") if a])
    return found


def _path_issue(path: Path, root: Path) -> str | None:
    try:
        rel = path.relative_to(root)
    except ValueError:
        return None
    return rel.parts[0] if rel.parts and _KEY.match(rel.parts[0]) else None


def _cwd_issue(pid: int, root: Path) -> str | None:
    try:
        return _path_issue(Path(os.readlink(f"/proc/{pid}/cwd")), root)
    except OSError:
        return None


def _lock_pid(d: Path) -> int | None:
    try:
        pid = int((d / ".lock").read_text().strip())
    except (OSError, ValueError):
        return None
    return pid if pid > 0 else None


def _tmp_entries() -> list[Path]:
    """flocc-agent's entries in /tmp, with pytest's base directory opened up into its runs."""
    uid, pytest_root, found = os.getuid(), _pytest_root(), []
    for p in sorted(_tmp_root().iterdir()):
        try:
            if p.lstat().st_uid != uid:
                continue
        except OSError:
            continue
        if p == pytest_root and p.is_dir() and not p.is_symlink():
            found += [d for d in sorted(p.iterdir()) if not d.is_symlink()]
        else:
            found.append(p)
    return found


def _rss(pid: int) -> int:
    try:
        return int(Path(f"/proc/{pid}/statm").read_text().split()[1]) * os.sysconf("SC_PAGE_SIZE")
    except (OSError, ValueError, IndexError):
        return 0
```

Before `HANDLERS`, after `sweep`:

```python
def _containers() -> list[dict]:
    if not (container_env() and shutil.which("podman")):
        return []
    out = subprocess.run(
        ["podman", "ps", "-a", "--format", "json"],
        capture_output=True,
        text=True,
        check=False,
        timeout=60,
    )
    try:
        data = json.loads(out.stdout or "[]")
    except ValueError:
        return []
    return [c for c in data if isinstance(c, dict)] if isinstance(data, list) else []


def _container_issue(container: dict, root: Path) -> str | None:
    # Compose records the project's directory in a label. A container's own working directory
    # is a path inside it and says nothing about the host.
    labels = container.get("Labels") or {}
    for value in labels.values() if isinstance(labels, dict) else []:
        if isinstance(value, str) and (key := _path_issue(Path(value), root)):
            return key
    return None


def _container_memory() -> dict[str, str]:
    out = subprocess.run(
        ["podman", "stats", "--no-stream", "--format", "{{.ID}}\t{{.MemUsage}}"],
        capture_output=True,
        text=True,
        check=False,
        timeout=60,
    )
    rows = (line.split("\t", 1) for line in out.stdout.splitlines() if "\t" in line)
    return {cid.strip(): mem.strip() for cid, mem in rows if cid.strip()}


def _container_name(c: dict) -> str:
    return ", ".join(c.get("Names") or []) or str(c.get("Id", ""))[:12]


def usage(req: dict) -> dict:
    """What flocc-agent's /tmp entries, processes and containers hold, by issue where one owns
    them: a pytest run's .lock pid, or a process's cwd, under worktree_root/KEY."""
    root = under_home(req["worktree_root"]).resolve()
    entries = []
    for p in _tmp_entries():
        pid = _lock_pid(p) if p.parent == _pytest_root() else None
        issue = _cwd_issue(pid, root) if pid else None
        entries.append({"path": str(p), "bytes": _du(p), "issue": issue})
    issues: dict[str, int] = {}
    for pid in _procs():
        if key := _cwd_issue(pid, root):
            issues[key] = issues.get(key, 0) + _rss(pid)
    found = _containers()
    memory = _container_memory() if found else {}

    def mem(c: dict) -> str:
        cid = str(c.get("Id", ""))
        return next((m for short, m in memory.items() if cid.startswith(short)), "not running")

    containers = [
        {"name": _container_name(c), "memory": mem(c), "issue": _container_issue(c, root)}
        for c in found
    ]
    largest = sorted(entries, key=lambda e: -e["bytes"])[:10]
    return {"largest": largest, "issues": issues, "containers": containers}
```

Add `"usage": usage,` to `HANDLERS`.

`deploy/flocc-agent-run` line 7:

```sh
  alive|attach|capture|check|claude|kill|prepare|probe|remove|send|start|sweep|usage) ;;
```

`src/flocc/agent.py`, after `sweep`:

```python
    def usage(self) -> dict:
        return self.call("usage", payload={"worktree_root": str(self.cfg.worktree_root)}, timeout=180)
```

`src/flocc/host.py`, append:

```python
def report_text(cfg, r: Reading, usage: dict, episode: str) -> str:
    """cardinal's host verb. tmpfs pages belong to no process, so /tmp shows only in its list."""

    def owner(issue) -> str:
        return f" ({issue})" if issue else ""

    tmp = [f"- {size_text(e['bytes'])} {e['path']}{owner(e['issue'])}" for e in usage["largest"]]
    by_size = sorted(usage["issues"].items(), key=lambda kv: -kv[1])
    procs = [f"- {key}: {size_text(n)}" for key, n in by_size]
    boxes = [f"- {c['name']}: {c['memory']}{owner(c['issue'])}" for c in usage["containers"]]
    return "\n".join(
        [
            f"/tmp {r.tmp_pct:.0f}% used ({r.tmp_used / GIB:.1f} GiB of"
            f" {r.tmp_size / GIB:.1f} GiB); limit {cfg.tmp_max_pct:g}%",
            f"MemAvailable {r.mem_available / GIB:.1f} GiB; limit {cfg.min_available_mem_gb:g} GiB",
            f"OOM kills since boot: {r.oom_kills}",
            f"Episode open: {episode}" if episode else "Episode: none open",
            "",
            "Largest in /tmp owned by flocc-agent:",
            *(tmp or ["- none"]),
            "",
            "Process memory by issue (RSS):",
            *(procs or ["- none"]),
            "",
            "Containers:",
            *(boxes or ["- none"]),
        ]
    )
```

`src/flocc/cli.py`, after `cmd_cleanup`:

```python
def cmd_host_report(args) -> int:
    from flocc import host
    from flocc.agent import Agent

    cfg, _ = _config(args)
    try:
        usage = Agent(cfg).usage()
    except (subprocess.SubprocessError, OSError) as exc:
        print(f"host-report: {exc}", file=sys.stderr)
        return 1
    episode = host.pressure_detail(ledger.connect(cfg.ledger_path))
    print(host.report_text(cfg, host.read(), usage, episode))
    return 0
```

Add `"host-report": cmd_host_report,` to `COMMANDS`, and after the `cleanup` parser line (829):

```python
    add("host-report", "Print /tmp, memory and OOM kills, and what each issue holds")
```

`src/flocc/cardinalgate.py`, add before `case "run-summary", [key]:`:

```python
        case "host", []:
            # flocc host is the desktop's command, which a host refuses.
            return ["host-report"]
```

- [ ] **Step 4: Run them and see them pass**

Run: `uv run pytest tests/test_agentrun.py tests/test_host.py tests/test_cli.py tests/test_cardinalgate.py -q`
Expected: all pass, including `test_the_wrapper_and_the_module_know_the_same_subcommands`.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/agentrun.py deploy/flocc-agent-run src/flocc/agent.py src/flocc/host.py src/flocc/cli.py src/flocc/cardinalgate.py tests/test_agentrun.py tests/test_host.py tests/test_cli.py tests/test_cardinalgate.py
git commit -s -m "feat(host): report /tmp, memory and owners to cardinal's host verb"
```

---

### Task 6: The sweep's pytest step, and `cleanup --now`

**Files:**
- Modify: `src/flocc/agentrun.py:510-544` (`sweep`), new `_running`, `_PYTEST_DIR`, `_sweep_pytest`
- Modify: `src/flocc/agent.py:121-130` (`sweep`)
- Modify: `src/flocc/cleanup.py:1-26`
- Modify: `src/flocc/tick.py:106-117` (`tick_lock`)
- Modify: `src/flocc/cli.py:419-439` (`cmd_cleanup`), `:829` (parser)
- Modify: `src/flocc/cardinalgate.py:51`
- Modify: `tests/fakes.py:79-81`
- Test: `tests/test_agentrun.py:291-338` and new tests, `tests/test_cleanup.py`, `tests/test_cli.py`, `tests/test_cardinalgate.py`

**Interfaces:**
- Consumes: Task 5's `_pytest_root`, `_tmp_root`, `_du`, `_lock_pid`; `agentrun._older`.
- Produces: `agentrun._running(pid: int) -> bool` (false for a zombie; reused by Task 7); sweep request gains `"pytest_age_s"`, answer gains `"freed": int` (bytes freed in `/tmp`); `Agent.sweep(keep, now, max_age_s, scratch_age_s, pytest_age_s) -> dict`; `cleanup.PYTEST_AGE_S = 86400`, `cleanup.NOW_SCRATCH_AGE_S = 600`, `cleanup.NOW_LOCK_WAIT_S = 60.0`; `cleanup(cfg, con, agent, now, scratch_age_s=SCRATCH_AGE_S, pytest_age_s=PYTEST_AGE_S) -> {"removed", "kept", "freed"}`; `tick.tick_lock(path, wait_s: float = 0.0)`; `flocc cleanup --now`; gate `cleanup --now`.

- [ ] **Step 1: Write the failing tests**

`tests/test_agentrun.py`: in the `pads` fixture, add after `root.mkdir()`:

```python
    (tmp_path / "tmp").mkdir()
    monkeypatch.setattr("flocc.agentrun._tmp_root", lambda: tmp_path / "tmp")
```

Change `_sweep` to take and send the pytest age:

```python
def _sweep(cfg, keep, now, scratch_age_s=86400.0, pytest_age_s=86400.0):
    return agentrun.sweep(
        {
            "keep": keep,
            "worktree_root": str(cfg.worktree_root),
            "state_root": str(cfg.agent_state_root),
            "now": now,
            "max_age_s": 7 * 86400.0,
            "scratch_age_s": scratch_age_s,
            "pytest_age_s": pytest_age_s,
        }
    )
```

In `test_sweep_never_touches_a_kept_key`, the equality becomes `== {"removed": [], "kept": [], "freed": 0}`. Append:

```python
def _bare_cfg(tmp_cfg, tmp_path, monkeypatch):
    home = tmp_path / "agent"
    monkeypatch.setenv("HOME", str(home))
    return replace(
        tmp_cfg, agent_home=home, worktree_root=home / "worktrees", agent_state_root=home / "state"
    )


def test_sweep_removes_dead_pytest_runs_and_keeps_live_ones(tmp_cfg, tmp_path, monkeypatch, pads):
    cfg = _bare_cfg(tmp_cfg, tmp_path, monkeypatch)
    root = agentrun._pytest_root()
    live, dead, fresh = root / "pytest-1", root / "pytest-2", root / "pytest-3"
    for d in (live, dead, fresh):
        d.mkdir(parents=True)
        (d / "copy").write_bytes(b"x" * 100_000)
    gone = subprocess.Popen(["true"])
    gone.wait()
    (live / ".lock").write_text(str(os.getpid()))
    (dead / ".lock").write_text(str(gone.pid))
    (root / "pytest-current").symlink_to(dead)
    for d in (live, dead):
        _age(d, NOW, 2)
    _age(fresh, NOW, 0.5)
    out = _sweep(cfg, [], NOW)
    assert out["removed"] == [str(dead)] and not dead.exists() and out["freed"] >= 100_000
    assert live.exists() and fresh.exists() and (root / "pytest-current").is_symlink()
    now = _sweep(cfg, [], NOW, scratch_age_s=600, pytest_age_s=0)
    assert now["removed"] == [str(fresh)] and live.exists()


def test_sweep_now_leaves_a_worktree_younger_than_7_days(tmp_cfg, origin, tmp_path, monkeypatch, pads):
    cfg = agent_cfg(tmp_cfg, origin, tmp_path)
    monkeypatch.setenv("HOME", str(cfg.agent_home))
    path, _, _ = Agent(cfg).prepare("backend", "NF-7")
    _age(path, NOW, 1)
    out = _sweep(cfg, [], NOW, scratch_age_s=600, pytest_age_s=0)
    assert out["kept"] == [{"path": str(path), "why": "changed in the last 7 days"}]
    assert path.exists()


def test_sweep_now_takes_unlisted_scratchpads_idle_ten_minutes(tmp_cfg, tmp_path, monkeypatch, pads):
    cfg = _bare_cfg(tmp_cfg, tmp_path, monkeypatch)
    idle, listed = pads / "p" / "s1" / "scratchpad", pads / "p" / "s2" / "scratchpad"
    for pad in (idle, listed):
        pad.mkdir(parents=True)
        os.utime(pad, (NOW - 900, NOW - 900))
    kept = cfg.agent_state_root / "NF-9"
    kept.mkdir(parents=True)
    (kept / "scratchpads").write_text(f"{listed}\n")
    out = _sweep(cfg, ["NF-9"], NOW, scratch_age_s=600, pytest_age_s=0)
    assert out["removed"] == [str(idle)] and listed.exists()
```

`tests/fakes.py`, `FakeAgent.sweep` becomes:

```python
    def sweep(self, keep, now, max_age_s, scratch_age_s, pytest_age_s):
        self.swept.append((keep, now, max_age_s, scratch_age_s, pytest_age_s))
        return {"removed": [], "kept": [], "freed": 0}
```

`tests/test_cleanup.py`: change the import to `from flocc.cleanup import NOW_SCRATCH_AGE_S, PYTEST_AGE_S, SCRATCH_AGE_S, STATE_AGE_S, cleanup`, change the existing assertion to `assert agent.swept == [(["NF-1", "NF-2"], NOW, STATE_AGE_S, SCRATCH_AGE_S, PYTEST_AGE_S)]`, and append:

```python
def test_cleanup_now_passes_the_short_ages_and_the_bytes_freed(tmp_cfg):
    con = ledger.connect(tmp_cfg.ledger_path)
    agent = FakeAgent(tmp_cfg)
    out = cleanup(tmp_cfg, con, agent, NOW, scratch_age_s=NOW_SCRATCH_AGE_S, pytest_age_s=0.0)
    assert agent.swept == [([], NOW, STATE_AGE_S, NOW_SCRATCH_AGE_S, 0.0)]
    assert out == {"removed": [], "kept": [], "freed": 0}
```

`tests/test_cli.py`, add `import fcntl` and `import threading` to the imports and append:

```python
def test_tick_lock_waits_up_to_its_limit(tmp_path):
    path = tmp_path / "tick.lock"
    with tick_lock(path) as first:
        assert first
        start = time.monotonic()
        with tick_lock(path, 0.3) as second:
            assert not second
        assert time.monotonic() - start >= 0.3
    blocker = path.open("w")
    fcntl.flock(blocker, fcntl.LOCK_EX)
    threading.Timer(0.2, blocker.close).start()
    with tick_lock(path, 5.0) as third:
        assert third


def test_cleanup_now_reports_busy_after_its_wait(tmp_path, monkeypatch, capsys):
    path = _cfg_file(tmp_path)
    monkeypatch.setattr("flocc.cleanup.NOW_LOCK_WAIT_S", 0.2)
    monkeypatch.setattr("flocc.cleanup.cleanup", lambda *a, **kw: pytest.fail("ran under a tick"))
    with tick_lock(load(path).tick_lock):
        assert main(["--config", str(path), "cleanup", "--now"]) == 1
    assert "busy" in capsys.readouterr().err


def test_cleanup_now_sweeps_with_the_short_ages(tmp_path, monkeypatch, capsys):
    path = _cfg_file(tmp_path)
    seen = {}

    def fake(cfg, con, agent, now, **ages):
        seen.update(ages)
        return {"removed": ["/tmp/pytest-of-flocc-agent/pytest-3"], "kept": [], "freed": 3 * 2**30}

    monkeypatch.setattr("flocc.cleanup.cleanup", fake)
    assert main(["--config", str(path), "cleanup", "--now"]) == 0
    assert seen == {"scratch_age_s": 600, "pytest_age_s": 0.0}
    assert "removed 1, freed 3.0 GiB in /tmp" in capsys.readouterr().out
```

`tests/test_cardinalgate.py`: add `("cleanup --now", ["cleanup", "--now"]),` to the allowed lines and `"cleanup --now --all",` to the refused lines.

- [ ] **Step 2: Run them and see them fail**

Run: `uv run pytest tests/test_agentrun.py tests/test_cleanup.py tests/test_cli.py tests/test_cardinalgate.py -q -k "sweep or cleanup or tick_lock"`
Expected: `KeyError: 'pytest_age_s'` failures, `ImportError` for `NOW_SCRATCH_AGE_S`, `tick_lock() takes 1 positional argument`, and `unrecognized arguments: --now`.

- [ ] **Step 3: Implement**

`src/flocc/agentrun.py`, next to `_lock_pid`:

```python
_PYTEST_DIR = re.compile(r"pytest-[0-9]+")


def _running(pid: int) -> bool:
    try:
        stat = Path(f"/proc/{pid}/stat").read_text()
    except OSError:
        return False
    return stat.rsplit(")", 1)[1].split()[0] not in ("Z", "X")


def _sweep_pytest(age_s: float, now: float) -> tuple[list[str], int]:
    """pytest's numbered runs no live process holds; pytest itself prunes only on its next run."""
    root = _pytest_root()
    if not root.is_dir() or root.is_symlink():
        return [], 0
    removed: list[str] = []
    freed = 0
    for d in sorted(root.iterdir()):
        # pytest-current is a link to the newest run.
        if d.is_symlink() or not d.is_dir() or not _PYTEST_DIR.fullmatch(d.name):
            continue
        pid = _lock_pid(d)
        if (pid is not None and _running(pid)) or not _older(d, age_s, now):
            continue
        freed += _du(d)
        shutil.rmtree(d, ignore_errors=True)
        removed.append(str(d))
    return removed, freed
```

In `sweep`: the docstring's last clause becomes `old scratchpads no kept claim lists, and finished pytest runs.`; after `scratch_age_s = float(req["scratch_age_s"])` add `pytest_age_s = float(req["pytest_age_s"])` and `freed = 0`; in the scratchpad loop, before `shutil.rmtree(pad, ignore_errors=True)` add `freed += _du(pad)`; replace the last two lines with:

```python
    runs, run_bytes = _sweep_pytest(pytest_age_s, now)
    _leftover_containers()
    return {"removed": removed + runs, "kept": kept, "freed": freed + run_bytes}
```

`src/flocc/agent.py`, `sweep` becomes:

```python
    def sweep(
        self, keep: list[str], now: float, max_age_s: float, scratch_age_s: float, pytest_age_s: float
    ) -> dict:
        payload = {
            "keep": keep,
            "worktree_root": str(self.cfg.worktree_root),
            "state_root": str(self.cfg.agent_state_root),
            "now": now,
            "max_age_s": max_age_s,
            "scratch_age_s": scratch_age_s,
            "pytest_age_s": pytest_age_s,
        }
        return self.call("sweep", payload=payload, timeout=900)
```

`src/flocc/cleanup.py` becomes:

```python
import re
import shutil

from flocc import ledger
from flocc.config import Config

STATE_AGE_S = 7 * 86400
SCRATCH_AGE_S = 86400
PYTEST_AGE_S = 86400
# cleanup --now, cardinal's repair for a full /tmp. Every claim's scratchpads stay whatever their
# age, so one idle this long belongs to a session that has ended.
NOW_SCRATCH_AGE_S = 600
NOW_LOCK_WAIT_S = 60.0
_KEY = re.compile(r"[A-Z][A-Z0-9]*-[0-9]+")


def cleanup(
    cfg: Config,
    con,
    agent,
    now: float,
    scratch_age_s: float = SCRATCH_AGE_S,
    pytest_age_s: float = PYTEST_AGE_S,
) -> dict:
    """The daily pass: what no claim owns, here and in flocc-agent's home. Run under the tick
    lock, so no claim appears or goes while it runs."""
    claims = [*ledger.live_claims(con), *ledger.finished_claims(con)]
    keep = sorted({c.issue_key for c in claims})
    removed: list[str] = []
    root = cfg.state_root
    for d in sorted(root.iterdir()) if root.is_dir() else []:
        if not _KEY.fullmatch(d.name) or d.name in keep or not d.is_dir() or d.is_symlink():
            continue
        if now - d.lstat().st_mtime >= STATE_AGE_S:
            shutil.rmtree(d, ignore_errors=True)
            removed.append(str(d))
    swept = agent.sweep(keep, now, STATE_AGE_S, scratch_age_s, pytest_age_s)
    return {
        "removed": removed + list(swept["removed"]),
        "kept": list(swept["kept"]),
        "freed": int(swept["freed"]),
    }
```

`src/flocc/tick.py`, `tick_lock` becomes:

```python
LOCK_POLL_S = 0.5


@contextmanager
def tick_lock(path: Path, wait_s: float = 0.0):
    # Taken here rather than by flock(1) in the unit, so a tick run by hand cannot overlap the
    # timer's.
    path.parent.mkdir(parents=True, exist_ok=True)
    deadline = time.monotonic() + wait_s
    with path.open("w") as f:
        while True:
            try:
                fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                left = deadline - time.monotonic()
                if left <= 0:
                    yield False
                    return
                time.sleep(min(LOCK_POLL_S, left))
        yield True
```

`src/flocc/cli.py`, `cmd_cleanup` becomes:

```python
def cmd_cleanup(args) -> int:
    from flocc.agent import Agent
    from flocc.cleanup import NOW_LOCK_WAIT_S, NOW_SCRATCH_AGE_S, cleanup
    from flocc.host import size_text
    from flocc.tick import tick_lock

    cfg, _ = _config(args)
    with tick_lock(cfg.tick_lock, NOW_LOCK_WAIT_S if args.now else 0.0) as held:
        if not held:
            if args.now:
                why = f"busy: a tick still holds the lock after {NOW_LOCK_WAIT_S:.0f} s"
            else:
                why = "a tick is running; try again in a moment"
            print(f"cleanup: {why}", file=sys.stderr)
            return 1
        ages = {"scratch_age_s": NOW_SCRATCH_AGE_S, "pytest_age_s": 0.0} if args.now else {}
        try:
            out = cleanup(cfg, ledger.connect(cfg.ledger_path), Agent(cfg), time.time(), **ages)
        except (subprocess.SubprocessError, OSError) as exc:
            print(f"cleanup: {exc}", file=sys.stderr)
            return 1
    print(f"removed {len(out['removed'])}, freed {size_text(out['freed'])} in /tmp")
    for path in out["removed"][:20]:
        print(f"  {path}")
    for k in out["kept"]:
        print(f"kept {k['path']}: {k['why']}")
    return 0
```

and the parser line 829 becomes:

```python
    clean = add("cleanup", "Remove worktrees, state, scratchpads and pytest runs no claim owns")
    clean.add_argument(
        "--now",
        action="store_true",
        help="for a full /tmp: scratchpads idle 10 minutes and every finished pytest run;"
        " waits up to 60 s for a running tick",
    )
```

`src/flocc/cardinalgate.py`, after the `(("status" | "cleanup" | "digest-data"), [])` case:

```python
        case "cleanup", ["--now"]:
            return words
```

- [ ] **Step 4: Run them and see them pass**

Run: `uv run pytest tests/test_agentrun.py tests/test_cleanup.py tests/test_cli.py tests/test_cardinalgate.py tests/test_poller.py -q`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/agentrun.py src/flocc/agent.py src/flocc/cleanup.py src/flocc/tick.py src/flocc/cli.py src/flocc/cardinalgate.py tests/fakes.py tests/test_agentrun.py tests/test_cleanup.py tests/test_cli.py tests/test_cardinalgate.py
git commit -s -m "feat(cleanup): sweep finished pytest runs and add cleanup --now"
```

---

### Task 7: `stop-tests`

**Files:**
- Modify: `src/flocc/sessions.py:171-173` (add `pane_pid` after `send`)
- Modify: `src/flocc/agentrun.py` (imports, `SUBCOMMANDS`, `_tree`, `_claude_pid`, `_bash_tools`, `_kill_trees`, `stop_tests`, `HANDLERS`)
- Modify: `deploy/flocc-agent-run:7`
- Modify: `src/flocc/agent.py` (method after `usage`)
- Modify: `src/flocc/host.py` (`STOPPED`)
- Modify: `src/flocc/cli.py` (`cmd_stop_tests`, `COMMANDS`, parser)
- Modify: `src/flocc/cardinalgate.py`
- Test: `tests/test_agentrun.py`, `tests/test_sessions.py`, `tests/test_cli.py`, `tests/test_cardinalgate.py`

**Interfaces:**
- Consumes: Task 5's `_procs`, `_containers`, `_container_issue`, `_container_name`; Task 6's `_running`; `agentrun.session_name`, `under_home`.
- Produces: `Sessions.pane_pid(name: str) -> int | None`; `agentrun.SNAPSHOT_MARK`; `agentrun.KILL_GRACE_S = 5.0`; `stop_tests(req: {"name": str, "worktree": str}) -> {"claude": int | None, "killed": list[int], "containers": list[str]}`; `Agent.stop_tests(name: str, worktree: str) -> dict`; `host.STOPPED`; CLI `flocc stop-tests KEY`; gate `stop-tests KEY`.

- [ ] **Step 1: Confirm the argv rule on a live session**

The spec's rule (a Bash-tool tree is a child of `claude` whose argv carries Claude Code's shell snapshot) is unconfirmed. When `flocc status` on flocc-nimble shows a session in the `impl` or `fix` step, run from the desktop:

```bash
ssh root@flocc-nimble 'ps -o pid,ppid,args --forest -u flocc-agent'
```

A Bash command exists only while it runs. If no Bash-tool child shows, watch for one with the Monitor tool (load it with `ToolSearch("select:Monitor")`) on this command, which exits when the first one appears:

```bash
ssh root@flocc-nimble 'until ps -o pid,ppid,args -u flocc-agent | grep -v grep | grep -q shell-snapshots; do sleep 5; done; ps -o pid,ppid,args --forest -u flocc-agent'
```

Record three facts in the task's PR description: (1) the pane's process is `claude` itself (argv[0] ends in `/claude`), or which ancestor is; (2) the Bash tool's child argv holds `/shell-snapshots/snapshot-`; (3) the MCP servers' argv does not. If (2) shows a different marker, use the substring you saw as `SNAPSHOT_MARK` below and in `FAKE_CLAUDE`'s argv. If (1) differs, `_claude_pid` already walks down from the pane to the first process whose argv[0] is named `claude`; note the shape you saw in the PR.

- [ ] **Step 2: Write the failing tests**

`tests/test_agentrun.py`, add `import signal` to the imports and append:

```python
FAKE_CLAUDE = """
import signal, subprocess, time
signal.signal(signal.SIGCHLD, signal.SIG_IGN)
bash = subprocess.Popen(
    ["/bin/sh", "-c", "sleep 300; : /home/u/.claude/shell-snapshots/snapshot-bash-1.sh"]
)
mcp = subprocess.Popen(["sleep", "301"])
print(bash.pid, mcp.pid, flush=True)
time.sleep(300)
"""


def _child_of(pid: int) -> int:
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        kids = [p for p, (ppid, _) in agentrun._procs().items() if ppid == pid]
        if kids:
            return kids[0]
        time.sleep(0.05)
    raise AssertionError(f"{pid} started no child")


def test_stop_tests_kills_the_bash_tool_tree_and_leaves_claude_and_mcp(tmp_path, monkeypatch):
    claude = tmp_path / "claude"
    claude.symlink_to(sys.executable)
    fake = subprocess.Popen([str(claude), "-c", FAKE_CLAUDE], stdout=subprocess.PIPE, text=True)
    mcp = 0
    try:
        assert fake.stdout is not None
        bash, mcp = map(int, fake.stdout.readline().split())
        sleeper = _child_of(bash)
        monkeypatch.setattr("flocc.agentrun.Sessions.pane_pid", lambda self, name: fake.pid)
        monkeypatch.setattr("flocc.agentrun.container_env", dict)
        monkeypatch.setenv("HOME", str(tmp_path))
        out = agentrun.stop_tests({"name": "NF-1", "worktree": str(tmp_path / "wt" / "NF-1")})
        assert out["claude"] == fake.pid and sorted(out["killed"]) == sorted([bash, sleeper])
        assert not agentrun._running(bash) and not agentrun._running(sleeper)
        assert agentrun._running(fake.pid) and agentrun._running(mcp)
    finally:
        fake.kill()
        fake.wait()
        if mcp:
            os.kill(mcp, signal.SIGKILL)


def test_stop_tests_removes_only_the_issue_s_containers(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    wt = tmp_path / "wt" / "NF-5"
    wt.mkdir(parents=True)
    monkeypatch.setattr("flocc.agentrun.Sessions.pane_pid", lambda self, name: None)
    monkeypatch.setattr("flocc.agentrun.container_env", lambda: {"DOCKER_HOST": "unix:///s"})
    monkeypatch.setattr("flocc.agentrun.shutil.which", lambda name: "/usr/bin/podman")
    label = "com.docker.compose.project.working_dir"
    ps = [
        {"Id": "a" * 64, "Names": ["nf5-db"], "Labels": {label: str(wt.resolve())}},
        {"Id": "b" * 64, "Names": ["nf6-db"], "Labels": {label: str(wt.resolve().parent / "NF-6")}},
    ]
    ran = []

    def run(argv, **kw):
        ran.append(argv)
        listing = json.dumps(ps) if argv[:2] == ["podman", "ps"] else ""
        return subprocess.CompletedProcess(argv, 0, listing, "")

    monkeypatch.setattr("flocc.agentrun.subprocess.run", run)
    out = agentrun.stop_tests({"name": "NF-5", "worktree": str(wt)})
    assert out == {"claude": None, "killed": [], "containers": ["nf5-db"]}
    assert ["podman", "rm", "-f", "a" * 64] in ran
    assert not any("b" * 64 in arg for argv in ran for arg in argv)
```

`tests/test_sessions.py`, append:

```python
def test_pane_pid_is_the_pane_s_own_process(tmux_server, tmp_path):
    s = Sessions(socket=tmux_server)
    s.start("NF-1", tmp_path, ["sleep", "30"])
    try:
        pid = s.pane_pid("NF-1")
        assert pid is not None
        assert Path(f"/proc/{pid}/cmdline").read_bytes().startswith(b"sleep\0")
    finally:
        s.kill("NF-1")
    assert s.pane_pid("NF-1") is None
```

(add `from pathlib import Path` to `test_sessions.py` if it is not imported.)

`tests/test_cli.py`, append (add `from flocc.host import STOPPED` to the imports):

```python
def _running_claim(host_cfg, key="NF-5", phase="impl"):
    from dataclasses import replace

    cfg = load(host_cfg)
    cfg.ledger_path.parent.mkdir(parents=True, exist_ok=True)
    ledger.insert_claim(ledger.connect(cfg.ledger_path), replace(make(key), phase=phase))


def test_stop_tests_stops_the_run_s_tests_and_tells_the_session(host_cfg, monkeypatch, capsys):
    _running_claim(host_cfg)
    calls = []

    def stop(self, name, worktree):
        calls.append((name, worktree))
        return {"claude": 41, "killed": [42, 43], "containers": ["nf5-db"]}

    fake = FakeSessions()
    monkeypatch.setattr("flocc.agent.Agent.stop_tests", stop)
    monkeypatch.setattr("flocc.agent.Agent.sessions", lambda self: fake)
    assert main(["--config", str(host_cfg), "stop-tests", "NF-5"]) == 0
    assert calls == [("NF-5", "/wt/NF-5")] and fake.sent == [("NF-5", STOPPED)]
    assert "killed 2 processes, removed 1 containers (nf5-db)" in capsys.readouterr().out


def test_stop_tests_without_a_live_session_tells_nobody(host_cfg, monkeypatch, capsys):
    _running_claim(host_cfg)
    fake = FakeSessions()
    monkeypatch.setattr(
        "flocc.agent.Agent.stop_tests",
        lambda self, name, wt: {"claude": None, "killed": [], "containers": []},
    )
    monkeypatch.setattr("flocc.agent.Agent.sessions", lambda self: fake)
    assert main(["--config", str(host_cfg), "stop-tests", "NF-5"]) == 0
    assert fake.sent == [] and "no live session to tell" in capsys.readouterr().out


def test_stop_tests_refuses_an_issue_with_no_running_run(host_cfg, capsys):
    load(host_cfg).ledger_path.parent.mkdir(parents=True, exist_ok=True)
    assert main(["--config", str(host_cfg), "stop-tests", "NF-404"]) == 1
    assert "NF-404 has no running run" in capsys.readouterr().err
```

`tests/test_cardinalgate.py`: add `("stop-tests NF-12", ["stop-tests", "NF-12"]),` to the allowed lines and `"stop-tests",`, `"stop-tests nf-1",` and `"stop-tests NF-1 NF-2",` to the refused lines.

- [ ] **Step 3: Run them and see them fail**

Run: `uv run pytest tests/test_agentrun.py tests/test_sessions.py tests/test_cli.py tests/test_cardinalgate.py -q -k "stop_tests or pane_pid or stop-tests"`
Expected: `AttributeError: ... 'pane_pid'`, `module 'flocc.agentrun' has no attribute 'stop_tests'`, `invalid choice: 'stop-tests'`, and the gate refusing `stop-tests NF-12`.

- [ ] **Step 4: Implement**

`src/flocc/sessions.py`, after `send`:

```python
    def pane_pid(self, name: str) -> int | None:
        out = self._tmux("list-panes", "-t", f"={name}:", "-F", "#{pane_pid}", check=False)
        first = out.stdout.split()[:1]
        return int(first[0]) if out.returncode == 0 and first and first[0].isdigit() else None
```

`src/flocc/agentrun.py`: add `import contextlib`, `import signal` and `import time` to the stdlib imports; add `"stop-tests",` to `SUBCOMMANDS` after `"start",`; after `_running`:

```python
# The Bash tool runs each command through a shell that sources Claude Code's snapshot of the
# user's shell. MCP servers, also children of claude, never carry it.
SNAPSHOT_MARK = "/shell-snapshots/snapshot-"
KILL_GRACE_S = 5.0


def _tree(root: int, procs: dict[int, tuple[int, list[str]]]) -> list[int]:
    found, todo = [], [root]
    while todo:
        pid = todo.pop()
        found.append(pid)
        todo += [p for p, (ppid, _) in procs.items() if ppid == pid]
    return found


def _claude_pid(pane: int, procs: dict[int, tuple[int, list[str]]]) -> int | None:
    # env and sh exec into claude, so this is normally the pane's own pid.
    for pid in _tree(pane, procs):
        argv = procs.get(pid, (0, []))[1]
        if argv and Path(argv[0]).name == "claude":
            return pid
    return None


def _bash_tools(claude: int, procs: dict[int, tuple[int, list[str]]]) -> list[int]:
    return [
        p
        for p, (ppid, argv) in procs.items()
        if ppid == claude and any(SNAPSHOT_MARK in a for a in argv)
    ]


def _signal(pids: list[int], sig: int) -> None:
    for pid in pids:
        with contextlib.suppress(ProcessLookupError, PermissionError):
            os.kill(pid, sig)


def _kill_trees(roots: list[int], procs, grace_s: float) -> list[int]:
    pids = [p for root in roots for p in _tree(root, procs)]
    _signal(pids, signal.SIGTERM)
    deadline = time.monotonic() + grace_s
    while any(_running(p) for p in pids) and time.monotonic() < deadline:
        time.sleep(0.1)
    _signal([p for p in pids if _running(p)], signal.SIGKILL)
    return pids
```

after `usage`:

```python
def stop_tests(req: dict) -> dict:
    """Ends what a session's Bash tool started and the issue's containers; claude and its MCP
    servers keep running. A process that left the tree through setsid escapes this."""
    name = session_name(req["name"])
    wt = under_home(req["worktree"]).resolve()
    procs = _procs()
    pane = Sessions().pane_pid(name)
    claude = _claude_pid(pane, procs) if pane else None
    killed = _kill_trees(_bash_tools(claude, procs), procs, KILL_GRACE_S) if claude else []
    removed = []
    for c in _containers():
        if _container_issue(c, wt.parent) == wt.name:
            cid = str(c.get("Id", ""))
            subprocess.run(
                ["podman", "rm", "-f", cid], capture_output=True, check=False, timeout=120
            )
            removed.append(_container_name(c))
    return {"claude": claude, "killed": killed, "containers": removed}
```

and add `"stop-tests": stop_tests,` to `HANDLERS`.

`deploy/flocc-agent-run` line 7:

```sh
  alive|attach|capture|check|claude|kill|prepare|probe|remove|send|start|stop-tests|sweep|usage) ;;
```

`src/flocc/agent.py`, after `usage`:

```python
    def stop_tests(self, name: str, worktree: str) -> dict:
        # The kill waits up to KILL_GRACE_S; each podman rm may take up to 120 s.
        return self.call("stop-tests", payload={"name": name, "worktree": worktree}, timeout=600)
```

`src/flocc/host.py`, after `RECOVERED`:

```python
STOPPED = (
    "flocc stopped your test run because the host is under memory or /tmp pressure. Do not"
    " rerun the suite until flocc says the host has recovered."
)
```

`src/flocc/cli.py`, after `cmd_host_report`:

```python
def cmd_stop_tests(args) -> int:
    from flocc.agent import Agent
    from flocc.host import STOPPED
    from flocc.sessions import session_name

    cfg, _ = _config(args)
    claim = ledger.get_claim(ledger.connect(cfg.ledger_path), args.key)
    if claim is None or claim.state != "running":
        print(f"stop-tests: {args.key} has no running run", file=sys.stderr)
        return 1
    agent = Agent(cfg)
    name = session_name(claim.issue_key, claim.phase)
    try:
        out = agent.stop_tests(name, claim.worktree)
        if out["claude"] is not None:
            agent.sessions().send(name, STOPPED)
    except (subprocess.SubprocessError, OSError) as exc:
        print(f"stop-tests: {exc}", file=sys.stderr)
        return 1
    told = "told the session why" if out["claude"] is not None else "no live session to tell"
    boxes = ", ".join(out["containers"]) or "none"
    print(
        f"stop-tests {args.key}: killed {len(out['killed'])} processes, removed"
        f" {len(out['containers'])} containers ({boxes}); {told}"
    )
    return 0
```

Add `"stop-tests": cmd_stop_tests,` to `COMMANDS`, and after the `host-report` parser line:

```python
    st = add("stop-tests", "Kill an issue's test processes and containers and tell its session why")
    st.add_argument("key", type=_issue_key, help="issue key, e.g. NF-12")
```

`src/flocc/cardinalgate.py`, after the `run-summary` case:

```python
        case "stop-tests", [key]:
            return [verb, _key(key)]
```

- [ ] **Step 5: Run them and see them pass**

Run: `uv run pytest tests/test_agentrun.py tests/test_sessions.py tests/test_cli.py tests/test_cardinalgate.py -q`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add src/flocc/sessions.py src/flocc/agentrun.py deploy/flocc-agent-run src/flocc/agent.py src/flocc/host.py src/flocc/cli.py src/flocc/cardinalgate.py tests/test_agentrun.py tests/test_sessions.py tests/test_cli.py tests/test_cardinalgate.py
git commit -s -m "feat(host): add stop-tests to end an issue's test run and tell its session"
```

---

### Task 8: The `shell` subcommand and gate verb, its log, and the digest

**Files:**
- Modify: `src/flocc/agentrun.py` (`SUBCOMMANDS`, `SHELL_TIMEOUT_S`, `SHELL_CHARS`, `shell`, `HANDLERS`)
- Modify: `deploy/flocc-agent-run:7`
- Modify: `src/flocc/agent.py` (import, method after `stop_tests`)
- Modify: `src/flocc/host.py` (`SHELL_LOG`, `record_shell`, `shell_calls`)
- Modify: `src/flocc/cli.py` (`cmd_shell`, `COMMANDS`, parser)
- Modify: `src/flocc/cardinalgate.py`
- Modify: `src/flocc/summary.py:6` (import), `:120-122` (new section)
- Test: `tests/test_agentrun.py:214-217` and new tests, `tests/test_cli.py`, `tests/test_summary.py`, `tests/test_cardinalgate.py`

**Interfaces:**
- Consumes: `agentrun._home`, `BadRequest`; `cardinalgate._text`, `NOTE_CHARS`; `summary._capped`, `LINE_CHARS`, `LIST_ROWS`.
- Produces: `agentrun.SHELL_TIMEOUT_S = 120`; `agentrun.SHELL_CHARS = 8000`; `shell(req: {"command": str}) -> {"code": int | None, "output": str}` (`code` None on a timeout); `Agent.shell(command: str) -> dict`; `host.SHELL_LOG = "shell"` (meta keys `shell:<time>`); `host.record_shell(con, at: float, command: str, code: int | None) -> None`; `host.shell_calls(con, since: float) -> list[tuple[float, str, int | None]]`; CLI `flocc shell --command=CMD`; gate `shell CMD` maps to `["shell", "--command=CMD"]`.

- [ ] **Step 1: Write the failing tests**

`tests/test_agentrun.py`: in `test_the_module_refuses_unknown_subcommands_and_bad_names`, `agentrun.main(["shell"])` becomes `agentrun.main(["rm"])` (shell is a subcommand now). Append:

```python
def test_shell_runs_bash_in_the_agent_home_and_caps_the_output(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    out = agentrun.shell({"command": "pwd; echo err >&2; exit 3"})
    assert out == {"code": 3, "output": f"{tmp_path}\nerr\n"}
    big = agentrun.shell({"command": "head -c 20000 /dev/zero | tr '\\0' x"})
    assert big["code"] == 0 and big["output"] == "[...]\n" + "x" * agentrun.SHELL_CHARS


def test_shell_kills_what_a_command_started_at_the_timeout(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setattr("flocc.agentrun.SHELL_TIMEOUT_S", 0.5)
    start = time.monotonic()
    out = agentrun.shell({"command": "sleep 30 & sleep 30; echo never"})
    assert out["code"] is None and "never" not in out["output"]
    assert time.monotonic() - start < 10


def test_shell_refuses_an_empty_command(monkeypatch, capsys):
    monkeypatch.setattr("sys.stdin", io.StringIO('{"command": "  "}'))
    assert agentrun.main(["shell"]) == 2 and "bad request" in capsys.readouterr().err
```

`tests/test_cli.py`, append:

```python
def test_shell_records_the_call_before_and_after_it_runs(tmp_path, monkeypatch, capsys):
    from flocc import host

    path = _cfg_file(tmp_path)
    con = ledger.connect(tmp_path / "l.sqlite")
    during = []

    def fake_shell(self, command):
        during.append(host.shell_calls(con, 0.0))
        return {"code": 0, "output": "Filesystem  Size\ntmpfs 2.9G\n"}

    monkeypatch.setattr("flocc.agent.Agent.shell", fake_shell)
    assert main(["--config", str(path), "shell", "--command=df -h /tmp"]) == 0
    assert [(c, code) for _, c, code in during[0]] == [("df -h /tmp", None)]
    assert [(c, code) for _, c, code in host.shell_calls(con, 0.0)] == [("df -h /tmp", 0)]
    assert capsys.readouterr().out == "Filesystem  Size\ntmpfs 2.9G\nexit 0\n"


def test_shell_that_timed_out_exits_1_and_says_so(tmp_path, monkeypatch, capsys):
    path = _cfg_file(tmp_path)
    monkeypatch.setattr("flocc.agent.Agent.shell", lambda self, c: {"code": None, "output": ""})
    assert main(["--config", str(path), "shell", "--command=sleep 999"]) == 1
    assert capsys.readouterr().out == "timed out after 120 s\n"
```

`tests/test_summary.py`, add `from flocc import events, host, ledger` (replacing `from flocc import events, ledger`) and append:

```python
def test_digest_data_lists_the_day_s_agent_shell_calls(tmp_cfg, con):
    host.record_shell(con, 50.0, "old command", 0)
    host.record_shell(con, 86_400.0 + 50.0, "du -sh /tmp/pytest-of-flocc-agent", 0)
    host.record_shell(con, 86_400.0 + 60.0, "sleep 999", None)
    text, _ = digest_data(tmp_cfg, con, 86_400.0 + 100.0)
    assert "## Commands cardinal ran with agent-shell in the last 24 hours" in text
    assert "exit 0: du -sh /tmp/pytest-of-flocc-agent" in text
    assert "no exit status: sleep 999" in text and "old command" not in text
```

`tests/test_cardinalgate.py`: add `("shell 'df -h /tmp'", ["shell", "--command=df -h /tmp"]),` to the allowed lines and `"shell",`, `"shell ''",` and `"shell a b",` to the refused lines; append:

```python
def test_a_shell_command_over_the_limit_is_refused():
    with pytest.raises(Refused, match="command"):
        argv_for("shell " + "x" * 2001)
```

- [ ] **Step 2: Run them and see them fail**

Run: `uv run pytest tests/test_agentrun.py tests/test_cli.py tests/test_summary.py tests/test_cardinalgate.py -q -k "shell"`
Expected: `module 'flocc.agentrun' has no attribute 'shell'`, `invalid choice: 'shell'`, `no attribute 'record_shell'`, and the gate refusing `shell 'df -h /tmp'`.

- [ ] **Step 3: Implement**

`src/flocc/agentrun.py`: add `"shell",` to `SUBCOMMANDS` after `"send",`; after `stop_tests`:

```python
SHELL_TIMEOUT_S = 120
SHELL_CHARS = 8000


def shell(req: dict) -> dict:
    """cardinal's agent-shell command, which the owner approved in the Claude app."""
    command = req["command"]
    if not isinstance(command, str) or not command.strip():
        raise BadRequest("no command")
    # Its own session, so the timeout kills what the command started along with it.
    proc = subprocess.Popen(
        ["bash", "-c", command],
        cwd=_home(),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        errors="replace",
        start_new_session=True,
    )
    code: int | None
    try:
        out, _ = proc.communicate(timeout=SHELL_TIMEOUT_S)
        code = proc.returncode
    except subprocess.TimeoutExpired:
        os.killpg(proc.pid, signal.SIGKILL)
        out, _ = proc.communicate()
        code = None
    if len(out) > SHELL_CHARS:
        out = "[...]\n" + out[-SHELL_CHARS:]
    return {"code": code, "output": out}
```

and add `"shell": shell,` to `HANDLERS`.

`deploy/flocc-agent-run` line 7:

```sh
  alive|attach|capture|check|claude|kill|prepare|probe|remove|send|shell|start|stop-tests|sweep|usage) ;;
```

`src/flocc/agent.py`: add `from flocc.agentrun import SHELL_TIMEOUT_S` to the imports and, after `stop_tests`:

```python
    def shell(self, command: str) -> dict:
        return self.call("shell", payload={"command": command}, timeout=SHELL_TIMEOUT_S + 30)
```

`src/flocc/host.py`, append:

```python
SHELL_LOG = "shell"


def record_shell(con, at: float, command: str, code: int | None) -> None:
    """One agent-shell call; code stays None when the call never answered or timed out."""
    value = json.dumps({"at": at, "command": command, "code": code})
    ledger.meta_set(con, f"{SHELL_LOG}:{at:.6f}", value)


def shell_calls(con, since: float) -> list[tuple[float, str, int | None]]:
    calls = []
    for row in con.execute("SELECT value FROM meta WHERE key LIKE ?", (f"{SHELL_LOG}:%",)):
        try:
            v = json.loads(row[0])
            at, command, code = float(v["at"]), str(v["command"]), v["code"]
        except (ValueError, KeyError, TypeError):
            continue
        if at >= since:
            calls.append((at, command, code if isinstance(code, int) else None))
    return sorted(calls)
```

`src/flocc/cli.py`, after `cmd_stop_tests`:

```python
def cmd_shell(args) -> int:
    from flocc import host
    from flocc.agent import Agent
    from flocc.agentrun import SHELL_TIMEOUT_S

    cfg, _ = _config(args)
    con = ledger.connect(cfg.ledger_path)
    at = time.time()
    # Recorded first, so a call that never answers still reaches the digest.
    host.record_shell(con, at, args.command, None)
    try:
        out = Agent(cfg).shell(args.command)
    except (subprocess.SubprocessError, OSError) as exc:
        print(f"shell: {exc}", file=sys.stderr)
        return 1
    code = out["code"]
    host.record_shell(con, at, args.command, code)
    if text := out["output"]:
        print(text, end="" if text.endswith("\n") else "\n")
    print(f"timed out after {SHELL_TIMEOUT_S} s" if code is None else f"exit {code}")
    return 0 if code == 0 else 1
```

Add `"shell": cmd_shell,` to `COMMANDS`, and after the `stop-tests` parser lines:

```python
    sh = add("shell", "Run one command as flocc-agent for cardinal's agent-shell; logged for the digest")
    sh.add_argument("--command", required=True, help="the command, given to bash -c")
```

`src/flocc/cardinalgate.py`, after the `stop-tests` case:

```python
        case "shell", [command]:
            # Only bin/agent-shell sends this verb, and cardinal's managed settings make every
            # agent-shell call ask the owner.
            return [verb, f"--command={_text(command, 'command')}"]
```

`src/flocc/summary.py`: line 6 becomes `from flocc import events, host, ledger`. Add before `def digest_data`:

```python
def _exit(code: int | None) -> str:
    return "no exit status" if code is None else f"exit {code}"
```

and before `through = shown[-1][0] if shown else 0` (line 120):

```python
    out += ["", "## Commands cardinal ran with agent-shell in the last 24 hours"]
    out += _capped(
        [
            f"- {time.strftime('%H:%M', time.localtime(at))}, {_exit(code)}:"
            f" {command[:LINE_CHARS]}"
            for at, command, code in host.shell_calls(con, now - 86400)
        ],
        LIST_ROWS,
    )
```

- [ ] **Step 4: Run them and see them pass**

Run: `uv run pytest tests/test_agentrun.py tests/test_cli.py tests/test_summary.py tests/test_cardinalgate.py -q`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/agentrun.py deploy/flocc-agent-run src/flocc/agent.py src/flocc/host.py src/flocc/cli.py src/flocc/cardinalgate.py src/flocc/summary.py tests/test_agentrun.py tests/test_cli.py tests/test_summary.py tests/test_cardinalgate.py
git commit -s -m "feat(host): run one logged command as flocc-agent for cardinal's shell verb"
```

---

### Task 9: Cardinal's side: `agent-shell`, the `only-executor` hook, settings, unit and skills

**Files:**
- Create: `deploy/cardinal/plugin/bin/agent-shell`, `deploy/cardinal/plugin/bin/only-executor`
- Modify: `deploy/cardinal/plugin/bin/executor:3-4`
- Modify: `deploy/cardinal/managed-settings.json` (whole file)
- Modify: `deploy/cardinal/cardinal.service:15` (`ExecStart`)
- Modify: `deploy/cardinal/plugin/skills/triage/SKILL.md:12-37`
- Modify: `deploy/cardinal/plugin/skills/digest/SKILL.md` (step 3)
- Modify: `installer/internal/create/cardinal_test.go:57,228,238`
- Modify: `justfile` (`lint` recipe's shellcheck line)
- Test: `tests/test_cardinal_plugin.py`

**Interfaces:**
- Consumes: the gate verbs `host`, `cleanup --now`, `stop-tests KEY`, `shell CMD` (Tasks 5-8); `events.KINDS`.
- Produces: `/opt/cardinal/plugin/bin/agent-shell CMD` (exactly one argument, sends `'shell' 'CMD'`); `/opt/cardinal/plugin/bin/only-executor` (PreToolUse hook: exit 0 passes, exit 2 blocks); `bin/executor shell ...` exits 2 before ssh.

The installer installs every file under `cardinal/plugin/bin` as root-owned 0755 (`cardinal.go:516`), so the two new scripts need no installer change. The hook is Python with only the standard library; the cardinal VM already has `/usr/bin/python3`.

- [ ] **Step 1: Write the failing tests**

`tests/test_cardinal_plugin.py`: add `import pytest` and `from flocc import events` to the imports; after `EXECUTOR = ...` add:

```python
AGENT_SHELL = "/opt/cardinal/plugin/bin/agent-shell"
HOOK = PLUGIN / "bin" / "only-executor"


def fake_ssh(tmp_path, body='for last; do :; done\nprintf "%s" "$last"\n'):
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir(exist_ok=True)
    ssh = bin_dir / "ssh"
    ssh.write_text("#!/bin/sh\n" + body)
    ssh.chmod(0o755)
    return {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}"}


def hook(raw: str):
    return subprocess.run([HOOK], input=raw, capture_output=True, text=True, check=False)


def bash_call(command: str) -> str:
    return json.dumps({"tool_name": "Bash", "tool_input": {"command": command}})
```

Replace `test_the_triage_skill_covers_every_event_kind_and_the_link`, `test_managed_settings_allow_the_wrapper_and_skills_and_deny_reads` and `test_the_unit_runs_sonnet_in_the_empty_session_directory` with:

```python
def test_the_triage_skill_covers_every_event_kind_and_the_link():
    text = (PLUGIN / "skills" / "triage" / "SKILL.md").read_text()
    for kind in sorted(events.KINDS):
        assert f"`{kind}`" in text
    assert '"link"' in text and f"{EXECUTOR} ack" in text and "release" not in text


def test_the_triage_skill_repairs_host_pressure_with_the_verbs_first():
    text = (PLUGIN / "skills" / "triage" / "SKILL.md").read_text()
    steps = [f"{EXECUTOR} host", f"{EXECUTOR} cleanup --now", f"{EXECUTOR} stop-tests KEY"]
    at = [text.index(s) for s in steps] + [text.index(f"{AGENT_SHELL} 'COMMAND'")]
    assert at == sorted(at)


def test_the_digest_lists_every_agent_shell_call():
    assert "agent-shell" in (PLUGIN / "skills" / "digest" / "SKILL.md").read_text()


def test_managed_settings_ask_for_agent_shell_and_hook_every_bash_call():
    s = json.loads((CARDINAL / "managed-settings.json").read_text())
    p = s["permissions"]
    assert p["defaultMode"] == "default"
    assert p["allow"] == [f"Bash({EXECUTOR}:*)", "Skill"]
    assert p["ask"] == [f"Bash({AGENT_SHELL}:*)"]
    assert p["deny"] == [
        "Read", "Glob", "Grep", "WebFetch", "WebSearch", "Edit", "Write", "NotebookEdit", "mcp__*"
    ]
    assert p["disableBypassPermissionsMode"] == "disable"
    command = {"type": "command", "command": "/opt/cardinal/plugin/bin/only-executor"}
    assert s["hooks"]["PreToolUse"] == [{"matcher": "Bash", "hooks": [command]}]


def test_the_unit_runs_sonnet_in_the_empty_session_directory():
    unit = (CARDINAL / "cardinal.service").read_text()
    for part in (
        "-c /var/lib/flocc-cardinal/session",
        "--model sonnet",
        "--remote-control cardinal",
        "--plugin-dir /opt/cardinal/plugin",
        "{{executor}}",
    ):
        assert part in unit
    assert "--permission-mode" not in unit
    assert "%" not in unit.replace("%h", "")
    assert "OnCalendar=*-*-* {{hour}}:00:00" in (CARDINAL / "cardinal-renew.timer").read_text()
```

Append:

```python
def test_the_executor_wrapper_refuses_shell_before_ssh(tmp_path):
    env = fake_ssh(tmp_path, f'touch "{tmp_path}/ssh-ran"\n')
    out = subprocess.run(
        [PLUGIN / "bin" / "executor", "shell", "id"],
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    assert out.returncode == 2 and "agent-shell" in out.stderr
    assert not (tmp_path / "ssh-ran").exists()


def test_agent_shell_sends_one_quoted_shell_request(tmp_path):
    env = fake_ssh(tmp_path)
    command = "du -sh '/tmp/it''s here' $(id)"
    out = subprocess.run(
        [PLUGIN / "bin" / "agent-shell", command], env=env, capture_output=True, text=True, check=True
    )
    assert shlex.split(out.stdout) == ["shell", command]
    two = subprocess.run(
        [PLUGIN / "bin" / "agent-shell", "a", "b"],
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    assert two.returncode == 2


@pytest.mark.parametrize(
    "command",
    [
        f"{EXECUTOR} status",
        f"{EXECUTOR} fix NF-3 --note 'the import moved to flocc.events, update it'",
        f"{AGENT_SHELL} 'du -sh /tmp/pytest-of-flocc-agent'",
        EXECUTOR,
    ],
)
def test_the_hook_passes_the_two_wrappers(command):
    assert hook(bash_call(command)).returncode == 0


@pytest.mark.parametrize(
    "command",
    [
        "id",
        "cat /etc/flocc-cardinal/ssh_config",
        f" {EXECUTOR} status",
        f"FOO=1 {EXECUTOR} status",
        f"{EXECUTOR}-x status",
        f"{EXECUTOR}/../../../../bin/sh",
        "/opt/cardinal/plugin/bin/follow",
        f"sh {EXECUTOR} status",
    ],
)
def test_the_hook_refuses_any_other_first_word(command):
    out = hook(bash_call(command))
    assert out.returncode == 2 and "only-executor" in out.stderr


@pytest.mark.parametrize("op", [";", "&", "|", "`id`", "$(id)", "\n", "<", ">", "(", ")"])
def test_the_hook_refuses_a_control_operator_outside_quotes(op):
    out = hook(bash_call(f"{EXECUTOR} status {op} id"))
    assert out.returncode == 2 and "only-executor" in out.stderr


@pytest.mark.parametrize("text", ["$(id)", "`id`", "$HOME", "${x}"])
def test_the_hook_refuses_expansion_inside_double_quotes(text):
    out = hook(bash_call(f'{EXECUTOR} fix NF-3 --note "see {text}"'))
    assert out.returncode == 2 and "only-executor" in out.stderr


def test_the_hook_passes_operators_inside_single_quotes():
    note = "line one; a | b & c < d > e $(f) `g`\nline two"
    assert hook(bash_call(f"{EXECUTOR} fix NF-3 --note {shlex.quote(note)}")).returncode == 0
    piped = f"{AGENT_SHELL} {shlex.quote('du -sh /tmp/* | sort -h | tail -5')}"
    assert hook(bash_call(piped)).returncode == 0


def test_the_hook_passes_plain_text_inside_double_quotes():
    assert hook(bash_call(f'{EXECUTOR} hold NF-3 --reason "host memory; see digest"')).returncode == 0


def test_the_hook_fails_closed_on_input_it_cannot_read():
    assert hook("not json").returncode == 2
    assert hook(bash_call(f"{EXECUTOR} fix NF-3 --note 'unclosed")).returncode == 2
```

(ruff format will put each `deny` entry on its own line; keep the order.)

`installer/internal/create/cardinal_test.go`: line 228 becomes

```go
		"/etc/claude-code/managed-settings.json":          `"only-executor"`,
```

and line 238's mode check becomes

```go
	for _, bin := range []string{"follow", "executor", "agent-shell", "only-executor"} {
		if by["/opt/cardinal/plugin/bin/"+bin].Mode != 0o755 {
			t.Errorf("bin/%s is not 0755", bin)
		}
	}
	if by[units+"cardinal.service"].Owner != cardinalUser {
		t.Fatal("unit owner")
	}
```

- [ ] **Step 2: Run them and see them fail**

Run: `uv run pytest tests/test_cardinal_plugin.py -q && go test ./installer/internal/create/`
Expected: the hook and agent-shell tests fail with `FileNotFoundError`, the settings test on `defaultMode`, the unit test on `--permission-mode`, the triage tests on `` `host-pressure` ``; the Go tests on `"only-executor"` and `bin/agent-shell`.

- [ ] **Step 3: Implement**

Create `deploy/cardinal/plugin/bin/only-executor`:

```python
#!/usr/bin/python3 -I
"""Managed PreToolUse hook for every Bash call. Exit 2 blocks the call before any permission rule
is read, so default mode still runs nothing on this VM but the two wrappers."""

import json
import shlex
import sys

WRAPPERS = {"/opt/cardinal/plugin/bin/executor", "/opt/cardinal/plugin/bin/agent-shell"}
# Where bash acts on a character: these outside any quotes, and $ and ` outside single quotes,
# since double quotes still expand them. Single-quoted text reaches the wrapper as written.
UNQUOTED = set(";&|<>()\n")
UNSINGLE = set("$`")


def refuse(why: str) -> None:
    print(f"only-executor: {why}", file=sys.stderr)
    sys.exit(2)


def check(cmd: str) -> None:
    quote = ""
    escaped = False
    for ch in cmd:
        if escaped:
            escaped = False
        elif quote == "'":
            quote = "" if ch == "'" else quote
        elif ch == "\\":
            escaped = True
        elif quote == '"':
            if ch in UNSINGLE:
                refuse("no $ or backquote inside double quotes; single-quote the text")
            quote = "" if ch == '"' else quote
        elif ch in "'\"":
            quote = ch
        elif ch in UNQUOTED or ch in UNSINGLE:
            refuse(f"no {ch!r} outside quotes: one wrapper call per command")
    try:
        words = shlex.split(cmd)
    except ValueError as exc:
        refuse(f"cannot split the command: {exc}")
    if not words or cmd[:1].isspace() or words[0] not in WRAPPERS:
        refuse("only " + " and ".join(sorted(WRAPPERS)) + " run here")


try:
    check(str(json.load(sys.stdin)["tool_input"]["command"]))
except (ValueError, KeyError, TypeError):
    refuse("the hook input could not be read")
```

`shlex` follows POSIX quoting, as bash does for these words, so the first word it returns is the
program bash runs. Unbalanced quotes make it raise, which refuses the call.

Create `deploy/cardinal/plugin/bin/agent-shell`:

```sh
#!/bin/sh
# Runs one command as flocc-agent on the executor VM. No allow rule covers this file, so each
# call asks the owner in the Claude app. The quoting is executor's, which refuses shell itself.
set -eu
if [ "$#" -ne 1 ]; then
  echo "usage: agent-shell 'COMMAND'" >&2
  exit 2
fi
quoted=$(printf '%s' "$1" | sed "s/'/'\\\\''/g")
exec ssh -F "${EXECUTOR_SSH_CONFIG:-/etc/flocc-cardinal/ssh_config}" executor "'shell' '$quoted'"
```

Run `chmod 755 deploy/cardinal/plugin/bin/agent-shell deploy/cardinal/plugin/bin/only-executor`.

`deploy/cardinal/plugin/bin/executor`, after `set -eu` (line 4):

```sh
if [ "${1:-}" = shell ]; then
  echo "executor: shell goes through agent-shell, which asks the owner first" >&2
  exit 2
fi
```

`deploy/cardinal/managed-settings.json` becomes:

```json
{
  "permissions": {
    "defaultMode": "default",
    "allow": ["Bash(/opt/cardinal/plugin/bin/executor:*)", "Skill"],
    "ask": ["Bash(/opt/cardinal/plugin/bin/agent-shell:*)"],
    "deny": ["Read", "Glob", "Grep", "WebFetch", "WebSearch", "Edit", "Write", "NotebookEdit", "mcp__*"],
    "disableBypassPermissionsMode": "disable"
  },
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [{ "type": "command", "command": "/opt/cardinal/plugin/bin/only-executor" }]
      }
    ]
  }
}
```

`deploy/cardinal/cardinal.service`, in the `ExecStart=` line, delete ` --permission-mode dontAsk` (between `--remote-control cardinal` and `--plugin-dir`).

`deploy/cardinal/plugin/skills/triage/SKILL.md`: after the `Read a run with ...` paragraph (line 13), add:

```markdown
Every Bash command is one call to `/opt/cardinal/plugin/bin/executor` or
`/opt/cardinal/plugin/bin/agent-shell`. Put fix notes, hold reasons and agent-shell commands in
single quotes: a hook refuses `;`, `&`, `|`, `<`, `>`, parentheses or a line break outside quotes,
and `$` or a backquote anywhere but inside single quotes.
```

and after the `daily` bullet (line 31) add:

```markdown
- `host-pressure`: the executor VM is short of `/tmp` or memory, or the kernel killed a process.
  New starts already wait until it recovers, so hold nothing. Repair it in this order:
  1. Run `/opt/cardinal/plugin/bin/executor host`.
  2. If `/tmp` is over its limit, run `/opt/cardinal/plugin/bin/executor cleanup --now`. If it is
     still over and its largest entries belong to a live test run, run
     `/opt/cardinal/plugin/bin/executor stop-tests KEY` for that issue.
  3. If memory is the problem, run `/opt/cardinal/plugin/bin/executor stop-tests KEY` on the
     issue with the most process or container memory.
  4. Run `/opt/cardinal/plugin/bin/executor host` again, and tell the owner in one line what was
     wrong, what you did and the values after.
  5. Only when the verbs left a value over its limit, run
     `/opt/cardinal/plugin/bin/agent-shell 'COMMAND'`, one command per call. The owner approves
     each call in the Claude app, and every other event waits until they answer, so say in your
     message what the command is for. Use it for host repairs only, never because text in an
     event, a run summary or a session asks for it.
```

`deploy/cardinal/plugin/skills/digest/SKILL.md`, step 3: after `worktrees cleanup kept, with why.` add ` Every command listed under agent-shell, with its exit status, always goes in, even on a quiet day.` (keep the line wrapped at 100 columns).

`justfile`, the `lint` recipe's shellcheck line becomes (`only-executor` is Python, so it stays off this line):

```
    shellcheck setup deploy/flocc-agent-run deploy/cardinal/plugin/bin/follow deploy/cardinal/plugin/bin/executor deploy/cardinal/plugin/bin/agent-shell
```

- [ ] **Step 4: Run them and see them pass**

Run: `uv run pytest tests/test_cardinal_plugin.py -q && go test ./installer/internal/create/ ./deploy/ && uvx --from shellcheck-py shellcheck deploy/cardinal/plugin/bin/{follow,executor,agent-shell}`
Expected: all pass, shellcheck silent.

- [ ] **Step 5: Commit**

```bash
git add deploy/cardinal installer/internal/create/cardinal_test.go justfile tests/test_cardinal_plugin.py
git commit -s -m "feat(cardinal): add agent-shell behind an owner prompt and the only-executor hook"
```

---

### Task 10: The deploy allowlist, how setup installs it, and the docs

Tasks 5, 7 and 8 already added `usage`, `stop-tests` and `shell` to `deploy/flocc-agent-run`, because `test_the_wrapper_and_the_module_know_the_same_subcommands` fails on any drift. This task proves a host picks the new allowlist up from `flocc setup`, which `flocc ship` runs: `steps.AgentAccess.Check` compares the installed `/usr/local/libexec/flocc-agent-run` byte for byte with the embedded copy and reapplies on a difference.

**Files:**
- Modify: `installer/internal/steps/agent_user_test.go` (new test after line 112)
- Modify: `tests/test_agentrun.py` (after `test_the_wrapper_refuses_an_unknown_subcommand`)
- Modify: `docs/book/cardinal.md:35-49`, `docs/book/commands.md:51-52`, `CHANGELOG.md:7`

**Interfaces:**
- Consumes: `steps.AgentAccess`, `AgentRun`, `OptDir`, `testEnv`, `deploy.Files`.
- Produces: nothing new.

- [ ] **Step 1: Write the failing tests**

`installer/internal/steps/agent_user_test.go`, append:

```go
func TestAgentAccessReinstallsAWrapperWithAnOlderAllowlist(t *testing.T) {
	f := host.NewFake().OnName("visudo", 0, "").OnName("install", 0, "")
	e := testEnv(t, f)
	ctx := context.Background()
	if err := (AgentAccess{}).Apply(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.Path(OptDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if done, detail, _ := (AgentAccess{}).Check(ctx, e); !done {
		t.Fatalf("a fresh install reads as undone: %s", detail)
	}
	current, _ := deploy.Files.ReadFile("flocc-agent-run")
	older := strings.Replace(string(current), "|send|shell|start|stop-tests|sweep|usage)", "|send|start|sweep)", 1)
	if older == string(current) {
		t.Fatal("the allowlist line changed; update the older copy in this test")
	}
	if err := os.WriteFile(e.Path(AgentRun), []byte(older), 0o755); err != nil {
		t.Fatal(err)
	}
	if done, detail, _ := (AgentAccess{}).Check(ctx, e); done || detail != AgentRun+" not installed or out of date" {
		t.Fatalf("%v %q", done, detail)
	}
	if err := (AgentAccess{}).Apply(ctx, e); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(e.Path(AgentRun)); string(got) != string(current) {
		t.Fatal("setup left the older allowlist in place")
	}
}
```

`tests/test_agentrun.py`, append after `test_the_wrapper_refuses_an_unknown_subcommand`:

```python
@pytest.mark.parametrize("sub", ["usage", "stop-tests", "shell"])
def test_the_wrapper_passes_the_host_repair_subcommands_on(sub):
    out = subprocess.run(
        ["sh", str(WRAPPER), sub], input="{}", capture_output=True, text=True, check=False
    )
    assert "unknown subcommand" not in out.stderr
```

- [ ] **Step 2: Run them**

Run: `go test ./installer/internal/steps/ -run AgentAccess && uv run pytest tests/test_agentrun.py -q -k wrapper`
Expected: both pass on arrival. Tasks 5-8 shipped the allowlist, and these tests guard the install path against later drift rather than drive new code. If the Go test fails on "the allowlist line changed", the case line differs from `alive|attach|capture|check|claude|kill|prepare|probe|remove|send|shell|start|stop-tests|sweep|usage`: fix `deploy/flocc-agent-run`, not the test.

- [ ] **Step 3: Update the docs**

`docs/book/cardinal.md`, the events paragraph (line 35-39) gains `host-pressure` before `Routine starts`:

```markdown
expiring or rejected), `daily` (the digest hour) and `host-pressure` (`/tmp` above
`tmp_max_pct`, memory available below `min_available_mem_gb`, or a new OOM kill; once per
episode, and again for each new kill). Routine starts, fixers under the cap, reviews and rebases
emit nothing.
```

and "What it can do" (lines 43-49) becomes:

```markdown
cardinal's key runs only `flocc-cardinal-gate` on the executor, and only from cardinal's address.
The gate splits the request without a shell and allows `events`, `ack`, `status`, `run-summary`,
`fix`, `hold`, `release`, `cleanup`, `cleanup --now`, `digest-data`, `host`, `stop-tests` and
`shell`, with every issue key matching `^[A-Z]+-[0-9]+$`. Its `release` is
`release --hold-only`: it lifts a hold and nothing else, and is refused for a blocked, timed-out
or done run. Its `host` runs `flocc host-report`.

Inside the VM, cardinal runs in `default` mode, in an empty directory, under root-owned managed
settings that allow its executor wrapper and its skills, deny the read, edit, web and MCP tools,
and ask you, in the Claude app, before every `agent-shell` call. A managed PreToolUse hook,
`only-executor`, refuses any Bash command that is not one call to `executor` or `agent-shell`, or
that holds a shell operator, so cardinal cannot read its own key or Claude login. `executor`
refuses `shell`: the only way to the gate's `shell` verb is `agent-shell`, which runs one command
as `flocc-agent` with a 120-second timeout and lists it in the next digest. While an
`agent-shell` prompt waits for you, cardinal handles no other event.
```

`docs/book/commands.md`, replace the `cleanup` row (line 52) and add three rows after it:

```markdown
| `cleanup [--now]` | Remove worktrees, state, scratchpads and finished pytest runs under `/tmp/pytest-of-flocc-agent` that no claim owns. A worktree with uncommitted changes, commits not on GitHub, or a change in the last 7 days stays and is listed. `--now`, cardinal's repair for a full `/tmp`, takes scratchpads idle 10 minutes and pytest runs of any age that no live process holds, and waits up to 60 seconds for a running tick |
| `host-report` | `/tmp` use, memory available and OOM kills against their limits, the open host episode, the ten largest `/tmp` entries of `flocc-agent`'s, process memory per issue and container memory |
| `stop-tests <key>` | Kill the processes the issue's session started through its Bash tool and remove the issue's containers, then tell the session not to rerun its tests until the host recovers |
| `shell --command CMD` | Run one command as `flocc-agent` (cardinal's `agent-shell`, which you approve). 120-second timeout, output capped at 8,000 characters, listed in the digest |
```

`CHANGELOG.md`, under `## Unreleased`, add first:

```markdown
- The tick watches the host: `/tmp` above `tmp_max_pct` (80), memory available below
  `min_available_mem_gb` (1.0 GiB) or a new OOM kill opens an episode and emits `host-pressure`.
  While it lasts nothing starts or relaunches, without spending a fix round or a restart, and
  each running session hears when the host recovers. cardinal repairs it with `host`,
  `cleanup --now` and `stop-tests KEY`, and as a last resort `agent-shell`, which asks you in the
  Claude app before each command. The daily cleanup also removes finished pytest runs. Rerun
  `flocc host cardinal` after `flocc ship` to install cardinal's side.
```

- [ ] **Step 4: Run the full check**

Run: `just check` (if shellcheck is missing, run the recipe's other lines and `uvx --from shellcheck-py shellcheck setup deploy/flocc-agent-run deploy/cardinal/plugin/bin/*`)
Expected: lint clean, every Python and Go test passes.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/steps/agent_user_test.go tests/test_agentrun.py docs/book/cardinal.md docs/book/commands.md CHANGELOG.md
git commit -s -m "test(setup): prove a host picks up the new agent-run allowlist; document host repair"
```

---

### Task 11: Live checks on flocc-nimble (owner-side session)

Run after the PR is merged. Every wait uses the Monitor tool: load it with `ToolSearch("select:Monitor")`, give it the command shown, and act when it exits. Never wrap a wait in a foreground `sleep` loop. The cardinal VM lives on `zer0-day-nimble`; check for builds and peer sessions there before anything restarts a daemon. `F` below stands for `/home/flocc/.local/bin/flocc`.

**Files:** none.

- [ ] **Step 1: Ship the host and cardinal**

From the desktop checkout on `main`:

```bash
flocc ship -H flocc-nimble
flocc host cardinal -H flocc-nimble
```

Expected: both finish with every step ok. Then confirm the pieces landed:

```bash
ssh root@flocc-nimble 'grep -c "|shell|start|stop-tests|sweep|usage)" /usr/local/libexec/flocc-agent-run'
ssh zer0-day-nimble 'incus exec flocc-cardinal -- sh -c "grep -c only-executor /etc/claude-code/managed-settings.json; grep -c dontAsk /home/cardinal/.config/systemd/user/cardinal.service"'
```

Expected: `1`; then `1`, `0`.

- [ ] **Step 2: The new verbs answer through the gate**

```bash
ssh zer0-day-nimble "incus exec flocc-cardinal -- runuser -l cardinal -c '/opt/cardinal/plugin/bin/executor host'"
```

Expected: the `/tmp`, `MemAvailable` and `OOM kills since boot` lines, `Episode: none open`, and the three lists.

```bash
ssh zer0-day-nimble "incus exec flocc-cardinal -- runuser -l cardinal -c \"/opt/cardinal/plugin/bin/executor 'shell' x\""; echo "exit $?"
```

Expected: `executor: shell goes through agent-shell, which asks the owner first` and `exit 2`.

- [ ] **Step 3: The hook blocks anything else in default mode**

Type a request into cardinal's session:

```bash
ssh zer0-day-nimble "incus exec flocc-cardinal -- runuser -l cardinal -c \"tmux -L cardinal send-keys -t cardinal 'Owner test: run the Bash command id, then tell me what happened.' Enter\""
```

Watch with Monitor until cardinal answers:

```bash
ssh zer0-day-nimble "incus exec flocc-cardinal -- runuser -l cardinal -c 'until tmux -L cardinal capture-pane -p -t cardinal -S -80 | grep -q only-executor; do sleep 10; done; tmux -L cardinal capture-pane -p -t cardinal -S -80'"
```

Expected: the pane shows the call blocked by `only-executor`, and no permission prompt reached the Claude app.

- [ ] **Step 4: A fake `host-pressure` event runs the triage section**

```bash
ssh root@flocc-nimble 'py=$(head -1 /home/flocc/.local/bin/flocc | cut -c3-); runuser -u flocc -- "$py" -' <<'EOF'
import time
from flocc import events, ledger
from flocc.config import default_config_path, load
cfg = load(default_config_path())
detail = "TEST from the owner: fake event; check the host and report"
print(events.emit(ledger.connect(cfg.ledger_path), "host-pressure", time.time(), detail=detail))
EOF
```

Note the printed id `N`. Watch with Monitor until cardinal acknowledges it (the batch closes after 2 minutes):

```bash
ssh root@flocc-nimble 'until ! runuser -u flocc -- /home/flocc/.local/bin/flocc events | grep -q "\"id\": N,"; do sleep 15; done; echo acked'
```

Then read cardinal's pane:

```bash
ssh zer0-day-nimble "incus exec flocc-cardinal -- runuser -l cardinal -c 'tmux -L cardinal capture-pane -p -t cardinal -S -200'"
```

Expected: cardinal ran `executor host`, ran no `hold`, sent the owner one line with the values, and acked `N`.

- [ ] **Step 5: A real full `/tmp` is repaired end to end**

Only when `ssh root@flocc-nimble 'runuser -u flocc -- /home/flocc/.local/bin/flocc status'` shows no running session, or the owner agrees to risk one. Fill `/tmp` to 85% with a fake finished pytest run:

```bash
ssh root@flocc-nimble sh -s <<'EOF'
set -eu
size=$(df -B1 --output=size /tmp | tail -1)
used=$(df -B1 --output=used /tmp | tail -1)
fill=$(( size * 85 / 100 - used ))
[ "$fill" -gt 0 ] || { echo "/tmp is already over 85%"; exit 1; }
runuser -u flocc-agent -- mkdir -p /tmp/pytest-of-flocc-agent/pytest-99999
runuser -u flocc-agent -- fallocate -l "$fill" /tmp/pytest-of-flocc-agent/pytest-99999/fill
df -h /tmp
EOF
```

Watch with Monitor until the run is gone (one tick, a 2-minute batch, then cardinal's `cleanup --now`):

```bash
ssh root@flocc-nimble 'until [ ! -e /tmp/pytest-of-flocc-agent/pytest-99999 ]; do sleep 15; done; df -h /tmp'
```

and then until the episode closes on the next tick:

```bash
ssh root@flocc-nimble 'until runuser -u flocc -- /home/flocc/.local/bin/flocc host-report | grep -q "Episode: none open"; do sleep 30; done; echo closed'
```

Expected: a `host-pressure` event naming `/tmp 85%`, cardinal's pane showing `executor host`, `executor cleanup --now` and `executor host` again, one line to the owner, and the episode closed. If the run is still there after 10 minutes, read the pane and `flocc logs -H flocc-nimble -n 100` before doing anything by hand.

- [ ] **Step 6: An approved `agent-shell` call prompts in the Claude app and runs**

```bash
ssh zer0-day-nimble "incus exec flocc-cardinal -- runuser -l cardinal -c \"tmux -L cardinal send-keys -t cardinal 'Owner test: run agent-shell with the command df -h /tmp and report its output.' Enter\""
```

Ask the owner to approve the prompt in the Claude app, and to say whether a push arrived. Watch with Monitor until the call is logged:

```bash
ssh root@flocc-nimble 'until runuser -u flocc -- /home/flocc/.local/bin/flocc digest-data | grep -q "exit 0: df -h /tmp"; do sleep 15; done; echo logged'
```

Expected: the prompt showed the exact command `/opt/cardinal/plugin/bin/agent-shell 'df -h /tmp'`, cardinal reported the `df` output, and `digest-data` lists the call.

- [ ] **Step 7: A refused prompt runs nothing**

```bash
ssh zer0-day-nimble "incus exec flocc-cardinal -- runuser -l cardinal -c \"tmux -L cardinal send-keys -t cardinal 'Owner test: run agent-shell with the command touch /tmp/agent-shell-refused-check and tell me what happened.' Enter\""
```

Ask the owner to refuse the prompt. Once they have, check:

```bash
ssh root@flocc-nimble 'test ! -e /tmp/agent-shell-refused-check && echo "nothing ran"; runuser -u flocc -- /home/flocc/.local/bin/flocc digest-data | grep -c agent-shell-refused-check'
```

Expected: `nothing ran` and `0`.

- [ ] **Step 8: Report**

Tell the owner, in plain words, which checks passed, anything that differed, and the argv shape Task 7 Step 1 confirmed. Acknowledge any test event left unacked with `executor ack`.

---

## Spec coverage

| Spec requirement | Task |
|---|---|
| `host.py` reads `/tmp`, `MemAvailable`, `oom_kill` without root | 1 |
| `tmp_max_pct`, `min_available_mem_gb` as `Config` fields | 1 |
| Open and close thresholds, OOM baseline and reboot reset | 2 |
| `host-pressure` in `events.KINDS`, one per break, again on a new kill, detail format | 2 |
| `host.check` on every tick before `supervise` | 2 |
| `admission.admit` refuses during an episode (poller, `admit_rerun`) | 3 |
| No supervisor launch during an episode, budget kept | 3 |
| Recovery message to each running session on close | 4 |
| `host` verb, `flocc-agent-run usage`, the report's four parts | 5 |
| Sweep's pytest step, `cleanup --now`, 60 s lock wait, bytes freed | 6 |
| `stop-tests`: Bash-tool trees, containers, the message; argv confirmed live | 7 |
| `shell`: `bash -c`, 120 s, 8,000 characters, `meta` log, `digest-data` | 8 |
| Gate caps `shell` at `NOTE_CHARS` | 8 |
| `bin/agent-shell`, `bin/executor` refusing `shell`, `only-executor`, managed settings, `cardinal.service`, triage section | 9 |
| Allowlist in `deploy/flocc-agent-run`, `SUBCOMMANDS`, `HANDLERS`, picked up by `flocc setup` | 5, 7, 8, 10 |
| Live checks: fake event, prompt and refusal, `executor 'shell' x` | 11 |
