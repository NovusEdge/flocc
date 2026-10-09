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
)


@dataclass(frozen=True)
class RepoConfig:
    path: Path
    base_branch: str = "main"
    check: str = "just check"


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
    digest_issue: str | None = None
    digest_hour: int = 18
    session_cap: int = 2
    five_hour_max_pct: float = 60.0
    weekly_max_pct: float = 50.0
    context_restart_pct: float = 60.0
    default_wall_clock_s: int = 3 * 3600
    mcp_servers: dict[str, dict] = field(default_factory=dict)


def default_config_path() -> Path:
    env = os.environ.get("LINEAR_AGENTS_CONFIG")
    return Path(env) if env else Path.home() / ".config" / "linear-agents" / "config.toml"


def load(path: Path) -> Config:
    raw = tomllib.loads(path.read_text())
    repos = {
        name: RepoConfig(
            path=Path(r["path"]).expanduser(),
            base_branch=r.get("base_branch", "main"),
            check=r.get("check", "just check"),
        )
        for name, r in raw.pop("repos", {}).items()
    }
    if not repos:
        raise ValueError(f"{path}: no [repos.<name>] tables")
    for key in _PATH_KEYS:
        if key in raw:
            raw[key] = Path(raw[key]).expanduser()
    return Config(repos=repos, **raw)
