import os
import shlex
import subprocess
from dataclasses import dataclass
from pathlib import Path

from linear_agents.config import RepoConfig
from linear_agents.ledger import Claim
from linear_agents.worktree import changed_files, git, run_bounded

_TAIL = 3000


@dataclass(frozen=True)
class PublishResult:
    status: str
    detail: str = ""
    url: str = ""
    untrailed: tuple[
        str, ...
    ] = ()  # short SHAs of branch commits without the Linear-Agents trailer


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
) -> PublishResult:
    wt = Path(claim.worktree)
    if claim.base_sha is None or claim.tester_sha is None:
        raise ValueError(f"{claim.issue_key} has no recorded tester commit to guard")
    tester_files = changed_files(wt, claim.base_sha, claim.tester_sha)
    touched = changed_files(wt, accepted_sha or claim.tester_sha, "HEAD") & tester_files
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
        return PublishResult("check_failed", detail.strip())
    if code != 0:
        return PublishResult("check_failed", out[-_TAIL:])
    # Commits made with --no-verify skip the hook; noted, not blocked, since the work is sound.
    untrailed = ()
    if trailer:
        out = git(
            "log",
            f"{claim.base_sha}..{claim.branch}",
            "--format=%h",
            "--invert-grep",
            "--grep=^Linear-Agents: ",
            cwd=wt,
        )
        untrailed = tuple(out.split())
    env = {**os.environ, "GH_TOKEN": token}
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
            f"Fixes {claim.issue_key}\n\n{summary}",
        ],
        cwd=wt,
        env=env,
        check=True,
        capture_output=True,
        text=True,
    )
    return PublishResult("opened", url=pr.stdout.strip().splitlines()[-1], untrailed=untrailed)


COMMENT_HEADER = "🤖 linear-agents review\n\n"
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
