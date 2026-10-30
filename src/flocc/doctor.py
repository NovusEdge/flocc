import json
import os
import re
import shutil
import subprocess
import time
from dataclasses import dataclass
from pathlib import Path

from flocc.agent import Agent
from flocc.config import Config
from flocc.linear import Linear, LinearError, http_transport
from flocc.tokens import (
    LOGIN_REMEDY,
    WARN_DAYS,
    TokenRejected,
    can_write_contents,
    claude_login,
    token_expiry,
)
from flocc.worktree import free_disk_gb

# Minimum claude version whose flags the launcher needs (--name, --remote-control).
MIN_CLAUDE = (2, 1, 293)


@dataclass(frozen=True)
class Result:
    status: str  # ok, warn or fail
    name: str
    detail: str


def secret_file(name: str, path: Path) -> Result:
    if not path.exists():
        return Result("fail", name, f"{path} is missing")
    text = path.read_text().strip()
    if not text:
        return Result("fail", name, f"{path} is empty")
    # The service sends the whole file as the credential, so a .env line breaks it.
    prefix = re.match(r"[A-Z_]+=", text)
    if prefix:
        return Result("fail", name, f"{path} holds {prefix.group()}…; keep only the value")
    mode = path.stat().st_mode & 0o777
    if mode & 0o077:
        return Result("fail", name, f"{path} is mode {mode:o}; chmod 600 {path}")
    return Result("ok", name, str(path))


def github_slug(url: str) -> str | None:
    m = re.match(r"(?:https://github\.com/|git@github\.com:)([^/]+/[^/]+?)(?:\.git)?/?$", url)
    return m.group(1) if m else None


def token_checks(
    name: str,
    path: Path,
    slug: str | None,
    should_write: bool,
    now: float,
    expiry=token_expiry,
    can_write=can_write_contents,
) -> list[Result]:
    label = f"github {name} token"
    file = secret_file(label, path)
    if file.status == "fail":
        return [file]
    token = path.read_text().strip()
    results = [file]
    try:
        expires = expiry(token)
    except TokenRejected:
        return [*results, Result("fail", label, "GitHub rejected it: expired or revoked")]
    except OSError as exc:
        return [*results, Result("warn", label, f"could not reach GitHub: {exc}")]
    if expires is None:
        results.append(Result("ok", label, "never expires"))
    else:
        days = int((expires - now) // 86400)
        day = time.strftime("%Y-%m-%d", time.gmtime(expires))
        status = "warn" if days < WARN_DAYS else "ok"
        results.append(Result(status, label, f"expires {day} ({days} days)"))
    if slug is None:
        return [*results, Result("warn", label, "no GitHub repo mapped to test access on")]
    try:
        writes = can_write(token, slug)
    except (LookupError, OSError) as exc:
        return [*results, Result("fail", label, f"{slug}: {exc}")]
    if writes and not should_write:
        detail = f"can push to {slug}; set Contents to Read-only so agents cannot push"
        results.append(Result("fail", label, detail))
    elif not writes and should_write:
        detail = f"cannot push to {slug}; give it Contents and Pull requests write"
        results.append(Result("fail", label, detail))
    else:
        results.append(Result("ok", label, f"{'write' if writes else 'read-only'} on {slug}"))
    return results


def claude_checks(claude: list[str] | None, run=subprocess.run) -> list[Result]:
    if claude is None:
        return [Result("fail", "claude", "flocc-agent-run is not set up")]
    out = run([*claude, "--version"], capture_output=True, text=True, check=False).stdout
    m = re.match(r"(\d+)\.(\d+)\.(\d+)", out.strip())
    version = tuple(int(g) for g in m.groups()) if m else None
    if version is None or version < MIN_CLAUDE:
        need = ".".join(map(str, MIN_CLAUDE))
        results = [Result("fail", "claude", f"version {out.strip() or '?'}; needs {need}")]
    else:
        results = [Result("ok", "claude", out.strip())]
    login = claude_login(claude, run)
    if login == "full":
        results.append(Result("ok", "claude login", "claude.ai subscription"))
    else:
        detail = login or "claude auth status gave no answer"
        results.append(Result("fail", "claude login", f"{detail}; {LOGIN_REMEDY}"))
    return results


def linear_checks(cfg: Config, linear) -> list[Result]:
    names = [cfg.trigger_label]
    try:
        # Separate calls: a long repo list must not be able to crowd out a required label.
        required = linear.missing_labels(cfg.team_key, names)
        unrouted = linear.missing_labels(cfg.team_key, list(cfg.repos))
    except LinearError as exc:
        return [Result("fail", "linear", str(exc))]
    names = [*names, *cfg.repos]
    if required:
        results = [
            Result("fail", "linear labels", f"create on {cfg.team_key}: {', '.join(required)}")
        ]
    else:
        results = [Result("ok", "linear labels", ", ".join(n for n in names if n not in unrouted))]
    if unrouted:
        detail = f"no label on {cfg.team_key} for: {', '.join(unrouted)}; issues for them are never routed"
        results.append(Result("warn", "repo labels", detail))
    try:
        digest = linear.issue(cfg.digest_issue)
    except LinearError as exc:
        return [*results, Result("fail", "digest issue", f"{cfg.digest_issue}: {exc}")]
    if digest.state_type in ("completed", "canceled"):
        detail = f"{cfg.digest_issue} is {digest.state_type}; reopen it"
        return [*results, Result("warn", "digest issue", detail)]
    return [*results, Result("ok", "digest issue", f"{cfg.digest_issue} {digest.title}")]


def repo_checks(cfg: Config, run=subprocess.run) -> tuple[list[Result], str | None]:
    """The results, and the GitHub slug of the first mapped repo for the token checks."""
    results, slug = [], None
    for name, repo in cfg.repos.items():
        label = f"repo {name}"
        if not (repo.path / ".git").exists():
            results.append(Result("fail", label, f"{repo.path} is not a git clone"))
            continue
        url = run(
            ["git", "remote", "get-url", "origin"],
            cwd=repo.path,
            capture_output=True,
            text=True,
            check=False,
        ).stdout.strip()
        slug = slug or github_slug(url)
        # The fetch prepare runs before every issue; its credentials must work unattended.
        fetch = run(
            ["git", "ls-remote", "--exit-code", "origin", f"refs/heads/{repo.base_branch}"],
            cwd=repo.path,
            capture_output=True,
            text=True,
            check=False,
            env={**os.environ, "GIT_TERMINAL_PROMPT": "0"},
        )
        if fetch.returncode != 0:
            detail = fetch.stderr.strip()[-300:] or f"no {repo.base_branch} on origin"
            results.append(Result("fail", label, f"cannot fetch {url}: {detail}"))
        else:
            results.append(Result("ok", label, f"{repo.path} fetches {repo.base_branch}"))
    return results, slug


def path_checks(cfg: Config) -> list[Result]:
    results = []
    for tool in ("git", "gh", "flocc"):
        found = shutil.which(tool)
        results.append(Result("ok" if found else "fail", tool, found or "not on PATH"))
    manifest = cfg.plugin_dir / ".claude-plugin" / "plugin.json"
    ok = manifest.exists()
    results.append(
        Result("ok" if ok else "fail", "plugin", str(manifest if ok else cfg.plugin_dir))
    )
    ok = cfg.worktree_root.is_dir()
    results.append(Result("ok" if ok else "fail", "worktree root", str(cfg.worktree_root)))
    return results


def scope_check(cfg: Config) -> list[Result]:
    if not cfg.skip_unmapped:
        return []
    return [
        Result(
            "ok",
            "skip_unmapped",
            f"issues matching none of {', '.join(cfg.repos)} are left to other hosts,"
            " with no comment",
        )
    ]


def disk_check(cfg: Config, free=free_disk_gb) -> Result:
    gb = free(cfg.worktree_root)
    detail = f"{gb:.1f} GB free under {cfg.worktree_root}"
    if gb < cfg.min_free_disk_gb:
        return Result("fail", "disk", f"{detail}, below min_free_disk_gb {cfg.min_free_disk_gb:g}")
    if gb < 2 * cfg.min_free_disk_gb:
        return Result("warn", "disk", f"{detail}, under twice min_free_disk_gb")
    return Result("ok", "disk", detail)


def report(results: list[Result]) -> int:
    for r in results:
        print(f"{'FAIL' if r.status == 'fail' else r.status:<6}{r.name}: {r.detail}")
    fails = sum(1 for r in results if r.status == "fail")
    print(f"{len(results)} checks, {fails} failed")
    return 1 if fails else 0


def agent_checks(cfg: Config, config_path: Path, agent) -> list[Result]:
    secrets = [
        cfg.linear_key_file,
        cfg.publish_gh_token_file,
        cfg.agent_gh_token_file,
        cfg.ledger_path,
        config_path,
    ]
    try:
        # Directories whose contents flocc, its hooks or its git run as flocc.
        writes = [
            cfg.plugin_dir,
            cfg.agent_flocc.parent,
            cfg.agent_flocc.parent.parent,
            cfg.state_root,
            *(repo.path / ".git" for repo in cfg.repos.values()),
        ]
        seen = agent.probe([str(p) for p in secrets], [str(p) for p in writes])
    except (subprocess.SubprocessError, OSError) as exc:
        detail = (
            f"flocc-agent-run did not answer ({exc}); flocc setup installs it and its sudoers rule"
        )
        return [Result("fail", "agent", detail)]
    results = [Result("ok", "agent", "flocc-agent-run answers through sudo")]
    reached = [p for p, ok in {**seen["read"], **seen["write"]}.items() if ok]
    if reached:
        detail = (
            f"flocc-agent can reach {', '.join(reached)}; keep /home/flocc mode 700 and"
            f" {cfg.plugin_dir}, {cfg.agent_flocc.parent.parent}, the state directory and the"
            " service clones flocc's"
        )
        results.append(Result("fail", "agent boundary", detail))
    else:
        detail = "flocc-agent cannot read the secrets or the ledger, nor write the plugin directory"
        results.append(Result("ok", "agent boundary", detail))
    if "docker" in seen.get("groups", []):
        detail = (
            "flocc-agent is in the docker group, which is root-equivalent; checks use its Podman"
            " socket instead: gpasswd -d flocc-agent docker"
        )
        results.append(Result("fail", "agent docker", detail))
    else:
        results.append(Result("ok", "agent docker", "flocc-agent is not in the docker group"))
    if seen.get("containers"):
        results.append(Result("ok", "containers", "checks reach flocc-agent's Podman socket"))
    else:
        detail = (
            "no Podman socket for flocc-agent; checks that start containers fail"
            " (docker = true in the host file sets it up)"
        )
        results.append(Result("ok", "containers", detail))
    if seen.get("tmux"):
        results.append(Result("ok", "tmux", "flocc-agent's session server is up"))
    else:
        detail = "flocc-agent has no session server; flocc ship -H <this host> starts it"
        results.append(Result("fail", "tmux", detail))
    return results


MANAGED_SETTINGS = Path("/etc/claude-code/managed-settings.json")
_DIALOG_TOOLS = {"AskUserQuestion", "EnterPlanMode", "ExitPlanMode"}


def managed_check(path: Path = MANAGED_SETTINGS) -> Result:
    remedy = "flocc ship -H <this host> writes it in the root phase"
    try:
        data = json.loads(path.read_text())
        deny = set(data["permissions"]["deny"])
        commands = [h["command"] for e in data["hooks"]["PreToolUse"] for h in e["hooks"]]
    except (OSError, ValueError, KeyError, TypeError) as exc:
        detail = f"{path} unreadable or incomplete ({exc}); {remedy}"
        return Result("fail", "managed settings", detail)
    if not _DIALOG_TOOLS <= deny or not any(c.endswith("flocc guard") for c in commands):
        detail = f"{path} lacks the guard or the dialog denies; {remedy}"
        return Result("fail", "managed settings", detail)
    return Result("ok", "managed settings", "the guard and the dialog denies are root's")


def collect_results(cfg: Config, config_path: Path, agent=None) -> list[Result]:
    agent = agent or Agent(cfg)
    results = path_checks(cfg) + scope_check(cfg)
    results.append(disk_check(cfg))
    key = secret_file("linear key", cfg.linear_key_file)
    results.append(key)
    if key.status == "ok":
        linear = Linear(http_transport(cfg.linear_key_file.read_text().strip()))
        results += linear_checks(cfg, linear)
    repos, slug = repo_checks(cfg)
    results += repos
    now = time.time()
    results += token_checks("agent", cfg.agent_gh_token_file, slug, False, now)
    results += token_checks("publish", cfg.publish_gh_token_file, slug, True, now)
    results += claude_checks(agent.claude_argv())
    results += agent_checks(cfg, config_path, agent)
    results.append(managed_check())
    return results


def run_doctor(cfg: Config, config_path: Path) -> int:
    return report(collect_results(cfg, config_path))


def doctor_json(cfg: Config, config_path: Path) -> int:
    results = collect_results(cfg, config_path)
    # A warning does not fail the doctor run, so it stays ok=true for readers that predate "warn".
    checks = [{"name": "config", "ok": True, "detail": str(config_path)}] + [
        {
            "name": r.name,
            "ok": r.status != "fail",
            **({"warn": True} if r.status == "warn" else {}),
            "detail": r.detail,
        }
        for r in results
    ]
    print(json.dumps({"v": 1, "checks": checks}))
    return 1 if any(r.status == "fail" for r in results) else 0
