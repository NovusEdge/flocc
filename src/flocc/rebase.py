import sys

from flocc import ledger
from flocc.poller import finished_prs, pushed_elsewhere, rerun_held, still_wanted
from flocc.supervisor import REBASE_TRIED, block_unrebased, start_rebase
from flocc.worktree import origin_base


def step(rt) -> None:
    """Starts a fix session on a finished claim's PR when the base moved and it conflicts.

    Needs the claim's worktree, so a PR whose worktree was reaped is left to its owner."""
    if rt.pr_view is None:
        return
    bases: dict[str, str | None] = {}
    for claim, url in finished_prs(rt):
        key = claim.issue_key
        try:
            view = rt.pr_view(claim, url)
            if view.get("state") != "OPEN" or view.get("mergeable") != "CONFLICTING":
                continue
            if pushed_elsewhere(rt, claim, view):
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
        if reason := rerun_held(rt, claim, bases):
            print(f"{key}: rebase held, {reason}", file=sys.stderr)
            continue
        start_rebase(rt, claim, base)
