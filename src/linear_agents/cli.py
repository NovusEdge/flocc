import argparse
import json
import os
import sys
import time
from pathlib import Path

from linear_agents import usage


def _state_dir() -> Path:
    return Path(os.environ["LINEAR_AGENTS_STATE"])


def cmd_statusline(args) -> int:
    payload = json.load(sys.stdin)
    state = _state_dir()
    usage.write_status(state, payload)
    limits = payload.get("rate_limits", {})
    now = time.time()
    five = usage.window_pct(limits.get("five_hour"), now)
    week = usage.window_pct(limits.get("seven_day"), now)
    ctx = usage.context_pct(payload)
    ctx_text = f"{ctx:.0f}%" if ctx is not None else "?"
    print(f"{state.name} · ctx {ctx_text} · 5h {five:.0f}% · wk {week:.0f}%")
    return 0


COMMANDS = {"statusline": cmd_statusline}


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="linear-agents")
    parser.add_argument("--config", default=None)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("statusline")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as exc:
        return int(exc.code or 0)
    return COMMANDS[args.command](args)
