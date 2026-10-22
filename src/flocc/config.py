import os
import re
import sys
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

_EFFORT_LEVELS = ("low", "medium", "high", "xhigh", "max")

# Allow rules and the judge's prefixes may name these only exactly: a wildcard after one admits
# any program.
INTERPRETERS = frozenset(
    {
        "sh",
        "bash",
        "zsh",
        "dash",
        "fish",
        "python",
        "python3",
        "node",
        "deno",
        "bun",
        "perl",
        "ruby",
        "php",
        "eval",
        "exec",
        "env",
        "xargs",
        "sudo",
        "su",
        "nohup",
        "timeout",
        "nice",
        "watch",
    }
)
_BASH_RULE = re.compile(r"Bash\((.*)\)")
_ENV_ASSIGN = re.compile(r"[A-Za-z_]\w*=[\w.,:/@%+=-]*")
_PROGRAM = re.compile(r"[A-Za-z0-9._/+-]+")
_PERMISSION_KEYS = {
    "allow": "permission_allow",
    "environment": "permission_environment",
    "judge_model": "judge_model",
    "judge_max_per_run": "judge_max_per_run",
}

_PATH_KEYS = (
    "state_root",
    "worktree_root",
    "plugin_dir",
    "ledger_path",
    "linear_key_file",
    "agent_gh_token_file",
    "publish_gh_token_file",
    "claude_json",
    "agent_plugins_file",
    "installed_plugins_file",
)


@dataclass(frozen=True)
class RepoConfig:
    path: Path
    base_branch: str = "main"
    check: str = "just check"
    # GitHub Free organizations cannot open draft PRs on private repos.
    draft: bool = True
    commit_trailer: bool | None = None  # None follows Config.commit_trailer
    # Files every issue edits (an append-only log): never claimed, never refused at commit.
    shared_paths: tuple[str, ...] = ()


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
    idle_escalate_s: float = 1800.0
    stall_escalate_s: float = 1200.0
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
    # One uv cache under worktree_root kept across setups, for hosts whose disk can hold it; the
    # default throwaway cache per setup downloads every package again each time.
    shared_uv_cache: bool = False
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
    # The plugins agent sessions get; None means deploy/agent-plugins.txt in the service repo.
    agent_plugins_file: Path | None = None
    installed_plugins_file: Path = field(
        default_factory=lambda: Path.home() / ".claude" / "plugins" / "installed_plugins.json"
    )
    # From permissions.toml beside the config, written by flocc ship from the host file.
    permission_allow: tuple[str, ...] = ()
    permission_environment: tuple[str, ...] = ()
    # Empty turns the permission judge off.
    judge_model: str = ""
    judge_max_per_run: int = 30

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
    shared = table.pop("shared_paths", [])
    # A bare string would become a char tuple; fail at load like steer_authors.
    if not isinstance(shared, list) or not all(isinstance(p, str) for p in shared):
        raise ValueError(f"{config_path}: repos.{name}.shared_paths must be a list of path strings")
    # Unpacked so a misspelt repo key fails like a misspelt top-level one.
    return RepoConfig(
        path=Path(table.pop("path")).expanduser(), shared_paths=tuple(shared), **table
    )


def wildcards_an_interpreter(rule: str) -> bool:
    rule = rule.strip()
    if rule == "Bash":
        return True
    m = _BASH_RULE.fullmatch(rule)
    if m is None:
        return False
    body = m.group(1).strip()
    if re.search(r"(?:^|\s)-c", body):
        return True
    # A VAR=value whose value holds a quote, escape or expansion can swallow spaces, so only a
    # plain value is skipped; anything else stays as the program token and fails _PROGRAM.
    tokens = body.split()
    while tokens and _ENV_ASSIGN.fullmatch(tokens[0]):
        tokens.pop(0)
    # "cmd:*" is Claude Code's legacy prefix form. Claude Code matches "*" anywhere in a rule, so
    # "py* *" reaches python, and $SHELL, {a,b} or a quoted name is a program this check cannot
    # resolve; hence a whitelist.
    program = tokens[0].split(":", 1)[0] if tokens else ""
    if not _PROGRAM.fullmatch(program):
        return True
    program = program.rsplit("/", 1)[-1].lower()
    return "*" in body and (
        program in INTERPRETERS or program.rstrip("0123456789.-") in INTERPRETERS
    )


def _permissions(path: Path) -> dict:
    # Its own file: the installer's Config step rewrites config.toml only when it fails to load.
    try:
        table = tomllib.loads(path.read_text())
    except FileNotFoundError:
        return {}
    unknown = sorted(set(table) - _PERMISSION_KEYS.keys())
    if unknown:
        raise ValueError(f"{path}: unknown key {unknown[0]}")
    out: dict = {}
    for key in ("allow", "environment"):
        value = table.get(key, [])
        if not isinstance(value, list) or not all(isinstance(v, str) for v in value):
            raise ValueError(f"{path}: {key} must be a list of strings")
        out[_PERMISSION_KEYS[key]] = tuple(value)
    # Dropped, not fatal: a refused rule raising here would stop every tick on the host until the
    # next ship.
    kept = []
    for rule in out["permission_allow"]:
        if wildcards_an_interpreter(rule):
            print(
                f"{path}: allow rule {rule} wildcards an interpreter or shell; dropped",
                file=sys.stderr,
            )
        else:
            kept.append(rule)
    out["permission_allow"] = tuple(kept)
    if "judge_model" in table:
        out["judge_model"] = str(table["judge_model"])
    if "judge_max_per_run" in table:
        n = table["judge_max_per_run"]
        if not isinstance(n, int) or n < 1:
            raise ValueError(f"{path}: judge_max_per_run {n!r} must be a whole number >= 1")
        out["judge_max_per_run"] = n
    return out


def load(path: Path) -> Config:
    raw = tomllib.loads(path.read_text())
    repos = {name: _repo(path, name, r) for name, r in raw.pop("repos", {}).items()}
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
    cfg = Config(repos=repos, **raw, **_permissions(path.with_name("permissions.toml")))
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
