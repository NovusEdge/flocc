import json
import os
import subprocess
from pathlib import Path

from linear_agents.agentblock import IssueSettings
from linear_agents.config import Config

SOCKET = "linear-agents"
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


def session_name(issue_key: str, phase: str) -> str:
    return f"{issue_key}-test" if phase == "test" else issue_key


def claude_argv(
    cfg: Config,
    name: str,
    settings: IssueSettings,
    prompt: str,
    env: dict[str, str],
    claude_exe: str,
    exe: str,
) -> list[str]:
    servers = {s: cfg.mcp_servers[s] for s in settings.mcp}
    session_settings = {
        "statusLine": {"type": "command", "command": f"{exe} statusline"},
        "autoContinueAtUsageLimit": True,
    }
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
        "--remote-control",
        name,
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
        # -N: the server belongs to linear-agents-tmux.service. A server started from here
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
