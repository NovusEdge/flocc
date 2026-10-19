from linear_agents.gate import BUDGETS, decide
from linear_agents.grants import Grants

NO_GRANTS = Grants()


def agent(sub="linear-agents:task-implementer", **extra):
    return {"tool_name": "Agent", "tool_input": {"subagent_type": sub, "prompt": "p", **extra}}


def d(payload, phase="impl", grants=NO_GRANTS, launched=0, running=0, refused=()):
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
