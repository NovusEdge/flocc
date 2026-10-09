import json
import subprocess

import pytest

from linear_agents import ledger
from linear_agents.context import state_dir
from linear_agents.ledger import Claim
from linear_agents.linear import LinearError
from linear_agents.publisher import PublishResult
from linear_agents.runtime import Runtime
from linear_agents.supervisor import LaunchError, supervise
from linear_agents.usage import write_status
from tests.fakes import FakeLinear, FakeNotifier, FakeSessions
from tests.test_ledger import make


@pytest.fixture
def rt(tmp_cfg, tmp_path):
    runtime = Runtime(
        cfg=tmp_cfg,
        con=ledger.connect(tmp_cfg.ledger_path),
        linear=FakeLinear(),
        sessions=FakeSessions(),
        notifier=FakeNotifier(),
        now=lambda: 100.0,
        exe="/opt/linear-agents",
        claude_exe="/opt/claude",
        config_path=tmp_path / "c.toml",
        publish_fn=lambda claim, title, summary: PublishResult("opened", url="https://pr/1"),
        git_head=lambda path: "sha-tests",
    )
    return runtime


def current(rt) -> Claim:
    c = ledger.get_claim(rt.con, "NF-1")
    assert c is not None
    return c


def add(rt, **kw):
    c = make("NF-1")
    c.base_sha = "sha-base"
    for k, v in kw.items():
        setattr(c, k, v)
    ledger.insert_claim(rt.con, c)
    state_dir(rt.cfg, "NF-1").mkdir(parents=True, exist_ok=True)
    return c


def done(rt, summary="ok"):
    (state_dir(rt.cfg, "NF-1") / "done.json").write_text(json.dumps({"summary": summary}))


def usage_status(ctx=10.0):
    window = {"used_percentage": 20.0, "resets_at": 4_000_000_000}
    return {
        "rate_limits": {"five_hour": window, "seven_day": window},
        "context_window": {"used_percentage": ctx},
    }


def test_tester_done_starts_implementer(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    done(rt)
    supervise(rt, current(rt))
    c = current(rt)
    assert c.phase == "impl" and c.tester_sha == "sha-tests"
    assert rt.sessions.killed == ["NF-1-test"] and rt.sessions.started[0][0] == "NF-1"
    assert not (state_dir(rt.cfg, "NF-1") / "done.json").exists()
    supervise(rt, c)
    assert len(rt.sessions.started) == 1
    assert ledger.remembered_tests(rt.con, "NF-1") == ("sha-base", "sha-tests")


def test_failed_start_puts_the_markers_back(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    done(rt, "tests written")
    d = state_dir(rt.cfg, "NF-1")
    (d / "handoff-requested").touch()
    before = (d / "done.json").read_text()
    working = rt.sessions.start

    def down(name, cwd, argv):
        raise subprocess.CalledProcessError(1, ["tmux"])

    rt.sessions.start = down
    with pytest.raises(subprocess.CalledProcessError):
        supervise(rt, current(rt))
    assert (d / "done.json").read_text() == before and (d / "handoff-requested").exists()
    c = current(rt)
    assert c.phase == "test" and c.restarts == 0
    rt.sessions.start = working
    supervise(rt, current(rt))
    assert current(rt).phase == "impl" and rt.sessions.started[0][0] == "NF-1"


def test_usage_snapshots_per_phase(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    write_status(state_dir(rt.cfg, "NF-1"), usage_status())
    done(rt)
    supervise(rt, current(rt))
    events = [(phase, event) for phase, event, _, _ in ledger.usage_for(rt.con, "NF-1")]
    assert events == [("test", "end"), ("impl", "start")]


def test_tester_without_commits_blocks(rt):
    add(rt)
    rt.git_head = lambda path: "sha-base"
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).state == "blocked"
    assert rt.notifier.sent[0].cause == "NF-1:no-tests"


def test_implementer_done_publishes(rt):
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt, "Implemented.")
    supervise(rt, current(rt))
    assert current(rt).state == "done"
    assert "https://pr/1" in rt.linear.comments[0]


def test_check_failure_gets_one_fix_then_blocks(rt):
    rt.publish_fn = lambda c, t, s: PublishResult("check_failed", "E1 boom")
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    c = current(rt)
    assert c.phase == "fix" and c.fix_attempts == 1
    assert any("E1 boom" in arg for arg in rt.sessions.started[-1][1])
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).state == "blocked"
    assert "linear-agents release --keep-worktree NF-1" in rt.notifier.sent[-1].action


def test_publish_that_keeps_failing_blocks(rt):
    def rejected(c, t, s):
        raise subprocess.CalledProcessError(1, ["git", "push"], stderr="rejected: non-fast-forward")

    rt.publish_fn = rejected
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    for _ in range(2):
        supervise(rt, current(rt))
        assert current(rt).state == "running" and rt.notifier.sent == []
    supervise(rt, current(rt))
    assert current(rt).state == "blocked"
    [esc] = rt.notifier.sent
    assert esc.cause == "NF-1:publish-failed" and "non-fast-forward" in esc.summary
    assert "linear-agents release --keep-worktree NF-1" in esc.action


def test_publish_failures_count_only_in_a_row(rt):
    results = ["fail", "fail", PublishResult("check_failed", "E1 boom"), "fail", "fail", "fail"]

    def scripted(c, t, s):
        r = results.pop(0)
        if r == "fail":
            raise subprocess.CalledProcessError(1, ["git", "push"])
        return r

    rt.publish_fn = scripted
    add(rt, phase="impl", tester_sha="sha-tests")
    for _ in range(3):
        done(rt)
        supervise(rt, current(rt))
    assert current(rt).phase == "fix"
    for _ in range(2):
        done(rt)
        supervise(rt, current(rt))
        assert current(rt).state == "running" and rt.notifier.sent == []
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).state == "blocked"


def test_fix_session_keeps_the_check_output_across_restarts(rt):
    rt.publish_fn = lambda c, t, s: PublishResult("check_failed", "E1 boom")
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    (state_dir(rt.cfg, "NF-1") / "handoff-ready").touch()
    supervise(rt, current(rt))
    assert any("E1 boom" in arg for arg in rt.sessions.started[-1][1])
    rt.sessions.alive_names.clear()
    supervise(rt, current(rt))
    assert len(rt.sessions.started) == 3
    assert any("E1 boom" in arg for arg in rt.sessions.started[-1][1])


def test_touched_tests_block_names_release(rt):
    rt.publish_fn = lambda c, t, s: PublishResult("tests_touched", "test_x.py")
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).state == "blocked"
    assert "linear-agents release" in rt.notifier.sent[-1].action


def test_unbuildable_launch_keeps_the_markers(rt):
    add(rt, settings_json="{not json")
    rt.sessions.alive_names.add("NF-1-test")
    done(rt)
    with pytest.raises(ValueError):
        supervise(rt, current(rt))
    assert (state_dir(rt.cfg, "NF-1") / "done.json").exists()
    assert current(rt).phase == "test"


def test_dead_session_restarts_once_then_blocks(rt):
    add(rt)
    supervise(rt, current(rt))
    assert current(rt).restarts == 1
    rt.sessions.alive_names.clear()
    supervise(rt, current(rt))
    assert current(rt).state == "blocked"
    assert rt.notifier.sent[-1].waiting is True


def test_launch_fails_without_agent_token(rt):
    add(rt)
    rt.cfg.agent_gh_token_file.write_text("")
    with pytest.raises(LaunchError):
        supervise(rt, current(rt))
    assert rt.sessions.started == [] and current(rt).restarts == 0


def test_linear_error_in_block_retries_next_tick(rt):
    add(rt)
    rt.git_head = lambda path: "sha-base"
    done(rt)
    calls = []

    def flaky(esc, issue, team_id):
        calls.append(esc)
        if len(calls) == 1:
            raise LinearError("timeout")

    rt.notifier.send = flaky
    with pytest.raises(LinearError):
        supervise(rt, current(rt))
    assert current(rt).state == "running"
    supervise(rt, current(rt))
    assert current(rt).state == "blocked" and len(calls) == 2


def test_wall_clock(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    rt.now = lambda: 100.0 + 3601
    supervise(rt, current(rt))
    assert current(rt).state == "timed_out"
    [(_, key, line)] = ledger.pending_digest(rt.con)
    assert key == "NF-1" and "linear-agents release NF-1" in line


def test_high_context_requests_handoff_once(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    write_status(state_dir(rt.cfg, "NF-1"), usage_status(ctx=70))
    supervise(rt, current(rt))
    supervise(rt, current(rt))
    assert len(rt.sessions.sent) == 1 and "handoff" in rt.sessions.sent[0][1]


def test_handoff_ready_restarts_same_phase_with_a_clean_slate(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    d = state_dir(rt.cfg, "NF-1")
    write_status(d, usage_status(ctx=70))
    (d / "handoff-ready").touch()
    (d / "handoff-requested").touch()
    supervise(rt, current(rt))
    assert rt.sessions.killed == ["NF-1-test"] and rt.sessions.started[0][0] == "NF-1-test"
    for name in ("handoff-ready", "handoff-requested", "status.json"):
        assert not (d / name).exists()
    supervise(rt, current(rt))
    assert rt.sessions.sent == [], "the old session's 70% must not ask the new one to hand off"
