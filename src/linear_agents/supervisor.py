import json
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
    # The previous session's markers would be taken for the new one's, and its context reading
    # would ask the new session to hand off at once.
    markers = {n: (d / n).read_bytes() for n in _MARKERS if (d / n).exists()}
    for leftover in (*_MARKERS, "status.json"):
        (d / leftover).unlink(missing_ok=True)
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
        launch(rt, claim, "impl")
        ledger.update_claim(
            rt.con, key, tester_sha=head, phase="impl", restarts=0, started_at=rt.now()
        )
        return
    issue = rt.linear.issue(key)
    result = rt.publish_fn(claim, issue.title, summary)
    if result.status == "opened":
        rt.linear.comment(issue.id, f"Draft PR: {result.url}")
        ledger.add_digest(rt.con, key, f"draft PR ready: {result.url}", rt.now())
        ledger.update_claim(rt.con, key, state="done")
    elif result.status == "check_failed" and claim.fix_attempts == 0:
        launch(rt, claim, "fix", extra=f"The check failed with:\n\n{result.detail}")
        ledger.update_claim(
            rt.con, key, phase="fix", fix_attempts=1, restarts=0, started_at=rt.now()
        )
    elif result.status == "check_failed":
        _block(
            rt,
            claim,
            "check-failed",
            f"The check still fails after one fix:\n{result.detail}",
            "Look at the branch and say how to proceed",
        )
    else:
        _block(
            rt,
            claim,
            "tests-touched",
            f"The implementer changed the tester's files: {result.detail}",
            "Decide whether the test change is legitimate",
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
        launch(rt, claim, claim.phase)
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
        launch(rt, claim, claim.phase)
        ledger.update_claim(rt.con, key, restarts=claim.restarts + 1)
        return
    if rt.now() - claim.started_at > claim.wall_clock_s:
        rt.sessions.kill(name)
        ledger.add_digest(rt.con, key, f"stopped at the wall-clock limit ({name})", rt.now())
        _record_usage(rt, key, claim.phase, "end")
        ledger.update_claim(rt.con, key, state="timed_out")
        return
    pct = context_pct(read_status(d) or {})
    requested = d / "handoff-requested"
    if pct is not None and pct >= rt.cfg.context_restart_pct and not requested.exists():
        rt.sessions.send(name, HANDOFF_ASK.format(pct=pct))
        requested.touch()
