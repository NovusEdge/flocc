import calendar
import re
import shutil
import socket
import sqlite3
import time
from pathlib import Path

from linear_agents import ledger, usage
from linear_agents.config import Config
from linear_agents.sessions import session_name

_TICK = re.compile(r"^--- tick (\S+)Z$")
_EXIT = re.compile(r"^--- exit (.*)$")
# A tick's own output sits between the markers, so the last pair can be far from the end.
_LOG_TAIL = 256 * 1024


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


def _live_claims(path: Path) -> list[ledger.Claim]:
    # Not ledger.connect: that creates the file, switches the journal mode and runs the schema.
    try:
        con = sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=2)
    except sqlite3.Error:
        return []
    con.row_factory = sqlite3.Row
    try:
        return ledger.live_claims(con)
    except sqlite3.Error:
        return []
    finally:
        con.close()


def _claim_row(cfg: Config, claim: ledger.Claim, sessions, now: float) -> dict:
    state_dir = cfg.state_root / claim.issue_key
    status = usage.read_status(state_dir)
    updated = _mtime(state_dir / "status.json")
    return {
        "issue": claim.issue_key,
        "state": claim.state,
        "phase": claim.phase,
        "started_age_s": max(0.0, now - claim.started_at),
        "updated_age_s": None if updated is None else max(0.0, now - updated),
        "tmux_alive": sessions.alive(session_name(claim.issue_key, claim.phase)),
        "context_pct": usage.context_pct(status) if status else None,
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


def collect(cfg: Config, sessions, now: float | None = None) -> dict:
    """Reads host state without taking the tick lock or touching Linear and GitHub."""
    now = time.time() if now is None else now
    tick = last_tick(cfg.state_root / "run.log")
    if tick is not None and tick["at"] is not None:
        tick["age_s"] = max(0.0, now - tick["at"])
    latest = usage.latest_usage(cfg.state_root, now)
    return {
        "host": socket.gethostname(),
        "mode": "systemd" if Path("/run/systemd/system").is_dir() else "loop",
        "now": now,
        "last_tick": tick,
        "session_cap": cfg.session_cap,
        "claims": [_claim_row(cfg, c, sessions, now) for c in _live_claims(cfg.ledger_path)],
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
    }
