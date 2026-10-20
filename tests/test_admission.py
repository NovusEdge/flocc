from dataclasses import replace

from linear_agents.admission import admit
from linear_agents.agentblock import IssueSettings
from linear_agents.config import load
from linear_agents.usage import Usage
from tests.paths import EXAMPLE
from tests.test_ledger import make

CFG = load(EXAMPLE)
OK = Usage(10, 10, observed_at=1000)
DEFAULT = IssueSettings()


def run(settings=DEFAULT, live=(), usage=OK, deps=None, now=1100, free=None, repo="backend"):
    return admit("NF-9", repo, settings, list(live), usage, deps or {}, CFG, now, free)


def test_admits_when_clear():
    assert run().ok


def test_low_disk_holds_the_start():
    d = run(free=1.54)
    assert not d.ok and d.reason == "disk free 1.5 GB < min_free_disk_gb"
    assert run(free=2.0).ok


def test_cap():
    d = run(live=[make("NF-1", ()), make("NF-2", ())])
    assert not d.ok and "cap" in d.reason


def test_usage_thresholds():
    assert not run(usage=Usage(80, 10, 1000)).ok
    assert not run(usage=Usage(10, 80, 1000)).ok


def test_unknown_usage_admits_only_when_idle():
    assert run(usage=None).ok
    assert not run(usage=None, live=[make("NF-1", ())]).ok
    stale = Usage(10, 10, observed_at=0)
    assert not run(usage=stale, live=[make("NF-1", ())], now=10_000).ok
    assert run(usage=stale, now=10_000).ok


def test_stale_reading_over_a_limit_holds_when_idle():
    d = run(usage=Usage(10, 95, observed_at=0), now=10_000)
    assert not d.ok and "weekly" in d.reason


def test_dependencies_must_be_completed():
    s = IssueSettings(depends_on=("NF-120",))
    assert not run(settings=s, deps={"NF-120": "started"}).ok
    assert run(settings=s, deps={"NF-120": "completed"}).ok


def test_path_conflict():
    s = IssueSettings(paths=("src/b*",))
    d = run(settings=s, live=[make("NF-1", ("src/billing/**",))])
    assert not d.ok and "NF-1" in d.reason


def test_a_shared_path_in_the_agent_block_does_not_hold_a_start():
    backend = replace(CFG.repos["backend"], shared_paths=("docs/verification.md",))
    cfg = replace(CFG, repos={**CFG.repos, "backend": backend})
    s = IssueSettings(paths=("docs/verification.md", "src/a.py"))
    live = [make("NF-1", ("docs/verification.md", "src/b.py"))]
    assert admit("NF-9", "backend", s, live, OK, {}, cfg, 1100).ok
    assert not run(settings=s, live=live).ok  # not shared in the default config
    live = [make("NF-1", ("docs/verification.md", "src/a.py"))]
    assert not admit("NF-9", "backend", s, live, OK, {}, cfg, 1100).ok


def test_a_claim_in_another_repo_does_not_hold_a_start():
    s = IssueSettings(paths=("docs/verification.md",))
    live = [make("NF-1", ("docs/verification.md",), repo="web")]
    assert run(settings=s, live=live).ok
    assert not run(settings=s, live=live, repo="web").ok
