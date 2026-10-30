import json
import subprocess
from dataclasses import replace

import pytest

from flocc import canary, ledger
from flocc.agentblock import IssueSettings, settings_to_json
from flocc.context import state_dir
from flocc.ledger import Claim
from flocc.linear import LinearError
from flocc.publisher import BranchMoved, PublishResult
from flocc.runtime import Runtime
from flocc.supervisor import LaunchError, supervise
from flocc.usage import write_status
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
    before = (d / "done.json").read_text()
    working = rt.sessions.start

    def down(name, cwd, argv):
        raise subprocess.CalledProcessError(1, ["tmux"])

    rt.sessions.start = down
    with pytest.raises(subprocess.CalledProcessError):
        supervise(rt, current(rt))
    assert (d / "done.json").read_text() == before
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


def test_publish_notes_a_label_that_was_not_added_in_the_digest(rt):
    rt.publish_fn = lambda c, t, s: PublishResult("opened", url="u", label_error="no such label")
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt, "Implemented.")
    supervise(rt, current(rt))
    lines = [line for _, _, line in ledger.pending_digest(rt.con)]
    assert any("no such label" in line and "label" in line for line in lines)
    assert any(name == "NF-1-review" for name, _ in rt.sessions.started)


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
    supervise(rt, current(rt))  # check fails: fix starts
    rt.sessions.alive_names.clear()
    supervise(rt, current(rt))  # the fix session died: relaunched once
    assert len(rt.sessions.started) == 2
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

    def flaky(notice, issue=None):
        calls.append(notice)
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


def test_only_implementer_and_fix_sessions_use_remote_control(rt):
    add(rt)
    supervise(rt, current(rt))  # tester relaunch
    assert "--remote-control" not in rt.sessions.started[-1][1]
    rt.sessions.alive_names.add("NF-1-test")
    done(rt)
    supervise(rt, current(rt))  # implementer
    assert "--remote-control" in rt.sessions.started[-1][1]


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


def test_relaunch_clears_the_idle_marker(rt):
    add(rt)
    d = state_dir(rt.cfg, "NF-1")
    (d / "idle").write_text("{}")
    rt.sessions.alive_names.clear()
    ledger.update_claim(rt.con, "NF-1", restarts=0)
    from flocc.supervisor import launch

    launch(rt, current(rt), "test")
    assert not (d / "idle").exists()


def test_an_idle_marker_ends_the_run_blocked(rt):
    add(rt, phase="impl")
    rt.sessions.alive_names.add("NF-1")
    d = state_dir(rt.cfg, "NF-1")
    (d / "idle").write_text(json.dumps({"at": 50.0, "message": "Which option do you want?"}))
    supervise(rt, current(rt))
    assert current(rt).state == "blocked" and rt.sessions.killed == ["NF-1"]
    (notice,) = rt.notifier.sent
    assert notice.cause == "NF-1:idle" and "Which option do you want?" in notice.summary


def test_an_idle_reviewer_skips_the_review_without_blocking(rt):
    add(rt, phase="review")
    rt.sessions.alive_names.add("NF-1-review")
    (state_dir(rt.cfg, "NF-1") / "idle").write_text(json.dumps({"at": 50.0, "message": ""}))
    supervise(rt, current(rt))
    assert current(rt).state == "done"
    assert any("review skipped" in line for _, _, line in ledger.pending_digest(rt.con))


def test_a_crash_restarts_once_with_a_note_then_blocks(rt):
    add(rt, phase="impl")
    supervise(rt, current(rt))
    name, argv = rt.sessions.started[-1]
    assert name == "NF-1" and current(rt).restarts == 1
    assert any("ended before flocc done" in a for a in argv)
    rt.sessions.alive_names.discard("NF-1")
    supervise(rt, current(rt))
    assert current(rt).state == "blocked" and rt.notifier.sent[-1].cause == "NF-1:crash"


def test_a_finished_step_leaves_its_summary_for_the_next(rt):
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    done(rt, "tests for A and B")
    supervise(rt, current(rt))
    assert (state_dir(rt.cfg, "NF-1") / "last-summary.md").read_text() == "tests for A and B"


def settings_of(argv):
    return json.loads(argv[argv.index("--settings") + 1])


def test_only_fix_launches_get_remote_control(rt):
    rt.publish_fn = lambda c, t, s: PublishResult("check_failed", "E1 boom")
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))  # check fails: fix
    argv = rt.sessions.started[-1][1]
    assert current(rt).phase == "fix" and "--remote-control" in argv
    assert "FLOCC_RUN" not in env_of(argv)
    rt.publish_fn = lambda c, t, s: PublishResult("opened", url="https://pr/1")
    done(rt)
    supervise(rt, current(rt))  # PR opens: review
    argv = rt.sessions.started[-1][1]
    assert current(rt).phase == "review" and "--remote-control" not in argv


BASE_SHA = "0123456789abcdef"


def failed(detail, log=None):
    return PublishResult("check_failed", detail, log=detail if log is None else log)


def base_check(rt, monkeypatch, sha=BASE_SHA, failure=None):
    """Wires the base-branch check to a fixed result and the repo's base SHA to `sha`."""
    runs = []

    def run(repo):
        runs.append(repo)
        return sha, failure

    rt.base_check = run
    monkeypatch.setattr("flocc.supervisor.origin_base", lambda repo: sha)
    return runs


def test_a_check_that_also_fails_on_the_base_starts_no_fix_round_and_holds_the_repo(
    rt, monkeypatch
):
    rt.publish_fn = lambda c, t, s: failed("E1 boom")
    runs = base_check(rt, monkeypatch, failure=failed("E1 boom"))
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    assert runs == ["backend"] and rt.sessions.started == []
    c = current(rt)
    assert c.state == "running" and c.fix_attempts == 0
    mark = canary.get_mark(rt.con, "backend")
    assert mark is not None and mark["sha"] == BASE_SHA and mark["check"] == "just check"
    line = rt.linear.comments[-1]
    assert "`just check`" in line and BASE_SHA[:9] in line and BASE_SHA not in line
    assert [d for _, k, d in ledger.pending_digest(rt.con) if k == "NF-1"] == [line]
    assert (state_dir(rt.cfg, "NF-1") / "done.json").exists()


def test_a_check_that_passes_on_the_base_gets_its_fix_round(rt, monkeypatch):
    rt.publish_fn = lambda c, t, s: failed("E1 boom")
    base_check(rt, monkeypatch, failure=None)
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).phase == "fix" and current(rt).fix_attempts == 1
    assert canary.get_mark(rt.con, "backend") is None


def test_a_different_failure_on_the_base_does_not_hold_the_branch(rt, monkeypatch):
    rt.publish_fn = lambda c, t, s: failed("E1 boom")
    base_check(rt, monkeypatch, failure=failed("E9 elsewhere"))
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).phase == "fix" and canary.get_mark(rt.con, "backend") is None


@pytest.mark.parametrize("exc", [subprocess.CalledProcessError(128, "git"), OSError("no disk")])
def test_a_base_check_that_cannot_run_falls_back_to_the_fix_round(rt, monkeypatch, exc):
    rt.publish_fn = lambda c, t, s: failed("E1 boom")

    def broken(repo):
        raise exc

    rt.base_check = broken
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).phase == "fix"


def test_the_base_is_checked_only_before_the_first_fix_round(rt, monkeypatch):
    rt.publish_fn = lambda c, t, s: failed("E1 boom")
    runs = base_check(rt, monkeypatch, failure=None)
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    done(rt)
    supervise(rt, current(rt))
    assert len(runs) == 1


def test_a_held_claim_waits_without_publishing_until_the_base_moves(rt, monkeypatch):
    published = []

    def publish(c, t, s, **kw):
        published.append(1)
        return failed("E1 boom")

    rt.publish_fn = publish
    base_check(rt, monkeypatch, failure=failed("E1 boom"))
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    supervise(rt, current(rt))
    assert published == [1] and current(rt).state == "running"
    assert len(rt.linear.comments) == 1
    monkeypatch.setattr("flocc.supervisor.origin_base", lambda repo: "fedcba9876543210")
    rt.publish_fn = lambda c, t, s, **kw: PublishResult("opened", url="https://pr/1")
    supervise(rt, current(rt))
    assert current(rt).phase == "review"
    assert canary.get_mark(rt.con, "backend") is None


def test_a_held_claim_keeps_waiting_when_the_remote_is_unreachable(rt, monkeypatch):
    rt.publish_fn = lambda c, t, s: failed("E1 boom")
    base_check(rt, monkeypatch, failure=failed("E1 boom"))
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    monkeypatch.setattr("flocc.supervisor.origin_base", lambda repo: None)
    rt.publish_fn = lambda c, t, s, **kw: pytest.fail("published while held")
    supervise(rt, current(rt))
    assert current(rt).state == "running"


PYTEST_BRANCH = "FAILED t.py::test_a - boom\n=== 1 failed, 120 passed in 3.2s ==="
PYTEST_BASE = "FAILED t.py::test_a - boom\n=== 1 failed, 100 passed in 2.9s ==="


def held_after(rt, monkeypatch, branch, base):
    rt.publish_fn = lambda c, t, s: failed(branch)
    base_check(rt, monkeypatch, failure=failed(base))
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    return canary.get_mark(rt.con, "backend") is not None


def test_the_same_failing_tests_hold_the_repo_whatever_else_the_output_counts(rt, monkeypatch):
    assert held_after(rt, monkeypatch, PYTEST_BRANCH, PYTEST_BASE)


def test_a_branch_failing_more_tests_than_the_base_is_not_held(rt, monkeypatch):
    more = "FAILED t.py::test_b - bang\n" + PYTEST_BRANCH
    assert not held_after(rt, monkeypatch, more, PYTEST_BASE)


def test_a_new_lint_error_of_a_kind_already_on_the_base_is_not_the_same_failure(rt, monkeypatch):
    base = "ERROR Argument `str | None` is not assignable [bad-argument-type]\n 9 errors"
    branch = (
        "ERROR Argument `str | None` is not assignable [bad-argument-type]\n"
        "ERROR Argument `int` is not assignable [bad-argument-type]\n 10 errors"
    )
    assert not held_after(rt, monkeypatch, branch, base)


def test_the_same_test_failing_twice_on_the_branch_is_not_the_base_s_single_failure(
    rt, monkeypatch
):
    branch = "FAILED t.py::test_a[1] - x\nFAILED t.py::test_a[1] - y\n"
    assert not held_after(rt, monkeypatch, branch, "FAILED t.py::test_a[1] - x\n")


def test_go_test_failures_match_by_name(rt, monkeypatch):
    out = "--- FAIL: TestA (0.10s)\n    a_test.go:3: boom\nFAIL\tpkg\t0.2s\n"
    assert held_after(rt, monkeypatch, out, out.replace("0.10s", "0.31s"))


def test_outputs_without_test_ids_match_after_the_checkout_path_and_counts_are_dropped(
    rt, monkeypatch
):
    branch = "/wt/NF-1/src/a.py:3: error: bad\nFound 3 errors, 12 passed"
    base = "<checkout>/src/a.py:3: error: bad\nFound 3 errors, 40 passed"
    assert held_after(rt, monkeypatch, branch, base)


def test_a_dirty_tree_failure_does_not_trigger_a_base_check(rt, monkeypatch):
    rt.publish_fn = lambda c, t, s: PublishResult("check_failed", "Uncommitted changes: x.py")
    runs = base_check(rt, monkeypatch, failure=failed("Uncommitted changes: x.py"))
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    assert runs == [] and current(rt).phase == "fix"


def test_a_released_claim_that_fails_the_same_way_is_told_to_merge_the_new_base(rt, monkeypatch):
    rt.publish_fn = lambda c, t, s: failed("E1 boom")
    base_check(rt, monkeypatch, failure=failed("E1 boom"))
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    monkeypatch.setattr("flocc.supervisor.origin_base", lambda repo: "fedcba9876543210")
    rt.base_check = lambda repo: ("fedcba9876543210", None)
    supervise(rt, current(rt))
    assert current(rt).phase == "fix"
    prompt = next(a for a in rt.sessions.started[-1][1] if "E1 boom" in a)
    assert "has since moved to fedcba987" in prompt and BASE_SHA[:9] in prompt
    assert "merge `origin/main`" in prompt


def test_a_hold_lifted_by_a_passing_canary_at_the_same_sha_says_it_passes_now(rt, monkeypatch):
    rt.publish_fn = lambda c, t, s: failed("E1 boom")
    base_check(rt, monkeypatch, failure=failed("E1 boom"))
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    canary.clear(rt.con, "backend")
    rt.base_check = lambda repo: (BASE_SHA, None)
    supervise(rt, current(rt))
    prompt = next(a for a in rt.sessions.started[-1][1] if "E1 boom" in a)
    assert f"now passes at {BASE_SHA[:9]}" in prompt and "has since moved" not in prompt


def test_a_different_failure_after_the_hold_gets_no_base_note(rt, monkeypatch):
    base_check(rt, monkeypatch, failure=failed("E1 boom"))
    rt.publish_fn = lambda c, t, s: failed("E1 boom")
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    monkeypatch.setattr("flocc.supervisor.origin_base", lambda repo: "fedcba9876543210")
    rt.base_check = lambda repo: ("fedcba9876543210", None)
    rt.publish_fn = lambda c, t, s: failed("E7 other")
    supervise(rt, current(rt))
    assert not any("has since moved" in a for a in rt.sessions.started[-1][1])


def test_a_check_fix_round_after_a_rebase_keeps_the_merge_instruction(rt):
    ledger.meta_set(rt.con, "rebase:NF-1", "1")
    d = state_dir(rt.cfg, "NF-1")
    add(rt, phase="fix", tester_sha="sha-tests")
    (d / "check-failure.md").write_text("merge origin/main please")
    rt.publish_fn = lambda c, t, s, **kw: failed("E1 boom")
    done(rt)
    supervise(rt, current(rt))
    prompt = next(a for a in rt.sessions.started[-1][1] if "E1 boom" in a)
    assert "merge origin/main please" in prompt


def test_block_comments_on_the_issue_without_labels(rt):
    from flocc.notice import Notifier

    rt.notifier = Notifier(rt.con, rt.linear, rt.now)
    add(rt)
    rt.sessions.alive_names.add("NF-1-test")
    rt.git_head = lambda path: "sha-base"
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).state == "blocked"
    assert rt.linear.comments == [
        (
            "The tester finished without committing tests.\n"
            "Action: Add detail to the acceptance criteria, then run flocc release NF-1"
        )
    ]
