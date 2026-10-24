import json
import os
import subprocess
import time
from dataclasses import replace

import pytest

from flocc import ledger
from flocc.agentblock import IssueSettings, settings_to_json
from flocc.context import state_dir
from flocc.gate import record_session, record_stop, run_gate, run_id
from flocc.grants import Grants, add_comment, for_phase
from flocc.ledger import Claim
from flocc.linear import LinearError
from flocc.publisher import BranchMoved, PublishResult
from flocc.runtime import Runtime
from flocc.supervisor import QUESTION_WAIT_S, LaunchError, supervise
from flocc.usage import write_status
from tests.fakes import FakeLinear, FakeNotifier, FakeSessions
from tests.test_ledger import make

IDLE_S = 1800.0


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
        exe="/opt/flocc",
        claude_exe="/opt/claude",
        config_path=tmp_path / "c.toml",
        publish_fn=lambda claim, title, summary, **kw: PublishResult("opened", url="https://pr/1"),
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


def test_review_input_lists_the_amended_tests(rt):
    add(rt, phase="impl", tester_sha="sha-tests")
    d = state_dir(rt.cfg, "NF-1")
    amended = {"t/test_a.py": {"reason": "asserts the old API"}}
    (d / "tests-amended.json").write_text(json.dumps(amended))
    done(rt, "Implemented.")
    supervise(rt, current(rt))
    text = (d / "review-input.md").read_text()
    assert "## Amended tests" in text and "- `t/test_a.py`: asserts the old API" in text


def test_review_input_has_no_amended_section_without_amendments(rt):
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt, "Implemented.")
    supervise(rt, current(rt))
    assert "Amended tests" not in (state_dir(rt.cfg, "NF-1") / "review-input.md").read_text()


def test_review_model_comes_from_config(rt):
    rt.cfg = replace(rt.cfg, review_model="sonnet")
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    argv = rt.sessions.started[-1][1]
    assert argv[argv.index("--model") + 1] == "sonnet"


def _effort(argv):
    return argv[argv.index("--effort") + 1]


def test_tester_launches_at_high_effort(rt):
    add(rt)
    supervise(rt, current(rt))
    name, argv = rt.sessions.started[0]
    assert name == "NF-1-test" and _effort(argv) == "high"


def test_implementer_launches_at_medium_effort(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    done(rt)
    supervise(rt, current(rt))
    name, argv = rt.sessions.started[0]
    assert name == "NF-1" and _effort(argv) == "medium"


def test_reviewer_launches_at_medium_effort(rt):
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    name, argv = rt.sessions.started[-1]
    assert name == "NF-1-review" and _effort(argv) == "medium"


def test_agentblock_effort_overrides_phase_default(rt):
    add(rt, settings_json=settings_to_json(IssueSettings(effort="max")))
    supervise(rt, current(rt))
    assert _effort(rt.sessions.started[0][1]) == "max"


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
    assert url == "https://pr/1" and body.startswith("🤖 flocc review")
    assert "a.py:1 nit" in body
    assert rt.linear.comments[-1] == "Review: approve — https://pr/1"
    assert current(rt).state == "done"
    assert not rt.notifier.sent and (d / "review.md").exists()


CHANGES = "## Critical\n- a.py:2 wrong\n\nVerdict: changes requested\n"


def test_second_changes_requested_review_ends_the_claim(rt):
    review_session(rt, CHANGES)
    ledger.meta_set(rt.con, "review_fix:NF-1", "1")
    done(rt, "review")
    supervise(rt, current(rt))
    assert rt.linear.comments[-1] == "Review: changes requested — https://pr/1"
    assert current(rt).state == "done"


def test_changes_requested_starts_one_fix_round_on_the_review(rt):
    review_session(rt, CHANGES)
    ledger.update_claim(rt.con, "NF-1", fix_attempts=2)
    done(rt, "review")
    supervise(rt, current(rt))
    c = current(rt)
    assert (c.state, c.phase, c.fix_attempts, c.restarts) == ("running", "fix", 0, 0)
    name, argv = rt.sessions.started[-1]
    assert name == "NF-1" and any("a.py:2 wrong" in a and "Needs the owner" in a for a in argv)
    assert "fix round" in rt.linear.comments[-1]
    assert "Review: changes requested — https://pr/1" in rt.linear.comments[-1]
    assert ledger.meta_get(rt.con, "review_fix:NF-1") == "1"
    assert not (state_dir(rt.cfg, "NF-1") / "done.json").exists()
    assert len(rt.pr_comments) == 1


def test_fix_round_ends_in_a_second_review_posted_again(rt):
    review_session(rt, CHANGES)
    done(rt, "review")
    supervise(rt, current(rt))
    rt.sessions.alive_names.add("NF-1")
    done(rt, "Fixed a.\n\nNeeds the owner: run AC-8 against the real server.")
    supervise(rt, current(rt))
    assert current(rt).phase == "review"
    d = state_dir(rt.cfg, "NF-1")
    assert "Your earlier review" in (d / "review-input.md").read_text()
    assert "a.py:2 wrong" in (d / "review-input.md").read_text()
    (d / "review.md").write_text(CHANGES)
    done(rt, "review")
    supervise(rt, current(rt))
    assert len(rt.pr_comments) == 2
    assert current(rt).state == "done"
    last = rt.linear.comments[-1]
    assert last.startswith("Review: changes requested — https://pr/1")
    assert "Needs the owner: run AC-8 against the real server." in last


OWNER = "Fixed a.\n\nNeeds the owner: run AC-8 against the real server."


def after_fix(rt, summary=OWNER):
    review_session(rt, CHANGES)
    done(rt, "review")
    supervise(rt, current(rt))
    rt.sessions.alive_names.add("NF-1")
    done(rt, summary)
    supervise(rt, current(rt))
    return state_dir(rt.cfg, "NF-1")


def test_owner_items_reach_the_comment_of_an_approving_second_review(rt):
    d = after_fix(rt)
    (d / "review.md").write_text("Verdict: approve\n")
    done(rt, "review")
    supervise(rt, current(rt))
    last = rt.linear.comments[-1]
    assert last.startswith("Review: approve — https://pr/1")
    assert "Needs the owner: run AC-8 against the real server." in last


def test_owner_items_reach_the_comment_when_the_second_review_is_skipped(rt):
    over = {"used_percentage": 95.0, "resets_at": 4_000_000_000}

    def publish(c, t, s, **kw):
        if kw:
            write_status(state_dir(rt.cfg, "NF-1"), {"rate_limits": {"five_hour": over}})
        return PublishResult("opened", url="https://pr/1", existing=bool(kw))

    rt.publish_fn = publish
    after_fix(rt)
    assert current(rt).state == "done"
    assert "Needs the owner: run AC-8" in rt.linear.comments[-1]


def test_owner_items_reach_the_comment_when_the_second_review_dies(rt):
    after_fix(rt)
    for _ in range(2):
        rt.sessions.alive_names.clear()
        supervise(rt, current(rt))
    assert current(rt).state == "done"
    assert "Needs the owner: run AC-8" in rt.linear.comments[-1]


def test_a_sentence_mentioning_the_owner_is_not_an_owner_item(rt):
    d = after_fix(rt, "Fixed a. Nothing here needs the owner today.")
    assert not (d / "owner-items.md").exists()


def test_a_bulleted_owner_heading_is_an_owner_item(rt):
    d = after_fix(rt, "Fixed a.\n\n## Needs the owner\n- run AC-8")
    assert "run AC-8" in (d / "owner-items.md").read_text()


def test_the_fix_push_comments_once_more_without_a_digest_line(rt):
    rt.publish_fn = lambda c, t, s, **kw: PublishResult(
        "opened", url="https://pr/1", existing=bool(ledger.meta_get(rt.con, "review_fix:NF-1"))
    )
    review_session(rt, CHANGES)
    done(rt, "review")
    supervise(rt, current(rt))
    rt.sessions.alive_names.add("NF-1")
    done(rt, "Fixed.")
    supervise(rt, current(rt))
    assert "Review fix pushed to https://pr/1; reviewing again" in rt.linear.comments
    assert "PR for review: https://pr/1" not in rt.linear.comments[-1]
    lines = [line for _, _, line in ledger.pending_digest(rt.con)]
    assert sum("PR ready for review" in line for line in lines) == 1


def test_a_pr_closed_during_the_fix_ends_the_claim(rt):
    seen = []

    def publish(c, t, s, **kw):
        seen.append(kw)
        return PublishResult("pr_closed") if kw else PublishResult("opened", url="https://pr/1")

    rt.publish_fn = publish
    review_session(rt, CHANGES)
    done(rt, "review")
    supervise(rt, current(rt))
    rt.sessions.alive_names.add("NF-1")
    done(rt, OWNER)
    supervise(rt, current(rt))
    assert seen == [{}, {"reuse_only": True}]
    assert current(rt).state == "done"
    last = rt.linear.comments[-1]
    assert "closed or merged during the review fix; nothing more was published" in last
    assert "Needs the owner: run AC-8" in last
    assert all(name != "NF-1-review" for name, _ in rt.sessions.started[1:])


def test_a_tick_that_dies_after_the_fix_launch_does_not_post_or_relaunch_a_review(rt):
    review_session(rt, CHANGES)

    def dies(name, cwd, argv):
        raise KeyboardInterrupt

    rt.sessions.start = dies
    done(rt, "review")
    with pytest.raises(KeyboardInterrupt):
        supervise(rt, current(rt))
    assert current(rt).phase == "fix" and len(rt.pr_comments) == 1
    assert ledger.meta_get(rt.con, "review_posted:NF-1:1") == "1"


def test_fix_launch_failure_restores_the_review_phase_and_ends_done(rt):
    review_session(rt, CHANGES)
    rt.cfg.agent_gh_token_file.write_text("")
    done(rt, "review")
    supervise(rt, current(rt))
    assert (current(rt).state, current(rt).phase) == ("done", "review")


def test_changes_requested_after_a_fix_round_without_owner_items_adds_nothing(rt):
    review_session(rt, CHANGES)
    done(rt, "review")
    supervise(rt, current(rt))
    rt.sessions.alive_names.add("NF-1")
    done(rt, "Fixed everything.")
    supervise(rt, current(rt))
    (state_dir(rt.cfg, "NF-1") / "review.md").write_text(CHANGES)
    done(rt, "review")
    supervise(rt, current(rt))
    assert rt.linear.comments[-1] == "Review: changes requested — https://pr/1"


def test_no_fix_round_when_usage_is_over_the_limit(rt):
    d = review_session(rt, CHANGES)
    over = {"used_percentage": 95.0, "resets_at": 4_000_000_000}
    write_status(d, {"rate_limits": {"five_hour": over, "seven_day": over}})
    done(rt, "review")
    supervise(rt, current(rt))
    assert current(rt).state == "done" and current(rt).phase == "review"
    assert rt.linear.comments[-1] == "Review: changes requested — https://pr/1"


def test_fix_round_that_cannot_start_ends_the_claim(rt):
    review_session(rt, CHANGES)
    rt.cfg.agent_gh_token_file.write_text("")
    done(rt, "review")
    supervise(rt, current(rt))
    assert current(rt).state == "done"
    assert rt.linear.comments[-1].startswith("Review: changes requested")


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


def test_publish_notes_commits_without_the_trailer_in_the_digest(rt):
    rt.publish_fn = lambda c, t, s: PublishResult("opened", url="u", untrailed=("abc1234",))
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt, "Implemented.")
    supervise(rt, current(rt))
    lines = [line for _, _, line in ledger.pending_digest(rt.con)]
    assert any("abc1234" in line and "trailer" in line for line in lines)


def test_fix_session_is_pointed_at_the_full_check_output(rt):
    rt.publish_fn = lambda c, t, s: PublishResult("check_failed", "E1 boom", log="all of it")
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    d = state_dir(rt.cfg, "NF-1")
    assert (d / "check-output.log").read_text() == "all of it"
    assert str(d / "check-output.log") in (d / "check-failure.md").read_text()


def test_check_failure_gets_two_fixes_then_blocks(rt):
    failures = iter(["E1 boom", "E2 bang", "E3 crash"])
    rt.publish_fn = lambda c, t, s: PublishResult("check_failed", next(failures))
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    c = current(rt)
    assert c.phase == "fix" and c.fix_attempts == 1
    assert any("E1 boom" in arg for arg in rt.sessions.started[-1][1])
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).phase == "fix" and current(rt).fix_attempts == 2
    extra = next(a for a in rt.sessions.started[-1][1] if "E2 bang" in a)
    assert "An earlier fix round failed with:" in extra and "E1 boom" in extra
    assert extra.index("E1 boom") < extra.index("E2 bang")
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).state == "blocked"
    assert "The check still fails after 2 fix rounds" in rt.notifier.sent[-1].summary
    assert "flocc release --keep-worktree NF-1" in rt.notifier.sent[-1].action


def test_the_same_check_failure_twice_blocks_without_another_round(rt):
    rt.publish_fn = lambda c, t, s: PublishResult("check_failed", "E1 boom")
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).phase == "fix"
    started = len(rt.sessions.started)
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).state == "blocked" and len(rt.sessions.started) == started
    summary = rt.notifier.sent[-1].summary
    assert summary == (
        "The check failed the same way twice; the cause is likely outside the code:\nE1 boom"
    )


@pytest.mark.parametrize(
    ("first", "second"),
    [
        ("FAILED t.py::test_a - 1 failed in 37.36s", "FAILED t.py::test_a - 1 failed in 41.02s"),
        ("--- FAIL: TestA (0.123s)\n\tx", "--- FAIL: TestA (0.456s)\n   x"),
    ],
)
def test_failures_differing_only_in_durations_and_spacing_count_as_the_same(rt, first, second):
    failures = iter([first, second])
    rt.publish_fn = lambda c, t, s: PublishResult("check_failed", next(failures))
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).state == "blocked"
    assert "the same way twice" in rt.notifier.sent[-1].summary


@pytest.mark.parametrize(
    ("first", "second"),
    [
        ("E1 boom", "E2 bang"),
        ("FAILED t.py::test_case_1 - got 500", "FAILED t.py::test_case_2 - got 404"),
    ],
)
def test_a_different_check_failure_gets_its_second_round(rt, first, second):
    failures = iter([first, second])
    rt.publish_fn = lambda c, t, s: PublishResult("check_failed", next(failures))
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).phase == "fix" and current(rt).fix_attempts == 2


def test_one_check_fix_round_keeps_the_old_behaviour(rt):
    rt.cfg = replace(rt.cfg, check_fix_rounds=1)
    failures = iter(["E1 boom", "E2 bang"])
    rt.publish_fn = lambda c, t, s: PublishResult("check_failed", next(failures))
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).phase == "fix"
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).state == "blocked"
    assert "The check still fails after 1 fix round:" in rt.notifier.sent[-1].summary


def test_publish_that_keeps_failing_blocks(rt):
    def rejected(c, t, s):
        raise subprocess.CalledProcessError(
            128, ["git", "push"], stderr="fatal: Authentication failed for 'https://github.com/o/r'"
        )

    rt.publish_fn = rejected
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    for _ in range(2):
        supervise(rt, current(rt))
        assert current(rt).state == "running" and rt.notifier.sent == []
    supervise(rt, current(rt))
    assert current(rt).state == "blocked"
    [esc] = rt.notifier.sent
    assert esc.cause == "NF-1:publish-failed" and "Authentication failed" in esc.summary
    assert "flocc release --keep-worktree NF-1" in esc.action


def test_a_branch_that_moved_on_github_blocks_at_once_and_says_so(rt):
    def moved(c, t, s):
        stderr = " ! [rejected]        agent/nf-1 -> agent/nf-1 (fetch first)\n"
        raise BranchMoved(1, ["git", "push"], "", stderr)

    rt.publish_fn = moved
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).state == "blocked"
    [esc] = rt.notifier.sent
    assert esc.cause == "NF-1:branch-moved"
    assert "GitHub's copy of the branch has commits this one lacks" in esc.summary
    assert "token" not in esc.action
    assert "flocc release --keep-worktree NF-1" in esc.action


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
    assert "flocc release" in rt.notifier.sent[-1].action
    assert "--accept-tests NF-1" in rt.notifier.sent[-1].action


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
    assert key == "NF-1" and "flocc release NF-1" in line


def test_an_open_question_holds_off_the_wall_clock(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    rt.now = lambda: 100.0 + 3601
    (state_dir(rt.cfg, "NF-1") / "asking").write_text(
        json.dumps({"at": 100.0 + 3600, "question": "Q?"})
    )
    supervise(rt, current(rt))
    assert current(rt).state == "running" and "NF-1-test" not in rt.sessions.killed
    (state_dir(rt.cfg, "NF-1") / "asking").unlink()
    supervise(rt, current(rt))
    assert current(rt).state == "timed_out"


def test_the_time_a_question_was_open_is_credited_to_the_wall_clock(rt):
    add(rt, wall_clock_s=3 * 3600)
    rt.sessions.alive_names.add("NF-1-test")
    d = state_dir(rt.cfg, "NF-1")
    (d / "asked-seconds").write_text("10800")
    rt.now = lambda: 100.0 + 4 * 3600
    supervise(rt, current(rt))
    assert current(rt).state == "running"
    rt.now = lambda: 100.0 + 6 * 3600 + 1
    supervise(rt, current(rt))
    assert current(rt).state == "timed_out"


def test_a_question_left_open_forever_times_out_after_the_wait(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    d = state_dir(rt.cfg, "NF-1")
    asked = 100.0 + 3500
    (d / "asking").write_text(json.dumps({"at": asked, "question": "Q?"}))
    # Asked just before the limit: the page goes out after QUESTION_WAIT_S, and the owner still
    # gets as long again to answer it.
    rt.now = lambda: asked + QUESTION_WAIT_S + 600
    supervise(rt, current(rt))
    assert current(rt).state == "running"
    rt.now = lambda: asked + 2 * QUESTION_WAIT_S + 1
    supervise(rt, current(rt))
    assert current(rt).state == "timed_out"


def test_a_torn_question_marker_does_not_hold_off_the_wall_clock(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    (state_dir(rt.cfg, "NF-1") / "asking").write_text("{torn")
    rt.now = lambda: 100.0 + 3601
    supervise(rt, current(rt))
    assert current(rt).state == "timed_out"


def test_an_open_question_never_holds_off_the_review_limit(rt):
    rt.cfg = replace(rt.cfg, review_wall_clock_s=60)
    review_session(rt, text=None)
    (state_dir(rt.cfg, "NF-1") / "asking").write_text(json.dumps({"at": 100.0, "question": "Q?"}))
    rt.now = lambda: 100.0 + 61
    supervise(rt, current(rt))
    assert current(rt).state == "done"


def test_high_context_requests_handoff_once(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    (state_dir(rt.cfg, "NF-1") / "compacted").touch()
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
    ledger.meta_set(rt.con, "review_posted:NF-1:1", "1")
    done(rt, "review")
    supervise(rt, current(rt))
    assert len(rt.pr_comments) == 1 and current(rt).state == "done"


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


def env_of(argv):
    end = argv.index("/bin/sh")
    return dict(x.split("=", 1) for x in argv[2:end])


def test_phase_sessions_share_one_run_id_across_relaunches(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    done(rt)
    supervise(rt, current(rt))
    started = current(rt).started_at
    env = env_of(rt.sessions.started[-1][1])
    assert env["FLOCC_RUN"] == f"impl@{started}"
    assert env["CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH"] == "1"
    assert env["CLAUDE_CODE_DISABLE_EXPLORE_PLAN_AGENTS"] == "1"
    rt.sessions.alive_names.clear()
    supervise(rt, current(rt))  # a crash relaunch of the same phase
    assert env_of(rt.sessions.started[-1][1])["FLOCC_RUN"] == f"impl@{started}"


def test_only_implementer_and_fix_sessions_use_remote_control(rt):
    add(rt)
    supervise(rt, current(rt))  # tester relaunch
    assert "--remote-control" not in rt.sessions.started[-1][1]
    rt.sessions.alive_names.add("NF-1-test")
    done(rt)
    supervise(rt, current(rt))  # implementer
    assert "--remote-control" in rt.sessions.started[-1][1]


def test_launch_records_block_grants_without_beating_comment_grants(rt):
    add(rt, settings_json=settings_to_json(IssueSettings(allow="launches 12")))
    path = state_dir(rt.cfg, "NF-1") / "grants.json"
    from_comment = Grants(launches=20)
    add_comment(path, from_comment, "impl")
    rt.sessions.alive_names.add("NF-1-test")
    done(rt)
    supervise(rt, current(rt))
    assert for_phase(path, "impl").launches == 20


def test_unlisted_installed_plugins_are_disabled_at_launch(rt):
    rt.cfg.agent_plugins_file.write_text("docket@NovusEdge\n")
    rt.cfg.installed_plugins_file.write_text(
        json.dumps({"plugins": {"docket@NovusEdge": [], "glowup@glowup": []}})
    )
    add(rt)
    supervise(rt, current(rt))  # not alive: relaunch
    argv = rt.sessions.started[-1][1]
    settings = json.loads(argv[argv.index("--settings") + 1])
    assert settings["enabledPlugins"] == {"glowup@glowup": False}


def test_no_handoff_before_the_first_compaction(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    d = state_dir(rt.cfg, "NF-1")
    write_status(d, usage_status(ctx=70))
    supervise(rt, current(rt))
    assert rt.sessions.sent == []
    (d / "compacted").touch()
    supervise(rt, current(rt))
    assert len(rt.sessions.sent) == 1 and "handoff" in rt.sessions.sent[0][1]


def test_relaunch_clears_compaction_and_question_markers(rt):
    add(rt)
    d = state_dir(rt.cfg, "NF-1")
    names = ("compacted", "asking", "asking-escalated", "idle", "idle-escalated")
    for name in names:
        (d / name).write_text("{}")
    supervise(rt, current(rt))  # not alive: relaunch
    for name in names:
        assert not (d / name).exists()


def idle_session(rt, at=100.0 - IDLE_S, message="please run it for me"):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    d = state_dir(rt.cfg, "NF-1")
    (d / "idle").write_text(json.dumps({"at": at, "message": message}))
    return d


def test_an_idle_session_is_escalated_once_per_idle_period(rt):
    d = idle_session(rt)
    supervise(rt, current(rt))
    supervise(rt, current(rt))
    (esc,) = rt.notifier.sent
    assert esc.tier == 2 and esc.waiting and esc.issue_key == "NF-1"
    assert esc.summary == (
        "NF-1-test has been idle for 30 min without finishing: please run it for me"
    )
    assert "release --keep-worktree NF-1" in esc.action
    (d / "idle").write_text(json.dumps({"at": 50.0 - IDLE_S, "message": "again"}))
    supervise(rt, current(rt))
    assert len(rt.notifier.sent) == 2 and "again" in rt.notifier.sent[1].summary


def test_an_idle_session_with_no_message_says_so(rt):
    idle_session(rt, message="")
    supervise(rt, current(rt))
    assert rt.notifier.sent[0].summary.endswith("without finishing: no last message")


def test_a_session_idle_for_less_than_the_threshold_is_left_alone(rt):
    idle_session(rt, at=100.0 - IDLE_S + 1)
    supervise(rt, current(rt))
    assert rt.notifier.sent == []


def test_a_status_rewrite_does_not_postpone_the_escalation(rt):
    # A cache expiry or a rate-limit reset rewrites status.json while the session sits idle.
    d = idle_session(rt)
    write_status(d, usage_status())
    os.utime(d / "status.json", (time.time(), time.time()))
    supervise(rt, current(rt))
    assert len(rt.notifier.sent) == 1


def agent_call(name):
    return {"tool_name": "Agent", "tool_input": {"subagent_type": f"flocc:{name}"}}


def test_a_session_waiting_on_background_agents_is_left_alone(rt):
    d = idle_session(rt)
    run = run_id("test", current(rt).started_at)
    record_session(d, run, 0.0, "test")
    assert run_gate(agent_call("source-finder"), d, "test", run, 1.0) is None
    supervise(rt, current(rt))
    assert rt.notifier.sent == []
    record_stop(d, run, 2.0, "source-finder", "test")
    supervise(rt, current(rt))
    assert len(rt.notifier.sent) == 1


def test_a_session_that_already_escalated_and_waits_is_left_alone(rt):
    d = idle_session(rt)
    (d / "idle").write_text(
        json.dumps({"at": 100.0 - IDLE_S, "message": "m", "waiting_on_owner": True})
    )
    supervise(rt, current(rt))
    assert rt.notifier.sent == []


def test_an_idle_session_with_an_open_question_is_left_to_the_question_check(rt):
    d = idle_session(rt)
    (d / "asking").write_text(json.dumps({"at": 100.0, "question": "A or B?"}))
    supervise(rt, current(rt))
    assert rt.notifier.sent == []


@pytest.mark.parametrize("content", ["[]", '{"at": "x"}', "not json", "{}"])
def test_a_malformed_idle_file_is_ignored(rt, content):
    d = idle_session(rt)
    (d / "idle").write_text(content)
    supervise(rt, current(rt))
    assert rt.notifier.sent == []


DIALOG = """\
 Do you want to proceed?
 > 1. Yes
   2. No
NF-1 · ctx 8% · 5h 20% · wk 25%
"""


def stalled(rt, pane=DIALOG):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    rt.sessions.panes["NF-1-test"] = pane
    return state_dir(rt.cfg, "NF-1")


def at(rt, t):
    rt.now = lambda: t


def test_a_screen_unchanged_mid_turn_is_escalated_once_with_its_tail(rt):
    stalled(rt)
    supervise(rt, current(rt))
    at(rt, 100.0 + rt.cfg.stall_escalate_s)
    supervise(rt, current(rt))
    supervise(rt, current(rt))
    (esc,) = rt.notifier.sent
    assert esc.tier == 2 and esc.waiting and esc.cause == "NF-1:stalled"
    assert esc.summary.startswith("NF-1-test has shown the same screen for 20 min")
    assert esc.summary.endswith("```\n Do you want to proceed?\n > 1. Yes\n   2. No\n```")
    assert "flocc press NF-1 Down Enter" in esc.action


def test_a_changing_screen_restarts_the_stall_clock(rt):
    stalled(rt)
    supervise(rt, current(rt))
    at(rt, 100.0 + rt.cfg.stall_escalate_s)
    rt.sessions.panes["NF-1-test"] = "✻ Running… (20m 1s)\n"
    supervise(rt, current(rt))
    assert rt.notifier.sent == []


def test_a_status_line_rewrite_does_not_restart_the_stall_clock(rt):
    stalled(rt)
    supervise(rt, current(rt))
    rt.sessions.panes["NF-1-test"] = DIALOG.replace("5h 20%", "5h 0%")
    at(rt, 100.0 + rt.cfg.stall_escalate_s)
    supervise(rt, current(rt))
    assert len(rt.notifier.sent) == 1


@pytest.mark.parametrize("marker", ["idle", "asking"])
def test_an_ended_turn_or_open_question_is_not_a_stall(rt, marker):
    d = stalled(rt)
    (d / marker).write_text(json.dumps({"at": 100.0 + rt.cfg.stall_escalate_s, "message": "m"}))
    supervise(rt, current(rt))
    at(rt, 100.0 + rt.cfg.stall_escalate_s)
    supervise(rt, current(rt))
    assert rt.notifier.sent == []


def test_a_still_screen_while_background_agents_run_is_not_a_stall(rt):
    d = stalled(rt)
    run = run_id("test", current(rt).started_at)
    record_session(d, run, 0.0, "test")
    assert run_gate(agent_call("source-finder"), d, "test", run, 1.0) is None
    supervise(rt, current(rt))
    at(rt, 100.0 + rt.cfg.stall_escalate_s)
    supervise(rt, current(rt))
    assert rt.notifier.sent == []


def test_idle_with_background_tasks_is_busy(rt):
    d = idle_session(rt)
    (d / "background").write_text('["shell"]')
    supervise(rt, current(rt))
    assert rt.notifier.sent == []


def test_a_stale_background_marker_does_not_hide_a_stall(rt):
    d = stalled(rt)
    (d / "background").write_text('["shell"]')
    supervise(rt, current(rt))
    at(rt, 100.0 + rt.cfg.stall_escalate_s)
    supervise(rt, current(rt))
    assert len(rt.notifier.sent) == 1


def test_a_still_screen_at_a_usage_limit_is_not_a_stall(rt):
    d = stalled(rt)
    full = {"used_percentage": 100.0, "resets_at": 4_000_000_000}
    write_status(d, {"rate_limits": {"five_hour": full, "seven_day": full}})
    supervise(rt, current(rt))
    at(rt, 100.0 + rt.cfg.stall_escalate_s)
    supervise(rt, current(rt))
    assert rt.notifier.sent == []


def test_a_relaunch_forgets_the_last_screen(rt):
    d = stalled(rt)
    supervise(rt, current(rt))
    assert (d / "pane.json").exists()
    rt.sessions.alive_names.clear()
    supervise(rt, current(rt))
    assert not (d / "pane.json").exists()


def test_a_stale_question_is_escalated_once(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    d = state_dir(rt.cfg, "NF-1")
    (d / "asking").write_text(
        json.dumps({"at": 100.0 - QUESTION_WAIT_S - 1, "question": "A or B?"})
    )
    supervise(rt, current(rt))
    supervise(rt, current(rt))
    (esc,) = rt.notifier.sent
    assert esc.tier == 2 and "A or B?" in esc.summary


def test_no_handoff_is_requested_while_a_question_is_open(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    d = state_dir(rt.cfg, "NF-1")
    write_status(d, usage_status(ctx=70))
    (d / "compacted").touch()
    (d / "asking").write_text(json.dumps({"at": 100.0, "question": "A or B?"}))
    supervise(rt, current(rt))
    assert rt.sessions.sent == [] and not (d / "handoff-requested").exists()
    (d / "asking").unlink()
    supervise(rt, current(rt))
    assert len(rt.sessions.sent) == 1 and (d / "handoff-requested").exists()


def test_a_tester_question_escalation_points_at_the_pane(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    d = state_dir(rt.cfg, "NF-1")
    (d / "asking").write_text(
        json.dumps({"at": 100.0 - QUESTION_WAIT_S - 1, "question": "A or B?"})
    )
    supervise(rt, current(rt))
    (esc,) = rt.notifier.sent
    assert esc.action == (
        "Answer it in its pane on the host (tmux -L flocc attach -t NF-1-test);"
        " a reply on this issue is delivered after the question is answered."
    )


def test_an_implementer_question_escalation_points_at_the_app(rt):
    add(rt, phase="impl", tester_sha="sha-tests")
    rt.sessions.alive_names.add("NF-1")
    d = state_dir(rt.cfg, "NF-1")
    (d / "asking").write_text(
        json.dumps({"at": 100.0 - QUESTION_WAIT_S - 1, "question": "A or B?"})
    )
    supervise(rt, current(rt))
    (esc,) = rt.notifier.sent
    assert esc.action == (
        "Answer it in the Claude app (Remote Control session NF-1);"
        " a reply on this issue is delivered after the question is answered."
    )


def test_a_fresh_question_is_not_escalated(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    d = state_dir(rt.cfg, "NF-1")
    (d / "asking").write_text(json.dumps({"at": 100.0, "question": "A or B?"}))
    supervise(rt, current(rt))
    assert rt.notifier.sent == []


def settings_of(argv):
    return json.loads(argv[argv.index("--settings") + 1])


def test_launch_applies_block_grants_to_the_session(rt):
    add(rt, settings_json=settings_to_json(IssueSettings(allow="agent general-purpose")))
    rt.sessions.alive_names.add("NF-1-test")
    done(rt)
    supervise(rt, current(rt))
    deny = settings_of(rt.sessions.started[-1][1])["permissions"]["deny"]
    assert "Agent(general-purpose)" not in deny
    raw = json.loads((state_dir(rt.cfg, "NF-1") / "grants.json").read_text())
    assert raw["block"]["agents"] == ["general-purpose"]


def test_launch_tells_the_gate_which_grants_the_session_started_with(rt):
    add(rt, settings_json=settings_to_json(IssueSettings(allow="agent general-purpose")))
    path = state_dir(rt.cfg, "NF-1") / "grants.json"
    add_comment(path, Grants(workflows=("big-build",), agents=("code-mapper",)), "impl")
    rt.sessions.alive_names.add("NF-1-test")
    done(rt)
    supervise(rt, current(rt))
    env = env_of(rt.sessions.started[-1][1])
    assert env["FLOCC_LAUNCH_GRANTS"] == "agent:general-purpose,workflow:big-build"


def test_launch_without_flag_grants_sets_an_empty_value(rt):
    add(rt)
    supervise(rt, current(rt))
    assert env_of(rt.sessions.started[-1][1])["FLOCC_LAUNCH_GRANTS"] == ""


@pytest.mark.parametrize("content", ["[]", '{"at": "x"}', "not json", "{}"])
def test_a_malformed_question_file_is_ignored(rt, content):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    (state_dir(rt.cfg, "NF-1") / "asking").write_text(content)
    supervise(rt, current(rt))
    assert rt.notifier.sent == []


def test_review_and_fix_launches_carry_their_own_run_id_and_remote_control(rt):
    rt.publish_fn = lambda c, t, s: PublishResult("check_failed", "E1 boom")
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))  # check fails: fix
    c = current(rt)
    argv = rt.sessions.started[-1][1]
    assert c.phase == "fix" and env_of(argv)["FLOCC_RUN"] == f"fix@{c.started_at}"
    assert "--remote-control" in argv
    rt.publish_fn = lambda c, t, s: PublishResult("opened", url="https://pr/1")
    done(rt)
    supervise(rt, current(rt))  # PR opens: review
    c = current(rt)
    argv = rt.sessions.started[-1][1]
    assert c.phase == "review" and env_of(argv)["FLOCC_RUN"] == f"review@{c.started_at}"
    assert "--remote-control" not in argv
