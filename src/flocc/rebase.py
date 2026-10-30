import sys
from dataclasses import replace
from pathlib import Path

from flocc import ledger
from flocc.admission import admit
from flocc.agentblock import settings_from_json
from flocc.poller import main_hold, still_wanted
from flocc.publisher import pr_url
from flocc.supervisor import REBASE_TRIED, block_unrebased, start_rebase
from flocc.usage import latest_usage
from flocc.worktree import origin_base


def step(rt) -> None:
    """Starts a fix session on a finished claim's PR when the base moved and it conflicts.

    Needs the claim's worktree, so a PR whose worktree was reaped is left to its owner."""
    if rt.pr_view is None:
        return
    bases: dict[str, str | None] = {}
    for claim in ledger.finished_claims(rt.con):
        url = pr_url(rt, claim)
        if claim.state != "done" or url is None or not Path(claim.worktree).exists():
            continue
        key = claim.issue_key
        try:
            view = rt.pr_view(claim, url)
            if view.get("state") != "OPEN" or view.get("mergeable") != "CONFLICTING":
                continue
            # Someone pushed to the PR after this host's branch: merging into the stale copy
            # would only fail at the push.
            if view.get("headRefOid") != rt.git_head(claim.worktree):
                continue
            if not still_wanted(rt, key):
                continue
        except Exception as exc:  # noqa: BLE001 - one PR's failure must not stop the others
            print(f"{key}: PR not checked for conflicts: {exc}", file=sys.stderr)
            continue
        if claim.repo not in bases:
            bases[claim.repo] = origin_base(rt.cfg.repos[claim.repo])
        base = bases[claim.repo]
        if base is None:
            continue
        if ledger.meta_get(rt.con, f"{REBASE_TRIED}:{key}") == base:
            block_unrebased(rt, claim, base)
            continue
        if reason := main_hold(rt, claim.repo, bases):
            print(f"{key}: rebase held, {reason}", file=sys.stderr)
            continue
        # Without its dependencies: they finished before the PR opened.
        settings = replace(settings_from_json(claim.settings_json), depends_on=())
        decision = admit(
            key,
            claim.repo,
            settings,
            ledger.live_claims(rt.con),
            latest_usage(rt.cfg.agent_state_root, rt.now()),
            {},
            rt.cfg,
            rt.now(),
            rt.free_disk_gb(rt.cfg.worktree_root),
            ledger.held_keys(rt.con),
        )
        if not decision.ok:
            print(f"{key}: rebase held, {decision.reason}", file=sys.stderr)
            continue
        start_rebase(rt, claim, base)
