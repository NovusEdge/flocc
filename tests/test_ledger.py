import pytest

from linear_agents import ledger
from linear_agents.ledger import Claim


def make(key="NF-1", paths=("src/a/**",), state="running", worktree=None):
    return Claim(
        issue_key=key,
        issue_id=f"id-{key}",
        repo="backend",
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
