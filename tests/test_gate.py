import json
import multiprocessing
import os
from concurrent.futures import ProcessPoolExecutor
from pathlib import Path

import pytest

from flocc.gate import (
    BUDGETS,
    LOG,
    Budget,
    clear_question,
    decide,
    launches_used,
    note_idle,
    note_question,
    record_session,
    record_stop,
    run_gate,
    run_id,
    running_agents,
)
from flocc.grants import Grants, launch_flags, set_block

NO_GRANTS = Grants()


def agent(sub="flocc:task-implementer", **extra):
    return {"tool_name": "Agent", "tool_input": {"subagent_type": sub, "prompt": "p", **extra}}


def d(payload, phase="impl", grants=NO_GRANTS, launched=0, running=0, refused=(), **kw):
    # By default the session was launched with the grants in force, as at a fresh handoff.
    flags = kw.get("flags", launch_flags(grants))
    return decide(
        payload,
        phase,
        grants,
        launched,
        running,
        set(refused),
        flags,
        frozenset(kw.get("started", ())),
    )


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


def test_per_call_effort_is_refused():
    dec = d(agent(effort="max"))
    assert dec.rule == "effort"
    assert "Retry without `effort`; the agent file sets it." in dec.reason


def test_teammates_and_isolation_are_refused():
    assert d(agent(team_name="t")).rule == "teammate"
    assert d(agent(name="helper")).rule == "teammate"
    assert d(agent(isolation="worktree")).rule == "isolation"


def test_per_call_parameters_are_refused_with_a_retry_that_keeps_the_delegation():
    assert "Retry without `model`; the agent file sets it." in d(agent(model="opus")).reason
    assert "Retry without `name` and `team_name`." in d(agent(name="h")).reason
    assert "Retry without `isolation`." in d(agent(isolation="worktree")).reason


def test_missing_subagent_type_is_general_purpose():
    p = {"tool_name": "Agent", "tool_input": {"prompt": "p"}}
    assert d(p).rule == "not-allowed"
    assert d(p, grants=Grants(agents=("general-purpose",))).allowed


def test_a_grant_that_changes_launch_flags_waits_for_the_next_session():
    g = Grants(agents=("general-purpose",), workflows=("big-build",))
    p = {"tool_name": "Agent", "tool_input": {"prompt": "p"}}
    dec = d(p, grants=g, flags=frozenset())
    assert (dec.allowed, dec.rule) == (False, "next-session")
    assert "next handoff" in dec.reason and "escalate" not in dec.reason
    wf = {"tool_name": "Workflow", "tool_input": {"name": "big-build"}}
    assert d(wf, grants=g, flags=frozenset()).rule == "next-session"
    assert d(wf, grants=g, flags=frozenset({"agent:general-purpose"})).rule == "next-session"
    assert d(wf, grants=g, flags=frozenset({"workflow:big-build"})).allowed
    assert d(p, grants=g, flags=frozenset({"workflow:big-build"})).rule == "next-session"
    assert d(p, grants=g, flags=frozenset({"agent:general-purpose"})).allowed


def test_a_catalogue_grant_applies_at_once():
    g = Grants(agents=("code-mapper",))
    assert d(agent("code-mapper"), phase="test", grants=g, flags=frozenset()).allowed


def test_an_ungranted_general_purpose_agent_is_not_allowed_whatever_the_flags():
    p = {"tool_name": "Agent", "tool_input": {"prompt": "p"}}
    assert d(p, flags=frozenset({"agent:general-purpose"})).rule == "not-allowed"


def test_agent_outside_the_phase_list_is_refused_unless_granted():
    assert d(agent("flocc:wave-reviewer"), phase="test").rule == "not-allowed"
    g = Grants(agents=("wave-reviewer",))
    assert d(agent("flocc:wave-reviewer"), phase="test", grants=g).allowed


def test_launch_budget_and_its_grant():
    full = BUDGETS["impl"].launches
    assert d(agent(), launched=full).rule == "launches"
    assert d(agent(), launched=full, grants=Grants(launches=full + 2)).allowed


def test_running_limit():
    assert d(agent(), running=BUDGETS["impl"].running).rule == "running"


def test_launches_reason_names_the_limit_and_the_count_used():
    full = BUDGETS["impl"].launches
    assert f"the impl phase used {full} of its {full} launches" in d(agent(), launched=full).reason
    granted = d(agent(), launched=full + 2, grants=Grants(launches=full + 2)).reason
    assert f"used {full + 2} of its {full + 2} launches" in granted


def test_workflow_needs_a_named_grant():
    wf = {"tool_name": "Workflow", "tool_input": {"name": "big-build"}}
    assert d(wf).rule == "workflow"
    assert d(wf, grants=Grants(workflows=("big-build",))).allowed
    unnamed = {"tool_name": "Workflow", "tool_input": {"script": "..."}}
    assert d(unnamed, grants=Grants(workflows=("big-build",))).rule == "workflow"


def test_a_granted_workflow_starts_once_per_run():
    wf = {"tool_name": "Workflow", "tool_input": {"name": "big-build"}}
    g = Grants(workflows=("big-build",))
    dec = d(wf, grants=g, started=["big-build"])
    assert (dec.allowed, dec.rule) == (False, "workflow")
    assert "already started in this run" in dec.reason and "escalate --tier" not in dec.reason
    assert d(wf, grants=g, started=["other"]).allowed


def test_a_granted_workflow_is_held_to_the_launch_budget():
    wf = {"tool_name": "Workflow", "tool_input": {"name": "big-build"}}
    g = Grants(workflows=("big-build",))
    full = BUDGETS["impl"].launches
    assert d(wf, grants=g, launched=full).rule == "launches"
    assert d(wf, grants=g, launched=full - 1).allowed


def test_unknown_or_missing_phase_is_refused():
    assert d(agent(), phase=None).rule == "phase"
    assert d(agent(), phase="deploy").rule == "phase"


def test_review_phase_allows_nothing():
    assert d(agent("flocc:source-finder"), phase="review").rule == "not-allowed"


def test_repeat_refusal_is_keyed_by_rule_and_agent():
    first = d(agent(), launched=99)
    assert "flocc escalate" in first.reason
    again = d(agent(), launched=99, refused={("launches", "task-implementer")})
    assert "already escalated" in again.reason and "escalate --tier" not in again.reason
    other = d(agent("code-mapper"), launched=99, refused={("launches", "task-implementer")})
    assert "flocc escalate" in other.reason


def test_only_grantable_refusals_offer_escalation():
    assert "flocc escalate" not in d(agent(), running=BUDGETS["impl"].running).reason
    nested = agent()
    nested["agent_id"] = "a1"
    for payload, phase in [
        (nested, "impl"),
        (agent(), "deploy"),
    ]:
        reason = d(payload, phase=phase).reason
        assert "cannot be granted" in reason and "flocc escalate" not in reason
    for payload in [
        agent(model="opus"),
        agent(effort="low"),
        agent(name="helper"),
        agent(team_name="t"),
        agent(isolation="worktree"),
    ]:
        reason = d(payload).reason
        assert "Retry without" in reason and "flocc escalate" not in reason
    assert "flocc escalate" in d(agent(), launched=99).reason
    assert "flocc escalate" in d(agent("code-mapper"), phase="test").reason


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
        record_stop(tmp_path, "fix@1", 0.0, "flocc:task-implementer")
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
    record_stop(tmp_path, "fix@1", 3.0, "flocc:fix-rereviewer")  # never launched
    reason = run_gate(agent(), tmp_path, "fix", "fix@1", 4.0)
    assert reason and "still running" in reason


def test_a_granted_workflow_is_a_launch_but_not_a_running_agent(tmp_path):
    set_block(tmp_path / "grants.json", Grants(workflows=("big-build",)))
    wf = {"tool_name": "Workflow", "tool_input": {"name": "big-build"}}
    flags = frozenset({"workflow:big-build"})
    assert run_gate(wf, tmp_path, "fix", "fix@1", 0.0, flags) is None
    assert run_gate(agent(), tmp_path, "fix", "fix@1", 1.0, flags) is None
    assert launches_used(tmp_path, "fix@1") == 2


def test_a_granted_workflow_cannot_start_twice_in_a_run(tmp_path):
    set_block(tmp_path / "grants.json", Grants(workflows=("big-build",)))
    wf = {"tool_name": "Workflow", "tool_input": {"name": "big-build"}}
    flags = frozenset({"workflow:big-build"})
    assert run_gate(wf, tmp_path, "impl", "impl@1", 0.0, flags) is None
    assert "already started" in (run_gate(wf, tmp_path, "impl", "impl@1", 1.0, flags) or "")
    assert run_gate(wf, tmp_path, "impl", "impl@2", 2.0, flags) is None, "a new run may start it"


def test_a_workflow_grant_waits_for_the_next_session_in_run_gate(tmp_path):
    set_block(tmp_path / "grants.json", Grants(workflows=("big-build",)))
    wf = {"tool_name": "Workflow", "tool_input": {"name": "big-build"}}
    assert "next handoff" in (run_gate(wf, tmp_path, "impl", "impl@1", 0.0) or "")


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


def test_running_agents_counts_launches_without_a_stop(tmp_path):
    assert running_agents(tmp_path, "impl@1", "impl") == 0
    assert run_gate(agent(), tmp_path, "impl", "impl@1", 0.0) is None
    assert run_gate(agent("flocc:code-mapper"), tmp_path, "impl", "impl@1", 0.0) is None
    assert running_agents(tmp_path, "impl@1", "impl") == 2
    assert running_agents(tmp_path, "impl@2", "impl") == 0
    record_stop(tmp_path, "impl@1", 1.0, "code-mapper")
    assert running_agents(tmp_path, "impl@1", "impl") == 1
    record_session(tmp_path, "impl@1", 2.0, "impl")
    assert running_agents(tmp_path, "impl@1", "impl") == 0


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
    assert json.loads((tmp_path / "idle").read_text())["message"] == "waiting on you"


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


def _escalate(state, waiting, mtime):
    path = state / "escalations.jsonl"
    path.write_text(json.dumps({"tier": 2, "waiting": waiting}) + "\n")
    os.utime(path, (mtime, mtime))


def _stopped_at(state, mtime):
    (state / "last-stop").touch()
    os.utime(state / "last-stop", (mtime, mtime))


def test_idle_marker_notes_a_waiting_escalation_made_since_the_last_stop(tmp_path):
    _stopped_at(tmp_path, 100)
    _escalate(tmp_path, True, 200)
    note_idle(tmp_path, {}, 300.0)
    assert json.loads((tmp_path / "idle").read_text())["waiting_on_owner"] is True
    # The Stop just recorded is now the reference: the same escalation no longer counts.
    note_idle(tmp_path, {}, 400.0)
    assert json.loads((tmp_path / "idle").read_text())["waiting_on_owner"] is False


@pytest.mark.parametrize(
    "setup",
    [
        lambda s: _escalate(s, True, 50),
        lambda s: _escalate(s, False, 200),
        lambda s: (s / "escalations.jsonl").write_text(""),
        lambda s: (s / "escalations.jsonl").write_text("not json\n"),
        lambda s: None,
    ],
    ids=["before-the-last-stop", "not-waiting", "empty", "torn", "no-file"],
)
def test_idle_marker_ignores_other_escalations(tmp_path, setup):
    _stopped_at(tmp_path, 100)
    setup(tmp_path)
    note_idle(tmp_path, {}, 300.0)
    assert json.loads((tmp_path / "idle").read_text())["waiting_on_owner"] is False
