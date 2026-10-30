from dataclasses import replace

import pytest

from flocc import canary, ledger, rebase
from flocc.context import state_dir
from flocc.publisher import PublishResult
from flocc.supervisor import supervise
from flocc.usage import write_status
from tests.fakes import ISSUE
from tests.test_ledger import make
from tests.test_supervisor import add, current, done, rt  # noqa: F401 - the fixture

CONFLICTING = {
    "state": "OPEN",
    "mergeable": "CONFLICTING",
    "files": [],
    "headRefOid": "sha-tests",
}
BASE = "0123456789abcdef"


@pytest.fixture
def pr(rt, monkeypatch, tmp_path):  # noqa: F811
    """A finished claim whose open PR the fake gh reports as `rt.view`."""
    rt.view = dict(CONFLICTING)
    rt.pr_view = lambda claim, url: rt.view
    rt.linear.state_types = lambda keys: {k: "started" for k in keys}
    rt.linear.issue = lambda key: replace(ISSUE, labels=("claude",))
    monkeypatch.setattr("flocc.rebase.origin_base", lambda repo: BASE)
    monkeypatch.setattr("flocc.poller.origin_base", lambda repo: BASE)
    wt = tmp_path / "wt-1"
    wt.mkdir()
    add(rt, state="done", phase="review", worktree=str(wt), tester_sha="sha-tests")
    (state_dir(rt.cfg, "NF-1") / "pr-url").write_text("https://github.com/o/r/pull/1")
    return rt


def test_a_conflicting_pr_starts_one_fix_session_on_the_same_branch(pr):
    rebase.step(pr)
    c = current(pr)
    assert (c.state, c.phase, c.fix_attempts, c.restarts) == ("running", "fix", 0, 0)
    name, argv = pr.sessions.started[-1]
    prompt = next(a for a in argv if "origin/main" in a)
    assert name == "NF-1" and "without changing behaviour" in prompt
    assert ledger.meta_get(pr.con, "rebase_tried:NF-1") == BASE
    assert "flocc tests-amended" in prompt
    assert (state_dir(pr.cfg, "NF-1") / "check-failure.md").read_text() in prompt


@pytest.mark.parametrize(
    "view",
    [
        {"state": "OPEN", "mergeable": "MERGEABLE"},
        {"state": "OPEN", "mergeable": "UNKNOWN"},
        {"state": "MERGED", "mergeable": "CONFLICTING"},
        {"state": "CLOSED", "mergeable": "CONFLICTING"},
    ],
)
def test_a_pr_that_merges_cleanly_or_is_settled_is_left_alone(pr, view):
    pr.view = view
    rebase.step(pr)
    assert pr.sessions.started == [] and current(pr).state == "done"


def test_a_claim_with_a_live_session_is_left_alone(pr):
    ledger.update_claim(pr.con, "NF-1", state="running")
    rebase.step(pr)
    assert pr.sessions.started == []


def test_a_reaped_worktree_is_left_alone(pr, tmp_path):
    (tmp_path / "wt-1").rmdir()
    rebase.step(pr)
    assert pr.sessions.started == [] and current(pr).state == "done"


def test_a_pr_view_failure_does_not_stop_the_other_claims(pr):
    def broken(claim, url):
        raise OSError("gh down")

    pr.pr_view = broken
    rebase.step(pr)
    assert pr.sessions.started == []


def test_the_same_base_is_not_tried_twice_and_the_second_conflict_blocks(pr):
    rebase.step(pr)
    started = len(pr.sessions.started)
    ledger.update_claim(pr.con, "NF-1", state="done")
    rebase.step(pr)
    assert len(pr.sessions.started) == started
    assert current(pr).state == "blocked"
    assert BASE[:9] in pr.notifier.sent[-1].summary
    (line,) = [d for _, k, d in ledger.pending_digest(pr.con) if k == "NF-1"]
    assert BASE[:9] in line and BASE not in line and "conflict" in line


def test_a_base_that_moved_again_gets_a_new_attempt(pr, monkeypatch):
    rebase.step(pr)
    ledger.update_claim(pr.con, "NF-1", state="done")
    monkeypatch.setattr("flocc.rebase.origin_base", lambda repo: "base-2")
    rebase.step(pr)
    assert len(pr.sessions.started) == 2
    assert ledger.meta_get(pr.con, "rebase_tried:NF-1") == "base-2"


def test_a_pr_pushed_to_by_someone_else_is_left_alone(pr):
    pr.view = {**CONFLICTING, "headRefOid": "pushed-by-the-owner"}
    rebase.step(pr)
    assert pr.sessions.started == [] and current(pr).state == "done"


def test_a_pr_whose_issue_is_unlabelled_or_parked_is_left_alone(pr):
    pr.linear.issue = lambda key: replace(ISSUE, labels=())
    rebase.step(pr)
    pr.linear.issue = lambda key: replace(ISSUE, labels=("claude",))
    pr.linear.state_types = lambda keys: {k: "unstarted" for k in keys}
    rebase.step(pr)
    assert pr.sessions.started == []


def test_the_session_cap_holds_a_rebase(pr):
    for key in ("NF-7", "NF-8"):
        ledger.insert_claim(pr.con, make(key, repo="web"))
    window = {"used_percentage": 20.0, "resets_at": 4_000_000_000}
    write_status(
        state_dir(pr.cfg, "NF-1"), {"rate_limits": {"five_hour": window, "seven_day": window}}
    )
    rebase.step(pr)
    assert pr.sessions.started == [] and current(pr).state == "done"
    ledger.meta_set(pr.con, "base_held:NF-7", "{}")
    rebase.step(pr)
    assert len(pr.sessions.started) == 1


def test_a_repo_held_on_a_broken_main_gets_no_rebase(pr):
    canary.mark(pr.con, "backend", BASE, pr.now(), "E1 boom", check="just check")
    rebase.step(pr)
    assert pr.sessions.started == [] and current(pr).state == "done"


def test_an_unreachable_remote_starts_nothing(pr, monkeypatch):
    monkeypatch.setattr("flocc.rebase.origin_base", lambda repo: None)
    rebase.step(pr)
    assert pr.sessions.started == []


def test_no_session_starts_when_usage_is_over_the_limit(pr):
    over = {"used_percentage": 95.0, "resets_at": 4_000_000_000}
    write_status(state_dir(pr.cfg, "NF-1"), {"rate_limits": {"five_hour": over, "seven_day": over}})
    rebase.step(pr)
    assert pr.sessions.started == []
    assert not ledger.meta_get(pr.con, "rebase_tried:NF-1")


def test_the_session_publishes_to_the_open_pr_and_ends_without_a_review(pr):
    calls = []

    def publish(c, t, s, **kw):
        calls.append(kw)
        return PublishResult("opened", url="https://pr/1", existing=True)

    rebase.step(pr)
    pr.publish_fn = publish
    done(pr, "Merged origin/main; two conflicts in a.py resolved.")
    supervise(pr, current(pr))
    assert calls == [{"reuse_only": True}]
    assert current(pr).state == "done" and len(pr.sessions.started) == 1
    assert "https://pr/1" in pr.linear.comments[-1]
    assert "not reviewed again" in pr.linear.comments[-1]
    assert not ledger.meta_get(pr.con, "rebase:NF-1")
    assert len(pr.pr_comments) == 0


def test_a_pr_closed_during_the_rebase_ends_the_claim(pr):
    rebase.step(pr)
    pr.publish_fn = lambda c, t, s, **kw: PublishResult("pr_closed")
    done(pr)
    supervise(pr, current(pr))
    assert current(pr).state == "done" and "closed or merged" in pr.linear.comments[-1]


def test_a_failed_launch_restores_the_finished_claim_and_forgets_the_attempt(pr):
    pr.cfg.agent_gh_token_file.write_text("")
    rebase.step(pr)
    c = current(pr)
    assert (c.state, c.phase) == ("done", "review")
    assert not ledger.meta_get(pr.con, "rebase_tried:NF-1")
    assert not ledger.meta_get(pr.con, "rebase:NF-1")
