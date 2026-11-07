import json

import pytest

from flocc import host, ledger
from flocc.context import state_dir
from flocc.control import Queued, Refused, resume, resumes
from flocc.publisher import PublishResult
from flocc.runtime import Runtime
from flocc.supervisor import BLOCKED, FIX_CONTEXT, RESUME_NOTE, LaunchError
from tests.fakes import FakeLinear, FakeNotifier, FakeSessions
from tests.test_ledger import make


@pytest.fixture
def rt(tmp_cfg, tmp_path):
    return Runtime(
        cfg=tmp_cfg,
        con=ledger.connect(tmp_cfg.ledger_path),
        linear=FakeLinear(),
        sessions=FakeSessions(),
        notifier=FakeNotifier(),
        now=lambda: 100.0,
        exe="/opt/flocc",
        claude_exe="/opt/claude",
        config_path=tmp_path / "c.toml",
        publish_fn=lambda claim, title, summary, **kw: PublishResult("opened", url="https://pr/1"),
        git_head=lambda claim: "sha",
        review_diff=lambda claim: "",
        comment_pr=lambda claim, url, body: None,
    )


def blocked(rt, tmp_path, cause="idle", **kw):
    wt = tmp_path / "wt" / "NF-1"
    wt.mkdir(parents=True, exist_ok=True)
    c = make("NF-1", state="blocked", worktree=str(wt))
    c.phase = "impl"
    for k, v in kw.items():
        setattr(c, k, v)
    ledger.insert_claim(rt.con, c)
    d = state_dir(rt.cfg, "NF-1")
    d.mkdir(parents=True, exist_ok=True)
    if cause is not None:
        (d / BLOCKED).write_text(f"{cause}\n\nwhy it stopped")
    return c


def test_resume_after_idle_relaunches_the_executor_with_the_note(rt, tmp_path):
    blocked(rt, tmp_path)
    claim = resume(rt, "NF-1", "Finish the migration in db/schema.py, then run flocc done.")
    assert (claim.state, claim.phase, claim.fix_attempts, claim.started_at) == (
        "running",
        "impl",
        0,
        100.0,
    )
    name, argv = rt.sessions.started[-1]
    assert name == "NF-1" and any("db/schema.py" in a for a in argv)
    d = state_dir(rt.cfg, "NF-1")
    assert "db/schema.py" in (d / RESUME_NOTE).read_text() and not (d / BLOCKED).exists()
    assert rt.linear.comments[-1].startswith("cardinal resumed the executor after idle:")
    assert ledger.meta_get(rt.con, "cardinal_resume:NF-1") == "1"


def test_resume_answers_the_questions_in_the_tester_s_own_phase(rt, tmp_path):
    blocked(rt, tmp_path, cause="needs-owner", phase="test")
    d = state_dir(rt.cfg, "NF-1")
    (d / "questions.json").write_text(json.dumps(["Which timezone do dates use?"]))
    resume(rt, "NF-1", "UTC everywhere; the spec says so under Dates.")
    name, argv = rt.sessions.started[-1]
    prompt = " ".join(argv)
    assert name == "NF-1-test"
    assert "1. Which timezone do dates use?" in prompt and "UTC everywhere" in prompt
    assert "answered the questions" in prompt


def test_resume_after_a_repeated_check_failure_starts_a_fix_round_with_the_check(rt, tmp_path):
    blocked(rt, tmp_path, cause="check-failed", phase="fix", fix_attempts=1)
    d = state_dir(rt.cfg, "NF-1")
    (d / FIX_CONTEXT).write_text("The check failed with:\n\nFAILED tests/test_a.py::test_x")
    claim = resume(rt, "NF-1", "test_x needs the fixture from conftest, not a new one.")
    assert (claim.phase, claim.fix_attempts) == ("fix", 2)
    context = (d / FIX_CONTEXT).read_text()
    assert context.index("needs the fixture") < context.index("FAILED tests/test_a.py::test_x")
    assert not (d / RESUME_NOTE).exists()


def test_resume_on_a_timed_out_run_says_so(rt, tmp_path):
    blocked(rt, tmp_path, cause=None, state="timed_out")
    resume(rt, "NF-1", "finish the migration")
    assert "after it timed out" in (state_dir(rt.cfg, "NF-1") / RESUME_NOTE).read_text()
    assert "after timed-out" in rt.linear.comments[-1]


@pytest.mark.parametrize(
    "cause", ["tests-touched", "branch-moved", "publish-failed", "rebase-conflict", "no-tests"]
)
def test_the_owner_s_stops_are_refused(rt, tmp_path, cause):
    blocked(rt, tmp_path, cause=cause)
    with pytest.raises(Refused, match=f"stopped with {cause}"):
        resume(rt, "NF-1", "note")
    assert rt.sessions.started == []


def test_a_block_with_no_recorded_cause_is_refused(rt, tmp_path):
    blocked(rt, tmp_path, cause=None)
    with pytest.raises(Refused, match="no recorded cause"):
        resume(rt, "NF-1", "note")


def test_a_check_failure_at_the_fix_cap_is_refused(rt, tmp_path):
    blocked(rt, tmp_path, cause="check-failed", phase="fix", fix_attempts=2)
    with pytest.raises(Refused, match="used 2 of 2 fix rounds"):
        resume(rt, "NF-1", "note")


def test_a_second_resume_on_the_same_run_is_refused(rt, tmp_path):
    blocked(rt, tmp_path)
    resume(rt, "NF-1", "first")
    ledger.update_claim(rt.con, "NF-1", state="blocked")
    (state_dir(rt.cfg, "NF-1") / BLOCKED).write_text("idle\n\nagain")
    with pytest.raises(Refused, match="already resumed"):
        resume(rt, "NF-1", "second")


def test_the_old_fix_key_no_longer_caps(rt, tmp_path):
    blocked(rt, tmp_path)
    ledger.meta_set(rt.con, "cardinal_fix:NF-1", "1")
    assert resume(rt, "NF-1", "note").state == "running"


@pytest.mark.parametrize(
    ("setup", "match"), [({"state": "running"}, "is running"), ({"state": "done"}, "is done")]
)
def test_resume_is_refused_outside_its_window(rt, tmp_path, setup, match):
    blocked(rt, tmp_path, **setup)
    with pytest.raises(Refused, match=match):
        resume(rt, "NF-1", "note")
    assert rt.sessions.started == []


def test_resume_is_refused_for_a_held_issue_and_without_a_note(rt, tmp_path):
    blocked(rt, tmp_path)
    with pytest.raises(Refused, match="pass a note"):
        resume(rt, "NF-1", "  ")
    with pytest.raises(Refused, match="keep it under 2000"):
        resume(rt, "NF-1", "x" * 2001)
    ledger.add_hold(rt.con, "NF-1", "wait", 1.0)
    with pytest.raises(Refused, match="is held"):
        resume(rt, "NF-1", "note")


def test_a_launch_that_fails_leaves_the_run_as_it_was(rt, tmp_path):
    blocked(rt, tmp_path)
    rt.cfg.agent_gh_token_file.unlink()
    with pytest.raises(LaunchError):
        resume(rt, "NF-1", "note")
    assert ledger.get_claim(rt.con, "NF-1").state == "blocked"  # type: ignore[union-attr]
    assert ledger.meta_get(rt.con, "cardinal_resume:NF-1") is None
    assert (state_dir(rt.cfg, "NF-1") / BLOCKED).exists()
    assert rt.linear.comments == [] and resumes(rt.con, 0.0) == []


def test_resume_is_refused_while_the_host_is_under_pressure(rt, tmp_path):
    blocked(rt, tmp_path)
    host.open_episode(rt.con, 1.0, "/tmp 93% (2.8/3.0 GiB)")
    with pytest.raises(Refused, match="not now: host under pressure"):
        resume(rt, "NF-1", "note")
    assert rt.sessions.started == []


def test_resume_with_the_session_cap_full_is_queued_and_uses_the_one_resume(rt, tmp_path):
    blocked(rt, tmp_path)
    for key in ("NF-8", "NF-9"):
        ledger.insert_claim(rt.con, make(key))
    with pytest.raises(Queued, match="session cap 2 reached"):
        resume(rt, "NF-1", "do the thing")
    assert rt.sessions.started == []
    assert json.loads(ledger.meta_get(rt.con, "resume_pending:NF-1") or "null") == {
        "note": "do the thing",
        "at": 100.0,
    }
    assert ledger.get_claim(rt.con, "NF-1").state == "blocked"  # type: ignore[union-attr]
    with pytest.raises(Refused, match="already resumed"):
        resume(rt, "NF-1", "again")


def test_resume_refused_for_usage_is_queued_too(rt, tmp_path):
    blocked(rt, tmp_path)
    ledger.insert_claim(rt.con, make("NF-9"))
    with pytest.raises(Queued, match="usage unknown"):
        resume(rt, "NF-1", "note")
    assert ledger.meta_get(rt.con, "resume_pending:NF-1")


def test_a_refusal_that_will_not_clear_by_itself_is_not_queued(rt, tmp_path):
    blocked(rt, tmp_path)
    host.open_episode(rt.con, 1.0, "/tmp 93% (2.8/3.0 GiB)")
    with pytest.raises(Refused, match="not now"):
        resume(rt, "NF-1", "note")
    assert not ledger.meta_get(rt.con, "resume_pending:NF-1")


@pytest.mark.parametrize("old", [None, "The check failed with:\n\nFAILED tests/test_a.py::test_x"])
def test_a_failed_fix_launch_restores_the_fix_context(rt, tmp_path, old):
    blocked(rt, tmp_path, cause="check-failed", phase="fix", fix_attempts=1)
    context = state_dir(rt.cfg, "NF-1") / FIX_CONTEXT
    if old is not None:
        context.write_text(old)
    rt.cfg.agent_gh_token_file.unlink()
    with pytest.raises(LaunchError):
        resume(rt, "NF-1", "note")
    assert (context.read_text() if context.exists() else None) == old


def test_each_resume_is_logged(rt, tmp_path):
    blocked(rt, tmp_path, cause="needs-owner")
    resume(rt, "NF-1", "answer")
    assert resumes(rt.con, 0.0) == [(100.0, "NF-1", "needs-owner", "impl")]
