import json

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
    assert cfg.context_restart_pct == 60.0
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


def test_shared_paths_load_per_repo(tmp_path):
    assert load(EXAMPLE).repos["backend"].shared_paths == ()
    p = tmp_path / "c.toml"
    table = '[repos.backend]\npath = "~/src/backend"'
    p.write_text(EXAMPLE.read_text().replace(table, f'{table}\nshared_paths = ["docs/log.md"]'))
    assert load(p).repos["backend"].shared_paths == ("docs/log.md",)
    p.write_text(EXAMPLE.read_text().replace(table, f'{table}\nshared_paths = "docs/log.md"'))
    with pytest.raises(ValueError, match="shared_paths"):
        load(p)


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


def with_permissions(tmp_path, text):
    config = tmp_path / "config.toml"
    config.write_text(EXAMPLE.read_text())
    (tmp_path / "permissions.toml").write_text(text)
    return config


def test_the_permissions_file_beside_the_config_is_loaded(tmp_path):
    cfg = load(
        with_permissions(
            tmp_path,
            'allow = ["Bash(npx playwright install)"]\n'
            'environment = ["Nothing on this VM is production."]\n'
            'judge_model = "sonnet"\n',
        )
    )
    assert cfg.permission_allow == ("Bash(npx playwright install)",)
    assert cfg.permission_environment == ("Nothing on this VM is production.",)
    assert (cfg.judge_model, cfg.judge_max_per_run) == ("sonnet", 30)


def test_no_permissions_file_means_no_rules_and_no_judge():
    cfg = load(EXAMPLE)
    assert (cfg.permission_allow, cfg.permission_environment, cfg.judge_model) == ((), (), "")


@pytest.mark.parametrize(
    "rule",
    [
        "Bash(python:*)",
        "Bash(sh *)",
        "Bash(bash -c ls)",
        "Bash(env:*)",
        "Bash(xargs *)",
        "Bash",
        "Bash()",
        "Bash(*)",
        "Bash(:*)",
        "Bash(*:*)",
        "Bash( python *)",
        "Bash(/usr/bin/python3 *)",
        "Bash(FOO=1 python *)",
        "Bash(FOO=1 *)",
        "Bash(python3.12 *)",
        "Bash(sh\\t*)",
        "Bash(Python:*)",
    ],
)
def test_an_allow_rule_that_wildcards_an_interpreter_is_refused(tmp_path, rule):
    with pytest.raises(ValueError, match="interpreter"):
        load(with_permissions(tmp_path, f'allow = ["{rule}"]\n'))


@pytest.mark.parametrize(
    "rule",
    [
        "Bash(py* *)",
        "Bash(pyth* *)",
        "Bash(?ython *)",
        "Bash(p*:*)",
        "Bash([a-z]* *)",
        "Bash(/usr/bin/* *)",
        'Bash("python" *)',
        "Bash('python' *)",
        'Bash(FOO="a b" python *)',
        "Bash(pyth\\on *)",
    ],
)
def test_a_glob_or_quote_in_the_program_token_is_refused(tmp_path, rule):
    with pytest.raises(ValueError, match="interpreter"):
        load(with_permissions(tmp_path, f"allow = [{json.dumps(rule)}]\n"))


@pytest.mark.parametrize(
    "rule",
    ["Bash(npx playwright install)", "Bash(uv run pytest *)", "Bash(git status:*)", "Read(*)"],
)
def test_an_ordinary_allow_rule_is_accepted(tmp_path, rule):
    cfg = load(with_permissions(tmp_path, f'allow = ["{rule}"]\n'))
    assert cfg.permission_allow == (rule,)


@pytest.mark.parametrize(
    "text, word",
    [
        ("alow = []\n", "alow"),
        ('environment = "x"\n', "environment"),
        ("judge_max_per_run = 0\n", "judge_max_per_run"),
    ],
)
def test_a_bad_permissions_file_fails_at_load(tmp_path, text, word):
    with pytest.raises(ValueError, match=word):
        load(with_permissions(tmp_path, text))
