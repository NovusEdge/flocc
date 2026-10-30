from dataclasses import dataclass

from flocc.agentblock import IssueSettings
from flocc.config import Config
from flocc.ledger import Claim
from flocc.usage import Usage

STALE_USAGE_S = 15 * 60


@dataclass(frozen=True)
class Decision:
    ok: bool
    reason: str = ""


def admit(
    key: str,
    repo: str,
    settings: IssueSettings,
    live: list[Claim],
    usage: Usage | None,
    dep_states: dict[str, str],
    cfg: Config,
    now: float,
    free_disk_gb: float | None = None,
    held: frozenset[str] = frozenset(),
) -> Decision:
    if free_disk_gb is not None and free_disk_gb < cfg.min_free_disk_gb:
        return Decision(False, f"disk free {free_disk_gb:.1f} GB < min_free_disk_gb")
    # A claim held on a broken base runs no session.
    running = [c for c in live if c.state == "running" and c.issue_key not in held]
    if len(running) >= cfg.session_cap:
        return Decision(False, f"session cap {cfg.session_cap} reached")
    # Usage within a window only grows, and latest_usage zeroes a window whose reset has passed,
    # so a stale reading over a limit still holds.
    if usage is not None and usage.five_hour_pct >= cfg.five_hour_max_pct:
        return Decision(False, f"5-hour usage {usage.five_hour_pct:.0f}%")
    if usage is not None and usage.weekly_pct >= cfg.weekly_max_pct:
        return Decision(False, f"weekly usage {usage.weekly_pct:.0f}%")
    # Usage is only readable from a live session's status line, so with nothing running the
    # first session has to start on a stale reading or none.
    if (usage is None or now - usage.observed_at > STALE_USAGE_S) and running:
        return Decision(False, "usage unknown while sessions are running")
    waiting = [d for d in settings.depends_on if dep_states.get(d) != "completed"]
    if waiting:
        return Decision(False, f"waiting on {', '.join(waiting)}")
    return Decision(True)
