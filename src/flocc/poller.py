import shutil
import subprocess
import sys
import time
from datetime import UTC, datetime
from pathlib import Path

from flocc import canary, ledger
from flocc.admission import admit
from flocc.agentblock import (
    BlockError,
    has_acceptance_criteria,
    parse_settings,
    settings_to_json,
)
from flocc.context import state_dir, write_context
from flocc.ledger import Claim
from flocc.linear import BOT_PREFIX, Issue, LinearError
from flocc.notice import Notice
from flocc.sequence import SequenceError, parse_sequence
from flocc.sessions import session_name
from flocc.supervisor import LAST_SUMMARY, launch
from flocc.usage import latest_usage
from flocc.worktree import (
    DirtyWorktree,
    agent_ref,
    contains,
    fetch_agent_branch,
    git,
    origin_base,
    recover_tests,
)

COMMENT_ONCE_S = 365 * 24 * 3600


def repo_for(issue: Issue, cfg) -> str | None:
    for name in (*issue.labels, issue.project):
        if name in cfg.repos:
            return name
    return None


def _comment_once(rt, issue: Issue, cause: str, text: str) -> None:
    if ledger.alert_seen(rt.con, cause, COMMENT_ONCE_S, rt.now()):
        return
    rt.linear.comment(issue.id, text)
    ledger.record_alert(rt.con, cause, 3, issue.key, rt.now())


def remove_issue(rt, claim: Claim, *, keep_dirty: bool = False) -> bool:
    """Removes the worktree and both state directories; the branch stays on GitHub. False when
    nothing was removed: a dirty worktree kept by keep_dirty, or flocc-agent did not answer."""
    try:
        out = rt.agent.remove(claim.issue_key, keep_dirty=keep_dirty)
    except (subprocess.SubprocessError, OSError) as exc:
        print(f"{claim.issue_key}: worktree and state not removed: {exc}", file=sys.stderr)
        return False
    if not out.get("removed"):
        return False
    shutil.rmtree(state_dir(rt.cfg, claim.issue_key), ignore_errors=True)
    return True


def _bad_sequence(rt, issue: Issue, exc: SequenceError) -> None:
    # The only place an unusable Run: line reaches the owner.
    _comment_once(rt, issue, f"{issue.key}:bad-sequence:{exc}", f"Skipping: {exc}.")


def release_claim(rt, claim: Claim, *, keep_worktree: bool = False, to_todo: bool = True) -> None:
    rt.sessions.kill(session_name(claim.issue_key, claim.phase))
    # Each step below may already be done or impossible (a deleted issue, a worktree removed
    # by an earlier release); the claim is cleared regardless, or it blocks every tick.
    if not keep_worktree:
        remove_issue(rt, claim)
    # Back to Todo so the next poll picks it up again; the rerun continues on the kept branch.
    if to_todo:
        try:
            rt.linear.unstart(rt.linear.issue(claim.issue_key))
        except LinearError as exc:
            print(f"release: {claim.issue_key} not moved back to Todo: {exc}", file=sys.stderr)
    ledger.delete_claim(rt.con, claim.issue_key)
    # Alert dedupe is keyed to the old claim; a rerun that fails again for the same cause must
    # still be told.
    ledger.clear_issue_alerts(rt.con, claim.issue_key)


def still_wanted(rt, key: str) -> bool:
    """False for an issue since parked, unlabelled or closed."""
    return (
        rt.linear.state_types([key])[key] == "started"
        and rt.cfg.trigger_label in rt.linear.issue(key).labels
    )


def restart_timed_out(rt) -> None:
    """Sends a run that hit the wall clock after committing back to Todo, once per issue."""
    for claim in ledger.finished_claims(rt.con):
        key = claim.issue_key
        once = f"timeout_restart:{key}"
        wt = Path(claim.worktree)
        # A tester's commits were never remembered, so a rerun from "test" would find none.
        if claim.state != "timed_out" or claim.phase not in ("impl", "fix"):
            continue
        if claim.base_sha is None or not wt.exists():
            continue
        if ledger.meta_get(rt.con, once):
            continue
        try:
            repo_path = rt.cfg.repos[claim.repo].path
            fetch_agent_branch(repo_path, wt, claim.branch, key)
            ref = agent_ref(key)
            stamps = git("log", "--format=%ct", ref, "--not", claim.base_sha, cwd=repo_path)
            if not any(float(s) > claim.started_at for s in stamps.split()):
                continue
            # An issue since parked, unlabelled or closed is no longer wanted; releasing it would
            # strand its worktree with nothing to pick it up again.
            if not still_wanted(rt, key):
                continue
        except (subprocess.CalledProcessError, OSError, LinearError, KeyError, ValueError) as exc:
            print(f"restart: {key} not checked: {exc}", file=sys.stderr)
            continue
        # Recorded first: a release that fails halfway must not restart the issue every tick.
        ledger.meta_set(rt.con, once, "1")
        text = "stopped at the wall-clock limit with new commits; restarting once"
        try:
            rt.linear.comment(claim.issue_id, text)
        except LinearError as exc:
            print(f"restart: {key} not commented: {exc}", file=sys.stderr)
        release_claim(rt, claim, keep_worktree=True, to_todo=True)


def reap_worktrees(rt) -> None:
    """Removes the worktree, the state and the claim of finished runs whose issue is closed."""
    for claim in ledger.finished_claims(rt.con):
        try:
            state = rt.linear.state_types([claim.issue_key])[claim.issue_key]
        except LinearError as exc:
            # A deleted issue can never be rerun, and asking again every tick would not change that.
            # Only Linear's entity error: a wrong-workspace key fails every issue with other text.
            if "entity not found" not in str(exc).lower():
                print(f"reap: {claim.issue_key} state not read: {exc}", file=sys.stderr)
                continue
            state = "canceled"
        # Anything else stays: the owner may move the issue to Todo to rerun, which reuses it.
        if state not in ("completed", "canceled"):
            continue
        # A run stopped by the wall clock may hold work nobody committed, new files included;
        # a finished one published, so what is left is disposable.
        if not remove_issue(rt, claim, keep_dirty=claim.state == "timed_out"):
            cause = f"{claim.issue_key}:worktree-kept"
            if not ledger.alert_seen(rt.con, cause, COMMENT_ONCE_S, rt.now()):
                print(f"reap: {claim.worktree} kept: uncommitted changes", file=sys.stderr)
                ledger.record_alert(rt.con, cause, 3, claim.issue_key, rt.now())
            continue
        ledger.delete_claim(rt.con, claim.issue_key)
        ledger.clear_issue_alerts(rt.con, claim.issue_key)


def main_hold(rt, repo: str, bases: dict[str, str | None]) -> str | None:
    # Fetches only for a marked repo, once per poll, so an unmarked one costs nothing per tick.
    if canary.get_mark(rt.con, repo) is None:
        return None
    cfg_repo = rt.cfg.repos[repo]
    if repo not in bases:
        bases[repo] = origin_base(cfg_repo)
    return canary.hold_reason(rt.con, repo, cfg_repo.base_branch, bases[repo], rt.now())


def poll(rt) -> None:
    cfg = rt.cfg
    bases: dict[str, str | None] = {}
    for issue in rt.linear.ready_issues(cfg.team_key, cfg.trigger_label):
        claim = ledger.get_claim(rt.con, issue.key)
        rerun = claim is not None
        if claim is not None:
            if claim.state == "running":
                continue
            # Only `release` moves a claimed issue back to Todo, so an issue that is in Todo
            # with a finished or blocked claim (its session is already killed) was moved by the
            # owner, who is asking for a rerun. The worktree is kept: block messages send the
            # owner to fix things there.
            release_claim(rt, claim, keep_worktree=True, to_todo=False)
        repo = repo_for(issue, cfg)
        if repo is None:
            if cfg.skip_unmapped:
                if rerun:
                    # The release above kept the worktree; nothing on this host will clean it up.
                    print(
                        f"{issue.key}: claim released, no mapped repo matches any more;"
                        f" worktree {claim.worktree} kept",
                        file=sys.stderr,
                    )
                continue
            _comment_once(
                rt,
                issue,
                f"{issue.key}:no-repo",
                f"No configured repo matches this issue's labels or project "
                f"({', '.join(cfg.repos)}).",
            )
            continue
        if not has_acceptance_criteria(issue.description):
            _comment_once(
                rt,
                issue,
                f"{issue.key}:no-criteria",
                "Skipping until the description has an 'Acceptance criteria', 'Done when' or"
                " 'Definition of done' heading followed by a list.",
            )
            continue
        try:
            settings = parse_settings(issue.description)
            for server in settings.mcp:
                if server not in cfg.mcp_servers:
                    raise BlockError(f"agent block: mcp server {server!r} is not configured")
        except BlockError as exc:
            _comment_once(rt, issue, f"{issue.key}:bad-block:{exc}", f"Skipping: {exc}.")
            continue
        try:
            sequence = parse_sequence(issue.description)
        except SequenceError as exc:
            _bad_sequence(rt, issue, exc)
            continue
        try:
            deps = rt.linear.state_types(settings.depends_on) if settings.depends_on else {}
        except LinearError as exc:
            # A mistyped or deleted key fails the lookup; hold this issue, not the whole tick.
            _comment_once(
                rt,
                issue,
                f"{issue.key}:bad-deps",
                f"Skipping: could not read depends_on {', '.join(settings.depends_on)}: {exc}.",
            )
            continue
        for cause in ("no-repo", "no-criteria", "bad-deps"):
            ledger.clear_alert(rt.con, f"{issue.key}:{cause}")
        hold = main_hold(rt, repo, bases)
        if hold:
            print(f"{issue.key}: held, {hold}", file=sys.stderr)
            continue
        live = ledger.live_claims(rt.con)
        usage = latest_usage(cfg.agent_state_root, rt.now())
        free = rt.free_disk_gb(cfg.worktree_root)
        decision = admit(
            issue.key,
            repo,
            settings,
            live,
            usage,
            deps,
            cfg,
            rt.now(),
            free,
            ledger.held_keys(rt.con),
        )
        if not decision.ok:
            print(f"{issue.key}: held, {decision.reason}", file=sys.stderr)
            continue
        _start(rt, issue, repo, settings, sequence)
        if rerun and ledger.get_claim(rt.con, issue.key):
            try:
                rt.linear.comment(issue.id, "moved back to Todo; restarting from a fresh claim.")
            except LinearError as exc:
                print(f"{issue.key}: rerun comment not posted: {exc}", file=sys.stderr)


def _start(rt, issue: Issue, repo: str, settings, sequence: tuple[str, ...]) -> None:
    cfg = rt.cfg
    cause = f"{issue.key}:prepare"
    # A kept worktree may hold a previous run's uncommitted work, so its failure is the issue's.
    reused = (cfg.worktree_root.resolve() / issue.key).exists()
    try:
        path, branch, base = rt.agent.prepare(repo, issue.key)
        repo_path = cfg.repos[repo].path
        fetch_agent_branch(repo_path, path, branch, issue.key)
    except (subprocess.SubprocessError, OSError) as exc:  # nothing is claimed yet
        stderr = getattr(exc, "stderr", None) or ""
        if isinstance(stderr, bytes):
            stderr = stderr.decode(errors="replace")
        detail = f"\n\n{stderr.strip()[-1500:]}" if stderr.strip() else ""
        # A fresh worktree still at the base that ./setup itself fails on is main's or the host's
        # failure, and every issue would repeat it. A git failure (a fetch on a network blip)
        # says nothing about main. Before the dedupe below: a mark must land even when this
        # issue's own alert is recent.
        # A timeout is a slow host as often as a broken main; it stays this issue's problem.
        setup_failed = isinstance(exc, DirtyWorktree) or (
            isinstance(exc, subprocess.CalledProcessError) and exc.cmd == ["./setup"]
        )
        base = getattr(exc, "at_base", None) if setup_failed and not reused else None
        if base is not None:
            base_branch = cfg.repos[repo].base_branch
            # Marked only once the notice is out: a mark holds every issue before _start, so one
            # set ahead of a failed send would hold them with nobody told.
            rt.notifier.send(
                Notice(
                    f"{repo}:main-broken",
                    f"./setup fails on a clean checkout of {repo}'s {base_branch} at {base[:9]},"
                    f" so its issues are held: {exc}{detail}",
                    f"Fix ./setup on {base_branch} or on the host. Issues resume after a new"
                    f" commit on {base_branch}, after 24 hours, or once flocc canary {repo} passes",
                    issue_key=issue.key,
                ),
                issue,
            )
            canary.mark(rt.con, repo, base, rt.now(), stderr.strip() or str(exc))
            return
        # A broken ./setup fails on every poll, and every send comments again.
        if ledger.alert_seen(rt.con, cause, ledger.DEDUPE_S, rt.now()):
            return
        rt.notifier.send(
            Notice(
                cause,
                f"Preparing the worktree failed: {exc}{detail}",
                "Fix the repo or ./setup",
                issue_key=issue.key,
            ),
            issue,
        )
        ledger.record_alert(rt.con, cause, 2, issue.key, rt.now())
        return
    # Cleared here, not with the other causes in poll(): those run before every prepare attempt.
    ledger.clear_alert(rt.con, cause)
    d = state_dir(cfg, issue.key)
    # A fresh claim gets its own review fix round.
    ledger.meta_set(rt.con, f"review_fix:{issue.key}", "")
    for kept in ("rebase", "rebase_tried", "base_held", "base_released"):
        ledger.meta_set(rt.con, f"{kept}:{issue.key}", "")
    for round_ in (1, 2):
        ledger.meta_set(rt.con, f"review_posted:{issue.key}:{round_}", "")
    (d / "owner-items.md").unlink(missing_ok=True)
    # A previous claim's scratchpads, idle marker and last summary belong to that run.
    for stale in ("scratchpads", "idle", LAST_SUMMARY):
        (d / stale).unlink(missing_ok=True)
    # A rerun on a kept branch that already holds the tester's commit resumes at the
    # executor: a fresh tester would block on adding nothing, and the guard would lose the
    # first run's tests.
    phase, tester_sha = ("test" if "tester" in sequence else "impl"), None
    base_branch = cfg.repos[repo].base_branch
    remembered = ledger.remembered_tests(rt.con, issue.key)
    # Tests already merged into the base belong to shipped work, and a reopened issue is new
    # work. After a merge commit, a branch cut fresh from the base contains them too.
    ref = agent_ref(issue.key)
    if remembered and contains(repo_path, remembered[1], f"origin/{base_branch}"):
        ledger.forget_tests(rt.con, issue.key)
        remembered = None
    # A branch another host pushed holds its tester's commits, but this ledger has no record of
    # them, and the publisher's guard and --accept-tests compare against that record. A rebase
    # rewrites the remembered commit the same way, so a record the branch lacks is recovered too.
    if (remembered is None or not contains(repo_path, remembered[1], ref)) and (
        recovered := recover_tests(repo_path, base_branch, issue.key, ref)
    ):
        remembered = recovered
        ledger.remember_tests(rt.con, issue.key, *remembered)
    if remembered and contains(repo_path, remembered[1], ref):
        (base, tester_sha), phase = remembered, "impl"
    claim = Claim(
        issue_key=issue.key,
        issue_id=issue.id,
        repo=repo,
        worktree=str(path),
        branch=branch,
        phase=phase,
        state="running",
        depends_on=list(settings.depends_on),
        settings_json=settings_to_json(settings),
        started_at=rt.now(),
        wall_clock_s=settings.wall_clock_s or cfg.default_wall_clock_s,
        base_sha=base,
        tester_sha=tester_sha,
        sequence=list(sequence),
        # Comments written before the claim are not replies to it.
        last_comment_at=datetime.fromtimestamp(rt.now(), UTC).isoformat(),
    )
    ledger.insert_claim(rt.con, claim)
    write_context(cfg, issue, claim)
    try:
        # A run with no tester commit has no amendments; one resuming at the executor keeps them,
        # or the guard blocks them.
        launch(rt, claim, phase, reset=("tests-amended.json",) if tester_sha is None else ())
    except BaseException:
        # A claim without a session would be relaunched by every tick and never count a restart,
        # and an In Progress issue would not be polled again. Leave it in Todo, unclaimed.
        ledger.delete_claim(rt.con, issue.key)
        raise
    try:
        rt.linear.start(issue)
    except LinearError:
        # A running claim on a Todo issue would be read as an owner's rerun once it finishes.
        rt.sessions.kill(session_name(issue.key, phase))
        ledger.delete_claim(rt.con, issue.key)
        raise


def forward_comments(rt) -> dict[str, LinearError]:
    """Returns the Linear failures by issue key; one deleted issue must not stop the others."""
    failed = {}
    for claim in ledger.live_claims(rt.con):
        if claim.state != "running":
            continue
        try:
            replies = rt.linear.comments_since(claim.issue_id, claim.last_comment_at)
        except LinearError as exc:
            failed[claim.issue_key] = exc
            continue
        if not replies:
            continue
        name = session_name(claim.issue_key, claim.phase)
        for comment in replies:
            # The service's own comments carry BOT_PREFIX, and teammates comment on NF issues too.
            # The owner's replies steer; so do an allowlisted author's (e.g. a steering agent).
            if comment.mine:
                who = "The owner"
            elif comment.author in rt.cfg.steer_authors:
                who = "A steering agent"
            else:
                who = None
            if who and not comment.body.startswith(BOT_PREFIX):
                # A dead session is relaunched by the next tick's supervisor, which delivers
                # this reply and the ones after it.
                if not rt.sessions.alive(name):
                    break
                rt.sessions.send(name, f"{who} replied on Linear: {comment.body}")
            ledger.update_claim(rt.con, claim.issue_key, last_comment_at=comment.created_at)
    return failed


def _local_hour_and_date(now: float) -> tuple[int, str]:
    t = time.localtime(now)
    return t.tm_hour, time.strftime("%Y-%m-%d", t)


def maybe_digest(rt) -> None:
    hour, today = _local_hour_and_date(rt.now())
    if hour < rt.cfg.digest_hour or ledger.meta_get(rt.con, "digest_date") == today:
        return
    rows = ledger.pending_digest(rt.con)
    if rows:
        body = "\n".join(f"- {key or 'service'}: {line}" for _, key, line in rows)
        digest = rt.linear.issue(rt.cfg.digest_issue)
        rt.linear.comment(digest.id, f"Digest for {today}\n\n{body}")
        ledger.clear_digest(rt.con, rows[-1][0])
    ledger.meta_set(rt.con, "digest_date", today)
