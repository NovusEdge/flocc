"""A repo check as a transient systemd user unit of flocc-agent, so it outlives the request, the
tick and flocc.service: the tick is a oneshot, and systemd kills every process left in its cgroup
when it exits. Stopping the unit ends its whole cgroup, build servers that called setsid included.
Runs as flocc-agent, inside flocc-agent-run's `check` subcommand."""

import json
import math
import os
import shlex
import subprocess
import time
from pathlib import Path

from flocc.worktree import git

CHECK_JOB = "check.json"
CHECK_LOG = "check.log"
TAIL_BYTES = 4_000_000
# SIGINT first: pytest turns it into KeyboardInterrupt and runs fixture teardown, which stops the
# testcontainers it started.
STOP_GRACE_S = 10
# systemd-run returns once the unit is queued; a poll in this window may not find it yet.
START_GRACE_S = 15
_RUNNING = ("start-pre", "start", "start-post", "running", "auto-restart")
# What the check needs from flocc-agent's environment; the unit does not inherit it.
_PASS = ("PATH", "HOME", "TMPDIR", "XDG_RUNTIME_DIR", "DOCKER_HOST", "TESTCONTAINERS_RYUK_DISABLED")


def unit_name(key: str) -> str:
    return f"flocc-check-{key}"


def _env() -> dict[str, str]:
    # flocc-agent lingers, so its user manager runs; sudo does not carry the session's socket.
    return {**os.environ, "XDG_RUNTIME_DIR": f"/run/user/{os.getuid()}"}


def _run(argv: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(
        argv, env=_env(), capture_output=True, text=True, check=False, timeout=STOP_GRACE_S * 6
    )


def _show(unit: str) -> dict[str, str]:
    props = "LoadState,ActiveState,SubState,Result,ExecMainStatus"
    out = _run(["systemctl", "--user", "show", unit, "-p", props])
    return dict(line.split("=", 1) for line in out.stdout.splitlines() if "=" in line)


def _stop(unit: str) -> None:
    _run(["systemctl", "--user", "stop", unit])
    # A failed unit stays loaded, and its name blocks the next start.
    _run(["systemctl", "--user", "reset-failed", unit])


def _read_job(state: Path) -> dict | None:
    try:
        job = json.loads((state / CHECK_JOB).read_text())
    except (OSError, ValueError):
        return None
    return job if isinstance(job, dict) and "started" in job and "head" in job else None


def _tail(state: Path) -> str:
    try:
        with open(state / CHECK_LOG, "rb") as f:
            size = f.seek(0, os.SEEK_END)
            f.seek(max(0, size - TAIL_BYTES))
            text = f.read().decode(errors="replace")
    except OSError:
        return ""
    return text if size <= TAIL_BYTES else "[...]\n" + text


def _clear(state: Path) -> None:
    for name in (CHECK_JOB, CHECK_LOG, "check.json.tmp"):
        (state / name).unlink(missing_ok=True)


def stop_job(state: Path, key: str) -> None:
    """Ends the issue's check and forgets it; for a release and for removing the worktree."""
    _stop(unit_name(key))
    _clear(state)


def _start(state: Path, wt: Path, key: str, command: str, timeout_s: float, lost: int) -> dict:
    head = git("rev-parse", "HEAD", cwd=wt)
    # Only commits are pushed and guarded; a check on the working tree would pass on files the PR
    # leaves out.
    if dirty := git("status", "--porcelain", cwd=wt):
        return {"status": "dirty", "output": dirty, "head": head}
    unit, log = unit_name(key), state / CHECK_LOG
    # A unit left by a start whose record was lost would refuse the name.
    _stop(unit)
    state.mkdir(parents=True, exist_ok=True)
    tmp = state / "check.json.tmp"
    tmp.write_text(json.dumps({"started": time.time(), "head": head, "lost": lost}))
    os.replace(tmp, state / CHECK_JOB)
    env = _env()
    argv = [
        "systemd-run",
        "--user",
        "--quiet",
        f"--unit={unit}",
        # The exit status stays readable after the process ends, until the poll stops the unit.
        "--property=RemainAfterExit=yes",
        # Also ends a check no poll reaches, as during a host episode.
        f"--property=RuntimeMaxSec={max(1, math.ceil(timeout_s))}",
        "--property=KillSignal=SIGINT",
        f"--property=TimeoutStopSec={STOP_GRACE_S}",
        f"--property=WorkingDirectory={wt}",
        f"--property=StandardOutput=truncate:{log}",
        "--property=StandardError=inherit",
        *(f"--setenv={k}={env[k]}" for k in _PASS if k in env),
        "--",
        *shlex.split(command),
    ]
    started = _run(argv)
    if started.returncode != 0:
        _clear(state)
        raise subprocess.CalledProcessError(
            started.returncode, argv[:2], output=started.stdout, stderr=started.stderr
        )
    return {"status": "running"}


def step(req: dict, key: str, wt: Path, state: Path) -> dict:
    """One step of the check: starts it when none is recorded, otherwise reports it. Status running,
    ok, failed, timeout, dirty, or lost (the unit vanished twice, as a host restart does)."""
    command, timeout_s = str(req["command"]), float(req["timeout_s"])
    state.mkdir(parents=True, exist_ok=True)
    job = _read_job(state)
    if job is None:
        _clear(state)
        return _start(state, wt, key, command, timeout_s, 0)
    unit = unit_name(key)
    # A check of older commits says nothing about these.
    if job["head"] != git("rev-parse", "HEAD", cwd=wt):
        stop_job(state, key)
        return _start(state, wt, key, command, timeout_s, 0)
    info = _show(unit)
    if info.get("LoadState", "not-found") == "not-found":
        if time.time() - float(job["started"]) < START_GRACE_S:
            return {"status": "running"}
        # A lost check is rerun once, like a session found dead; the second loss is reported.
        lost = int(job.get("lost", 0)) + 1
        _clear(state)
        return _start(state, wt, key, command, timeout_s, lost) if lost < 2 else {"status": "lost"}
    if info.get("SubState") in _RUNNING:
        if time.time() - float(job["started"]) <= timeout_s:
            return {"status": "running"}
        status = "timeout"
    elif info.get("Result") == "timeout":
        status = "timeout"
    else:
        ok = info.get("Result") == "success" and info.get("ExecMainStatus") == "0"
        status = "ok" if ok else "failed"
    out = _tail(state)
    stop_job(state, key)
    return {"status": status, "output": out, "head": job["head"]}
