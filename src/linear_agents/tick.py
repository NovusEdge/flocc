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

LINEAR_FAILS_BEFORE_PAGE = 5


def _which(name: str) -> str:
    # The pane's PATH is the tick's, and the unit's PATH is systemd's unless the unit sets one.
    path = shutil.which(name)
    if path is None:
        raise RuntimeError(f"{name} is not on PATH; set Environment=PATH in the unit")
    return path


def build_runtime(cfg: Config, config_path: Path) -> Runtime:
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
        exe=_which("linear-agents"),
        claude_exe=_which("claude"),
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


def tick(cfg: Config, config_path: Path) -> int:
    rt = build_runtime(cfg, config_path)
    try:
        for claim in ledger.live_claims(rt.con):
            if claim.state != "running":
                continue
            try:
                supervise(rt, claim)
            except LinearError:
                raise
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
        ledger.meta_set(rt.con, "linear_failures", "0")
    except LinearError as exc:
        fails = int(ledger.meta_get(rt.con, "linear_failures") or 0) + 1
        ledger.meta_set(rt.con, "linear_failures", str(fails))
        print(f"linear: {exc}", file=sys.stderr)
        if fails >= LINEAR_FAILS_BEFORE_PAGE:
            rt.notifier.send(
                Escalation(
                    1,
                    "linear-api",
                    f"Linear has failed {fails} ticks in a row: {exc}",
                    "Check the API key and Linear status",
                    scope="global",
                ),
                None,
                None,
            )
        return 1
    return 0
