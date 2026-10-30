import pytest

from flocc.config import load, trailer_enabled
from tests.paths import EXAMPLE


def test_example_loads_with_defaults():
    cfg = load(EXAMPLE)
    assert cfg.team_key == "NF"
    assert cfg.session_cap == 2
    assert cfg.digest_hour == 18
    assert cfg.default_wall_clock_s == 10800
    assert cfg.review_wall_clock_s == 3600
    assert cfg.idle_escalate_s == 1800.0
    assert cfg.review_model == "opus"
    assert cfg.five_hour_max_pct == 80.0
    assert cfg.weekly_max_pct == 80.0
    assert cfg.tester_effort == "high"
    assert cfg.impl_effort == "medium"
    assert cfg.review_effort == "medium"
    assert cfg.repos["backend"].check == "just check"
    assert cfg.repos["backend"].draft is True
    assert cfg.check_timeout_s == 1800
    assert cfg.setup_timeout_s == 900
    assert cfg.min_free_disk_gb == 2.0
    assert cfg.shared_uv_cache is False
    assert cfg.state_root.is_absolute()
    assert cfg.mcp_servers["sentry"]["type"] == "http"
    assert cfg.agent_plugins_file is None
    assert cfg.installed_plugins_file.name == "installed_plugins.json"


def test_effort_for_maps_phase_to_config():
    cfg = load(EXAMPLE)
    assert cfg.effort_for("test") == "high"
    assert cfg.effort_for("impl") == "medium"
    assert cfg.effort_for("review") == "medium"
    # The post-check fix session runs the implementer skill, so it takes the implementer level.
    assert cfg.effort_for("fix") == "medium"


def test_bad_effort_level_is_an_error(tmp_path):
    p = tmp_path / "c.toml"
    p.write_text('impl_effort = "mediium"\n' + EXAMPLE.read_text())
    with pytest.raises(ValueError, match="impl_effort.*mediium"):
        load(p)


@pytest.mark.parametrize(
    "line",
    ["digest_hour = 24", "session_cap = 0", "five_hour_max_pct = 150.0", "weekly_max_pct = -1.0"],
)
def test_out_of_range_number_is_an_error(tmp_path, line):
    p = tmp_path / "c.toml"
    p.write_text(line + "\n" + EXAMPLE.read_text())
    with pytest.raises(ValueError, match=line.split(" =")[0]):
        load(p)


def test_bad_steer_authors_is_an_error(tmp_path):
    p = tmp_path / "c.toml"
    p.write_text('steer_authors = "just-one-id"\n' + EXAMPLE.read_text())
    with pytest.raises(ValueError, match="steer_authors"):
        load(p)


def test_a_retired_repo_key_is_ignored_with_a_warning(tmp_path, capsys):
    p = tmp_path / "config.toml"
    table = "[repos.backend]"
    p.write_text(EXAMPLE.read_text().replace(table, f'{table}\nshared_paths = ["docs/log.md"]'))
    assert load(p).repos["backend"].base_branch == "main"
    assert "repos.backend.shared_paths is no longer used" in capsys.readouterr().err


def test_missing_repos_is_an_error(tmp_path):
    p = tmp_path / "c.toml"
    p.write_text(EXAMPLE.read_text().split("[repos.backend]")[0])
    with pytest.raises(ValueError, match="no \\[repos"):
        load(p)


def test_missing_digest_issue_is_an_error(tmp_path):
    p = tmp_path / "c.toml"
    p.write_text(EXAMPLE.read_text().replace('digest_issue = "NF-1"\n', ""))
    with pytest.raises(ValueError, match="digest_issue"):
        load(p)


def test_unknown_key_is_an_error(tmp_path):
    p = tmp_path / "c.toml"
    p.write_text("sesion_cap = 3\n" + EXAMPLE.read_text())
    with pytest.raises(TypeError, match="sesion_cap"):
        load(p)


def test_repo_draft_can_be_turned_off_and_unknown_repo_keys_fail(tmp_path):
    p = tmp_path / "c.toml"
    p.write_text(
        EXAMPLE.read_text().replace("[repos.backend]\n", "[repos.backend]\ndraft = false\n")
    )
    assert load(p).repos["backend"].draft is False
    p.write_text(EXAMPLE.read_text().replace("[repos.backend]\n", "[repos.backend]\ndarft = 1\n"))
    with pytest.raises(TypeError, match="darft"):
        load(p)


def test_commit_trailer_is_on_unless_a_repo_overrides_it(tmp_path):
    cfg = load(EXAMPLE)
    repo = cfg.repos["backend"]
    assert cfg.commit_trailer is True and repo.commit_trailer is None
    assert trailer_enabled(cfg, repo)
    p = tmp_path / "c.toml"
    p.write_text(EXAMPLE.read_text().replace('base_branch = "main"\n', "commit_trailer = false\n"))
    off = load(p)
    assert not trailer_enabled(off, off.repos["backend"])
    p.write_text("commit_trailer = false\n" + EXAMPLE.read_text())
    global_off = load(p)
    assert not trailer_enabled(global_off, global_off.repos["backend"])


def test_a_leftover_permissions_file_is_ignored_with_a_warning(tmp_path, capsys):
    path = tmp_path / "config.toml"
    path.write_text(EXAMPLE.read_text())
    (tmp_path / "permissions.toml").write_text('allow = ["Bash(just:*)"]\n')
    cfg = load(path)
    assert not hasattr(cfg, "permission_allow")
    assert "permissions.toml is no longer read" in capsys.readouterr().err


def test_context_restart_pct_is_retired(tmp_path, capsys):
    p = tmp_path / "config.toml"
    p.write_text("context_restart_pct = 60.0\n" + EXAMPLE.read_text())
    cfg = load(p)
    assert not hasattr(cfg, "context_restart_pct")
    assert "context_restart_pct is no longer used" in capsys.readouterr().err


def test_retired_top_level_keys_are_ignored_with_a_warning(tmp_path, capsys):
    p = tmp_path / "config.toml"
    kept = [ln for ln in EXAMPLE.read_text().splitlines() if not ln.startswith("ntfy_url")]
    p.write_text('ntfy_url = "https://ntfy.sh/x"\n' + "\n".join(kept) + "\n")
    assert load(p).team_key == "NF"
    assert "ntfy_url is no longer used" in capsys.readouterr().err
