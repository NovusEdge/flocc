import sqlite3

import pytest

from flocc import events, ledger
from flocc.ledger import Claim


def make(key="NF-1", state="running", worktree=None, repo="backend"):
    return Claim(
        issue_key=key,
        issue_id=f"id-{key}",
        repo=repo,
        worktree=worktree or f"/wt/{key}",
        branch=f"agent/{key.lower()}",
        phase="test",
        state=state,
        depends_on=[],
        settings_json="{}",
        started_at=0.0,
        wall_clock_s=3600,
    )


@pytest.fixture
def con(tmp_path):
    return ledger.connect(tmp_path / "l.sqlite")


def test_insert_get_update_live(con):
    ledger.insert_claim(con, make())
    ledger.insert_claim(con, make("NF-2", state="done"))
    ledger.update_claim(con, "NF-1", phase="impl")
    got = ledger.get_claim(con, "NF-1")
    assert got is not None
    assert got.phase == "impl"
    assert [c.issue_key for c in ledger.live_claims(con)] == ["NF-1"]
    by_tree = ledger.claim_for_worktree(con, "/wt/NF-1")
    assert by_tree is not None and by_tree.issue_key == "NF-1"


def test_alert_dedupe_window(con):
    assert not ledger.alert_seen(con, "setup-broken", within_s=86400, now=1000)
    ledger.record_alert(con, "setup-broken", 1, None, now=1000)
    assert ledger.alert_seen(con, "setup-broken", within_s=86400, now=2000)
    assert not ledger.alert_seen(con, "setup-broken", within_s=86400, now=1000 + 86401)
    ledger.clear_alert(con, "setup-broken")
    assert not ledger.alert_seen(con, "setup-broken", within_s=86400, now=2000)


def test_digest_cleared_only_up_to_what_was_posted(con):
    ledger.add_digest(con, "NF-1", "PR opened", now=1)
    rows = ledger.pending_digest(con)
    ledger.add_digest(con, "NF-2", "later", now=2)
    ledger.clear_digest(con, rows[-1][0])
    assert [r[1:] for r in ledger.pending_digest(con)] == [("NF-2", "later")]


def test_usage_log(con):
    ledger.add_usage(con, "NF-1", "test", "start", 10.0, 20.0, now=1)
    ledger.add_usage(con, "NF-1", "test", "end", 14.0, 21.0, now=2)
    assert ledger.usage_for(con, "NF-1") == [
        ("test", "start", 10.0, 20.0),
        ("test", "end", 14.0, 21.0),
    ]


def test_two_connections_share_state(tmp_path):
    a, b = ledger.connect(tmp_path / "l.sqlite"), ledger.connect(tmp_path / "l.sqlite")
    ledger.insert_claim(a, make())
    assert ledger.get_claim(b, "NF-1") is not None


def test_remembered_tests_outlive_the_claim(con):
    assert ledger.remembered_tests(con, "NF-1") is None
    ledger.insert_claim(con, make())
    ledger.remember_tests(con, "NF-1", "sha-base", "sha-tests")
    ledger.delete_claim(con, "NF-1")
    assert ledger.remembered_tests(con, "NF-1") == ("sha-base", "sha-tests")
    assert ledger.remembered_tests(con, "NF-2") is None


def test_an_acceptance_ends_with_its_tester_commit(con):
    ledger.remember_tests(con, "NF-1", "sha-base", "sha-tests")
    ledger.accept_tests(con, "NF-1", "sha-ok")
    assert ledger.accepted_tests(con, "NF-1") == "sha-ok"
    ledger.remember_tests(con, "NF-1", "sha-base", "sha-tests-2")
    assert ledger.accepted_tests(con, "NF-1") is None
    ledger.accept_tests(con, "NF-1", "sha-ok")
    ledger.forget_tests(con, "NF-1")
    assert ledger.accepted_tests(con, "NF-1") is None


V0_CLAIM = (
    "INSERT INTO claims (issue_key, issue_id, repo, worktree, branch, phase, state, paths,"
    " depends_on, settings_json, started_at, wall_clock_s)"
    " VALUES ('NF-9', 'id-9', 'backend', '/wt/NF-9', 'agent/nf-9', 'impl', 'running',"
    " '[\"src/**\"]', '[]', '{}', 1.0, 3600)"
)


def _v0_ledger(path):
    raw = sqlite3.connect(path)
    raw.executescript(ledger._SCHEMA)
    raw.execute(V0_CLAIM)
    raw.commit()
    raw.close()


def _columns(con, table):
    return {r[1] for r in con.execute(f"PRAGMA table_info({table})")}


def test_a_fresh_ledger_is_at_the_latest_version(con):
    assert con.execute("PRAGMA user_version").fetchone()[0] == len(ledger._MIGRATIONS)
    assert {
        "id",
        "kind",
        "issue_key",
        "repo",
        "detail",
        "at",
        "printed_at",
        "acked_at",
        "prints",
        "cause",
        "type",
    } == (_columns(con, "events"))
    assert {"issue_key", "reason", "at"} == _columns(con, "holds")


def test_an_old_ledger_migrates_and_keeps_its_claims(tmp_path):
    path = tmp_path / "old.sqlite"
    _v0_ledger(path)
    con = ledger.connect(path)
    assert con.execute("PRAGMA user_version").fetchone()[0] == len(ledger._MIGRATIONS)
    claim = ledger.get_claim(con, "NF-9")
    assert claim is not None and claim.phase == "impl" and claim.wall_clock_s == 3600
    assert "events" in {r[0] for r in con.execute("SELECT name FROM sqlite_master")}


def test_a_second_connection_finds_the_ledger_migrated(tmp_path):
    path = tmp_path / "old.sqlite"
    _v0_ledger(path)
    first, second = ledger.connect(path), ledger.connect(path)
    assert second.execute("PRAGMA user_version").fetchone()[0] == len(ledger._MIGRATIONS)
    assert ledger.get_claim(first, "NF-9") is not None


def _ledger_at(path, version):
    raw = sqlite3.connect(path)
    raw.executescript(ledger._SCHEMA)
    for statements in ledger._MIGRATIONS[:version]:
        for sql in statements:
            raw.execute(sql)
    raw.execute(f"PRAGMA user_version = {version}")
    raw.commit()
    return raw


def test_events_written_before_the_stop_columns_read_null(tmp_path):
    path = tmp_path / "old.sqlite"
    raw = _ledger_at(path, 5)
    raw.execute(
        "INSERT INTO events (kind, issue_key, detail, at) VALUES ('ended', 'NF-1', 'crash: x', 1.0)"
    )
    raw.commit()
    raw.close()
    con = ledger.connect(path)
    [e] = events.unacked(con)
    assert (e.kind, e.detail, e.cause, e.stop_type) == ("ended", "crash: x", None, None)


def test_a_claim_keeps_its_sequence(con):
    c = make()
    c.sequence = ["tester", "executor"]
    ledger.insert_claim(con, c)
    claim = ledger.get_claim(con, "NF-1")
    assert claim is not None and claim.sequence == ["tester", "executor"]


def test_an_old_claim_gets_the_default_sequence(tmp_path):
    path = tmp_path / "l.sqlite"
    raw = sqlite3.connect(path)
    raw.executescript(ledger._SCHEMA)
    raw.execute(
        "INSERT INTO claims (issue_key, issue_id, repo, worktree, branch, phase, state, paths,"
        " depends_on, settings_json, started_at, wall_clock_s) VALUES ('NF-1', 'i', 'backend',"
        " '/wt/NF-1', 'agent/nf-1', 'impl', 'done', '[]', '[]', '{}', 0, 3600)"
    )
    raw.commit()
    raw.close()
    claim = ledger.get_claim(ledger.connect(path), "NF-1")
    assert claim is not None and claim.sequence == ["executor", "reviewer"]


def test_a_column_the_code_no_longer_knows_is_ignored_on_read(con):
    con.execute("ALTER TABLE claims ADD COLUMN retired TEXT")
    ledger.insert_claim(con, make())
    claim = ledger.get_claim(con, "NF-1")
    assert claim is not None and not hasattr(claim, "retired")


def test_the_escalation_offset_column_is_gone(tmp_path):
    path = tmp_path / "old.sqlite"
    _v0_ledger(path)
    con = ledger.connect(path)
    assert "escalation_offset" not in _columns(con, "claims")
    assert ledger.get_claim(con, "NF-9") is not None


def test_holds_upsert_list_and_remove(con):
    ledger.add_hold(con, "NF-1", "waiting on the API", 1.0)
    ledger.add_hold(con, "NF-1", "still waiting", 2.0)
    assert ledger.issue_holds(con) == {"NF-1": ("still waiting", 2.0)}
    assert ledger.remove_hold(con, "NF-1") is True
    assert ledger.remove_hold(con, "NF-1") is False
    assert ledger.issue_holds(con) == {}


def test_the_paths_column_is_gone(tmp_path):
    path = tmp_path / "old.sqlite"
    _v0_ledger(path)
    con = ledger.connect(path)
    assert "paths" not in _columns(con, "claims")
    claim = ledger.get_claim(con, "NF-9")
    assert claim is not None and claim.depends_on == []
