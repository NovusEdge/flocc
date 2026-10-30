import json

import pytest

from flocc import events, ledger
from flocc.cli import main
from flocc.events import Follower
from tests.paths import EXAMPLE


@pytest.fixture
def con(tmp_path):
    return ledger.connect(tmp_path / "l.sqlite")


def config(tmp_path):
    path = tmp_path / "c.toml"
    path.write_text(
        EXAMPLE.read_text()
        .replace('"~/.local/state/flocc/ledger.sqlite"', f'"{tmp_path / "l.sqlite"}"')
        .replace("~/.local/state/flocc", str(tmp_path / "state"))
    )
    return path


def ids(text):
    return [e["id"] for e in json.loads(text)["events"]]


def every_id(out):
    return [i for text in out for i in ids(text)]


def test_emit_gives_increasing_ids_and_trims_the_detail(con):
    a = events.emit(con, "ended", 1.0, issue_key="NF-1", repo="backend", detail="x" * 900)
    b = events.emit(con, "daily", 2.0)
    assert b > a
    first = events.unacked(con)[0]
    assert (first.kind, first.issue_key, first.repo, first.prints) == (
        "ended",
        "NF-1",
        "backend",
        0,
    )
    assert len(first.detail) == events.DETAIL_CHARS


def test_an_unknown_kind_is_refused(con):
    with pytest.raises(ValueError, match="unknown event kind"):
        events.emit(con, "started", 1.0)


def test_a_batch_closes_two_minutes_after_its_first_event(con):
    f = Follower(con, [].append)
    events.emit(con, "ended", 0.0, issue_key="NF-1")
    events.emit(con, "cap", 60.0, issue_key="NF-2")
    assert f.due(119.0) == []
    assert [e.issue_key for e in f.due(120.0)] == ["NF-1", "NF-2"]


def test_redelivery_every_thirty_minutes_stops_after_three(con):
    out: list[str] = []
    f = Follower(con, out.append)
    n = events.emit(con, "ended", 0.0, issue_key="NF-1")
    f.connect(0.0)
    f.poll(events.REDELIVER_S - 1)
    assert len(out) == 1
    for k in range(1, 6):
        f.poll(k * events.REDELIVER_S)
    assert every_id(out) == [n] * (1 + events.REDELIVERIES)
    assert [e.id for e in events.attention(con, 6000.0)] == [n]
    assert events.attention(con, events.ATTENTION_S - 1) == []


def test_reconnects_do_not_make_an_event_need_attention(con):
    n = events.emit(con, "ended", 0.0, issue_key="NF-1")
    for k in range(5):
        Follower(con, [].append).connect(float(k))
    assert events.unacked(con)[0].prints == 5
    assert events.attention(con, 10.0) == []
    assert [e.id for e in events.attention(con, events.ATTENTION_S)] == [n]


def test_an_event_past_its_redeliveries_is_still_printed_on_connect(con):
    n = events.emit(con, "ended", 0.0)
    for _ in range(1 + events.REDELIVERIES):
        events.mark_printed(con, [n], 1.0)
    out: list[str] = []
    Follower(con, out.append).connect(2.0)
    assert every_id(out) == [n]


def test_two_followers_each_print_every_event(con):
    first, second = [], []
    a, b = Follower(con, first.append), Follower(con, second.append)
    e1 = events.emit(con, "ended", 0.0, issue_key="NF-1")
    a.connect(0.0)
    b.connect(5.0)
    e2 = events.emit(con, "cap", 10.0, issue_key="NF-2")
    a.poll(130.0)
    b.poll(130.0)
    assert every_id(first) == [e1, e2] and every_id(second) == [e1, e2]


def test_an_acked_event_is_never_printed_again(con):
    f = Follower(con, [].append)
    n = events.emit(con, "ended", 0.0, issue_key="NF-1")
    f.connect(0.0)
    events.ack(con, [n], 5.0)
    assert events.unacked(con) == []
    assert f.due(10_000.0) == []


def test_ack_is_idempotent_and_names_unknown_ids(con):
    n = events.emit(con, "ended", 0.0)
    assert events.ack(con, [n, 999], 5.0) == (1, [999])
    assert events.ack(con, [n], 6.0) == (0, [])
    assert events.since(con, 0.0)[0].acked_at == 5.0


def test_follow_prints_on_connect_then_each_closed_batch(con):
    clock = [0.0]
    events.emit(con, "ended", 0.0, issue_key="NF-1")
    out: list[str] = []
    rounds = iter(range(3))

    def sleep(seconds):
        clock[0] += 60.0
        if clock[0] == 60.0:
            events.emit(con, "cap", clock[0], issue_key="NF-2")

    events.follow(
        con, out.append, lambda: clock[0], sleep, alive=lambda: next(rounds, None) is not None
    )
    assert [json.loads(t)["events"][0]["issue"] for t in out] == ["NF-1", "NF-2"]
    assert json.loads(out[1])["events"][0]["age_s"] == 120


def test_connect_prints_every_unacked_event(con):
    a = events.emit(con, "ended", 0.0, issue_key="NF-1")
    b = events.emit(con, "cap", 1.0, issue_key="NF-2")
    c = events.emit(con, "daily", 2.0)
    events.mark_printed(con, [a], 3.0)
    events.ack(con, [c], 4.0)
    out: list[str] = []
    events.follow(con, out.append, lambda: 5.0, lambda s: None, alive=lambda: False)
    assert len(out) == 1 and ids(out[0]) == [a, b]


def test_hello_says_the_link_is_up_first(con):
    out: list[str] = []
    events.follow(con, out.append, lambda: 0.0, lambda s: None, alive=lambda: False, hello=True)
    assert out == ['{"link": "up"}']


def test_a_big_backlog_is_split_into_lines_of_fifty(con):
    for n in range(events.PER_LINE + 1):
        events.emit(con, "ended", float(n))
    out: list[str] = []
    events.follow(con, out.append, lambda: 500.0, lambda s: None, alive=lambda: False)
    assert [len(json.loads(t)["events"]) for t in out] == [events.PER_LINE, 1]


def test_prune_drops_only_old_acked_events(con):
    old = events.emit(con, "ended", 0.0)
    kept = events.emit(con, "ended", 0.0)
    events.ack(con, [old], 1.0)
    assert events.prune(con, 1.0 + events.KEEP_ACKED_S) == 1
    assert [e.id for e in events.unacked(con)] == [kept]


def test_the_ack_command_is_idempotent(tmp_path, capsys):
    path = config(tmp_path)
    n = events.emit(ledger.connect(tmp_path / "l.sqlite"), "ended", 0.0)
    assert main(["--config", str(path), "ack", str(n), "77"]) == 0
    assert capsys.readouterr().out.strip() == "acked 1; no event 77"
    assert main(["--config", str(path), "ack", str(n)]) == 0
    assert capsys.readouterr().out.strip() == "acked 0"


def test_events_without_follow_prints_and_counts_nothing(tmp_path, capsys):
    path = config(tmp_path)
    con = ledger.connect(tmp_path / "l.sqlite")
    n = events.emit(con, "ended", 0.0, issue_key="NF-1")
    assert main(["--config", str(path), "events"]) == 0
    assert ids(capsys.readouterr().out) == [n]
    assert events.unacked(con)[0].prints == 0
