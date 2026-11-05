import json
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
from flocc.supervisor import BLOCKED, FIX_CONTEXT, QUESTIONS, RESUME_NOTE, launch
from flocc.usage import latest_usage

# One cardinal resume per claim; poller._start clears both keys with the claim's other per-run
# meta. cardinal_fix is what the fix verb set before resume replaced it.
CARDINAL_RESUME = "cardinal_resume"
_CAPS = (CARDINAL_RESUME, "cardinal_fix")
RESUME_LOG = "resume_log"
NOTE_CHARS = 2000
# The stops a note can redirect. tests-touched guards the tester's work against the executor,
# the publish, branch and rebase stops need the host or the owner, and no-tests needs criteria.
RESUMABLE = frozenset({"needs-owner", "check-failed", "no-changes", "idle", "crash", "timed-out"})


class Refused(Exception):
    pass


def stop_cause(rt, claim: Claim) -> str:
    """Why the claim stopped: timed-out, or the cause _block wrote on blocked.md's first line."""
    if claim.state == "timed_out":
        return "timed-out"
    path = state_dir(rt.cfg, claim.issue_key) / BLOCKED
    return path.read_text().split("\n", 1)[0].strip() if path.exists() else ""


def _questions(rt, key: str) -> list[str]:
    path = state_dir(rt.cfg, key) / QUESTIONS
    try:
        asked = json.loads(path.read_text())
    except (OSError, ValueError):
        return []
    return [str(q) for q in asked] if isinstance(asked, list) else []


def _resume_prompt(rt, claim: Claim, cause: str, note: str) -> str:
    if cause == "needs-owner":
        asked = _questions(rt, claim.issue_key)
        listed = "\n".join(f"{n}. {q}" for n, q in enumerate(asked, 1)) or "(not kept)"
        head = (
            "cardinal, the owner's supervisor, answered the questions this step stopped on."
            f"\n\nThe questions:\n\n{listed}\n\nIts answer:\n\n{note}"
        )
    else:
        stopped = "timed out" if cause == "timed-out" else f"stopped ({cause})"
        head = (
            f"cardinal, the owner's supervisor, resumed this step after it {stopped}."
            f" Its note:\n\n{note}"
        )
    return (
        f"{head}\n\nThe previous step's summary is in `flocc context`; the branch holds what was"
        " committed. For anything that still needs the owner, use flocc done --needs-owner."
        " Then finish as usual."
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


def resume(rt, key: str, note: str) -> Claim:
    note = note.strip()
    if not note:
        raise Refused("pass a note saying what the next attempt does")
    if len(note) > NOTE_CHARS:
        raise Refused(f"the note is {len(note)} characters; keep it under {NOTE_CHARS}")
    claim = ledger.get_claim(rt.con, key)
    if claim is None:
        raise Refused(f"no claim for {key}")
    if claim.state not in ("blocked", "timed_out"):
        raise Refused(f"{key} is {claim.state}; cardinal resumes only a blocked or timed-out run")
    cause = stop_cause(rt, claim)
    if cause not in RESUMABLE:
        raise Refused(
            f"{key} stopped with {cause or 'no recorded cause'}; that stop is the owner's"
        )
    cap = rt.cfg.check_fix_rounds
    # The same-failure block and the cap block share the cause; only the rounds tell them apart.
    if cause == "check-failed" and claim.fix_attempts >= cap:
        raise Refused(
            f"{key} used {claim.fix_attempts} of {cap} fix rounds; that stop is the owner's"
        )
    if any(ledger.meta_get(rt.con, f"{k}:{key}") for k in _CAPS):
        raise Refused(f"cardinal already resumed this run of {key}")
    if key in ledger.issue_holds(rt.con):
        raise Refused(f"{key} is held; the owner's flocc release {key} lifts the hold")
    if not Path(claim.worktree).exists():
        raise Refused(f"{key}'s worktree is gone")
    decision = admit_rerun(rt, claim)
    if not decision.ok:
        raise Refused(f"not now: {decision.reason}")
    phase = "fix" if cause == "check-failed" else claim.phase
    extra = _resume_prompt(rt, claim, cause, note)
    d = state_dir(rt.cfg, key)
    d.mkdir(parents=True, exist_ok=True)
    if phase == "fix":
        # Before the launch, which hands the file to the session; a fixer keeps the check output
        # it was started with.
        context = d / FIX_CONTEXT
        if context.exists():
            extra = f"{extra}\n\n{context.read_text()}"
        context.write_text(extra)
    rt.sessions.kill(session_name(key, claim.phase))
    start = rt.now()
    launch(rt, claim, phase, extra=extra)
    if phase != "fix":
        (d / RESUME_NOTE).write_text(extra)
    ledger.meta_set(rt.con, f"{CARDINAL_RESUME}:{key}", "1")
    ledger.update_claim(
        rt.con,
        key,
        state="running",
        phase=phase,
        fix_attempts=claim.fix_attempts + (cause == "check-failed"),
        restarts=0,
        started_at=start,
    )
    (d / BLOCKED).unlink(missing_ok=True)
    row = {"at": start, "key": key, "cause": cause, "phase": phase}
    ledger.meta_set(rt.con, f"{RESUME_LOG}:{start:.6f}", json.dumps(row))
    try:
        rt.linear.comment(
            claim.issue_id, f"cardinal resumed the {ARCHETYPE[phase]} after {cause}:\n\n{note}"
        )
    except LinearError as exc:
        print(f"{key}: resume comment not posted: {exc}", file=sys.stderr)
    return ledger.get_claim(rt.con, key) or claim


def resumes(con, since: float) -> list[tuple[float, str, str, str]]:
    """cardinal's resumes since `since`: (at, key, cause, phase), oldest first."""
    rows = []
    for (value,) in con.execute("SELECT value FROM meta WHERE key LIKE ?", (f"{RESUME_LOG}:%",)):
        try:
            v = json.loads(value)
            row = (float(v["at"]), str(v["key"]), str(v["cause"]), str(v["phase"]))
        except (ValueError, KeyError, TypeError):
            continue
        if row[0] >= since:
            rows.append(row)
    return sorted(rows)
