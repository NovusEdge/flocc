import fcntl
import json
import os
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
LAUNCH_GRANTS_ENV = "LINEAR_AGENTS_LAUNCH_GRANTS"
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


def _refuse(
    rule: str, what: str, why: str, refused: set[tuple[str, str]], tail: str | None = None
) -> Decision:
    if tail is None and rule not in GRANTABLE:
        tail = "This cannot be granted; carry on without it."
    elif tail is None and (rule, what) in refused:
        tail = "This was already escalated in this run; do not retry it or escalate again."
    elif tail is None:
        tail = ESCALATE.format(rule=rule)
    return Decision(False, rule, what, f"linear-agents gate: {why} {tail}")


_FINAL = "This cannot be granted; carry on without it."


def _used(phase: str | None, launched: int, limit: int) -> str:
    return f"the {phase} phase used {launched} of its {limit} launches."


def _next_session(item: str, what: str, refused: set[tuple[str, str]]) -> Decision:
    # The grant is recorded, but this session started with the deny rule or disableWorkflows,
    # so Claude Code would refuse the call after the gate allowed it and no stop would follow.
    why = f"{item} is granted, but the grant applies from the next handoff, not this session."
    return _refuse("next-session", what, why, refused, "Carry on without it until then.")


def decide(
    payload: dict,
    phase: str | None,
    grants: Grants,
    launched: int,
    running: int,
    refused: set[tuple[str, str]],
    flags: frozenset[str],
    started: frozenset[str],
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
    limit = grants.launches if grants.launches is not None else budget.launches
    if tool == "Workflow":
        if not what or what not in grants.workflows:
            return _refuse("workflow", what, f"workflow {what!r} has no grant.", refused)
        if f"workflow:{what}" not in flags:
            return _next_session(f"workflow {what!r}", what, refused)
        if what in started:
            why = f"workflow {what!r} was already started in this run."
            return _refuse("workflow", what, why, refused, _FINAL)
        if launched >= limit:
            return _refuse("launches", what, _used(phase, launched, limit), refused)
        return Decision(True, "allowed", what)
    if args.get("model"):
        why = "agent files fix the model."
        return _refuse(
            "model", what, why, refused, "Retry without `model`; the agent file sets it."
        )
    if args.get("effort"):
        why = "agent files fix the effort."
        tail = "Retry without `effort`; the agent file sets it."
        return _refuse("effort", what, why, refused, tail)
    if args.get("team_name") or args.get("name"):
        why = "named teammates are not allowed here."
        return _refuse("teammate", what, why, refused, "Retry without `name` and `team_name`.")
    if args.get("isolation"):
        why = "agents share the claimed worktree."
        return _refuse("isolation", what, why, refused, "Retry without `isolation`.")
    if what not in budget.agents and what not in grants.agents:
        names = ", ".join(PREFIX + a for a in budget.agents) or "none"
        why = f"{what} is not allowed in the {phase} phase (allowed: {names})."
        return _refuse("not-allowed", what, why, refused)
    if what == GENERAL and f"agent:{GENERAL}" not in flags:
        return _next_session(f"agent {GENERAL}", what, refused)
    if launched >= limit:
        return _refuse("launches", what, _used(phase, launched, limit), refused)
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
    with (state / LOG).open("a+b") as f:
        # After a torn line, start on a fresh one so this entry is not glued to the fragment.
        if f.seek(0, 2):
            f.seek(-1, 2)
            if f.read(1) != b"\n":
                f.write(b"\n")
        f.write((json.dumps(entry) + "\n").encode())


def run_id(phase: str, started_at: float) -> str:
    return f"{phase}@{started_at}"


def _counts(entries: list[dict], counted: set[str]) -> tuple[int, int, set[tuple[str, str]]]:
    launched = sum(e.get("event") == "launch" for e in entries)
    refused = {
        (e.get("rule", ""), e.get("what", "")) for e in entries if e.get("event") == "refused"
    }
    # Agents end with their session, so only what happened since the last session start counts.
    last = max((i for i, e in enumerate(entries) if e.get("event") == "session"), default=-1)
    since = entries[last + 1 :]
    # Per type, so a stop with no matching launch (or a workflow's) frees no other type's slot.
    running = 0
    for kind in counted:
        started = sum(e.get("event") == "launch" and e.get("what") == kind for e in since)
        stopped = sum(e.get("event") == "stop" and e.get("what") == kind for e in since)
        running += max(started - stopped, 0)
    return launched, running, refused


def _counted(grants: Grants) -> set[str]:
    return set(CATALOGUE) | set(grants.agents)


def running_agents(state: Path, run: str | None, phase: str | None) -> int:
    grants = for_phase(state / "grants.json", phase or "")
    return _counts(_entries(state, run), _counted(grants))[1]


def _line(at: float, run: str | None, phase: str | None, event: str, what: str, rule: str) -> dict:
    return {"at": at, "run": run, "phase": phase, "event": event, "what": what, "rule": rule}


def run_gate(
    payload: dict,
    state: Path,
    phase: str | None,
    run: str | None,
    now: float,
    flags: frozenset[str] = frozenset(),
) -> str | None:
    if payload.get("tool_name") not in GATED_TOOLS:
        return None
    with _locked(state):
        grants = for_phase(state / "grants.json", phase or "")
        entries = _entries(state, run)
        launched, running, refused = _counts(entries, _counted(grants))
        started = frozenset(str(e.get("what")) for e in entries if e.get("event") == "launch")
        dec = decide(payload, phase, grants, launched, running, refused, flags, started)
        event = "launch" if dec.allowed else "refused"
        _append(state, _line(now, run, phase, event, dec.what, dec.rule))
    return None if dec.allowed else dec.reason


def record_stop(
    state: Path, run: str | None, now: float, agent_type: str, phase: str | None = None
) -> None:
    with _locked(state):
        _append(state, _line(now, run, phase, "stop", agent_type.removeprefix(PREFIX), ""))


def record_session(state: Path, run: str | None, now: float, phase: str | None = None) -> None:
    with _locked(state):
        _append(state, _line(now, run, phase, "session", "", ""))


def launches_used(state: Path, run: str) -> int:
    return sum(e.get("event") == "launch" for e in _entries(state, run))


def note_question(state: Path, payload: dict, now: float) -> None:
    questions = (payload.get("tool_input") or {}).get("questions") or [{}]
    text = str(questions[0].get("question", "")) if isinstance(questions[0], dict) else ""
    state.mkdir(parents=True, exist_ok=True)
    # A fresh question has not been escalated yet, whatever an earlier one was.
    (state / "asking-escalated").unlink(missing_ok=True)
    # The supervisor polls this file; a rename keeps it from reading a half-written one.
    tmp = state / "asking.tmp"
    tmp.write_text(json.dumps({"at": now, "question": text}))
    os.replace(tmp, state / "asking")


def clear_question(state: Path) -> None:
    for name in ("asking", "asking-escalated"):
        (state / name).unlink(missing_ok=True)


IDLE_MESSAGE_CHARS = 600


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


LAST_STOP = "last-stop"


def _waiting_on_owner(state: Path, since: float) -> bool:
    """True when the session recorded a `--waiting` escalation after `since`."""
    path = state / "escalations.jsonl"
    try:
        last = json.loads(path.read_text().splitlines()[-1])
        return path.stat().st_mtime > since and last.get("waiting") is True
    except (OSError, IndexError, ValueError, AttributeError):
        return False


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
    # The idle marker is removed whenever the session acts, so the previous Stop is kept apart.
    # SessionStart touches this file too, so an escalation from an earlier session never counts.
    stopped = state / LAST_STOP
    since = stopped.stat().st_mtime if stopped.exists() else 0.0
    marker = {
        "at": now,
        "message": message[:IDLE_MESSAGE_CHARS],
        "waiting_on_owner": _waiting_on_owner(state, since),
    }
    # Written whole by rename: the supervisor polls this file.
    tmp = state / "idle.tmp"
    tmp.write_text(json.dumps(marker))
    os.replace(tmp, state / "idle")
    stopped.touch()


def clear_idle(state: Path) -> None:
    for name in ("idle", "idle-escalated"):
        (state / name).unlink(missing_ok=True)
