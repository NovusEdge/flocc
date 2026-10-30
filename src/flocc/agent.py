import json
import os
import subprocess
from pathlib import Path

from flocc.config import Config


class AgentError(subprocess.SubprocessError):
    pass


class Agent:
    """The service's side of flocc-agent-run: every call is one sudo to flocc-agent."""

    def __init__(self, cfg: Config, runner=subprocess.run):
        self.cfg = cfg
        self._run = runner

    def run(
        self, sub: str, *args: str, payload: dict | None = None, timeout: float = 60
    ) -> subprocess.CompletedProcess:
        # sudo -H replaces HOME with flocc-agent's. Without sudo (tests, a one-user dev host) this
        # keeps flocc-agent's files under agent_home instead of the caller's home.
        env = {**os.environ, "HOME": str(self.cfg.agent_home)}
        return self._run(
            [*self.cfg.agent_run, sub, *args],
            input="" if payload is None else json.dumps(payload),
            capture_output=True,
            text=True,
            env=env,
            timeout=timeout,
            check=False,
        )

    def call(self, sub: str, *args: str, payload: dict | None = None, timeout: float = 60) -> dict:
        """The subcommand's JSON answer; an answer with "error" is returned, anything else raises."""
        out = self.run(sub, *args, payload=payload, timeout=timeout)
        why = (out.stderr or "").strip()[-1500:]
        if out.returncode not in (0, 1):
            raise AgentError(f"flocc-agent-run {sub} exited {out.returncode}: {why}")
        try:
            data = json.loads(out.stdout or "null")
        except ValueError as exc:
            raise AgentError(f"flocc-agent-run {sub} answered no JSON: {why}") from exc
        if not isinstance(data, dict) or (out.returncode == 1 and "error" not in data):
            raise AgentError(f"flocc-agent-run {sub} failed: {why}")
        return data

    def token(self) -> str:
        path = self.cfg.agent_gh_token_file
        return path.read_text().strip() if path.exists() else ""

    def sessions(self) -> "AgentSessions":
        return AgentSessions(self)


class AgentSessions:
    """flocc-agent's tmux sessions, with the surface supervisor and poller use."""

    def __init__(self, agent: Agent):
        self.agent = agent

    def start(self, name, cwd: Path, argv, *, state, files=None, clear=(), trust=(), plugins=None):
        self.agent.call(
            "start",
            payload={
                "name": name,
                "cwd": str(cwd),
                "argv": list(argv),
                "state": str(state),
                "files": dict(files or {}),
                "clear": list(clear),
                "trust": [str(t) for t in trust],
                "plugins": None if plugins is None else list(plugins),
                "token": self.agent.token(),
            },
        )

    def alive(self, name: str) -> bool:
        out = self.agent.run("alive", name)
        if out.returncode not in (0, 1):
            raise AgentError(f"flocc-agent-run alive exited {out.returncode}: {out.stderr.strip()}")
        return out.returncode == 0

    def kill(self, name: str) -> None:
        self.agent.run("kill", name)

    def send(self, name: str, text: str) -> None:
        self.agent.call("send", payload={"name": name, "text": text})

    def capture(self, name: str, lines: int) -> str:
        return str(
            self.agent.call("capture", payload={"name": name, "lines": lines}).get("text", "")
        )
