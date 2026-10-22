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
    lines = "\n".join(f"- `{path}`: {entry.get('question', '')}" for path, entry in amended.items())
    return f"\n\n## Amended tests\n\nThe owner approved these changes to the tester's files:\n\n{lines}"


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
) -> PublishResult:
    wt = Path(claim.worktree)
    if claim.base_sha is None or claim.tester_sha is None:
        raise ValueError(f"{claim.issue_key} has no recorded tester commit to guard")
    tester_files = changed_files(wt, claim.base_sha, claim.tester_sha)
    touched = changed_files(wt, accepted_sha or claim.tester_sha, "HEAD") & tester_files
    touched -= set(amended or {})
    if touched:
        return PublishResult("tests_touched", ", ".join(sorted(touched)))
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
    if existing.stdout.strip():
        return PublishResult("opened", url=existing.stdout.strip(), untrailed=untrailed)
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
            f"{claim.issue_key}: {title}",
            "--body",
            f"Fixes {claim.issue_key}\n\n{summary}{amended_section(amended)}",
        ],
        cwd=wt,
        env=env,
        check=True,
        capture_output=True,
        text=True,
    )
    return PublishResult("opened", url=pr.stdout.strip().splitlines()[-1], untrailed=untrailed)


COMMENT_HEADER = "🤖 flocc review\n\n"
# GitHub rejects issue and PR comments past this many characters.
COMMENT_LIMIT = 65_536
_TRUNCATED = "\n\n[Truncated at GitHub's 65,536-character comment limit; full text is on the host.]"


def review_comment(review: str) -> str:
    body = COMMENT_HEADER + review
    if len(body) <= COMMENT_LIMIT:
        return body
    return body[: COMMENT_LIMIT - len(_TRUNCATED)] + _TRUNCATED


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
