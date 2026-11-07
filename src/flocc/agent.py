import json
import os
import subprocess
from pathlib import Path

from flocc.agentrun import SHELL_TIMEOUT_S
from flocc.config import Config, trailer_enabled
from flocc.ledger import Claim
from flocc.publisher import PublishResult
from flocc.worktree import BranchDiverged, DirtyWorktree, git


class AgentError(subprocess.SubprocessError):
    pass


class CheckLost(AgentError):
    pass


class BaseCheckWaiting(AgentError):
    """The base check did not run: the host was short of memory."""


def _memory(repo) -> dict:
    return {
        "min_mem_gb": repo.check_min_mem_gb,
        "memory_max": repo.check_memory_max,
        "memory_swap_max": repo.check_memory_swap_max,
    }


def _prepare_error(data: dict) -> subprocess.SubprocessError:
    kind, detail = data["error"], str(data.get("detail", ""))
    exc: subprocess.SubprocessError
    if kind == "setup":
        exc = subprocess.CalledProcessError(int(data.get("code") or 1), ["./setup"], stderr=detail)
    elif kind == "timeout":
        exc = subprocess.TimeoutExpired(["./setup"], float(data.get("timeout") or 0), stderr=detail)
    elif kind == "dirty":
        exc = DirtyWorktree(detail)
    elif kind == "diverged":
        exc = BranchDiverged(detail)
    else:
        exc = subprocess.CalledProcessError(int(data.get("code") or 1), ["git"], stderr=detail)
    # Set when the worktree is still exactly at the base, after a setup failure or a dirty one.
    setattr(exc, "at_base", data.get("at_base"))  # noqa: B010
    return exc


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

    def _repo(self, name: str) -> tuple[dict, float]:
        repo = self.cfg.repos[name]
        url = git("remote", "get-url", "origin", cwd=repo.path)
        common = {
            "repo": name,
            "url": url,
            "base_branch": repo.base_branch,
            "worktree_root": str(self.cfg.worktree_root),
            "setup_timeout_s": self.cfg.setup_timeout_s,
            "shared_uv_cache": self.cfg.shared_uv_cache,
            "token": self.token(),
        }
        # A first clone of a big repo, then ./setup.
        return common, self.cfg.setup_timeout_s + 900

    def prepare(self, repo_name: str, key: str) -> tuple[Path, str, str]:
        common, timeout = self._repo(repo_name)
        payload = {
            **common,
            "key": key,
            "state_root": str(self.cfg.agent_state_root),
            "trailer": trailer_enabled(self.cfg, self.cfg.repos[repo_name]),
        }
        data = self.call("prepare", payload=payload, timeout=timeout)
        if "error" in data:
            raise _prepare_error(data)
        return Path(data["path"]), str(data["branch"]), str(data["base"])

    def claude_argv(self) -> list[str]:
        return [*self.cfg.agent_run, "claude"]

    def probe(self, read: list[str], write: list[str]) -> dict:
        return self.call("probe", payload={"read": read, "write": write})

    def remove(self, key: str, keep_dirty: bool = False) -> dict:
        payload = {
            "key": key,
            "worktree_root": str(self.cfg.worktree_root),
            "state_root": str(self.cfg.agent_state_root),
            "keep_dirty": keep_dirty,
        }
        return self.call("remove", payload=payload, timeout=300)

    def sweep(
        self,
        keep: list[str],
        now: float,
        max_age_s: float,
        scratch_age_s: float,
        pytest_age_s: float,
    ) -> dict:
        payload = {
            "keep": keep,
            "worktree_root": str(self.cfg.worktree_root),
            "state_root": str(self.cfg.agent_state_root),
            "now": now,
            "max_age_s": max_age_s,
            "scratch_age_s": scratch_age_s,
            "pytest_age_s": pytest_age_s,
        }
        return self.call("sweep", payload=payload, timeout=900)

    def usage(self) -> dict:
        return self.call(
            "usage", payload={"worktree_root": str(self.cfg.worktree_root)}, timeout=180
        )

    def stop_tests(self, name: str, worktree: str) -> dict:
        # The kill waits up to KILL_GRACE_S; each podman rm may take up to 120 s.
        return self.call("stop-tests", payload={"name": name, "worktree": worktree}, timeout=600)

    def shell(self, command: str) -> dict:
        return self.call("shell", payload={"command": command}, timeout=SHELL_TIMEOUT_S + 30)

    def check_worktree(self, claim: Claim, repo, timeout_s: float) -> dict:
        """One step of the check, which runs detached: status running (or waiting, while low
        memory holds the start; wait_timeout once it held for timeout_s) until it ends, then ok,
        failed, timeout or dirty with its output and the head it ran on."""
        payload = {
            "mode": "background",
            "worktree": claim.worktree,
            "state_root": str(self.cfg.agent_state_root),
            "command": repo.check,
            "timeout_s": timeout_s,
            "wait_s": self.cfg.memory_wait_s,
            **_memory(repo),
        }
        # Returns at once; the longest call stops a timed-out unit.
        data = self.call("check", payload=payload, timeout=120)
        if data.get("status") == "lost":
            raise CheckLost(f"{claim.issue_key}: the check job vanished twice (host restarted?)")
        return data

    def stop_check(self, claim: Claim) -> None:
        payload = {
            "mode": "stop",
            "worktree": claim.worktree,
            "state_root": str(self.cfg.agent_state_root),
        }
        self.call("check", payload=payload, timeout=120)

    def check_base(self, repo_name: str) -> tuple[str, PublishResult | None]:
        common, timeout = self._repo(repo_name)
        payload = {
            **common,
            "mode": "base-check",
            "command": self.cfg.repos[repo_name].check,
            "check_timeout_s": self.cfg.check_timeout_s,
            **_memory(self.cfg.repos[repo_name]),
        }
        data = self.call("check", payload=payload, timeout=timeout + self.cfg.check_timeout_s)
        if "waiting" in data:
            # Raised, not a failure: a failure here would be cached or mark the canary.
            raise BaseCheckWaiting(f"base check not run: {data['waiting']}")
        failure = data.get("failure")
        if failure is None:
            return str(data["sha"]), None
        return str(data["sha"]), PublishResult(
            "check_failed",
            str(failure["detail"]),
            log=str(failure["log"]),
            transient=bool(failure.get("transient")),
        )

    def setup_base(self, repo_name: str) -> tuple[int | None, str, str]:
        common, timeout = self._repo(repo_name)
        payload = {**common, "mode": "base-setup", "command": self.cfg.repos[repo_name].check}
        data = self.call("check", payload=payload, timeout=timeout)
        return data["code"], str(data["output"]), str(data["sha"])


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
