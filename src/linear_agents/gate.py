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
