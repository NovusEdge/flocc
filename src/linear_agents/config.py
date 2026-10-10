import os
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

_PATH_KEYS = (
    "state_root",
    "worktree_root",
    "plugin_dir",
    "ledger_path",
    "linear_key_file",
    "agent_gh_token_file",
    "publish_gh_token_file",
    "claude_json",
)


@dataclass(frozen=True)
class RepoConfig:
    path: Path
    base_branch: str = "main"
    check: str = "just check"
    # GitHub Free organizations cannot open draft PRs on private repos.
    draft: bool = True
    commit_trailer: bool | None = None  # None follows Config.commit_trailer


# No claude_config_dir: sessions must share the owner's Claude Code login and
# folder trust, so CLAUDE_CONFIG_DIR stays unset.
@dataclass(frozen=True)
class Config:
    team_key: str
    trigger_label: str
    repos: dict[str, RepoConfig]
    state_root: Path
    worktree_root: Path
    plugin_dir: Path
    ledger_path: Path
    linear_key_file: Path
    agent_gh_token_file: Path
    publish_gh_token_file: Path
    ntfy_url: str
    # Required: tier 3 reports, wall-clock stops among them, reach the owner only through it.
    digest_issue: str
    digest_hour: int = 18
    session_cap: int = 2
    five_hour_max_pct: float = 60.0
    weekly_max_pct: float = 50.0
    context_restart_pct: float = 60.0
    default_wall_clock_s: int = 3 * 3600
    review_model: str = "opus"
    review_wall_clock_s: int = 3600
    check_timeout_s: int = 1800
    setup_timeout_s: int = 900
    mcp_servers: dict[str, dict] = field(default_factory=dict)
    commit_trailer: bool = True
    # Hosts sharing a Linear team each map only some repos, so an issue for another host's repo
    # is left alone instead of getting the no-repo comment.
    skip_unmapped: bool = False
    # Where Claude Code keeps folder trust; the launcher pre-trusts each worktree in it.
    claude_json: Path = field(default_factory=lambda: Path.home() / ".claude.json")


def trailer_enabled(cfg: Config, repo: RepoConfig) -> bool:
    return cfg.commit_trailer if repo.commit_trailer is None else repo.commit_trailer


def default_config_path() -> Path:
    env = os.environ.get("LINEAR_AGENTS_CONFIG")
    return Path(env) if env else Path.home() / ".config" / "linear-agents" / "config.toml"


def load(path: Path) -> Config:
    raw = tomllib.loads(path.read_text())
    # Unpacked so a misspelt repo key fails like a misspelt top-level one.
    repos = {
        name: RepoConfig(path=Path(r.pop("path")).expanduser(), **r)
        for name, r in raw.pop("repos", {}).items()
    }
    if not repos:
        raise ValueError(f"{path}: no [repos.<name>] tables")
    if not raw.get("digest_issue"):
        raise ValueError(f"{path}: digest_issue is required, the key of the pinned digest issue")
    for key in _PATH_KEYS:
        if key in raw:
            raw[key] = Path(raw[key]).expanduser()
    return Config(repos=repos, **raw)
