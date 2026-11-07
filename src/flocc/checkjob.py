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
# Present while a start is held back for memory; holds when the wait began. Status shows it.
WAITING = "check-waiting"
_LIMITS = (("MemoryMax", "memory_max"), ("MemorySwapMax", "memory_swap_max"))
MEMINFO = Path("/proc/meminfo")
TAIL_BYTES = 4_000_000
# SIGINT first: pytest turns it into KeyboardInterrupt and runs fixture teardown, which stops the
# testcontainers it started. 30 s, not 10: only more time for that teardown. The SIGKILL at the
# end of the grace already took the whole cgroup, since KillMode=control-group is systemd's default.
STOP_GRACE_S = 30
# dotnet build servers outlive the check by design and survive the SIGINT; with these off, the
# build runs in the check's own process tree and ends with it.
_NO_BUILD_SERVERS = ("MSBUILDDISABLENODEREUSE=1", "DOTNET_CLI_USE_MSBUILD_SERVER=0")
# Bounds one systemctl call; the service's call into flocc-agent-run allows 120 s.
_RUN_TIMEOUT_S = 60
# systemd-run returns once the unit is queued; a poll in this window may not find it yet.
START_GRACE_S = 15
_RUNNING = ("start-pre", "start", "start-post", "running", "auto-restart")
# Time blocked on a lock (a repo's machine-wide suite lock, held by another worktree's check) does
# not count against timeout_s; RuntimeMaxSec allows this much waiting on top before systemd stops
# the unit anyway.
WAIT_MAX_S = 4 * 3600
# The most one gap between polls can credit: a tick is 3 minutes, and a gap of hours (a host
# episode, flocc stopped) could hide a run between two blocked polls.
WAIT_CREDIT_MAX_S = 300
LOCKS = Path("/proc/locks")
CGROUPS = Path("/sys/fs/cgroup")
# What the check needs from flocc-agent's environment; the unit does not inherit it.
_PASS = ("PATH", "HOME", "TMPDIR", "XDG_RUNTIME_DIR", "DOCKER_HOST", "TESTCONTAINERS_RYUK_DISABLED")


def unit_name(key: str) -> str:
    return f"flocc-check-{key}"


def _env() -> dict[str, str]:
    # flocc-agent lingers, so its user manager runs; sudo does not carry the session's socket.
    return {**os.environ, "XDG_RUNTIME_DIR": f"/run/user/{os.getuid()}"}


def scoped(req: dict) -> tuple[list[str], dict[str, str]]:
    """An argv prefix and environment that run a command in a transient scope with the request's
    memory limits, for a check that runs in the caller's own process (the base check) instead of
    as a unit. Empty prefix when the request carries no limit."""
    props = [f"--property={p}={req[k]}" for p, k in _LIMITS if req.get(k)]
    if not props:
        return [], dict(os.environ)
    return ["systemd-run", "--user", "--scope", "--quiet", *props, "--"], _env()


def _run(argv: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(
        argv, env=_env(), capture_output=True, text=True, check=False, timeout=_RUN_TIMEOUT_S
    )


def _show(unit: str) -> dict[str, str]:
    props = "LoadState,ActiveState,SubState,Result,ExecMainStatus,ControlGroup"
    argv = ["systemctl", "--user", "show", unit, "-p", props]
    out = _run(argv)
    # A unit that does not exist still answers 0, with LoadState=not-found. A bus that does not
    # answer must not read as a lost check.
    if out.returncode != 0:
        raise subprocess.CalledProcessError(out.returncode, argv[:3], out.stdout, out.stderr)
    return dict(line.split("=", 1) for line in out.stdout.splitlines() if "=" in line)


def _waiting(info: dict[str, str]) -> bool:
    """Whether a process of the unit is blocked on a flock or POSIX lock held outside the unit.
    /proc/locks lists a held lock as `N: FLOCK ... PID ...` and each request blocked on it as
    `N: -> FLOCK ... PID ...`; a lock held inside the unit is the check's own deadlock."""
    try:
        procs = (CGROUPS / info.get("ControlGroup", "").lstrip("/") / "cgroup.procs").read_text()
        locks = LOCKS.read_text()
    except OSError:
        return False
    ours = set(procs.split())
    held: dict[str, set[str]] = {}
    blocked: set[str] = set()
    for fields in map(str.split, locks.splitlines()):
        if fields[1:2] == ["->"]:
            if fields[5] in ours:
                blocked.add(fields[0])
        elif len(fields) > 4:
            held.setdefault(fields[0], set()).add(fields[4])
    return any(held.get(n, set()) - ours for n in blocked)


def _available_gb() -> float | None:
    try:
        for line in MEMINFO.read_text().splitlines():
            if line.startswith("MemAvailable:"):
                return int(line.split()[1]) / (1024 * 1024)
    except (OSError, ValueError, IndexError):
        pass
    return None


def memory_wait(need_gb: float | None) -> str | None:
    """Why a check may not start now for lack of memory, or None. A check that cannot read
    MemAvailable starts rather than wait on nothing."""
    if need_gb is not None and (have := _available_gb()) is not None and have < need_gb:
        return f"available memory {have:.1f} GB is below {need_gb:g} GB"
    return None


def _marker(state: Path) -> dict:
    try:
        marker = json.loads((state / WAITING).read_text())
    except (OSError, ValueError):
        return {}
    return marker if isinstance(marker, dict) else {}


def _wait(state: Path, reason: str, limit_s: float, lost: int, requeued: bool) -> dict:
    """Records a start held for memory. `since` survives the retries, so the wait has an end:
    past limit_s the answer is wait_timeout, and the next start begins a new wait. `lost` and
    `requeued` are the restart state of the check this start replaces; the wait outlives its
    check.json, so the marker carries them to the start that follows."""
    marker = state / WAITING
    try:
        since = float(_marker(state)["since"])
    except (KeyError, TypeError, ValueError):
        since = time.time()
    if time.time() - since >= limit_s:
        marker.unlink(missing_ok=True)
        return {"status": "wait_timeout", "output": reason}
    state.mkdir(parents=True, exist_ok=True)
    marker.write_text(
        json.dumps({"since": since, "reason": reason, "lost": lost, "requeued": requeued})
    )
    return {"status": "waiting", "reason": reason}


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


def _write_job(state: Path, job: dict) -> None:
    tmp = state / "check.json.tmp"
    tmp.write_text(json.dumps(job))
    os.replace(tmp, state / CHECK_JOB)


def _clear(state: Path) -> None:
    # Not WAITING: step clears a missing job on every poll, and the wait must outlive that.
    for name in (CHECK_JOB, CHECK_LOG, "check.json.tmp"):
        (state / name).unlink(missing_ok=True)


def stop_job(state: Path, key: str) -> None:
    """Ends the issue's check and forgets it; for a release and for removing the worktree."""
    _stop(unit_name(key))
    _clear(state)
    (state / WAITING).unlink(missing_ok=True)


def _start(
    state: Path,
    wt: Path,
    key: str,
    command: str,
    timeout_s: float,
    lost: int,
    requeued: bool = False,
    restore: dict | None = None,
    req: dict | None = None,
) -> dict:
    # `restore` is put back if systemd-run refuses, so a refused rerun keeps the lost count.
    head = git("rev-parse", "HEAD", cwd=wt)
    # Only commits are pushed and guarded; a check on the working tree would pass on files the PR
    # leaves out.
    if dirty := git("status", "--porcelain", cwd=wt):
        return {"status": "dirty", "output": dirty, "head": head}
    if req is not None and (reason := memory_wait(req.get("min_mem_gb"))):
        # The old record goes, so the next poll finds none and asks again; the marker holds the
        # restart state meanwhile.
        _clear(state)
        wait_s = float(req.get("wait_s", 2 * timeout_s))
        return _wait(state, reason, wait_s, lost, requeued)
    (state / WAITING).unlink(missing_ok=True)
    unit, log = unit_name(key), state / CHECK_LOG
    # A unit left by a start whose record was lost would refuse the name.
    _stop(unit)
    state.mkdir(parents=True, exist_ok=True)
    now = time.time()
    _write_job(
        state,
        {
            "started": now,
            "head": head,
            "lost": lost,
            "requeued": requeued,
            "polled": now,
            "waited": 0,
        },
    )
    env = _env()
    argv = [
        "systemd-run",
        "--user",
        "--quiet",
        f"--unit={unit}",
        # Otherwise systemd expands $ in the command, and `$$` or `${X}` reach it changed.
        "--expand-environment=no",
        # The exit status stays readable after the process ends, until the poll stops the unit.
        "--property=RemainAfterExit=yes",
        # Also ends a check no poll reaches, as during a host episode.
        f"--property=RuntimeMaxSec={max(1, math.ceil(timeout_s)) + WAIT_MAX_S}",
        "--property=KillSignal=SIGINT",
        f"--property=TimeoutStopSec={STOP_GRACE_S}",
        # Max counts page cache, which the kernel reclaims, so it ends a check only once swap is
        # full too; SwapMax bounds that, so a runaway dies alone instead of filling the swap every
        # session shares.
        *(f"--property={p}={req[k]}" for p, k in _LIMITS if req and req.get(k)),
        f"--property=WorkingDirectory={wt}",
        f"--property=StandardOutput=truncate:{log}",
        "--property=StandardError=inherit",
        *(f"--setenv={k}={env[k]}" for k in _PASS if k in env),
        *(f"--setenv={kv}" for kv in _NO_BUILD_SERVERS),
        "--",
        *shlex.split(command),
    ]
    started = _run(argv)
    if started.returncode != 0:
        _clear(state)
        if restore is not None:
            _write_job(state, restore)
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
        # A wait that outlived its check.json keeps the restart state of the check it replaced.
        held = _marker(state)
        return _start(
            state,
            wt,
            key,
            command,
            timeout_s,
            int(held.get("lost", 0)),
            bool(held.get("requeued", False)),
            req=req,
        )
    unit = unit_name(key)
    # A check of older commits says nothing about these.
    if job["head"] != git("rev-parse", "HEAD", cwd=wt):
        stop_job(state, key)
        return _start(state, wt, key, command, timeout_s, 0, req=req)
    info = _show(unit)
    if info.get("LoadState", "not-found") == "not-found":
        if time.time() - float(job["started"]) < START_GRACE_S:
            return {"status": "running"}
        # A lost check is rerun once, like a session found dead; the second loss is reported.
        lost = int(job.get("lost", 0)) + 1
        if lost >= 2:
            _clear(state)
            return {"status": "lost"}
        return _start(state, wt, key, command, timeout_s, lost, restore=job, req=req)
    if info.get("SubState") in _RUNNING:
        now, waiting = time.time(), _waiting(info)
        # Only a span with the unit blocked at both polls counts as waiting, so a check that ran
        # for a while and then blocked is never credited time it spent running.
        waited = float(job.get("waited", 0))
        if waiting and job.get("waiting"):
            waited += min(now - float(job.get("polled", now)), WAIT_CREDIT_MAX_S)
        job.update(polled=now, waiting=waiting, waited=waited)
        _write_job(state, job)
        if waiting or now - float(job["started"]) - waited <= timeout_s:
            return {"status": "running"}
        status = "timeout"
    elif info.get("Result") == "timeout":
        # Stopped by RuntimeMaxSec while it waited: the lock's holder ran long, not this check,
        # so it goes back to the lock once.
        if job.get("waiting") and not job.get("requeued"):
            stop_job(state, key)
            lost = int(job.get("lost", 0))
            return _start(state, wt, key, command, timeout_s, lost, True, req=req)
        status = "timeout"
    else:
        ok = info.get("Result") == "success" and info.get("ExecMainStatus") == "0"
        status = "ok" if ok else "failed"
    out = _tail(state)
    if status == "timeout" and job.get("waiting"):
        hours = WAIT_MAX_S // 3600
        out += f"\nStopped while still waiting on a lock, after more than {hours} h of waiting.\n"
    stop_job(state, key)
    return {"status": status, "output": out, "head": job["head"]}
