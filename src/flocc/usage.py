import json
import os
from dataclasses import dataclass
from pathlib import Path

from flocc import agentfs

_CONTEXT_PATH = ("context_window", "used_percentage")


@dataclass(frozen=True)
class Usage:
    five_hour_pct: float
    weekly_pct: float
    observed_at: float


def write_status(state_dir: Path, payload: dict) -> None:
    state_dir.mkdir(parents=True, exist_ok=True)
    tmp = state_dir / "status.json.tmp"
    tmp.write_text(json.dumps(payload))
    os.replace(tmp, state_dir / "status.json")


def read_status(state_dir: Path) -> dict | None:
    try:
        data = json.loads(agentfs.read_text(state_dir, "status.json") or "null")
    except (OSError, ValueError):
        return None
    return data if isinstance(data, dict) else None


def window_pct(window: object, now: float) -> float | None:
    # Claude Code drops a window once resets_at passes, but a stale file still holds it.
    if not window:
        return 0.0
    if not isinstance(window, dict):
        return None
    resets, used = window.get("resets_at"), window.get("used_percentage")
    if not isinstance(resets, int | float):
        return None
    if resets <= now:
        return 0.0
    if not isinstance(used, int | float):
        return None
    return float(used)


def limit_pcts(status: dict, now: float) -> tuple[float | None, float | None]:
    limits = status.get("rate_limits", {})
    if not isinstance(limits, dict):
        return None, None
    return window_pct(limits.get("five_hour"), now), window_pct(limits.get("seven_day"), now)


def latest_usage(state_root: Path, now: float) -> Usage | None:
    best: tuple[float, dict] | None = None
    try:
        keys = agentfs.entries(state_root)
    except OSError:  # flocc-agent replaced the directory with a link
        return None
    for key in keys:
        # Each launch deletes the previous session's status file, so it can vanish mid-scan.
        try:
            mtime = agentfs.mtime(state_root / key, "status.json")
            data = read_status(state_root / key)
        except OSError:
            continue
        if mtime is None or data is None:
            continue
        if "rate_limits" in data and (best is None or mtime > best[0]):
            best = (mtime, data)
    if best is None:
        return None
    five, week = limit_pcts(best[1], now)
    # Not 0%: admission would start sessions on a reading it cannot interpret.
    if five is None or week is None:
        return None
    return Usage(five_hour_pct=five, weekly_pct=week, observed_at=best[0])


def context_pct(status: dict) -> float | None:
    node: object = status
    for key in _CONTEXT_PATH:
        if not isinstance(node, dict) or key not in node:
            return None
        node = node[key]
    if not isinstance(node, int | float):
        return None
    return float(node)
