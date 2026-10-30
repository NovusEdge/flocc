import re

DEFAULT = ("executor", "reviewer")
# The service inserts fixers itself; a Run: line never names one.
PHASE = {"tester": "test", "executor": "impl", "fixer": "fix", "reviewer": "review"}
ARCHETYPE = {phase: name for name, phase in PHASE.items()}
_LISTABLE = ("tester", "executor", "reviewer")
_ORDERS = frozenset(
    {
        ("executor",),
        ("executor", "reviewer"),
        ("tester", "executor"),
        ("tester", "executor", "reviewer"),
    }
)
_LINE = re.compile(r"^[ \t]*run[ \t]*:(.*)$", re.IGNORECASE | re.MULTILINE)


class SequenceError(ValueError):
    pass


def parse_sequence(description: str | None) -> tuple[str, ...]:
    lines = _LINE.findall((description or "").replace("\r\n", "\n"))
    if not lines:
        return DEFAULT
    if len(lines) > 1:
        raise SequenceError("the description has more than one Run: line")
    raw = lines[0].strip()
    names = tuple(n.strip().lower() for n in raw.split(","))
    if "" in names:
        raise SequenceError(f"Run: {raw} has an empty name")
    if "fixer" in names:
        raise SequenceError("Run: never lists fixer; the service adds fixers itself")
    unknown = [n for n in names if n not in _LISTABLE]
    if unknown:
        raise SequenceError(
            f"Run: names an unknown archetype: {', '.join(unknown)}"
            " (use tester, executor and reviewer)"
        )
    if len(set(names)) != len(names):
        raise SequenceError("Run: names an archetype twice")
    if names not in _ORDERS:
        raise SequenceError(
            "Run: needs executor, optionally after tester and before reviewer, in that order"
        )
    return names
