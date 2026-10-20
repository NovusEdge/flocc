from dataclasses import dataclass

from linear_agents.agentblock import IssueSettings
from linear_agents.config import Config
from linear_agents.ledger import Claim, conflicting_claims, in_repo
from linear_agents.usage import Usage

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
) -> Decision:
    if free_disk_gb is not None and free_disk_gb < cfg.min_free_disk_gb:
        return Decision(False, f"disk free {free_disk_gb:.1f} GB < min_free_disk_gb")
    running = [c for c in live if c.state == "running"]
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
    clash = conflicting_claims(settings.paths, in_repo(live, repo), exclude=key)
    if clash:
        owners = ", ".join(f"{g} ({k})" for g, k in clash.items())
        return Decision(False, f"paths claimed: {owners}")
    return Decision(True)
