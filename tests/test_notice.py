from flocc import ledger
from flocc.notice import Notice, Notifier
from tests.fakes import ISSUE, FakeLinear


def test_a_notice_with_an_issue_is_one_comment_and_no_label(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    linear = FakeLinear()
    Notifier(con, linear, lambda: 5.0).send(
        Notice("NF-1:crash", "The session died twice.", "Release it", issue_key="NF-1"), ISSUE
    )
    assert linear.comments == ["The session died twice.\nAction: Release it"]
    assert ledger.pending_digest(con) == []


def test_a_notice_without_an_issue_goes_to_the_digest(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    linear = FakeLinear()
    Notifier(con, linear, lambda: 5.0).send(Notice("claude-login", "Login lapsed."))
    assert linear.comments == []
    assert [line for _, _, line in ledger.pending_digest(con)] == ["Login lapsed."]
