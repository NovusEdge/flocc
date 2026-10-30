import argparse
import fcntl
import json
import os
import re
import signal
import subprocess
import sys
import time
from pathlib import Path

from flocc import idle, ledger, usage
from flocc.config import TRAILER, default_config_path, load


def _state_dir() -> Path:
    return Path(os.environ["FLOCC_STATE"])


_KEY = re.compile(r"[A-Z][A-Z0-9]*-[0-9]+")


def _issue_key(raw: str) -> str:
    # fullmatch: a $ anchor would let a trailing newline through.
    if not _KEY.fullmatch(raw):
        raise argparse.ArgumentTypeError(f"{raw!r} is not an issue key such as NF-12")
    return raw


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
    key = os.environ.get("FLOCC_STATE")
    phase = os.environ.get("FLOCC_PHASE")
    # A commit made by hand in the worktree has neither; it stays the owner's plain commit.
    if not key or not phase or phase == "review":
        return 0
    trailer = f"{TRAILER}: {Path(key).name} {phase}"
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
    if args.name == "commit-msg":
        return _commit_msg(args.message_file, not args.no_trailer)
    # Worktrees prepared by 0.12 still call pre-commit; it no longer checks anything.
    return 0


def cmd_guard(args) -> int:
    from flocc.guard import decide

    if not os.environ.get("FLOCC_PHASE"):
        return 0
    # Exit 0 always: a guard that fails must not stop the session, and it is not the boundary.
    try:
        reason = decide(
            _hook_input(strict=True),
            os.environ.get("FLOCC_PHASE", ""),
            os.environ.get("FLOCC_STATE", ""),
        )
    except Exception as exc:  # noqa: BLE001
        print(f"flocc guard could not judge the call: {exc}", file=sys.stderr)
        return 0
    if reason:
        decision = {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": reason,
        }
        print(json.dumps({"hookSpecificOutput": decision}))
    return 0


def cmd_tests_amended(args) -> int:
    from flocc.publisher import AMENDED
    from flocc.worktree import changed_files, git

    state = _state_dir()
    try:
        tester = json.loads((state / "tester.json").read_text())
        base_sha, tester_sha = str(tester["base_sha"]), str(tester["tester_sha"])
    except (OSError, ValueError, KeyError, TypeError):
        print(
            f"tests-amended: no tester ran for {state.name}, so it has no tester files",
            file=sys.stderr,
        )
        return 1
    top = Path(git("rev-parse", "--show-toplevel", cwd=Path.cwd()))
    tester_files = changed_files(top, base_sha, tester_sha)
    foreign = [p for p in args.paths if p not in tester_files]
    if foreign:
        print(
            f"tests-amended: {', '.join(foreign)} not among the files the tester committed",
            file=sys.stderr,
        )
        return 2
    path = state / AMENDED
    try:
        recorded = json.loads(path.read_text())
    except (OSError, ValueError):
        recorded = {}
    if not isinstance(recorded, dict):
        recorded = {}
    for p in args.paths:
        recorded[p] = {"reason": args.reason}
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(recorded))
    os.replace(tmp, path)
    n = len(args.paths)
    print(f"recorded {n} amended test file{'s' * (n != 1)}")
    return 0


def cmd_context(args) -> int:
    state = _state_dir()
    context = state / "context.md"
    if context.exists():
        print(context.read_text())
    last = state / "last-summary.md"
    if last.exists():
        print(f"## The previous step's summary\n\n{last.read_text()}")
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


STOP_REASON = (
    "flocc done has not run, so this run is not finished. Finish the work and run flocc done,"
    ' or write what needs the owner under a line that starts with "Needs the owner" in your'
    " summary and run flocc done. Nobody answers questions in this session."
)


def cmd_stop(args) -> int:
    # Exit 2 on Stop would block the stop; bookkeeping failures exit 1 so they never do.
    try:
        state = _state_dir()
        payload = _hook_input()
    except Exception as exc:  # noqa: BLE001
        print(f"flocc stop could not read its input: {exc}", file=sys.stderr)
        return 1
    if args.failure:
        # autoContinueAtUsageLimit resumes the turn after the reset.
        if payload.get("error") == "rate_limit":
            return 0
        message = payload.get("last_assistant_message") or str(payload.get("error", ""))
        payload = {"last_assistant_message": message}
    else:
        failed = 0
        try:
            idle.note_background(state, payload)
        except Exception as exc:  # noqa: BLE001
            print(f"flocc stop could not record the background tasks: {exc}", file=sys.stderr)
            failed = 1
        # Background work or a scheduled wakeup brings the session back on its own.
        if (state / "done.json").exists() or idle.has_background(state):
            return failed
        if not payload.get("stop_hook_active"):
            # Hook JSON is read on exit 0 only.
            print(json.dumps({"decision": "block", "reason": STOP_REASON}))
            return 0
    try:
        idle.note_idle(state, payload, time.time())
    except Exception as exc:  # noqa: BLE001
        print(f"flocc stop could not record the stop: {exc}", file=sys.stderr)
        return 1
    return 0


def _session_bookkeeping(state: Path) -> None:
    state.mkdir(parents=True, exist_ok=True)
    payload = _hook_input()
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
        print(f"flocc session-start bookkeeping failed: {exc}", file=sys.stderr)
    return cmd_context(args)


def cmd_done(args) -> int:
    state = _state_dir()
    if os.environ.get("FLOCC_PHASE") == "review":
        review = state / "review.md"
        if not review.exists() or not review.read_text().strip():
            print(f"done: write {review} first; the service posts it", file=sys.stderr)
            return 1
        summary = "review"
    elif not (args.summary or "").strip():
        print("done: pass --summary '<what you did, for the PR body>'", file=sys.stderr)
        return 2
    else:
        summary = args.summary
    (state / "done.json").write_text(json.dumps({"summary": summary, "at": time.time()}))
    print("marked done; the service takes over on its next run")
    return 0


def cmd_commit(args) -> int:
    from flocc.worktree import git

    paths = [p for p in args.paths if p != "--"]
    if not paths:
        print("commit: name the paths to commit after --", file=sys.stderr)
        return 2
    git_dir = Path(git("rev-parse", "--absolute-git-dir", cwd=Path.cwd()))
    # Parallel task-implementers share one worktree and its index: one commit at a time, each
    # with only its own paths.
    with (git_dir / "flocc-commit.lock").open("w") as lock:
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


def cmd_tick(args) -> int:
    from flocc.tick import tick, tick_lock

    cfg, path = _config(args)
    with tick_lock(cfg.tick_lock) as held:
        if not held:
            print("tick: another tick is running; skipped", file=sys.stderr)
            return 0
        return tick(cfg, path)


def cmd_run(args) -> int:
    from flocc.tick import run_loop

    cfg, _ = _config(args)
    config = ["--config", args.config] if args.config else []
    run_loop([sys.argv[0], *config, "tick"], cfg.state_root / "run.log", 180)
    return 0


def cmd_release(args) -> int:
    from flocc.poller import release_claim
    from flocc.tick import build_runtime, tick_lock

    cfg, path = _config(args)
    if args.hold_only:
        return _lift_hold(cfg, args.key)
    with tick_lock(cfg.tick_lock) as held:
        if not held:
            print("release: a tick is running; try again in a moment", file=sys.stderr)
            return 1
        con = ledger.connect(cfg.ledger_path)
        claim = ledger.get_claim(con, args.key)
        if claim is None:
            if ledger.remove_hold(con, args.key):
                print(f"released the hold on {args.key}")
                return 0
            print(f"release: no claim for {args.key}", file=sys.stderr)
            return 1
        rt = build_runtime(cfg, path)
        keep = args.keep_worktree or args.accept_tests
        if args.accept_tests:
            # The rerun resumes at the implementer only with the remembered tester commit, and
            # the acceptance is checked against the branch it came from, so both need the worktree.
            if ledger.remembered_tests(rt.con, args.key) is None:
                print(f"release: {args.key} has no tester commit to accept", file=sys.stderr)
                return 1
            ledger.accept_tests(rt.con, args.key, rt.git_head(claim))
        ledger.remove_hold(rt.con, args.key)
        release_claim(rt, claim, keep_worktree=keep)
    wt = "kept" if keep else "removed"
    accepted = ", tester files accepted as they stand" if args.accept_tests else ""
    print(f"released {args.key}, branch {claim.branch} kept, worktree {wt}{accepted}")
    return 0


def _lift_hold(cfg, key: str) -> int:
    # cardinal's release. Rerunning stopped or finished work is the owner's: with the hold gone,
    # the timed-out restart, or a move to Todo made while it was held, would start it again.
    con = ledger.connect(cfg.ledger_path)
    claim = ledger.get_claim(con, key)
    if claim is not None and claim.state != "running":
        state = claim.state.replace("_", " ")
        print(f"release: {key}'s run is {state}; only the owner reruns it", file=sys.stderr)
        return 1
    if not ledger.remove_hold(con, key):
        print(f"release: {key} has no hold", file=sys.stderr)
        return 1
    print(f"released the hold on {key}")
    return 0


def cmd_hold(args) -> int:
    cfg, _ = _config(args)
    con = ledger.connect(cfg.ledger_path)
    ledger.add_hold(con, args.key, args.reason.strip() or "held by cardinal", time.time())
    claim = ledger.get_claim(con, args.key)
    if claim is not None and claim.state == "running":
        print(
            f"held {args.key}; its run in progress goes on, and nothing starts it again until"
            f" flocc release {args.key}"
        )
    else:
        print(f"held {args.key}; nothing starts it until flocc release {args.key}")
    return 0


def cmd_canary(args) -> int:
    from flocc import canary
    from flocc.agent import Agent
    from flocc.tick import tick_lock

    cfg, _ = _config(args)
    if args.forget:
        n = canary.forget_all(ledger.connect(cfg.ledger_path))
        print(f"canary: cleared {n} main-broken hold{'s' * (n != 1)}")
        return 0
    if args.repo is None:
        print("canary: name a repo, or pass --forget", file=sys.stderr)
        return 2
    if args.repo not in cfg.repos:
        print(
            f"canary: no repo {args.repo} in the config ({', '.join(cfg.repos)})", file=sys.stderr
        )
        return 2
    # ./setup runs under the tick lock, as the poller's does, so the two never share a cache.
    with tick_lock(cfg.tick_lock) as held:
        if not held:
            print("canary: a tick is running; try again in a moment", file=sys.stderr)
            return 1
        con = ledger.connect(cfg.ledger_path)

        # A signal's default action skips setup_at_base's finally, leaving ./setup running and
        # the .canary-* worktree behind.
        def _exit(signum, frame):
            raise SystemExit(128 + signum)

        saved = {sig: signal.signal(sig, _exit) for sig in (signal.SIGTERM, signal.SIGHUP)}
        mark = canary.get_mark(con, args.repo)
        check = cfg.repos[args.repo].check if mark and mark.get("check") else None
        try:
            if check:
                sha, failure = Agent(cfg).check_base(args.repo)
                code = 1 if failure else 0
                out = failure.log or failure.detail if failure else ""
            else:
                code, out, sha = Agent(cfg).setup_base(args.repo)
        except (subprocess.SubprocessError, OSError) as exc:
            why = getattr(exc, "stderr", None) or exc
            print(f"canary: {args.repo}'s base was not checked out: {why}", file=sys.stderr)
            return 1
        finally:
            for sig, handler in saved.items():
                signal.signal(sig, handler)
        ran = f"`{check}`" if check else "./setup"
        if code == 0:
            canary.clear(con, args.repo)
            print(
                f"canary: {ran} passes on {args.repo} at {sha[:9]}; its issues are no longer held"
            )
            return 0
        canary.mark(con, args.repo, sha, time.time(), out, check=check)
        what = "timed out" if code is None else f"exited {code}"
        if check:
            what = "fails"
        print(
            f"canary: {ran} {what} on {args.repo} at {sha[:9]}; its issues stay held\n"
            f"{out[-canary.OUTPUT_CHARS :]}",
            file=sys.stderr,
        )
        return 1


def cmd_doctor(args) -> int:
    from flocc.doctor import doctor_json, run_doctor

    cfg, path = _config(args)
    if args.json:
        return doctor_json(cfg, path)
    print(f"ok    config: {path}")
    return run_doctor(cfg, path)


def cmd_status(args) -> int:
    from flocc.agent import Agent
    from flocc.status import collect

    cfg, _ = _config(args)
    agent = Agent(cfg)
    report = collect(cfg, agent.sessions(), claude=agent.claude_argv())
    if args.json:
        print(json.dumps(report))
        return 0
    print(f"{report['host']} ({report['mode']}), v{_version()}, cap {report['session_cap']}")
    for c in report["claims"]:
        alive = "up" if c["tmux_alive"] else "down"
        print(f"  {c['issue']}  {c['phase']}  {c['state']}  ctx {_pct(c['context_pct'])}  {alive}")
    if report["ledger_error"]:
        print(f"  ledger unreadable: {report['ledger_error']}")
    elif not report["claims"]:
        print("  no live claims")
    if report["claude_login"]:
        print(f"  claude login {report['claude_login']}")
    for name, value in report["token_expiry"].items():
        print(f"  github {name} token {_expiry(value, report['now'])}")
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


def _live_session(args):
    from flocc.agent import Agent
    from flocc.sessions import session_name
    from flocc.status import _read_ledger

    cfg, _ = _config(args)
    claims, _, _ = _read_ledger(cfg.ledger_path)
    claim = next((c for c in claims if c.issue_key == args.key), None)
    sessions = Agent(cfg).sessions()
    name = session_name(claim.issue_key, claim.phase) if claim else None
    if name and sessions.alive(name):
        return sessions, name
    print(f"{args.command}: no live session for {args.key}", file=sys.stderr)
    return None, None


def cmd_peek(args) -> int:
    sessions, name = _live_session(args)
    # Blank rows below the prompt count toward the capture, so ask for more than N.
    text = sessions.capture(name, max(args.lines, 1) + 200) if sessions and name else ""
    rows = text.rstrip().splitlines()
    if not rows:
        if sessions:
            print(f"peek: no live session for {args.key}", file=sys.stderr)
        return 3
    for row in rows[-max(args.lines, 1) :]:
        print("".join(c if c.isprintable() else " " for c in row))
    return 0


def cmd_logs(args) -> int:
    from flocc.status import detect_mode, issue_lines

    cfg, _ = _config(args)
    if args.issue:
        if args.follow:
            print("--issue does not follow; drop -f", file=sys.stderr)
            return 2
        mode = detect_mode()
        try:
            hits = issue_lines(cfg, args.issue, args.lines, mode, subprocess.run)
        except (OSError, subprocess.SubprocessError) as exc:
            print(f"logs: {exc}", file=sys.stderr)
            return 1
        for at, text in hits:
            stamp = f"{time.strftime('%H:%M:%S', time.localtime(at))} " if mode == "systemd" else ""
            print(stamp + text)
        return 0
    follow = ["-f"] if args.follow else []
    lines = ["-n", str(args.lines)]
    if detect_mode() == "systemd":
        argv = ["journalctl", "--user", "-u", "flocc", "--no-pager", *follow, *lines]
    else:
        argv = ["tail", *follow, *lines, str(cfg.state_root / "run.log")]
    os.execvp(argv[0], argv)


def cmd_attach(args) -> int:
    cfg, _ = _config(args)
    argv = [*cfg.agent_run, "attach", args.session]
    os.execvp(argv[0], argv)


def cmd_events(args) -> int:
    from flocc import events

    cfg, _ = _config(args)
    con = ledger.connect(cfg.ledger_path)
    if not args.follow:
        for text in events.lines(events.unacked(con), time.time()):
            print(text)
        return 0
    # sshd ends the session of a vanished client, and the follower is reparented; it notices on
    # its next poll instead of on a write that may be hours away.
    parent = os.getppid()
    try:
        events.follow(
            con,
            lambda text: print(text, flush=True),
            time.time,
            time.sleep,
            alive=lambda: os.getppid() == parent,
            hello=args.hello,
        )
    except BrokenPipeError:
        # Python flushes stdout again at exit and would print a second traceback.
        os.dup2(os.open(os.devnull, os.O_WRONLY), sys.stdout.fileno())
    except KeyboardInterrupt:
        pass
    return 0


def cmd_ack(args) -> int:
    from flocc import events

    cfg, _ = _config(args)
    n, unknown = events.ack(ledger.connect(cfg.ledger_path), args.ids, time.time())
    missing = f"; no event {', '.join(map(str, unknown))}" if unknown else ""
    print(f"acked {n}{missing}")
    return 0


def _expiry(value: str, now: float) -> str:
    if value == "never":
        return "never expires"
    if value == "rejected":
        return "rejected by GitHub"
    at = float(value)
    day = time.strftime("%Y-%m-%d", time.gmtime(at))
    return f"expires {day} ({int((at - now) // 86400)} days)"


def _age(seconds: float) -> str:
    if seconds < 60:
        return f"{seconds:.0f}s"
    if seconds < 3600:
        return f"{seconds / 60:.0f}m"
    if seconds < 172800:
        return f"{seconds / 3600:.0f}h"
    return f"{seconds / 86400:.0f}d"


COMMANDS = {
    "stop": cmd_stop,
    "statusline": cmd_statusline,
    "hook": cmd_hook,
    "guard": cmd_guard,
    "tests-amended": cmd_tests_amended,
    "context": cmd_context,
    "session-start": cmd_session_start,
    "commit": cmd_commit,
    "done": cmd_done,
    "tick": cmd_tick,
    "run": cmd_run,
    "release": cmd_release,
    "canary": cmd_canary,
    "doctor": cmd_doctor,
    "status": cmd_status,
    "logs": cmd_logs,
    "peek": cmd_peek,
    "attach": cmd_attach,
    "events": cmd_events,
    "ack": cmd_ack,
    "hold": cmd_hold,
}


def _version() -> str:
    from importlib.metadata import version

    return version("flocc")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="flocc", description="Run Claude sessions on Linear issues."
    )
    parser.add_argument("--version", action="version", version=f"%(prog)s {_version()}")
    parser.add_argument("--config", default=None, help="config file (default: standard path)")
    sub = parser.add_subparsers(dest="command", required=True)

    def add(name, text):
        return sub.add_parser(name, help=text, description=text)

    add("statusline", "Record session usage from stdin and print the status line")
    hook = add("hook", "Git hook entry point")
    hook.add_argument("name", choices=["pre-commit", "commit-msg"], help="which hook is running")
    hook.add_argument("--no-trailer", action="store_true", help="skip the commit trailer")
    hook.add_argument("message_file", nargs="?", help="commit message file (commit-msg)")
    add("guard", "Hook (PreToolUse): deny dialogs, pushes and GitHub writes")
    amended = add("tests-amended", "Declare tester files you changed because a test was wrong")
    amended.add_argument("paths", nargs="+", help="repo-relative paths of the amended tester files")
    amended.add_argument("--reason", required=True, help="why the test was wrong")
    add("context", "Print the issue's context")
    stop = add("stop", "Hook (Stop, StopFailure): refuse one early stop, then record the idle stop")
    stop.add_argument("--failure", action="store_true", help="the turn ended on an API error")
    add("session-start", "Hook: record the scratchpad, print the context")
    com = add("commit", "Commit only the given paths, one commit at a time per worktree")
    com.add_argument("-m", dest="message", required=True, help="commit message")
    com.add_argument("paths", nargs=argparse.REMAINDER, help="-- then the paths to commit")
    done = add("done", "Mark this step done (the reviewer's review.md, or --summary)")
    done.add_argument("--summary", default=None, help="what was done, for the PR body")
    add("tick", "Run one service tick")
    add("run", "Run ticks in a loop")
    rel = add("release", "Clear a claim and kill its session")
    rel.add_argument("key", type=_issue_key, help="issue key, e.g. NF-12")
    rel.add_argument("--keep-worktree", action="store_true", help="leave the worktree on disk")
    rel.add_argument(
        "--accept-tests",
        action="store_true",
        help="approve the tester's files as they stand on the branch (keeps the worktree)",
    )
    rel.add_argument(
        "--hold-only",
        action="store_true",
        help="only lift a hold; refused for a blocked, timed-out or done run (cardinal's release)",
    )
    hold = add("hold", "Start nothing for an issue until flocc release lifts the hold")
    hold.add_argument("key", type=_issue_key, help="issue key, e.g. NF-12")
    hold.add_argument("--reason", default="", help="why, for flocc status")
    can = add(
        "canary", "Run ./setup on a repo's base branch; clears its broken-main hold on a pass"
    )
    can.add_argument("repo", nargs="?", help="repo name from the config")
    can.add_argument("--forget", action="store_true", help="clear every repo's broken-main hold")
    doctor = add("doctor", "Check config and host setup")
    doctor.add_argument("--json", action="store_true", help="print the checks as JSON")
    status = add("status", "Show claims, last tick, usage and disk")
    status.add_argument("--json", action="store_true", help="print the raw report as JSON")
    logs = add("logs", "Print the service log: the journal on systemd hosts, run.log otherwise")
    logs.add_argument("-n", dest="lines", type=int, default=100, help="lines to print")
    logs.add_argument("-f", "--follow", action="store_true", help="keep printing new lines")
    logs.add_argument("--issue", metavar="KEY", help="only the lines that name this issue")
    peek = add("peek", "Print the tail of an issue's live session")
    peek.add_argument("key", help="issue key, e.g. NF-12")
    peek.add_argument("-n", dest="lines", type=int, default=40, help="lines to print")
    attach = add("attach", "Attach read-only to an agent's tmux session")
    attach.add_argument("session", help="session name, e.g. NF-12 or NF-12-review")
    ev = add("events", "Print unacknowledged events as JSON lines; --follow keeps printing")
    ev.add_argument("--follow", action="store_true", help="print each batch as it closes")
    ev.add_argument("--hello", action="store_true", help='print {"link": "up"} on connect')
    ack = add("ack", "Acknowledge events by id; acking twice is harmless")
    ack.add_argument("ids", nargs="+", type=int, help="event ids from flocc events")
    return parser


DESKTOP_VERBS = ("ship", "login", "keys", "host")


def _command(argv: list[str]) -> str | None:
    it = iter(argv)
    for arg in it:
        if arg == "--config":
            next(it, None)
        elif not arg.startswith("-"):
            return arg
    return None


def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    # argparse would report "invalid choice", or choke on the -H that follows the verb.
    if (verb := _command(argv)) in DESKTOP_VERBS:
        print(f"flocc {verb}: that is a desktop command; run it on your desktop", file=sys.stderr)
        return 2
    parser = build_parser()
    try:
        args = parser.parse_args(argv)
        return COMMANDS[args.command](args)
    except SystemExit as exc:
        return int(exc.code or 0)
