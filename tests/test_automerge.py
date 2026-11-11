from dataclasses import replace

import pytest

from flocc import automerge, ledger
from flocc.context import state_dir
from flocc.supervisor import supervise
from tests.test_supervisor import add, current, done, review_session, rt  # noqa: F401 - the fixture

URL = "https://github.com/o/r/pull/1"
HEAD = "sha-head"
OPEN = {"state": "OPEN", "mergeable": "MERGEABLE", "headRefOid": HEAD, "reviews": [], "labels": []}


@pytest.fixture
def pr(rt, tmp_path):  # noqa: F811
    """A finished claim approved at HEAD whose PR the fake gh reports as `rt.view`."""
    rt.view = dict(OPEN)
    rt.merged = []
    rt.merge_error = None

    def merge_pr(claim, url, head):
        if rt.merge_error:
            raise rt.merge_error
        rt.merged.append((url, head))

    rt.pr_view = lambda claim, url: rt.view
    rt.merge_pr = merge_pr
    wt = tmp_path / "wt-1"
    wt.mkdir()
    add(rt, state="done", phase="review", worktree=str(wt))
    (state_dir(rt.cfg, "NF-1") / "pr-url").write_text(URL)
    ledger.meta_set(rt.con, "review_approved:NF-1", HEAD)
    return rt


def set_auto_merge(rt, on: bool) -> None:
    repos = {n: replace(r, auto_merge=on) for n, r in rt.cfg.repos.items()}
    rt.cfg = replace(rt.cfg, repos=repos)


def test_an_approved_mergeable_pr_is_merged_once_and_reported(pr):
    automerge.step(pr)
    automerge.step(pr)
    pr.view = {**OPEN, "state": "MERGED"}
    automerge.step(pr)
    assert pr.merged == [(URL, HEAD)]
    assert current(pr).state == "done"
    assert pr.linear.comments == [f"Merged {URL} after review."]
    assert [d for _, k, d in ledger.pending_digest(pr.con) if k == "NF-1"] == [
        f"merged {URL} after review"
    ]


def test_a_repo_without_auto_merge_is_never_merged(pr):
    set_auto_merge(pr, False)
    automerge.step(pr)
    assert pr.merged == []


def test_a_review_that_asked_for_changes_is_not_merged(pr):
    ledger.meta_set(pr.con, "review_approved:NF-1", "")
    automerge.step(pr)
    assert pr.merged == []


def test_a_head_that_moved_after_the_approve_is_not_merged(pr):
    pr.view = {**OPEN, "headRefOid": "sha-after-fix"}
    automerge.step(pr)
    assert pr.merged == []


def test_an_unknown_mergeability_waits_for_the_next_tick(pr):
    pr.view = {**OPEN, "mergeable": "UNKNOWN"}
    automerge.step(pr)
    assert pr.merged == []
    pr.view = dict(OPEN)
    automerge.step(pr)
    assert pr.merged == [(URL, HEAD)]


@pytest.mark.parametrize(
    "view",
    [
        {"mergeable": "CONFLICTING"},
        {"state": "CLOSED"},
        {"labels": [{"name": "do-not-merge"}]},
        {"labels": [{"name": "hold"}]},
        {"reviews": [{"author": {"login": "bob"}, "state": "CHANGES_REQUESTED"}]},
    ],
)
def test_a_pr_that_is_not_clear_to_merge_is_left_alone(pr, view):
    pr.view = {**OPEN, **view}
    automerge.step(pr)
    assert pr.merged == []


def test_a_person_who_later_approved_does_not_block(pr):
    pr.view = {
        **OPEN,
        "reviews": [
            {"author": {"login": "bob"}, "state": "CHANGES_REQUESTED"},
            {"author": {"login": "bob"}, "state": "APPROVED"},
        ],
    }
    automerge.step(pr)
    assert pr.merged == [(URL, HEAD)]


def test_an_issue_on_hold_is_left_alone(pr):
    ledger.add_hold(pr.con, "NF-1", "cardinal", 1.0)
    automerge.step(pr)
    assert pr.merged == []


def test_a_failed_merge_alerts_once_and_is_not_retried(pr):
    pr.merge_error = RuntimeError("branch protection")
    automerge.step(pr)
    pr.merge_error = None
    automerge.step(pr)
    assert pr.merged == []
    lines = [d for _, k, d in ledger.pending_digest(pr.con) if k == "NF-1"]
    assert len(lines) == 1 and "branch protection" in lines[0]
    assert ledger.alert_seen(pr.con, "NF-1:auto-merge", ledger.DEDUPE_S, pr.now())


def test_a_new_head_after_a_failed_merge_is_tried_again(pr):
    pr.merge_error = RuntimeError("race")
    automerge.step(pr)
    pr.merge_error = None
    ledger.meta_set(pr.con, "review_approved:NF-1", "sha-2")
    pr.view = {**OPEN, "headRefOid": "sha-2"}
    automerge.step(pr)
    assert pr.merged == [(URL, "sha-2")]


def test_an_approving_review_records_the_head_it_judged(rt):
    review_session(rt)
    done(rt, "review")
    supervise(rt, current(rt))
    assert ledger.meta_get(rt.con, "review_approved:NF-1") == "sha-impl"


def test_a_review_asking_for_changes_clears_the_record(rt):
    ledger.meta_set(rt.con, "review_approved:NF-1", "old")
    review_session(rt, "## Critical\n- a.py:2 wrong\n\nVerdict: changes requested\n")
    done(rt, "review")
    supervise(rt, current(rt))
    assert ledger.meta_get(rt.con, "review_approved:NF-1") == ""
