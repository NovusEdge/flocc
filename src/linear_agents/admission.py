from dataclasses import dataclass

from linear_agents.agentblock import IssueSettings
from linear_agents.config import Config
from linear_agents.ledger import Claim, conflicting_claims
from linear_agents.usage import Usage

STALE_USAGE_S = 15 * 60


@dataclass(frozen=True)
class Decision:
    ok: bool
    reason: str = ""


def admit(
    key: str,
    settings: IssueSettings,
    live: list[Claim],
    usage: Usage | None,
    dep_states: dict[str, str],
    cfg: Config,
    now: float,
) -> Decision:
    running = [c for c in live if c.state == "running"]
    if len(running) >= cfg.session_cap:
        return Decision(False, f"session cap {cfg.session_cap} reached")
    # Usage is only readable from a live session's status line, so with nothing running the
    # first session has to start blind.
    if usage is None or now - usage.observed_at > STALE_USAGE_S:
        if running:
            return Decision(False, "usage unknown while sessions are running")
    elif usage.five_hour_pct >= cfg.five_hour_max_pct:
        return Decision(False, f"5-hour usage {usage.five_hour_pct:.0f}%")
    elif usage.weekly_pct >= cfg.weekly_max_pct:
        return Decision(False, f"weekly usage {usage.weekly_pct:.0f}%")
    waiting = [d for d in settings.depends_on if dep_states.get(d) != "completed"]
    if waiting:
        return Decision(False, f"waiting on {', '.join(waiting)}")
    clash = conflicting_claims(settings.paths, live, exclude=key)
    if clash:
        owners = ", ".join(f"{g} ({k})" for g, k in clash.items())
        return Decision(False, f"paths claimed: {owners}")
    return Decision(True)
