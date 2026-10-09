import json
import sys
from pathlib import Path

from linear_agents import ledger
from linear_agents.agentblock import settings_from_json
from linear_agents.context import phase_prompt, state_dir
from linear_agents.escalation import Escalation, scoped
from linear_agents.ledger import Claim
from linear_agents.sessions import claude_argv, clean_env, session_name
from linear_agents.usage import context_pct, latest_usage, read_status

HANDOFF_ASK = (
    "Your context is past {pct:.0f}%. Follow the handoff skill now: write handoff.md in your state"
    " directory, then run linear-agents handoff-ready. A fresh session will continue from it."
)
_MARKERS = ("done.json", "handoff-ready", "handoff-requested")
FIX_CONTEXT = "check-failure.md"
PUBLISH_TRIES = 3


class LaunchError(RuntimeError):
    pass


def _record_usage(rt, key: str, phase: str, event: str) -> None:
    u = latest_usage(rt.cfg.state_root, rt.now())
    if u is not None:
        ledger.add_usage(rt.con, key, phase, event, u.five_hour_pct, u.weekly_pct, rt.now())


def launch(rt, claim: Claim, phase: str, extra: str = "") -> None:
    token = rt.cfg.agent_gh_token_file
    if not token.exists() or not token.read_text().strip():
        raise LaunchError(f"{token} is missing or empty; agents get their GitHub token from it")
    d = state_dir(rt.cfg, claim.issue_key)
    # Before the deletes below: the outgoing session's status file is the freshest usage reading.
    _record_usage(rt, claim.issue_key, phase, "start")
    name = session_name(claim.issue_key, phase)
    env = clean_env(
        {
            "LINEAR_AGENTS_STATE": str(d),
            "LINEAR_AGENTS_CONFIG": str(rt.config_path),
            "LINEAR_AGENTS_PHASE": phase,
        }
    )
    settings = settings_from_json(claim.settings_json)
    prompt = phase_prompt(phase, claim.issue_key, extra)
    argv = claude_argv(rt.cfg, name, settings, prompt, env, rt.claude_exe, rt.exe)
    # After everything above that can raise. The previous session's markers would be taken for
    # the new one's, and its context reading would ask the new session to hand off at once.
    markers = {n: (d / n).read_bytes() for n in _MARKERS if (d / n).exists()}
    for leftover in (*_MARKERS, "status.json"):
        (d / leftover).unlink(missing_ok=True)
    try:
        rt.sessions.start(name, Path(claim.worktree), argv)
    except BaseException:
        # Without its marker the next tick would take the step for a crash instead of repeating
        # it: a tester rerun on its own commits, or a fix started without the check output.
        for n, content in markers.items():
            (d / n).write_bytes(content)
        raise


def _block(rt, claim: Claim, cause: str, summary: str, action: str) -> None:
    rt.sessions.kill(session_name(claim.issue_key, claim.phase))
    esc = scoped(Escalation(2, cause, summary, action, waiting=True), claim.issue_key)
    rt.notifier.send(esc, rt.linear.issue(claim.issue_key), None)
    ledger.update_claim(rt.con, claim.issue_key, state="blocked")


def _publish_failed(rt, claim: Claim, exc: Exception) -> None:
    # done.json stays, so every tick retries the whole publish, check included, under the tick
    # lock; a push that is rejected for good would hold the lock that long forever.
    key = claim.issue_key
    counter = f"publish_failures:{key}"
    fails = int(ledger.meta_get(rt.con, counter) or 0) + 1
    detail = (getattr(exc, "stderr", None) or "").strip()
    print(f"{key}: publish failed ({fails}/{PUBLISH_TRIES}): {exc} {detail}", file=sys.stderr)
    if fails < PUBLISH_TRIES:
        ledger.meta_set(rt.con, counter, str(fails))
        return
    _block(
        rt,
        claim,
        "publish-failed",
        f"Publishing failed {fails} times in a row: {exc}\n{detail[-1500:]}".strip(),
        f"Fix the push or the gh token, then run linear-agents release --keep-worktree {key}",
    )
    ledger.meta_set(rt.con, counter, "0")


def _relaunch(rt, claim: Claim) -> None:
    # The check output reaches a fix session only through its prompt; a restart needs it again.
    path = state_dir(rt.cfg, claim.issue_key) / FIX_CONTEXT
    extra = path.read_text() if claim.phase == "fix" and path.exists() else ""
    launch(rt, claim, claim.phase, extra=extra)


def _finish_phase(rt, claim: Claim, summary: str) -> None:
    key = claim.issue_key
    _record_usage(rt, key, claim.phase, "end")
    if claim.phase == "test":
        head = rt.git_head(claim.worktree)
        if head == claim.base_sha:
            _block(
                rt,
                claim,
                "no-tests",
                "The tester finished without committing tests.",
                f"Add detail to the acceptance criteria, then run linear-agents release {key}",
            )
            return
        if claim.base_sha is not None:
            ledger.remember_tests(rt.con, key, claim.base_sha, head)
        launch(rt, claim, "impl")
        ledger.update_claim(
            rt.con, key, tester_sha=head, phase="impl", restarts=0, started_at=rt.now()
        )
        return
    issue = rt.linear.issue(key)
    try:
        result = rt.publish_fn(claim, issue.title, summary)
    except Exception as exc:  # noqa: BLE001 - a push or gh failure is counted, not re-raised
        _publish_failed(rt, claim, exc)
        return
    if result.status == "opened":
        rt.linear.comment(issue.id, f"Draft PR: {result.url}")
        ledger.add_digest(rt.con, key, f"draft PR ready: {result.url}", rt.now())
        ledger.update_claim(rt.con, key, state="done")
    elif result.status == "check_failed" and claim.fix_attempts == 0:
        extra = f"The check failed with:\n\n{result.detail}"
        (state_dir(rt.cfg, key) / FIX_CONTEXT).write_text(extra)
        launch(rt, claim, "fix", extra=extra)
        ledger.update_claim(
            rt.con, key, phase="fix", fix_attempts=1, restarts=0, started_at=rt.now()
        )
    elif result.status == "check_failed":
        _block(
            rt,
            claim,
            "check-failed",
            f"The check still fails after one fix:\n{result.detail}",
            f"Fix the branch or the issue, then run linear-agents release --keep-worktree {key}",
        )
    else:
        _block(
            rt,
            claim,
            "tests-touched",
            f"The implementer changed the tester's files: {result.detail}",
            "If the test change is wrong, revert it on the branch, then run"
            f" linear-agents release --keep-worktree {key}",
        )


def supervise(rt, claim: Claim) -> None:
    key = claim.issue_key
    d = state_dir(rt.cfg, key)
    name = session_name(key, claim.phase)
    done = d / "done.json"
    if done.exists():
        try:
            summary = json.loads(done.read_text()).get("summary", "")
        except ValueError:  # still being written; the next tick reads it whole
            return
        rt.sessions.kill(name)
        _finish_phase(rt, claim, summary)
        return
    if (d / "handoff-ready").exists():
        rt.sessions.kill(name)
        _relaunch(rt, claim)
        return
    if not rt.sessions.alive(name):
        if claim.restarts >= 1:
            _block(
                rt,
                claim,
                "crash",
                "The session died twice.",
                f"Check the worktree, then run linear-agents release --keep-worktree {key}",
            )
            return
        _relaunch(rt, claim)
        ledger.update_claim(rt.con, key, restarts=claim.restarts + 1)
        return
    if rt.now() - claim.started_at > claim.wall_clock_s:
        rt.sessions.kill(name)
        line = f"stopped at the wall-clock limit ({name}); rerun with linear-agents release {key}"
        ledger.add_digest(rt.con, key, line, rt.now())
        _record_usage(rt, key, claim.phase, "end")
        ledger.update_claim(rt.con, key, state="timed_out")
        return
    pct = context_pct(read_status(d) or {})
    requested = d / "handoff-requested"
    if pct is not None and pct >= rt.cfg.context_restart_pct and not requested.exists():
        rt.sessions.send(name, HANDOFF_ASK.format(pct=pct))
        requested.touch()
