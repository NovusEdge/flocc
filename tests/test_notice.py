from flocc import events, ledger
from flocc.linear import LinearError
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


def test_a_notice_with_an_event_writes_it_after_the_comment(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    Notifier(con, FakeLinear(), lambda: 5.0).send(
        Notice("NF-1:crash", "Died twice.", issue_key="NF-1", event="ended"), ISSUE
    )
    [e] = events.unacked(con)
    assert (e.kind, e.issue_key, e.detail, e.at) == ("ended", "NF-1", "crash: Died twice.", 5.0)


def test_a_notice_detail_replaces_the_cause_and_summary_in_the_event(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    Notifier(con, FakeLinear(), lambda: 5.0).send(
        Notice(
            "NF-1:needs-owner",
            "The executor needs you:\n1. Which currency?",
            issue_key="NF-1",
            event="needs-owner",
            detail="1 question",
        ),
        ISSUE,
    )
    [e] = events.unacked(con)
    assert (e.kind, e.detail) == ("needs-owner", "1 question")


def test_no_event_when_the_comment_fails(tmp_path):
    class Down:
        def comment(self, issue_id, text):
            raise LinearError("down")

    con = ledger.connect(tmp_path / "l.sqlite")
    try:
        Notifier(con, Down(), lambda: 5.0).send(
            Notice("NF-1:crash", "Died.", issue_key="NF-1", event="ended"), ISSUE
        )
    except LinearError:
        pass
    assert events.unacked(con) == []


def test_a_notice_without_an_event_writes_none(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    Notifier(con, FakeLinear(), lambda: 5.0).send(Notice("NF-1:prepare", "Failed."), ISSUE)
    assert events.unacked(con) == []


def test_a_stop_notice_emits_its_cause_and_type_and_keeps_the_detail(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    Notifier(con, FakeLinear(), lambda: 5.0).send(
        Notice(
            "NF-1:publish-failed",
            "Publishing failed 3 times in a row",
            issue_key="NF-1",
            event="ended",
            stop_type="transient",
        ),
        ISSUE,
    )
    [e] = events.unacked(con)
    assert (e.cause, e.stop_type) == ("publish-failed", "transient")
    assert e.detail == "publish-failed: Publishing failed 3 times in a row"


def test_a_notice_without_a_stop_type_emits_no_cause(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    Notifier(con, FakeLinear(), lambda: 5.0).send(
        Notice("NF-1:supervisor-error", "boom", issue_key="NF-1", event="service-error"), ISSUE
    )
    [e] = events.unacked(con)
    assert (e.cause, e.stop_type) == (None, None)
