import pytest

from flocc import host, ledger
from flocc.context import state_dir
from flocc.control import Refused, start_fix
from flocc.publisher import PublishResult
from flocc.runtime import Runtime
from flocc.supervisor import BLOCKED, FIX_CONTEXT, LaunchError
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


def blocked(rt, tmp_path, **kw):
    wt = tmp_path / "wt" / "NF-1"
    wt.mkdir(parents=True, exist_ok=True)
    c = make("NF-1", state="blocked", worktree=str(wt))
    c.phase = "impl"
    for k, v in kw.items():
        setattr(c, k, v)
    ledger.insert_claim(rt.con, c)
    d = state_dir(rt.cfg, "NF-1")
    d.mkdir(parents=True, exist_ok=True)
    (d / BLOCKED).write_text("idle\n\nstopped twice")
    return c


def test_fix_starts_one_fixer_with_the_note(rt, tmp_path):
    blocked(rt, tmp_path)
    claim = start_fix(rt, "NF-1", "The import path moved to flocc.events; update it.")
    assert (claim.state, claim.phase, claim.fix_attempts, claim.started_at) == (
        "running",
        "fix",
        1,
        100.0,
    )
    name, argv = rt.sessions.started[-1]
    assert name == "NF-1" and any("flocc.events" in a for a in argv)
    d = state_dir(rt.cfg, "NF-1")
    assert "flocc.events" in (d / FIX_CONTEXT).read_text() and not (d / BLOCKED).exists()
    assert "cardinal started a fixer" in rt.linear.comments[-1]


def test_a_second_cardinal_fixer_on_the_same_run_is_refused(rt, tmp_path):
    blocked(rt, tmp_path)
    start_fix(rt, "NF-1", "first")
    ledger.update_claim(rt.con, "NF-1", state="blocked")
    with pytest.raises(Refused, match="already started a fixer"):
        start_fix(rt, "NF-1", "second")


@pytest.mark.parametrize(
    ("setup", "match"),
    [
        ({"state": "running"}, "is running"),
        ({"state": "done"}, "is done"),
        ({"phase": "test"}, "stopped at the tester"),
        ({"fix_attempts": 2}, "used 2 of 2 fix rounds"),
    ],
)
def test_fix_is_refused_outside_its_window(rt, tmp_path, setup, match):
    blocked(rt, tmp_path, **setup)
    with pytest.raises(Refused, match=match):
        start_fix(rt, "NF-1", "note")
    assert rt.sessions.started == []


def test_fix_is_refused_for_a_held_issue_and_without_a_note(rt, tmp_path):
    blocked(rt, tmp_path)
    with pytest.raises(Refused, match="pass a note"):
        start_fix(rt, "NF-1", "  ")
    ledger.add_hold(rt.con, "NF-1", "wait", 1.0)
    with pytest.raises(Refused, match="is held"):
        start_fix(rt, "NF-1", "note")


def test_fix_on_a_timed_out_run_says_so_in_the_prompt(rt, tmp_path):
    blocked(rt, tmp_path, state="timed_out")
    start_fix(rt, "NF-1", "finish the migration")
    assert "after the run timed out" in (state_dir(rt.cfg, "NF-1") / FIX_CONTEXT).read_text()


def test_a_launch_that_fails_leaves_the_run_as_it_was(rt, tmp_path):
    blocked(rt, tmp_path)
    rt.cfg.agent_gh_token_file.unlink()
    with pytest.raises(LaunchError):
        start_fix(rt, "NF-1", "note")
    assert ledger.get_claim(rt.con, "NF-1").state == "blocked"  # type: ignore[union-attr]
    assert ledger.meta_get(rt.con, "cardinal_fix:NF-1") is None
    assert (state_dir(rt.cfg, "NF-1") / BLOCKED).exists()


def test_fix_is_refused_while_the_host_is_under_pressure(rt, tmp_path):
    blocked(rt, tmp_path)
    host.open_episode(rt.con, 1.0, "/tmp 93% (2.8/3.0 GiB)")
    with pytest.raises(Refused, match="not now: host under pressure"):
        start_fix(rt, "NF-1", "note")
    assert rt.sessions.started == []
