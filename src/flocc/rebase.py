import sys
from pathlib import Path

from flocc import ledger
from flocc.a2atick import pr_url
from flocc.supervisor import REBASE_TRIED, block_unrebased, start_rebase
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
        try:
            view = rt.pr_view(claim, url)
        except Exception as exc:  # noqa: BLE001 - one PR's gh failure must not stop the others
            print(f"{claim.issue_key}: PR state not read: {exc}", file=sys.stderr)
            continue
        if view.get("state") != "OPEN" or view.get("mergeable") != "CONFLICTING":
            continue
        if claim.repo not in bases:
            bases[claim.repo] = origin_base(rt.cfg.repos[claim.repo])
        base = bases[claim.repo]
        if base is None:
            continue
        if ledger.meta_get(rt.con, f"{REBASE_TRIED}:{claim.issue_key}") == base:
            block_unrebased(rt, claim, base)
        else:
            start_rebase(rt, claim, base)
