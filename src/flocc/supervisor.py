import hashlib
import json
import re
import subprocess
import sys
from collections import Counter
from dataclasses import replace
from pathlib import Path

from flocc import canary, ledger
from flocc.agentblock import settings_from_json
from flocc.context import phase_prompt, state_dir
from flocc.idle import BACKGROUND, clear_idle
from flocc.ledger import Claim
from flocc.linear import LinearError
from flocc.notice import Notice
from flocc.publisher import (
    BASE_CHECKOUT,
    BranchMoved,
    PublishResult,
    amended_section,
    pr_title,
    read_amended,
    review_comment,
)
from flocc.sessions import (
    allowed_plugins,
    claude_argv,
    clean_env,
    installed_plugins,
    session_name,
    trust_folders,
)
from flocc.usage import latest_usage
from flocc.worktree import origin_base

_MARKERS = ("done.json",)
FIX_CONTEXT = "check-failure.md"
FAILURE_HASH = "check-failure.sha256"
BASE_HELD = "base_held"
BASE_RELEASED = "base_released"
REBASE = "rebase"
REBASE_TRIED = "rebase_tried"
CHECK_OUTPUT = "check-output.log"
OWNER_ITEMS = "owner-items.md"
LAST_SUMMARY = "last-summary.md"
_RESTART = (
    "Your previous session for this step ended before flocc done. The branch holds what it"
    " committed; check git log and git status, then carry on from there."
)
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
            "FLOCC_STATE": str(d),
            "FLOCC_CONFIG": str(rt.config_path),
            "FLOCC_PHASE": phase,
            "CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH": "1",
            "CLAUDE_CODE_DISABLE_EXPLORE_PLAN_AGENTS": "1",
        }
    )
    settings = settings_from_json(claim.settings_json)
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
        disabled_plugins=_disabled_plugins(rt),
        remote_control=phase in ("impl", "fix"),
    )
    # After everything above that can raise: the previous session's markers would be taken for
    # the new one's.
    markers = {n: (d / n).read_bytes() for n in _MARKERS if (d / n).exists()}
    for leftover in (*_MARKERS, "status.json", BACKGROUND):
        (d / leftover).unlink(missing_ok=True)
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
    key = claim.issue_key
    rt.notifier.send(Notice(f"{key}:{cause}", summary, action, issue_key=key), rt.linear.issue(key))
    ledger.update_claim(rt.con, key, state="blocked")


def _end_done(rt, claim: Claim, **values) -> None:
    ledger.update_claim(rt.con, claim.issue_key, state="done", **values)


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
    launch(rt, claim, claim.phase, extra=f"{_RESTART}\n\n{extra}".strip())


def _review_skipped(rt, claim: Claim, why: str) -> None:
    # A review is advice on an open PR; losing it must not block the claim.
    key = claim.issue_key
    url = _pr_url(rt, claim)
    rt.sessions.kill(session_name(key, "review"))
    ledger.add_digest(rt.con, key, f"review skipped, {why}; PR: {url}", rt.now())
    _post_owner_items(rt, claim)
    _end_done(rt, claim)


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
        _end_done(rt, claim)
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
        launch(rt, claim, "review")
    except Exception as exc:  # noqa: BLE001 - the PR is open; nothing here may block the claim
        print(f"{key}: review not started: {exc}", file=sys.stderr)
        ledger.add_digest(rt.con, key, f"review not started ({exc}); PR: {url}", rt.now())
        _post_owner_items(rt, claim)
        _end_done(rt, claim)
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
    _end_done(rt, claim)


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
        launch(rt, claim, "fix", extra=extra)
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


def start_rebase(rt, claim: Claim, base_sha: str) -> None:
    """One fix session that merges the base into a finished claim's conflicting PR branch."""
    key = claim.issue_key
    base = rt.cfg.repos[claim.repo].base_branch
    extra = (
        f"The PR for this issue no longer merges cleanly into {base}. Run `git fetch origin`,"
        f" merge `origin/{base}` into this branch and resolve the conflicts without changing"
        " behaviour: keep what the branch does and take the base's side of anything unrelated."
        " Where a resolution changes a file the tester committed, record it with"
        " `flocc tests-amended`. Run the check, commit the merge, then finish as usual."
    )
    d = state_dir(rt.cfg, key)
    start = rt.now()
    (d / FIX_CONTEXT).write_text(extra)
    # A stale done.json would be read as this session finishing.
    (d / "done.json").unlink(missing_ok=True)
    # Before the launch, like the review fix: a tick that dies after the session starts must find
    # the claim running in the fix phase, with its publish reusing the open PR.
    ledger.meta_set(rt.con, f"{REBASE}:{key}", "1")
    ledger.meta_set(rt.con, f"{REBASE_TRIED}:{key}", base_sha)
    ledger.update_claim(
        rt.con, key, state="running", phase="fix", fix_attempts=0, restarts=0, started_at=start
    )
    try:
        launch(rt, claim, "fix", extra=extra)
    except Exception as exc:  # noqa: BLE001 - the PR is open; nothing here may block the claim
        print(f"{key}: rebase not started: {exc}", file=sys.stderr)
        cause = f"{key}:rebase-start"
        if not ledger.alert_seen(rt.con, cause, ledger.DEDUPE_S, rt.now()):
            ledger.add_digest(rt.con, key, f"rebase not started ({exc})", rt.now())
            ledger.record_alert(rt.con, cause, 3, key, rt.now())
        ledger.meta_set(rt.con, f"{REBASE}:{key}", "")
        ledger.meta_set(rt.con, f"{REBASE_TRIED}:{key}", "")
        ledger.update_claim(
            rt.con,
            key,
            state=claim.state,
            phase=claim.phase,
            fix_attempts=claim.fix_attempts,
            restarts=claim.restarts,
            started_at=claim.started_at,
        )


def block_unrebased(rt, claim: Claim, base_sha: str) -> None:
    base = rt.cfg.repos[claim.repo].base_branch
    line = f"the PR still conflicts with {base} at {base_sha[:9]} after a rebase attempt"
    ledger.add_digest(rt.con, claim.issue_key, line, rt.now())
    _block(
        rt,
        claim,
        "rebase-conflict",
        f"The rebase session ran, and {line}.",
        f"Resolve the conflicts on the branch by hand, push it, then run flocc release"
        f" --keep-worktree {claim.issue_key}",
    )


def _finish_phase(rt, claim: Claim, summary: str) -> None:
    key = claim.issue_key
    # Before the usage record, which would add a row on every tick of the wait.
    if not _base_hold_lifted(rt, claim):
        return
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
        launch(rt, claim, "impl")
        ledger.update_claim(
            rt.con, key, tester_sha=head, phase="impl", restarts=0, started_at=start
        )
        return
    review_fix = bool(ledger.meta_get(rt.con, f"review_fix:{key}"))
    rebasing = bool(ledger.meta_get(rt.con, f"{REBASE}:{key}"))
    if (
        claim.phase == "fix"
        and review_fix
        and not rebasing
        and (found := _OWNER_HEADING.search(summary))
    ):
        # Kept apart from later check-fix summaries, which would overwrite it.
        (state_dir(rt.cfg, key) / OWNER_ITEMS).write_text(summary[found.start() :].lstrip())
    issue = rt.linear.issue(key)
    title = pr_title(key, issue.title, issue.labels)
    try:
        # After a review fix or a rebase the PR must still be open: a new one would reopen
        # finished work.
        result = (
            rt.publish_fn(claim, title, summary, reuse_only=True)
            if review_fix or rebasing
            else rt.publish_fn(claim, title, summary)
        )
    except Exception as exc:  # noqa: BLE001 - a push or gh failure is counted, not re-raised
        _publish_failed(rt, claim, exc)
        return
    # Failures count only in a row: the fix phase's publish, or a rerun's, starts from zero.
    ledger.meta_set(rt.con, f"publish_failures:{key}", "0")
    if result.status == "pr_closed":
        what = "rebase" if rebasing else "review fix"
        line = f"The PR was closed or merged during the {what}; nothing more was published."
        owner = _owner_items(rt, claim)
        _comment_or_digest(rt, claim, f"{line}\n\n{owner}" if owner else line)
        ledger.meta_set(rt.con, f"{REBASE}:{key}", "")
        _end_done(rt, claim)
    elif result.status == "opened":
        if rebasing:
            base = rt.cfg.repos[claim.repo].base_branch
            rt.linear.comment(
                issue.id,
                f"Merged {base} into the branch and pushed to {result.url}. The conflict"
                " resolution was not reviewed again.",
            )
        elif review_fix and result.existing:
            rt.linear.comment(issue.id, f"Review fix pushed to {result.url}; reviewing again")
        else:
            rt.linear.comment(issue.id, f"PR for review: {result.url}")
            ledger.add_digest(rt.con, key, f"PR ready for review: {result.url}", rt.now())
        if result.label_error:
            line = f"{key}: label not added to {result.url}: {result.label_error}"
            ledger.add_digest(rt.con, key, line, rt.now())
        if result.untrailed:
            line = f"{key}: commits without the Flocc trailer: {', '.join(result.untrailed)}"
            ledger.add_digest(rt.con, key, line, rt.now())
        if rebasing:
            # A merge adds no behaviour for a reviewer to judge, and the earlier review stands.
            ledger.meta_set(rt.con, f"{REBASE}:{key}", "")
            _end_done(rt, claim, phase="review")
        elif "reviewer" in claim.sequence:
            _start_review(rt, claim, issue.title, issue.description, result.url)
        else:
            (state_dir(rt.cfg, key) / "pr-url").write_text(result.url)
            _end_done(rt, claim)
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
        # Only before the first round: a later round's failure is the branch's own.
        # A dirty tree has no log, and its detail would never match a base failure.
        if claim.fix_attempts == 0 and result.log and _base_fails_too(rt, claim, result):
            return
        extra = f"The check failed with:\n\n{result.detail}"
        if result.log:
            log = state_dir(rt.cfg, key) / CHECK_OUTPUT
            log.write_text(result.log)
            extra += f"\n\nThis is an excerpt; the full output is in {log}."
        if claim.fix_attempts == 0 and (note := _base_moved_note(rt, claim, result.detail)):
            extra = f"{note}\n\n{extra}"
        context = state_dir(rt.cfg, key) / FIX_CONTEXT
        if context.exists() and claim.fix_attempts > 0:
            extra = f"An earlier fix round failed with:\n\n{context.read_text()}\n\n{extra}"
        elif context.exists() and rebasing:
            extra = f"This session was started with:\n\n{context.read_text()}\n\n{extra}"
        context.write_text(extra)
        (state_dir(rt.cfg, key) / FAILURE_HASH).write_text(_digest(result.detail))
        start = rt.now()
        launch(rt, claim, "fix", extra=extra)
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


# Test-shaped IDs only: pytest node IDs and Go tests. A linter's `ERROR <message>` line has no ID,
# and its first word would make two different errors of one kind look alike.
_FAILED_TEST = re.compile(r"^(?:(?:FAILED|ERROR) (\S+::\S+)|--- FAIL: (\S+))", re.MULTILINE)
_SUMMARY_COUNT = re.compile(r"\b\d+ (passed|failed|skipped|deselected|xfailed|xpassed|warnings?)\b")


def _failing_tests(text: str) -> Counter[str]:
    return Counter(a or b for a, b in _FAILED_TEST.findall(text))


def _same_as_base(result: PublishResult, claim: Claim, failure: PublishResult) -> bool:
    # The branch carries the tester's extra tests and runs in its own checkout, so the counts in a
    # summary line and the checkout path differ even when the failure is the base's.
    mine, theirs = result.log or result.detail, failure.log or failure.detail
    if (ids := _failing_tests(mine)) and (base_ids := _failing_tests(theirs)):
        return ids == base_ids

    def steady(text: str) -> str:
        return _digest(_SUMMARY_COUNT.sub(r"N \1", text.replace(claim.worktree, BASE_CHECKOUT)))

    return steady(mine) == steady(theirs)


def _base_fails_too(rt, claim: Claim, result: PublishResult) -> bool:
    """Holds the claim and its repo when the base branch fails the check the same way."""
    if rt.base_check is None:
        return False
    key = claim.issue_key
    try:
        sha, failure = rt.base_check(claim.repo)
    except (subprocess.SubprocessError, OSError) as exc:
        print(f"{key}: base check did not run: {exc}", file=sys.stderr)
        return False
    if failure is None or not _same_as_base(result, claim, failure):
        return False
    repo = rt.cfg.repos[claim.repo]
    canary.mark(rt.con, claim.repo, sha, rt.now(), failure.log or failure.detail, check=repo.check)
    # done.json stays: once the hold lifts, the next tick publishes again.
    held = {"sha": sha, "digest": _digest(result.detail)}
    ledger.meta_set(rt.con, f"{BASE_HELD}:{key}", json.dumps(held))
    line = (
        f"`{repo.check}` fails the same way on {repo.base_branch} at {sha[:9]}, so no fix round"
        f" started; {claim.repo} is held until {repo.base_branch} moves or flocc canary"
        f" {claim.repo} passes, then {key} publishes again"
    )
    ledger.add_digest(rt.con, key, line, rt.now())
    try:
        rt.linear.comment(claim.issue_id, line)
    except LinearError as exc:
        print(f"{key}: base-broken comment not posted: {exc}", file=sys.stderr)
    return True


def _base_hold_lifted(rt, claim: Claim) -> bool:
    key = f"{BASE_HELD}:{claim.issue_key}"
    held = ledger.meta_get(rt.con, key)
    if not held:
        return True
    repo = rt.cfg.repos[claim.repo]
    if canary.hold_reason(rt.con, claim.repo, repo.base_branch, origin_base(repo), rt.now()):
        return False
    # Read by the fix round if the branch, still on the old base, fails the same way again.
    ledger.meta_set(rt.con, f"{BASE_RELEASED}:{claim.issue_key}", held)
    ledger.meta_set(rt.con, key, "")
    return True


def _base_moved_note(rt, claim: Claim, detail: str) -> str:
    key = f"{BASE_RELEASED}:{claim.issue_key}"
    try:
        released = json.loads(ledger.meta_get(rt.con, key) or "")
    except ValueError:
        return ""
    ledger.meta_set(rt.con, key, "")
    if not isinstance(released, dict) or released.get("digest") != _digest(detail):
        return ""
    repo = rt.cfg.repos[claim.repo]
    was = str(released.get("sha"))[:9]
    now = (origin_base(repo) or "a newer commit")[:9]
    moved = (
        f"{repo.base_branch} now passes at {now}"
        if now == was
        else f"{repo.base_branch} has since moved to {now}"
    )
    return (
        f"This failure was on {repo.base_branch} at {was}, and {moved}. Run `git fetch origin`, merge"
        f" `origin/{repo.base_branch}` into this branch, then run the check again before changing"
        " anything else."
    )


def _idle_message(d: Path) -> str | None:
    """The last words of a session that stopped twice without flocc done; None when it did not."""
    try:
        raw = json.loads((d / "idle").read_text())
    except FileNotFoundError:
        return None
    except (OSError, ValueError):
        return ""
    return str(raw.get("message", "")) if isinstance(raw, dict) else ""


def _stopped(rt, claim: Claim, name: str, message: str) -> None:
    if claim.phase == "review":
        _review_skipped(rt, claim, f"the reviewer stopped without flocc done ({name})")
        return
    _block(
        rt,
        claim,
        "idle",
        f"{name} stopped twice without running flocc done. Its last words:\n{message or '(none)'}"[
            :3000
        ],
        "Reply on this issue with what it needs, then move the issue back to Todo; the rerun"
        " continues on the kept branch",
    )


def _timed_out(rt, claim: Claim, name: str) -> None:
    key = claim.issue_key
    rt.sessions.kill(name)
    line = f"stopped at the wall-clock limit ({name}); rerun with flocc release {key}"
    ledger.add_digest(rt.con, key, line, rt.now())
    _record_usage(rt, key, claim.phase, "end")
    ledger.update_claim(rt.con, key, state="timed_out")


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
        if claim.phase != "review":
            (d / LAST_SUMMARY).write_text(summary)
        _finish_phase(rt, claim, summary)
        return
    if (message := _idle_message(d)) is not None:
        _stopped(rt, claim, name, message)
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
    limit = rt.cfg.review_wall_clock_s if claim.phase == "review" else claim.wall_clock_s
    if rt.now() - claim.started_at > limit:
        if claim.phase == "review":
            _record_usage(rt, key, claim.phase, "end")
            _review_skipped(rt, claim, f"stopped at the wall-clock limit ({name})")
            return
        _timed_out(rt, claim, name)
