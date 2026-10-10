import json
import subprocess
from dataclasses import replace

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
    pr_comments: list = []
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
        review_diff=lambda claim: "diff --git a/x b/x",
        comment_pr=lambda claim, url, body: pr_comments.append((url, body)),
    )
    runtime.pr_comments = pr_comments  # type: ignore[attr-defined]
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


def test_implementer_done_publishes_and_starts_the_review(rt):
    add(rt, phase="impl", tester_sha="sha-tests")
    rt.sessions.alive_names.add("NF-1")
    done(rt, "Implemented.")
    supervise(rt, current(rt))
    c = current(rt)
    assert c.state == "running" and c.phase == "review" and c.restarts == 0
    assert "https://pr/1" in rt.linear.comments[0]
    name, argv = rt.sessions.started[-1]
    assert name == "NF-1-review" and "NF-1-review" in argv
    assert argv[argv.index("--model") + 1] == "opus"
    d = state_dir(rt.cfg, "NF-1")
    assert (d / "pr.diff").read_text().startswith("diff --git")
    assert "https://pr/1" in (d / "review-input.md").read_text()
    assert "desc" in (d / "review-input.md").read_text()
    assert not (d / "done.json").exists()


def test_review_model_comes_from_config(rt):
    rt.cfg = replace(rt.cfg, review_model="sonnet")
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    argv = rt.sessions.started[-1][1]
    assert argv[argv.index("--model") + 1] == "sonnet"


def review_session(rt, text="## Minor\n- a.py:1 nit\n\nVerdict: approve\n"):
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    d = state_dir(rt.cfg, "NF-1")
    if text is not None:
        (d / "review.md").write_text(text)
    return d


def test_review_done_posts_the_comment_and_finishes_the_claim(rt):
    d = review_session(rt)
    done(rt, "review")
    supervise(rt, current(rt))
    assert rt.sessions.killed[-1] == "NF-1-review"
    [(url, body)] = rt.pr_comments
    assert url == "https://pr/1" and body.startswith("🤖 linear-agents review")
    assert "a.py:1 nit" in body
    assert rt.linear.comments[-1] == "Review: approve — https://pr/1"
    assert current(rt).state == "done"
    assert not rt.notifier.sent and (d / "review.md").exists()


def test_review_verdict_changes_requested(rt):
    review_session(rt, "## Critical\n- a.py:2 wrong\n\nVerdict: changes requested\n")
    done(rt, "review")
    supervise(rt, current(rt))
    assert rt.linear.comments[-1] == "Review: changes requested — https://pr/1"


def test_review_without_a_verdict_line_says_so(rt):
    review_session(rt, "looks fine")
    done(rt, "review")
    supervise(rt, current(rt))
    assert rt.linear.comments[-1].startswith("Review: no verdict")


def test_review_comment_is_retried_then_dropped_without_blocking(rt):
    def broken(claim, url, body):
        raise subprocess.CalledProcessError(1, ["gh"], stderr="boom")

    rt.comment_pr = broken
    review_session(rt)
    for _ in range(2):
        done(rt, "review")
        supervise(rt, current(rt))
        assert current(rt).state == "running"
    done(rt, "review")
    supervise(rt, current(rt))
    assert current(rt).state == "done" and rt.notifier.sent == []
    _, key, line = ledger.pending_digest(rt.con)[-1]
    assert key == "NF-1" and "review skipped" in line and "https://pr/1" in line


def test_linear_failure_after_the_comment_goes_to_the_digest(rt):
    def down(issue_id, text):
        if text.startswith("Review:"):
            raise LinearError("timeout")

    review_session(rt)
    rt.linear.comment = down
    done(rt, "review")
    supervise(rt, current(rt))
    assert current(rt).state == "done" and len(rt.pr_comments) == 1
    assert any("Review: approve" in line for _, _, line in ledger.pending_digest(rt.con))


def test_review_session_that_dies_twice_is_skipped_quietly(rt):
    review_session(rt, text=None)
    rt.sessions.alive_names.clear()
    supervise(rt, current(rt))
    assert current(rt).restarts == 1 and current(rt).phase == "review"
    rt.sessions.alive_names.clear()
    supervise(rt, current(rt))
    assert current(rt).state == "done" and rt.notifier.sent == [] and rt.pr_comments == []
    assert "review skipped" in ledger.pending_digest(rt.con)[-1][2]


def test_review_past_its_wall_clock_is_skipped_quietly(rt):
    review_session(rt, text=None)
    rt.now = lambda: 100.0 + 3601
    supervise(rt, current(rt))
    assert rt.sessions.killed[-1] == "NF-1-review"
    assert current(rt).state == "done" and rt.notifier.sent == [] and rt.pr_comments == []
    assert "wall-clock" in ledger.pending_digest(rt.con)[-1][2]


def test_review_that_cannot_start_does_not_block_the_claim(rt):
    def nodiff(claim):
        raise subprocess.CalledProcessError(128, ["git", "diff"])

    rt.review_diff = nodiff
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).state == "done" and rt.notifier.sent == []
    assert "review not started" in ledger.pending_digest(rt.con)[-1][2]


def test_no_review_when_publish_fails(rt):
    rt.publish_fn = lambda c, t, s: PublishResult("check_failed", "E1 boom")
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).phase == "fix"
    assert all(name != "NF-1-review" for name, _ in rt.sessions.started)


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


def test_launch_trusts_the_worktree_and_its_checkout(rt):
    cj = rt.cfg.claude_json
    cj.write_text(json.dumps({"userID": "u", "projects": {"/other": {"allowedTools": ["x"]}}}))
    cj.chmod(0o600)
    add(rt)
    supervise(rt, current(rt))
    assert rt.sessions.started[0][0] == "NF-1-test"
    data = json.loads(cj.read_text())
    checkout = str(rt.cfg.repos["backend"].path)
    assert data["projects"]["/wt/NF-1"]["hasTrustDialogAccepted"] is True
    assert data["projects"][checkout]["hasTrustDialogAccepted"] is True
    assert data["userID"] == "u" and data["projects"]["/other"] == {"allowedTools": ["x"]}
    assert cj.stat().st_mode & 0o777 == 0o600


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


def test_comment_already_posted_is_not_posted_again(rt):
    review_session(rt)
    rt.linear.comment = lambda issue_id, text: (_ for _ in ()).throw(LinearError("x"))
    done(rt, "review")
    supervise(rt, current(rt))
    assert len(rt.pr_comments) == 1
    # A tick that died after the post but before finishing the claim.
    ledger.update_claim(rt.con, "NF-1", state="running")
    ledger.meta_set(rt.con, "review_posted:NF-1", "1")
    done(rt, "review")
    supervise(rt, current(rt))
    assert len(rt.pr_comments) == 1 and current(rt).state == "done"
    assert not ledger.meta_get(rt.con, "review_posted:NF-1")


@pytest.mark.parametrize("pct", ["five_hour", "seven_day"])
def test_review_is_skipped_when_usage_is_over_the_limit(rt, pct):
    window = {"used_percentage": 20.0, "resets_at": 4_000_000_000}
    over = {**window, "used_percentage": 95.0}
    status = {"rate_limits": {"five_hour": window, "seven_day": window, "context_window": {}}}
    status["rate_limits"][pct] = over
    add(rt, phase="impl", tester_sha="sha-tests")
    write_status(state_dir(rt.cfg, "NF-1"), status)
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).state == "done" and rt.notifier.sent == []
    assert all(name != "NF-1-review" for name, _ in rt.sessions.started)
    assert "usage over the limit" in ledger.pending_digest(rt.con)[-1][2]


@pytest.mark.parametrize(
    "line",
    ["**Verdict:** approve", "Verdict: **approve**", "## Verdict: Approve", "_Verdict_: approve"],
)
def test_verdict_tolerates_markdown_emphasis(rt, line):
    review_session(rt, f"## Minor\n- a.py:1 nit\n\n{line}\n")
    done(rt, "review")
    supervise(rt, current(rt))
    assert rt.linear.comments[-1] == "Review: approve — https://pr/1"


def test_review_is_bounded_by_review_wall_clock_s(rt):
    rt.cfg = replace(rt.cfg, review_wall_clock_s=60)
    review_session(rt, text=None)
    rt.now = lambda: 100.0 + 61
    supervise(rt, current(rt))
    assert current(rt).state == "done" and "wall-clock" in ledger.pending_digest(rt.con)[-1][2]
