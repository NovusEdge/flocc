import fcntl
import shutil
import sys
import time
import traceback
from contextlib import contextmanager
from pathlib import Path

from linear_agents import ledger
from linear_agents.config import Config
from linear_agents.escalation import Escalation, Notifier
from linear_agents.linear import Linear, LinearError, http_transport
from linear_agents.poller import forward_comments, maybe_digest, poll, route_escalations
from linear_agents.publisher import publish
from linear_agents.runtime import Runtime
from linear_agents.sessions import Sessions
from linear_agents.supervisor import supervise
from linear_agents.worktree import git

FAILS_BEFORE_PAGE = 5


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
    exe, claude_exe = _which("linear-agents"), _which("claude")
    _require_token(cfg.agent_gh_token_file)
    con = ledger.connect(cfg.ledger_path)
    linear = Linear(http_transport(cfg.linear_key_file.read_text().strip()))
    notifier = Notifier(cfg, con, linear, now=time.time)
    token = cfg.publish_gh_token_file.read_text().strip()

    def publish_fn(claim, title, summary):
        return publish(claim, cfg.repos[claim.repo], title, summary, token)

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


def _page_after_repeats(rt, counter: str, cause: str, what: str, exc: Exception, action: str):
    fails = int(ledger.meta_get(rt.con, counter) or 0) + 1
    ledger.meta_set(rt.con, counter, str(fails))
    if fails >= FAILS_BEFORE_PAGE:
        summary = f"{what} has failed {fails} ticks in a row: {exc}"
        rt.notifier.send(Escalation(1, cause, summary, action, scope="global"), None, None)


def tick(cfg: Config, config_path: Path) -> int:
    rt = build_runtime(cfg, config_path)
    linear_exc: LinearError | None = None
    try:
        for claim in ledger.live_claims(rt.con):
            if claim.state != "running":
                continue
            try:
                supervise(rt, claim)
            except LinearError as exc:
                # A deleted issue fails only its own claim; the tick still counts as a Linear
                # failure once the other claims are done.
                print(f"{claim.issue_key}: linear: {exc}", file=sys.stderr)
                linear_exc = exc
            except Exception:  # noqa: BLE001 - one issue's failure must not stop the others
                traceback.print_exc()
                rt.notifier.send(
                    Escalation(
                        2,
                        f"{claim.issue_key}:supervisor-error",
                        "The supervisor failed on this issue; see journalctl -u linear-agents.",
                        "Read the log",
                        issue_key=claim.issue_key,
                        waiting=True,
                    ),
                    None,
                    None,
                )
        route_escalations(rt)
        forward_comments(rt)
        poll(rt)
        maybe_digest(rt)
        if linear_exc is not None:
            raise linear_exc
        ledger.meta_set(rt.con, "linear_failures", "0")
        ledger.meta_set(rt.con, "tick_failures", "0")
    except LinearError as exc:
        print(f"linear: {exc}", file=sys.stderr)
        _page_after_repeats(
            rt,
            "linear_failures",
            "linear-api",
            "Linear",
            exc,
            "Check the API key and Linear status",
        )
        return 1
    except Exception as exc:
        # Re-raised so the unit still fails, but a tmux server that is down fails every tick
        # and nothing else pages.
        _page_after_repeats(
            rt,
            "tick_failures",
            "tick-failing",
            "The tick",
            exc,
            "Read journalctl --user -u linear-agents",
        )
        raise
    return 0
