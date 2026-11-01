import tomllib

import pytest

from flocc import settings
from flocc.cli import main

BASE = """team_key = "NF"
trigger_label = "claude"
state_root = "/s"
ledger_path = "/s/l.sqlite"
linear_key_file = "/s/k"
agent_gh_token_file = "/s/a"
publish_gh_token_file = "/s/p"
"""
TABLES = """
[repos.r]
path = "/r"
"""


def _file(tmp_path, top="", mode=0o640):
    path = tmp_path / "config.toml"
    path.write_text(BASE + top + TABLES)
    path.chmod(mode)
    return path


def _run(path, *args):
    return main(["--config", str(path), "config", *args])


def test_set_inserts_at_the_end_of_the_top_level_section(tmp_path):
    path = _file(tmp_path)
    assert _run(path, "set", "session_cap", "3") == 0
    assert path.read_text() == BASE + "session_cap = 3\n" + TABLES
    assert tomllib.loads(path.read_text())["session_cap"] == 3


def test_set_replaces_the_existing_line(tmp_path):
    path = _file(tmp_path, "session_cap = 2\ndigest_hour = 7\n")
    assert _run(path, "set", "session_cap", "4") == 0
    assert path.read_text() == BASE + "session_cap = 4\ndigest_hour = 7\n" + TABLES


def test_set_appends_when_there_are_no_tables(tmp_path):
    path = tmp_path / "config.toml"
    path.write_text(BASE.rstrip("\n") + '\n[repos.r]\npath = "/r"')
    assert _run(path, "set", "check_fix_rounds", "1") == 0
    assert path.read_text().endswith('path = "/r"\n')
    assert "check_fix_rounds = 1\n[repos.r]" in path.read_text()


def test_unset_removes_the_line_and_the_default_applies(tmp_path, capsys):
    path = _file(tmp_path, "session_cap = 5\n")
    assert _run(path, "unset", "session_cap") == 0
    assert "session_cap" not in path.read_text()
    capsys.readouterr()
    assert _run(path, "get", "session_cap") == 0
    assert capsys.readouterr().out == "2\n"


@pytest.mark.parametrize(
    ("key", "text", "want"),
    [
        ("session_cap", "3", 3),
        ("shared_uv_cache", "true", True),
        ("skip_unmapped", "false", False),
        ("five_hour_max_pct", "75.5", 75.5),
        ("five_hour_max_pct", "70", 70.0),
        ("review_model", "sonnet", "sonnet"),
        ("review_model", 'a "q" \\ é', 'a "q" \\ é'),
    ],
)
def test_values_round_trip_through_tomllib(tmp_path, key, text, want):
    path = _file(tmp_path)
    assert _run(path, "set", key, text) == 0
    got = tomllib.loads(path.read_text())[key]
    assert got == want
    assert type(got) is type(want)


def test_get_prints_bools_in_toml_case(tmp_path, capsys):
    path = _file(tmp_path, "shared_uv_cache = true\n")
    assert _run(path, "get", "shared_uv_cache") == 0
    assert capsys.readouterr().out == "true\n"


def test_list_shows_defaults_and_what_the_file_sets(tmp_path, capsys):
    path = _file(tmp_path, "session_cap = 3\n")
    assert _run(path, "list") == 0
    out = capsys.readouterr().out.splitlines()
    assert "session_cap = 3  (set)" in out
    assert "digest_hour = 18  (default)" in out
    assert 'review_model = "opus"  (default)' in out


def test_only_scalars_that_are_not_secrets_are_settable():
    keys = settings.settable()
    assert {"session_cap", "shared_uv_cache", "five_hour_max_pct", "review_model"} <= set(keys)
    for hidden in ("repos", "mcp_servers", "steer_authors", "linear_key_file", "agent_user"):
        assert hidden not in keys
    assert not [k for k in keys if "token" in k or "key" in k]


@pytest.mark.parametrize(
    ("args", "message"),
    [
        (("set", "nope", "1"), "unknown setting nope"),
        (("set", "repos", "1"), "unknown setting repos"),
        (("set", "linear_key_file", "x"), "unknown setting"),
        (("get", "nope"), "unknown setting nope"),
        (("unset", "nope"), "unknown setting nope"),
        (("set", "session_cap", "many"), "session_cap takes int"),
        (("set", "session_cap", "2.5"), "session_cap takes int"),
        (("set", "shared_uv_cache", "yes"), "shared_uv_cache takes bool"),
        (("set", "five_hour_max_pct", "nan"), "five_hour_max_pct takes float"),
        (("set", "review_model", "a\nb"), "control characters"),
    ],
)
def test_refusals_exit_2_with_one_line_and_leave_the_file(tmp_path, capsys, args, message):
    path = _file(tmp_path)
    before = path.read_text()
    assert _run(path, *args) == 2
    err = capsys.readouterr().err
    assert message in err
    assert len(err.splitlines()) == 1
    assert path.read_text() == before
    assert not (tmp_path / "config.toml.bak").exists()


def test_a_value_the_loader_rejects_leaves_the_file_and_makes_no_backup(tmp_path, capsys):
    path = _file(tmp_path)
    before = path.read_text()
    assert _run(path, "set", "session_cap", "0") == 2
    err = capsys.readouterr().err
    assert f"{path}: session_cap 0 must be >= 1" in err
    assert path.read_text() == before
    assert not (tmp_path / "config.toml.bak").exists()
    assert [p.name for p in tmp_path.iterdir()] == ["config.toml"]


def test_unsetting_a_required_key_is_refused_by_the_loader(tmp_path, capsys):
    path = _file(tmp_path)
    before = path.read_text()
    assert _run(path, "unset", "trigger_label") == 2
    assert "trigger_label" in capsys.readouterr().err
    assert path.read_text() == before


def test_success_keeps_the_old_file_as_bak_and_the_mode(tmp_path):
    path = _file(tmp_path, "session_cap = 2\n", mode=0o600)
    before = path.read_text()
    assert _run(path, "set", "session_cap", "3") == 0
    assert (tmp_path / "config.toml.bak").read_text() == before
    assert path.stat().st_mode & 0o777 == 0o600
    assert [p.name for p in tmp_path.iterdir()] == ["config.toml", "config.toml.bak"]


def test_tables_and_other_lines_are_untouched(tmp_path):
    top = "# kept\nsession_cap = 2  # trailing\nshared_uv_cache = true\n"
    path = _file(tmp_path, top)
    assert _run(path, "set", "digest_hour", "9") == 0
    assert _run(path, "set", "session_cap", "6") == 0
    text = path.read_text()
    assert text.endswith(TABLES)
    assert "# kept\nsession_cap = 6\nshared_uv_cache = true\ndigest_hour = 9\n" in text
