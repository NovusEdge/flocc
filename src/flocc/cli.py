import argparse
import fcntl
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import time
from pathlib import Path

from flocc import idle, ledger, usage
from flocc.config import TRAILER, default_config_path, load


def _state_dir() -> Path:
    return Path(os.environ["FLOCC_STATE"])


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


def _ledger(args):
    env = os.environ.get("FLOCC_LEDGER")
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
    from flocc.worktree import git

    if args.name == "commit-msg":
        return _commit_msg(args.message_file, not args.no_trailer)
    top = Path(git("rev-parse", "--show-toplevel", cwd=Path.cwd()))
    mine = ledger.claim_for_worktree(_ledger(args), str(top))
    if mine is not None and mine.phase == "review":
        print("flocc: the review session is read-only; do not commit", file=sys.stderr)
        return 1
    return 0


def cmd_tests_amended(args) -> int:
    from flocc.publisher import AMENDED
    from flocc.worktree import changed_files

    state = _state_dir()
    claim = ledger.get_claim(_ledger(args), state.name)
    if claim is None or claim.base_sha is None or claim.tester_sha is None:
        print(
            f"tests-amended: no live claim with a tester commit for {state.name}", file=sys.stderr
        )
        return 1
    tester_files = changed_files(Path(claim.worktree), claim.base_sha, claim.tester_sha)
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


def cmd_idle(args) -> int:
    # Exit 2 on Stop blocks the stop and keeps the session running.
    try:
        idle.note_idle(_state_dir(), _hook_input(), time.time())
    except Exception as exc:  # noqa: BLE001 - must not block the stop
        print(f"flocc idle could not record the stop: {exc}", file=sys.stderr)
        return 1
    return 0


def cmd_stop(args) -> int:
    # Exit 2 on Stop blocks the stop; bookkeeping failures exit 1 as cmd_idle does.
    try:
        state = _state_dir()
        payload = _hook_input()
    except Exception as exc:  # noqa: BLE001
        print(f"flocc stop could not read its input: {exc}", file=sys.stderr)
        return 1
    # note_idle still runs when this fails, so the supervisor sees the session idle.
    failed = 0
    try:
        idle.note_background(state, payload)
    except Exception as exc:  # noqa: BLE001
        print(f"flocc stop could not record the background tasks: {exc}", file=sys.stderr)
        failed = 1
    try:
        idle.note_idle(state, payload, time.time())
    except Exception as exc:  # noqa: BLE001
        print(f"flocc stop could not record the stop: {exc}", file=sys.stderr)
        return 1
    return failed


def _session_bookkeeping(state: Path) -> None:
    state.mkdir(parents=True, exist_ok=True)
    payload = _hook_input()
    if payload.get("source") == "compact":
        # status.json still holds the pre-compaction reading; the supervisor would hand off at once.
        (state / "compacted").touch()
        (state / "status.json").unlink(missing_ok=True)
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
    with tick_lock(cfg.tick_lock) as held:
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


def cmd_canary(args) -> int:
    from flocc import canary, publisher, worktree
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
                sha, failure = publisher.check_at_base(
                    cfg.repos[args.repo],
                    cfg.worktree_root,
                    cfg.setup_timeout_s,
                    cfg.check_timeout_s,
                    cfg.shared_uv_cache,
                )
                code = 1 if failure else 0
                out = failure.log or failure.detail if failure else ""
            else:
                code, out, sha = worktree.setup_at_base(
                    cfg.repos[args.repo],
                    cfg.worktree_root,
                    cfg.setup_timeout_s,
                    cfg.shared_uv_cache,
                )
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
    return run_doctor(cfg)


def cmd_status(args) -> int:
    from flocc.sessions import Sessions
    from flocc.status import collect

    cfg, _ = _config(args)
    report = collect(cfg, Sessions(), claude=shutil.which("claude"))
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
    from flocc.sessions import Sessions, session_name
    from flocc.status import _read_ledger

    cfg, _ = _config(args)
    claims, _, _ = _read_ledger(cfg.ledger_path)
    claim = next((c for c in claims if c.issue_key == args.key), None)
    sessions = Sessions()
    name = session_name(claim.issue_key, claim.phase) if claim else None
    if name and sessions.alive(name):
        return sessions, name
    print(f"{args.command}: no live session for {args.key}", file=sys.stderr)
    return None, None


def cmd_peek(args) -> int:
    sessions, name = _live_session(args)
    # Blank rows below the prompt count toward the capture, so ask for more than N.
    text = sessions.capture(name, max(args.lines, 1) + 200) if sessions else ""
    rows = text.rstrip().splitlines()
    if not rows:
        if sessions:
            print(f"peek: no live session for {args.key}", file=sys.stderr)
        return 3
    for row in rows[-max(args.lines, 1) :]:
        print("".join(c if c.isprintable() else " " for c in row))
    return 0


# Enough to move through and answer any Claude Code dialog. Control keys stay out: C-c and C-d
# end the Claude session, and a dialog never needs them. Single characters are letters and
# digits only: tmux reads a lone ";" as a command separator and "-x" as a flag.
PRESS_KEYS = frozenset(
    {"Enter", "Escape", "Up", "Down", "Left", "Right", "Tab", "BTab", "Space", "BSpace"}
)


def cmd_press(args) -> int:
    bad = [k for k in args.keys if k not in PRESS_KEYS and not (len(k) == 1 and k.isalnum())]
    if bad:
        allowed = " ".join(sorted(PRESS_KEYS))
        print(
            f"press: not a key flocc sends: {' '.join(bad)}; use {allowed}, a letter or a digit",
            file=sys.stderr,
        )
        return 2
    sessions, name = _live_session(args)
    if not sessions:
        return 3
    sessions.press(name, args.keys)
    print(f"pressed {' '.join(args.keys)} in {name}; flocc peek {args.key} shows the result")
    return 0


def cmd_say(args) -> int:
    sessions, name = _live_session(args)
    if not sessions:
        return 3
    sessions.send(name, " ".join(args.text))
    print(f"sent to {name}; flocc peek {args.key} shows the reply")
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
    from flocc.sessions import SOCKET

    # Typing into an attached pane goes nowhere, so the way out and the way to answer are shown
    # in tmux's status line, where the attached screen does not cover them.
    hint = "read-only · Ctrl-b d detaches, the session keeps running · answer with flocc press"
    print(f"Attaching {args.session} {hint}", file=sys.stderr)
    argv = [
        "tmux",
        "-L",
        SOCKET,
        "attach",
        "-r",
        "-t",
        args.session,
        ";",
        "display-message",
        "-d",
        "15000",
        hint,
    ]
    os.execvp(argv[0], argv)


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
    "tests-amended": cmd_tests_amended,
    "context": cmd_context,
    "idle": cmd_idle,
    "session-start": cmd_session_start,
    "commit": cmd_commit,
    "done": cmd_done,
    "review-done": cmd_review_done,
    "handoff-ready": cmd_handoff_ready,
    "tick": cmd_tick,
    "run": cmd_run,
    "release": cmd_release,
    "canary": cmd_canary,
    "doctor": cmd_doctor,
    "status": cmd_status,
    "logs": cmd_logs,
    "peek": cmd_peek,
    "press": cmd_press,
    "say": cmd_say,
    "attach": cmd_attach,
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
    amended = add("tests-amended", "Declare tester files you changed because a test was wrong")
    amended.add_argument("paths", nargs="+", help="repo-relative paths of the amended tester files")
    amended.add_argument("--reason", required=True, help="why the test was wrong")
    add("context", "Print the issue's context")
    add("idle", "Hook (Stop): record that the session ended its turn, with its last message")
    add("stop", "Hook (Stop): record background tasks and the idle stop")
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
    press = add("press", "Press keys in an issue's live session, e.g. to answer a dialog")
    press.add_argument("key", help="issue key, e.g. NF-12")
    press.add_argument("keys", nargs="+", help="Enter, Escape, Up, Down, Tab, ... or one character")
    say = add("say", "Type a message into an issue's live session and press Enter")
    say.add_argument("key", help="issue key, e.g. NF-12")
    say.add_argument("text", nargs="+", help="the message")
    attach = add("attach", "Attach read-only to an agent's tmux session")
    attach.add_argument("session", help="session name, e.g. NF-12 or NF-12-review")
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
