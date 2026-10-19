import pytest

from linear_agents.agentblock import (
    BlockError,
    IssueSettings,
    has_acceptance_criteria,
    parse_settings,
    settings_from_json,
    settings_to_json,
)

FULL = """Fix the invoice rounding.

```agent
model: opus
effort: high
wall_clock: 2h
paths: [src/billing/**, tests/billing/**]
depends_on: [NF-120]
mcp: [sentry]
```

## Acceptance criteria

- Totals round half-even
"""


def test_full_block():
    s = parse_settings(FULL)
    assert s == IssueSettings(
        model="opus",
        effort="high",
        wall_clock_s=7200,
        paths=("src/billing/**", "tests/billing/**"),
        depends_on=("NF-120",),
        mcp=("sentry",),
    )


def test_no_block_gives_defaults():
    assert parse_settings("just text") == IssueSettings()
    assert parse_settings(None) == IssueSettings()


def test_crlf_descriptions_parse():
    assert parse_settings(FULL.replace("\n", "\r\n")).wall_clock_s == 7200


@pytest.mark.parametrize(
    "line,msg",
    [("colour: red", "unknown keys"), ("wall_clock: soon", "wall_clock"), ("paths", "key: value")],
)
def test_bad_blocks(line, msg):
    with pytest.raises(BlockError, match=msg):
        parse_settings(f"```agent\n{line}\n```\n")


def test_a_huge_allow_number_is_a_block_error():
    with pytest.raises(BlockError, match="launches"):
        parse_settings(f"```agent\nallow: launches {'9' * 5000}\n```\n")


def test_unclosed_block_is_an_error():
    with pytest.raises(BlockError, match="closing"):
        parse_settings("```agent\nmodel: opus\n\n## Acceptance criteria\n\n- works\n")


@pytest.mark.parametrize(
    "text,expected",
    [
        (FULL, True),
        ("## Acceptance Criteria\n\n1. works\n", True),
        ("**Acceptance criteria**\n- works\n", True),
        ("**Acceptance criteria:**\n- works\n", True),
        ("## Acceptance criteria\n\nTBD\n", False),
        ("acceptance criteria are obvious", False),
        (None, False),
    ],
)
def test_acceptance_criteria(text, expected):
    assert has_acceptance_criteria(text) is expected


def test_json_round_trip():
    s = parse_settings(FULL)
    assert settings_from_json(settings_to_json(s)) == s


def test_allow_key_is_kept_raw_and_validated():
    s = parse_settings("```agent\nallow: launches 4, agent code-mapper\n```")
    assert s.allow == "launches 4, agent code-mapper"
    assert settings_from_json(settings_to_json(s)).allow == s.allow
    with pytest.raises(BlockError, match="allow"):
        parse_settings("```agent\nallow: launches lots\n```")
