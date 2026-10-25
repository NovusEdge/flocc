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
from pathlib import Path, PurePosixPath

from flocc import gate, inbox, ledger, permission, usage
from flocc.config import TRAILER, default_config_path, load
from flocc.escalation import Escalation, validate
from flocc.grants import for_phase


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
    top = Path.cwd()
    top = Path(git("rev-parse", "--show-toplevel", cwd=top))
    con = _ledger(args)
    mine = ledger.claim_for_worktree(con, str(top))
    if mine is None:
        return 0
    if mine.phase == "review":
        print("flocc: the review session is read-only; do not commit", file=sys.stderr)
        return 1
    shared = _shared_paths(args, mine.repo)
    staged = [
        n
        for n in git("diff", "--cached", "--name-only", cwd=top).splitlines()
        if not ledger.is_shared(n, shared)
    ]
    peers = ledger.in_repo(ledger.live_claims(con), mine.repo)
    clash = ledger.file_conflicts(staged, peers, exclude=mine.issue_key)
    if not clash:
        return 0
    for name, owner in clash.items():
        print(
            f"flocc: {name} is claimed by {owner}; message that session or escalate",
            file=sys.stderr,
        )
    return 1


def _shared_paths(args, repo: str) -> tuple[str, ...]:
    # A config that will not load must not stop a commit; the check then falls back to claims only.
    try:
        cfg = load(Path(args.config) if args.config else default_config_path())
    except (OSError, ValueError, TypeError):
        return ()
    return cfg.repos[repo].shared_paths if repo in cfg.repos else ()


def cmd_tests_amended(args) -> int:
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
    path = state / gate.AMENDED
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


def cmd_claim(args) -> int:
    key = _state_dir().name
    for glob in args.globs:
        if "**" in glob:
            print(
                f"claim: {glob} claims a whole tree; claim the files you will edit"
                " (a * within one directory is fine)",
                file=sys.stderr,
            )
            return 1
    con = _ledger(args)
    mine = ledger.get_claim(con, key)
    if mine is None:
        print(f"claim: no live claim for {key}", file=sys.stderr)
        return 1
    shared = _shared_paths(args, mine.repo)
    globs = []
    for glob in args.globs:
        if not glob or glob.startswith("/") or ".." in PurePosixPath(glob).parts:
            print(f"claim: {glob!r} is not a path inside the worktree", file=sys.stderr)
            return 1
        # file_conflicts also matches a glob against every parent directory of a staged file, so
        # a glob that matches a directory claims everything under it, whatever it is spelled.
        tree = glob.endswith("/") or next(
            (p for p in Path(mine.worktree).glob(glob) if p.is_dir()), None
        )
        if tree:
            where = glob if tree is True else tree.relative_to(mine.worktree)
            print(
                f"claim: {glob} matches the directory {where}; claim the files in it",
                file=sys.stderr,
            )
            return 1
        if ledger.is_shared(glob, shared):
            print(f"claim: {glob} is shared; edit it without claiming")
        else:
            globs.append(glob)
    if not globs:
        return 0
    try:
        clash = ledger.add_paths(con, key, globs)
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
    from flocc.sessions import session_name

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
        f"## Peers in this repo\n\n{names}. The flocc-a2a tool `agents` lists every host's"
        " issues; message one with `send`.\n"
    )
    g = for_phase(state / "grants.json", os.environ.get("FLOCC_PHASE", ""))
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
    run = os.environ.get("FLOCC_RUN")
    phase = os.environ.get("FLOCC_PHASE")
    # Exit 2 on SubagentStop blocks the stop and keeps the agent alive, so bookkeeping
    # failures exit 1, which Claude Code treats as non-blocking.
    if args.stopped or args.answered:
        try:
            state = _state_dir()
            if args.stopped:
                agent_type = str(_hook_input().get("agent_type", ""))
                gate.record_stop(state, run, time.time(), agent_type, phase)
            else:
                gate.clear_question(state, time.time())
        except Exception as exc:  # noqa: BLE001 - must not block the stop
            print(f"flocc gate could not record the event: {exc}", file=sys.stderr)
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
                print(f"flocc gate could not record the question: {exc}", file=sys.stderr)
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
        print(f"flocc gate failed, so the launch is refused: {exc}", file=sys.stderr)
        return 2
    return 0


def cmd_idle(args) -> int:
    # Exit 2 on Stop blocks the stop and keeps the session running; see cmd_gate.
    try:
        gate.note_idle(_state_dir(), _hook_input(), time.time())
    except Exception as exc:  # noqa: BLE001 - must not block the stop
        print(f"flocc idle could not record the stop: {exc}", file=sys.stderr)
        return 1
    return 0


def _context_out(event: str, text: str) -> None:
    print(json.dumps({"hookSpecificOutput": {"hookEventName": event, "additionalContext": text}}))


def cmd_inbox(args) -> int:
    from flocc import delivery

    # A failure must not block the call or the prompt it rides on; the rows wait for the next hook.
    try:
        payload = _hook_input()
        if payload.get("agent_id"):
            return 0
        event = str(payload.get("hook_event_name") or "PostToolUse")
        state = _state_dir()
        if event == "SessionStart":
            text = delivery.session_text(state, time.time())
        elif delivery.ending(state):
            return 0
        else:
            text = delivery.take(state, time.time())
        if text:
            _context_out(event, text)
    except Exception as exc:  # noqa: BLE001
        print(f"flocc inbox delivered nothing: {exc}", file=sys.stderr)
    return 0


def cmd_stop(args) -> int:
    from flocc import delivery

    # Exit 2 on Stop blocks the stop; bookkeeping failures exit 1 as cmd_gate and cmd_idle do.
    try:
        state = _state_dir()
        payload = _hook_input()
    except Exception as exc:  # noqa: BLE001
        print(f"flocc stop could not read its input: {exc}", file=sys.stderr)
        return 1
    # The inbox and note_idle still run when this fails, so the supervisor sees the session idle.
    failed = 0
    try:
        gate.clear_question(state, time.time())
        gate.note_background(state, payload)
    except Exception as exc:  # noqa: BLE001
        print(f"flocc stop could not record the answer: {exc}", file=sys.stderr)
        failed = 1
    if (state / inbox.MARKER).exists() and not delivery.ending(state):
        try:
            text = delivery.take(state, time.time())
        except Exception as exc:  # noqa: BLE001
            print(f"flocc stop delivered nothing: {exc}", file=sys.stderr)
            text = ""
        if text:
            # Continues the turn; Claude Code's stop_hook_active protection bounds the loop.
            _context_out("Stop", text)
            return 0
    try:
        gate.note_idle(state, payload, time.time())
    except Exception as exc:  # noqa: BLE001
        print(f"flocc stop could not record the stop: {exc}", file=sys.stderr)
        return 1
    return failed


def cmd_permission(args) -> int:
    # Exit 1 is non-blocking for every event this hook is registered on, and printing nothing
    # leaves the decision to Claude Code.
    try:
        payload = _hook_input(strict=True)
        out = permission.handle(
            payload,
            _state_dir(),
            os.environ.get("FLOCC_PHASE"),
            os.environ.get("FLOCC_RUN"),
            time.time(),
        )
    except Exception as exc:  # noqa: BLE001 - see above
        print(f"flocc permission failed, so it made no decision: {exc}", file=sys.stderr)
        return 1
    if out is not None:
        print(json.dumps(out))
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
        run = os.environ.get("FLOCC_RUN")
        (state / gate.LAST_STOP).touch()
        gate.record_session(state, run, time.time(), os.environ.get("FLOCC_PHASE"))
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
        print(
            f"canary: {ran} {what if not check else 'fails'} on {args.repo} at {sha[:9]};"
            f" its issues stay held\n"
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
        line = f"  {c['issue']}  {c['phase']}  {c['state']}  ctx {_pct(c['context_pct'])}  {alive}"
        print(f"{line}  launches {c['launches']}/{c['launch_limit']}")
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
    if report.get("a2a"):
        print(f"  a2a service {report['a2a']}")
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


def cmd_permissions(args) -> int:
    cfg, _ = _config(args)
    since = time.time() - args.days * 86400
    print(permission.report(permission.load_entries(cfg.state_root, since), since))
    return 0


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


def cmd_a2a(args) -> int:
    from flocc.a2aconf import NotConfigured, load
    from flocc.worktree import git

    cfg, _ = _config(args)
    try:
        load()
    except NotConfigured as exc:
        # Exit 0: the unit restarts only on failure, and a host shipped without A2A has nothing
        # to serve.
        print(f"a2a serve: {exc}; run flocc ship from the desktop", file=sys.stderr)
        return 0
    host = args.bind
    if host is None:
        out = subprocess.run(["tailscale", "ip", "-4"], capture_output=True, text=True, check=False)
        host = out.stdout.split()[0] if out.returncode == 0 and out.stdout.split() else None
    if host is None:
        print("a2a serve: no tailnet address yet; systemd restarts this", file=sys.stderr)
        return 1
    try:
        from flocc import a2aserver
    except ImportError:
        print(
            "a2a serve: install flocc with the a2a extra: uv tool install '.[a2a]'", file=sys.stderr
        )
        return 1
    a2aserver.write_served(cfg, git("rev-parse", "HEAD", cwd=cfg.plugin_dir.parent))
    a2aserver.serve(cfg, host, args.port)
    return 0


def cmd_mcp_a2a(args) -> int:
    from flocc import mcp_a2a

    return mcp_a2a.main()


COMMANDS = {
    "inbox": cmd_inbox,
    "stop": cmd_stop,
    "a2a": cmd_a2a,
    "mcp-a2a": cmd_mcp_a2a,
    "statusline": cmd_statusline,
    "escalate": cmd_escalate,
    "hook": cmd_hook,
    "claim": cmd_claim,
    "tests-amended": cmd_tests_amended,
    "context": cmd_context,
    "gate": cmd_gate,
    "idle": cmd_idle,
    "permission": cmd_permission,
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
    "permissions": cmd_permissions,
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

    a2a = add("a2a", "Serve this host's A2A endpoint on the tailnet")
    a2a.add_argument("action", choices=["serve"], help="what to do")
    a2a.add_argument("--bind", default=None, help="address to bind (default: tailscale ip -4)")
    a2a.add_argument("--port", type=int, default=7443, help="port to bind")
    add("mcp-a2a", "Run the flocc-a2a MCP server on stdio")
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
    amended = add("tests-amended", "Declare tester files you changed because a test was wrong")
    amended.add_argument("paths", nargs="+", help="repo-relative paths of the amended tester files")
    amended.add_argument("--reason", required=True, help="why the test was wrong")
    add("context", "Print the issue's claims and context")
    gate = add("gate", "Hook: allow or refuse a subagent or workflow launch, track questions")
    which = gate.add_mutually_exclusive_group()
    which.add_argument("--stopped", action="store_true", help="SubagentStop: an agent finished")
    which.add_argument("--answered", action="store_true", help="PostToolUse: a question answered")
    add("idle", "Hook (Stop): record that the session ended its turn, with its last message")
    add("inbox", "Hook: hand this session its pending A2A messages")
    add("stop", "Hook (Stop): answered-question check, then mail, else record the idle stop")
    add("permission", "Hook: log an auto-mode block or prompt")
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
    perms = add("permissions", "Summarise the auto-mode blocks and prompts sessions met")
    perms.add_argument("action", choices=["report"], help="what to show")
    perms.add_argument("--days", type=int, default=7, help="how many days back")
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
