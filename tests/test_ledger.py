import sqlite3

import pytest

from flocc import ledger
from flocc.ledger import Claim


def make(key="NF-1", paths=("src/a/**",), state="running", worktree=None, repo="backend"):
    return Claim(
        issue_key=key,
        issue_id=f"id-{key}",
        repo=repo,
        worktree=worktree or f"/wt/{key}",
        branch=f"agent/{key.lower()}",
        phase="test",
        state=state,
        paths=list(paths),
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
    ledger.update_claim(con, "NF-1", phase="impl", paths=["src/a/**", "src/b.py"])
    got = ledger.get_claim(con, "NF-1")
    assert got is not None
    assert got.phase == "impl" and got.paths == ["src/a/**", "src/b.py"]
    assert [c.issue_key for c in ledger.live_claims(con)] == ["NF-1"]
    by_tree = ledger.claim_for_worktree(con, "/wt/NF-1")
    assert by_tree is not None and by_tree.issue_key == "NF-1"


@pytest.mark.parametrize(
    "a,b,overlap",
    [
        ("src/billing/**", "src/billing/tax.py", True),
        ("src/b*", "src/billing/**", True),
        ("src/bill.py", "src/billing/**", False),
        ("docs/**", "src/**", False),
        ("**", "anything", True),
    ],
)
def test_globs_overlap_cases(a, b, overlap):
    assert ledger.globs_overlap(a, b) is overlap
    assert ledger.globs_overlap(b, a) is overlap


def test_conflicts_exclude_self():
    mine, other = make("NF-1", ("src/a/**",)), make("NF-2", ("src/a/x.py",))
    assert ledger.conflicting_claims(["src/a/**"], [mine, other], exclude="NF-1") == {
        "src/a/**": "NF-2"
    }
    assert ledger.file_conflicts(["src/a/x.py", "README.md"], [mine, other], exclude="NF-1") == {
        "src/a/x.py": "NF-2"
    }


def test_file_under_a_claimed_directory_conflicts():
    prefix = make("NF-2", ("src/b*",))
    assert ledger.file_conflicts(["src/billing/tax.py", "src/a.py"], [prefix], exclude="NF-1") == {
        "src/billing/tax.py": "NF-2"
    }


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


def test_add_paths_extends_a_claim_or_reports_the_owner(con):
    ledger.insert_claim(con, make("NF-1", ("a/**",)))
    ledger.insert_claim(con, make("NF-2", ("src/billing/**",)))
    assert ledger.add_paths(con, "NF-1", ["docs/**", "a/**"]) == {}
    claim = ledger.get_claim(con, "NF-1")
    assert claim and claim.paths == ["a/**", "docs/**"]
    assert ledger.add_paths(con, "NF-1", ["src/b*", "x/**"]) == {"src/b*": "NF-2"}
    claim = ledger.get_claim(con, "NF-1")
    assert claim and claim.paths == ["a/**", "docs/**"]
    with pytest.raises(KeyError):
        ledger.add_paths(con, "NF-9", ["z/**"])


def test_add_paths_ignores_claims_in_other_repos(con):
    ledger.insert_claim(con, make("NF-1", ("a/**",)))
    ledger.insert_claim(con, make("NF-2", ("docs/verification.md",), repo="web"))
    assert ledger.add_paths(con, "NF-1", ["docs/verification.md"]) == {}
    ledger.insert_claim(con, make("NF-3", ("docs/**",)))
    assert ledger.add_paths(con, "NF-1", ["docs/x.md"]) == {"docs/x.md": "NF-3"}


def test_is_shared_matches_exact_names_and_globs():
    shared = ("docs/verification.md", "logs/*.log")
    assert ledger.is_shared("docs/verification.md", shared)
    assert ledger.is_shared("logs/a.log", shared)
    assert not ledger.is_shared("docs/other.md", shared)
    assert not ledger.is_shared("logs/sub/a.log", shared)
    assert not ledger.is_shared("docs/verification.md", ())


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
    assert {"id", "kind", "issue_key", "repo", "detail", "at", "printed_at", "acked_at"} == (
        _columns(con, "events")
    )
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
