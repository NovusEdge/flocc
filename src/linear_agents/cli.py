import argparse
import fcntl
import json
import os
import re
import subprocess
import sys
import time
from pathlib import Path

from linear_agents import gate, ledger, usage
from linear_agents.config import default_config_path, load
from linear_agents.escalation import Escalation, validate
from linear_agents.grants import for_phase


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
    g = for_phase(state / "grants.json", os.environ.get("LINEAR_AGENTS_PHASE", ""))
    items = [f"launches {g.launches}"] if g.launches is not None else []
    items += [f"agent {a}" for a in g.agents] + [f"workflow {w}" for w in g.workflows]
    items += [f"fix-rounds {g.fix_rounds}"] if g.fix_rounds is not None else []
    print(f"## Grants\n\n{', '.join(items) or 'none'}.\n")
    handoff = state / "handoff.md"
    if handoff.exists():
        print(handoff.read_text())
    return 0


def _hook_input(strict: bool = False) -> dict:
    """Strict raises on input that is not a JSON object; lenient gives {} for it."""
    raw = "" if sys.stdin.isatty() else sys.stdin.read()
    try:
        data = json.loads(raw)
    except ValueError:
        if strict:
            raise
        return {}
    if isinstance(data, dict):
        return data
    if strict:
        raise ValueError("hook input is not a JSON object")
    return {}


def cmd_gate(args) -> int:
    run = os.environ.get("LINEAR_AGENTS_RUN")
    phase = os.environ.get("LINEAR_AGENTS_PHASE")
    # Exit 2 on SubagentStop blocks the stop and keeps the agent alive, so bookkeeping
    # failures exit 1, which Claude Code treats as non-blocking.
    if args.stopped or args.answered:
        try:
            state = _state_dir()
            if args.stopped:
                agent_type = str(_hook_input().get("agent_type", ""))
                gate.record_stop(state, run, time.time(), agent_type, phase)
            else:
                gate.clear_question(state)
        except Exception as exc:  # noqa: BLE001 - must not block the stop
            print(f"linear-agents gate could not record the event: {exc}", file=sys.stderr)
            return 1
        return 0
    # Exit 2 refuses a PreToolUse call in every Claude Code version, whatever the hook's
    # onFailure setting does; any error of the gate's own must refuse, never allow.
    try:
        state = _state_dir()
        payload = _hook_input(strict=True)
        if payload.get("tool_name") == "AskUserQuestion":
            # A question spends nothing, so a bookkeeping failure must not refuse it.
            try:
                gate.note_question(state, payload, time.time())
            except Exception as exc:  # noqa: BLE001
                print(f"linear-agents gate could not record the question: {exc}", file=sys.stderr)
                return 1
            return 0
        flags = frozenset(filter(None, os.environ.get(gate.LAUNCH_GRANTS_ENV, "").split(",")))
        reason = gate.run_gate(payload, state, phase, run, time.time(), flags)
        if reason is not None:
            out = {
                "hookSpecificOutput": {
                    "hookEventName": "PreToolUse",
                    "permissionDecision": "deny",
                    "permissionDecisionReason": reason,
                }
            }
            print(json.dumps(out))
    except Exception as exc:  # noqa: BLE001 - see above
        print(f"linear-agents gate failed, so the launch is refused: {exc}", file=sys.stderr)
        return 2
    return 0


def cmd_idle(args) -> int:
    # Exit 2 on Stop blocks the stop and keeps the session running; see cmd_gate.
    try:
        gate.note_idle(_state_dir(), _hook_input(), time.time())
    except Exception as exc:  # noqa: BLE001 - must not block the stop
        print(f"linear-agents idle could not record the stop: {exc}", file=sys.stderr)
        return 1
    return 0


def _session_bookkeeping(state: Path) -> None:
    state.mkdir(parents=True, exist_ok=True)
    payload = _hook_input()
    if payload.get("source") == "compact":
        # status.json still holds the pre-compaction reading; the supervisor would hand off at once.
        (state / "compacted").touch()
        (state / "status.json").unlink(missing_ok=True)
    else:
        # Background agents survive compaction, so only a real start resets the running count.
        run = os.environ.get("LINEAR_AGENTS_RUN")
        gate.record_session(state, run, time.time(), os.environ.get("LINEAR_AGENTS_PHASE"))
    pad = payload.get("scratchpad_dir")
    if isinstance(pad, str) and pad:
        known = state / "scratchpads"
        listed = known.read_text().splitlines() if known.exists() else []
        if pad not in listed:
            with known.open("a") as f:
                f.write(pad + "\n")


def cmd_session_start(args) -> int:
    # The context is the hook's output; a bookkeeping failure must not take it away.
    try:
        _session_bookkeeping(_state_dir())
    except Exception as exc:  # noqa: BLE001
        print(f"linear-agents session-start bookkeeping failed: {exc}", file=sys.stderr)
    return cmd_context(args)


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


def cmd_commit(args) -> int:
    from linear_agents.worktree import git

    paths = [p for p in args.paths if p != "--"]
    if not paths:
        print("commit: name the paths to commit after --", file=sys.stderr)
        return 2
    git_dir = Path(git("rev-parse", "--absolute-git-dir", cwd=Path.cwd()))
    # Parallel task-implementers share one worktree and its index: one commit at a time, each
    # with only its own paths.
    with (git_dir / "la-commit.lock").open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        added = subprocess.run(["git", "add", "--", *paths], check=False)
        if added.returncode:
            return added.returncode
        commit = ["git", "commit", "-m", args.message, "--", *paths]
        return subprocess.run(commit, check=False).returncode


def _config(args):
    path = Path(args.config) if args.config else default_config_path()
    try:
        return load(path), path
    except (OSError, ValueError, TypeError) as exc:
        print(f"FAIL  config: {exc}")
        raise SystemExit(1) from exc


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
        keep = args.keep_worktree or args.accept_tests
        if args.accept_tests:
            # The rerun resumes at the implementer only with the remembered tester commit, and
            # the acceptance is checked against the branch it came from, so both need the worktree.
            if ledger.remembered_tests(rt.con, args.key) is None:
                print(f"release: {args.key} has no tester commit to accept", file=sys.stderr)
                return 1
            ledger.accept_tests(rt.con, args.key, rt.git_head(claim.worktree))
        release_claim(rt, claim, keep_worktree=keep)
    wt = "kept" if keep else "removed"
    accepted = ", tester files accepted as they stand" if args.accept_tests else ""
    print(f"released {args.key}, branch {claim.branch} kept, worktree {wt}{accepted}")
    return 0


def cmd_doctor(args) -> int:
    from linear_agents.doctor import run_doctor

    cfg, path = _config(args)
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
        line = f"  {c['issue']}  {c['phase']}  {c['state']}  ctx {_pct(c['context_pct'])}  {alive}"
        print(f"{line}  launches {c['launches']}/{c['launch_limit']}")
    if report["ledger_error"]:
        print(f"  ledger unreadable: {report['ledger_error']}")
    elif not report["claims"]:
        print("  no live claims")
    t = report["last_tick"]
    if t is None:
        print("  last tick: none")
    else:
        state = (
            "running" if t["exit"] is None else ("ok" if t["exit"] == "0" else f"exit {t['exit']}")
        )
        age = t.get("age_s")
        print(
            f"  last tick {_age(age)} ago, {state}" if age is not None else f"  last tick {state}"
        )
    u = report["usage"]
    if u is None:
        print("  usage: none yet")
    else:
        print(f"  usage 5h {_pct(u['five_hour_pct'])}  wk {_pct(u['weekly_pct'])}")
    if report["disk"]:
        print(f"  disk {report['disk']['free_bytes'] / 2**30:.1f} GiB free")
    return 0


def _age(seconds: float) -> str:
    if seconds < 60:
        return f"{seconds:.0f}s"
    if seconds < 3600:
        return f"{seconds / 60:.0f}m"
    if seconds < 172800:
        return f"{seconds / 3600:.0f}h"
    return f"{seconds / 86400:.0f}d"


COMMANDS = {
    "statusline": cmd_statusline,
    "escalate": cmd_escalate,
    "hook": cmd_hook,
    "claim": cmd_claim,
    "context": cmd_context,
    "gate": cmd_gate,
    "idle": cmd_idle,
    "session-start": cmd_session_start,
    "commit": cmd_commit,
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
    parser = argparse.ArgumentParser(
        prog="linear-agents", description="Run Claude sessions on Linear issues."
    )
    parser.add_argument("--config", default=None, help="config file (default: standard path)")
    sub = parser.add_subparsers(dest="command", required=True)

    def add(name, text):
        return sub.add_parser(name, help=text, description=text)

    add("statusline", "Record session usage from stdin and print the status line")
    esc = add("escalate", "Record an escalation for the service to route")
    esc.add_argument("--tier", type=int, required=True, help="escalation tier")
    esc.add_argument("--cause", required=True, help="why it is escalated")
    esc.add_argument("--summary", required=True, help="one-line summary")
    esc.add_argument("--action", help="what the human should do")
    esc.add_argument("--default", help="what happens if nobody answers")
    esc.add_argument("--deadline-min", type=int, help="minutes until the default applies")
    esc.add_argument("--waiting", action="store_true", help="session waits for the answer")
    esc.add_argument("--global", dest="is_global", action="store_true", help="affects all issues")
    hook = add("hook", "Git hook entry point")
    hook.add_argument("name", choices=["pre-commit", "commit-msg"], help="which hook is running")
    hook.add_argument("--no-trailer", action="store_true", help="skip the commit trailer")
    hook.add_argument("message_file", nargs="?", help="commit message file (commit-msg)")
    claim = add("claim", "Claim file globs for this issue")
    claim.add_argument("globs", nargs="+", help="file globs to claim")
    add("context", "Print the issue's claims and context")
    gate = add("gate", "Hook: allow or refuse a subagent or workflow launch, track questions")
    which = gate.add_mutually_exclusive_group()
    which.add_argument("--stopped", action="store_true", help="SubagentStop: an agent finished")
    which.add_argument("--answered", action="store_true", help="PostToolUse: a question answered")
    add("idle", "Hook (Stop): record that the session ended its turn, with its last message")
    add("session-start", "Hook: record compaction and the scratchpad, print the context")
    com = add("commit", "Commit only the given paths, one commit at a time per worktree")
    com.add_argument("-m", dest="message", required=True, help="commit message")
    com.add_argument("paths", nargs=argparse.REMAINDER, help="-- then the paths to commit")
    done = add("done", "Mark the work done")
    done.add_argument("--summary", required=True, help="what was done")
    add("review-done", "Mark the review done (reads review.md)")
    add("handoff-ready", "Mark handoff.md ready")
    add("tick", "Run one service tick")
    add("run", "Run ticks in a loop")
    rel = add("release", "Clear a claim and kill its session")
    rel.add_argument("key", help="issue key, e.g. NF-12")
    rel.add_argument("--keep-worktree", action="store_true", help="leave the worktree on disk")
    rel.add_argument(
        "--accept-tests",
        action="store_true",
        help="approve the tester's files as they stand on the branch (keeps the worktree)",
    )
    add("doctor", "Check config and host setup")
    status = add("status", "Show claims, last tick, usage and disk")
    status.add_argument("--json", action="store_true", help="print the raw report as JSON")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
        return COMMANDS[args.command](args)
    except SystemExit as exc:
        return int(exc.code or 0)
