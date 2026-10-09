import pytest

from linear_agents.config import load
from tests.paths import EXAMPLE


def test_example_loads_with_defaults():
    cfg = load(EXAMPLE)
    assert cfg.team_key == "NF"
    assert cfg.session_cap == 2
    assert cfg.five_hour_max_pct == 60.0
    assert cfg.weekly_max_pct == 50.0
    assert cfg.repos["backend"].check == "just check"
    assert cfg.repos["backend"].draft is True
    assert cfg.check_timeout_s == 1800
    assert cfg.setup_timeout_s == 900
    assert cfg.state_root.is_absolute()
    assert cfg.mcp_servers["sentry"]["type"] == "http"


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
