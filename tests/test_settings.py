import dataclasses
import tomllib

import pytest

from flocc import settings
from flocc.cli import main
from flocc.config import Config

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
        ("tmp_max_pct", "75.5", 75.5),
        ("tmp_max_pct", "70", 70.0),
        ("digest_hour", "0", 0),
        ("session_cap", "6", 6),
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
    assert not [line for line in out if line.split()[0] not in settings.SETTABLE]
    assert len(out) == len(settings.SETTABLE)


def test_cardinal_file_on_is_a_settable_bool():
    assert settings.SETTABLE["cardinal_file_on"][0] is bool


def test_comment_fix_max_starts_paid_sessions_so_it_is_not_settable():
    assert "comment_fix_max" in settings.NOT_SETTABLE
    with pytest.raises(settings.SettingError, match="not settable"):
        settings.check("comment_fix_max")


def test_github_steer_logins_start_paid_sessions_so_they_are_not_settable():
    assert "github_steer_logins" in settings.NOT_SETTABLE
    with pytest.raises(settings.SettingError, match="not settable"):
        settings.check("github_steer_logins")


def test_every_config_field_is_decided():
    fields = {f.name for f in dataclasses.fields(Config)}
    assert not settings.SETTABLE.keys() & settings.NOT_SETTABLE
    assert fields == settings.SETTABLE.keys() | settings.NOT_SETTABLE
    for key, (kind, _, _) in settings.SETTABLE.items():
        assert next(f.type for f in dataclasses.fields(Config) if f.name == key) is kind


@pytest.mark.parametrize(
    ("args", "message"),
    [
        (("set", "nope", "1"), "nope is not settable"),
        (("set", "repos", "1"), "repos is not settable"),
        (("set", "trigger_label", "x"), "trigger_label is not settable"),
        (("set", "review_model", "x"), "review_model is not settable"),
        (("set", "five_hour_max_pct", "90"), "five_hour_max_pct is not settable"),
        (("get", "nope"), "nope is not settable"),
        (("unset", "trigger_label"), "trigger_label is not settable"),
        (("set", "session_cap", "many"), "session_cap takes int"),
        (("set", "session_cap", "2.5"), "session_cap takes int"),
        (("set", "session_cap", "0"), "session_cap 0 is outside 1..6"),
        (("set", "session_cap", "7"), "session_cap 7 is outside 1..6"),
        (("set", "session_cap", "-1"), "session_cap -1 is outside 1..6"),
        (("set", "check_fix_rounds", "4"), "outside 0..3"),
        (("set", "tmp_max_pct", "0.5"), "outside 1..100"),
        (("set", "shared_uv_cache", "yes"), "shared_uv_cache takes bool"),
        (("set", "tmp_max_pct", "nan"), "tmp_max_pct takes float"),
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


def test_a_result_the_loader_rejects_leaves_the_file_and_makes_no_backup(tmp_path, capsys):
    path = _file(tmp_path, 'tester_effort = "bogus"\n')
    before = path.read_text()
    assert _run(path, "set", "session_cap", "3") == 2
    assert f"{path}: tester_effort 'bogus' is not one of" in capsys.readouterr().err
    assert path.read_text() == before
    assert [p.name for p in tmp_path.iterdir()] == ["config.toml"]


def test_a_symlinked_bak_is_replaced_not_written_through(tmp_path):
    path = _file(tmp_path, "session_cap = 2\n")
    victim = tmp_path / "victim"
    victim.write_text("keep")
    (tmp_path / "config.toml.bak").symlink_to(victim)
    assert _run(path, "set", "session_cap", "3") == 0
    assert victim.read_text() == "keep"
    bak = tmp_path / "config.toml.bak"
    assert not bak.is_symlink()
    assert "session_cap = 2" in bak.read_text()


@pytest.mark.parametrize("bad", ["a\nb", "x\x7f", "\ud800"])
def test_render_refuses_strings_toml_or_utf8_cannot_hold(bad):
    with pytest.raises(settings.SettingError):
        settings.render(bad)


def test_render_quotes_strings_so_tomllib_reads_them_back():
    text = 'a "q" \\ é'
    assert tomllib.loads(f"k = {settings.render(text)}")["k"] == text


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
