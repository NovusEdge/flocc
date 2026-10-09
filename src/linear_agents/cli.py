import argparse
import json
import os
import sys
import time
from pathlib import Path

from linear_agents import ledger, usage
from linear_agents.config import default_config_path, load
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


def _ledger(args):
    env = os.environ.get("LINEAR_AGENTS_LEDGER")
    if env:
        return ledger.connect(env)
    path = Path(args.config) if args.config else default_config_path()
    return ledger.connect(load(path).ledger_path)


def cmd_hook(args) -> int:
    from linear_agents.worktree import git

    top = Path.cwd()
    top = Path(git("rev-parse", "--show-toplevel", cwd=top))
    con = _ledger(args)
    mine = ledger.claim_for_worktree(con, str(top))
    if mine is None:
        return 0
    staged = git("diff", "--cached", "--name-only", cwd=top).splitlines()
    clash = ledger.file_conflicts(staged, ledger.live_claims(con), exclude=mine.issue_key)
    if not clash:
        return 0
    for name, owner in clash.items():
        print(
            f"linear-agents: {name} is claimed by {owner}; message that session or escalate",
            file=sys.stderr,
        )
    return 1


def cmd_claim(args) -> int:
    key = _state_dir().name
    con = _ledger(args)
    try:
        clash = ledger.add_paths(con, key, args.globs)
    except KeyError:
        print(f"claim: no live claim for {key}", file=sys.stderr)
        return 1
    if clash:
        for glob, owner in clash.items():
            print(f"claim: {glob} overlaps {owner}'s claim", file=sys.stderr)
        return 1
    print("claimed")
    return 0


COMMANDS = {
    "statusline": cmd_statusline,
    "escalate": cmd_escalate,
    "hook": cmd_hook,
    "claim": cmd_claim,
}


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
    hook = sub.add_parser("hook")
    hook.add_argument("name", choices=["pre-commit"])
    claim = sub.add_parser("claim")
    claim.add_argument("globs", nargs="+")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as exc:
        return int(exc.code or 0)
    return COMMANDS[args.command](args)
