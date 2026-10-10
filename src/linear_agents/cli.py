import argparse
import json
import os
import re
import subprocess
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
    five, week = usage.limit_pcts(payload, time.time())
    ctx = usage.context_pct(payload)
    print(f"{state.name} · ctx {_pct(ctx)} · 5h {_pct(five)} · wk {_pct(week)}")
    return 0


def _pct(value: float | None) -> str:
    return "?" if value is None else f"{value:.0f}%"


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


# Claude Code's own attribution; the session settings turn it off, this catches what slips by.
_CLAUDE_ATTRIBUTION = re.compile(r"^(Claude-Session:|Co-Authored-By: Claude)", re.IGNORECASE)


def _strip_attribution(message_file: str) -> None:
    path = Path(message_file)
    lines = path.read_text().splitlines(keepends=True)
    kept = [ln for ln in lines if not _CLAUDE_ATTRIBUTION.match(ln)]
    if len(kept) == len(lines):
        return
    while kept and not kept[-1].strip():
        kept.pop()
    path.write_text("".join(kept))


def _commit_msg(message_file: str, trailer_on: bool) -> int:
    _strip_attribution(message_file)
    if not trailer_on:
        return 0
    key = os.environ.get("LINEAR_AGENTS_STATE")
    phase = os.environ.get("LINEAR_AGENTS_PHASE")
    # A commit made by hand in the worktree has neither; it stays the owner's plain commit.
    if not key or not phase or phase == "review":
        return 0
    trailer = f"Linear-Agents: {Path(key).name} {phase}"
    # doNothing matches on the key alone: an amend keeps the phase it was first committed in, and
    # never stacks a second copy. Rebase and cherry-pick skip commit-msg, so they keep it as is.
    subprocess.run(
        [
            "git",
            "interpret-trailers",
            "--in-place",
            "--if-exists",
            "doNothing",
            "--trailer",
            trailer,
            message_file,
        ],
        check=True,
    )
    return 0


def cmd_hook(args) -> int:
    from linear_agents.worktree import git

    if args.name == "commit-msg":
        return _commit_msg(args.message_file, not args.no_trailer)
    top = Path.cwd()
    top = Path(git("rev-parse", "--show-toplevel", cwd=top))
    con = _ledger(args)
    mine = ledger.claim_for_worktree(con, str(top))
    if mine is None:
        return 0
    if mine.phase == "review":
        print("linear-agents: the review session is read-only; do not commit", file=sys.stderr)
        return 1
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


def cmd_context(args) -> int:
    from linear_agents.sessions import session_name

    state = _state_dir()
    context = state / "context.md"
    if context.exists():
        print(context.read_text())
    # Peers come from the live ledger, so a session sees issues admitted after it started.
    live = ledger.live_claims(_ledger(args))
    mine = next((c for c in live if c.issue_key == state.name), None)
    peers = [c for c in live if mine and c.repo == mine.repo and c.issue_key != state.name]
    names = ", ".join(session_name(c.issue_key, c.phase) for c in peers) or "none"
    print(
        f"## Peers in this repo\n\n{names}. ListAgents shows them under generated names; match"
        " on the tmux session name it lists. Message no other session.\n"
    )
    handoff = state / "handoff.md"
    if handoff.exists():
        print(handoff.read_text())
    return 0


def cmd_done(args) -> int:
    state = _state_dir()
    (state / "done.json").write_text(json.dumps({"summary": args.summary, "at": time.time()}))
    print("phase marked done; the service takes over on its next run")
    return 0


def cmd_review_done(args) -> int:
    state = _state_dir()
    review = state / "review.md"
    if not review.exists() or not review.read_text().strip():
        print(f"review-done: write {review} first", file=sys.stderr)
        return 1
    (state / "done.json").write_text(json.dumps({"summary": "review", "at": time.time()}))
    print("review marked done; the service posts it on its next run")
    return 0


def cmd_handoff_ready(args) -> int:
    state = _state_dir()
    if not (state / "handoff.md").exists():
        print(f"handoff-ready: write {state / 'handoff.md'} first", file=sys.stderr)
        return 1
    (state / "handoff-ready").touch()
    return 0


def _config(args):
    path = Path(args.config) if args.config else default_config_path()
    return load(path), path


def _lock_path(cfg):
    return cfg.ledger_path.with_suffix(".lock")


def cmd_tick(args) -> int:
    from linear_agents.tick import tick, tick_lock

    cfg, path = _config(args)
    with tick_lock(_lock_path(cfg)) as held:
        if not held:
            print("tick: another tick is running; skipped", file=sys.stderr)
            return 0
        return tick(cfg, path)


def cmd_run(args) -> int:
    from linear_agents.tick import run_loop

    cfg, _ = _config(args)
    config = ["--config", args.config] if args.config else []
    run_loop([sys.argv[0], *config, "tick"], cfg.state_root / "run.log", 180)
    return 0


def cmd_release(args) -> int:
    from linear_agents.poller import release_claim
    from linear_agents.tick import build_runtime, tick_lock

    cfg, path = _config(args)
    with tick_lock(_lock_path(cfg)) as held:
        if not held:
            print("release: a tick is running; try again in a moment", file=sys.stderr)
            return 1
        rt = build_runtime(cfg, path)
        claim = ledger.get_claim(rt.con, args.key)
        if claim is None:
            print(f"release: no claim for {args.key}", file=sys.stderr)
            return 1
        release_claim(rt, claim, keep_worktree=args.keep_worktree)
    print(f"released {args.key}, branch {claim.branch} kept")
    return 0


def cmd_doctor(args) -> int:
    from linear_agents.doctor import run_doctor

    try:
        cfg, path = _config(args)
    except (OSError, ValueError, TypeError) as exc:
        print(f"FAIL  config: {exc}")
        return 1
    print(f"ok    config: {path}")
    return run_doctor(cfg)


def cmd_status(args) -> int:
    from linear_agents.sessions import Sessions
    from linear_agents.status import collect

    cfg, _ = _config(args)
    report = collect(cfg, Sessions())
    if args.json:
        print(json.dumps(report))
        return 0
    print(f"{report['host']} ({report['mode']}), cap {report['session_cap']}")
    for c in report["claims"]:
        alive = "up" if c["tmux_alive"] else "down"
        print(f"  {c['issue']}  {c['phase']}  {c['state']}  ctx {_pct(c['context_pct'])}  {alive}")
    if report["ledger_error"]:
        print(f"  ledger unreadable: {report['ledger_error']}")
    elif not report["claims"]:
        print("  no live claims")
    return 0


COMMANDS = {
    "statusline": cmd_statusline,
    "escalate": cmd_escalate,
    "hook": cmd_hook,
    "claim": cmd_claim,
    "context": cmd_context,
    "done": cmd_done,
    "review-done": cmd_review_done,
    "handoff-ready": cmd_handoff_ready,
    "tick": cmd_tick,
    "run": cmd_run,
    "release": cmd_release,
    "doctor": cmd_doctor,
    "status": cmd_status,
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
    hook.add_argument("name", choices=["pre-commit", "commit-msg"])
    hook.add_argument("--no-trailer", action="store_true")
    hook.add_argument("message_file", nargs="?")
    claim = sub.add_parser("claim")
    claim.add_argument("globs", nargs="+")
    sub.add_parser("context")
    done = sub.add_parser("done")
    done.add_argument("--summary", required=True)
    sub.add_parser("review-done")
    sub.add_parser("handoff-ready")
    sub.add_parser("tick")
    sub.add_parser("run")
    rel = sub.add_parser("release")
    rel.add_argument("key")
    rel.add_argument("--keep-worktree", action="store_true")
    sub.add_parser("doctor")
    status = sub.add_parser("status")
    status.add_argument("--json", action="store_true")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
    except SystemExit as exc:
        return int(exc.code or 0)
    return COMMANDS[args.command](args)
