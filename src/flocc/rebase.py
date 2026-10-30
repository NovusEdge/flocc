import sys
from pathlib import Path

from flocc import ledger
from flocc.control import admit_rerun
from flocc.poller import main_hold, still_wanted
from flocc.publisher import pr_url
from flocc.supervisor import REBASE_TRIED, block_unrebased, start_rebase
from flocc.worktree import origin_base


def step(rt) -> None:
    """Starts a fix session on a finished claim's PR when the base moved and it conflicts.

    Needs the claim's worktree, so a PR whose worktree was reaped is left to its owner."""
    if rt.pr_view is None:
        return
    bases: dict[str, str | None] = {}
    holds = ledger.issue_holds(rt.con)
    for claim in ledger.finished_claims(rt.con):
        url = pr_url(rt, claim)
        if claim.state != "done" or url is None or not Path(claim.worktree).exists():
            continue
        key = claim.issue_key
        if key in holds:
            continue
        try:
            view = rt.pr_view(claim, url)
            if view.get("state") != "OPEN" or view.get("mergeable") != "CONFLICTING":
                continue
            # Someone pushed to the PR after this host's branch: merging into the stale copy
            # would only fail at the push.
            if view.get("headRefOid") != rt.git_head(claim):
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
        decision = admit_rerun(rt, claim)
        if not decision.ok:
            print(f"{key}: rebase held, {decision.reason}", file=sys.stderr)
            continue
        start_rebase(rt, claim, base)
