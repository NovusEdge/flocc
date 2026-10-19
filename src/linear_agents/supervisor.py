import json
import re
import sys
from dataclasses import replace
from pathlib import Path

from linear_agents import grants as grants_mod
from linear_agents import ledger
from linear_agents.agentblock import settings_from_json
from linear_agents.context import phase_prompt, state_dir
from linear_agents.escalation import Escalation, scoped
from linear_agents.gate import clear_question, run_id
from linear_agents.ledger import Claim
from linear_agents.linear import LinearError
from linear_agents.publisher import review_comment
from linear_agents.sessions import (
    allowed_plugins,
    claude_argv,
    clean_env,
    installed_plugins,
    session_name,
    trust_folders,
)
from linear_agents.usage import context_pct, latest_usage, read_status

HANDOFF_ASK = (
    "Your context is past {pct:.0f}%. Follow the handoff skill now: write handoff.md in your state"
    " directory, then run linear-agents handoff-ready. A fresh session will continue from it."
)
_MARKERS = ("done.json", "handoff-ready", "handoff-requested")
FIX_CONTEXT = "check-failure.md"
QUESTION_WAIT_S = 7200
PUBLISH_TRIES = 3
POST_TRIES = 3
_VERDICT = re.compile(
    r"^[\s*_#>-]*verdict[\s*_]*:?[\s*_]*(approve|changes requested)", re.IGNORECASE | re.MULTILINE
)


class LaunchError(RuntimeError):
    pass


def _record_usage(rt, key: str, phase: str, event: str) -> None:
    u = latest_usage(rt.cfg.state_root, rt.now())
    if u is not None:
        ledger.add_usage(rt.con, key, phase, event, u.five_hour_pct, u.weekly_pct, rt.now())


def _disabled_plugins(rt) -> tuple[str, ...]:
    cfg = rt.cfg
    listed = cfg.agent_plugins_file or cfg.plugin_dir.parent / "deploy" / "agent-plugins.txt"
    allowed = allowed_plugins(listed)
    if allowed is None:
        return ()
    return tuple(p for p in installed_plugins(cfg.installed_plugins_file) if p not in allowed)


def launch(rt, claim: Claim, phase: str, extra: str = "", started_at: float | None = None) -> None:
    token = rt.cfg.agent_gh_token_file
    if not token.exists() or not token.read_text().strip():
        raise LaunchError(f"{token} is missing or empty; agents get their GitHub token from it")
    d = state_dir(rt.cfg, claim.issue_key)
    # Before the deletes below: the outgoing session's status file is the freshest usage reading.
    _record_usage(rt, claim.issue_key, phase, "start")
    name = session_name(claim.issue_key, phase)
    start = claim.started_at if started_at is None else started_at
    env = clean_env(
        {
            "LINEAR_AGENTS_STATE": str(d),
            "LINEAR_AGENTS_CONFIG": str(rt.config_path),
            "LINEAR_AGENTS_PHASE": phase,
            # Handoffs and crash relaunches of one phase share its launch budget.
            "LINEAR_AGENTS_RUN": run_id(phase, start),
            "CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH": "1",
            "CLAUDE_CODE_DISABLE_EXPLORE_PLAN_AGENTS": "1",
        }
    )
    settings = settings_from_json(claim.settings_json)
    if settings.allow:
        grants_mod.set_block(d / "grants.json", grants_mod.parse_allow(settings.allow))
    granted = grants_mod.for_phase(d / "grants.json", phase)
    if phase == "review":
        settings = replace(settings, model=rt.cfg.review_model)
    if settings.effort is None:
        settings = replace(settings, effort=rt.cfg.effort_for(phase))
    prompt = phase_prompt(phase, claim.issue_key, extra)
    argv = claude_argv(
        rt.cfg,
        name,
        settings,
        prompt,
        env,
        rt.claude_exe,
        rt.exe,
        grants=granted,
        disabled_plugins=_disabled_plugins(rt),
        remote_control=phase in ("impl", "fix"),
    )
    # After everything above that can raise. The previous session's markers would be taken for
    # the new one's, and its context reading would ask the new session to hand off at once.
    markers = {n: (d / n).read_bytes() for n in _MARKERS if (d / n).exists()}
    for leftover in (*_MARKERS, "status.json", "compacted"):
        (d / leftover).unlink(missing_ok=True)
    clear_question(d)
    # Both keys: for a worktree, trust may be keyed on the main checkout's root instead.
    repo = rt.cfg.repos.get(claim.repo)
    trust_folders(rt.cfg.claude_json, [claim.worktree, *([repo.path] if repo else [])])
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


def _review_skipped(rt, claim: Claim, why: str) -> None:
    # A review is advice on an open PR; losing it must neither block the claim nor page anyone.
    key = claim.issue_key
    url = _pr_url(rt, claim)
    rt.sessions.kill(session_name(key, "review"))
    ledger.add_digest(rt.con, key, f"review skipped, {why}; PR: {url}", rt.now())
    ledger.meta_set(rt.con, f"review_posted:{key}", "")
    ledger.update_claim(rt.con, key, state="done")


def _pr_url(rt, claim: Claim) -> str:
    path = state_dir(rt.cfg, claim.issue_key) / "pr-url"
    return path.read_text().strip() if path.exists() else "unknown"


def _start_review(rt, claim: Claim, title: str, description: str, url: str) -> None:
    key = claim.issue_key
    d = state_dir(rt.cfg, key)
    start = rt.now()
    usage = latest_usage(rt.cfg.state_root, rt.now())
    if usage is not None and (
        usage.five_hour_pct >= rt.cfg.five_hour_max_pct or usage.weekly_pct >= rt.cfg.weekly_max_pct
    ):
        (d / "pr-url").write_text(url)
        ledger.add_digest(rt.con, key, f"review skipped, usage over the limit; PR: {url}", rt.now())
        ledger.update_claim(rt.con, key, state="done")
        return
    try:
        (d / "pr-url").write_text(url)
        (d / "pr.diff").write_text(rt.review_diff(claim) + "\n")
        (d / "review.md").unlink(missing_ok=True)
        (d / "review-input.md").write_text(
            f"# Review {key}: {title}\n\nPR: {url}\n\nThe diff is in {d / 'pr.diff'}.\n\n"
            f"## Issue description, including the acceptance criteria\n\n{description}\n"
        )
        launch(rt, claim, "review", started_at=start)
    except Exception as exc:  # noqa: BLE001 - the PR is open; nothing here may block the claim
        print(f"{key}: review not started: {exc}", file=sys.stderr)
        ledger.add_digest(rt.con, key, f"review not started ({exc}); PR: {url}", rt.now())
        ledger.update_claim(rt.con, key, state="done")
        return
    ledger.update_claim(rt.con, key, phase="review", restarts=0, started_at=start)


def _finish_review(rt, claim: Claim) -> None:
    key = claim.issue_key
    review = state_dir(rt.cfg, key) / "review.md"
    text = review.read_text() if review.exists() else ""
    if not text.strip():
        _review_skipped(rt, claim, "the session finished without review.md")
        return
    url = _pr_url(rt, claim)
    counter = f"review_post_failures:{key}"
    posted = f"review_posted:{key}"
    # Set right after the post: a tick that dies before finishing the claim must not comment twice.
    if not ledger.meta_get(rt.con, posted):
        try:
            rt.comment_pr(claim, url, review_comment(text))
        except Exception as exc:  # noqa: BLE001 - a gh failure is counted, not re-raised
            fails = int(ledger.meta_get(rt.con, counter) or 0) + 1
            print(f"{key}: review comment failed ({fails}/{POST_TRIES}): {exc}", file=sys.stderr)
            if fails < POST_TRIES:
                ledger.meta_set(rt.con, counter, str(fails))
                return
            ledger.meta_set(rt.con, counter, "0")
            _review_skipped(rt, claim, f"posting the comment failed {fails} times: {exc}")
            return
        ledger.meta_set(rt.con, counter, "0")
        ledger.meta_set(rt.con, posted, "1")
    matches = _VERDICT.findall(text)
    verdict = matches[-1].lower() if matches else "no verdict"
    line = f"Review: {verdict} — {url}"
    try:
        rt.linear.comment(claim.issue_id, line)
    except LinearError:
        ledger.add_digest(rt.con, key, line, rt.now())
    ledger.meta_set(rt.con, posted, "")
    ledger.update_claim(rt.con, key, state="done")


def _finish_phase(rt, claim: Claim, summary: str) -> None:
    key = claim.issue_key
    _record_usage(rt, key, claim.phase, "end")
    if claim.phase == "review":
        _finish_review(rt, claim)
        return
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
        start = rt.now()
        launch(rt, claim, "impl", started_at=start)
        ledger.update_claim(
            rt.con, key, tester_sha=head, phase="impl", restarts=0, started_at=start
        )
        return
    issue = rt.linear.issue(key)
    try:
        result = rt.publish_fn(claim, issue.title, summary)
    except Exception as exc:  # noqa: BLE001 - a push or gh failure is counted, not re-raised
        _publish_failed(rt, claim, exc)
        return
    # Failures count only in a row: the fix phase's publish, or a rerun's, starts from zero.
    ledger.meta_set(rt.con, f"publish_failures:{key}", "0")
    if result.status == "opened":
        rt.linear.comment(issue.id, f"PR for review: {result.url}")
        ledger.add_digest(rt.con, key, f"PR ready for review: {result.url}", rt.now())
        if result.untrailed:
            line = (
                f"{key}: commits without the Linear-Agents trailer: {', '.join(result.untrailed)}"
            )
            ledger.add_digest(rt.con, key, line, rt.now())
        _start_review(rt, claim, issue.title, issue.description, result.url)
    elif result.status == "check_failed" and claim.fix_attempts == 0:
        extra = f"The check failed with:\n\n{result.detail}"
        (state_dir(rt.cfg, key) / FIX_CONTEXT).write_text(extra)
        start = rt.now()
        launch(rt, claim, "fix", extra=extra, started_at=start)
        ledger.update_claim(rt.con, key, phase="fix", fix_attempts=1, restarts=0, started_at=start)
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


def _check_question(rt, claim: Claim, d: Path) -> None:
    asking = d / "asking"
    if not asking.exists() or (d / "asking-escalated").exists():
        return
    try:
        q = json.loads(asking.read_text())
        at, text = float(q["at"]), str(q.get("question", ""))
    except (OSError, ValueError, KeyError, TypeError):
        return
    if rt.now() - at < QUESTION_WAIT_S:
        return
    esc = scoped(
        Escalation(
            2,
            "question",
            f"Unanswered question in the Claude app: {text}",
            "Answer it in the Claude app, or reply on this issue",
            waiting=True,
        ),
        claim.issue_key,
    )
    rt.notifier.send(esc, rt.linear.issue(claim.issue_key), None)
    (d / "asking-escalated").touch()


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
        if claim.restarts >= 1 and claim.phase == "review":
            _review_skipped(rt, claim, f"the session died twice ({name})")
            return
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
    limit = rt.cfg.review_wall_clock_s if claim.phase == "review" else claim.wall_clock_s
    if rt.now() - claim.started_at > limit:
        if claim.phase == "review":
            _record_usage(rt, key, claim.phase, "end")
            _review_skipped(rt, claim, f"stopped at the wall-clock limit ({name})")
            return
        rt.sessions.kill(name)
        line = f"stopped at the wall-clock limit ({name}); rerun with linear-agents release {key}"
        ledger.add_digest(rt.con, key, line, rt.now())
        _record_usage(rt, key, claim.phase, "end")
        ledger.update_claim(rt.con, key, state="timed_out")
        return
    pct = context_pct(read_status(d) or {})
    requested = d / "handoff-requested"
    # One compaction first: the handoff applies only once the session has compacted.
    if (
        pct is not None
        and pct >= rt.cfg.context_restart_pct
        and (d / "compacted").exists()
        and not requested.exists()
    ):
        rt.sessions.send(name, HANDOFF_ASK.format(pct=pct))
        requested.touch()
    # Last: a Linear outage while escalating must not skip the handoff request above.
    _check_question(rt, claim, d)
