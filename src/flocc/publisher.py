import json
import os
import re
import shlex
import subprocess
from collections.abc import Callable
from dataclasses import dataclass, replace
from pathlib import Path

from flocc import agentfs
from flocc.config import TRAILER, RepoConfig
from flocc.context import state_dir
from flocc.ledger import Claim
from flocc.worktree import (
    agent_ref,
    base_checkout,
    changed_files,
    fetch_agent_branch,
    git,
    origin_base,
    run_bounded,
    setup_in_checkout,
)

AMENDED = "tests-amended.json"
_TAIL = 3000
# Stands in for the base checkout's path when its failure is compared with a branch's.
BASE_CHECKOUT = "<checkout>"
_FAILURE = re.compile(r"error|fail|traceback|panic|exit code [1-9]", re.IGNORECASE)
_NOISE = re.compile(r"warning", re.IGNORECASE)
# git's words for a remote branch holding commits the push lacks; `[remote rejected]` (a hook or
# branch protection) is a different failure.
_MOVED = re.compile(r"\[rejected\].*\((non-fast-forward|fetch first)\)")


# The same push is rejected until someone reconciles the branches, so retrying cannot help.
class BranchMoved(subprocess.CalledProcessError):
    def __str__(self) -> str:
        return (
            "GitHub's copy of the branch has commits this one lacks"
            " (another host or a person pushed, or a pushed commit was rewritten)"
        )


@dataclass(frozen=True)
class PublishResult:
    status: str
    detail: str = ""
    url: str = ""
    untrailed: tuple[str, ...] = ()  # short SHAs of branch commits without the Flocc trailer
    log: str = ""  # the check's full output, when it ran and failed
    existing: bool = False  # "opened" by finding the PR that was already open
    label_error: str = ""  # why the amended-tests label was not added to the open PR


def pr_url(rt, claim: Claim) -> str | None:
    path = state_dir(rt.cfg, claim.issue_key) / "pr-url"
    url = path.read_text().strip() if path.exists() else ""
    return url if url and url != "unknown" else None


def failure_excerpt(out: str, limit: int = _TAIL) -> str:
    # A check over several components prints the failing one first and passing ones after it, so
    # the tail alone showed only passing tests (NF-340, 2026-10-20).
    if len(out) <= limit:
        return out
    lines = out.splitlines()
    keep: set[int] = set(range(max(0, len(lines) - 10), len(lines)))
    for i, line in enumerate(lines):
        if _FAILURE.search(line) and not _NOISE.search(line):
            near = range(max(0, i - 2), min(len(lines), i + 3))
            keep.update(j for j in near if not _NOISE.search(lines[j]))
    picked, last = [], -2
    for i in sorted(keep):
        if i != last + 1:
            picked.append("...")
        picked.append(lines[i])
        last = i
    text = "\n".join(picked)
    return text if len(text) <= limit else text[: limit - 4] + "\n..."


def read_amended(state: Path) -> dict[str, dict]:
    try:
        data = json.loads(agentfs.read_text(state, AMENDED) or "{}")
    except ValueError:
        return {}
    return data if isinstance(data, dict) else {}


def amended_section(amended: dict[str, dict] | None) -> str:
    if not amended:
        return ""
    lines = "\n".join(f"- `{path}`: {entry.get('reason', '')}" for path, entry in amended.items())
    return f"\n\n## Amended tests\n\nThe implementer changed these tester files, with these reasons:\n\n{lines}"


# describe() groups tests without being one: renaming a group drops no test.
_TEST_NAME = re.compile(
    r"^\s*(?:(?:async\s+)?def (test_\w*)|func (Test\w*)"
    r"|(?:test|it)(?:\.\w+)*\(\s*(['\"`])(.+?)\3)"
)
# Each form is tied to a test framework's own name, so .iter().skip(1) or client.todo(3) in the
# code under test does not read as a skipped test.
_SKIPS_TEST = re.compile(
    r"pytest\.mark\.(?:skip|skipif|xfail)\b|pytest\.(?:skip|xfail|importorskip)\("
    r"|unittest\.(?:skip|skipIf|skipUnless|expectedFailure)\b"
    r"|@(?:skip|skipIf|skipUnless|expectedFailure)\b|self\.skipTest\("
    r"|\b(?:test|it|describe)\.(?:skip|only|todo|skipIf|fails)\b|\bx(?:it|test|describe)\("
    r"|\bt\.Skip(?:f|Now)?\("
)


def _test_names(lines: list[str]) -> set[str]:
    return {m[1] or m[2] or m[4] for line in lines if (m := _TEST_NAME.match(line))}


def weakens_test(wt: Path, since: str, path: str, head: str = "HEAD") -> str | None:
    """Why the change to a tester's file since `since` drops or disables a test, if it does.

    Not covered: subtests, parametrize cases and a test wrapped in a comment; the reviewer reads
    the amendment diff for those."""
    diff = git("diff", "-U0", f"{since}..{head}", "--", path, cwd=wt)
    removed = [x[1:] for x in diff.splitlines() if x.startswith("-") and not x.startswith("---")]
    added = [x[1:] for x in diff.splitlines() if x.startswith("+") and not x.startswith("+++")]
    # First: a skip wrapper such as it.skipIf(c)('a') hides the name, which would read as removal.
    if sum(map(bool, map(_SKIPS_TEST.search, added))) > sum(
        map(bool, map(_SKIPS_TEST.search, removed))
    ):
        return "skips a test"
    # Names, not counts: a test renamed in place leaves the counts level and still loses a test.
    if _test_names(removed) - _test_names(added):
        return "removes a test"
    return None


# Repos gate PR titles on a Conventional Commits type (amannn/action-semantic-pull-request), and a
# squash merge takes the title as the commit subject.
_TITLE_TYPE = {"Bug": "fix", "Chore": "chore"}


_COMMIT_TYPE = re.compile(
    r"^(feat|fix|docs|chore|refactor|test|perf|build|ci|style)(?:\([^)]*\))?!?: "
)


def pr_title(
    key: str, title: str, labels: tuple[str, ...] = (), subjects: tuple[str, ...] = ()
) -> str:
    """The branch's own commit type wins when its commits agree on one (a docs-only change reads
    `docs:`, not the label's `feat:`); otherwise the issue's labels decide."""
    types = {m[1] if (m := _COMMIT_TYPE.match(s)) else None for s in subjects}
    if len(types) == 1 and None not in types:
        (kind,) = types
    else:
        kind = next((_TITLE_TYPE[x] for x in labels if x in _TITLE_TYPE), "feat")
    return f"{kind}: {title} ({key})"


def branch_subjects(claim: Claim, repo: RepoConfig) -> tuple[str, ...]:
    """Subjects of the executor's and fixers' commits since the base; the tester's are left out.
    Empty when the branch cannot be read, which leaves the title to the labels."""
    try:
        ref = agent_ref(claim.issue_key)
        fetch_agent_branch(repo.path, Path(claim.worktree), claim.branch, claim.issue_key)
        out = git(
            "log",
            f"{claim.base_sha}..{ref}",
            "--no-merges",
            "--format=%s",
            "--invert-grep",
            f"--grep=^{TRAILER}: {claim.issue_key} test$",
            cwd=repo.path,
        )
    except (subprocess.SubprocessError, OSError):
        return ()
    return tuple(out.splitlines())


def run_check(repo: RepoConfig, wt: Path, timeout_s: float) -> PublishResult | None:
    code, out = run_bounded(shlex.split(repo.check), wt, timeout_s)
    if code is None:
        detail = f"`{repo.check}` timed out after {timeout_s}s\n\n{out[-_TAIL:]}"
        return PublishResult("check_failed", detail.strip(), log=out)
    if code != 0:
        return PublishResult("check_failed", failure_excerpt(out), log=out)
    return None


def check_at_base(
    repo: RepoConfig,
    worktree_root: Path,
    setup_timeout_s: float,
    check_timeout_s: float,
    shared_uv_cache: bool,
) -> tuple[str, PublishResult | None]:
    """origin/<base>'s SHA and how the repo's check fails there; None when it passes."""
    with base_checkout(repo, worktree_root) as (path, sha):
        code, out = setup_in_checkout(path, setup_timeout_s, shared_uv_cache)
        if code != 0:
            # Not the check's failure, so its digest never matches a branch's and no hold follows.
            why = "timed out" if code is None else f"exited {code}"
            return sha, PublishResult(
                "check_failed", f"./setup {why} on the base\n\n{out[-_TAIL:]}", log=out
            )
        failure = run_check(repo, path, check_timeout_s)
        if failure is None:
            return sha, None
        # The checkout's name is random, so a branch's failure could never match the same text.
        return sha, replace(
            failure,
            detail=failure.detail.replace(str(path), BASE_CHECKOUT),
            log=failure.log.replace(str(path), BASE_CHECKOUT),
        )


def _without_merged_base(wt: Path, since: str, base_branch: str, head: str = "HEAD") -> str:
    """`since`, or the tree it makes when merged with the base commits `head` took in.

    After a merge from the base, the two-dot diff from the tester's commit lists every file the
    base changed; against this tree only the branch's own edits and conflict resolutions differ."""
    try:
        taken = git("merge-base", head, f"origin/{base_branch}", cwd=wt)
    except subprocess.CalledProcessError:
        return since
    ancestor = subprocess.run(
        ["git", "merge-base", "--is-ancestor", taken, since], cwd=wt, check=False
    )
    if ancestor.returncode == 0:
        return since
    # Exit 1 is a conflicted merge; its tree still holds the markers, so a resolution differs.
    merged = subprocess.run(
        ["git", "merge-tree", "--write-tree", "--no-messages", since, taken],
        cwd=wt,
        capture_output=True,
        text=True,
        check=False,
    )
    return merged.stdout.split()[0] if merged.returncode in (0, 1) and merged.stdout else since


def _tests_guard(
    claim: Claim,
    repo: RepoConfig,
    repo_path: Path,
    ref: str,
    accepted_sha: str | None,
    amended: dict[str, dict] | None,
) -> PublishResult | None:
    assert claim.base_sha is not None and claim.tester_sha is not None
    tester_files = changed_files(repo_path, claim.base_sha, claim.tester_sha)
    since = _without_merged_base(repo_path, accepted_sha or claim.tester_sha, repo.base_branch, ref)
    touched = changed_files(repo_path, since, ref) & tester_files
    weakened = {}
    for path in touched & set(amended or {}):
        if why := weakens_test(repo_path, since, path, ref):
            weakened[path] = why
    touched -= set(amended or {})
    if touched or weakened:
        parts = [*sorted(touched), *(f"{p} {why}" for p, why in sorted(weakened.items()))]
        return PublishResult("tests_touched", ", ".join(parts))
    return None


def check_worktree(command: str, wt: Path, timeout_s: float) -> dict:
    """The repo's check in wt: status ok, dirty, failed or timeout, its output, and the head."""
    head = git("rev-parse", "HEAD", cwd=wt)
    # Only commits are pushed and guarded; a check on the working tree would pass on files the PR
    # leaves out.
    dirty = git("status", "--porcelain", cwd=wt)
    if dirty:
        return {"status": "dirty", "output": dirty, "head": head}
    code, out = run_bounded(shlex.split(command), wt, timeout_s)
    status = "timeout" if code is None else "ok" if code == 0 else "failed"
    return {"status": status, "output": out, "head": git("rev-parse", "HEAD", cwd=wt)}


def publish(
    claim: Claim,
    repo: RepoConfig,
    title: str,
    summary: str,
    token: str,
    runner=subprocess.run,
    check_timeout_s: float = 1800,
    trailer: bool = True,
    accepted_sha: str | None = None,
    amended: dict[str, dict] | None = None,
    reuse_only: bool = False,
    *,
    check: Callable[[Claim, RepoConfig, float], dict],
) -> PublishResult:
    wt = Path(claim.worktree)
    if claim.base_sha is None:
        raise ValueError(f"{claim.issue_key} has no recorded base commit")
    head = fetch_agent_branch(repo.path, wt, claim.branch, claim.issue_key)
    ref = agent_ref(claim.issue_key)
    # The guard's merge-base needs this clone's origin/<base> current; the agent's fetch updates
    # only its own clone.
    origin_base(repo)
    # No tester ran: there are no tester files to guard.
    if claim.tester_sha is not None and (
        touched := _tests_guard(claim, repo, repo.path, ref, accepted_sha, amended)
    ):
        return touched
    ran = check(claim, repo, check_timeout_s)
    out = str(ran.get("output", ""))
    if ran["status"] == "dirty":
        detail = f"Uncommitted changes; commit them or remove them, then finish again:\n{out}"
        return PublishResult("check_failed", detail[:_TAIL])
    if ran["status"] == "timeout":
        detail = f"`{repo.check}` timed out after {check_timeout_s}s\n\n{out[-_TAIL:]}"
        return PublishResult("check_failed", detail.strip(), log=out)
    if ran["status"] != "ok":
        return PublishResult("check_failed", failure_excerpt(out), log=out)
    if ran["head"] != head:
        return PublishResult(
            "check_failed",
            "The branch moved while the check ran: commit nothing during the check, then finish"
            " again.",
        )
    # Commits made with --no-verify skip the hook; noted, not blocked, since the work is sound.
    untrailed = ()
    if trailer:
        out = git(
            "log",
            f"{claim.base_sha}..{ref}",
            "--format=%h",
            "--invert-grep",
            f"--grep=^{TRAILER}: ",
            cwd=repo.path,
        )
        untrailed = tuple(out.split())
    env = {**os.environ, "GH_TOKEN": token}
    try:
        runner(
            [
                "git",
                "-c",
                "credential.helper=",
                "-c",
                "credential.helper=!gh auth git-credential",
                "push",
                "-q",
                "origin",
                f"{ref}:refs/heads/{claim.branch}",
            ],
            cwd=repo.path,
            env=env,
            check=True,
            capture_output=True,
            text=True,
        )
    except subprocess.CalledProcessError as exc:
        if _MOVED.search(exc.stderr or ""):
            raise BranchMoved(exc.returncode, exc.cmd, exc.output, exc.stderr) from exc
        raise
    # A publish retried after a later step failed finds the PR it already opened.
    existing = runner(
        [
            "gh",
            "pr",
            "list",
            "--head",
            claim.branch,
            "--state",
            "open",
            "--json",
            "url",
            "--jq",
            ".[0].url // empty",
        ],
        cwd=repo.path,
        env=env,
        check=True,
        capture_output=True,
        text=True,
    )

    def label(url: str) -> str:
        """The reason the label was not added, or ''. The PR is open either way: a failure here
        would rerun the check and block a published PR, over a missing label or a token scope."""
        if not (amended and repo.amended_label):
            return ""
        try:
            runner(
                ["gh", "pr", "edit", url, "--add-label", repo.amended_label],
                cwd=repo.path,
                env=env,
                check=True,
                capture_output=True,
                text=True,
            )
        except subprocess.CalledProcessError as exc:
            return (exc.stderr or str(exc)).strip()
        return ""

    if existing.stdout.strip():
        url = existing.stdout.strip()
        return PublishResult(
            "opened", url=url, untrailed=untrailed, existing=True, label_error=label(url)
        )
    if reuse_only:
        # A review fix round: the owner closed or merged the PR meanwhile, and a new one on the
        # same branch would reopen work they just finished.
        return PublishResult("pr_closed")
    pr = runner(
        [
            "gh",
            "pr",
            "create",
            *(["--draft"] if repo.draft else []),
            "--head",
            claim.branch,
            "--base",
            repo.base_branch,
            "--title",
            title,
            "--body",
            f"Fixes {claim.issue_key}\n\n{summary}{amended_section(amended)}",
        ],
        cwd=repo.path,
        env=env,
        check=True,
        capture_output=True,
        text=True,
    )
    url = pr.stdout.strip().splitlines()[-1]
    return PublishResult("opened", url=url, untrailed=untrailed, label_error=label(url))


COMMENT_HEADER = "🤖 flocc review\n\n"
# GitHub rejects issue and PR comments past this many characters.
COMMENT_LIMIT = 65_536
_TRUNCATED = "\n\n[Truncated at GitHub's 65,536-character comment limit; full text is on the host.]"


def review_comment(review: str) -> str:
    body = COMMENT_HEADER + review
    if len(body) <= COMMENT_LIMIT:
        return body
    return body[: COMMENT_LIMIT - len(_TRUNCATED)] + _TRUNCATED


def pr_view(claim: Claim, url: str, token: str, runner=subprocess.run) -> dict:
    out = runner(
        ["gh", "pr", "view", url, "--json", "state,mergedAt,files,mergeable,headRefOid"],
        cwd=None,
        env={**os.environ, "GH_TOKEN": token},
        check=True,
        capture_output=True,
        text=True,
    )
    return json.loads(out.stdout)


def comment_on_pr(claim: Claim, url: str, body: str, token: str, runner=subprocess.run) -> None:
    # The body goes in on stdin: a long review would otherwise ride on the command line.
    runner(
        ["gh", "pr", "comment", url, "--body-file", "-"],
        input=body,
        env={**os.environ, "GH_TOKEN": token},
        check=True,
        capture_output=True,
        text=True,
    )
