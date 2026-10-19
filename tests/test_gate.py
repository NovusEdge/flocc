import json
import multiprocessing
from concurrent.futures import ProcessPoolExecutor
from pathlib import Path

from linear_agents.gate import (
    BUDGETS,
    LOG,
    Budget,
    clear_question,
    decide,
    launches_used,
    note_question,
    record_session,
    record_stop,
    run_gate,
    run_id,
)
from linear_agents.grants import Grants, set_block

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


def test_only_grantable_refusals_offer_escalation():
    assert "linear-agents escalate" not in d(agent(), running=BUDGETS["impl"].running).reason
    nested = agent()
    nested["agent_id"] = "a1"
    for payload, phase in [
        (agent(model="opus"), "impl"),
        (nested, "impl"),
        (agent(name="helper"), "impl"),
        (agent(team_name="t"), "impl"),
        (agent(isolation="worktree"), "impl"),
        (agent(), "deploy"),
    ]:
        reason = d(payload, phase=phase).reason
        assert "cannot be granted" in reason and "linear-agents escalate" not in reason
    assert "linear-agents escalate" in d(agent(), launched=99).reason
    assert "linear-agents escalate" in d(agent("code-mapper"), phase="test").reason


def test_budgets_are_pinned():
    assert BUDGETS["test"] == Budget(("source-finder", "source-reader"), 2, 2)
    assert BUDGETS["impl"] == Budget(
        (
            "task-implementer",
            "wave-reviewer",
            "fix-rereviewer",
            "plan-reviewer",
            "code-mapper",
            "source-finder",
            "source-reader",
        ),
        10,
        4,
    )
    assert BUDGETS["fix"] == Budget(("task-implementer", "fix-rereviewer"), 2, 1)
    assert BUDGETS["review"] == Budget((), 0, 0)


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


def test_a_stop_frees_only_the_slot_of_its_own_type(tmp_path):
    assert run_gate(agent(), tmp_path, "fix", "fix@1", 0.0) is None
    record_stop(tmp_path, "fix@1", 1.0, "some-workflow-agent")
    assert run_gate(agent(), tmp_path, "fix", "fix@1", 2.0)
    record_stop(tmp_path, "fix@1", 3.0, "linear-agents:fix-rereviewer")  # never launched
    reason = run_gate(agent(), tmp_path, "fix", "fix@1", 4.0)
    assert reason and "still running" in reason


def test_a_granted_workflow_is_a_launch_but_not_a_running_agent(tmp_path):
    set_block(tmp_path / "grants.json", Grants(workflows=("big-build",)))
    wf = {"tool_name": "Workflow", "tool_input": {"name": "big-build"}}
    assert run_gate(wf, tmp_path, "fix", "fix@1", 0.0) is None
    assert run_gate(agent(), tmp_path, "fix", "fix@1", 1.0) is None
    assert launches_used(tmp_path, "fix@1") == 2


def test_log_lines_carry_the_phase(tmp_path):
    run_gate(agent(), tmp_path, "fix", "fix@1", 0.0)
    record_stop(tmp_path, "fix@1", 1.0, "task-implementer", "fix")
    record_session(tmp_path, "fix@1", 2.0, "fix")
    lines = [json.loads(x) for x in (tmp_path / LOG).read_text().splitlines()]
    assert [(x["event"], x["phase"]) for x in lines] == [
        ("launch", "fix"),
        ("stop", "fix"),
        ("session", "fix"),
    ]


def test_run_id():
    assert run_id("fix", 1.5) == "fix@1.5"


def test_ignored_tools_are_not_logged(tmp_path):
    assert run_gate({"tool_name": "TaskList", "tool_input": {}}, tmp_path, "impl", "r", 0.0) is None
    assert not (tmp_path / LOG).exists()


def test_torn_line_is_skipped_and_the_next_entry_kept(tmp_path):
    first = {"at": 0, "run": "fix@1", "event": "launch", "what": "task-implementer"}
    (tmp_path / LOG).write_text(json.dumps(first) + '\n{"at": 1, "ru')
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
    payload = {
        "tool_name": "AskUserQuestion",
        "tool_input": {"questions": [{"question": "A or B?"}]},
    }
    note_question(tmp_path, payload, 5.0)
    assert json.loads((tmp_path / "asking").read_text()) == {"at": 5.0, "question": "A or B?"}
    (tmp_path / "asking-escalated").touch()
    clear_question(tmp_path)
    assert not (tmp_path / "asking").exists() and not (tmp_path / "asking-escalated").exists()


def test_a_new_question_drops_the_old_escalation_mark(tmp_path):
    payload = {"tool_name": "AskUserQuestion", "tool_input": {"questions": [{"question": "Q"}]}}
    (tmp_path / "asking-escalated").touch()
    note_question(tmp_path, payload, 5.0)
    assert not (tmp_path / "asking-escalated").exists()
    assert sorted(p.name for p in tmp_path.iterdir()) == ["asking"]
