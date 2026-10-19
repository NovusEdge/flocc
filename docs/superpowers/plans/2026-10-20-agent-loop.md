# Agent Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Host agent sessions delegate only to catalogue subagents within per-run, per-phase budgets enforced by a fail-closed gate, with grants from the agent block or owner comments, one compaction before handoff, an allowlisted plugin set, no-TUI Remote Control and scratchpad cleanup.

**Architecture:** A pure decision module (`gate.py`) holds the catalogue budgets and rules; a thin CLI wraps it as Claude Code hooks (`gate`, `gate --stopped`, `session-start`) that read and append `<state_dir>/launches.jsonl` under a `flock`. Grants live in `<state_dir>/grants.json`, written by the supervisor from the agent block at launch and by `forward_comments` from owner comments. `claude_argv` and `launch()` add the environment, deny rules and plugin overrides; the installer gains the plugin allowlist, settings and a tmpfiles step.

**Tech Stack:** Python 3.13 (uv, pytest, ruff, pyrefly), Go installer (`go test`), Claude Code 2.1.295+ hooks and plugin agents.

**Spec:** `docs/superpowers/specs/2026-10-20-agent-loop-design.md`

## Global Constraints

- Applies to host agent sessions only; nothing in the owner's desktop config changes, except the documented manual tmpfiles line.
- Claude Code minimum on hosts: 2.1.295 (was 2.1.293).
- Catalogue agent names are `linear-agents:<name>`; the gate also accepts the bare `<name>`.
- Budgets, verbatim: test 2 launches / 2 running (`source-finder`, `source-reader`); impl 10 / 4 (`task-implementer`, `wave-reviewer`, `fix-rereviewer`, `plan-reviewer`, `code-mapper`, `source-finder`, `source-reader`); fix 2 / 1 (`task-implementer`, `fix-rereviewer`); review 0 / 0.
- Models, verbatim: `source-finder` haiku; `source-reader`, `code-mapper`, `task-implementer`, `fix-rereviewer` sonnet; `plan-reviewer`, `wave-reviewer` opus.
- Do not set `CLAUDE_CODE_SUBAGENT_MODEL_FORCE`.
- Grants come only from the agent block, the owner's comments (`comment.mine`) and `steer_authors`.
- The gate makes no network call and does not open the ledger.
- Scratchpad cleanup deletes only paths under `/tmp/claude-`.
- Commits: `git commit -s`, no `Co-Authored-By`, no AI attribution.
- Every task ends green on `uv run pytest -q`, `uv run ruff check`, `uv run ruff format --check`, `uv run pyrefly check` and, for Go tasks, `go test ./...`.

## Review Focus

1. A launch naming `task-implementer` without the `linear-agents:` prefix must be treated as the same catalogue agent, not refused as unknown (Task 2 test `test_bare_name_matches_catalogue`).
2. A truncated last line in `launches.jsonl` (a hook killed mid-write) must not crash the gate or the counts; it is skipped (Task 3 test `test_gate_skips_a_torn_log_line`).
3. Two gate calls racing for the last launch slot must not both be allowed (Task 3 test `test_concurrent_gates_share_the_last_slot`).
4. An owner comment with `Allow:` in another case, or `allow:` after other text on the line, follows one rule: only a line that starts with `allow:` (case-insensitive, after leading spaces) grants (Task 1 test `test_allow_lines_only_at_line_start`).
5. A recorded scratchpad path outside `/tmp/claude-` (a corrupted or hand-edited record) must never be deleted (Task 8 test `test_cleanup_never_deletes_outside_tmp_claude`).

---

### Task 1: Grants and the `allow` agent-block key

**Files:**
- Create: `src/linear_agents/grants.py`
- Modify: `src/linear_agents/agentblock.py` (`_KEYS`, `IssueSettings`, `parse_settings`, `settings_from_json`)
- Test: `tests/test_grants.py`, `tests/test_agentblock.py`

**Interfaces:**
- Produces:
  - `class GrantError(ValueError)`
  - `@dataclass(frozen=True) class Grants: launches: int | None = None; agents: tuple[str, ...] = (); workflows: tuple[str, ...] = (); fix_rounds: int | None = None`
  - `parse_allow(text: str) -> Grants` (raises `GrantError`)
  - `allow_lines(body: str) -> list[str]` (the text after `allow:` for each granting line)
  - `merge(a: Grants, b: Grants) -> Grants` (b's numbers win when set; names unioned, order kept)
  - `read(path: Path) -> Grants` (missing or unparseable file → `Grants()`)
  - `write(path: Path, g: Grants) -> None`
  - `IssueSettings.allow: str | None = None` (raw `allow` text, validated at parse)

- [ ] **Step 1: Write the failing tests**

`tests/test_grants.py`:

```python
import json

import pytest

from linear_agents.grants import GrantError, Grants, allow_lines, merge, parse_allow, read, write


def test_parse_allow_reads_every_item():
    g = parse_allow("launches 10, agent general-purpose, workflow sdd-waves, fix-rounds 2")
    assert g == Grants(launches=10, agents=("general-purpose",), workflows=("sdd-waves",), fix_rounds=2)


def test_parse_allow_rejects_unknown_and_malformed_items():
    for bad in ("launches ten", "agents x", "fix-rounds", "workflow"):
        with pytest.raises(GrantError):
            parse_allow(bad)


def test_allow_lines_only_at_line_start():
    body = "thanks!\n  Allow: launches 4\nplease allow: agent x\nallow:agent code-mapper"
    assert allow_lines(body) == ["launches 4", "agent code-mapper"]


def test_merge_later_numbers_win_and_names_union():
    a = Grants(launches=4, agents=("a",), fix_rounds=2)
    b = Grants(launches=8, agents=("b", "a"))
    assert merge(a, b) == Grants(launches=8, agents=("a", "b"), fix_rounds=2)


def test_read_missing_or_corrupt_is_no_grants(tmp_path):
    assert read(tmp_path / "none.json") == Grants()
    bad = tmp_path / "grants.json"
    bad.write_text("{half")
    assert read(bad) == Grants()


def test_write_then_read_round_trips(tmp_path):
    g = Grants(launches=3, workflows=("sdd-waves",))
    write(tmp_path / "grants.json", g)
    assert read(tmp_path / "grants.json") == g
    assert json.loads((tmp_path / "grants.json").read_text())["launches"] == 3
```

Add to `tests/test_agentblock.py`:

```python
def test_allow_key_is_kept_raw_and_validated():
    s = parse_settings("```agent\nallow: launches 4, agent code-mapper\n```")
    assert s.allow == "launches 4, agent code-mapper"
    assert settings_from_json(settings_to_json(s)).allow == s.allow
    with pytest.raises(BlockError, match="allow"):
        parse_settings("```agent\nallow: launches lots\n```")
```

(Import `settings_from_json`, `settings_to_json` and `BlockError` in that file if not already imported.)

- [ ] **Step 2: Run the tests to see them fail**

Run: `uv run pytest tests/test_grants.py tests/test_agentblock.py -q`
Expected: FAIL, `ModuleNotFoundError: linear_agents.grants` and `unknown keys ['allow']`.

- [ ] **Step 3: Implement**

`src/linear_agents/grants.py`:

```python
import json
import os
import re
from dataclasses import asdict, dataclass
from pathlib import Path

_LINE = re.compile(r"^[ \t]*allow:[ \t]*(.+)$", re.IGNORECASE | re.MULTILINE)


class GrantError(ValueError):
    pass


@dataclass(frozen=True)
class Grants:
    launches: int | None = None
    agents: tuple[str, ...] = ()
    workflows: tuple[str, ...] = ()
    fix_rounds: int | None = None


def _count(word: str, value: str) -> int:
    if not value.isdigit():
        raise GrantError(f"allow: {word} needs a number, got {value!r}")
    return int(value)


def parse_allow(text: str) -> Grants:
    launches = fix_rounds = None
    agents: list[str] = []
    workflows: list[str] = []
    for item in (i.strip() for i in text.split(",")):
        if not item:
            continue
        word, _, value = item.partition(" ")
        value = value.strip()
        if word == "launches":
            launches = _count(word, value)
        elif word == "fix-rounds":
            fix_rounds = _count(word, value)
        elif word in ("agent", "workflow") and value and " " not in value:
            (agents if word == "agent" else workflows).append(value)
        else:
            raise GrantError(f"allow: unknown item {item!r}")
    return Grants(launches, tuple(agents), tuple(workflows), fix_rounds)


def allow_lines(body: str) -> list[str]:
    return [m.group(1).strip() for m in _LINE.finditer(body)]


def _union(a: tuple[str, ...], b: tuple[str, ...]) -> tuple[str, ...]:
    return a + tuple(x for x in b if x not in a)


def merge(a: Grants, b: Grants) -> Grants:
    return Grants(
        launches=b.launches if b.launches is not None else a.launches,
        agents=_union(a.agents, b.agents),
        workflows=_union(a.workflows, b.workflows),
        fix_rounds=b.fix_rounds if b.fix_rounds is not None else a.fix_rounds,
    )


def read(path: Path) -> Grants:
    # The gate fails closed on its own errors, so a damaged file must read as "no grants",
    # not raise.
    try:
        raw = json.loads(path.read_text())
        return Grants(
            launches=raw.get("launches"),
            agents=tuple(raw.get("agents", ())),
            workflows=tuple(raw.get("workflows", ())),
            fix_rounds=raw.get("fix_rounds"),
        )
    except (OSError, ValueError, AttributeError, TypeError):
        return Grants()


def write(path: Path, g: Grants) -> None:
    tmp = path.with_name(f"{path.name}.tmp")
    tmp.write_text(json.dumps(asdict(g)))
    os.replace(tmp, path)
```

In `src/linear_agents/agentblock.py`: add `"allow"` to `_KEYS`; add `allow: str | None = None` to `IssueSettings`; in `parse_settings`, before the `return`:

```python
    allow = values.get("allow") or None
    if allow is not None:
        from linear_agents.grants import GrantError, parse_allow

        try:
            parse_allow(allow)
        except GrantError as exc:
            raise BlockError(f"agent block: {exc}") from exc
```

and pass `allow=allow` in the returned `IssueSettings`. In `settings_from_json`, add `allow=raw.get("allow")`.

- [ ] **Step 4: Run the tests**

Run: `uv run pytest tests/test_grants.py tests/test_agentblock.py -q`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/linear_agents/grants.py src/linear_agents/agentblock.py tests/test_grants.py tests/test_agentblock.py
git commit -s -m "Parse subagent grants from the agent block"
```

---

### Task 2: Gate decisions

**Files:**
- Create: `src/linear_agents/gate.py`
- Test: `tests/test_gate.py`

**Interfaces:**
- Consumes: `Grants` from Task 1.
- Produces:
  - `@dataclass(frozen=True) class Budget: agents: tuple[str, ...]; launches: int; running: int`
  - `BUDGETS: dict[str, Budget]`
  - `PREFIX = "linear-agents:"`
  - `@dataclass(frozen=True) class Decision: allowed: bool; rule: str; reason: str = ""`
  - `decide(payload: dict, phase: str | None, grants: Grants, launched: int, running: int, refused_rules: set[str]) -> Decision`
    - `launched`: allowed launches in this run; `running`: those not yet stopped; `refused_rules`: rules already refused in this run.
  - `ESCALATE` (the escalation command template, used in reasons)

- [ ] **Step 1: Write the failing tests**

`tests/test_gate.py`:

```python
from linear_agents.gate import BUDGETS, decide
from linear_agents.grants import Grants


def agent(sub="linear-agents:task-implementer", **extra):
    return {"tool_name": "Agent", "tool_input": {"subagent_type": sub, "prompt": "p", **extra}}


def d(payload, phase="impl", grants=Grants(), launched=0, running=0, refused=()):
    return decide(payload, phase, grants, launched, running, set(refused))


def test_catalogue_agent_within_budget_is_allowed():
    assert d(agent()).allowed


def test_bare_name_matches_catalogue():
    assert d(agent("task-implementer")).allowed


def test_call_from_inside_a_subagent_is_refused():
    p = agent()
    p["agent_id"] = "a1"
    assert d(p).rule == "nested"


def test_per_call_model_is_refused():
    assert d(agent(model="opus")).rule == "model"


def test_teammates_are_refused():
    assert d(agent(team_name="t")).rule == "teammate"
    assert d(agent(name="helper")).rule == "teammate"


def test_missing_subagent_type_is_general_purpose():
    p = {"tool_name": "Agent", "tool_input": {"prompt": "p"}}
    assert d(p).rule == "not-allowed"
    assert d(p, grants=Grants(agents=("general-purpose",))).allowed


def test_agent_outside_the_phase_list_is_refused_unless_granted():
    assert d(agent("linear-agents:wave-reviewer"), phase="test").rule == "not-allowed"
    g = Grants(agents=("wave-reviewer",))
    assert d(agent("linear-agents:wave-reviewer"), phase="test", grants=g).allowed


def test_launch_budget_and_its_grant():
    full = BUDGETS["impl"].launches
    assert d(agent(), launched=full).rule == "launches"
    assert d(agent(), launched=full, grants=Grants(launches=full + 2)).allowed


def test_running_limit():
    assert d(agent(), running=BUDGETS["impl"].running).rule == "running"


def test_workflow_needs_a_named_grant():
    wf = {"tool_name": "Workflow", "tool_input": {"name": "sdd-waves"}}
    assert d(wf).rule == "workflow"
    assert d(wf, grants=Grants(workflows=("sdd-waves",))).allowed
    unnamed = {"tool_name": "Workflow", "tool_input": {"script": "..."}}
    assert d(unnamed, grants=Grants(workflows=("sdd-waves",))).rule == "workflow"


def test_unknown_or_missing_phase_is_refused():
    assert d(agent(), phase=None).rule == "phase"
    assert d(agent(), phase="deploy").rule == "phase"


def test_review_phase_allows_nothing():
    assert d(agent("linear-agents:source-finder"), phase="review").rule == "not-allowed"


def test_first_refusal_points_at_escalate_and_a_repeat_says_stop():
    first = d(agent(), launched=99)
    assert "linear-agents escalate" in first.reason
    again = d(agent(), launched=99, refused={"launches"})
    assert "already escalated" in again.reason and "escalate --tier" not in again.reason
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `uv run pytest tests/test_gate.py -q`
Expected: FAIL, `ModuleNotFoundError: linear_agents.gate`.

- [ ] **Step 3: Implement**

`src/linear_agents/gate.py`:

```python
from dataclasses import dataclass

from linear_agents.grants import Grants

PREFIX = "linear-agents:"
GENERAL = "general-purpose"
ESCALATE = (
    "If the run needs it, escalate once: linear-agents escalate --tier 2 --cause grant:{rule}"
    " --summary '<why it is needed>' --default '<doing it without>' --deadline-min 120,"
    " then carry on without it."
)


@dataclass(frozen=True)
class Budget:
    agents: tuple[str, ...]
    launches: int
    running: int


_RESEARCH = ("source-finder", "source-reader")
BUDGETS = {
    "test": Budget(_RESEARCH, 2, 2),
    "impl": Budget(
        (
            "task-implementer",
            "wave-reviewer",
            "fix-rereviewer",
            "plan-reviewer",
            "code-mapper",
            *_RESEARCH,
        ),
        10,
        4,
    ),
    "fix": Budget(("task-implementer", "fix-rereviewer"), 2, 1),
    "review": Budget((), 0, 0),
}


@dataclass(frozen=True)
class Decision:
    allowed: bool
    rule: str
    reason: str = ""


def _refuse(rule: str, why: str, refused: set[str]) -> Decision:
    if rule in refused:
        tail = "This was already escalated in this run; do not retry it or escalate again."
    else:
        tail = ESCALATE.format(rule=rule)
    return Decision(False, rule, f"linear-agents gate: {why} {tail}")


def decide(
    payload: dict,
    phase: str | None,
    grants: Grants,
    launched: int,
    running: int,
    refused_rules: set[str],
) -> Decision:
    budget = BUDGETS.get(phase or "")
    if budget is None:
        return _refuse("phase", f"no budget for phase {phase!r}.", refused_rules)
    if payload.get("agent_id"):
        return _refuse("nested", "subagents may not start subagents or workflows.", refused_rules)
    args = payload.get("tool_input") or {}
    if payload.get("tool_name") == "Workflow":
        name = args.get("name")
        if not name or name not in grants.workflows:
            return _refuse("workflow", f"workflow {name!r} has no grant.", refused_rules)
        return Decision(True, "allowed")
    if args.get("model"):
        return _refuse("model", "agent files fix the model; drop the model parameter.", refused_rules)
    if args.get("team_name") or args.get("name"):
        return _refuse("teammate", "named teammates are not allowed in host sessions.", refused_rules)
    sub = (args.get("subagent_type") or GENERAL).removeprefix(PREFIX)
    if sub not in budget.agents and sub not in grants.agents:
        allowed = ", ".join(PREFIX + a for a in budget.agents) or "none"
        return _refuse("not-allowed", f"{sub} is not allowed in the {phase} phase (allowed: {allowed}).", refused_rules)
    limit = grants.launches if grants.launches is not None else budget.launches
    if launched >= limit:
        return _refuse("launches", f"the {phase} phase has used its {limit} launches.", refused_rules)
    if running >= budget.running:
        return _refuse("running", f"{running} agents are still running; wait for one to finish.", refused_rules)
    return Decision(True, "allowed")
```

(Wrap any line over the repo's ruff line length when formatting.)

- [ ] **Step 4: Run the tests**

Run: `uv run pytest tests/test_gate.py -q`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/linear_agents/gate.py tests/test_gate.py
git commit -s -m "Decide subagent launches against per-phase budgets"
```

---

### Task 3: Gate hook commands and the launch log

**Files:**
- Modify: `src/linear_agents/gate.py` (add the log and hook entry points)
- Modify: `src/linear_agents/cli.py` (`gate`, `session-start` commands; `context` prints grants)
- Test: `tests/test_gate.py`, `tests/test_cli.py`

**Interfaces:**
- Consumes: `decide`, `BUDGETS` (Task 2); `grants.read` (Task 1).
- Produces:
  - `LOG = "launches.jsonl"`
  - `run_gate(payload: dict, state: Path, phase: str | None, run: str | None, now: float) -> dict | None` — returns the hook output dict to print for a refusal, `None` to allow; appends one log line either way.
  - `record_stop(state: Path, run: str | None, now: float) -> None`
  - `record_session(state: Path, run: str | None, now: float) -> None`
  - CLI: `linear-agents gate [--stopped]` (hook stdin JSON), `linear-agents session-start` (hook stdin JSON; prints the context).
  - Log line shape: `{"at": float, "run": str, "event": "launch"|"refused"|"stop"|"session", "what": str, "rule": str}`

- [ ] **Step 1: Write the failing tests**

Add to `tests/test_gate.py`:

```python
import json
import threading

from linear_agents.gate import LOG, record_session, record_stop, run_gate


def lines(state):
    return [json.loads(x) for x in (state / LOG).read_text().splitlines()]


def test_run_gate_logs_and_counts_per_run(tmp_path):
    for _ in range(BUDGETS["fix"].launches):
        assert run_gate(agent(), tmp_path, "fix", "fix@1", 0.0) is None
        record_stop(tmp_path, "fix@1", 0.0)
    out = run_gate(agent(), tmp_path, "fix", "fix@1", 0.0)
    assert out["hookSpecificOutput"]["permissionDecision"] == "deny"
    assert run_gate(agent(), tmp_path, "fix", "fix@2", 0.0) is None, "a new run starts a new count"
    assert [e["event"] for e in lines(tmp_path)].count("refused") == 1


def test_running_count_resets_at_session_start(tmp_path):
    assert run_gate(agent(), tmp_path, "fix", "fix@1", 0.0) is None
    assert run_gate(agent(), tmp_path, "fix", "fix@1", 0.0)["hookSpecificOutput"]
    record_session(tmp_path, "fix@1", 1.0)
    # The launch budget (2) is not spent by the refused call; the running slot came back.
    assert run_gate(agent(), tmp_path, "fix", "fix@1", 2.0) is None


def test_gate_skips_a_torn_log_line(tmp_path):
    (tmp_path / LOG).write_text('{"at": 0, "run": "fix@1", "event": "launch"}\n{"at": 1, "ru')
    assert run_gate(agent(), tmp_path, "fix", "fix@1", 2.0) is None


def test_concurrent_gates_share_the_last_slot(tmp_path):
    results = []
    threads = [
        threading.Thread(target=lambda: results.append(run_gate(agent(), tmp_path, "test-like", "r", 0.0)))
        for _ in range(8)
    ]
    BUDGETS["test-like"] = BUDGETS["fix"]
    try:
        for t in threads:
            t.start()
        for t in threads:
            t.join()
    finally:
        del BUDGETS["test-like"]
    assert results.count(None) == 1
```

Add to `tests/test_cli.py` (follow that file's existing `main([...])` and `monkeypatch.setenv` style):

```python
def test_gate_command_denies_with_hook_json(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("LINEAR_AGENTS_STATE", str(tmp_path))
    monkeypatch.setenv("LINEAR_AGENTS_PHASE", "review")
    monkeypatch.setenv("LINEAR_AGENTS_RUN", "review@1")
    payload = {"tool_name": "Agent", "tool_input": {"subagent_type": "linear-agents:source-finder"}}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    assert main(["gate"]) == 0
    out = json.loads(capsys.readouterr().out)
    assert out["hookSpecificOutput"]["permissionDecision"] == "deny"


def test_session_start_marks_compaction_and_records_the_scratchpad(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("LINEAR_AGENTS_STATE", str(tmp_path))
    monkeypatch.setenv("LINEAR_AGENTS_LEDGER", str(tmp_path / "l.sqlite"))
    (tmp_path / "status.json").write_text("{}")
    payload = {"source": "compact", "scratchpad_dir": "/tmp/claude-1000/x/s1"}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    assert main(["session-start"]) == 0
    assert (tmp_path / "compacted").exists() and not (tmp_path / "status.json").exists()
    assert (tmp_path / "scratchpads").read_text().splitlines() == ["/tmp/claude-1000/x/s1"]


def test_context_prints_current_grants(tmp_path, monkeypatch, capsys):
    from linear_agents.grants import Grants, write

    monkeypatch.setenv("LINEAR_AGENTS_STATE", str(tmp_path))
    monkeypatch.setenv("LINEAR_AGENTS_LEDGER", str(tmp_path / "l.sqlite"))
    write(tmp_path / "grants.json", Grants(launches=12, fix_rounds=2))
    main(["context"])
    out = capsys.readouterr().out
    assert "## Grants" in out and "launches 12" in out and "fix-rounds 2" in out
```

(Add `import io` and `import json` to `tests/test_cli.py` if missing.)

- [ ] **Step 2: Run the tests to see them fail**

Run: `uv run pytest tests/test_gate.py tests/test_cli.py -q`
Expected: FAIL, `ImportError: cannot import name 'run_gate'` and `invalid choice: 'gate'`.

- [ ] **Step 3: Implement**

Append to `src/linear_agents/gate.py`:

```python
import fcntl
import json
from contextlib import contextmanager
from pathlib import Path

from linear_agents import grants as grants_mod

LOG = "launches.jsonl"


@contextmanager
def _locked(state: Path):
    state.mkdir(parents=True, exist_ok=True)
    with (state / f"{LOG}.lock").open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        yield


def _entries(state: Path, run: str | None) -> list[dict]:
    path = state / LOG
    if not path.exists():
        return []
    out = []
    for line in path.read_text().splitlines():
        try:
            e = json.loads(line)
        except ValueError:  # a hook killed mid-write leaves a torn last line
            continue
        if isinstance(e, dict) and e.get("run") == run:
            out.append(e)
    return out


def _append(state: Path, entry: dict) -> None:
    with (state / LOG).open("a") as f:
        f.write(json.dumps(entry) + "\n")


def _counts(entries: list[dict]) -> tuple[int, int, set[str]]:
    launched = sum(e["event"] == "launch" for e in entries)
    refused = {e.get("rule", "") for e in entries if e["event"] == "refused"}
    # Running agents end with their session, so only launches since the last session start count.
    last = max((i for i, e in enumerate(entries) if e["event"] == "session"), default=-1)
    since = entries[last + 1 :]
    running = sum(e["event"] == "launch" for e in since) - sum(e["event"] == "stop" for e in since)
    return launched, max(running, 0), refused


def run_gate(payload: dict, state: Path, phase: str | None, run: str | None, now: float) -> dict | None:
    args = payload.get("tool_input") or {}
    what = args.get("subagent_type") or args.get("name") or payload.get("tool_name", "")
    with _locked(state):
        launched, running, refused = _counts(_entries(state, run))
        g = grants_mod.read(state / "grants.json")
        dec = decide(payload, phase, g, launched, running, refused)
        event = "launch" if dec.allowed else "refused"
        _append(state, {"at": now, "run": run, "event": event, "what": what, "rule": dec.rule})
    if dec.allowed:
        return None
    return {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": dec.reason,
        }
    }


def record_stop(state: Path, run: str | None, now: float) -> None:
    with _locked(state):
        _append(state, {"at": now, "run": run, "event": "stop", "what": "", "rule": ""})


def record_session(state: Path, run: str | None, now: float) -> None:
    with _locked(state):
        _append(state, {"at": now, "run": run, "event": "session", "what": "", "rule": ""})
```

(Move the new imports to the top of the module.)

In `src/linear_agents/cli.py`:

```python
def _hook_input() -> dict:
    raw = sys.stdin.read() if not sys.stdin.isatty() else ""
    try:
        data = json.loads(raw) if raw.strip() else {}
    except ValueError:
        return {}
    return data if isinstance(data, dict) else {}


def cmd_gate(args) -> int:
    from linear_agents.gate import record_stop, run_gate

    state = _state_dir()
    run = os.environ.get("LINEAR_AGENTS_RUN")
    payload = _hook_input()
    if args.stopped:
        record_stop(state, run, time.time())
        return 0
    out = run_gate(payload, state, os.environ.get("LINEAR_AGENTS_PHASE"), run, time.time())
    if out is not None:
        print(json.dumps(out))
    return 0


def cmd_session_start(args) -> int:
    from linear_agents.gate import record_session

    state = _state_dir()
    state.mkdir(parents=True, exist_ok=True)
    payload = _hook_input()
    if payload.get("source") == "compact":
        # status.json still holds the pre-compaction reading; the supervisor would hand off at once.
        (state / "compacted").touch()
        (state / "status.json").unlink(missing_ok=True)
    pad = payload.get("scratchpad_dir")
    if isinstance(pad, str) and pad:
        known = state / "scratchpads"
        listed = known.read_text().splitlines() if known.exists() else []
        if pad not in listed:
            with known.open("a") as f:
                f.write(pad + "\n")
    record_session(state, os.environ.get("LINEAR_AGENTS_RUN"), time.time())
    return cmd_context(args)
```

`_state_dir()` raises `KeyError` when `LINEAR_AGENTS_STATE` is unset; that exit is non-zero, which the hook's `onFailure: block` turns into a refusal (Task 4).

In `cmd_context`, after the peers block:

```python
    from linear_agents.grants import read as read_grants

    g = read_grants(state / "grants.json")
    items = [f"launches {g.launches}"] if g.launches is not None else []
    items += [f"agent {a}" for a in g.agents] + [f"workflow {w}" for w in g.workflows]
    items += [f"fix-rounds {g.fix_rounds}"] if g.fix_rounds is not None else []
    print(f"## Grants\n\n{', '.join(items) or 'none'}.\n")
```

Register in `COMMANDS` (`"gate": cmd_gate`, `"session-start": cmd_session_start`) and in `build_parser`:

```python
    gate = add("gate", "PreToolUse hook: allow or refuse a subagent or workflow launch")
    gate.add_argument("--stopped", action="store_true", help="SubagentStop hook: record a finished agent")
    add("session-start", "SessionStart hook: record compaction and the scratchpad, print the context")
```

- [ ] **Step 4: Run the tests**

Run: `uv run pytest tests/test_gate.py tests/test_cli.py -q`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/linear_agents/gate.py src/linear_agents/cli.py tests/test_gate.py tests/test_cli.py
git commit -s -m "Add the gate and session-start hook commands with a per-run launch log"
```

---

### Task 4: Plugin catalogue, hooks and skills

**Files:**
- Create: `plugin/agents/source-finder.md`, `plugin/agents/source-reader.md`, `plugin/agents/code-mapper.md`, `plugin/agents/plan-reviewer.md`, `plugin/agents/task-implementer.md`, `plugin/agents/wave-reviewer.md`, `plugin/agents/fix-rereviewer.md`
- Modify: `plugin/hooks/hooks.json`, `plugin/skills/implementer/SKILL.md`, `plugin/skills/tester/SKILL.md`, `src/linear_agents/context.py` (`_COMMANDS`)
- Test: `tests/test_plugin.py` (create)

**Interfaces:**
- Consumes: `BUDGETS`, `PREFIX` (Task 2); `gate`, `session-start` commands (Task 3).
- Produces: agent files whose `name` matches every name in `BUDGETS`.

- [ ] **Step 1: Write the failing test**

`tests/test_plugin.py`:

```python
import json
from pathlib import Path

from linear_agents.gate import BUDGETS

PLUGIN = Path(__file__).parent.parent / "plugin"
MODELS = {
    "source-finder": "haiku",
    "source-reader": "sonnet",
    "code-mapper": "sonnet",
    "plan-reviewer": "opus",
    "task-implementer": "sonnet",
    "wave-reviewer": "opus",
    "fix-rereviewer": "sonnet",
}


def frontmatter(path: Path) -> dict[str, str]:
    head = path.read_text().split("---")[1]
    return {k.strip(): v.strip() for k, _, v in (ln.partition(":") for ln in head.splitlines()) if k.strip()}


def test_every_budgeted_agent_has_a_file_with_its_model():
    names = {a for b in BUDGETS.values() for a in b.agents}
    assert names == MODELS.keys()
    for name, model in MODELS.items():
        fm = frontmatter(PLUGIN / "agents" / f"{name}.md")
        assert fm["name"] == name and fm["model"] == model


def test_reviewers_and_mappers_cannot_edit_or_run_shell():
    for name in ("code-mapper", "plan-reviewer", "wave-reviewer", "fix-rereviewer"):
        tools = frontmatter(PLUGIN / "agents" / f"{name}.md")["tools"]
        assert not {"Edit", "Write", "Bash"} & {t.strip() for t in tools.split(",")}


def test_hooks_gate_fails_closed_and_session_start_uses_the_new_command():
    hooks = json.loads((PLUGIN / "hooks" / "hooks.json").read_text())["hooks"]
    pre = hooks["PreToolUse"][0]
    assert pre["matcher"] == "Agent|Task|Workflow"
    gate = pre["hooks"][0]
    assert gate["command"] == "linear-agents gate" and gate["onFailure"] == "block"
    assert hooks["SubagentStop"][0]["hooks"][0]["command"] == "linear-agents gate --stopped"
    assert hooks["SessionStart"][0]["hooks"][0]["command"] == "linear-agents session-start"
```

- [ ] **Step 2: Run the test to see it fail**

Run: `uv run pytest tests/test_plugin.py -q`
Expected: FAIL, `FileNotFoundError` for `plugin/agents/source-finder.md`.

- [ ] **Step 3: Write the agent files, hooks and skill changes**

`plugin/hooks/hooks.json`:

```json
{
  "hooks": {
    "SessionStart": [
      { "hooks": [ { "type": "command", "command": "linear-agents session-start" } ] }
    ],
    "PreToolUse": [
      {
        "matcher": "Agent|Task|Workflow",
        "hooks": [
          { "type": "command", "command": "linear-agents gate", "timeout": 10, "onFailure": "block" }
        ]
      }
    ],
    "SubagentStop": [
      { "hooks": [ { "type": "command", "command": "linear-agents gate --stopped" } ] }
    ]
  }
}
```

`plugin/agents/source-finder.md`:

```markdown
---
name: source-finder
description: Find dated, URL-cited sources on one library, API or service question. Use before relying on or adding a dependency. Finds sources only; source-reader reads them.
model: haiku
tools: WebSearch, WebFetch
maxTurns: 20
---

You are finding sources, not writing conclusions. The question is in your task. Return at most
eight sources as a list: URL, publication or last-updated date, and one line on what it covers.
Prefer official docs, release notes and maintainers' posts over blogs. Today's date is in your
task; mark anything older than a year. Do not write files. Do not start subagents.
```

`plugin/agents/source-reader.md`:

```markdown
---
name: source-reader
description: Read the sources source-finder returned and answer the stated question with citations. Use only after source-finder.
model: sonnet
tools: WebSearch, WebFetch, Read
maxTurns: 30
---

Read each source in your task and answer its question. Every claim carries the URL and date it
came from; say plainly what the sources do not settle. Keep the answer under 400 words. Do not
write files. Do not start subagents.
```

`plugin/agents/code-mapper.md`:

```markdown
---
name: code-mapper
description: Map where something lives in a large repo before planning. Use when the plan needs paths you cannot find with a couple of searches yourself.
model: sonnet
tools: Read, Grep, Glob
maxTurns: 30
---

Find where the thing named in your task lives. Return paths with line numbers and two or three
sentences on how the pieces connect. Read excerpts, not whole large files. Change nothing. Do not
start subagents.
```

`plugin/agents/plan-reviewer.md`:

```markdown
---
name: plan-reviewer
description: One adversarial read of the implementer's plan, for plans of four or more tasks, before any building starts.
model: opus
tools: Read, Grep, Glob
maxTurns: 30
---

Review the plan at the path in your task against the issue's acceptance criteria and the tester's
committed tests (paths in your task). Look for criteria with no task, tasks that touch the same
paths but are scheduled in parallel, missing tests, and steps an implementer would have to guess.
Return findings ranked blocker / should-fix / nit, each with evidence and a concrete fix. Edit
nothing. Do not start subagents.
```

`plugin/agents/task-implementer.md`:

```markdown
---
name: task-implementer
description: Implement one task of the implementer's plan from its brief file, and make the fixes a wave review asks for. Plan tasks always go through this agent, even a single one.
model: sonnet
tools: Read, Edit, Write, Bash, Grep, Glob
maxTurns: 80
---

Your brief is the file named in your task; it is your requirements, including the paths you own.
Write the test first when the brief asks for new behaviour, then the code. Edit only your paths.
Run only the focused tests your brief names; never run Unity, long builds, training or the full
suite. Commit only your paths with `linear-agents commit -m '<message>' -- <paths>`. Never push.
Report what you changed and the test output. Do not start subagents.
```

`plugin/agents/wave-reviewer.md`:

```markdown
---
name: wave-reviewer
description: Review one wave of implemented tasks against their briefs, once per wave, not per task.
model: opus
tools: Read, Grep, Glob
maxTurns: 40
---

Your task names the briefs, the wave's diff file and its test output. Check first whether each
task meets its brief, then whether it is well built. Return findings per task, ranked blocker /
should-fix / nit, each with the file and line and a concrete fix. You write findings only; the
implementer makes every edit. Do not start subagents.
```

`plugin/agents/fix-rereviewer.md`:

```markdown
---
name: fix-rereviewer
description: Verdict each finding of one fix round against the fix diff file, and nothing else.
model: sonnet
tools: Read, Grep, Glob
maxTurns: 20
---

Your task names the findings and the fix diff file. For each finding answer fixed, not fixed or
made worse, with the line that shows it. Review nothing outside the findings. Edit nothing. Do
not start subagents.
```

Replace the bullet list in `plugin/skills/implementer/SKILL.md` with:

```markdown
Make the committed tests pass by implementing the behavior.

- Do not edit, delete or skip any file the tester committed. The service rejects the run if you
  do. If you believe a test is wrong, do not edit it: escalate with `--tier 2 --waiting`, state
  the test change you propose in `--summary`, and keep working only on parts that do not depend
  on that test.
- Claim paths before editing them: `linear-agents claim '<glob>'`. A rejected claim names the
  issue that holds it; message that session or escalate.
- Plan first: write `plan.md` in your state directory with numbered tasks, the paths each task
  touches, and its tests. Tasks that share a path go in different waves; a wave holds at most four
  tasks.
- With four or more tasks, have `linear-agents:plan-reviewer` read the plan once before building,
  and fix what it finds. With fewer, check the plan against the acceptance criteria yourself.
- Write each task's brief to `briefs/task-N.md` in your state directory: its requirements, the
  paths it owns, the focused test command. Start one `linear-agents:task-implementer` per task with
  the brief's path, every plan task included, even if there is only one.
- After each wave, write its `git diff` and test output to `waves/N.diff` and `waves/N.tests`
  in your state directory, and have one `linear-agents:wave-reviewer` review the wave.
- Findings get one fix round: the same `task-implementer` route, then one
  `linear-agents:fix-rereviewer` on the fix diff. A second round needs a `fix-rounds` grant (see
  `linear-agents context`); without one, escalate what is left.
- Do small work yourself: a change outside the plan's tasks that fits in a few lines is not a task.
- Never pass `model` when starting an agent, never start general-purpose agents, workflows or
  teammates, and never run `claude` from the shell. The gate refuses them and says how to ask.
- Run the repo's check (usually `just check`) before finishing.
- Never push or open a PR; the service does that.
- Finish with `linear-agents done --summary '<what changed and why, for the PR body>'`.
```

Append to `plugin/skills/tester/SKILL.md`:

```markdown
- To look up how an unfamiliar library or API behaves, use `linear-agents:source-finder` and then
  `linear-agents:source-reader`; you have two launches. Start no other agents.
```

In `src/linear_agents/context.py` `_COMMANDS`, add:

```python
- `linear-agents commit -m '<message>' -- <paths>` to commit only your paths (task-implementer).
- `linear-agents context` also lists your subagent grants under "Grants".
```

- [ ] **Step 4: Run the tests**

Run: `uv run pytest tests/test_plugin.py tests/test_context.py -q`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add plugin tests/test_plugin.py src/linear_agents/context.py
git commit -s -m "Ship the subagent catalogue, gate hooks and skill changes in the plugin"
```

---

### Task 5: Session settings and launch environment

**Files:**
- Modify: `src/linear_agents/sessions.py` (`_DENY`, `claude_argv`, new `installed_plugins`, `allowed_plugins`)
- Modify: `src/linear_agents/supervisor.py` (`launch`, phase transitions, context check)
- Test: `tests/test_sessions.py`, `tests/test_supervisor.py`

**Interfaces:**
- Consumes: `Grants`, `parse_allow`, `merge`, `read`, `write` (Task 1).
- Produces:
  - `claude_argv(cfg, name, settings, prompt, env, claude_exe, exe, grants: Grants = Grants(), disabled_plugins: tuple[str, ...] = ())`
  - `installed_plugins(path: Path) -> tuple[str, ...]` (ids in `installed_plugins.json`; missing or bad file → `()`)
  - `allowed_plugins(path: Path) -> tuple[str, ...] | None` (lines of `deploy/agent-plugins.txt`; missing file → `None`, meaning "no override")
  - `launch(rt, claim, phase, extra="", started_at: float | None = None)`; environment gains `LINEAR_AGENTS_RUN=<phase>@<started_at>`, `CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH=1`, `CLAUDE_CODE_DISABLE_EXPLORE_PLAN_AGENTS=1`.

- [ ] **Step 1: Write the failing tests**

Add to `tests/test_sessions.py`:

```python
from linear_agents.grants import Grants
from linear_agents.sessions import allowed_plugins, installed_plugins


def settings_of(argv):
    return json.loads(argv[argv.index("--settings") + 1])


def test_settings_deny_general_purpose_questions_and_nested_claude():
    s = settings_of(claude_argv(CFG, "NF-1", IssueSettings(), "p", env={}, claude_exe="c", exe="x"))
    deny = s["permissions"]["deny"]
    for rule in ("Agent(general-purpose)", "AskUserQuestion", "Bash(claude:*)"):
        assert rule in deny
    assert s["disableWorkflows"] is True


def test_grants_lift_the_matching_launch_flags():
    g = Grants(agents=("general-purpose",), workflows=("sdd-waves",))
    s = settings_of(claude_argv(CFG, "NF-1", IssueSettings(), "p", env={}, claude_exe="c", exe="x", grants=g))
    assert "Agent(general-purpose)" not in s["permissions"]["deny"]
    assert s["disableWorkflows"] is False


def test_unlisted_plugins_are_disabled():
    s = settings_of(
        claude_argv(
            CFG, "NF-1", IssueSettings(), "p", env={}, claude_exe="c", exe="x",
            disabled_plugins=("glowup@glowup",),
        )
    )
    assert s["enabledPlugins"] == {"glowup@glowup": False}


def test_installed_and_allowed_plugin_files(tmp_path):
    inst = tmp_path / "installed_plugins.json"
    inst.write_text(json.dumps({"plugins": {"a@m": [{"scope": "user"}], "b@m": []}}))
    assert installed_plugins(inst) == ("a@m", "b@m")
    assert installed_plugins(tmp_path / "none.json") == ()
    allow = tmp_path / "agent-plugins.txt"
    allow.write_text("# comment\na@m\n\n")
    assert allowed_plugins(allow) == ("a@m",)
    assert allowed_plugins(tmp_path / "none.txt") is None
```

Add to `tests/test_supervisor.py`:

```python
def env_of(argv):
    i = argv.index("-i")
    return dict(x.split("=", 1) for x in argv[i + 1 :] if "=" in x and not x.startswith("/"))


def test_phase_sessions_share_one_run_id_across_relaunches(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    done(rt)
    supervise(rt, current(rt))
    started = current(rt).started_at
    env = env_of(rt.sessions.started[-1][1])
    assert env["LINEAR_AGENTS_RUN"] == f"impl@{started}"
    assert env["CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH"] == "1"
    assert env["CLAUDE_CODE_DISABLE_EXPLORE_PLAN_AGENTS"] == "1"
    rt.sessions.alive_names.clear()
    supervise(rt, current(rt))  # crash relaunch of the same phase
    assert env_of(rt.sessions.started[-1][1])["LINEAR_AGENTS_RUN"] == f"impl@{started}"


def test_launch_writes_block_grants(rt):
    add(rt, settings_json=settings_to_json(IssueSettings(allow="launches 12")))
    rt.sessions.alive_names.add("NF-1-test")
    done(rt)
    supervise(rt, current(rt))
    from linear_agents.grants import read

    assert read(state_dir(rt.cfg, "NF-1") / "grants.json").launches == 12


def test_no_handoff_before_the_first_compaction(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    d = state_dir(rt.cfg, "NF-1")
    write_status(d, usage_status(ctx=70))
    supervise(rt, current(rt))
    assert rt.sessions.sent == []
    (d / "compacted").touch()
    supervise(rt, current(rt))
    assert len(rt.sessions.sent) == 1 and "handoff" in rt.sessions.sent[0][1]


def test_relaunch_clears_the_compaction_marker(rt):
    add(rt)
    d = state_dir(rt.cfg, "NF-1")
    (d / "compacted").touch()
    supervise(rt, current(rt))  # session not alive: relaunch
    assert not (d / "compacted").exists()
```

Change `test_high_context_requests_handoff_once` to touch `state_dir(rt.cfg, "NF-1") / "compacted"` before the first `supervise`.

- [ ] **Step 2: Run the tests to see them fail**

Run: `uv run pytest tests/test_sessions.py tests/test_supervisor.py -q`
Expected: FAIL on the new tests (`ImportError: installed_plugins`, missing env keys, handoff sent before compaction).

- [ ] **Step 3: Implement**

`src/linear_agents/sessions.py`:

```python
from linear_agents.grants import Grants

_DENY = [
    "Bash(git commit --no-verify:*)",
    "Bash(git commit -n:*)",
    "Bash(git commit * --no-verify*)",
    "Bash(git commit * -n *)",
    "Bash(git commit * -n)",
    # A second claude process is a session the gate and the usage caps never see.
    "Bash(claude:*)",
    # Unattended, a question waits for nobody until the wall clock; agents escalate instead.
    "AskUserQuestion",
]
_GENERAL_DENY = "Agent(general-purpose)"


def installed_plugins(path: Path) -> tuple[str, ...]:
    try:
        return tuple(json.loads(path.read_text()).get("plugins", {}))
    except (OSError, ValueError, AttributeError):
        return ()


def allowed_plugins(path: Path) -> tuple[str, ...] | None:
    if not path.exists():
        return None
    lines = (ln.strip() for ln in path.read_text().splitlines())
    return tuple(ln for ln in lines if ln and not ln.startswith("#"))
```

In `claude_argv`, add parameters `grants: Grants = Grants()` and `disabled_plugins: tuple[str, ...] = ()`, and build the settings as:

```python
    deny = list(_DENY)
    if "general-purpose" not in grants.agents:
        deny.append(_GENERAL_DENY)
    session_settings = {
        "statusLine": {"type": "command", "command": f"{exe} statusline"},
        "autoContinueAtUsageLimit": True,
        "attribution": False,
        "permissions": {"deny": deny},
        "disableWorkflows": not grants.workflows,
    }
    if disabled_plugins:
        session_settings["enabledPlugins"] = {p: False for p in disabled_plugins}
```

`src/linear_agents/supervisor.py`:

```python
from linear_agents import grants as grants_mod
from linear_agents.sessions import allowed_plugins, installed_plugins


def _disabled_plugins(rt) -> tuple[str, ...]:
    allowed = allowed_plugins(rt.cfg.plugin_dir.parent / "deploy" / "agent-plugins.txt")
    if allowed is None:
        return ()
    installed = installed_plugins(Path.home() / ".claude" / "plugins" / "installed_plugins.json")
    return tuple(p for p in installed if p not in allowed)
```

In `launch`, add the `started_at: float | None = None` parameter and:

```python
    start = claim.started_at if started_at is None else started_at
    env = clean_env(
        {
            "LINEAR_AGENTS_STATE": str(d),
            "LINEAR_AGENTS_CONFIG": str(rt.config_path),
            "LINEAR_AGENTS_PHASE": phase,
            # Handoffs and crash relaunches of one phase share its launch budget.
            "LINEAR_AGENTS_RUN": f"{phase}@{start}",
            "CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH": "1",
            "CLAUDE_CODE_DISABLE_EXPLORE_PLAN_AGENTS": "1",
        }
    )
    settings = settings_from_json(claim.settings_json)
    granted = grants_mod.read(d / "grants.json")
    if settings.allow:
        granted = grants_mod.merge(granted, grants_mod.parse_allow(settings.allow))
```

pass `grants=granted, disabled_plugins=_disabled_plugins(rt)` to `claude_argv`, and after the marker deletes:

```python
    grants_mod.write(d / "grants.json", granted)
```

Change the leftover delete to `for leftover in (*_MARKERS, "status.json", "compacted"):`.

At the three phase transitions in `_finish_phase` and `_start_review`, compute `start = rt.now()` once, pass `started_at=start` to `launch`, and use `started_at=start` in the matching `ledger.update_claim`.

In `supervise`, change the context check to:

```python
    pct = context_pct(read_status(d) or {})
    requested = d / "handoff-requested"
    # One compaction first: the handoff applies only once the session has compacted.
    if (
        pct is not None
        and pct >= rt.cfg.context_restart_pct
        and (d / "compacted").exists()
        and not requested.exists()
    ):
```

- [ ] **Step 4: Run the tests**

Run: `uv run pytest -q`
Expected: PASS (all tests)

- [ ] **Step 5: Commit**

```bash
git add src/linear_agents/sessions.py src/linear_agents/supervisor.py tests/test_sessions.py tests/test_supervisor.py
git commit -s -m "Launch sessions with run ids, subagent limits, grants and the plugin allowlist"
```

---

### Task 6: Grants from owner comments

**Files:**
- Modify: `src/linear_agents/poller.py` (`forward_comments`, `_start`)
- Test: `tests/test_poller.py`

**Interfaces:**
- Consumes: `allow_lines`, `parse_allow`, `merge`, `read`, `write`, `GrantError` (Task 1).
- Produces: `grants.json` updated from comments; `_start` clears `grants.json`, `scratchpads` and `compacted` left by a previous claim.

- [ ] **Step 1: Write the failing tests**

Add to `tests/test_poller.py`:

```python
from linear_agents.grants import read as read_grants


def test_owner_allow_comment_grants_and_is_still_forwarded(rt):
    ledger.insert_claim(rt.con, make("NF-1", ()))
    rt.linear.replies = {
        "id-NF-1": [
            Comment("c1", "go ahead\nallow: launches 14", "2026-10-09T10:00:00Z", mine=True),
            Comment("c2", "allow: agent general-purpose", "2026-10-09T10:01:00Z", mine=False, author="x"),
        ]
    }
    rt.sessions.alive_names.add("NF-1-test")
    forward_comments(rt)
    g = read_grants(state_dir(rt.cfg, "NF-1") / "grants.json")
    assert g.launches == 14 and g.agents == ()
    assert rt.sessions.sent[0][1].startswith("The owner replied on Linear: go ahead")


def test_bad_allow_comment_is_answered_once_and_keeps_grants(rt):
    ledger.insert_claim(rt.con, make("NF-1", ()))
    rt.linear.replies = {
        "id-NF-1": [
            Comment("c1", "allow: launches 9", "2026-10-09T10:00:00Z", mine=True),
            Comment("c2", "allow: launches lots", "2026-10-09T10:01:00Z", mine=True),
        ]
    }
    rt.sessions.alive_names.add("NF-1-test")
    forward_comments(rt)
    assert read_grants(state_dir(rt.cfg, "NF-1") / "grants.json").launches == 9
    assert any("launches lots" in body for _, body in rt.linear.comments)
```

(If `PollLinear` does not record comments as `rt.linear.comments` of `(issue_id, body)`, use the attribute it does record them in; check `tests/test_poller.py`'s `PollLinear`.)

```python
def test_a_new_claim_starts_without_the_last_claims_grants(rt):
    d = state_dir(rt.cfg, "NF-9")
    d.mkdir(parents=True)
    for name in ("grants.json", "scratchpads", "compacted"):
        (d / name).write_text("{}")
    rt.linear.ready = [issue()]
    poll(rt)
    assert not (d / "scratchpads").exists() and not (d / "compacted").exists()
    assert read_grants(d / "grants.json").launches is None
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `uv run pytest tests/test_poller.py -q`
Expected: FAIL on the three new tests.

- [ ] **Step 3: Implement**

In `forward_comments`, inside `if who and not comment.body.startswith(BOT_PREFIX):` and before the alive check:

```python
                _apply_grants(rt, claim, comment.body)
```

with:

```python
def _apply_grants(rt, claim: Claim, body: str) -> None:
    path = state_dir(rt.cfg, claim.issue_key) / "grants.json"
    current = grants_read(path)
    for line in allow_lines(body):
        try:
            current = merge(current, parse_allow(line))
        except GrantError as exc:
            try:
                rt.linear.comment(claim.issue_id, f"{BOT_PREFIX} grant not applied: {exc}")
            except LinearError as err:
                print(f"{claim.issue_key}: grant reply not posted: {err}", file=sys.stderr)
    path.parent.mkdir(parents=True, exist_ok=True)
    grants_write(path, current)
```

Imports: `from linear_agents.grants import GrantError, allow_lines, merge, parse_allow` and `from linear_agents.grants import read as grants_read, write as grants_write`.

In `_start`, next to the `handoff.md` move:

```python
    # A previous claim's grants, scratchpads and compaction belong to that run.
    for stale in ("grants.json", "scratchpads", "compacted"):
        (d / stale).unlink(missing_ok=True)
```

(Task 8's cleanup runs earlier in the same tick, so the listing is already acted on when `_start` removes it.)

- [ ] **Step 4: Run the tests**

Run: `uv run pytest -q`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/linear_agents/poller.py tests/test_poller.py
git commit -s -m "Take subagent grants from the owner's allow comments"
```

---

### Task 7: `linear-agents commit`

**Files:**
- Modify: `src/linear_agents/cli.py`
- Test: `tests/test_cli.py`

**Interfaces:**
- Produces: `linear-agents commit -m MSG -- PATH...` — under an exclusive `flock` on `<git-dir>/la-commit.lock`, runs `git add -- PATH...` then `git commit -m MSG -- PATH...` in the current worktree; returns git's exit code.

- [ ] **Step 1: Write the failing test**

Add to `tests/test_cli.py`:

```python
import threading
import subprocess


def test_parallel_commits_take_only_their_own_paths(origin, monkeypatch):
    monkeypatch.chdir(origin)
    for name in ("a1.txt", "a2.txt", "b1.txt", "b2.txt"):
        (origin / name).write_text(name)

    def commit(msg, paths):
        assert main(["commit", "-m", msg, "--", *paths]) == 0

    t1 = threading.Thread(target=commit, args=("A", ["a1.txt", "a2.txt"]))
    t2 = threading.Thread(target=commit, args=("B", ["b1.txt", "b2.txt"]))
    t1.start(); t2.start(); t1.join(); t2.join()
    for msg, files in (("A", {"a1.txt", "a2.txt"}), ("B", {"b1.txt", "b2.txt"})):
        sha = subprocess.run(
            ["git", "log", "-1", "--format=%H", f"--grep=^{msg}$"], cwd=origin,
            capture_output=True, text=True, check=True,
        ).stdout.strip()
        shown = subprocess.run(
            ["git", "show", "--name-only", "--format=", sha], cwd=origin,
            capture_output=True, text=True, check=True,
        ).stdout.split()
        assert set(shown) == files
```

- [ ] **Step 2: Run the test to see it fail**

Run: `uv run pytest tests/test_cli.py -q -k parallel_commits`
Expected: FAIL, `invalid choice: 'commit'`.

- [ ] **Step 3: Implement**

```python
def cmd_commit(args) -> int:
    import fcntl

    from linear_agents.worktree import git

    paths = [p for p in args.paths if p != "--"]
    if not paths:
        print("commit: name the paths to commit after --", file=sys.stderr)
        return 2
    git_dir = Path(git("rev-parse", "--absolute-git-dir", cwd=Path.cwd()))
    # Parallel task-implementers share one worktree and its index; one commit at a time, each
    # with only its own paths.
    with (git_dir / "la-commit.lock").open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        added = subprocess.run(["git", "add", "--", *paths], check=False)
        if added.returncode:
            return added.returncode
        return subprocess.run(["git", "commit", "-m", args.message, "--", *paths], check=False).returncode
```

Register `"commit": cmd_commit` and:

```python
    com = add("commit", "Commit only the given paths, one commit at a time per worktree")
    com.add_argument("-m", dest="message", required=True, help="commit message")
    com.add_argument("paths", nargs=argparse.REMAINDER, help="-- then the paths to commit")
```

- [ ] **Step 4: Run the tests**

Run: `uv run pytest tests/test_cli.py -q`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/linear_agents/cli.py tests/test_cli.py
git commit -s -m "Add a locked commit helper for parallel implementers"
```

---

### Task 8: Scratchpad cleanup

**Files:**
- Modify: `src/linear_agents/poller.py` (new `clean_scratchpads`), `src/linear_agents/tick.py` (call it after `reap_worktrees`)
- Test: `tests/test_poller.py`

**Interfaces:**
- Consumes: `<state_dir>/scratchpads` written by `session-start` (Task 3).
- Produces: `clean_scratchpads(rt) -> None`.

- [ ] **Step 1: Write the failing tests**

```python
from linear_agents.poller import clean_scratchpads


def pads(rt, key, paths):
    d = state_dir(rt.cfg, key)
    d.mkdir(parents=True, exist_ok=True)
    (d / "scratchpads").write_text("".join(f"{p}\n" for p in paths))
    return d


def test_cleanup_removes_a_finished_runs_scratchpads_only(rt, tmp_path, monkeypatch):
    monkeypatch.setattr("linear_agents.poller.SCRATCH_PREFIX", str(tmp_path / "claude-"))
    done_pad = tmp_path / "claude-1000" / "a" / "s1"
    live_pad = tmp_path / "claude-1000" / "b" / "s2"
    done_pad.mkdir(parents=True)
    live_pad.mkdir(parents=True)
    ledger.insert_claim(rt.con, make("NF-1", (), state="blocked"))
    ledger.insert_claim(rt.con, make("NF-2", ()))
    d1 = pads(rt, "NF-1", [done_pad])
    pads(rt, "NF-2", [live_pad])
    clean_scratchpads(rt)
    assert not done_pad.exists() and live_pad.exists()
    assert not (d1 / "scratchpads").exists()


def test_cleanup_never_deletes_outside_tmp_claude(rt, tmp_path):
    outside = tmp_path / "keep"
    outside.mkdir()
    ledger.insert_claim(rt.con, make("NF-1", (), state="done"))
    pads(rt, "NF-1", [outside, "/", "/tmp/claude-1000/../etc"])
    clean_scratchpads(rt)
    assert outside.exists()
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `uv run pytest tests/test_poller.py -q -k cleanup`
Expected: FAIL, `ImportError: cannot import name 'clean_scratchpads'`.

- [ ] **Step 3: Implement**

In `src/linear_agents/poller.py`:

```python
SCRATCH_PREFIX = "/tmp/claude-"


def clean_scratchpads(rt) -> None:
    """Removes the Claude Code scratchpads of claims that are no longer running."""
    for listing in rt.cfg.state_root.glob("*/scratchpads"):
        claim = ledger.get_claim(rt.con, listing.parent.name)
        if claim is not None and claim.state == "running":
            continue
        for line in listing.read_text().splitlines():
            path = Path(line.strip()).resolve()
            # Only Claude Code's own scratch tree; a damaged record must not reach anything else.
            if str(path).startswith(SCRATCH_PREFIX) and path != Path(SCRATCH_PREFIX).parent:
                shutil.rmtree(path, ignore_errors=True)
        listing.unlink(missing_ok=True)
```

In `src/linear_agents/tick.py`, import `clean_scratchpads` with the other poller names and call `clean_scratchpads(rt)` right after `reap_worktrees(rt)`. That is before `poll(rt)`, so a finished claim's scratchpads are removed before `_start` (Task 6) drops its listing for a rerun.

- [ ] **Step 4: Run the tests**

Run: `uv run pytest -q`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/linear_agents/poller.py src/linear_agents/tick.py tests/test_poller.py
git commit -s -m "Remove a finished run's Claude Code scratchpads"
```

---

### Task 9: Installer: plugin allowlist, Claude settings, tmpfiles, build tools

**Files:**
- Create: `deploy/agent-plugins.txt`, `installer/internal/steps/scratch.go`, `installer/internal/steps/scratch_test.go`
- Modify: `installer/internal/ship/plugins.go`, `installer/internal/ship/ship.go:80`, `installer/internal/ship/ship_test.go`, `installer/internal/steps/claude.go` (`minClaude`, `pushSettings`, `loginNote`, the version message), `installer/internal/steps/claude_test.go`, `installer/internal/steps/list.go`, `installer/internal/steps/packages.go`, `installer/internal/steps/root_test.go`

**Interfaces:**
- Produces:
  - `planPlugins(configDir string, allow []string, private func(string) (bool, error)) (planned, error)`; `allow == nil` keeps the old behaviour; a listed plugin that is not enabled is an error naming it.
  - `readAllowlist(path string) ([]string, error)` (missing file → `nil, nil`)
  - `type Scratch struct{}`, a root step writing `/etc/tmpfiles.d/claude-scratch.conf` in systemd mode.

- [ ] **Step 1: Write the failing tests**

In `installer/internal/ship/ship_test.go`, change the existing call to `planPlugins(dir, nil, func...)` and add:

```go
func TestPlanPluginsKeepsOnlyTheAllowlist(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{
		"docket@NovusEdge": true, "curt@curt": true}})
	writeJSON(t, filepath.Join(dir, "plugins/known_marketplaces.json"), map[string]any{
		"NovusEdge": map[string]any{"source": map[string]string{"source": "github", "repo": "NovusEdge/docket"}},
		"curt":      map[string]any{"source": map[string]string{"source": "github", "repo": "o/curt"}},
	})
	pub := func(string) (bool, error) { return false, nil }
	p, err := planPlugins(dir, []string{"docket@NovusEdge"}, pub)
	if err != nil || !slices.Equal(p.List.Plugins, []string{"docket@NovusEdge"}) || len(p.List.Marketplaces) != 1 {
		t.Fatalf("%+v %v", p.List, err)
	}
	if _, err := planPlugins(dir, []string{"ghost@x"}, pub); err == nil || !strings.Contains(err.Error(), "ghost@x") {
		t.Fatalf("want an error naming ghost@x, got %v", err)
	}
}

func TestReadAllowlistSkipsCommentsAndBlanks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-plugins.txt")
	os.WriteFile(path, []byte("# agents\ndocket@NovusEdge\n\ncurt@curt\n"), 0o644)
	got, err := readAllowlist(path)
	if err != nil || !slices.Equal(got, []string{"docket@NovusEdge", "curt@curt"}) {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := readAllowlist(filepath.Join(t.TempDir(), "none")); got != nil || err != nil {
		t.Fatalf("missing file: %v %v", got, err)
	}
}
```

In `installer/internal/steps/claude_test.go`, extend `TestClaudeApplyWritesPushSettings` (or add a test) to assert the written settings contain `"remoteControlAtStartup": true`, and add:

```go
func TestMinimumClaudeIsTheGateVersion(t *testing.T) {
	if minClaude != [3]int{2, 1, 295} {
		t.Fatalf("minClaude %v", minClaude)
	}
}
```

`installer/internal/steps/scratch_test.go`:

```go
package steps

import (
	"context"
	"os"
	"testing"

	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

func TestScratchWritesTheTmpfilesRuleInSystemdMode(t *testing.T) {
	e := testEnv(t, host.NewFake())
	if done, _, _ := (Scratch{}).Check(context.Background(), e); done {
		t.Fatal("done before apply")
	}
	if err := (Scratch{}).Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(e.Path(tmpfilesConf))
	if string(b) != tmpfilesLine {
		t.Fatalf("%q", b)
	}
	if done, _, _ := (Scratch{}).Check(context.Background(), e); !done {
		t.Fatal("not done after apply")
	}
}

func TestScratchIsSkippedInLoopMode(t *testing.T) {
	e := testEnv(t, host.NewFake())
	e.Mode = Loop
	if done, detail, _ := (Scratch{}).Check(context.Background(), e); !done || detail != "loop mode; cleaned by the service" {
		t.Fatalf("%v %q", done, detail)
	}
}
```

In `installer/internal/steps/root_test.go`, update `TestPackagesCheckNamesMissingTools` so the fake also answers `command -v make` and `command -v cc`, and add a case that a host missing them reports `missing: make, cc` and installs `make gcc`.

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./installer/...`
Expected: FAIL (wrong `planPlugins` arity, missing `readAllowlist`, `Scratch`, version).

- [ ] **Step 3: Implement**

`deploy/agent-plugins.txt`:

```
# Plugins agent sessions on linear-agents hosts get, one name@marketplace per line.
# `just ship` installs only these; sessions disable any other installed plugin.
docket@NovusEdge
superpowers@claude-plugins-official
software-architecture@software-architecture
ponytail@ponytail
curt@curt
```

`installer/internal/ship/plugins.go`: add the `allow []string` parameter; after building `names`, when `allow != nil`:

```go
	if allow != nil {
		enabled := map[string]bool{}
		for _, n := range names {
			enabled[n] = true
		}
		for _, a := range allow {
			if !enabled[a] {
				return p, fmt.Errorf("%s is in deploy/agent-plugins.txt but not enabled in %s", a, configDir)
			}
		}
		names = slices.Clone(allow)
		slices.Sort(names)
	}
```

and:

```go
func readAllowlist(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out, nil
}
```

`installer/internal/ship/ship.go:80`:

```go
	allow, err := readAllowlist(filepath.Join(root, "deploy", "agent-plugins.txt"))
	if err != nil {
		return err
	}
	plan, err := planPlugins(configDir, allow, private)
```

`installer/internal/steps/claude.go`: `var minClaude = [3]int{2, 1, 295}` with its comment changed to "The gate hook's onFailure needs this version."; the version message `"needs 2.1.295"`; add `"remoteControlAtStartup": true` to `pushSettings` (rename the comment to "the /config toggles and Remote Control at startup"); drop step 2 from `loginNote` and renumber.

`installer/internal/steps/scratch.go`:

```go
package steps

import (
	"context"
	"os"
	"path/filepath"
)

const (
	tmpfilesConf = "/etc/tmpfiles.d/claude-scratch.conf"
	// Claude Code scratchpads untouched for two days; the service removes a run's own sooner.
	tmpfilesLine = "e /tmp/claude-* - - - 2d\n"
)

type Scratch struct{}

func (Scratch) Name() string { return "Scratch cleanup" }

func (Scratch) Check(ctx context.Context, e *Env) (bool, string, error) {
	if e.Mode != Systemd {
		return true, "loop mode; cleaned by the service", nil
	}
	b, err := os.ReadFile(e.Path(tmpfilesConf))
	if err == nil && string(b) == tmpfilesLine {
		return true, tmpfilesConf, nil
	}
	return false, "no tmpfiles rule", nil
}

func (Scratch) Apply(ctx context.Context, e *Env) error {
	if e.Mode != Systemd {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(e.Path(tmpfilesConf)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(e.Path(tmpfilesConf), []byte(tmpfilesLine), 0o644)
}
```

`installer/internal/steps/list.go`: `RootSteps` returns `[]Step{Packages{}, Tailscale{}, User{}, Scratch{}}`.

`installer/internal/steps/packages.go`: `var tools = []string{"git", "tmux", "curl", "gh", "node", "make", "cc"}`, and in `pkgName` add `case tool == "cc": return "gcc"`. Add a comment on `tools`: repos' ./setup builds native extensions (pgvector in nimblefox) and needs make and a C compiler.

- [ ] **Step 4: Run the tests**

Run: `go test ./... && uv run pytest -q`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add deploy/agent-plugins.txt installer
git commit -s -m "Ship only allowlisted plugins, enable Remote Control and add scratch cleanup to hosts"
```

---

### Task 10: Docs, ledger and host verification

**Files:**
- Modify: `docs/vm-setup.md`, `deploy/config.example.toml` (no new key; check the comments still match), `docs/superpowers/specs/2026-10-20-agent-loop-design.md` (Unconfirmed results)

- [ ] **Step 1: Document**

In `docs/vm-setup.md`, add under "What the installer cannot do":

```markdown
- On your desktop, add the same scratchpad backstop the installer writes on hosts:
  `echo 'e /tmp/claude-* - - - 2d' | sudo tee /etc/tmpfiles.d/claude-scratch.conf`.
- `deploy/agent-plugins.txt` decides which of your plugins agent sessions get. A plugin listed
  there must be enabled on your desktop, or `just ship` stops and names it.
```

and a section "Subagents in agent sessions" with three sentences: the catalogue lives in `plugin/agents/`, the budgets in `src/linear_agents/gate.py`, every launch is logged in `<state_dir>/launches.jsonl`, and grants are an `allow:` line in the agent block or in a comment.

- [ ] **Step 2: Ship and verify on la-nimble**

Run, from the desktop, once the branch is merged:

```bash
just ship root@la-nimble --skip-interactive
just host root@la-nimble claude --version
```

Then check each Unconfirmed item on the host, recording the answer in the spec's Unconfirmed section (replace the list item with the result and the date):

1. `onFailure` key spelling: start a session in a scratch worktree with `LINEAR_AGENTS_STATE` unset and confirm an `Agent` call is refused (the gate exits non-zero). If it is allowed, the key is wrong: fix `hooks.json` from the hooks doc and repeat.
2. Workflow input: in a host session with a `workflow` grant, start a workflow and read the logged `what` field in `launches.jsonl`; if the name is absent, record which field carries it and update `decide`.
3. `maxTurns` / `effort` honoured for plugin agents: run `linear-agents:source-finder` once and check its transcript's turn count stays under 20.
4. `enabledPlugins` merge: run `/plugin` in a host session and confirm glowup is disabled.
5. `remoteControlAtStartup`: on a fresh images:debian/13 VM, ship with `--skip-interactive` after a login and confirm a launched session appears in the Claude app without a prompt.

- [ ] **Step 3: Commit**

```bash
git add docs deploy
git commit -s -m "Document subagent limits, the plugin allowlist and the desktop scratch rule"
```
