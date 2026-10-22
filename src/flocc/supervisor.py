import hashlib
import json
import re
import sys
from dataclasses import replace
from pathlib import Path

from flocc import grants as grants_mod
from flocc import ledger
from flocc.agentblock import settings_from_json
from flocc.context import phase_prompt, state_dir
from flocc.escalation import Escalation, scoped
from flocc.gate import (
    ASKED,
    LAUNCH_GRANTS_ENV,
    clear_idle,
    clear_question,
    run_id,
    running_agents,
)
from flocc.ledger import Claim
from flocc.linear import LinearError
from flocc.publisher import BranchMoved, amended_section, read_amended, review_comment
from flocc.sessions import (
    allowed_plugins,
    claude_argv,
    clean_env,
    installed_plugins,
    session_name,
    trust_folders,
)
from flocc.usage import context_pct, latest_usage, read_status

HANDOFF_ASK = (
    "Your context is past {pct:.0f}%. Follow the handoff skill now: write handoff.md in your state"
    " directory, then run flocc handoff-ready. A fresh session will continue from it."
)
_MARKERS = ("done.json", "handoff-ready", "handoff-requested")
FIX_CONTEXT = "check-failure.md"
FAILURE_HASH = "check-failure.sha256"
CHECK_OUTPUT = "check-output.log"
OWNER_ITEMS = "owner-items.md"
QUESTION_WAIT_S = 7200
PANE = "pane.json"
STALL_ESCALATED = "stall-escalated"
STALL_LINES = 60
STALL_TAIL = 12
PUBLISH_TRIES = 3
POST_TRIES = 3
_VERDICT = re.compile(
    r"^[\s*_#>-]*verdict[\s*_]*:?[\s*_]*(approve|changes requested)", re.IGNORECASE | re.MULTILINE
)


_OWNER_HEADING = re.compile(r"^[\s#*-]*needs the owner", re.IGNORECASE | re.MULTILINE)


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
            "FLOCC_STATE": str(d),
            "FLOCC_CONFIG": str(rt.config_path),
            "FLOCC_PHASE": phase,
            # Handoffs and crash relaunches of one phase share its launch budget.
            "FLOCC_RUN": run_id(phase, start),
            "CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH": "1",
            "CLAUDE_CODE_DISABLE_EXPLORE_PLAN_AGENTS": "1",
        }
    )
    settings = settings_from_json(claim.settings_json)
    if settings.allow:
        grants_mod.set_block(d / "grants.json", grants_mod.parse_allow(settings.allow))
    granted = grants_mod.for_phase(d / "grants.json", phase)
    env[LAUNCH_GRANTS_ENV] = ",".join(sorted(grants_mod.launch_flags(granted)))
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
    for leftover in (*_MARKERS, "status.json", "compacted", PANE, STALL_ESCALATED):
        (d / leftover).unlink(missing_ok=True)
    clear_question(d)
    # A new phase or claim starts a new clock; a restart of the same session keeps its credit.
    if started_at is not None:
        (d / ASKED).unlink(missing_ok=True)
    clear_idle(d)
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
    detail = (getattr(exc, "stderr", None) or "").strip()
    if isinstance(exc, BranchMoved):
        print(f"{key}: publish failed: {exc} {detail}", file=sys.stderr)
        _block(
            rt,
            claim,
            "branch-moved",
            f"Publishing {claim.branch} failed: {exc}.\n{detail[-1500:]}".strip(),
            f"See who pushed {claim.branch}, fetch origin, merge origin/{claim.branch} into the"
            f" worktree's branch or reset it to that, then run flocc release --keep-worktree {key}",
        )
        ledger.meta_set(rt.con, counter, "0")
        return
    fails = int(ledger.meta_get(rt.con, counter) or 0) + 1
    print(f"{key}: publish failed ({fails}/{PUBLISH_TRIES}): {exc} {detail}", file=sys.stderr)
    if fails < PUBLISH_TRIES:
        ledger.meta_set(rt.con, counter, str(fails))
        return
    _block(
        rt,
        claim,
        "publish-failed",
        f"Publishing failed {fails} times in a row: {exc}\n{detail[-1500:]}".strip(),
        f"Fix the push or the gh token, then run flocc release --keep-worktree {key}",
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
    _post_owner_items(rt, claim)
    ledger.update_claim(rt.con, key, state="done")


def _pr_url(rt, claim: Claim) -> str:
    path = state_dir(rt.cfg, claim.issue_key) / "pr-url"
    return path.read_text().strip() if path.exists() else "unknown"


def _over_usage_limit(rt) -> bool:
    usage = latest_usage(rt.cfg.state_root, rt.now())
    return usage is not None and (
        usage.five_hour_pct >= rt.cfg.five_hour_max_pct or usage.weekly_pct >= rt.cfg.weekly_max_pct
    )


def _start_review(rt, claim: Claim, title: str, description: str, url: str) -> None:
    key = claim.issue_key
    d = state_dir(rt.cfg, key)
    start = rt.now()
    if _over_usage_limit(rt):
        (d / "pr-url").write_text(url)
        ledger.add_digest(rt.con, key, f"review skipped, usage over the limit; PR: {url}", rt.now())
        _post_owner_items(rt, claim)
        ledger.update_claim(rt.con, key, state="done")
        return
    try:
        (d / "pr-url").write_text(url)
        (d / "pr.diff").write_text(rt.review_diff(claim) + "\n")
        earlier = ""
        if ledger.meta_get(rt.con, f"review_fix:{key}") and (d / "review.md").exists():
            earlier = (
                "\n## Your earlier review\n\nA fix round followed it. Judge the diff against these"
                f" findings.\n\n{(d / 'review.md').read_text().strip()}\n"
            )
        (d / "review.md").unlink(missing_ok=True)
        (d / "review-input.md").write_text(
            f"# Review {key}: {title}\n\nPR: {url}\n\nThe diff is in {d / 'pr.diff'}.\n\n"
            f"## Issue description, including the acceptance criteria\n\n{description}\n"
            f"{amended_section(read_amended(d))}\n{earlier}"
        )
        launch(rt, claim, "review", started_at=start)
    except Exception as exc:  # noqa: BLE001 - the PR is open; nothing here may block the claim
        print(f"{key}: review not started: {exc}", file=sys.stderr)
        ledger.add_digest(rt.con, key, f"review not started ({exc}); PR: {url}", rt.now())
        _post_owner_items(rt, claim)
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
    fixed = bool(ledger.meta_get(rt.con, f"review_fix:{key}"))
    counter = f"review_post_failures:{key}"
    # One key per round, never cleared before the fresh claim (poller._start): a tick that dies
    # anywhere after the post must not comment the same review twice, nor skip the second one.
    posted = f"review_posted:{key}:{2 if fixed else 1}"
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
    if verdict == "changes requested" and not fixed and _start_review_fix(rt, claim, text):
        _comment_or_digest(rt, claim, f"{line}\n\nA fix round started; a second review follows.")
        return
    owner = _owner_items(rt, claim)
    if owner:
        line += f"\n\n{owner}"
    _comment_or_digest(rt, claim, line)
    ledger.update_claim(rt.con, key, state="done")


def _comment_or_digest(rt, claim: Claim, line: str) -> None:
    try:
        rt.linear.comment(claim.issue_id, line)
    except LinearError:
        ledger.add_digest(rt.con, claim.issue_key, line, rt.now())


def _owner_items(rt, claim: Claim) -> str:
    path = state_dir(rt.cfg, claim.issue_key) / OWNER_ITEMS
    return path.read_text().strip() if path.exists() else ""


def _post_owner_items(rt, claim: Claim) -> None:
    owner = _owner_items(rt, claim)
    if owner:
        _comment_or_digest(rt, claim, owner)


def _start_review_fix(rt, claim: Claim, review: str) -> bool:
    """One fix session on the review's findings; False when it is skipped or cannot start."""
    key = claim.issue_key
    if _over_usage_limit(rt):
        return False
    extra = (
        f"The reviewer asked for changes:\n\n{review.strip()}\n\n"
        "Fix what the review asks that is agent work. For what needs the owner (a manual run, a"
        " product decision, a question for the lead), make no change and list it in your"
        ' `flocc done` summary under a line that starts with "Needs the owner". Then finish as'
        " usual."
    )
    d = state_dir(rt.cfg, key)
    start = rt.now()
    (d / OWNER_ITEMS).unlink(missing_ok=True)
    (d / FIX_CONTEXT).write_text(extra)
    # The review's done.json goes first: left in place, a tick that dies before the launch would
    # read it as the fix finishing and publish with no fix. Dying here instead relaunches the
    # review, which costs a session but loses nothing.
    (d / "done.json").unlink(missing_ok=True)
    # Both before the launch: a tick that dies after the session starts must find the claim in
    # the fix phase, not in review where it would start a reviewer beside the live fix.
    ledger.meta_set(rt.con, f"review_fix:{key}", "1")
    ledger.update_claim(rt.con, key, phase="fix", fix_attempts=0, restarts=0, started_at=start)
    try:
        launch(rt, claim, "fix", extra=extra, started_at=start)
    except Exception as exc:  # noqa: BLE001 - the PR is open; nothing here may block the claim
        print(f"{key}: review fix not started: {exc}", file=sys.stderr)
        ledger.add_digest(rt.con, key, f"review fix not started ({exc})", rt.now())
        ledger.update_claim(
            rt.con,
            key,
            phase=claim.phase,
            fix_attempts=claim.fix_attempts,
            restarts=claim.restarts,
            started_at=claim.started_at,
        )
        return False
    return True


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
                f"Add detail to the acceptance criteria, then run flocc release {key}",
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
    review_fix = bool(ledger.meta_get(rt.con, f"review_fix:{key}"))
    if claim.phase == "fix" and review_fix and (found := _OWNER_HEADING.search(summary)):
        # Kept apart from later check-fix summaries, which would overwrite it.
        (state_dir(rt.cfg, key) / OWNER_ITEMS).write_text(summary[found.start() :].lstrip())
    issue = rt.linear.issue(key)
    try:
        # After a review fix the PR must still be open: a new one would reopen finished work.
        result = (
            rt.publish_fn(claim, issue.title, summary, reuse_only=True)
            if review_fix
            else rt.publish_fn(claim, issue.title, summary)
        )
    except Exception as exc:  # noqa: BLE001 - a push or gh failure is counted, not re-raised
        _publish_failed(rt, claim, exc)
        return
    # Failures count only in a row: the fix phase's publish, or a rerun's, starts from zero.
    ledger.meta_set(rt.con, f"publish_failures:{key}", "0")
    if result.status == "pr_closed":
        line = "The PR was closed or merged during the review fix; nothing more was published."
        owner = _owner_items(rt, claim)
        _comment_or_digest(rt, claim, f"{line}\n\n{owner}" if owner else line)
        ledger.update_claim(rt.con, key, state="done")
    elif result.status == "opened":
        if review_fix and result.existing:
            rt.linear.comment(issue.id, f"Review fix pushed to {result.url}; reviewing again")
        else:
            rt.linear.comment(issue.id, f"PR for review: {result.url}")
            ledger.add_digest(rt.con, key, f"PR ready for review: {result.url}", rt.now())
        if result.untrailed:
            line = f"{key}: commits without the Flocc trailer: {', '.join(result.untrailed)}"
            ledger.add_digest(rt.con, key, line, rt.now())
        _start_review(rt, claim, issue.title, issue.description, result.url)
    elif (
        result.status == "check_failed"
        and claim.fix_attempts > 0
        and _same_failure(rt, claim, result.detail)
    ):
        if result.log:
            (state_dir(rt.cfg, key) / CHECK_OUTPUT).write_text(result.log)
        _block(
            rt,
            claim,
            "check-failed",
            "The check failed the same way twice; the cause is likely outside the code:\n"
            + result.detail,
            f"Fix the branch or the issue, then run flocc release --keep-worktree {key}",
        )
    elif result.status == "check_failed" and claim.fix_attempts < rt.cfg.check_fix_rounds:
        extra = f"The check failed with:\n\n{result.detail}"
        if result.log:
            log = state_dir(rt.cfg, key) / CHECK_OUTPUT
            log.write_text(result.log)
            extra += f"\n\nThis is an excerpt; the full output is in {log}."
        context = state_dir(rt.cfg, key) / FIX_CONTEXT
        if claim.fix_attempts > 0 and context.exists():
            extra = f"An earlier fix round failed with:\n\n{context.read_text()}\n\n{extra}"
        context.write_text(extra)
        (state_dir(rt.cfg, key) / FAILURE_HASH).write_text(_digest(result.detail))
        start = rt.now()
        launch(rt, claim, "fix", extra=extra, started_at=start)
        ledger.update_claim(
            rt.con,
            key,
            phase="fix",
            fix_attempts=claim.fix_attempts + 1,
            restarts=0,
            started_at=start,
        )
    elif result.status == "check_failed":
        if result.log:
            (state_dir(rt.cfg, key) / CHECK_OUTPUT).write_text(result.log)
        rounds = f"{claim.fix_attempts} fix round{'s' * (claim.fix_attempts != 1)}"
        _block(
            rt,
            claim,
            "check-failed",
            f"The check still fails after {rounds}:\n{result.detail}",
            f"Fix the branch or the issue, then run flocc release --keep-worktree {key}",
        )
    else:
        _block(
            rt,
            claim,
            "tests-touched",
            f"The implementer changed the tester's files: {result.detail}",
            "If the test change is wrong, revert it on the branch, then run"
            f" flocc release --keep-worktree {key}. If it is right, run"
            f" flocc release --accept-tests {key}",
        )


def _asked_seconds(d: Path) -> float:
    try:
        return float((d / ASKED).read_text())
    except (OSError, ValueError):
        return 0.0


_RUN_NOISE = re.compile(
    r"\d{4}-\d\d-\d\d[T ][\d:.]+Z?"  # timestamps
    r"|0x[0-9a-fA-F]+"  # addresses
    r"|\b\d+(?:\.\d+)?\s?(?:ms|s|sec|seconds)\b"  # durations
)


def _digest(detail: str) -> str:
    # Only what changes between runs of one failure is dropped; test names and their numbers stay,
    # so two different failing cases do not read as the same failure.
    steady = " ".join(_RUN_NOISE.sub("~", detail).split())
    return hashlib.sha256(steady.encode()).hexdigest()


def _same_failure(rt, claim: Claim, detail: str) -> bool:
    path = state_dir(rt.cfg, claim.issue_key) / FAILURE_HASH
    return path.exists() and path.read_text() == _digest(detail)


def _check_question(rt, claim: Claim, d: Path, name: str) -> None:
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
    if claim.phase in ("impl", "fix"):
        where = f"in the Claude app (Remote Control session {name})"
    else:
        where = f"in its pane on the host (tmux -L flocc attach -t {name})"
    esc = scoped(
        Escalation(
            2,
            "question",
            f"Unanswered question in session {name}: {text}",
            f"Answer it {where};"
            " a reply on this issue is delivered after the question is answered.",
            waiting=True,
        ),
        claim.issue_key,
    )
    rt.notifier.send(esc, rt.linear.issue(claim.issue_key), None)
    (d / "asking-escalated").touch()


def _check_idle(rt, claim: Claim, d: Path, name: str) -> None:
    idle = d / "idle"
    if not idle.exists() or (d / "asking").exists():
        return
    try:
        raw = json.loads(idle.read_text())
        at, message = float(raw["at"]), str(raw.get("message", ""))
        waiting = raw.get("waiting_on_owner") is True
    except (OSError, ValueError, KeyError, TypeError):
        return
    escalated = d / "idle-escalated"
    if waiting or (escalated.exists() and escalated.read_text() == str(at)):
        return
    # Hooks remove `idle` when the session acts again, so one that is left was written by the
    # last Stop. status.json is no wake signal: the status line also rewrites it on cache expiry
    # and rate-limit resets, and goes quiet while the session waits on background agents.
    if rt.now() - at < rt.cfg.idle_escalate_s:
        return
    if running_agents(d, run_id(claim.phase, claim.started_at), claim.phase):
        return
    minutes = int((rt.now() - at) / 60)
    esc = scoped(
        Escalation(
            2,
            "idle",
            f"{name} has been idle for {minutes} min without finishing:"
            f" {message or 'no last message'}",
            "Reply on this issue to steer it, or release it with"
            f" flocc release --keep-worktree {claim.issue_key}",
            waiting=True,
        ),
        claim.issue_key,
    )
    rt.notifier.send(esc, rt.linear.issue(claim.issue_key), None)
    escalated.write_text(str(at))


def _check_stall(rt, claim: Claim, d: Path, name: str) -> None:
    # A session mid-turn redraws its pane every second (spinner, elapsed time, streamed output),
    # so a pane that stays the same across ticks is waiting on a dialog no hook reports. Ended
    # turns and open questions have their own checks. flocc's status line is left out: it
    # rewrites on cache expiry and rate-limit resets while the session sits still. Background
    # agents' tool calls clear `idle`, and a turn cut off by a usage limit writes no `idle` and
    # sits still until the reset, so both are left alone too.
    if (d / "idle").exists() or (d / "asking").exists():
        return
    if running_agents(d, run_id(claim.phase, claim.started_at), claim.phase):
        return
    u = latest_usage(rt.cfg.state_root, rt.now())
    if u is not None and max(u.five_hour_pct, u.weekly_pct) >= 100:
        return
    own = f"{claim.issue_key} · ctx"
    rows = [
        r
        for r in rt.sessions.capture(name, STALL_LINES).splitlines()
        if r.strip() and not r.strip().startswith(own)
    ]
    if not rows:
        return
    digest = hashlib.sha256("\n".join(rows).encode()).hexdigest()
    pane = d / PANE
    try:
        seen = json.loads(pane.read_text())
        same, since = seen["hash"] == digest, float(seen["since"])
    except (OSError, ValueError, KeyError, TypeError):
        same, since = False, rt.now()
    if not same:
        pane.write_text(json.dumps({"hash": digest, "since": rt.now()}))
        return
    escalated = d / STALL_ESCALATED
    if rt.now() - since < rt.cfg.stall_escalate_s or (
        escalated.exists() and escalated.read_text() == str(since)
    ):
        return
    tail = "\n".join(
        "".join(c if c.isprintable() else " " for c in r).rstrip() for r in rows[-STALL_TAIL:]
    )
    minutes = int((rt.now() - since) / 60)
    key = claim.issue_key
    esc = scoped(
        Escalation(
            2,
            "stalled",
            f"{name} has shown the same screen for {minutes} min in the middle of a turn,"
            f" most likely a dialog waiting for an answer:\n```\n{tail}\n```",
            f"See it with flocc peek {key} and answer with flocc press {key} <keys>"
            f" (e.g. flocc press {key} Down Enter), or release it with"
            f" flocc release --keep-worktree {key}",
            waiting=True,
        ),
        key,
    )
    rt.notifier.send(esc, rt.linear.issue(key), None)
    escalated.write_text(str(since))


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
                f"Check the worktree, then run flocc release --keep-worktree {key}",
            )
            return
        _relaunch(rt, claim)
        ledger.update_claim(rt.con, key, restarts=claim.restarts + 1)
        return
    elapsed = rt.now() - claim.started_at
    if claim.phase == "review":
        limit, held = rt.cfg.review_wall_clock_s, False
    else:
        limit = claim.wall_clock_s + _asked_seconds(d)
        # A session waiting on an answer burns no tokens. The owner is paged QUESTION_WAIT_S after
        # the question and gets as long again to answer; measured from the limit instead, a
        # question asked near it would be paged and killed in the same tick. A torn marker holds
        # nothing, or every tick would read it as a fresh question.
        held = False
        if (d / "asking").exists():
            try:
                asked_at = float(json.loads((d / "asking").read_text())["at"])
            except (OSError, ValueError, KeyError, TypeError):
                asked_at = float("-inf")
            held = rt.now() - asked_at <= 2 * QUESTION_WAIT_S
    if elapsed > limit and not held:
        if claim.phase == "review":
            _record_usage(rt, key, claim.phase, "end")
            _review_skipped(rt, claim, f"stopped at the wall-clock limit ({name})")
            return
        rt.sessions.kill(name)
        line = f"stopped at the wall-clock limit ({name}); rerun with flocc release {key}"
        ledger.add_digest(rt.con, key, line, rt.now())
        _record_usage(rt, key, claim.phase, "end")
        ledger.update_claim(rt.con, key, state="timed_out")
        return
    pct = context_pct(read_status(d) or {})
    requested = d / "handoff-requested"
    # One compaction first: the handoff applies only once the session has compacted. Not while a
    # question is open: the keystrokes could select an option in its dialog.
    if (
        not (d / "asking").exists()
        and pct is not None
        and pct >= rt.cfg.context_restart_pct
        and (d / "compacted").exists()
        and not requested.exists()
    ):
        rt.sessions.send(name, HANDOFF_ASK.format(pct=pct))
        requested.touch()
    _check_question(rt, claim, d, name)
    _check_idle(rt, claim, d, name)
    _check_stall(rt, claim, d, name)
