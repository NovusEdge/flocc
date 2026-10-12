import os
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

_EFFORT_LEVELS = ("low", "medium", "high", "xhigh", "max")

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
    five_hour_max_pct: float = 80.0
    weekly_max_pct: float = 80.0
    context_restart_pct: float = 60.0
    default_wall_clock_s: int = 3 * 3600
    review_model: str = "opus"
    # Per-phase reasoning effort, overridden by an issue's agentblock effort. The tester plans the
    # behaviour before any code exists, so it runs high; implementer and reviewer run medium.
    tester_effort: str = "high"
    impl_effort: str = "medium"
    review_effort: str = "medium"
    review_wall_clock_s: int = 3600
    check_timeout_s: int = 1800
    setup_timeout_s: int = 900
    min_free_disk_gb: float = 2.0
    mcp_servers: dict[str, dict] = field(default_factory=dict)
    commit_trailer: bool = True
    # Hosts sharing a Linear team each map only some repos, so an issue for another host's repo
    # is left alone instead of getting the no-repo comment.
    skip_unmapped: bool = False
    # Linear user or bot/app ids whose comments on a running issue are forwarded into the live
    # session like the owner's own, letting another agent steer. Empty forwards only the owner's.
    steer_authors: tuple[str, ...] = ()
    # Where Claude Code keeps folder trust; the launcher pre-trusts each worktree in it.
    claude_json: Path = field(default_factory=lambda: Path.home() / ".claude.json")

    def effort_for(self, phase: str) -> str:
        if phase == "test":
            return self.tester_effort
        if phase == "review":
            return self.review_effort
        return self.impl_effort


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
    if "steer_authors" in raw:
        authors = raw["steer_authors"]
        # A bare string would become a char tuple and match nothing, so steering would silently
        # never fire; fail at load like the effort check instead.
        if not isinstance(authors, list) or not all(isinstance(a, str) for a in authors):
            raise ValueError(f"{path}: steer_authors must be a list of id strings")
        raw["steer_authors"] = tuple(authors)
    cfg = Config(repos=repos, **raw)
    # launch() passes a per-phase effort to --effort for every session, so a typo here would
    # crash the tick for all issues with an opaque CLI error; fail at load instead.
    for key in ("tester_effort", "impl_effort", "review_effort"):
        value = getattr(cfg, key)
        if value not in _EFFORT_LEVELS:
            raise ValueError(f"{path}: {key} {value!r} is not one of {', '.join(_EFFORT_LEVELS)}")
    if not 0 <= cfg.digest_hour <= 23:
        raise ValueError(f"{path}: digest_hour {cfg.digest_hour} is not in 0-23")
    if cfg.session_cap < 1:
        raise ValueError(f"{path}: session_cap {cfg.session_cap} must be >= 1")
    for key in ("five_hour_max_pct", "weekly_max_pct"):
        if not 0 <= getattr(cfg, key) <= 100:
            raise ValueError(f"{path}: {key} {getattr(cfg, key)} is not in 0-100")
    return cfg
