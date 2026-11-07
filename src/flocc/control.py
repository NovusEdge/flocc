import json
import subprocess
import sys
import traceback
from dataclasses import replace
from pathlib import Path

from flocc import host, ledger
from flocc.admission import admit
from flocc.agentblock import settings_from_json
from flocc.causes import AGENT, RESUMABLE
from flocc.config import Config
from flocc.context import state_dir
from flocc.ledger import Claim
from flocc.linear import LinearError
from flocc.notice import Notice
from flocc.sequence import ARCHETYPE
from flocc.sessions import session_name
from flocc.supervisor import BLOCKED, FIX_CONTEXT, QUESTIONS, RESUME_NOTE, LaunchError, launch
from flocc.usage import latest_usage

# One cardinal resume per claim; poller._start clears it with the claim's other per-run meta.
CARDINAL_RESUME = "cardinal_resume"
RESUME_LOG = "resume_log"
# A resume admission refused for a cap or usage, as {"note", "at", "started_at"} (the claim's); a
# restarted claim has another started_at, so the entry is dropped rather than resumed twice.
# poller._start clears it too.
PENDING_RESUME = "resume_pending"
PENDING_TTL_S = 24 * 3600
NOTE_CHARS = 2000


class Refused(Exception):
    pass


class Queued(Exception):
    """Admission refused for a cap or usage; the resume waits for run_pending_resumes."""


def stop_cause(cfg: Config, claim: Claim) -> str:
    """Why the claim stopped: timed-out, or the cause _block wrote on blocked.md's first line."""
    if claim.state == "timed_out":
        return "timed-out"
    path = state_dir(cfg, claim.issue_key) / BLOCKED
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


def _resumable(rt, key: str, *, queued: bool = False) -> tuple[Claim, str]:
    """The claim and its stop cause, or Refused. A queued resume already holds the run's one
    resume."""
    claim = ledger.get_claim(rt.con, key)
    if claim is None:
        raise Refused(f"no claim for {key}")
    if claim.state not in ("blocked", "timed_out"):
        raise Refused(f"{key} is {claim.state}; cardinal resumes only a blocked or timed-out run")
    cause = stop_cause(rt.cfg, claim)
    if cause not in RESUMABLE:
        raise Refused(
            f"{key} stopped with {cause or 'no recorded cause'}; that stop is the owner's"
        )
    cap = rt.cfg.check_fix_rounds
    # Blocks at the cap write check-cap now; one written before that still says check-failed.
    if cause == "check-failed" and claim.fix_attempts >= cap:
        raise Refused(
            f"{key} used {claim.fix_attempts} of {cap} fix rounds; that stop is the owner's"
        )
    if not queued and ledger.meta_get(rt.con, f"{CARDINAL_RESUME}:{key}"):
        raise Refused(f"cardinal already resumed this run of {key}")
    if key in ledger.issue_holds(rt.con):
        raise Refused(f"{key} is held; the owner's flocc release {key} lifts the hold")
    if not Path(claim.worktree).exists():
        raise Refused(f"{key}'s worktree is gone")
    return claim, cause


def resume(rt, key: str, note: str) -> Claim:
    """Restarts the run; raises Queued when a cap or usage refused it and it was stored."""
    note = note.strip()
    if not note:
        raise Refused("pass a note saying what the next attempt does")
    if len(note) > NOTE_CHARS:
        raise Refused(f"the note is {len(note)} characters; keep it under {NOTE_CHARS}")
    claim, cause = _resumable(rt, key)
    decision = admit_rerun(rt, claim)
    if not decision.ok:
        if not decision.queue:
            raise Refused(f"not now: {decision.reason}")
        # The mark is set now, so a second resume of this run is refused while this one waits.
        ledger.meta_set(
            rt.con,
            f"{PENDING_RESUME}:{key}",
            json.dumps({"note": note, "at": rt.now(), "started_at": claim.started_at}),
        )
        ledger.meta_set(rt.con, f"{CARDINAL_RESUME}:{key}", "1")
        raise Queued(decision.reason)
    return _launch_resume(rt, claim, cause, note)


def _launch_resume(rt, claim: Claim, cause: str, note: str) -> Claim:
    key = claim.issue_key
    phase = "fix" if cause == "check-failed" else claim.phase
    extra = _resume_prompt(rt, claim, cause, note)
    d = state_dir(rt.cfg, key)
    d.mkdir(parents=True, exist_ok=True)
    context = d / FIX_CONTEXT
    old_context = context.read_text() if context.exists() else None
    if phase == "fix":
        # Before the launch, which hands the file to the session; a fixer keeps the check output
        # it was started with.
        if old_context is not None:
            extra = f"{extra}\n\n{old_context}"
        context.write_text(extra)
    rt.sessions.kill(session_name(key, claim.phase))
    start = rt.now()
    try:
        launch(rt, claim, phase, extra=extra)
    except BaseException:
        # A retry would otherwise stack its note on one that no session ever saw.
        if phase == "fix":
            if old_context is None:
                context.unlink(missing_ok=True)
            else:
                context.write_text(old_context)
        raise
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


def run_pending_resumes(rt) -> None:
    """Launches queued resumes, oldest first, while admission allows; drops those past
    PENDING_TTL_S, whose issue is no longer wanted (moved to Todo, closed, unlabelled) or whose
    run changed. Runs before poll admits Todo work, so a queued resume gets a freed slot first,
    and so a move to Todo is seen here before the resume could start on it."""
    rows = rt.con.execute(
        "SELECT key, value FROM meta WHERE key LIKE ? AND value != ''", (f"{PENDING_RESUME}:%",)
    ).fetchall()
    pending = []
    for row in rows:
        key = row["key"].split(":", 1)[1]
        try:
            entry = json.loads(row["value"])
            pending.append(
                (float(entry["at"]), key, str(entry["note"]), float(entry["started_at"]))
            )
        except (ValueError, KeyError, TypeError):
            ledger.meta_set(rt.con, row["key"], "")
    if not pending:
        return
    states = _issue_states(rt, [key for _, key, _, _ in pending])
    for at, key, note, started_at in sorted(pending):
        try:
            if rt.now() - at >= PENDING_TTL_S:
                _drop_resume(rt, key, note, "it waited 24 hours for a slot")
                continue
            claim = ledger.get_claim(rt.con, key)
            if claim is not None and claim.started_at != started_at:
                # A newer run holds the key; its own resume mark is not this entry's to clear.
                print(f"{key}: queued resume belongs to an earlier run, dropped", file=sys.stderr)
                ledger.meta_set(rt.con, f"{PENDING_RESUME}:{key}", "")
                continue
            if key not in states:
                continue
            if not wanted(rt, key, states[key]):
                _drop_resume(rt, key, note, "the issue is no longer in progress with its label")
                continue
            try:
                claim, cause = _resumable(rt, key, queued=True)
            except Refused as exc:
                _drop_resume(rt, key, note, str(exc))
                continue
            decision = admit_rerun(rt, claim)
            if not decision.ok:
                # Oldest first: a younger one must not take the slot the older one waits for.
                print(f"{key}: queued resume waits, {decision.reason}", file=sys.stderr)
                return
            try:
                _launch_resume(rt, claim, cause, note)
            except (LaunchError, subprocess.SubprocessError, OSError) as exc:
                print(f"{key}: queued resume did not start, will retry: {exc}", file=sys.stderr)
                continue
            ledger.meta_set(rt.con, f"{PENDING_RESUME}:{key}", "")
        except Exception:  # noqa: BLE001 - one bad entry must not stop the others or Todo admission
            print(f"{key}: queued resume failed, will retry", file=sys.stderr)
            traceback.print_exc()


def wanted(rt, key: str, state_type: str) -> bool:
    """False for an issue since parked, unlabelled or closed, given its Linear state type."""
    return state_type == "started" and rt.cfg.trigger_label in rt.linear.issue(key).labels


def _issue_states(rt, keys: list[str]) -> dict[str, str]:
    """Linear's state type per key in one call; a key it cannot read is absent, and its entry
    waits for the next tick. A deleted issue makes the batch fail, so the keys are then asked one
    by one and a deleted one counts as canceled, as reap_worktrees reads it."""
    try:
        return rt.linear.state_types(keys)
    except LinearError:
        pass
    states = {}
    for key in keys:
        try:
            states[key] = rt.linear.state_types([key])[key]
        except LinearError as exc:
            if "entity not found" in str(exc).lower():
                states[key] = "canceled"
            else:
                print(f"{key}: queued resume not checked: {exc}", file=sys.stderr)
    return states


def _drop_resume(rt, key: str, note: str, why: str) -> None:
    """Clears a queued resume and tells cardinal and the owner, with the note, since the resume
    mark goes too and cardinal may then resume the run again."""
    print(f"{key}: queued resume dropped: {why}", file=sys.stderr)
    ledger.meta_set(rt.con, f"{PENDING_RESUME}:{key}", "")
    ledger.meta_set(rt.con, f"{CARDINAL_RESUME}:{key}", "")
    notice = Notice(
        f"{key}:resume-dropped",
        f"Cardinal's queued resume of {key} was dropped: {why}. Its note was: {note}",
        "Cardinal may resume the run again if it is still wanted",
        issue_key=key,
        event="ended",
        detail=f"resume-dropped: {why}. Note: {note}",
        stop_type=AGENT,
    )
    try:
        issue = rt.linear.issue(key)
    except LinearError:
        issue = None
    try:
        rt.notifier.send(notice, issue)
    except LinearError:
        rt.notifier.send(notice)


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
