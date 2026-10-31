import sys
from dataclasses import replace
from pathlib import Path

from flocc import host, ledger
from flocc.admission import admit
from flocc.agentblock import settings_from_json
from flocc.context import state_dir
from flocc.ledger import Claim
from flocc.linear import LinearError
from flocc.sequence import ARCHETYPE
from flocc.sessions import session_name
from flocc.supervisor import BLOCKED, FIX_CONTEXT, launch
from flocc.usage import latest_usage

# One cardinal fixer per run; poller._start clears it with the claim's other per-run meta.
CARDINAL_FIX = "cardinal_fix"
NOTE_CHARS = 2000


class Refused(Exception):
    pass


def _fix_prompt(claim: Claim, note: str) -> str:
    stopped = "timed out" if claim.state == "timed_out" else "blocked"
    return (
        f"cardinal, the owner's supervisor, started this fix round after the run {stopped}."
        f" Its note:\n\n{note}\n\nThe previous step's summary is in `flocc context`. Fix what"
        " the note asks. What needs the owner goes in your summary under a line that starts"
        ' with "Needs the owner". Then finish as usual.'
    )


def admit_rerun(rt, claim: Claim):
    """Admission for another session on a finished claim, without its dependencies: they
    finished before the PR opened."""
    settings = replace(settings_from_json(claim.settings_json), depends_on=())
    return admit(
        claim.issue_key,
        claim.repo,
        settings,
        ledger.live_claims(rt.con),
        latest_usage(rt.cfg.agent_state_root, rt.now()),
        {},
        rt.cfg,
        rt.now(),
        rt.free_disk_gb(rt.cfg.worktree_root),
        ledger.held_keys(rt.con),
        host.pressure_detail(rt.con),
    )


def start_fix(rt, key: str, note: str) -> Claim:
    note = note.strip()
    if not note:
        raise Refused("pass a note saying what to fix")
    if len(note) > NOTE_CHARS:
        raise Refused(f"the note is {len(note)} characters; keep it under {NOTE_CHARS}")
    claim = ledger.get_claim(rt.con, key)
    if claim is None:
        raise Refused(f"no claim for {key}")
    if claim.state not in ("blocked", "timed_out"):
        raise Refused(f"{key} is {claim.state}; a fixer starts only on a blocked or timed-out run")
    if claim.phase not in ("impl", "fix"):
        raise Refused(
            f"{key} stopped at the {ARCHETYPE[claim.phase]}; a fixer needs the executor's branch"
        )
    cap = rt.cfg.check_fix_rounds
    if claim.fix_attempts >= cap:
        raise Refused(f"{key} used {claim.fix_attempts} of {cap} fix rounds")
    if ledger.meta_get(rt.con, f"{CARDINAL_FIX}:{key}"):
        raise Refused(f"cardinal already started a fixer on this run of {key}")
    if key in ledger.issue_holds(rt.con):
        raise Refused(f"{key} is held; the owner's flocc release {key} lifts the hold")
    if not Path(claim.worktree).exists():
        raise Refused(f"{key}'s worktree is gone")
    decision = admit_rerun(rt, claim)
    if not decision.ok:
        raise Refused(f"not now: {decision.reason}")
    extra = _fix_prompt(claim, note)
    d = state_dir(rt.cfg, key)
    d.mkdir(parents=True, exist_ok=True)
    (d / FIX_CONTEXT).write_text(extra)
    rt.sessions.kill(session_name(key, claim.phase))
    start = rt.now()
    launch(rt, claim, "fix", extra=extra)
    ledger.meta_set(rt.con, f"{CARDINAL_FIX}:{key}", "1")
    ledger.update_claim(
        rt.con,
        key,
        state="running",
        phase="fix",
        fix_attempts=claim.fix_attempts + 1,
        restarts=0,
        started_at=start,
    )
    (d / BLOCKED).unlink(missing_ok=True)
    try:
        rt.linear.comment(claim.issue_id, f"cardinal started a fixer:\n\n{note}")
    except LinearError as exc:
        print(f"{key}: fix comment not posted: {exc}", file=sys.stderr)
    return ledger.get_claim(rt.con, key) or claim
