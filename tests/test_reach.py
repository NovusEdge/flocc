from dataclasses import replace

import pytest

from flocc import host, ledger, reach
from flocc.linear import IssueDetail


@pytest.fixture
def con(tmp_cfg):
    return ledger.connect(tmp_cfg.ledger_path)


def detail(**over):
    base = IssueDetail(
        "u1",
        "NF-1",
        "Fix rounding",
        "Round half up.",
        "https://l/NF-1",
        "In Progress",
        "t1",
        ("claude",),
        (("2026-11-01T10:00:00Z", "Owner", "use banker's rounding?"),),
        (("blocks", "NF-2", "Todo", "Export"),),
    )
    return replace(base, **over)


def test_render_issue_marks_the_text_as_data_and_lists_everything():
    text = reach.render_issue(detail())
    assert text.startswith(reach.DATA_NOTE)
    for part in (
        "NF-1: Fix rounding",
        "State: In Progress",
        "Labels: claude",
        "Round half up.",
        "- blocks NF-2 (Todo): Export",
        "- 2026-11-01 10:00 Owner: use banker's rounding?",
    ):
        assert part in text


def test_a_long_first_comment_still_leaves_newer_comments_visible():
    comments = (
        ("2026-11-01T10:00:00Z", "flocc", "x" * 20_000),
        ("2026-11-02T10:00:00Z", "Owner", "newest"),
    )
    assert "Owner: newest" in reach.render_issue(detail(comments=comments))
    view = {
        "reviews": [{"author": {"login": "bot"}, "state": "COMMENTED", "body": "y" * 20_000}],
        "comments": [
            {"author": {"login": "owner"}, "body": "newest", "createdAt": "2026-11-02T09:00:00Z"}
        ],
    }
    assert "owner: newest" in reach.render_pr("u", view)


def test_render_issue_with_nothing_in_it_prints_placeholders():
    text = reach.render_issue(detail(description="", labels=(), comments=(), relations=()))
    assert "Labels: none" in text and "(empty)" in text and "- none" in text
    assert "## Related" not in text


def test_render_issue_is_cut_at_the_read_limit():
    text = reach.render_issue(detail(description="x" * 50_000))
    assert len(text) <= reach.READ_CHARS + len("\n[...cut]") and text.endswith("[...cut]")


def test_render_pr_counts_failing_checks_and_survives_ghost_authors():
    view = {
        "state": "OPEN",
        "mergeable": "MERGEABLE",
        "reviewDecision": None,
        "additions": 10,
        "deletions": 2,
        "changedFiles": 3,
        "statusCheckRollup": [
            {"name": "test", "conclusion": "FAILURE", "status": "COMPLETED"},
            {"context": "lint", "state": "SUCCESS"},
            {"name": "build", "conclusion": None, "status": "IN_PROGRESS"},
        ],
        "reviews": [{"author": None, "state": "COMMENTED", "body": "nit"}],
        "comments": [
            {"author": {"login": "owner"}, "body": "ship it", "createdAt": "2026-11-02T09:00:00Z"}
        ],
    }
    text = reach.render_pr("https://github.com/o/r/pull/7", view)
    assert "State: OPEN, mergeable MERGEABLE, review none" in text
    assert "Diff: 3 files, +10 -2" in text
    assert "Checks: 3, failing: test, pending: build" in text
    assert "- ghost COMMENTED: nit" in text and "- 2026-11-02 09:00 owner: ship it" in text


def test_render_pr_with_no_checks_reviews_or_comments():
    text = reach.render_pr("u", {"state": "MERGED"})
    assert "Checks: 0, failing: none, pending: none" in text and text.count("- none") == 2


def test_render_search_lists_rows_or_says_none():
    assert (
        reach.render_search([("NF-5", "Backlog", "Flaky export")])
        == "- NF-5 (Backlog): Flaky export"
    )
    assert reach.render_search([]) == "No open issue matches."


def test_writes_are_logged_and_read_back_since_a_time(con):
    reach.record_write(con, 10.0, "comment", "NF-1", "hello")
    reach.record_write(con, 20.0, "file", "NF-1", "Title", mode="observe")
    assert [(w["kind"], w["mode"]) for w in reach.writes(con, 15.0)] == [("file", "observe")]


def test_file_is_refused_past_two_per_source_counting_observe_drafts(con):
    reach.record_write(con, 1.0, "file", "NF-1", "a", mode="observe")
    reach.record_write(con, 2.0, "file", "NF-1", "b", filed="NF-9")
    assert "NF-1 already has 2" in reach.file_refusal(con, "NF-1", 3.0)
    assert reach.file_refusal(con, "NF-2", 3.0) == ""


def test_file_is_refused_past_five_in_a_rolling_day(con):
    for i in range(5):
        reach.record_write(con, 100.0 + i, "file", f"NF-{i}", "t")
    assert "5 follow-ups in the last 24 hours" in reach.file_refusal(con, "NF-9", 200.0)
    assert reach.file_refusal(con, "NF-9", 100.0 + 86_400.0 + 1) == ""


def test_comments_do_not_count_toward_the_file_caps(con):
    for i in range(6):
        reach.record_write(con, float(i), "comment", "NF-1", "t")
    assert reach.file_refusal(con, "NF-1", 10.0) == ""


def test_in_team_checks_the_key_prefix(tmp_cfg):
    assert reach.in_team(tmp_cfg, "NF-12") and not reach.in_team(tmp_cfg, "ENG-12")


def test_the_shell_log_still_reads_back_through_the_ledger_log(con):
    host.record_shell(con, 5.0, "df -h", None)
    host.record_shell(con, 5.0, "df -h", 0)
    assert host.shell_calls(con, 0.0) == [(5.0, "df -h", 0)]
