import fcntl
import re
import subprocess
import sys
import time
import traceback
from contextlib import contextmanager
from pathlib import Path

from flocc import host, ledger, rebase
from flocc.agent import Agent
from flocc.config import Config, trailer_enabled
from flocc.context import agent_dir
from flocc.linear import Linear, LinearError, http_transport
from flocc.notice import Notice, Notifier
from flocc.poller import (
    comment_fixes,
    forward_comments,
    maybe_daily,
    poll,
    reap_worktrees,
    restart_timed_out,
    retry_transient,
)
from flocc.publisher import comment_on_pr, pr_view, publish, read_amended
from flocc.runtime import Runtime
from flocc.supervisor import supervise
from flocc.tokens import check_claude_login, check_tokens
from flocc.worktree import agent_ref, fetch_agent_branch, git, origin_base

# The spec's service-error: a tick that raised, and Linear failing three ticks in a row.
TICK_FAILS_BEFORE_EVENT = 1
LINEAR_FAILS_BEFORE_EVENT = 3


class IssueLinearErrors(LinearError):
    def __init__(self, failed: dict[str, LinearError]):
        self.keys = sorted(failed)
        super().__init__("; ".join(f"{k}: {failed[k]}" for k in self.keys))


def _require_token(path: Path) -> None:
    # launch() needs it too, but failing there comes after a claim is made; every ready issue
    # would be claimed and dropped again each tick.
    if not path.exists() or not path.read_text().strip():
        raise RuntimeError(f"{path} is missing or empty; agents get their GitHub token from it")


def build_runtime(cfg: Config, config_path: Path) -> Runtime:
    exe, claude_exe = str(cfg.agent_flocc), str(cfg.agent_claude)
    agent = Agent(cfg)
    _require_token(cfg.agent_gh_token_file)
    con = ledger.connect(cfg.ledger_path)
    linear = Linear(http_transport(cfg.linear_key_file.read_text().strip()))
    notifier = Notifier(con, linear, now=time.time)
    token = cfg.publish_gh_token_file.read_text().strip()

    def publish_fn(claim, title, summary, **kw):
        return publish(
            claim,
            cfg.repos[claim.repo],
            title,
            summary,
            token,
            check_timeout_s=cfg.check_timeout_s,
            trailer=trailer_enabled(cfg, cfg.repos[claim.repo]),
            accepted_sha=ledger.accepted_tests(con, claim.issue_key),
            amended=read_amended(agent_dir(cfg, claim.issue_key)),
            check=agent.check_worktree,
            **kw,
        )

    def git_head(claim):
        repo = cfg.repos[claim.repo]
        return fetch_agent_branch(repo.path, Path(claim.worktree), claim.branch, claim.issue_key)

    def review_diff(claim):
        repo = cfg.repos[claim.repo]
        git_head(claim)
        origin_base(repo)
        return git(
            "diff", f"origin/{repo.base_branch}...{agent_ref(claim.issue_key)}", cwd=repo.path
        )

    def comment_pr(claim, url, body):
        comment_on_pr(claim, url, body, token)

    return Runtime(
        cfg=cfg,
        con=con,
        linear=linear,
        sessions=agent.sessions(),
        notifier=notifier,
        now=time.time,
        exe=exe,
        claude_exe=claude_exe,
        config_path=config_path,
        publish_fn=publish_fn,
        git_head=git_head,
        review_diff=review_diff,
        comment_pr=comment_pr,
        pr_view=lambda claim, url: pr_view(claim, url, token),
        base_check=agent.check_base,
        agent=agent,
    )


LOCK_POLL_S = 0.5


@contextmanager
def tick_lock(path: Path, wait_s: float = 0.0):
    # Taken here rather than by flock(1) in the unit, so a tick run by hand cannot overlap the
    # timer's.
    path.parent.mkdir(parents=True, exist_ok=True)
    deadline = time.monotonic() + wait_s
    with path.open("w") as f:
        while True:
            try:
                fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                left = deadline - time.monotonic()
                if left <= 0:
                    yield False
                    return
                time.sleep(min(LOCK_POLL_S, left))
        yield True


def _page_after_repeats(con, notifier, counter, cause, what, exc: Exception, action: str, after):
    fails = int(ledger.meta_get(con, counter) or 0) + 1
    ledger.meta_set(con, counter, str(fails))
    now = time.time()
    if fails < after or ledger.alert_seen(con, cause, ledger.DEDUPE_S, now):
        return
    tick_word = "tick" if fails == 1 else "ticks"
    summary = f"{what} has failed {fails} {tick_word} in a row: {_cause(exc)}"
    notifier.send(Notice(cause, summary, action, event="service-error"))
    ledger.record_alert(con, cause, 0, None, now)


def _page_unbuilt(cfg: Config, exc: Exception) -> None:
    # The notice must not need what failed: with no issue it only writes the digest, so it gets
    # no Linear client. Without a ledger to count in, the journal is all there is.
    try:
        con = ledger.connect(cfg.ledger_path)
    except Exception as err:  # noqa: BLE001
        print(f"tick: failure not counted, the ledger did not open: {err}", file=sys.stderr)
        return
    _page_tick_failure(con, Notifier(con, None, now=time.time), exc)


def _page_tick_failure(con, notifier, exc: Exception) -> None:
    _page_after_repeats(
        con,
        notifier,
        "tick_failures",
        "tick-failing",
        "The tick",
        exc,
        "Read journalctl --user -u flocc",
        TICK_FAILS_BEFORE_EVENT,
    )


_URL_CREDENTIALS = re.compile(r"://[^/\s@]+@")


def _cause(exc: Exception) -> str:
    """The exception's type and first message line, on one line, for an event a reader triages
    without the journal."""
    first = (str(exc).strip().splitlines() or [""])[0]
    masked = _URL_CREDENTIALS.sub("://***@", first)
    return " ".join(f"{type(exc).__name__}: {masked}".split())[:200]


def tick(cfg: Config, config_path: Path) -> int:
    try:
        rt = build_runtime(cfg, config_path)
    except Exception as exc:
        _page_unbuilt(cfg, exc)
        raise
    # A deleted issue fails only its own claim; the tick still counts as a Linear failure once
    # the other claims, the poll and the digest are done.
    failed: dict[str, LinearError] = {}
    try:
        # Before supervise, so nothing it would launch starts into an episode this tick opens.
        try:
            host.check(rt)
        except (OSError, ValueError):
            traceback.print_exc()
        for claim in ledger.live_claims(rt.con):
            if claim.state != "running":
                continue
            try:
                supervise(rt, claim)
            except LinearError as exc:
                failed[claim.issue_key] = exc
            except Exception as exc:  # noqa: BLE001 - one issue's failure must not stop the others
                traceback.print_exc()
                cause = f"{claim.issue_key}:supervisor-error"
                if not ledger.alert_seen(rt.con, cause, ledger.DEDUPE_S, rt.now()):
                    rt.notifier.send(
                        Notice(
                            cause,
                            f"The supervisor failed on this issue ({_cause(exc)}); see journalctl -u"
                            " flocc.",
                            "Read the log",
                            issue_key=claim.issue_key,
                            event="service-error",
                        )
                    )
                    ledger.record_alert(rt.con, cause, 0, claim.issue_key, rt.now())
        failed |= forward_comments(rt)
        # Before poll, which then picks the released issue up in this tick.
        restart_timed_out(rt)
        retry_transient(rt)
        # Before poll, so the space it frees counts toward this tick's admission.
        reap_worktrees(rt)
        poll(rt)
        # After poll, so an issue the owner moved to Todo this tick reruns with the comment
        # instead; before the rebase step, which would start a second session on the claim.
        try:
            comment_fixes(rt)
        except Exception:  # noqa: BLE001 - a comment fix that cannot start must not stop the digest
            traceback.print_exc()
        # After poll, so an issue started this tick is not mistaken for a finished one.
        try:
            rebase.step(rt)
        except Exception:  # noqa: BLE001 - a rebase that cannot start must not stop the digest
            traceback.print_exc()
        # Before the digest, so a warning written today goes out with today's digest.
        check_tokens(rt)
        check_claude_login(rt)
        maybe_daily(rt)
        if failed:
            raise IssueLinearErrors(failed)
        ledger.meta_set(rt.con, "linear_failures", "0")
        ledger.meta_set(rt.con, "tick_failures", "0")
    except LinearError as exc:
        print(f"linear: {exc}", file=sys.stderr)
        action = "Check the API key and Linear status"
        if isinstance(exc, IssueLinearErrors):
            commands = ", ".join(f"flocc release {k}" for k in exc.keys)
            action += f"; if an issue was deleted, run {commands}"
        _page_after_repeats(
            rt.con,
            rt.notifier,
            "linear_failures",
            "linear-api",
            "Linear",
            exc,
            action,
            LINEAR_FAILS_BEFORE_EVENT,
        )
        return 1
    except Exception as exc:
        # Re-raised so the unit still fails, but a tmux server that is down fails every tick
        # and nothing else pages.
        _page_tick_failure(rt.con, rt.notifier, exc)
        raise
    return 0


def run_loop(
    argv: list[str],
    log: Path,
    interval_s: float,
    run=subprocess.run,
    sleep=time.sleep,
    now=time.time,
    iterations: int | None = None,
) -> None:
    """Tick every interval_s on hosts without systemd, where the timer cannot."""
    n = 0
    while iterations is None or n < iterations:
        # A subprocess per tick, as under the timer: one that crashes does not end the loop.
        # Neither does an OSError: uv replacing the script mid-upgrade makes the exec fail, and
        # this loop runs detached, so an escaping error would end it with nobody watching.
        # On a full disk open() succeeds and the writes, flush and close fail instead, so the
        # whole body is covered, not just the open.
        try:
            log.parent.mkdir(parents=True, exist_ok=True)
            with log.open("a") as f:
                f.write(f"--- tick {time.strftime('%Y-%m-%dT%H:%M:%S', time.gmtime(now()))}Z\n")
                f.flush()
                try:
                    outcome = str(
                        run(argv, stdout=f, stderr=subprocess.STDOUT, check=False).returncode
                    )
                except OSError as exc:
                    outcome = f"error: {exc}"
                f.write(f"--- exit {outcome}\n")
        except OSError as exc:
            print(f"run: log {log} not written: {exc}", file=sys.stderr)
        n += 1
        if iterations is None or n < iterations:
            sleep(interval_s)
