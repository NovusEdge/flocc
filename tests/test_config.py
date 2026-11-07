from pathlib import Path

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
    assert cfg.tmp_max_pct == 80.0
    assert cfg.min_available_mem_gb == 1.0
    assert cfg.shared_uv_cache is False
    backend = cfg.repos["backend"]
    assert (backend.check_min_mem_gb, backend.check_memory_max, backend.check_memory_swap_max) == (
        2.0,
        "6G",
        "1G",
    )
    assert not hasattr(backend, "check_memory_high")
    assert cfg.check_wait_s is None and cfg.memory_wait_s == 2 * cfg.check_timeout_s
    assert not hasattr(cfg, "check_cap")
    assert cfg.state_root.is_absolute()
    assert cfg.mcp_servers["sentry"]["type"] == "http"
    assert cfg.agent_plugins_file is None


def test_layout_keys_in_an_old_config_are_ignored_with_a_warning(tmp_path, capsys):
    path = tmp_path / "config.toml"
    old = 'worktree_root = "~/worktrees"\nplugin_dir = "~/flocc/plugin"\n'
    path.write_text(old + EXAMPLE.read_text())
    cfg = load(path)
    assert cfg.worktree_root == Path("/home/flocc-agent/worktrees")
    assert cfg.plugin_dir == Path("/opt/flocc/plugin")
    assert cfg.agent_token_file == Path("/home/flocc-agent/.config/flocc/gh-agent.token")
    err = capsys.readouterr().err
    assert "worktree_root is fixed by the two-user layout" in err and "plugin_dir" in err


def test_effort_for_maps_phase_to_config():
    cfg = load(EXAMPLE)
    assert cfg.effort_for("test") == "high"
    assert cfg.effort_for("impl") == "medium"
    assert cfg.effort_for("review") == "medium"
    # The post-check fix session runs the fixer skill, so it takes the executor level.
    assert cfg.effort_for("fix") == "medium"


def test_bad_effort_level_is_an_error(tmp_path):
    p = tmp_path / "c.toml"
    p.write_text('impl_effort = "mediium"\n' + EXAMPLE.read_text())
    with pytest.raises(ValueError, match="impl_effort.*mediium"):
        load(p)


@pytest.mark.parametrize(
    "line",
    [
        "digest_hour = 24",
        "session_cap = 0",
        "five_hour_max_pct = 150.0",
        "weekly_max_pct = -1.0",
        "tmp_max_pct = 101.0",
        "min_available_mem_gb = -1.0",
        "comment_fix_max = 11",
        "comment_fix_max = -1",
    ],
)
def test_out_of_range_number_is_an_error(tmp_path, line):
    p = tmp_path / "c.toml"
    p.write_text(line + "\n" + EXAMPLE.read_text())
    with pytest.raises(ValueError, match=line.split(" =")[0]):
        load(p)


def test_comment_fix_max_defaults_to_three():
    assert load(EXAMPLE).comment_fix_max == 3


@pytest.mark.parametrize("key", ["steer_authors", "github_steer_logins"])
def test_a_bad_login_list_is_an_error(tmp_path, key):
    p = tmp_path / "c.toml"
    p.write_text(f'{key} = "just-one-id"\n' + EXAMPLE.read_text())
    with pytest.raises(ValueError, match=key):
        load(p)


def test_github_steer_logins_default_to_none_and_load_as_a_tuple(tmp_path):
    assert load(EXAMPLE).github_steer_logins == ()
    p = tmp_path / "c.toml"
    p.write_text('github_steer_logins = ["NovusEdge"]\n' + EXAMPLE.read_text())
    assert load(p).github_steer_logins == ("NovusEdge",)


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


def test_a_config_without_digest_issue_loads(tmp_path):
    p = tmp_path / "c.toml"
    p.write_text(EXAMPLE.read_text().replace('digest_issue = "NF-1"\n', ""))
    assert load(p).digest_issue == ""


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


def test_retired_notice_keys_are_ignored(tmp_path, capsys):
    path = tmp_path / "config.toml"
    path.write_text("idle_escalate_s = 60.0\nstall_escalate_s = 60.0\n" + EXAMPLE.read_text())
    load(path)
    err = capsys.readouterr().err
    assert "idle_escalate_s is no longer used" in err and "stall_escalate_s" in err


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


def _with(tmp_path, top="", repo=""):
    text = EXAMPLE.read_text().replace('base_branch = "main"\n', f'base_branch = "main"\n{repo}', 1)
    p = tmp_path / "config.toml"
    p.write_text(top + text)
    return p


def test_check_wait_s_overrides_the_default_wait(tmp_path):
    cfg = load(_with(tmp_path, top="check_wait_s = 600\n"))
    assert cfg.memory_wait_s == 600


def test_a_negative_check_min_mem_gb_is_rejected(tmp_path):
    with pytest.raises(ValueError, match="check_min_mem_gb"):
        load(_with(tmp_path, repo="check_min_mem_gb = -1\n"))


@pytest.mark.parametrize("value", ["6G", "512M", "1T", "100K", "123", "infinity"])
def test_systemd_sizes_are_accepted(tmp_path, value):
    cfg = load(
        _with(tmp_path, repo=f'check_memory_max = "{value}"\ncheck_memory_swap_max = "{value}"\n')
    )
    assert cfg.repos["backend"].check_memory_max == value


@pytest.mark.parametrize("value", ["6GB", "5.5G", "", "-1G", "G", "lots", "50%"])
def test_other_memory_strings_are_rejected(tmp_path, value):
    for key in ("check_memory_max", "check_memory_swap_max"):
        with pytest.raises(ValueError, match=key):
            load(_with(tmp_path, repo=f'{key} = "{value}"\n'))
