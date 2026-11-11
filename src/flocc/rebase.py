import sys

from flocc import ledger
from flocc.poller import finished_prs, pushed_elsewhere, rerun_held, still_wanted
from flocc.supervisor import REBASE_TRIED, block_unrebased, start_rebase
from flocc.worktree import origin_base

MERGE_PUSHED = "merge_pushed"
MERGE_HEAD = "merge_head"


def _merge_cleanly(rt, claim, base_sha: str) -> bool:
    """True when the base merged into the branch without a session: pushed, or waiting on the
    check. False leaves the worktree as it was and the caller starts the fix session.

    GitHub cannot run the repo's merge drivers, so a PR that conflicts only in files they settle
    (the docket ledgers) merges here for nothing."""
    key = claim.issue_key
    base = rt.cfg.repos[claim.repo].base_branch
    if rt.agent is None:
        return False
    try:
        merged = rt.agent.merge_base(claim, base)
        if merged["status"] == "dirty":
            print(f"{key}: base not merged, the worktree has uncommitted changes", file=sys.stderr)
            return False
        if merged["status"] == "conflict":
            return False
        ledger.meta_set(rt.con, f"{MERGE_HEAD}:{key}", merged["head"])
        # The check runs before any push; the PR is open, so only its branch is published.
        result = rt.publish_fn(claim, key, "", reuse_only=True)
    except Exception as exc:  # noqa: BLE001 - one PR's failure must not stop the others
        print(f"{key}: base not merged: {exc}", file=sys.stderr)
        return True
    if result.status == "check_running":
        return True
    ledger.meta_set(rt.con, f"{MERGE_HEAD}:{key}", "")
    if result.status == "opened":
        ledger.meta_set(rt.con, f"{MERGE_PUSHED}:{key}", base_sha)
        line = f"merged {base} cleanly, no session needed: {result.url}"
        ledger.add_digest(rt.con, key, line, rt.now())
        rt.linear.comment(
            rt.linear.issue(key).id, f"Merged {base} into the branch and pushed to {result.url}."
        )
        return True
    if result.status == "pr_closed":
        return True
    print(f"{key}: merged {base} but it is not pushed: {result.status}", file=sys.stderr)
    return False


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
            # A merge waiting on its check leaves the branch ahead of the PR's head.
            merging = ledger.meta_get(rt.con, f"{MERGE_HEAD}:{key}")
            if pushed_elsewhere(rt, claim, view) and not (
                merging and merging == rt.git_head(claim)
            ):
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
        if ledger.meta_get(rt.con, f"{MERGE_PUSHED}:{key}") == base:
            continue  # pushed for this base; GitHub has not re-read the PR yet
        if ledger.meta_get(rt.con, f"{REBASE_TRIED}:{key}") == base:
            block_unrebased(rt, claim, base)
            continue
        if reason := rerun_held(rt, claim, bases):
            print(f"{key}: rebase held, {reason}", file=sys.stderr)
            continue
        if _merge_cleanly(rt, claim, base):
            continue
        start_rebase(rt, claim, base)
