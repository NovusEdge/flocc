import ast
from pathlib import Path

import pytest

from flocc import causes, control, supervisor


def test_every_cause_has_one_of_the_four_types():
    assert causes.TYPES == {"transient", "agent", "owner", "repo"}
    assert set(causes.CAUSES.values()) <= causes.TYPES


def test_the_table_matches_the_spec():
    assert causes.CAUSES == {
        "publish-failed": "transient",
        "crash": "agent",
        "idle": "agent",
        "timed-out": "agent",
        "check-failed": "agent",
        "no-changes": "agent",
        "needs-owner": "owner",
        "check-cap": "owner",
        "tests-touched": "owner",
        "no-tests": "owner",
        "branch-moved": "owner",
        "rebase-conflict": "owner",
        "base-broken": "repo",
    }


def test_resumable_is_the_agent_stops_and_needs_owner():
    agent = {c for c, t in causes.CAUSES.items() if t == causes.AGENT}
    assert control.RESUMABLE == agent | {"needs-owner"}
    assert control.RESUMABLE == {
        "needs-owner",
        "check-failed",
        "no-changes",
        "idle",
        "crash",
        "timed-out",
    }


def test_an_unknown_cause_is_refused():
    assert causes.type_of("crash") == "agent"
    with pytest.raises(ValueError, match="unknown stop cause 'crashed'"):
        causes.type_of("crashed")


def _block_causes() -> set[str]:
    """Every string literal in the cause argument of a _block call in supervisor.py."""
    tree = ast.parse(Path(supervisor.__file__).read_text())
    found = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Call) and getattr(node.func, "id", None) == "_block":
            found |= {
                n.value
                for n in ast.walk(node.args[2])
                if isinstance(n, ast.Constant) and isinstance(n.value, str)
            }
    return found


def test_every_block_cause_in_the_source_is_in_the_table():
    # timed-out is emitted by _timed_out and base-broken by canary.mark, not through _block.
    assert _block_causes() == set(causes.CAUSES) - {"timed-out", "base-broken"}
