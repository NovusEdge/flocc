import fcntl
import json
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path

from linear_agents.grants import Grants, for_phase

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
GRANTABLE = frozenset({"not-allowed", "launches", "workflow"})
LOG = "launches.jsonl"


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
    if rule not in GRANTABLE:
        tail = "This cannot be granted; carry on without it."
    elif (rule, what) in refused:
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


@contextmanager
def _locked(state: Path) -> Iterator[None]:
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
    refused = {
        (e.get("rule", ""), e.get("what", "")) for e in entries if e.get("event") == "refused"
    }
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
