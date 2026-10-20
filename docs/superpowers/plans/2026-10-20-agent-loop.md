# Agent Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Host agent sessions delegate only to catalogue subagents within per-run, per-phase budgets enforced by a fail-closed gate, take grants from the agent block or owner comments, ask blocking questions in the Claude app, compact once before handoff, load an allowlisted plugin set, connect to Remote Control only in the implementer and fix phases, and clean up their scratchpads.

**Architecture:** A pure decision module (`gate.py`) holds the catalogue budgets and rules; a thin CLI wraps it as Claude Code hooks (`gate`, `gate --stopped`, `gate --answered`, `session-start`) that read and append `<state_dir>/launches.jsonl` under a `flock`. Grants live in `<state_dir>/grants.json` (`grants.py`), written by the supervisor from the agent block and by `forward_comments` from owner comments, and resolved per phase. `claude_argv` and `launch()` add the environment, deny rules and plugin overrides; the supervisor gates the handoff on a compaction marker and escalates stale questions; the installer gains the plugin allowlist, settings and a tmpfiles step.

**Tech Stack:** Python 3.13 (uv, pytest, ruff with line length 100, pyrefly), Go installer (`go test`), Claude Code 2.1.295+ hooks and plugin agents.

**Spec:** `docs/superpowers/specs/2026-10-20-agent-loop-design.md`

## Global Constraints

- Applies to host agent sessions only; nothing in the owner's desktop config changes, except the documented manual tmpfiles line.
- Claude Code minimum on hosts: 2.1.295 (was 2.1.293).
- Catalogue agent names are `linear-agents:<name>`; the gate also accepts the bare `<name>`.
- Budgets, verbatim: test 2 launches / 2 running (`source-finder`, `source-reader`); impl 10 / 4 (`task-implementer`, `wave-reviewer`, `fix-rereviewer`, `plan-reviewer`, `code-mapper`, `source-finder`, `source-reader`); fix 2 / 1 (`task-implementer`, `fix-rereviewer`); review 0 / 0.
- Models and effort, verbatim: `source-finder` haiku/low; `source-reader`, `code-mapper`, `task-implementer`, `fix-rereviewer` sonnet/medium; `plan-reviewer`, `wave-reviewer` opus/high.
- Do not set `CLAUDE_CODE_SUBAGENT_MODEL_FORCE`.
- Grants come only from the agent block, the owner's comments (`comment.mine`) and `steer_authors`. A comment `launches` grant applies to the phase running when it arrives; a block `launches` grant applies to `impl`; other grants apply to the claim. A comment grant wins over the block.
- The gate makes no network call, does not open the ledger, and refuses (exit 2) on any error of its own.
- Scratchpad cleanup deletes only `/tmp/claude-<uid>/<a>/<b>/<c...>` paths (three or more levels below the uid root).
- A question unanswered for 7200 s becomes one tier 2 escalation.
- Put every import at the top of its module, including in tests (ruff E402); one statement per line (E702).
- Commits: `git commit -s`, no `Co-Authored-By`, no AI attribution.
- Every task ends green on `uv run pytest -q`, `uv run ruff check`, `uv run ruff format --check`, `uv run pyrefly check` and, for Go tasks, `go test ./...`.

## Review Focus

1. A launch naming `task-implementer` without the `linear-agents:` prefix must be treated as the same catalogue agent, not refused as unknown (Task 2 test `test_bare_name_matches_catalogue`).
2. A truncated last line in `launches.jsonl` (a hook killed mid-write) must neither crash the gate nor swallow the next entry (Task 3 test `test_torn_line_is_skipped_and_the_next_entry_kept`).
3. Two separate processes racing for the last launch slot must not both be allowed (Task 3 test `test_processes_racing_for_the_last_slot`).
4. An owner comment with `Allow:` in another case is a grant; `allow:` after other text on a line is not (Task 1 test `test_allow_lines_only_at_line_start`).
5. A recorded scratchpad path that is the uid root, a project directory, outside the tree, or blank must never be deleted (Task 8 test `test_cleanup_only_removes_full_session_paths`).

---

### Task 1: Grants and the `allow` agent-block key

**Files:**
- Create: `src/linear_agents/grants.py`
- Modify: `src/linear_agents/agentblock.py` (`_KEYS`, `IssueSettings`, `parse_settings`, `settings_from_json`)
- Test: `tests/test_grants.py` (create), `tests/test_agentblock.py`

**Interfaces:**
- Produces:
  - `class GrantError(ValueError)`
  - `@dataclass(frozen=True) class Grants: launches: int | None = None; agents: tuple[str, ...] = (); workflows: tuple[str, ...] = (); fix_rounds: int | None = None`
  - `parse_allow(text: str) -> Grants` (raises `GrantError` whose message contains the offending item)
  - `allow_lines(body: str) -> list[str]`
  - `merge(a: Grants, b: Grants) -> Grants` (b's numbers win when set; names unioned in order)
  - `set_block(path: Path, g: Grants) -> None` (records the agent block's grants; keeps comment grants)
  - `add_comment(path: Path, g: Grants, phase: str) -> None` (merges a comment's grants; its `launches` is stored for `phase`)
  - `for_phase(path: Path, phase: str) -> Grants` (missing or corrupt file → `Grants()`)
  - `IssueSettings.allow: str | None = None`

- [ ] **Step 1: Write the failing tests**

`tests/test_grants.py`:

```python
import pytest

from linear_agents.grants import (
    GrantError,
    Grants,
    add_comment,
    allow_lines,
    for_phase,
    merge,
    parse_allow,
    set_block,
)


def test_parse_allow_reads_every_item():
    g = parse_allow("launches 10, agent general-purpose, workflow big-build, fix-rounds 2")
    assert g == Grants(
        launches=10, agents=("general-purpose",), workflows=("big-build",), fix_rounds=2
    )


@pytest.mark.parametrize("bad", ["launches ten", "launches ²", "agents x", "fix-rounds", "workflow"])
def test_parse_allow_errors_name_the_item(bad):
    with pytest.raises(GrantError, match=bad.split()[0]) as exc:
        parse_allow(bad)
    assert bad in str(exc.value)


def test_allow_lines_only_at_line_start():
    body = "thanks!\n  Allow: launches 4\nplease allow: agent x\nallow:agent code-mapper"
    assert allow_lines(body) == ["launches 4", "agent code-mapper"]


def test_merge_later_numbers_win_and_names_union():
    a = Grants(launches=4, agents=("a",), fix_rounds=2)
    b = Grants(launches=8, agents=("b", "a"))
    assert merge(a, b) == Grants(launches=8, agents=("a", "b"), fix_rounds=2)


def test_missing_or_corrupt_file_is_no_grants(tmp_path):
    assert for_phase(tmp_path / "none.json", "impl") == Grants()
    bad = tmp_path / "grants.json"
    bad.write_text("{half")
    assert for_phase(bad, "impl") == Grants()


def test_block_launches_apply_to_impl_only(tmp_path):
    p = tmp_path / "grants.json"
    set_block(p, Grants(launches=14, agents=("code-mapper",)))
    assert for_phase(p, "impl") == Grants(launches=14, agents=("code-mapper",))
    assert for_phase(p, "test") == Grants(agents=("code-mapper",))


def test_comment_launches_are_scoped_to_their_phase_and_beat_the_block(tmp_path):
    p = tmp_path / "grants.json"
    set_block(p, Grants(launches=10, fix_rounds=1))
    add_comment(p, Grants(launches=16, fix_rounds=2), "impl")
    add_comment(p, Grants(launches=3), "fix")
    set_block(p, Grants(launches=10, fix_rounds=1))  # a relaunch rewrites the block
    assert for_phase(p, "impl") == Grants(launches=16, fix_rounds=2)
    assert for_phase(p, "fix").launches == 3
    assert for_phase(p, "test").launches is None
```

Add to `tests/test_agentblock.py` (imports at the top of the file):

```python
def test_allow_key_is_kept_raw_and_validated():
    s = parse_settings("```agent\nallow: launches 4, agent code-mapper\n```")
    assert s.allow == "launches 4, agent code-mapper"
    assert settings_from_json(settings_to_json(s)).allow == s.allow
    with pytest.raises(BlockError, match="allow"):
        parse_settings("```agent\nallow: launches lots\n```")
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `uv run pytest tests/test_grants.py tests/test_agentblock.py -q`
Expected: FAIL, `ModuleNotFoundError: linear_agents.grants` and `unknown keys ['allow']`.

- [ ] **Step 3: Implement**

`src/linear_agents/grants.py`:

```python
import json
import os
import re
from dataclasses import dataclass
from pathlib import Path

_LINE = re.compile(r"^[ \t]*allow:[ \t]*(.+)$", re.IGNORECASE | re.MULTILINE)
_NUMBER = re.compile(r"[0-9]+")


class GrantError(ValueError):
    pass


@dataclass(frozen=True)
class Grants:
    launches: int | None = None
    agents: tuple[str, ...] = ()
    workflows: tuple[str, ...] = ()
    fix_rounds: int | None = None


def parse_allow(text: str) -> Grants:
    launches = fix_rounds = None
    agents: list[str] = []
    workflows: list[str] = []
    for item in (i.strip() for i in text.split(",")):
        if not item:
            continue
        word, _, value = item.partition(" ")
        value = value.strip()
        if word in ("launches", "fix-rounds"):
            if not _NUMBER.fullmatch(value):
                raise GrantError(f"allow: {item!r} needs a whole number")
            if word == "launches":
                launches = int(value)
            else:
                fix_rounds = int(value)
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


def _load(path: Path) -> dict:
    # The gate refuses on its own errors, so a damaged file reads as "no grants", never raises.
    try:
        raw = json.loads(path.read_text())
    except (OSError, ValueError):
        return {}
    return raw if isinstance(raw, dict) else {}


def _save(path: Path, raw: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f"{path.name}.tmp")
    tmp.write_text(json.dumps(raw))
    os.replace(tmp, path)


def _grants(raw: dict) -> Grants:
    try:
        return Grants(
            launches=raw.get("launches"),
            agents=tuple(raw.get("agents", ())),
            workflows=tuple(raw.get("workflows", ())),
            fix_rounds=raw.get("fix_rounds"),
        )
    except (AttributeError, TypeError):
        return Grants()


def _without_launches(g: Grants) -> Grants:
    return Grants(None, g.agents, g.workflows, g.fix_rounds)


def set_block(path: Path, g: Grants) -> None:
    raw = _load(path)
    raw["block"] = {
        "launches": g.launches,
        "agents": list(g.agents),
        "workflows": list(g.workflows),
        "fix_rounds": g.fix_rounds,
    }
    _save(path, raw)


def add_comment(path: Path, g: Grants, phase: str) -> None:
    raw = _load(path)
    c = raw.get("comments") if isinstance(raw.get("comments"), dict) else {}
    launches = c.get("launches") if isinstance(c.get("launches"), dict) else {}
    if g.launches is not None:
        launches[phase] = g.launches
    merged = merge(_grants({**c, "launches": None}), _without_launches(g))
    raw["comments"] = {
        "launches": launches,
        "agents": list(merged.agents),
        "workflows": list(merged.workflows),
        "fix_rounds": merged.fix_rounds,
    }
    _save(path, raw)


def for_phase(path: Path, phase: str) -> Grants:
    raw = _load(path)
    block = _grants(raw.get("block") or {})
    comments_raw = raw.get("comments") if isinstance(raw.get("comments"), dict) else {}
    launches = comments_raw.get("launches")
    per_phase = launches if isinstance(launches, dict) else {}
    comments = _grants({**comments_raw, "launches": per_phase.get(phase)})
    # A launch grant in the agent block is for the implementer, which spends the budget.
    base = block if phase == "impl" else _without_launches(block)
    return merge(base, comments)
```

In `src/linear_agents/agentblock.py`: add `"allow"` to `_KEYS`; add `allow: str | None = None` to `IssueSettings`; add `from linear_agents.grants import GrantError, parse_allow` to the imports; in `parse_settings`, before the `return`:

```python
    allow = values.get("allow") or None
    if allow is not None:
        try:
            parse_allow(allow)
        except GrantError as exc:
            raise BlockError(f"agent block: {exc}") from exc
```

pass `allow=allow` in the returned `IssueSettings`, and in `settings_from_json` add `allow=raw.get("allow")`.

- [ ] **Step 4: Run the tests**

Run: `uv run pytest tests/test_grants.py tests/test_agentblock.py -q`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/linear_agents/grants.py src/linear_agents/agentblock.py tests/test_grants.py tests/test_agentblock.py
git commit -s -m "Parse subagent grants and scope launch grants to a phase"
```

---

### Task 2: Gate decisions

**Files:**
- Create: `src/linear_agents/gate.py`
- Test: `tests/test_gate.py` (create)

**Interfaces:**
- Consumes: `Grants` (Task 1).
- Produces:
  - `@dataclass(frozen=True) class Budget: agents: tuple[str, ...]; launches: int; running: int`
  - `BUDGETS: dict[str, Budget]`; `CATALOGUE: frozenset[str]` (every agent in any budget)
  - `PREFIX = "linear-agents:"`; `GATED_TOOLS = frozenset({"Agent", "Task", "Workflow"})`
  - `@dataclass(frozen=True) class Decision: allowed: bool; rule: str; what: str = ""; reason: str = ""`
  - `subject(payload: dict) -> str` (the bare agent name, `general-purpose` when missing, or the workflow name)
  - `decide(payload: dict, phase: str | None, grants: Grants, launched: int, running: int, refused: set[tuple[str, str]]) -> Decision` — `refused` holds `(rule, subject)` pairs already refused in this run.

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


def test_tools_outside_the_gate_are_allowed():
    assert d({"tool_name": "TaskCreate", "tool_input": {}}, phase=None).allowed


def test_call_from_inside_a_subagent_is_refused():
    p = agent()
    p["agent_id"] = "a1"
    assert d(p).rule == "nested"


def test_per_call_model_is_refused():
    assert d(agent(model="opus")).rule == "model"


def test_teammates_and_isolation_are_refused():
    assert d(agent(team_name="t")).rule == "teammate"
    assert d(agent(name="helper")).rule == "teammate"
    assert d(agent(isolation="worktree")).rule == "isolation"


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
    wf = {"tool_name": "Workflow", "tool_input": {"name": "big-build"}}
    assert d(wf).rule == "workflow"
    assert d(wf, grants=Grants(workflows=("big-build",))).allowed
    unnamed = {"tool_name": "Workflow", "tool_input": {"script": "..."}}
    assert d(unnamed, grants=Grants(workflows=("big-build",))).rule == "workflow"


def test_unknown_or_missing_phase_is_refused():
    assert d(agent(), phase=None).rule == "phase"
    assert d(agent(), phase="deploy").rule == "phase"


def test_review_phase_allows_nothing():
    assert d(agent("linear-agents:source-finder"), phase="review").rule == "not-allowed"


def test_repeat_refusal_is_keyed_by_rule_and_agent():
    first = d(agent(), launched=99)
    assert "linear-agents escalate" in first.reason
    again = d(agent(), launched=99, refused={("launches", "task-implementer")})
    assert "already escalated" in again.reason and "escalate --tier" not in again.reason
    other = d(agent("code-mapper"), launched=99, refused={("launches", "task-implementer")})
    assert "linear-agents escalate" in other.reason
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
GATED_TOOLS = frozenset({"Agent", "Task", "Workflow"})
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
_IMPL = ("task-implementer", "wave-reviewer", "fix-rereviewer", "plan-reviewer", "code-mapper")
BUDGETS = {
    "test": Budget(_RESEARCH, 2, 2),
    "impl": Budget((*_IMPL, *_RESEARCH), 10, 4),
    "fix": Budget(("task-implementer", "fix-rereviewer"), 2, 1),
    "review": Budget((), 0, 0),
}
CATALOGUE = frozenset(a for b in BUDGETS.values() for a in b.agents)


@dataclass(frozen=True)
class Decision:
    allowed: bool
    rule: str
    what: str = ""
    reason: str = ""


def subject(payload: dict) -> str:
    args = payload.get("tool_input") or {}
    if payload.get("tool_name") == "Workflow":
        return str(args.get("name") or "")
    return str(args.get("subagent_type") or GENERAL).removeprefix(PREFIX)


def _refuse(rule: str, what: str, why: str, refused: set[tuple[str, str]]) -> Decision:
    if (rule, what) in refused:
        tail = "This was already escalated in this run; do not retry it or escalate again."
    else:
        tail = ESCALATE.format(rule=rule)
    return Decision(False, rule, what, f"linear-agents gate: {why} {tail}")


def decide(
    payload: dict,
    phase: str | None,
    grants: Grants,
    launched: int,
    running: int,
    refused: set[tuple[str, str]],
) -> Decision:
    tool = payload.get("tool_name")
    if tool not in GATED_TOOLS:
        return Decision(True, "ignored")
    what = subject(payload)
    budget = BUDGETS.get(phase or "")
    if budget is None:
        return _refuse("phase", what, f"no budget for phase {phase!r}.", refused)
    if payload.get("agent_id"):
        return _refuse("nested", what, "subagents may not start agents or workflows.", refused)
    args = payload.get("tool_input") or {}
    if tool == "Workflow":
        if not what or what not in grants.workflows:
            return _refuse("workflow", what, f"workflow {what!r} has no grant.", refused)
        return Decision(True, "allowed", what)
    if args.get("model"):
        return _refuse("model", what, "agent files fix the model; drop `model`.", refused)
    if args.get("team_name") or args.get("name"):
        return _refuse("teammate", what, "named teammates are not allowed here.", refused)
    if args.get("isolation"):
        return _refuse("isolation", what, "agents share the claimed worktree.", refused)
    if what not in budget.agents and what not in grants.agents:
        names = ", ".join(PREFIX + a for a in budget.agents) or "none"
        why = f"{what} is not allowed in the {phase} phase (allowed: {names})."
        return _refuse("not-allowed", what, why, refused)
    limit = grants.launches if grants.launches is not None else budget.launches
    if launched >= limit:
        return _refuse("launches", what, f"the {phase} phase used its {limit} launches.", refused)
    if running >= budget.running:
        why = f"{running} agents are still running; wait for one to finish."
        return _refuse("running", what, why, refused)
    return Decision(True, "allowed", what)
```

- [ ] **Step 4: Run the tests**

Run: `uv run pytest tests/test_gate.py -q`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/linear_agents/gate.py tests/test_gate.py
git commit -s -m "Decide subagent launches against per-phase budgets"
```

---

### Task 3: Gate hook commands, the launch log and status

**Files:**
- Modify: `src/linear_agents/gate.py` (log and hook entry points), `src/linear_agents/cli.py` (`gate`, `session-start`; `context` prints grants; `status` prints launches), `src/linear_agents/status.py` (`_claim_row`)
- Test: `tests/test_gate.py`, `tests/test_cli.py`, `tests/test_status.py`

**Interfaces:**
- Consumes: `decide`, `subject`, `BUDGETS`, `CATALOGUE` (Task 2); `for_phase` (Task 1).
- Produces:
  - `LOG = "launches.jsonl"`
  - `run_gate(payload: dict, state: Path, phase: str | None, run: str | None, now: float) -> str | None` — the refusal reason, or `None` to allow; logs gated calls.
  - `record_stop(state: Path, run: str | None, now: float, agent_type: str) -> None`
  - `record_session(state: Path, run: str | None, now: float) -> None`
  - `launches_used(state: Path, run: str) -> int`
  - `note_question(state: Path, payload: dict, now: float) -> None`; `clear_question(state: Path) -> None` (files `asking`, `asking-escalated`)
  - CLI: `linear-agents gate [--stopped | --answered]`, `linear-agents session-start`.
  - Log line: `{"at": float, "run": str|None, "event": "launch"|"refused"|"stop"|"session", "what": str, "rule": str}`
  - Status row keys: `"launches"`, `"launch_limit"`.

- [ ] **Step 1: Write the failing tests**

Add to `tests/test_gate.py` (imports at the top):

```python
import json
import multiprocessing
from concurrent.futures import ProcessPoolExecutor
from pathlib import Path

from linear_agents.gate import (
    LOG,
    clear_question,
    launches_used,
    note_question,
    record_session,
    record_stop,
    run_gate,
)


def events(state):
    out = []
    for line in (state / LOG).read_text().splitlines():
        try:
            out.append(json.loads(line)["event"])
        except ValueError:
            continue
    return out


def test_run_gate_logs_and_counts_per_run(tmp_path):
    for _ in range(BUDGETS["fix"].launches):
        assert run_gate(agent(), tmp_path, "fix", "fix@1", 0.0) is None
        record_stop(tmp_path, "fix@1", 0.0, "linear-agents:task-implementer")
    assert run_gate(agent(), tmp_path, "fix", "fix@1", 0.0)
    assert run_gate(agent(), tmp_path, "fix", "fix@2", 0.0) is None, "a new run starts a new count"
    assert launches_used(tmp_path, "fix@1") == 2


def test_running_resets_at_session_start_not_at_compaction(tmp_path):
    assert run_gate(agent(), tmp_path, "fix", "fix@1", 0.0) is None
    assert run_gate(agent(), tmp_path, "fix", "fix@1", 0.0)  # running limit 1
    record_session(tmp_path, "fix@1", 1.0)
    assert run_gate(agent(), tmp_path, "fix", "fix@1", 2.0) is None


def test_a_workflow_stop_does_not_free_a_catalogue_slot(tmp_path):
    assert run_gate(agent(), tmp_path, "fix", "fix@1", 0.0) is None
    record_stop(tmp_path, "fix@1", 1.0, "some-workflow-agent")
    assert run_gate(agent(), tmp_path, "fix", "fix@1", 2.0)


def test_ignored_tools_are_not_logged(tmp_path):
    assert run_gate({"tool_name": "TaskList", "tool_input": {}}, tmp_path, "impl", "r", 0.0) is None
    assert not (tmp_path / LOG).exists()


def test_torn_line_is_skipped_and_the_next_entry_kept(tmp_path):
    (tmp_path / LOG).write_text('{"at": 0, "run": "fix@1", "event": "launch"}\n{"at": 1, "ru')
    assert run_gate(agent(), tmp_path, "fix", "fix@1", 2.0)  # 1 running already
    assert events(tmp_path)[-1] == "refused"


def _race(state: str) -> bool:
    return run_gate(agent(), Path(state), "fix", "fix@1", 0.0) is None


def test_processes_racing_for_the_last_slot(tmp_path):
    assert run_gate(agent(), tmp_path, "fix", "fix@1", 0.0) is None
    record_stop(tmp_path, "fix@1", 0.0, "task-implementer")
    ctx = multiprocessing.get_context("fork")
    with ProcessPoolExecutor(max_workers=8, mp_context=ctx) as pool:
        results = list(pool.map(_race, [str(tmp_path)] * 8))
    assert results.count(True) == 1


def test_question_marker(tmp_path):
    payload = {"tool_name": "AskUserQuestion", "tool_input": {"questions": [{"question": "A or B?"}]}}
    note_question(tmp_path, payload, 5.0)
    assert json.loads((tmp_path / "asking").read_text()) == {"at": 5.0, "question": "A or B?"}
    (tmp_path / "asking-escalated").touch()
    clear_question(tmp_path)
    assert not (tmp_path / "asking").exists() and not (tmp_path / "asking-escalated").exists()
```

Add to `tests/test_cli.py` (imports `io`, `json` at the top):

```python
def gate_env(monkeypatch, tmp_path, phase="review"):
    monkeypatch.setenv("LINEAR_AGENTS_STATE", str(tmp_path))
    monkeypatch.setenv("LINEAR_AGENTS_PHASE", phase)
    monkeypatch.setenv("LINEAR_AGENTS_RUN", f"{phase}@1")


def test_gate_command_denies_with_hook_json(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path)
    payload = {"tool_name": "Agent", "tool_input": {"subagent_type": "linear-agents:source-finder"}}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    assert main(["gate"]) == 0
    out = json.loads(capsys.readouterr().out)
    assert out["hookSpecificOutput"]["permissionDecision"] == "deny"


def test_gate_refuses_with_exit_2_when_it_breaks(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path, "impl")

    def boom(*a, **kw):
        raise RuntimeError("disk gone")

    monkeypatch.setattr("linear_agents.gate.run_gate", boom)
    monkeypatch.setattr("sys.stdin", io.StringIO('{"tool_name": "Agent", "tool_input": {}}'))
    assert main(["gate"]) == 2
    assert "disk gone" in capsys.readouterr().err


def test_gate_without_a_state_dir_refuses(monkeypatch, capsys):
    monkeypatch.delenv("LINEAR_AGENTS_STATE", raising=False)
    monkeypatch.setattr("sys.stdin", io.StringIO('{"tool_name": "Agent", "tool_input": {}}'))
    assert main(["gate"]) == 2


def test_gate_records_and_clears_a_question(tmp_path, monkeypatch):
    gate_env(monkeypatch, tmp_path, "impl")
    q = {"tool_name": "AskUserQuestion", "tool_input": {"questions": [{"question": "Q?"}]}}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(q)))
    assert main(["gate"]) == 0 and (tmp_path / "asking").exists()
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(q)))
    assert main(["gate", "--answered"]) == 0 and not (tmp_path / "asking").exists()


def test_session_start_on_compaction(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setenv("LINEAR_AGENTS_LEDGER", str(tmp_path / "l.sqlite"))
    (tmp_path / "status.json").write_text("{}")
    payload = {"source": "compact", "scratchpad_dir": "/tmp/claude-1000/x/s1/scratchpad"}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    assert main(["session-start"]) == 0
    assert (tmp_path / "compacted").exists() and not (tmp_path / "status.json").exists()
    assert (tmp_path / "scratchpads").read_text().splitlines() == [payload["scratchpad_dir"]]
    assert not (tmp_path / "launches.jsonl").exists(), "compaction is not a new session"


def test_session_start_on_startup_records_a_session(tmp_path, monkeypatch):
    gate_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setenv("LINEAR_AGENTS_LEDGER", str(tmp_path / "l.sqlite"))
    monkeypatch.setattr("sys.stdin", io.StringIO('{"source": "startup"}'))
    assert main(["session-start"]) == 0
    assert '"event": "session"' in (tmp_path / "launches.jsonl").read_text()


def test_context_prints_the_phase_grants(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setenv("LINEAR_AGENTS_LEDGER", str(tmp_path / "l.sqlite"))
    set_block(tmp_path / "grants.json", Grants(launches=12, fix_rounds=2))
    main(["context"])
    out = capsys.readouterr().out
    assert "## Grants" in out and "launches 12" in out and "fix-rounds 2" in out
```

(`from linear_agents.grants import Grants, set_block` at the top.)

Add to `tests/test_status.py`:

```python
def test_claims_carry_launches_against_budget(cfg):
    con = ledger.connect(cfg.ledger_path)
    ledger.insert_claim(con, make("NF-1"))
    state = cfg.state_root / "NF-1"
    run_gate(
        {"tool_name": "Agent", "tool_input": {"subagent_type": "source-finder"}},
        state, "test", "test@0.0", 1.0,
    )
    (row,) = collect(cfg, FakeSessions(), now=1000.0, mode="loop")["claims"]
    assert (row["launches"], row["launch_limit"]) == (1, 2)
```

(`from linear_agents.gate import run_gate` at the top. `make()` gives phase `test` and `started_at=0.0`, so the run is `test@0.0`.)

- [ ] **Step 2: Run the tests to see them fail**

Run: `uv run pytest tests/test_gate.py tests/test_cli.py tests/test_status.py -q`
Expected: FAIL, `ImportError: cannot import name 'run_gate'`, `invalid choice: 'gate'`, `KeyError: 'launches'`.

- [ ] **Step 3: Implement**

Add to `src/linear_agents/gate.py` (new imports at the top: `fcntl`, `json`, `contextlib.contextmanager`, `pathlib.Path`, and `from linear_agents.grants import for_phase`):

```python
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
        except ValueError:  # a hook killed mid-write leaves a torn line
            continue
        if isinstance(e, dict) and e.get("run") == run:
            out.append(e)
    return out


def _append(state: Path, entry: dict) -> None:
    path = state / LOG
    with path.open("ab") as f:
        # After a torn line, start on a fresh one so this entry is not glued to the fragment.
        if f.tell() and path.read_bytes()[-1:] != b"\n":
            f.write(b"\n")
        f.write((json.dumps(entry) + "\n").encode())


def _counts(entries: list[dict], counted: set[str]) -> tuple[int, int, set[tuple[str, str]]]:
    launched = sum(e.get("event") == "launch" for e in entries)
    refused = {(e.get("rule", ""), e.get("what", "")) for e in entries if e.get("event") == "refused"}
    # Agents end with their session, so only what happened since the last session start counts.
    last = max((i for i, e in enumerate(entries) if e.get("event") == "session"), default=-1)
    since = entries[last + 1 :]
    started = sum(e.get("event") == "launch" for e in since)
    stopped = sum(e.get("event") == "stop" and e.get("what") in counted for e in since)
    return launched, max(started - stopped, 0), refused


def run_gate(
    payload: dict, state: Path, phase: str | None, run: str | None, now: float
) -> str | None:
    if payload.get("tool_name") not in GATED_TOOLS:
        return None
    with _locked(state):
        grants = for_phase(state / "grants.json", phase or "")
        counted = set(CATALOGUE) | set(grants.agents)
        launched, running, refused = _counts(_entries(state, run), counted)
        dec = decide(payload, phase, grants, launched, running, refused)
        event = "launch" if dec.allowed else "refused"
        _append(state, {"at": now, "run": run, "event": event, "what": dec.what, "rule": dec.rule})
    return None if dec.allowed else dec.reason


def record_stop(state: Path, run: str | None, now: float, agent_type: str) -> None:
    what = agent_type.removeprefix(PREFIX)
    with _locked(state):
        _append(state, {"at": now, "run": run, "event": "stop", "what": what, "rule": ""})


def record_session(state: Path, run: str | None, now: float) -> None:
    with _locked(state):
        _append(state, {"at": now, "run": run, "event": "session", "what": "", "rule": ""})


def launches_used(state: Path, run: str) -> int:
    return sum(e.get("event") == "launch" for e in _entries(state, run))


def note_question(state: Path, payload: dict, now: float) -> None:
    questions = (payload.get("tool_input") or {}).get("questions") or [{}]
    text = str(questions[0].get("question", "")) if isinstance(questions[0], dict) else ""
    state.mkdir(parents=True, exist_ok=True)
    (state / "asking").write_text(json.dumps({"at": now, "question": text}))


def clear_question(state: Path) -> None:
    for name in ("asking", "asking-escalated"):
        (state / name).unlink(missing_ok=True)
```

In `src/linear_agents/cli.py`:

```python
def _hook_input() -> dict:
    raw = "" if sys.stdin.isatty() else sys.stdin.read()
    try:
        data = json.loads(raw) if raw.strip() else {}
    except ValueError:
        return {}
    return data if isinstance(data, dict) else {}


def cmd_gate(args) -> int:
    from linear_agents import gate

    # Exit 2 refuses a PreToolUse call in every Claude Code version, whatever the hook's
    # onFailure setting does; any error of the gate's own must refuse, never allow.
    try:
        state = _state_dir()
        run = os.environ.get("LINEAR_AGENTS_RUN")
        payload = _hook_input()
        if args.stopped:
            gate.record_stop(state, run, time.time(), str(payload.get("agent_type", "")))
            return 0
        if args.answered:
            gate.clear_question(state)
            return 0
        if payload.get("tool_name") == "AskUserQuestion":
            gate.note_question(state, payload, time.time())
            return 0
        reason = gate.run_gate(payload, state, os.environ.get("LINEAR_AGENTS_PHASE"), run, time.time())
    except Exception as exc:  # noqa: BLE001 - see above
        print(f"linear-agents gate failed, so the launch is refused: {exc}", file=sys.stderr)
        return 2
    if reason is not None:
        out = {
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": "deny",
                "permissionDecisionReason": reason,
            }
        }
        print(json.dumps(out))
    return 0


def cmd_session_start(args) -> int:
    from linear_agents.gate import record_session

    state = _state_dir()
    state.mkdir(parents=True, exist_ok=True)
    payload = _hook_input()
    source = payload.get("source")
    if source == "compact":
        # status.json still holds the pre-compaction reading; the supervisor would hand off at once.
        (state / "compacted").touch()
        (state / "status.json").unlink(missing_ok=True)
    else:
        # Background agents survive compaction, so only a real start resets the running count.
        record_session(state, os.environ.get("LINEAR_AGENTS_RUN"), time.time())
    pad = payload.get("scratchpad_dir")
    if isinstance(pad, str) and pad:
        known = state / "scratchpads"
        listed = known.read_text().splitlines() if known.exists() else []
        if pad not in listed:
            with known.open("a") as f:
                f.write(pad + "\n")
    return cmd_context(args)
```

In `cmd_context`, after the peers block (import `for_phase` from `linear_agents.grants` at the top of the module):

```python
    g = for_phase(state / "grants.json", os.environ.get("LINEAR_AGENTS_PHASE", ""))
    items = [f"launches {g.launches}"] if g.launches is not None else []
    items += [f"agent {a}" for a in g.agents] + [f"workflow {w}" for w in g.workflows]
    items += [f"fix-rounds {g.fix_rounds}"] if g.fix_rounds is not None else []
    print(f"## Grants\n\n{', '.join(items) or 'none'}.\n")
```

Register `"gate": cmd_gate` and `"session-start": cmd_session_start` in `COMMANDS`, and in `build_parser`:

```python
    gate = add("gate", "Hook: allow or refuse a subagent or workflow launch, track questions")
    which = gate.add_mutually_exclusive_group()
    which.add_argument("--stopped", action="store_true", help="SubagentStop: an agent finished")
    which.add_argument("--answered", action="store_true", help="PostToolUse: a question answered")
    add("session-start", "Hook: record compaction and the scratchpad, print the context")
```

In `src/linear_agents/status.py` `_claim_row` (imports `BUDGETS`, `launches_used` from `linear_agents.gate` and `for_phase` from `linear_agents.grants` at the top):

```python
    run = f"{claim.phase}@{claim.started_at}"
    budget = BUDGETS.get(claim.phase)
    granted = for_phase(state_dir / "grants.json", claim.phase).launches
    limit = granted if granted is not None else (budget.launches if budget else 0)
```

and add `"launches": launches_used(state_dir, run), "launch_limit": limit` to the returned dict. In `cmd_status`, print ` launches {c['launches']}/{c['launch_limit']}` at the end of each claim line.

- [ ] **Step 4: Run the tests**

Run: `uv run pytest -q`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/linear_agents/gate.py src/linear_agents/cli.py src/linear_agents/status.py tests/test_gate.py tests/test_cli.py tests/test_status.py
git commit -s -m "Add the gate and session-start hooks, a per-run launch log and status counts"
```

---

### Task 4: Plugin catalogue, hooks and skills

**Files:**
- Create: `plugin/agents/source-finder.md`, `plugin/agents/source-reader.md`, `plugin/agents/code-mapper.md`, `plugin/agents/plan-reviewer.md`, `plugin/agents/task-implementer.md`, `plugin/agents/wave-reviewer.md`, `plugin/agents/fix-rereviewer.md`, `tests/test_plugin.py`
- Modify: `plugin/hooks/hooks.json`, `plugin/skills/implementer/SKILL.md`, `plugin/skills/tester/SKILL.md`, `plugin/skills/handoff/SKILL.md`, `plugin/skills/escalate/SKILL.md`, `src/linear_agents/context.py` (`_COMMANDS`, `phase_prompt` for `fix`)

**Interfaces:**
- Consumes: `BUDGETS`, `CATALOGUE` (Task 2); hook commands (Task 3).
- Produces: one agent file per name in `CATALOGUE`.

- [ ] **Step 1: Write the failing test**

`tests/test_plugin.py`:

```python
import json
from pathlib import Path

from linear_agents.context import phase_prompt
from linear_agents.gate import CATALOGUE

PLUGIN = Path(__file__).parent.parent / "plugin"
SPEC = {
    "source-finder": ("haiku", "low"),
    "source-reader": ("sonnet", "medium"),
    "code-mapper": ("sonnet", "medium"),
    "plan-reviewer": ("opus", "high"),
    "task-implementer": ("sonnet", "medium"),
    "wave-reviewer": ("opus", "high"),
    "fix-rereviewer": ("sonnet", "medium"),
}


def frontmatter(path: Path) -> dict[str, str]:
    head = path.read_text().split("---")[1]
    pairs = (line.partition(":") for line in head.splitlines())
    return {k.strip(): v.strip() for k, _, v in pairs if k.strip()}


def test_every_catalogue_agent_has_a_file_with_its_model_and_effort():
    assert set(CATALOGUE) == SPEC.keys()
    for name, (model, effort) in SPEC.items():
        fm = frontmatter(PLUGIN / "agents" / f"{name}.md")
        assert (fm["name"], fm["model"], fm["effort"]) == (name, model, effort)


def test_reviewers_and_mappers_cannot_edit_or_run_shell():
    for name in ("code-mapper", "plan-reviewer", "wave-reviewer", "fix-rereviewer"):
        tools = {t.strip() for t in frontmatter(PLUGIN / "agents" / f"{name}.md")["tools"].split(",")}
        assert not {"Edit", "Write", "Bash"} & tools


def test_hooks():
    hooks = json.loads((PLUGIN / "hooks" / "hooks.json").read_text())["hooks"]
    pre = hooks["PreToolUse"][0]
    assert pre["matcher"] == "Agent|Task|Workflow|AskUserQuestion"
    assert pre["hooks"][0]["command"] == "linear-agents gate"
    assert pre["hooks"][0]["onFailure"] == "block"
    assert hooks["PostToolUse"][0]["matcher"] == "AskUserQuestion"
    assert hooks["PostToolUse"][0]["hooks"][0]["command"] == "linear-agents gate --answered"
    assert hooks["SubagentStop"][0]["hooks"][0]["command"] == "linear-agents gate --stopped"
    assert hooks["SessionStart"][0]["hooks"][0]["command"] == "linear-agents session-start"


def test_fix_prompt_points_at_the_fix_section():
    assert "Fix phase" in phase_prompt("fix", "NF-1", "check output")
```

- [ ] **Step 2: Run the test to see it fail**

Run: `uv run pytest tests/test_plugin.py -q`
Expected: FAIL, `FileNotFoundError` for `plugin/agents/source-finder.md`.

- [ ] **Step 3: Write the files**

`plugin/hooks/hooks.json`:

```json
{
  "hooks": {
    "SessionStart": [
      { "hooks": [ { "type": "command", "command": "linear-agents session-start" } ] }
    ],
    "PreToolUse": [
      {
        "matcher": "Agent|Task|Workflow|AskUserQuestion",
        "hooks": [
          { "type": "command", "command": "linear-agents gate", "timeout": 10, "onFailure": "block" }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "AskUserQuestion",
        "hooks": [ { "type": "command", "command": "linear-agents gate --answered" } ]
      }
    ],
    "SubagentStop": [
      { "hooks": [ { "type": "command", "command": "linear-agents gate --stopped" } ] }
    ]
  }
}
```

Agent files (each exactly as below):

`plugin/agents/source-finder.md`

```markdown
---
name: source-finder
description: Find dated, URL-cited sources on one library, API or service question. Use before relying on or adding a dependency. Finds sources only; source-reader reads them.
model: haiku
effort: low
tools: WebSearch, WebFetch
maxTurns: 20
---

You are finding sources, not writing conclusions. The question and today's date are in your
task. Return at most eight sources as a list: URL, publication or last-updated date, and one line
on what it covers. Prefer official docs, release notes and maintainers' posts over blogs; mark
anything older than a year. Do not write files. Do not start subagents.
```

`plugin/agents/source-reader.md`

```markdown
---
name: source-reader
description: Read the sources source-finder returned and answer the stated question with citations. Use only after source-finder.
model: sonnet
effort: medium
tools: WebSearch, WebFetch, Read
maxTurns: 30
---

Read each source in your task and answer its question; today's date is in your task. Every claim
carries the URL and date it came from; say plainly what the sources do not settle. Keep the
answer under 400 words. Do not write files. Do not start subagents.
```

`plugin/agents/code-mapper.md`

```markdown
---
name: code-mapper
description: Map where something lives in a large repo before planning. Use when the plan needs paths you cannot find with a couple of searches yourself.
model: sonnet
effort: medium
tools: Read, Grep, Glob
maxTurns: 30
---

Find where the thing named in your task lives. Return paths with line numbers and two or three
sentences on how the pieces connect. Read excerpts, not whole large files. Change nothing. Do not
start subagents.
```

`plugin/agents/plan-reviewer.md`

```markdown
---
name: plan-reviewer
description: One adversarial read of the implementer's plan, for plans of four or more tasks, before any building starts.
model: opus
effort: high
tools: Read, Grep, Glob
maxTurns: 30
---

Review the plan at the path in your task against the issue's acceptance criteria and the tester's
committed tests (paths in your task). Look for criteria with no task, tasks that touch the same
paths in one wave, missing tests, a plan that will not fit its launch budget, and steps an
implementer would have to guess. Return findings ranked blocker / should-fix / nit, each with
evidence and a concrete fix. Edit nothing. Do not start subagents.
```

`plugin/agents/task-implementer.md`

```markdown
---
name: task-implementer
description: Implement one task of the implementer's plan from its brief file, and make the fixes a wave review asks for. Plan tasks always go through this agent, even a single one.
model: sonnet
effort: medium
tools: Read, Edit, Write, Bash, Grep, Glob
maxTurns: 80
---

Your brief is the file named in your task; it is your requirements, including the paths you own.
Write the test first when the brief asks for new behaviour, then the code. Edit only your paths.
Run only the focused tests your brief names; never run Unity, long builds, training or the full
suite. Commit only your paths with `linear-agents commit -m '<message>' -- <paths>`, never a bare
`git commit`. Never push. Report what you changed and the test output. Do not start subagents.
```

`plugin/agents/wave-reviewer.md`

```markdown
---
name: wave-reviewer
description: Review one wave of implemented tasks against their briefs, once per wave, not per task.
model: opus
effort: high
tools: Read, Grep, Glob
maxTurns: 40
---

Your task names the briefs, the wave's diff file and its test output. Check first whether each
task meets its brief, then whether it is well built. Return findings per task, ranked blocker /
should-fix / nit, each with the file and line and a concrete fix. You write findings only; the
implementer makes every edit. Do not start subagents.
```

`plugin/agents/fix-rereviewer.md`

```markdown
---
name: fix-rereviewer
description: Verdict each finding of one fix round against the fix diff file, and nothing else.
model: sonnet
effort: medium
tools: Read, Grep, Glob
maxTurns: 20
---

Your task names the findings and the fix diff file. For each finding answer fixed, not fixed or
made worse, with the line that shows it. Review nothing outside the findings. Edit nothing. Do
not start subagents.
```

Replace the body of `plugin/skills/implementer/SKILL.md` (keep its frontmatter) with:

```markdown
Make the committed tests pass by implementing the behavior.

- Do not edit, delete or skip any file the tester committed. The service rejects the run if you
  do. If you believe a test is wrong, do not edit it: ask the owner (see the escalate skill), state
  the test change you propose, and keep working only on parts that do not depend on that test.
- Claim paths before editing them: `linear-agents claim '<glob>'`. A rejected claim names the
  issue that holds it; message that session or escalate.
- Run `linear-agents context` and read "Grants" before planning; it lists what you may do beyond
  the defaults.
- Plan first: write `plan.md` in your state directory with numbered tasks, the paths each task
  touches, and its tests. Tasks that share a path go in different waves; a wave holds at most four
  tasks. Fit the plan to your launch budget (10 unless a grant says otherwise: one plan review,
  one implementer per task, one reviewer per wave, one fix and its re-review); if it cannot fit,
  ask for `launches` before building.
- With four or more tasks, have `linear-agents:plan-reviewer` read the plan once before building,
  and fix what it finds. With fewer, check the plan against the acceptance criteria yourself.
- Write each task's brief to `briefs/task-N.md` in your state directory: its requirements, the
  paths it owns, the focused test command. Start one `linear-agents:task-implementer` per task with
  the brief's path, every plan task included, even if there is only one.
- After each wave, write its `git diff` and test output to `waves/N.diff` and `waves/N.tests`
  in your state directory, and have one `linear-agents:wave-reviewer` review the wave.
- Findings get one fix round: the same `task-implementer` route, then one
  `linear-agents:fix-rereviewer` on the fix diff. A second round needs a `fix-rounds` grant;
  without one, escalate what is left.
- When you start `source-finder` or `source-reader`, put today's date in the task.
- Do small work yourself: a change outside the plan's tasks that fits in a few lines is not a task.
- While a wave runs, commit your own work with `linear-agents commit -m '<message>' -- <paths>`,
  never a bare `git commit`, which would take the implementers' half-done files.
- Never pass `model` or `isolation` when starting an agent, and never start general-purpose agents,
  workflows or teammates unless "Grants" lists them. Never run `claude` from the shell. The gate
  refuses these and says how to ask.
- Run the repo's check (usually `just check`) before finishing.
- Never push or open a PR; the service does that.
- Finish with `linear-agents done --summary '<what changed and why, for the PR body>'`.

## Fix phase

When the service starts you to fix a failed check, the plan steps above do not apply. Fix the
failure inline; if it is more than a few lines, use one `linear-agents:task-implementer` with a
brief and one `linear-agents:fix-rereviewer` on its diff, which is all the fix phase allows. Then
run the check and finish with `linear-agents done`.
```

Append to `plugin/skills/tester/SKILL.md`:

```markdown
- To look up how an unfamiliar library or API behaves, use `linear-agents:source-finder` and then
  `linear-agents:source-reader`, with today's date in the task; you have two launches. Start no
  other agents.
```

Replace the last paragraph of `plugin/skills/handoff/SKILL.md` with:

```markdown
If you started agents, wait until they have all finished before handing off; a handoff kills the
session and every agent it is running. In `handoff.md`, also record `plan.md`'s path, the waves
done, and the wave in progress.

Commit any work in progress with `linear-agents commit -m '<message>' -- <paths>`. Then run
`linear-agents handoff-ready` and stop.
```

In `plugin/skills/escalate/SKILL.md`, add a section after the tier table:

```markdown
## Asking the owner directly

For a decision you cannot go on without (what tier 2 `--waiting` was for), ask with
`AskUserQuestion`: the owner gets a push in the Claude app and answers there. Only implementer and
fix sessions are connected to the app; a tester or reviewer escalates with `--tier 2 --waiting`
instead. First record it:
`linear-agents escalate --tier 3 --cause asked:<topic> --summary '<the question>'`. Give two to
four options with your recommendation first. If nobody answers within two hours the service
escalates it again. For a reversible choice, do not ask: escalate with `--default` and carry on.
```

In `src/linear_agents/context.py`, add to `_COMMANDS`:

```python
- `linear-agents commit -m '<message>' -- <paths>` to commit only those paths.
- "Grants" below lists what you may do beyond the defaults; the gate refuses anything else.
```

and change the `fix` branch of `phase_prompt` to say "Follow the implementer skill's Fix phase section, fix the failure below, then run linear-agents done."

- [ ] **Step 4: Run the tests**

Run: `uv run pytest tests/test_plugin.py tests/test_context.py -q`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add plugin tests/test_plugin.py src/linear_agents/context.py
git commit -s -m "Ship the subagent catalogue, gate hooks and skill changes in the plugin"
```

---

### Task 5: Config paths, session settings, launch environment and supervisor

**Files:**
- Modify: `src/linear_agents/config.py`, `tests/conftest.py` (`tmp_cfg`), `src/linear_agents/sessions.py`, `src/linear_agents/supervisor.py`
- Test: `tests/test_sessions.py`, `tests/test_supervisor.py`, `tests/test_config.py`

**Interfaces:**
- Consumes: `set_block`, `for_phase`, `parse_allow`, `Grants` (Task 1); `clear_question` (Task 3).
- Produces:
  - `Config.agent_plugins_file: Path | None = None` (None → `plugin_dir.parent / "deploy" / "agent-plugins.txt"`), `Config.installed_plugins_file: Path` (default `~/.claude/plugins/installed_plugins.json`); both in `_PATH_KEYS`.
  - `installed_plugins(path: Path) -> tuple[str, ...]`; `allowed_plugins(path: Path) -> tuple[str, ...] | None`
  - `claude_argv(..., grants: Grants = Grants(), disabled_plugins: tuple[str, ...] = (), remote_control: bool = True)`; `launch()` passes `remote_control=phase in ("impl", "fix")`
  - `launch(rt, claim, phase, extra="", started_at: float | None = None)`; environment adds `LINEAR_AGENTS_RUN=<phase>@<start>`, `CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH=1`, `CLAUDE_CODE_DISABLE_EXPLORE_PLAN_AGENTS=1`.
  - `QUESTION_WAIT_S = 7200`

- [ ] **Step 1: Write the failing tests**

In `tests/conftest.py` `tmp_cfg`, add to the `replace(...)`:

```python
        agent_plugins_file=tmp_path / "agent-plugins.txt",
        installed_plugins_file=tmp_path / "installed_plugins.json",
```

Add to `tests/test_config.py` `test_example_loads_with_defaults`:

```python
    assert cfg.agent_plugins_file is None
    assert cfg.installed_plugins_file.name == "installed_plugins.json"
```

Add to `tests/test_sessions.py` (imports at the top):

```python
def settings_of(argv):
    return json.loads(argv[argv.index("--settings") + 1])


def argv_with(**kw):
    return claude_argv(CFG, "NF-1", IssueSettings(), "p", env={}, claude_exe="c", exe="x", **kw)


def test_settings_deny_general_purpose_and_nested_claude_but_allow_questions():
    s = settings_of(argv_with())
    deny = s["permissions"]["deny"]
    assert "Agent(general-purpose)" in deny and "Bash(claude:*)" in deny
    assert "AskUserQuestion" not in deny
    assert s["disableWorkflows"] is True


def test_grants_lift_the_matching_launch_flags():
    s = settings_of(argv_with(grants=Grants(agents=("general-purpose",), workflows=("w",))))
    assert "Agent(general-purpose)" not in s["permissions"]["deny"]
    assert s["disableWorkflows"] is False


def test_unlisted_plugins_are_disabled():
    s = settings_of(argv_with(disabled_plugins=("glowup@glowup",)))
    assert s["enabledPlugins"] == {"glowup@glowup": False}


def test_remote_control_can_be_left_off():
    assert "--remote-control" in argv_with()
    argv = argv_with(remote_control=False)
    assert "--remote-control" not in argv and argv[argv.index("--name") + 1] == "NF-1"


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

Add to `tests/test_supervisor.py` (imports at the top: `from linear_agents.grants import for_phase`; `from linear_agents.supervisor import QUESTION_WAIT_S`):

```python
def env_of(argv):
    end = argv.index("/bin/sh")
    return dict(x.split("=", 1) for x in argv[2:end])


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
    supervise(rt, current(rt))  # a crash relaunch of the same phase
    assert env_of(rt.sessions.started[-1][1])["LINEAR_AGENTS_RUN"] == f"impl@{started}"


def test_only_implementer_and_fix_sessions_use_remote_control(rt):
    add(rt)
    supervise(rt, current(rt))  # tester relaunch
    assert "--remote-control" not in rt.sessions.started[-1][1]
    rt.sessions.alive_names.add("NF-1-test")
    done(rt)
    supervise(rt, current(rt))  # implementer
    assert "--remote-control" in rt.sessions.started[-1][1]


def test_launch_records_block_grants_without_beating_comment_grants(rt):
    add(rt, settings_json=settings_to_json(IssueSettings(allow="launches 12")))
    path = state_dir(rt.cfg, "NF-1") / "grants.json"
    from_comment = Grants(launches=20)
    add_comment(path, from_comment, "impl")
    rt.sessions.alive_names.add("NF-1-test")
    done(rt)
    supervise(rt, current(rt))
    assert for_phase(path, "impl").launches == 20


def test_unlisted_installed_plugins_are_disabled_at_launch(rt):
    rt.cfg.agent_plugins_file.write_text("docket@NovusEdge\n")
    rt.cfg.installed_plugins_file.write_text(
        json.dumps({"plugins": {"docket@NovusEdge": [], "glowup@glowup": []}})
    )
    add(rt)
    supervise(rt, current(rt))  # not alive: relaunch
    settings = json.loads(rt.sessions.started[-1][1][rt.sessions.started[-1][1].index("--settings") + 1])
    assert settings["enabledPlugins"] == {"glowup@glowup": False}


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


def test_relaunch_clears_compaction_and_question_markers(rt):
    add(rt)
    d = state_dir(rt.cfg, "NF-1")
    for name in ("compacted", "asking", "asking-escalated"):
        (d / name).write_text("{}")
    supervise(rt, current(rt))  # not alive: relaunch
    for name in ("compacted", "asking", "asking-escalated"):
        assert not (d / name).exists()


def test_a_stale_question_is_escalated_once(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    d = state_dir(rt.cfg, "NF-1")
    (d / "asking").write_text(json.dumps({"at": 100.0 - QUESTION_WAIT_S - 1, "question": "A or B?"}))
    supervise(rt, current(rt))
    supervise(rt, current(rt))
    (esc,) = rt.notifier.sent
    assert esc.tier == 2 and "A or B?" in esc.summary


def test_a_fresh_question_is_not_escalated(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    d = state_dir(rt.cfg, "NF-1")
    (d / "asking").write_text(json.dumps({"at": 100.0, "question": "A or B?"}))
    supervise(rt, current(rt))
    assert rt.notifier.sent == []
```

(Also import `Grants` and `add_comment` from `linear_agents.grants` at the top.) Change `test_high_context_requests_handoff_once` to touch `state_dir(rt.cfg, "NF-1") / "compacted"` before its first `supervise`.

- [ ] **Step 2: Run the tests to see them fail**

Run: `uv run pytest tests/test_sessions.py tests/test_supervisor.py tests/test_config.py -q`
Expected: FAIL on the new tests (unknown config fields, `ImportError: installed_plugins`, missing env keys, handoff before compaction, no question escalation).

- [ ] **Step 3: Implement**

`src/linear_agents/config.py`: add `"agent_plugins_file"` and `"installed_plugins_file"` to `_PATH_KEYS`, and to `Config`:

```python
    # The plugins agent sessions get; None means deploy/agent-plugins.txt in the service repo.
    agent_plugins_file: Path | None = None
    installed_plugins_file: Path = field(
        default_factory=lambda: Path.home() / ".claude" / "plugins" / "installed_plugins.json"
    )
```

`src/linear_agents/sessions.py` (import `Grants` from `linear_agents.grants` at the top):

```python
_DENY = [
    "Bash(git commit --no-verify:*)",
    "Bash(git commit -n:*)",
    "Bash(git commit * --no-verify*)",
    "Bash(git commit * -n *)",
    "Bash(git commit * -n)",
    # A second claude process is a session the gate and the usage caps never see.
    "Bash(claude:*)",
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
    lines = (line.strip() for line in path.read_text().splitlines())
    return tuple(line for line in lines if line and not line.startswith("#"))
```

In `claude_argv`, add parameters `grants: Grants = Grants()` and `disabled_plugins: tuple[str, ...] = ()`, and build:

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

and replace the fixed `"--remote-control", name,` pair in the argv list with a conditional, keeping `--name`:

```python
    argv = [
        "/usr/bin/env",
        "-i",
        *(f"{k}={v}" for k, v in env.items()),
        "/bin/sh",
        "-c",
        _WRAPPER,
        str(cfg.agent_gh_token_file),
        claude_exe,
        prompt,
        # The Claude app announces every Remote Control session; only the long phases get one.
        *(["--remote-control", name] if remote_control else []),
        "--name",
        name,
        # ...the remaining flags unchanged
    ]
```

`src/linear_agents/supervisor.py` (imports at the top: `time` is not needed; add `from linear_agents import grants as grants_mod`, `from linear_agents.gate import clear_question`, and `allowed_plugins`, `installed_plugins` from `linear_agents.sessions`):

```python
QUESTION_WAIT_S = 7200


def _disabled_plugins(rt) -> tuple[str, ...]:
    cfg = rt.cfg
    listed = cfg.agent_plugins_file or cfg.plugin_dir.parent / "deploy" / "agent-plugins.txt"
    allowed = allowed_plugins(listed)
    if allowed is None:
        return ()
    return tuple(p for p in installed_plugins(cfg.installed_plugins_file) if p not in allowed)
```

In `launch`, add `started_at: float | None = None` and:

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
    if settings.allow:
        grants_mod.set_block(d / "grants.json", grants_mod.parse_allow(settings.allow))
    granted = grants_mod.for_phase(d / "grants.json", phase)
```

pass `grants=granted, disabled_plugins=_disabled_plugins(rt), remote_control=phase in ("impl", "fix")` to `claude_argv`; change the leftover delete to `for leftover in (*_MARKERS, "status.json", "compacted"):` and call `clear_question(d)` right after it.

At the three phase transitions (`_finish_phase` test→impl, `_finish_phase` check→fix, `_start_review`), compute `start = rt.now()` once, pass `started_at=start` to `launch`, and use `started_at=start` in the matching `ledger.update_claim`.

In `supervise`, before the context check add:

```python
    _check_question(rt, claim, d)
```

with:

```python
def _check_question(rt, claim: Claim, d: Path) -> None:
    asking = d / "asking"
    if not asking.exists() or (d / "asking-escalated").exists():
        return
    try:
        q = json.loads(asking.read_text())
        at, text = float(q["at"]), str(q.get("question", ""))
    except (OSError, ValueError, KeyError, TypeError):
        return
    if rt.now() - at < QUESTION_WAIT_S:
        return
    esc = scoped(
        Escalation(
            2,
            "question",
            f"Unanswered question in the Claude app: {text}",
            "Answer it in the Claude app, or reply on this issue",
            waiting=True,
        ),
        claim.issue_key,
    )
    rt.notifier.send(esc, rt.linear.issue(claim.issue_key), None)
    (d / "asking-escalated").touch()
```

and change the context check to:

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
git add src/linear_agents/config.py src/linear_agents/sessions.py src/linear_agents/supervisor.py tests/conftest.py tests/test_config.py tests/test_sessions.py tests/test_supervisor.py
git commit -s -m "Launch sessions with run ids, subagent limits, grants and the plugin allowlist"
```

---

### Task 6: Grants from owner comments

**Files:**
- Modify: `src/linear_agents/poller.py` (`forward_comments`, `_start`)
- Test: `tests/test_poller.py`

**Interfaces:**
- Consumes: `allow_lines`, `parse_allow`, `add_comment`, `for_phase`, `GrantError` (Task 1).

- [ ] **Step 1: Write the failing tests**

Add to `tests/test_poller.py` (imports at the top: `from linear_agents.grants import Grants, for_phase, set_block`):

```python
def test_owner_and_steer_author_allow_comments_grant_for_the_running_phase(rt):
    rt.cfg = replace(rt.cfg, steer_authors=("hermes-id",))
    ledger.insert_claim(rt.con, make("NF-1", ()))
    rt.linear.replies = {
        "id-NF-1": [
            Comment("c1", "go ahead\nallow: launches 4", "2026-10-09T10:00:00Z", mine=True),
            Comment("c2", "allow: agent code-mapper", "2026-10-09T10:01:00Z", author="hermes-id"),
            Comment("c3", "allow: agent general-purpose", "2026-10-09T10:02:00Z", author="x"),
        ]
    }
    rt.sessions.alive_names.add("NF-1-test")
    forward_comments(rt)
    path = state_dir(rt.cfg, "NF-1") / "grants.json"
    assert for_phase(path, "test") == Grants(launches=4, agents=("code-mapper",))
    assert for_phase(path, "impl").launches is None
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
    # Real Linear returns each comment once (last_comment_at advances); PollLinear ignores it.
    assert for_phase(state_dir(rt.cfg, "NF-1") / "grants.json", "test").launches == 9
    replies = [body for _, body in rt.linear.comments if "launches lots" in body]
    assert len(replies) == 1


def test_grants_wait_for_a_live_session(rt):
    ledger.insert_claim(rt.con, make("NF-1", ()))
    rt.linear.replies = {"id-NF-1": [Comment("c1", "allow: launches 4", "2026-10-09T10:00:00Z", mine=True)]}
    forward_comments(rt)  # session down: nothing applied, nothing advanced
    assert not (state_dir(rt.cfg, "NF-1") / "grants.json").exists()


def test_a_new_claim_starts_without_the_last_claims_state(rt):
    d = state_dir(rt.cfg, "NF-9")
    d.mkdir(parents=True)
    from_before = Grants(launches=5)
    set_block(d / "grants.json", from_before)
    for name in ("scratchpads", "compacted", "asking"):
        (d / name).write_text("x")
    rt.linear.ready = [issue()]
    poll(rt)
    for name in ("scratchpads", "compacted", "asking"):
        assert not (d / name).exists()
    assert for_phase(d / "grants.json", "impl").launches is None
```

(`replace` from `dataclasses` is already imported in that file; if not, add it at the top.)

- [ ] **Step 2: Run the tests to see them fail**

Run: `uv run pytest tests/test_poller.py -q`
Expected: FAIL on the four new tests.

- [ ] **Step 3: Implement**

In `src/linear_agents/poller.py` (imports at the top: `from linear_agents.grants import GrantError, add_comment, allow_lines, parse_allow`):

```python
def _apply_grants(rt, claim: Claim, body: str) -> None:
    path = state_dir(rt.cfg, claim.issue_key) / "grants.json"
    for line in allow_lines(body):
        try:
            add_comment(path, parse_allow(line), claim.phase)
        except GrantError as exc:
            try:
                rt.linear.comment(claim.issue_id, f"{BOT_PREFIX} grant not applied: {exc}")
            except LinearError as err:
                print(f"{claim.issue_key}: grant reply not posted: {err}", file=sys.stderr)
```

In `forward_comments`, apply grants after the comment is delivered, so a dead session (the loop `break`s and `last_comment_at` stays) neither applies nor answers it twice:

```python
            if who and not comment.body.startswith(BOT_PREFIX):
                if not rt.sessions.alive(name):
                    break
                rt.sessions.send(name, f"{who} replied on Linear: {comment.body}")
                _apply_grants(rt, claim, comment.body)
```

In `_start`, replace the state reset next to the `handoff.md` move with:

```python
    # A previous claim's grants, scratchpads, compaction and question belong to that run; the
    # agent block's grants are written again at launch.
    for stale in ("grants.json", "scratchpads", "compacted", "asking", "asking-escalated"):
        (d / stale).unlink(missing_ok=True)
```

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

Add to `tests/test_cli.py` (imports at the top: `subprocess`, `from concurrent.futures import ThreadPoolExecutor`):

```python
def test_parallel_commits_take_only_their_own_paths(origin, monkeypatch):
    monkeypatch.chdir(origin)
    for name in ("a1.txt", "a2.txt", "b1.txt", "b2.txt"):
        (origin / name).write_text(name)
    jobs = [("A", ["a1.txt", "a2.txt"]), ("B", ["b1.txt", "b2.txt"])]
    with ThreadPoolExecutor(max_workers=2) as pool:
        codes = list(pool.map(lambda job: main(["commit", "-m", job[0], "--", *job[1]]), jobs))
    assert codes == [0, 0]
    for msg, files in (("A", {"a1.txt", "a2.txt"}), ("B", {"b1.txt", "b2.txt"})):
        log = ["git", "log", "-1", "--format=%H", f"--grep=^{msg}$"]
        sha = subprocess.run(log, cwd=origin, capture_output=True, text=True, check=True)
        show = ["git", "show", "--name-only", "--format=", sha.stdout.strip()]
        shown = subprocess.run(show, cwd=origin, capture_output=True, text=True, check=True)
        assert set(shown.stdout.split()) == files


def test_commit_needs_paths(capsys):
    assert main(["commit", "-m", "x", "--"]) == 2
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `uv run pytest tests/test_cli.py -q -k commit`
Expected: FAIL, `invalid choice: 'commit'`.

- [ ] **Step 3: Implement**

In `src/linear_agents/cli.py` (import `fcntl` at the top):

```python
def cmd_commit(args) -> int:
    from linear_agents.worktree import git

    paths = [p for p in args.paths if p != "--"]
    if not paths:
        print("commit: name the paths to commit after --", file=sys.stderr)
        return 2
    git_dir = Path(git("rev-parse", "--absolute-git-dir", cwd=Path.cwd()))
    # Parallel task-implementers share one worktree and its index: one commit at a time, each
    # with only its own paths.
    with (git_dir / "la-commit.lock").open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        added = subprocess.run(["git", "add", "--", *paths], check=False)
        if added.returncode:
            return added.returncode
        commit = ["git", "commit", "-m", args.message, "--", *paths]
        return subprocess.run(commit, check=False).returncode
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
- Modify: `src/linear_agents/poller.py` (`scratch_root`, `clean_scratchpads`), `src/linear_agents/tick.py`
- Test: `tests/test_poller.py`

**Interfaces:**
- Consumes: `<state_dir>/scratchpads` (Task 3).
- Produces: `scratch_root() -> Path`; `clean_scratchpads(rt) -> None`.

- [ ] **Step 1: Write the failing tests**

Add to `tests/test_poller.py` (import `clean_scratchpads` from `linear_agents.poller` at the top):

```python
def pads(rt, key, lines):
    d = state_dir(rt.cfg, key)
    d.mkdir(parents=True, exist_ok=True)
    (d / "scratchpads").write_text("".join(f"{x}\n" for x in lines))
    return d


def test_cleanup_removes_a_finished_runs_scratchpads_only(rt, tmp_path, monkeypatch):
    root = tmp_path / "claude-1000"
    monkeypatch.setattr("linear_agents.poller.scratch_root", lambda: root)
    done_pad = root / "proj" / "s1" / "scratchpad"
    live_pad = root / "proj" / "s2" / "scratchpad"
    done_pad.mkdir(parents=True)
    live_pad.mkdir(parents=True)
    ledger.insert_claim(rt.con, make("NF-1", (), state="blocked"))
    ledger.insert_claim(rt.con, make("NF-2", ()))
    d1 = pads(rt, "NF-1", [done_pad])
    pads(rt, "NF-2", [live_pad])
    clean_scratchpads(rt)
    assert not done_pad.exists() and live_pad.exists()
    assert not (d1 / "scratchpads").exists()


def test_cleanup_only_removes_full_session_paths(rt, tmp_path, monkeypatch):
    root = tmp_path / "claude-1000"
    monkeypatch.setattr("linear_agents.poller.scratch_root", lambda: root)
    project = root / "proj"
    (project / "s1" / "scratchpad").mkdir(parents=True)
    outside = tmp_path / "keep"
    outside.mkdir()
    ledger.insert_claim(rt.con, make("NF-1", (), state="done"))
    pads(rt, "NF-1", [root, project, project / "s1", outside, "", "/", f"{project}/../.."])
    clean_scratchpads(rt)
    assert root.exists() and (project / "s1" / "scratchpad").exists() and outside.exists()
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `uv run pytest tests/test_poller.py -q -k cleanup`
Expected: FAIL, `ImportError: cannot import name 'clean_scratchpads'`.

- [ ] **Step 3: Implement**

In `src/linear_agents/poller.py` (import `os` at the top):

```python
def scratch_root() -> Path:
    return Path(f"/tmp/claude-{os.getuid()}")


def _scratch_path(line: str) -> Path | None:
    if not line.strip():
        return None
    root = scratch_root()
    try:
        rel = Path(line.strip()).resolve().relative_to(root)
    except ValueError:
        return None
    # <project>/<session>/<scratchpad>: anything shorter is shared by other sessions.
    return root / rel if len(rel.parts) >= 3 else None


def clean_scratchpads(rt) -> None:
    """Removes the Claude Code scratchpads of claims that are no longer running."""
    for listing in rt.cfg.state_root.glob("*/scratchpads"):
        claim = ledger.get_claim(rt.con, listing.parent.name)
        if claim is not None and claim.state == "running":
            continue
        for line in listing.read_text().splitlines():
            path = _scratch_path(line)
            if path is not None:
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

### Task 9: Installer: plugin allowlist, Claude settings, tmpfiles

**Files:**
- Create: `deploy/agent-plugins.txt`, `installer/internal/steps/scratch.go`, `installer/internal/steps/scratch_test.go`
- Modify: `installer/internal/ship/plugins.go`, `installer/internal/ship/ship.go:80`, `installer/internal/ship/ship_test.go`, `installer/internal/steps/claude.go`, `installer/internal/steps/claude_test.go`, `installer/internal/steps/list.go`

**Interfaces:**
- Produces:
  - `planPlugins(configDir string, allow []string, private func(string) (bool, error)) (planned, error)`; `allow == nil` keeps the old behaviour; a listed plugin that is not enabled is an error naming it.
  - `readAllowlist(path string) ([]string, error)` (missing file → `nil, nil`)
  - `type Scratch struct{}`, a root step writing `/etc/tmpfiles.d/claude-scratch.conf` in systemd mode.

- [ ] **Step 1: Write the failing tests**

In `installer/internal/ship/ship_test.go`, change the existing call to `planPlugins(dir, nil, func...)` and add (imports `os`, `strings` if missing):

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
	if err := os.WriteFile(path, []byte("# agents\ndocket@NovusEdge\n\ncurt@curt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readAllowlist(path)
	if err != nil || !slices.Equal(got, []string{"docket@NovusEdge", "curt@curt"}) {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := readAllowlist(filepath.Join(t.TempDir(), "none")); got != nil || err != nil {
		t.Fatalf("missing file: %v %v", got, err)
	}
}
```

In `installer/internal/steps/claude_test.go`, add:

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

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./installer/...`
Expected: FAIL (wrong `planPlugins` arity, missing `readAllowlist`, `Scratch`, version, setting).

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

`installer/internal/ship/plugins.go` (add `errors` to the imports): add the `allow []string` parameter; after `slices.Sort(names)`:

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

`installer/internal/steps/claude.go`: `var minClaude = [3]int{2, 1, 295}` with the comment "The gate hook's onFailure setting needs this version."; the check message `"needs 2.1.295"`. Leave `pushSettings` and `loginNote` as they are: `remoteControlAtStartup` would connect every session, and the consent step stays in the login. Update `TestClaudeCheck` or any test that asserts the old version text.

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

- [ ] **Step 4: Run the tests**

Run: `go test ./... && uv run pytest -q`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add deploy/agent-plugins.txt installer
git commit -s -m "Ship only allowlisted plugins, enable Remote Control and add scratch cleanup to hosts"
```

---

### Task 10: Host build tools (outside the spec)

Hosts need `make` and a C compiler: nimblefox's `./setup` builds pgvector from source, and `la-nimble` failed NF-211's setup without them on 2026-10-19. This is a separate commit so it can be reviewed and reverted on its own.

**Files:**
- Modify: `installer/internal/steps/packages.go`, `installer/internal/steps/root_test.go`

- [ ] **Step 1: Update the tests**

In `installer/internal/steps/root_test.go`:
- `TestPackagesCheckNamesMissingTools`: also answer `sh -c command -v make` and `sh -c command -v cc` with exit 0, so the detail stays `missing: gh`.
- `TestPackagesApplyInstallsOnlyMissingTools`: also answer `make` and `cc` with exit 0, so the expected `pacman -Syu --noconfirm --needed github-cli nodejs` call is unchanged.
- `TestPackagesApplyAddsGHRepoOnRHELOnly`: also answer `make` and `cc` with exit 0.
- Add:

```go
func TestPackagesInstallsBuildToolsWhenMissing(t *testing.T) {
	f := host.NewFake().On("pacman -Syu --noconfirm --needed make gcc", 0, "")
	for _, tool := range []string{"git", "tmux", "curl", "gh", "node"} {
		f.On("sh -c command -v "+tool, 0, "/usr/bin/"+tool)
	}
	e := testEnv(t, f)
	e.Distro = host.Distro{ID: "arch", Manager: host.Pacman}
	if err := (Packages{}).Apply(context.Background(), e); err != nil {
		t.Fatalf("%v; calls %v", err, f.Calls)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./installer/internal/steps/`
Expected: FAIL in `TestPackagesInstallsBuildToolsWhenMissing`.

- [ ] **Step 3: Implement**

In `installer/internal/steps/packages.go`:

```go
// make and cc: repos' ./setup builds native code (nimblefox compiles pgvector).
var tools = []string{"git", "tmux", "curl", "gh", "node", "make", "cc"}
```

and in `pkgName` add `case tool == "cc": return "gcc"`.

- [ ] **Step 4: Run the tests**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add installer/internal/steps/packages.go installer/internal/steps/root_test.go
git commit -s -m "Install make and a C compiler on hosts"
```

---

### Task 11: Docs and host verification

**Files:**
- Modify: `docs/vm-setup.md`, `docs/superpowers/specs/2026-10-20-agent-loop-design.md` (Unconfirmed results)

- [ ] **Step 1: Document**

In `docs/vm-setup.md`, add under "What the installer cannot do":

```markdown
- On your desktop, add the same scratchpad backstop the installer writes on hosts:
  `echo 'e /tmp/claude-* - - - 2d' | sudo tee /etc/tmpfiles.d/claude-scratch.conf`.
- `deploy/agent-plugins.txt` decides which of your plugins agent sessions get. A plugin listed
  there must be enabled on your desktop, or `just ship` stops and names it.
```

and a short section "Subagents in agent sessions": the catalogue lives in `plugin/agents/`, the budgets in `src/linear_agents/gate.py`, every launch is logged in `<state_dir>/launches.jsonl` and counted in `linear-agents status`, grants are an `allow:` line in the agent block or in a comment, and blocking questions arrive in the Claude app.

Commit:

```bash
git add docs/vm-setup.md
git commit -s -m "Document subagent limits, the plugin allowlist and the desktop scratch rule"
```

- [ ] **Step 2 (owner or main session, not a subagent): ship and verify on la-nimble**

After the branch is merged, run from the desktop:

```bash
just ship root@la-nimble --skip-interactive
just host root@la-nimble claude --version
```

Then check each Unconfirmed item on the host and record the answer and date in the spec's Unconfirmed section:

1. `onFailure`: temporarily break the gate (`chmod -x` on the `linear-agents` entry point in a scratch session's PATH copy) and confirm an `Agent` call is refused; restore it.
2. Workflow input: in a session with a `workflow` grant, start a workflow and read the logged `what` in `launches.jsonl`; if empty, record which field carries the name and update `subject()`.
3. `maxTurns` / `effort` honoured for plugin agents: run `linear-agents:source-finder` once and check its transcript stays under 20 turns.
4. `enabledPlugins` merge: run `/plugin` in a host session and confirm glowup is disabled.
5. `AskUserQuestion` push: in an implementer session, ask a question and confirm the phone gets a push.
6. Remote Control scope: confirm a tester session does not appear in the Claude app and the implementer that follows does.
7. `SubagentStop` carries `agent_type`: after a catalogue agent finishes, its `stop` line in `launches.jsonl` has a non-empty `what`, and the running count drops.
8. Keystrokes in an open `AskUserQuestion` dialog: in a host session with a question open, send text with `tmux send-keys` and note whether it selects an option. This decides whether the hold on forwarded comments and handoff requests (`asking` present) is needed or can be relaxed.
9. `PostToolUseFailure`: (a) refuse an `Agent` call through a permission rule after the gate allowed it (a session whose deny rule still blocks `general-purpose`) and check whether the hook fires and the running count drops; (b) press Esc on an open `AskUserQuestion` and check that `asking` is gone, through `PostToolUseFailure` or `Stop`.
10. The `Workflow` tool's `name`: check whether it is a free label the agent chooses. If so, a `workflow <name>` grant does not restrict which script runs, only the label; record that.
