import calendar
import os
import re
import shutil
import socket
import sqlite3
import subprocess
import time
from pathlib import Path

from flocc import ledger, usage
from flocc.config import Config
from flocc.gate import BUDGETS, launches_used, run_id
from flocc.grants import for_phase
from flocc.sessions import session_name
from flocc.tokens import claude_login

_TICK = re.compile(r"^--- tick (\S+)Z$")
_EXIT = re.compile(r"^--- exit (.*)$")
# A tick's own output sits between the markers, so the last pair can be far from the end.
_LOG_TAIL = 256 * 1024
# The desktop gives each host 20s for the whole report; a hung claude must not make it unreachable.
_LOGIN_TIMEOUT = 5


def last_tick(log: Path) -> dict | None:
    try:
        with log.open("rb") as f:
            f.seek(0, 2)
            f.seek(max(0, f.tell() - _LOG_TAIL))
            lines = f.read().decode(errors="replace").splitlines()
    except OSError:
        return None
    tick: dict | None = None
    for line in lines:
        if m := _TICK.match(line):
            tick = {"at": _parse_utc(m.group(1)), "exit": None}
        elif (m := _EXIT.match(line)) and tick is not None:
            tick["exit"] = m.group(1)
    return tick


def _parse_utc(text: str) -> float | None:
    try:
        return float(calendar.timegm(time.strptime(text, "%Y-%m-%dT%H:%M:%S")))
    except ValueError:
        return None


_SYSTEMD_PROPS = "ExecMainStartTimestamp,ExecMainExitTimestamp,ExecMainStatus,ActiveState"


def _parse_systemd_ts(text: str) -> float | None:
    # "Sat 2026-01-01 00:03:00 UTC": systemd prints the zone name, which is UTC or the host's own.
    parts = text.split()
    if len(parts) < 4:
        return None
    try:
        t = time.strptime(f"{parts[1]} {parts[2]}", "%Y-%m-%d %H:%M:%S")
    except ValueError:
        return None
    return float(calendar.timegm(t) if parts[3] == "UTC" else time.mktime(t))


def systemd_tick(run=subprocess.run) -> dict | None:
    env = {**os.environ, "XDG_RUNTIME_DIR": f"/run/user/{os.getuid()}"}
    try:
        out = run(
            ["systemctl", "--user", "show", "flocc.service", "-p", _SYSTEMD_PROPS],
            capture_output=True,
            text=True,
            env=env,
            timeout=10,
            check=False,
        ).stdout
    except (OSError, subprocess.SubprocessError):
        return None
    props = dict(line.split("=", 1) for line in out.splitlines() if "=" in line)
    at = _parse_systemd_ts(props.get("ExecMainStartTimestamp", ""))
    if at is None:
        return None
    running = props.get("ActiveState") in ("active", "activating")
    return {"at": at, "exit": None if running else props.get("ExecMainStatus")}


def _read_ledger(path: Path) -> tuple[list[ledger.Claim], dict[str, str], str | None]:
    # Not ledger.connect: that creates the file, switches the journal mode and runs the schema.
    # mode=ro still creates the -shm and -wal files beside a WAL ledger, but never changes the
    # ledger file itself.
    if not path.exists():
        return [], {}, None
    try:
        con = sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=2)
        con.row_factory = sqlite3.Row
        try:
            expiry = {
                name: value
                for name in ("agent", "publish")
                if (value := ledger.meta_get(con, f"token_expiry:{name}")) is not None
            }
            return ledger.live_claims(con), expiry, None
        finally:
            con.close()
    except sqlite3.Error as exc:
        return [], {}, str(exc)


# Matches `logs`; bounded so a status call stays inside the desktop's 20s budget.
_ACTIVITY_LINES = 2000
_ACTIVITY_KEPT = 5
_JOURNAL_LINE = re.compile(r"^(\d+(?:\.\d+)?)\s+\S+\s+(?:[\w.@-]+\[\d+\]: )?(.*)$")


def _recent_lines(cfg: Config, mode: str, run) -> list[tuple[int, str]]:
    if mode == "systemd":
        env = {**os.environ, "XDG_RUNTIME_DIR": f"/run/user/{os.getuid()}"}
        argv = ["journalctl", "--user", "-u", "flocc", "-n", str(_ACTIVITY_LINES)]
        out = run(
            [*argv, "--no-pager", "-o", "short-unix"],
            capture_output=True,
            text=True,
            env=env,
            timeout=10,
            check=False,
        ).stdout
        parsed = (_JOURNAL_LINE.match(line) for line in out.splitlines())
        return [(int(float(m.group(1))), m.group(2)) for m in parsed if m]
    log = cfg.state_root / "run.log"
    # run.log lines carry no timestamp, so they all take the file's last write time.
    try:
        at = int(log.stat().st_mtime)
        with log.open("rb") as f:
            f.seek(0, 2)
            f.seek(max(0, f.tell() - _LOG_TAIL))
            tail = f.read().decode(errors="replace").splitlines()
    except FileNotFoundError:
        # A loop host writes run.log on its first tick; before that there is nothing to report.
        return []
    return [(at, line) for line in tail[-_ACTIVITY_LINES:]]


def _naming(key: str) -> re.Pattern:
    # Word-bounded: NF-24 must not match NF-240.
    return re.compile(rf"\b{re.escape(key)}\b")


def _activity(lines: list[tuple[int, str]], key: str) -> list[dict]:
    named = _naming(key)
    hits = [{"at": at, "text": text} for at, text in lines if named.search(text)]
    return hits[-_ACTIVITY_KEPT:]


def issue_lines(
    cfg: Config, key: str, n: int, mode: str | None = None, run=subprocess.run
) -> list[tuple[int, str]]:
    named = _naming(key)
    hits = [
        (at, text)
        for at, text in _recent_lines(cfg, mode or detect_mode(), run)
        if named.search(text)
    ]
    return hits[-n:] if n > 0 else []


def _claim_row(cfg: Config, claim: ledger.Claim, sessions, now: float) -> dict:
    state_dir = cfg.state_root / claim.issue_key
    status = usage.read_status(state_dir)
    updated = _mtime(state_dir / "status.json")
    run = run_id(claim.phase, claim.started_at)
    budget = BUDGETS.get(claim.phase)
    granted = for_phase(state_dir / "grants.json", claim.phase).launches
    limit = granted if granted is not None else (budget.launches if budget else 0)
    return {
        "issue": claim.issue_key,
        "state": claim.state,
        "phase": claim.phase,
        "started_age_s": max(0.0, now - claim.started_at),
        "updated_age_s": None if updated is None else max(0.0, now - updated),
        "tmux_alive": sessions.alive(session_name(claim.issue_key, claim.phase)),
        "context_pct": usage.context_pct(status) if status else None,
        "launches": launches_used(state_dir, run),
        "launch_limit": limit,
    }


def _mtime(path: Path) -> float | None:
    try:
        return path.stat().st_mtime
    except OSError:
        return None


def _disk(path: Path) -> dict | None:
    probe = path
    while not probe.exists() and probe != probe.parent:
        probe = probe.parent
    try:
        total, _, free = shutil.disk_usage(probe)
    except OSError:
        return None
    return {"free_bytes": free, "total_bytes": total}


def detect_mode() -> str:
    # The same signal as the installer (steps.DetectMode): a container can hold /run/systemd/system
    # without systemd as PID 1.
    try:
        comm = Path("/proc/1/comm").read_text().strip()
    except OSError:
        comm = ""
    return "systemd" if comm == "systemd" else "loop"


def collect(
    cfg: Config,
    sessions,
    now: float | None = None,
    mode: str | None = None,
    run=subprocess.run,
    claude: str | None = None,
) -> dict:
    """Reads host state without taking the tick lock or touching Linear and GitHub."""
    now = time.time() if now is None else now
    mode = mode or detect_mode()
    tick = systemd_tick(run) if mode == "systemd" else last_tick(cfg.state_root / "run.log")
    if tick is not None and tick["at"] is not None:
        tick["age_s"] = max(0.0, now - tick["at"])
    latest = usage.latest_usage(cfg.state_root, now)
    claims, expiry, ledger_error = _read_ledger(cfg.ledger_path)
    rows = [_claim_row(cfg, c, sessions, now) for c in claims]
    activity_error = None
    if rows:
        try:
            lines = _recent_lines(cfg, mode, run)
        except (OSError, subprocess.SubprocessError) as exc:
            lines, activity_error = [], str(exc) or type(exc).__name__
        for row in rows:
            row["activity"] = _activity(lines, row["issue"])
    report = {
        "v": 1,
        "host": socket.gethostname(),
        "mode": mode,
        "now": now,
        "last_tick": tick,
        "session_cap": cfg.session_cap,
        "claims": rows,
        "ledger_error": ledger_error,
        # The desktop's login takes this lock with flock while it runs.
        "tick_lock": str(cfg.tick_lock),
        "five_hour_max_pct": cfg.five_hour_max_pct,
        "weekly_max_pct": cfg.weekly_max_pct,
        "usage": None
        if latest is None
        else {
            "five_hour_pct": latest.five_hour_pct,
            "weekly_pct": latest.weekly_pct,
            "age_s": max(0.0, now - latest.observed_at),
        },
        "disk": _disk(cfg.state_root),
        # null: not asked. "unknown": asked, and claude gave no answer, which is not a logout.
        "claude_login": None
        if claude is None
        else claude_login(claude, run, _LOGIN_TIMEOUT) or "unknown",
        "token_expiry": expiry,
    }
    if activity_error is not None:
        report["activity_error"] = activity_error
    return report
