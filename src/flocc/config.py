import os
import sys
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

_EFFORT_LEVELS = ("low", "medium", "high", "xhigh", "max")

_RETIRED = ("ntfy_url", "context_restart_pct", "idle_escalate_s", "stall_escalate_s")
_LAYOUT = (
    "worktree_root",
    "plugin_dir",
    "agent_home",
    "agent_state_root",
    "agent_flocc",
    "agent_run",
    "claude_json",
    "installed_plugins_file",
)
AGENT_RUN = ("sudo", "-n", "-H", "-u", "flocc-agent", "/usr/local/libexec/flocc-agent-run")
_PATH_KEYS = (
    "state_root",
    "ledger_path",
    "linear_key_file",
    "agent_gh_token_file",
    "publish_gh_token_file",
    "agent_plugins_file",
)


@dataclass(frozen=True)
class RepoConfig:
    path: Path
    base_branch: str = "main"
    check: str = "just check"
    # GitHub Free organizations cannot open draft PRs on private repos.
    draft: bool = True
    commit_trailer: bool | None = None  # None follows Config.commit_trailer
    # Added with the publish token to a PR whose implementer amended tester files, for repos whose
    # PR check fails such PRs without it. Empty adds none; flocc never removes it.
    amended_label: str = ""
    # A check starts only with this much MemAvailable, in GiB: its peak plus
    # Config.min_available_mem_gb, so starting it never opens a host memory episode. A repo whose
    # check builds a lot (dotnet, a full test stack) needs more than the default.
    check_min_mem_gb: float = 5.5
    # systemd MemoryHigh and MemoryMax of the check's unit. A runaway check throttles at High and
    # is killed alone at Max, instead of the host's OOM killer choosing among sessions.
    check_memory_high: str = "4.5G"
    check_memory_max: str = "5G"


# No claude_config_dir: sessions must share the owner's Claude Code login and
# folder trust, so CLAUDE_CONFIG_DIR stays unset.
@dataclass(frozen=True)
class Config:
    team_key: str
    trigger_label: str
    repos: dict[str, RepoConfig]
    state_root: Path
    ledger_path: Path
    linear_key_file: Path
    agent_gh_token_file: Path
    publish_gh_token_file: Path
    digest_hour: int = 18
    session_cap: int = 2
    five_hour_max_pct: float = 80.0
    weekly_max_pct: float = 80.0
    check_fix_rounds: int = 2
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
    # flocc.host opens an episode past either: starts and relaunches wait, and cardinal is told.
    tmp_max_pct: float = 80.0
    min_available_mem_gb: float = 1.0
    # One uv cache under worktree_root kept across setups, for hosts whose disk can hold it; the
    # default throwaway cache per setup downloads every package again each time.
    shared_uv_cache: bool = False
    mcp_servers: dict[str, dict] = field(default_factory=dict)
    commit_trailer: bool = True
    # Hosts sharing a Linear team each map only some repos, so an issue for another host's repo
    # is left alone instead of getting the no-repo comment.
    skip_unmapped: bool = False
    # cardinal's file verb creates Linear issues only when this is on; off, it records each draft
    # for the digest so the owner can read what it would have filed.
    cardinal_file_on: bool = False
    # Linear user or bot/app ids whose comments on a running issue are forwarded into the live
    # session like the owner's own, letting another agent steer. Empty forwards only the owner's.
    steer_authors: tuple[str, ...] = ()
    # GitHub logins whose `/flocc ...` comments or reviews on a done issue's open PR start a
    # comment fix like a Linear comment. Empty turns the GitHub scan off.
    github_steer_logins: tuple[str, ...] = ()
    # Fix sessions that steering comments on a done issue's open PR may start per claim. Past it
    # the comments wait for a Todo rerun; 0 turns the scan off.
    comment_fix_max: int = 3
    # Written by flocc setup and no longer read: cardinal writes the digest.
    digest_issue: str = ""
    # The two-user layout: the same on every host, created by flocc setup and not configurable.
    agent_user: str = "flocc-agent"
    agent_home: Path = Path("/home/flocc-agent")
    worktree_root: Path = Path("/home/flocc-agent/worktrees")
    agent_state_root: Path = Path("/home/flocc-agent/state")
    plugin_dir: Path = Path("/opt/flocc/plugin")
    agent_flocc: Path = Path("/opt/flocc/bin/flocc")
    agent_run: tuple[str, ...] = AGENT_RUN
    # The plugins agent sessions get; None means /opt/flocc/deploy/agent-plugins.txt.
    agent_plugins_file: Path | None = None

    @property
    def agent_token_file(self) -> Path:
        """flocc-agent's copy of the read-only token, written by flocc-agent-run."""
        return self.agent_home / ".config" / "flocc" / "gh-agent.token"

    @property
    def agent_claude(self) -> Path:
        return self.agent_home / ".local" / "bin" / "claude"

    def effort_for(self, phase: str) -> str:
        if phase == "test":
            return self.tester_effort
        if phase == "review":
            return self.review_effort
        return self.impl_effort

    @property
    def tick_lock(self) -> Path:
        return self.ledger_path.with_suffix(".lock")


# Branch recovery finds the tester's commits by this key, so renaming it strands branches already
# pushed.
TRAILER = "Flocc"


def trailer_enabled(cfg: Config, repo: RepoConfig) -> bool:
    return cfg.commit_trailer if repo.commit_trailer is None else repo.commit_trailer


def default_config_path() -> Path:
    env = os.environ.get("FLOCC_CONFIG")
    return Path(env) if env else Path.home() / ".config" / "flocc" / "config.toml"


def _repo(config_path: Path, name: str, table: dict) -> RepoConfig:
    if table.pop("shared_paths", None) is not None:
        print(
            f"{config_path}: repos.{name}.shared_paths is no longer used; delete it",
            file=sys.stderr,
        )
    # Unpacked so a misspelt repo key fails like a misspelt top-level one.
    return RepoConfig(path=Path(table.pop("path")).expanduser(), **table)


def load(path: Path) -> Config:
    raw = tomllib.loads(path.read_text())
    repos = {name: _repo(path, name, r) for name, r in raw.pop("repos", {}).items()}
    if not repos:
        raise ValueError(f"{path}: no [repos.<name>] tables")
    # Hosts keep their config.toml across upgrades, so a key a release removed must not stop
    # the tick.
    for key in _RETIRED:
        if raw.pop(key, None) is not None:
            print(f"{path}: {key} is no longer used; delete it", file=sys.stderr)
    for key in _LAYOUT:
        if raw.pop(key, None) is not None:
            print(f"{path}: {key} is fixed by the two-user layout; delete it", file=sys.stderr)
    for key in _PATH_KEYS:
        if key in raw:
            raw[key] = Path(raw[key]).expanduser()
    for key in ("steer_authors", "github_steer_logins"):
        if key not in raw:
            continue
        names = raw[key]
        # A bare string would become a char tuple and match nothing, so steering would silently
        # never fire; fail at load like the effort check instead.
        if not isinstance(names, list) or not all(isinstance(a, str) for a in names):
            raise ValueError(f"{path}: {key} must be a list of strings")
        raw[key] = tuple(names)
    stale = path.with_name("permissions.toml")
    if stale.exists():
        print(
            f"{stale} is no longer read: sessions run in bypassPermissions; delete it",
            file=sys.stderr,
        )
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
    for key in ("five_hour_max_pct", "weekly_max_pct", "tmp_max_pct"):
        if not 0 <= getattr(cfg, key) <= 100:
            raise ValueError(f"{path}: {key} {getattr(cfg, key)} is not in 0-100")
    if cfg.min_available_mem_gb < 0:
        raise ValueError(f"{path}: min_available_mem_gb {cfg.min_available_mem_gb} must be >= 0")
    if not 0 <= cfg.comment_fix_max <= 10:
        raise ValueError(f"{path}: comment_fix_max {cfg.comment_fix_max} is not in 0-10")
    return cfg
