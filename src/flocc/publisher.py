import json
import os
import re
import shlex
import subprocess
from dataclasses import dataclass
from pathlib import Path

from flocc.config import TRAILER, RepoConfig
from flocc.gate import AMENDED
from flocc.ledger import Claim
from flocc.worktree import changed_files, git, run_bounded

_TAIL = 3000
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
        data = json.loads((state / AMENDED).read_text())
    except (OSError, ValueError):
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


def weakens_test(wt: Path, since: str, path: str) -> str | None:
    """Why the change to a tester's file since `since` drops or disables a test, if it does.

    Not covered: subtests, parametrize cases and a test wrapped in a comment; the reviewer reads
    the amendment diff for those."""
    diff = git("diff", "-U0", f"{since}..HEAD", "--", path, cwd=wt)
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


def pr_title(key: str, title: str, labels: tuple[str, ...] = ()) -> str:
    kind = next((_TITLE_TYPE[x] for x in labels if x in _TITLE_TYPE), "feat")
    return f"{kind}: {title} ({key})"


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
) -> PublishResult:
    wt = Path(claim.worktree)
    if claim.base_sha is None or claim.tester_sha is None:
        raise ValueError(f"{claim.issue_key} has no recorded tester commit to guard")
    tester_files = changed_files(wt, claim.base_sha, claim.tester_sha)
    since = accepted_sha or claim.tester_sha
    touched = changed_files(wt, since, "HEAD") & tester_files
    weakened = {}
    for path in touched & set(amended or {}):
        if why := weakens_test(wt, since, path):
            weakened[path] = why
    touched -= set(amended or {})
    if touched or weakened:
        parts = [*sorted(touched), *(f"{p} {why}" for p, why in sorted(weakened.items()))]
        return PublishResult("tests_touched", ", ".join(parts))
    # Only commits are pushed, and the guard above compares commits only; a check on the working
    # tree would pass on files the PR leaves out.
    dirty = git("status", "--porcelain", cwd=wt)
    if dirty:
        detail = f"Uncommitted changes; commit them or remove them, then finish again:\n{dirty}"
        return PublishResult("check_failed", detail[:_TAIL])
    code, out = run_bounded(shlex.split(repo.check), wt, check_timeout_s)
    if code is None:
        detail = f"`{repo.check}` timed out after {check_timeout_s}s\n\n{out[-_TAIL:]}"
        return PublishResult("check_failed", detail.strip(), log=out)
    if code != 0:
        return PublishResult("check_failed", failure_excerpt(out), log=out)
    # Commits made with --no-verify skip the hook; noted, not blocked, since the work is sound.
    untrailed = ()
    if trailer:
        out = git(
            "log",
            f"{claim.base_sha}..{claim.branch}",
            "--format=%h",
            "--invert-grep",
            f"--grep=^{TRAILER}: ",
            cwd=wt,
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
                "-u",
                "origin",
                claim.branch,
            ],
            cwd=wt,
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
        cwd=wt,
        env=env,
        check=True,
        capture_output=True,
        text=True,
    )

    def label(url: str) -> None:
        if not (amended and repo.amended_label):
            return
        runner(
            ["gh", "pr", "edit", url, "--add-label", repo.amended_label],
            cwd=wt,
            env=env,
            check=True,
            capture_output=True,
            text=True,
        )

    if existing.stdout.strip():
        label(existing.stdout.strip())
        return PublishResult(
            "opened", url=existing.stdout.strip(), untrailed=untrailed, existing=True
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
        cwd=wt,
        env=env,
        check=True,
        capture_output=True,
        text=True,
    )
    url = pr.stdout.strip().splitlines()[-1]
    label(url)
    return PublishResult("opened", url=url, untrailed=untrailed)


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
        ["gh", "pr", "view", url, "--json", "state,mergedAt,files"],
        # A finished claim's worktree may already be reaped.
        cwd=claim.worktree if Path(claim.worktree).exists() else None,
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
        cwd=claim.worktree,
        env={**os.environ, "GH_TOKEN": token},
        check=True,
        capture_output=True,
        text=True,
    )
