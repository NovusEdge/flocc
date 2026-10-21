import pytest

from flocc.grants import (
    GrantError,
    Grants,
    add_comment,
    add_judge,
    allow_lines,
    for_phase,
    judge_grants,
    merge,
    parse_allow,
    set_block,
)


def test_parse_allow_reads_every_item():
    g = parse_allow("launches 10, agent general-purpose, workflow big-build, fix-rounds 2")
    assert g == Grants(
        launches=10, agents=("general-purpose",), workflows=("big-build",), fix_rounds=2
    )


@pytest.mark.parametrize(
    "bad", ["launches ten", "launches ²", "agents x", "fix-rounds", "workflow"]
)
def test_parse_allow_errors_name_the_item(bad):
    with pytest.raises(GrantError, match=bad.split()[0]) as exc:
        parse_allow(bad)
    assert bad in str(exc.value)


@pytest.mark.parametrize("word", ["launches", "fix-rounds"])
def test_a_huge_number_is_a_grant_error_not_a_plain_value_error(word):
    with pytest.raises(GrantError, match=word):
        parse_allow(f"{word} {'9' * 5000}")


def test_allow_lines_only_at_line_start():
    body = "thanks!\n  Allow: launches 4\nplease allow: agent x\nallow:agent code-mapper"
    assert allow_lines(body) == ["launches 4", "agent code-mapper"]


def test_merge_later_numbers_win_and_names_union():
    a = Grants(launches=4, agents=("a",), fix_rounds=2)
    b = Grants(launches=8, agents=("b", "a"))
    assert merge(a, b) == Grants(launches=8, agents=("a", "b"), fix_rounds=2)


def test_missing_or_corrupt_file_is_no_grants(tmp_path):
    assert for_phase(tmp_path / "none.json", "impl") == Grants()
    bad = tmp_path / "grants.json"
    bad.write_text("{half")
    assert for_phase(bad, "impl") == Grants()


def test_block_launches_apply_to_impl_only(tmp_path):
    p = tmp_path / "grants.json"
    set_block(p, Grants(launches=14, agents=("code-mapper",)))
    assert for_phase(p, "impl") == Grants(launches=14, agents=("code-mapper",))
    assert for_phase(p, "test") == Grants(agents=("code-mapper",))


def test_comment_launches_are_scoped_to_their_phase_and_beat_the_block(tmp_path):
    p = tmp_path / "grants.json"
    set_block(p, Grants(launches=10, fix_rounds=1))
    add_comment(p, Grants(launches=16, fix_rounds=2), "impl")
    add_comment(p, Grants(launches=3), "fix")
    set_block(p, Grants(launches=10, fix_rounds=1))  # a relaunch rewrites the block
    assert for_phase(p, "impl") == Grants(launches=16, fix_rounds=2)
    assert for_phase(p, "fix").launches == 3
    assert for_phase(p, "test").launches is None


def test_a_command_grant_takes_the_rest_of_the_line_commas_included():
    g = parse_allow("command npx playwright test --grep 'a, b'")
    assert g == Grants(commands=("npx playwright test --grep 'a, b'",))


def test_a_command_grant_needs_a_command():
    with pytest.raises(GrantError, match="command"):
        parse_allow("command   ")


def test_command_grants_from_comments_apply_to_every_phase(tmp_path):
    p = tmp_path / "grants.json"
    add_comment(p, parse_allow("command make e2e"), "impl")
    add_comment(p, parse_allow("command make lint"), "impl")
    assert for_phase(p, "fix").commands == ("make e2e", "make lint")


def test_judge_grants_are_kept_apart_and_deduplicated(tmp_path):
    p = tmp_path / "grants.json"
    add_judge(p, "npx playwright install", "npx playwright")
    add_judge(p, "npx playwright install", None)
    assert judge_grants(p) == (("npx playwright install",), ("npx playwright",))
    assert for_phase(p, "impl").commands == ()


def test_judge_grants_survive_a_corrupt_section(tmp_path):
    p = tmp_path / "grants.json"
    p.write_text('{"judge": ["not", "a", "table"]}')
    assert judge_grants(p) == ((), ())
