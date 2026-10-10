import subprocess
import sys
import time
from datetime import UTC, datetime
from pathlib import Path

from linear_agents import ledger
from linear_agents.admission import admit
from linear_agents.agentblock import (
    BlockError,
    has_acceptance_criteria,
    parse_settings,
    settings_to_json,
)
from linear_agents.config import trailer_enabled
from linear_agents.context import state_dir, write_context
from linear_agents.escalation import DEDUPE_S, Escalation, scoped
from linear_agents.ledger import Claim
from linear_agents.linear import BOT_PREFIX, Issue, LinearError
from linear_agents.sessions import session_name
from linear_agents.supervisor import launch
from linear_agents.usage import latest_usage
from linear_agents.worktree import contains, git, install_hook, prepare_worktree

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


def release_claim(rt, claim: Claim, *, keep_worktree: bool = False, to_todo: bool = True) -> None:
    rt.sessions.kill(session_name(claim.issue_key, claim.phase))
    # Each step below may already be done or impossible (a deleted issue, a worktree removed
    # by an earlier release); the claim is cleared regardless, or it blocks every tick.
    if not keep_worktree:
        try:
            git("worktree", "remove", "--force", claim.worktree, cwd=rt.cfg.repos[claim.repo].path)
        except (subprocess.CalledProcessError, OSError, KeyError) as exc:
            print(f"release: worktree {claim.worktree} not removed: {exc}", file=sys.stderr)
    # Back to Todo so the next poll picks it up again; the rerun continues on the kept branch.
    if to_todo:
        try:
            rt.linear.unstart(rt.linear.issue(claim.issue_key))
        except LinearError as exc:
            print(f"release: {claim.issue_key} not moved back to Todo: {exc}", file=sys.stderr)
    ledger.delete_claim(rt.con, claim.issue_key)
    # Alert dedupe is keyed to the old claim; a rerun that blocks again for the same cause must
    # still push.
    ledger.clear_issue_alerts(rt.con, claim.issue_key)


def poll(rt) -> None:
    cfg = rt.cfg
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
                "Skipping until the description has an 'Acceptance criteria' heading"
                " followed by a list.",
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
        live = ledger.live_claims(rt.con)
        usage = latest_usage(cfg.state_root, rt.now())
        decision = admit(issue.key, settings, live, usage, deps, cfg, rt.now())
        if not decision.ok:
            print(f"{issue.key}: held, {decision.reason}", file=sys.stderr)
            continue
        _start(rt, issue, repo, settings)
        if rerun and ledger.get_claim(rt.con, issue.key):
            try:
                rt.linear.comment(issue.id, "moved back to Todo; restarting from a fresh claim.")
            except LinearError as exc:
                print(f"{issue.key}: rerun comment not posted: {exc}", file=sys.stderr)


def _size(path: Path) -> int:
    return path.stat().st_size if path.exists() else 0


def _start(rt, issue: Issue, repo: str, settings) -> None:
    cfg = rt.cfg
    cause = f"{issue.key}:prepare"
    try:
        path, branch, base = prepare_worktree(
            cfg.repos[repo], cfg.worktree_root, issue.key, setup_timeout_s=cfg.setup_timeout_s
        )
    except (subprocess.SubprocessError, OSError) as exc:  # nothing is claimed yet
        # A tier 2 escalation labels and comments every time it is sent, and a broken ./setup
        # fails on every poll.
        if ledger.alert_seen(rt.con, cause, DEDUPE_S, rt.now()):
            return
        stderr = getattr(exc, "stderr", None) or ""
        if isinstance(stderr, bytes):
            stderr = stderr.decode(errors="replace")
        detail = f"\n\n{stderr.strip()[-1500:]}" if stderr.strip() else ""
        rt.notifier.send(
            Escalation(
                2,
                cause,
                f"Preparing the worktree failed: {exc}{detail}",
                "Fix the repo or ./setup",
                issue_key=issue.key,
                waiting=True,
            ),
            issue,
            issue.team_id,
        )
        # The notifier records a cause only when it pushes.
        ledger.record_alert(rt.con, cause, 2, issue.key, rt.now())
        return
    # Cleared here, not with the other causes in poll(): those run before every prepare attempt.
    ledger.clear_alert(rt.con, cause)
    d = state_dir(cfg, issue.key)
    install_hook(
        path,
        d / "hooks",
        rt.exe,
        rt.config_path,
        trailer=trailer_enabled(cfg, cfg.repos[repo]),
    )
    # The previous claim's notes would be read as this session's handoff, and would satisfy
    # handoff-ready before the new session wrote its own. Kept for the owner.
    if (d / "handoff.md").exists():
        (d / "handoff.md").replace(d / "handoff.prev.md")
    # A rerun on a kept branch that already holds the tester's commit resumes at the
    # implementer: a fresh tester would block on adding nothing, and the guard would lose the
    # first run's tests.
    phase, tester_sha = "test", None
    remembered = ledger.remembered_tests(rt.con, issue.key)
    # Tests already merged into the base belong to shipped work, and a reopened issue is new
    # work. After a merge commit, a branch cut fresh from the base contains them too.
    if remembered and contains(path, remembered[1], f"origin/{cfg.repos[repo].base_branch}"):
        ledger.forget_tests(rt.con, issue.key)
        remembered = None
    if remembered and contains(path, remembered[1]):
        (base, tester_sha), phase = remembered, "impl"
    claim = Claim(
        issue_key=issue.key,
        issue_id=issue.id,
        repo=repo,
        worktree=str(path),
        branch=branch,
        phase=phase,
        state="running",
        paths=list(settings.paths),
        depends_on=list(settings.depends_on),
        settings_json=settings_to_json(settings),
        started_at=rt.now(),
        wall_clock_s=settings.wall_clock_s or cfg.default_wall_clock_s,
        base_sha=base,
        tester_sha=tester_sha,
        # Comments written before the claim are not replies to it.
        last_comment_at=datetime.fromtimestamp(rt.now(), UTC).isoformat(),
        # A released issue's state directory keeps the previous claim's escalations; start past
        # them. launch() deletes its old markers.
        escalation_offset=_size(d / "escalations.jsonl"),
    )
    ledger.insert_claim(rt.con, claim)
    write_context(cfg, issue, claim)
    try:
        launch(rt, claim, phase)
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


def _new_lines(path: Path, offset: int) -> list[bytes]:
    # Only complete lines; a line still being written is picked up next tick.
    try:
        new = path.read_bytes()[offset:]
    except FileNotFoundError:
        return []
    return new.split(b"\n")[:-1]


def route_escalations(rt) -> dict[str, LinearError]:
    """Returns the Linear failures by issue key; one deleted issue must not stop the others."""
    failed = {}
    for claim in ledger.live_claims(rt.con):
        path = state_dir(rt.cfg, claim.issue_key) / "escalations.jsonl"
        lines = _new_lines(path, claim.escalation_offset)
        if not lines:
            continue
        offset = claim.escalation_offset
        if claim.phase == "review":
            # The PR is already open; a review finding must not label the issue or push the owner.
            for line in lines:
                text = line.decode(errors="replace")[:300]
                ledger.add_digest(
                    rt.con, claim.issue_key, f"review session escalated: {text}", rt.now()
                )
                offset += len(line) + 1
                ledger.update_claim(rt.con, claim.issue_key, escalation_offset=offset)
            continue
        try:
            issue = rt.linear.issue(claim.issue_key)
        except LinearError as exc:
            failed[claim.issue_key] = exc
            continue
        for line in lines:
            raw = line.decode(errors="replace")
            try:
                esc = Escalation.from_json(raw)
            except (ValueError, TypeError):
                esc = Escalation(
                    3, "malformed-escalation", f"Unreadable escalation line: {raw[:200]}"
                )
            rt.notifier.send(scoped(esc, claim.issue_key), issue, issue.team_id)
            # Per line: a failure on a later one must not send this one again next tick.
            offset += len(line) + 1
            ledger.update_claim(rt.con, claim.issue_key, escalation_offset=offset)
    return failed


def forward_comments(rt) -> dict[str, LinearError]:
    """Returns the Linear failures by issue key, like route_escalations."""
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
            # The service's own comments carry BOT_PREFIX, and teammates comment on NF issues
            # too; only the owner's replies go to the session.
            if comment.mine and not comment.body.startswith(BOT_PREFIX):
                # A dead session is relaunched by the next tick's supervisor, which delivers
                # this reply and the ones after it.
                if not rt.sessions.alive(name):
                    break
                rt.sessions.send(name, f"The owner replied on Linear: {comment.body}")
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
