import json
import os
import subprocess
from pathlib import Path

from flocc.agentblock import IssueSettings
from flocc.config import Config
from flocc.grants import Grants

SOCKET = "flocc"
# --no-verify skips the pre-commit claim check and the commit-msg trailer. Flag order varies, so
# the wildcard forms cover the flag anywhere after `git commit`; -n also arrives bundled (-nm).
_DENY = [
    "Bash(git commit --no-verify:*)",
    "Bash(git commit -n:*)",
    "Bash(git commit * --no-verify*)",
    "Bash(git commit * -n *)",
    "Bash(git commit * -n)",
    # A second claude process is a session the gate and the usage caps never see.
    "Bash(claude:*)",
]
_GENERAL_DENY = "Agent(general-purpose)"
NO_GRANTS = Grants()
_KEEP = ("HOME", "USER", "PATH", "LANG", "TERM")
# $0 is the token file. Reading it inside the pane keeps the token off the tmux and ps command
# lines, and a missing file stops the session instead of leaving gh to find other credentials.
_WRAPPER = 'GH_TOKEN=$(cat "$0") || exit 1; export GH_TOKEN; exec "$@"'


def clean_env(extra: dict[str, str] | None = None) -> dict[str, str]:
    # A session started from inside another Claude Code session inherits its messaging socket,
    # session ID and task list, ANTHROPIC_API_KEY would switch billing to the API, and
    # CLAUDE_CONFIG_DIR would move ~/.claude.json away from the login and folder trust.
    env = {k: os.environ[k] for k in _KEEP if k in os.environ}
    env.setdefault("TERM", "xterm-256color")
    env.setdefault("LANG", "C.UTF-8")
    return {**env, **(extra or {})}


def trust_folders(claude_json: Path, paths) -> None:
    # A git worktree does not inherit trust from a trusted parent directory, and an unattended
    # session would sit at the folder-trust dialog until its wall-clock limit. The docs name this
    # key as the manual way to trust a folder. Running sessions rewrite the file from memory and
    # can drop a key written meanwhile, so every launch writes it again just before starting.
    # `claude auth login` signs in without the first-run onboarding, so a host logged in that way
    # (0.4.0's flocc-setup --phase login) starts every session on the "Press Enter to continue"
    # security notes; flocc-nimble lost three hours of NF-260 to it on 2026-10-20.
    try:
        data = json.loads(claude_json.read_text())
    except FileNotFoundError:
        data = {}
    projects = data.setdefault("projects", {})
    missing = [str(p) for p in paths if not projects.get(str(p), {}).get("hasTrustDialogAccepted")]
    if not missing and data.get("hasCompletedOnboarding") is True:
        return
    for p in missing:
        projects.setdefault(p, {})["hasTrustDialogAccepted"] = True
    data["hasCompletedOnboarding"] = True
    tmp = claude_json.with_name(f"{claude_json.name}.flocc.tmp")
    # Mode 600 from the start: the file holds the account's OAuth state.
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        json.dump(data, f, indent=2)
    os.replace(tmp, claude_json)


def session_name(issue_key: str, phase: str) -> str:
    return f"{issue_key}-{phase}" if phase in ("test", "review") else issue_key


def installed_plugins(path: Path) -> tuple[str, ...]:
    try:
        return tuple(json.loads(path.read_text()).get("plugins", {}))
    except (OSError, ValueError, AttributeError):
        return ()


def allowed_plugins(path: Path) -> tuple[str, ...] | None:
    if not path.exists():
        return None
    lines = (line.strip() for line in path.read_text().splitlines())
    return tuple(line for line in lines if line and not line.startswith("#"))


def claude_argv(
    cfg: Config,
    name: str,
    settings: IssueSettings,
    prompt: str,
    env: dict[str, str],
    claude_exe: str,
    exe: str,
    grants: Grants = NO_GRANTS,
    disabled_plugins: tuple[str, ...] = (),
    remote_control: bool = True,
) -> list[str]:
    servers = {s: cfg.mcp_servers[s] for s in settings.mcp}
    deny = list(_DENY)
    if "general-purpose" not in grants.agents:
        deny.append(_GENERAL_DENY)
    session_settings: dict = {
        "statusLine": {"type": "command", "command": f"{exe} statusline"},
        "autoContinueAtUsageLimit": True,
        "attribution": False,
        "permissions": {"deny": deny},
        "disableWorkflows": not grants.workflows,
    }
    if disabled_plugins:
        session_settings["enabledPlugins"] = {p: False for p in disabled_plugins}
    # The pane would otherwise inherit the tmux server's environment, which is whatever the
    # process that started the server had.
    argv = [
        "/usr/bin/env",
        "-i",
        *(f"{k}={v}" for k, v in env.items()),
        "/bin/sh",
        "-c",
        _WRAPPER,
        str(cfg.agent_gh_token_file),
        claude_exe,
        prompt,
        # The Claude app announces every Remote Control session; only the long phases get one.
        *(["--remote-control", name] if remote_control else []),
        "--name",
        name,
        "--plugin-dir",
        str(cfg.plugin_dir),
        "--settings",
        json.dumps(session_settings),
        "--permission-mode",
        "auto",
        "--strict-mcp-config",
    ]
    if settings.model:
        argv += ["--model", settings.model]
    if settings.effort:
        argv += ["--effort", settings.effort]
    # --mcp-config is variadic and would swallow anything after it.
    argv += ["--mcp-config", json.dumps({"mcpServers": servers})]
    return argv


class Sessions:
    def __init__(self, socket: str = SOCKET, runner=subprocess.run):
        self.socket = socket
        self._run = runner

    def _tmux(self, *args: str, check=True):
        # -N: the server belongs to flocc-tmux.service. A server started from here
        # would live in the tick's cgroup, which systemd kills when the oneshot tick exits.
        return self._run(
            ["tmux", "-N", "-L", self.socket, *args], check=check, capture_output=True, text=True
        )

    def start(self, name: str, cwd: Path, argv: list[str]) -> None:
        self._tmux("new-session", "-d", "-s", name, "-x", "200", "-y", "50", "-c", str(cwd), *argv)

    def alive(self, name: str) -> bool:
        return self._tmux("has-session", "-t", f"={name}", check=False).returncode == 0

    def kill(self, name: str) -> None:
        self._tmux("kill-session", "-t", f"={name}", check=False)

    def send(self, name: str, text: str) -> None:
        self._tmux("send-keys", "-t", f"={name}:", "-l", text)
        self._tmux("send-keys", "-t", f"={name}:", "Enter")
