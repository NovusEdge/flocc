from pathlib import Path

from linear_agents.config import load

TESTDATA = Path(__file__).parent.parent / "installer/internal/steps/testdata"
GOLDEN = TESTDATA / "config.golden.toml"
GOLDEN_SKIP = TESTDATA / "config.skip.golden.toml"


def test_the_installers_rendered_config_loads():
    cfg = load(GOLDEN)
    assert cfg.team_key == "NF"
    assert cfg.digest_issue == "NF-186"
    assert cfg.ntfy_url == "https://ntfy.sh/la-test"
    repo = cfg.repos["nimblefox-template"]
    assert repo.draft is False
    assert repo.check == "mise exec -- just check"
    assert repo.path == Path("~/src/nimblefox-template").expanduser()
    assert cfg.mcp_servers == {}
    assert cfg.skip_unmapped is False


def test_the_installers_config_with_excluded_repos_skips_unmapped_issues():
    assert load(GOLDEN_SKIP).skip_unmapped is True
