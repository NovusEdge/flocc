import calendar
import os
import re
import shutil
import socket
import sqlite3
import subprocess
import time
from collections.abc import Callable
from pathlib import Path

from flocc import agentfs, events, ledger, usage
from flocc.config import Config
from flocc.sequence import ARCHETYPE
from flocc.sessions import session_name
from flocc.tokens import claude_login

_TICK = re.compile(r"^--- tick (\S+)Z$")
_EXIT = re.compile(r"^--- exit (.*)$")
# A tick's own output sits between the markers, so the last pair can be far from the end.
_LOG_TAIL = 256 * 1024
# The desktop gives each host 20s for the whole report; a hung claude must not make it unreachable.
_LOGIN_TIMEOUT = 5


def age_text(seconds: float) -> str:
    if seconds < 60:
        return f"{seconds:.0f}s"
    if seconds < 3600:
        return f"{seconds / 60:.0f}m"
    if seconds < 172800:
        return f"{seconds / 3600:.0f}h"
    return f"{seconds / 86400:.0f}d"


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
_JOURNAL_LINE = re.compile(r"^(\d+(?:\.\d+)?)\s+\S+\s+(?:([\w.@-]+)\[\d+\]: )?(.*)$")
# `-u flocc` also returns the sudo and PAM lines of every flocc-agent-run call.
# Only the routine ones go: a refused command or a password prompt still shows. The filter runs
# after journalctl's -n window, so a busy host's sudo lines shrink the lines that remain.
_ROUTINE_SUDO = re.compile(r"^\s*\S+ : PWD=|^pam_unix\(sudo:session\): session (?:opened|closed)")


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
        return [
            (int(float(m.group(1))), m.group(3))
            for m in parsed
            if m and not (m.group(2) == "sudo" and _ROUTINE_SUDO.match(m.group(3)))
        ]
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


def _read_queue(
    path: Path, now: float
) -> tuple[list[ledger.Claim], dict[str, tuple[str, float]], tuple]:
    """Finished claims, holds and (unacked, oldest unacked time, needing attention). Each table is
    read on its own: a ledger the new code has not migrated lacks holds, events or printed_at, and
    that must not hide the finished runs. _read_ledger reports an unreadable ledger."""
    nothing: tuple = (0, None, 0)
    if not path.exists():
        return [], {}, nothing
    try:
        con = sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=2)
    except sqlite3.Error:
        return [], {}, nothing
    con.row_factory = sqlite3.Row

    def read(fn, default):
        try:
            return fn()
        except sqlite3.Error:
            return default

    try:
        finished = read(lambda: ledger.finished_claims(con), [])
        holds = read(lambda: ledger.issue_holds(con), {})
        counts = read(
            lambda: tuple(
                con.execute(
                    "SELECT COUNT(*), MIN(at),"
                    " COUNT(CASE WHEN printed_at IS NOT NULL AND at <= ? THEN 1 END)"
                    " FROM events WHERE acked_at IS NULL",
                    (now - events.ATTENTION_S,),
                ).fetchone()
            ),
            nothing,
        )
    finally:
        con.close()
    return finished, holds, counts


def pr_state(pr_view: Callable[[ledger.Claim, str], dict] | None, claim, url: str) -> str:
    """The PR's state on GitHub (open, merged, closed), "unknown" when GitHub did not answer, ""
    when nothing was asked: no reader, no PR, or a run that is not done."""
    if pr_view is None or not url or url == "unknown" or claim.state != "done":
        return ""
    try:
        return str(pr_view(claim, url)["state"]).lower()
    except Exception:  # noqa: BLE001 - gh, its token and the network all fail alike; the row stays
        return "unknown"


def _finished_row(cfg: Config, claim: ledger.Claim, now: float, pr_view=None) -> dict:
    url_file = cfg.state_root / claim.issue_key / "pr-url"
    url = url_file.read_text().strip() if url_file.is_file() else ""
    return {
        "pr_state": pr_state(pr_view, claim, url),
        "issue": claim.issue_key,
        "state": claim.state,
        "phase": claim.phase,
        "archetype": ARCHETYPE.get(claim.phase, claim.phase),
        "fix_attempts": claim.fix_attempts,
        "started_age_s": max(0.0, now - claim.started_at),
        "pr_url": url if url != "unknown" else "",
    }


def _claim_row(cfg: Config, claim: ledger.Claim, sessions, now: float) -> dict:
    agent = cfg.agent_state_root / claim.issue_key
    status = usage.read_status(agent)
    try:
        updated = agentfs.mtime(agent, "status.json")
    except OSError:
        updated = None
    return {
        "issue": claim.issue_key,
        "state": claim.state,
        "phase": claim.phase,
        "archetype": ARCHETYPE.get(claim.phase, claim.phase),
        "fix_attempts": claim.fix_attempts,
        "sequence": claim.sequence,
        "started_age_s": max(0.0, now - claim.started_at),
        "updated_age_s": None if updated is None else max(0.0, now - updated),
        "tmux_alive": sessions.alive(session_name(claim.issue_key, claim.phase)),
        # The session ended and the repo's check runs in the background.
        "checking": agentfs.exists(agent, "check.json"),
        "context_pct": usage.context_pct(status) if status else None,
    }


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


def running_commit(cfg: Config) -> str | None:
    # flocc ship writes this beside the published plugin, from the commit it installs.
    try:
        sha = (cfg.plugin_dir.parent / ".flocc-commit").read_text().strip()
    except OSError:
        return None
    return sha or None


def collect(
    cfg: Config,
    sessions,
    now: float | None = None,
    mode: str | None = None,
    run=subprocess.run,
    claude: list[str] | None = None,
    pr_view: Callable[[ledger.Claim, str], dict] | None = None,
) -> dict:
    """Reads host state without taking the tick lock or touching Linear. GitHub is asked only
    through pr_view, for the PR of each finished run: the desktop's report passes none."""
    now = time.time() if now is None else now
    mode = mode or detect_mode()
    tick = systemd_tick(run) if mode == "systemd" else last_tick(cfg.state_root / "run.log")
    if tick is not None and tick["at"] is not None:
        tick["age_s"] = max(0.0, now - tick["at"])
    latest = usage.latest_usage(cfg.agent_state_root, now)
    claims, expiry, ledger_error = _read_ledger(cfg.ledger_path)
    rows = [_claim_row(cfg, c, sessions, now) for c in claims]
    finished, holds, (unacked, oldest, attention) = _read_queue(cfg.ledger_path, now)
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
        "commit": running_commit(cfg),
        "mode": mode,
        "now": now,
        "last_tick": tick,
        "session_cap": cfg.session_cap,
        "check_fix_rounds": cfg.check_fix_rounds,
        "finished": [_finished_row(cfg, c, now, pr_view) for c in finished],
        "holds": [
            {"issue": k, "reason": reason, "age_s": max(0.0, now - at)}
            for k, (reason, at) in holds.items()
        ],
        "events": {
            "unacked": unacked,
            "oldest_age_s": None if oldest is None else max(0.0, now - oldest),
            "attention": attention,
        },
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
