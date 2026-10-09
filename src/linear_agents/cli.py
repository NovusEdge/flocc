import argparse
import json
import os
import sys
import time
from pathlib import Path

from linear_agents import usage
from linear_agents.escalation import Escalation, validate


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


def cmd_escalate(args) -> int:
    state = _state_dir()
    esc = Escalation(
        tier=args.tier,
        cause=args.cause,
        summary=args.summary,
        action=args.action or "",
        issue_key=state.name,
        default=args.default,
        deadline_min=args.deadline_min,
        waiting=args.waiting,
        scope="global" if args.is_global else "issue",
    )
    try:
        validate(esc)
    except ValueError as exc:
        print(f"escalate: {exc}", file=sys.stderr)
        return 2
    state.mkdir(parents=True, exist_ok=True)
    with (state / "escalations.jsonl").open("a") as f:
        f.write(esc.to_json() + "\n")
    print("recorded; the service routes it on its next run")
    return 0


COMMANDS = {"statusline": cmd_statusline, "escalate": cmd_escalate}


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="linear-agents")
    parser.add_argument("--config", default=None)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("statusline")
    esc = sub.add_parser("escalate")
    esc.add_argument("--tier", type=int, required=True)
    esc.add_argument("--cause", required=True)
    esc.add_argument("--summary", required=True)
    esc.add_argument("--action")
    esc.add_argument("--default")
    esc.add_argument("--deadline-min", type=int)
    esc.add_argument("--waiting", action="store_true")
    esc.add_argument("--global", dest="is_global", action="store_true")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as exc:
        return int(exc.code or 0)
    return COMMANDS[args.command](args)
