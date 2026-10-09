from linear_agents.admission import admit
from linear_agents.agentblock import IssueSettings
from linear_agents.config import load
from linear_agents.usage import Usage
from tests.paths import EXAMPLE
from tests.test_ledger import make

CFG = load(EXAMPLE)
OK = Usage(10, 10, observed_at=1000)
DEFAULT = IssueSettings()


def run(settings=DEFAULT, live=(), usage=OK, deps=None, now=1100):
    return admit("NF-9", settings, list(live), usage, deps or {}, CFG, now)


def test_admits_when_clear():
    assert run().ok


def test_cap():
    d = run(live=[make("NF-1", ()), make("NF-2", ())])
    assert not d.ok and "cap" in d.reason


def test_usage_thresholds():
    assert not run(usage=Usage(60, 10, 1000)).ok
    assert not run(usage=Usage(10, 50, 1000)).ok


def test_unknown_usage_admits_only_when_idle():
    assert run(usage=None).ok
    assert not run(usage=None, live=[make("NF-1", ())]).ok
    stale = Usage(10, 10, observed_at=0)
    assert not run(usage=stale, live=[make("NF-1", ())], now=10_000).ok


def test_dependencies_must_be_completed():
    s = IssueSettings(depends_on=("NF-120",))
    assert not run(settings=s, deps={"NF-120": "started"}).ok
    assert run(settings=s, deps={"NF-120": "completed"}).ok


def test_path_conflict():
    s = IssueSettings(paths=("src/b*",))
    d = run(settings=s, live=[make("NF-1", ("src/billing/**",))])
    assert not d.ok and "NF-1" in d.reason
