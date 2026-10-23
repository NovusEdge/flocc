import fcntl
import shutil
import subprocess
import sys
import time
import traceback
from contextlib import contextmanager
from pathlib import Path

from flocc import a2atick, ledger
from flocc.config import Config, trailer_enabled
from flocc.context import state_dir
from flocc.escalation import Escalation, Notifier, ntfy_push
from flocc.linear import Linear, LinearError, http_transport
from flocc.poller import (
    clean_scratchpads,
    forward_comments,
    maybe_digest,
    poll,
    reap_worktrees,
    restart_timed_out,
    route_escalations,
)
from flocc.publisher import comment_on_pr, pr_view, publish, read_amended
from flocc.runtime import Runtime
from flocc.sessions import Sessions
from flocc.supervisor import supervise
from flocc.tokens import check_claude_login, check_tokens
from flocc.worktree import git

FAILS_BEFORE_PAGE = 5


class IssueLinearErrors(LinearError):
    def __init__(self, failed: dict[str, LinearError]):
        self.keys = sorted(failed)
        super().__init__("; ".join(f"{k}: {failed[k]}" for k in self.keys))


def _which(name: str) -> str:
    # The pane's PATH is the tick's, and the unit's PATH is systemd's unless the unit sets one.
    path = shutil.which(name)
    if path is None:
        raise RuntimeError(f"{name} is not on PATH; set Environment=PATH in the unit")
    return path


def _require_token(path: Path) -> None:
    # launch() needs it too, but failing there comes after a claim is made; every ready issue
    # would be claimed and dropped again each tick.
    if not path.exists() or not path.read_text().strip():
        raise RuntimeError(f"{path} is missing or empty; agents get their GitHub token from it")


def build_runtime(cfg: Config, config_path: Path) -> Runtime:
    exe, claude_exe = _which("flocc"), _which("claude")
    _require_token(cfg.agent_gh_token_file)
    con = ledger.connect(cfg.ledger_path)
    linear = Linear(http_transport(cfg.linear_key_file.read_text().strip()))
    notifier = Notifier(cfg, con, linear, now=time.time)
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
            amended=read_amended(state_dir(cfg, claim.issue_key)),
            **kw,
        )

    def review_diff(claim):
        base = f"origin/{cfg.repos[claim.repo].base_branch}"
        return git("diff", f"{base}...HEAD", cwd=Path(claim.worktree))

    def comment_pr(claim, url, body):
        comment_on_pr(claim, url, body, token)

    return Runtime(
        cfg=cfg,
        con=con,
        linear=linear,
        sessions=Sessions(),
        notifier=notifier,
        now=time.time,
        exe=exe,
        claude_exe=claude_exe,
        config_path=config_path,
        publish_fn=publish_fn,
        git_head=lambda path: git("rev-parse", "HEAD", cwd=Path(path)),
        review_diff=review_diff,
        comment_pr=comment_pr,
        pr_view=lambda claim, url: pr_view(claim, url, token),
    )


@contextmanager
def tick_lock(path: Path):
    # Taken here rather than by flock(1) in the unit, so a tick run by hand cannot overlap the
    # timer's.
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w") as f:
        try:
            fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            yield False
            return
        yield True


def _page_after_repeats(con, notifier, counter, cause, what, exc: Exception, action: str):
    fails = int(ledger.meta_get(con, counter) or 0) + 1
    ledger.meta_set(con, counter, str(fails))
    if fails >= FAILS_BEFORE_PAGE:
        summary = f"{what} has failed {fails} ticks in a row: {exc}"
        notifier.send(Escalation(1, cause, summary, action, scope="global"), None, None)


def _page_unbuilt(cfg: Config, exc: Exception) -> None:
    # The page must not need what failed. A global tier 1 with no team only pushes, so the
    # notifier gets no Linear client; without a ledger to count in, the journal is all there is.
    try:
        con = ledger.connect(cfg.ledger_path)
    except Exception as err:  # noqa: BLE001
        print(f"tick: failure not counted, the ledger did not open: {err}", file=sys.stderr)
        return
    _page_tick_failure(con, Notifier(cfg, con, None, now=time.time, push=ntfy_push), exc)


def _page_tick_failure(con, notifier, exc: Exception) -> None:
    _page_after_repeats(
        con,
        notifier,
        "tick_failures",
        "tick-failing",
        "The tick",
        exc,
        "Read journalctl --user -u flocc",
    )


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
        for claim in ledger.live_claims(rt.con):
            if claim.state != "running":
                continue
            try:
                supervise(rt, claim)
            except LinearError as exc:
                failed[claim.issue_key] = exc
            except Exception:  # noqa: BLE001 - one issue's failure must not stop the others
                traceback.print_exc()
                rt.notifier.send(
                    Escalation(
                        2,
                        f"{claim.issue_key}:supervisor-error",
                        "The supervisor failed on this issue; see journalctl -u flocc.",
                        "Read the log",
                        issue_key=claim.issue_key,
                        waiting=True,
                    ),
                    None,
                    None,
                )
        failed |= route_escalations(rt)
        failed |= forward_comments(rt)
        # Before poll, which then picks the released issue up in this tick.
        restart_timed_out(rt)
        # Before poll, so the space it frees counts toward this tick's admission.
        reap_worktrees(rt)
        # Before poll: a rerun's _start drops the old listing, which would orphan its pads.
        clean_scratchpads(rt)
        poll(rt)
        # After poll, so an issue started this tick counts as live for the record comments.
        a2atick.step(rt)
        # Before the digest, so a warning written today goes out with today's digest.
        check_tokens(rt)
        check_claude_login(rt)
        maybe_digest(rt)
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
            rt.con, rt.notifier, "linear_failures", "linear-api", "Linear", exc, action
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
