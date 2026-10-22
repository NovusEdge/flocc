import os
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
from flocc.config import trailer_enabled
from flocc.context import state_dir, write_context
from flocc.escalation import DEDUPE_S, Escalation, scoped
from flocc.grants import GrantError, add_comment, allow_lines, parse_allow
from flocc.ledger import Claim
from flocc.linear import BOT_PREFIX, Issue, LinearError
from flocc.sessions import session_name
from flocc.supervisor import launch
from flocc.usage import latest_usage
from flocc.worktree import (
    DirtyWorktree,
    contains,
    git,
    has_uncommitted_changes,
    head_at_base,
    install_hook,
    origin_base,
    prepare_worktree,
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


def restart_timed_out(rt) -> None:
    """Sends a run that hit the wall clock after committing back to Todo, once per issue."""
    for claim in ledger.finished_claims(rt.con):
        key = claim.issue_key
        once = f"timeout_restart:{key}"
        wt = Path(claim.worktree)
        if claim.state != "timed_out" or claim.base_sha is None or not wt.exists():
            continue
        if ledger.meta_get(rt.con, once):
            continue
        try:
            stamps = git("log", "--format=%ct", claim.branch, "--not", claim.base_sha, cwd=wt)
            if not any(float(s) > claim.started_at for s in stamps.split()):
                continue
            if rt.linear.state_types([key])[key] in ("completed", "canceled"):
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
    """Removes the worktrees of finished runs whose issue is closed; the branch stays."""
    # Caches a killed tick left behind; setup runs under the tick lock, so none is in use here.
    for stale in rt.cfg.worktree_root.glob(".uv-cache-*"):
        shutil.rmtree(
            stale, onexc=lambda fn, p, exc: print(f"reap: {p} not removed: {exc}", file=sys.stderr)
        )
    for claim in ledger.finished_claims(rt.con):
        # A removed worktree is not retried: its path is gone, so no Linear call is made.
        if not Path(claim.worktree).exists():
            continue
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
        try:
            # A run stopped by the wall clock may hold work nobody committed, new files included;
            # a finished one published, so what is left is disposable.
            if claim.state == "timed_out" and has_uncommitted_changes(Path(claim.worktree)):
                cause = f"{claim.issue_key}:worktree-kept"
                if not ledger.alert_seen(rt.con, cause, COMMENT_ONCE_S, rt.now()):
                    print(f"reap: {claim.worktree} has uncommitted changes, kept", file=sys.stderr)
                    ledger.record_alert(rt.con, cause, 3, claim.issue_key, rt.now())
                continue
            git("worktree", "remove", "--force", claim.worktree, cwd=rt.cfg.repos[claim.repo].path)
        except (subprocess.CalledProcessError, OSError, KeyError) as exc:
            print(f"reap: worktree {claim.worktree} not removed: {exc}", file=sys.stderr)


def scratch_root() -> Path:
    return Path(f"/tmp/claude-{os.getuid()}")


def _scratch_path(line: str) -> Path | None:
    if not line.strip():
        return None
    root = scratch_root()
    path = Path(line.strip())
    try:
        rel = path.resolve().relative_to(root.resolve())
    except ValueError:
        return None
    # A link or ".." in the listed path would make the delete land on another session's pad.
    if path != root / rel:
        return None
    # <project>/<session>/<scratchpad>: anything shorter is shared by other sessions.
    return path if len(rel.parts) >= 3 else None


def clean_scratchpads(rt) -> None:
    """Removes the Claude Code scratchpads of claims that are no longer running."""
    for listing in rt.cfg.state_root.glob("*/scratchpads"):
        claim = ledger.get_claim(rt.con, listing.parent.name)
        if claim is not None and claim.state == "running":
            continue
        # A bad listing must not abort the tick: poll comes after this.
        try:
            for line in listing.read_text().splitlines():
                path = _scratch_path(line)
                if path is not None:
                    shutil.rmtree(path, ignore_errors=True)
            listing.unlink(missing_ok=True)
        except (OSError, UnicodeDecodeError) as exc:
            print(f"scratchpads: {listing} not cleaned: {exc}", file=sys.stderr)


def _main_hold(rt, repo: str, bases: dict[str, str | None]) -> str | None:
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
        hold = _main_hold(rt, repo, bases)
        if hold:
            print(f"{issue.key}: held, {hold}", file=sys.stderr)
            continue
        live = ledger.live_claims(rt.con)
        usage = latest_usage(cfg.state_root, rt.now())
        free = rt.free_disk_gb(cfg.worktree_root)
        decision = admit(issue.key, repo, settings, live, usage, deps, cfg, rt.now(), free)
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
    # A kept worktree may hold a previous run's uncommitted work, so its failure is the issue's.
    reused = (cfg.worktree_root.resolve() / issue.key).exists()
    try:
        path, branch, base = prepare_worktree(
            cfg.repos[repo],
            cfg.worktree_root,
            issue.key,
            setup_timeout_s=cfg.setup_timeout_s,
            shared_uv_cache=cfg.shared_uv_cache,
        )
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
        base = None
        if setup_failed and not reused:
            base = head_at_base(cfg.repos[repo], cfg.worktree_root.resolve() / issue.key)
        if base is not None:
            base_branch = cfg.repos[repo].base_branch
            # Marked only once the escalation is out: a mark holds every issue before _start,
            # so one set ahead of a failed send would hold them with nobody told.
            rt.notifier.send(
                Escalation(
                    1,
                    f"{repo}:main-broken",
                    f"./setup fails on a clean checkout of {repo}'s {base_branch} at {base[:9]},"
                    f" so its issues are held: {exc}{detail}",
                    f"Fix ./setup on {base_branch} or on the host. Issues resume after a new"
                    f" commit on {base_branch}, after 24 hours, or once flocc canary {repo} passes",
                    issue_key=issue.key,
                ),
                issue,
                issue.team_id,
            )
            canary.mark(rt.con, repo, base, rt.now(), stderr.strip() or str(exc))
            return
        # A tier 2 escalation labels and comments every time it is sent, and a broken ./setup
        # fails on every poll.
        if ledger.alert_seen(rt.con, cause, DEDUPE_S, rt.now()):
            return
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
    # A previous claim's grants, scratchpads, compaction and question belong to that run; the
    # agent block's grants are written again at launch.
    for stale in (
        "grants.json",
        "scratchpads",
        "compacted",
        "asking",
        "asking-escalated",
        "idle",
        "idle-escalated",
    ):
        (d / stale).unlink(missing_ok=True)
    # A rerun on a kept branch that already holds the tester's commit resumes at the
    # implementer: a fresh tester would block on adding nothing, and the guard would lose the
    # first run's tests.
    phase, tester_sha = "test", None
    base_branch = cfg.repos[repo].base_branch
    remembered = ledger.remembered_tests(rt.con, issue.key)
    # Tests already merged into the base belong to shipped work, and a reopened issue is new
    # work. After a merge commit, a branch cut fresh from the base contains them too.
    if remembered and contains(path, remembered[1], f"origin/{base_branch}"):
        ledger.forget_tests(rt.con, issue.key)
        remembered = None
    # A branch another host pushed holds its tester's commits, but this ledger has no record of
    # them, and the publisher's guard and --accept-tests compare against that record.
    if remembered is None:
        remembered = recover_tests(path, base_branch, branch, issue.key)
        if remembered:
            ledger.remember_tests(rt.con, issue.key, *remembered)
    if remembered and contains(path, remembered[1]):
        (base, tester_sha), phase = remembered, "impl"
    # A run resuming at the implementer still needs the first run's amendments, or the guard
    # blocks them; a run that starts at the tester has none.
    if phase == "test":
        (d / "tests-amended.json").unlink(missing_ok=True)
    claim = Claim(
        issue_key=issue.key,
        issue_id=issue.id,
        repo=repo,
        worktree=str(path),
        branch=branch,
        phase=phase,
        state="running",
        paths=ledger.unshared(list(settings.paths), cfg.repos[repo].shared_paths),
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


def _apply_grants(rt, claim: Claim, body: str) -> None:
    path = state_dir(rt.cfg, claim.issue_key) / "grants.json"
    for line in allow_lines(body):
        try:
            add_comment(path, parse_allow(line), claim.phase)
        except GrantError as exc:
            try:
                rt.linear.comment(claim.issue_id, f"{BOT_PREFIX} grant not applied: {exc}")
            except LinearError as err:
                print(f"{claim.issue_key}: grant reply not posted: {err}", file=sys.stderr)


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
                # this reply and the ones after it. While a question is open, typing into the
                # pane could select an option in the dialog, so the reply waits for the answer.
                if (
                    not rt.sessions.alive(name)
                    or (state_dir(rt.cfg, claim.issue_key) / "asking").exists()
                ):
                    break
                rt.sessions.send(name, f"{who} replied on Linear: {comment.body}")
                # After delivery: a dead session breaks out above with last_comment_at unmoved, so
                # the comment is seen again and neither applied nor answered twice.
                _apply_grants(rt, claim, comment.body)
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
