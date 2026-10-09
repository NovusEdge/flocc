import json

import pytest

from linear_agents import ledger
from linear_agents.cli import main
from linear_agents.config import load
from linear_agents.escalation import Escalation, Notifier, ntfy_push, route, scoped, validate
from linear_agents.linear import Issue
from tests.paths import EXAMPLE

CFG = load(EXAMPLE)
ISSUE = Issue("u1", "NF-1", "T", "", "https://l/NF-1", "started", (), None, "t1")


@pytest.mark.parametrize(
    "esc,expected",
    [
        (Escalation(1, "c", "s", "a"), ["ntfy:5", "label:escalation", "comment"]),
        (Escalation(1, "c", "s", "a", scope="global"), ["ntfy:5", "create-issue"]),
        (
            Escalation(2, "c", "s", "a", default="d", deadline_min=60),
            ["label:needs-decision", "comment"],
        ),
        (Escalation(2, "c", "s", "a", waiting=True), ["label:needs-decision", "comment", "ntfy:3"]),
        (Escalation(3, "c", "s"), ["digest"]),
        (Escalation(4, "c", "s"), ["log"]),
    ],
)
def test_route(esc, expected):
    assert route(esc, seen_recently=False) == expected


def test_repeat_cause_drops_only_the_push():
    assert route(Escalation(1, "c", "s", "a"), seen_recently=True) == [
        "label:escalation",
        "comment",
    ]
    assert route(Escalation(2, "c", "s", "a", waiting=True), seen_recently=True) == [
        "label:needs-decision",
        "comment",
    ]
    assert route(Escalation(1, "c", "s", "a", scope="global"), seen_recently=True) == ["log"]


def test_scoped_prefixes_issue_causes_only():
    esc = Escalation(2, "schema", "s", "a", default="d", deadline_min=5)
    assert scoped(esc, "NF-1").cause == "NF-1:schema"
    assert scoped(Escalation(1, "setup", "s", "a", scope="global"), "NF-1").cause == "setup"


@pytest.mark.parametrize(
    "esc,msg",
    [
        (Escalation(1, "c", "s"), "action"),
        (Escalation(2, "c", "s", "a"), "default"),
        (Escalation(5, "c", "s"), "tier"),
    ],
)
def test_validate(esc, msg):
    with pytest.raises(ValueError, match=msg):
        validate(esc)


class Recorder:
    def __init__(self):
        self.calls = []

    def __getattr__(self, name):
        return lambda *a, **k: self.calls.append((name, a))


def test_notifier_dedupes_by_cause(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    pushes, linear = [], Recorder()
    n = Notifier(CFG, con, linear, now=lambda: 1000.0, push=lambda *a, **k: pushes.append(a))
    esc = Escalation(1, "setup-broken", "setup fails", "fix ./setup", issue_key="NF-1")
    n.send(esc, ISSUE, "t1")
    n.send(esc, ISSUE, "t1")
    assert len(pushes) == 1
    assert [c[0] for c in linear.calls] == ["add_label", "comment", "add_label", "comment"]


def test_failed_action_is_not_recorded_as_seen(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")

    class Down:
        def add_label(self, *a):
            raise RuntimeError("linear down")

    n = Notifier(CFG, con, Down(), now=lambda: 1000.0, push=lambda *a, **k: None)
    with pytest.raises(RuntimeError):
        n.send(Escalation(2, "NF-1:x", "s", "a", waiting=True), ISSUE, "t1")
    assert not ledger.alert_seen(con, "NF-1:x", within_s=86400, now=1000.0)


def test_global_cause_gets_one_shared_issue(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    clock = {"now": 1000.0}

    class Shared:
        def __init__(self):
            self.created = []

        def create_issue(self, team_id, title, description):
            self.created.append(title)
            return "https://l/ESC-1"

    linear = Shared()
    n = Notifier(CFG, con, linear, now=lambda: clock["now"], push=lambda *a, **k: None)
    esc = Escalation(1, "setup-broken", "setup fails", "fix ./setup", scope="global")
    n.send(esc, None, "t1")
    clock["now"] += 25 * 3600
    n.send(esc, None, "t1")
    assert linear.created == ["Escalation: setup-broken"]


def test_ntfy_keeps_unicode_out_of_headers():
    seen = {}

    def opener(req, timeout):
        seen["headers"] = dict(req.header_items())
        seen["body"] = req.data.decode()

        class R:
            def __enter__(self):
                return self

            def __exit__(self, *a):
                return False

        return R()

    ntfy_push("https://ntfy.sh/t", "NF-1 décision", "café ☕", 5, opener=opener)
    for value in seen["headers"].values():
        value.encode("latin-1")
    assert "café ☕" in seen["body"] and "décision" in seen["body"]


def test_escalate_cli_appends_json(tmp_path, monkeypatch):
    monkeypatch.setenv("LINEAR_AGENTS_STATE", str(tmp_path))
    rc = main(
        [
            "escalate",
            "--tier",
            "2",
            "--cause",
            "schema",
            "--summary",
            "Rename column?",
            "--action",
            "Answer yes or no",
            "--default",
            "keep name",
            "--deadline-min",
            "120",
        ]
    )
    assert rc == 0
    line = json.loads((tmp_path / "escalations.jsonl").read_text().splitlines()[0])
    assert line["tier"] == 2 and line["deadline_min"] == 120 and line["issue_key"] == tmp_path.name


def test_escalate_cli_rejects_tier2_without_default(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("LINEAR_AGENTS_STATE", str(tmp_path))
    rc = main(["escalate", "--tier", "2", "--cause", "x", "--summary", "s", "--action", "a"])
    assert rc == 2 and "default" in capsys.readouterr().err


def test_dedupe_window_starts_at_the_last_push(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    clock = {"now": 0.0}
    pushes = []
    n = Notifier(
        CFG, con, Recorder(), now=lambda: clock["now"], push=lambda *a, **k: pushes.append(a)
    )
    esc = Escalation(1, "NF-1:x", "s", "a", issue_key="NF-1")
    for hours in (0, 12, 25):
        clock["now"] = hours * 3600.0
        n.send(esc, ISSUE, "t1")
    assert len(pushes) == 2


def test_push_failure_still_lands_linear_actions(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    linear = Recorder()

    def down(*a, **k):
        raise OSError("ntfy down")

    n = Notifier(CFG, con, linear, now=lambda: 1000.0, push=down)
    n.send(Escalation(1, "NF-1:x", "s", "a", issue_key="NF-1"), ISSUE, "t1")
    assert [c[0] for c in linear.calls] == ["add_label", "comment"]


def test_linear_failure_sends_no_push_and_retry_pushes_once(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    pushes = []

    class Flaky:
        fail = True

        def add_label(self, *a):
            if self.fail:
                raise RuntimeError("linear down")

        def comment(self, *a):
            pass

    linear = Flaky()
    n = Notifier(CFG, con, linear, now=lambda: 1000.0, push=lambda *a, **k: pushes.append(a))
    esc = Escalation(1, "NF-1:x", "s", "a", issue_key="NF-1")
    with pytest.raises(RuntimeError):
        n.send(esc, ISSUE, "t1")
    assert pushes == []
    assert not ledger.alert_seen(con, "NF-1:x", within_s=86400, now=1000.0)
    linear.fail = False
    n.send(esc, ISSUE, "t1")
    assert len(pushes) == 1
