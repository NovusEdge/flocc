import pytest

from flocc.sequence import ARCHETYPE, DEFAULT, PHASE, SequenceError, parse_sequence

CRITERIA = "\n## Acceptance criteria\n\n- works\n"


def test_no_run_line_is_the_default():
    assert parse_sequence("Do it." + CRITERIA) == DEFAULT == ("executor", "reviewer")
    assert parse_sequence(None) == DEFAULT


@pytest.mark.parametrize(
    "line, want",
    [
        ("Run: tester, executor, reviewer", ("tester", "executor", "reviewer")),
        ("Run: executor", ("executor",)),
        ("Run: tester, executor", ("tester", "executor")),
    ],
)
def test_a_run_line_overrides_it(line, want):
    assert parse_sequence(f"Do it.\n\n{line}\n{CRITERIA}") == want


def test_names_are_trimmed_and_case_insensitive():
    assert parse_sequence("run: Tester , EXECUTOR\r\n") == ("tester", "executor")


@pytest.mark.parametrize(
    "line, words",
    [
        ("Run: executor, planner", "unknown archetype: planner"),
        ("Run: just check", "unknown archetype: just check"),
        ("Run: executor, fixer", "never lists fixer"),
        ("Run: executor, executor", "twice"),
        ("Run: reviewer, executor", "order"),
        ("Run: tester", "order"),
        ("Run: executor,", "empty"),
    ],
)
def test_a_bad_run_line_is_refused(line, words):
    with pytest.raises(SequenceError, match=words):
        parse_sequence(line)


def test_two_run_lines_are_refused():
    with pytest.raises(SequenceError, match="more than one"):
        parse_sequence("Run: executor\nRun: tester, executor\n")


def test_archetypes_map_onto_the_ledger_phases():
    assert PHASE == {"tester": "test", "executor": "impl", "fixer": "fix", "reviewer": "review"}
    assert ARCHETYPE["impl"] == "executor"
