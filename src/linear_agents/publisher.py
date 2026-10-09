import os
import shlex
import subprocess
from dataclasses import dataclass
from pathlib import Path

from linear_agents.config import RepoConfig
from linear_agents.ledger import Claim
from linear_agents.worktree import changed_files

_TAIL = 3000


@dataclass(frozen=True)
class PublishResult:
    status: str
    detail: str = ""
    url: str = ""


def publish(
    claim: Claim, repo: RepoConfig, title: str, summary: str, token: str, runner=subprocess.run
) -> PublishResult:
    wt = Path(claim.worktree)
    if claim.base_sha is None or claim.tester_sha is None:
        raise ValueError(f"{claim.issue_key} has no recorded tester commit to guard")
    tester_files = changed_files(wt, claim.base_sha, claim.tester_sha)
    touched = changed_files(wt, claim.tester_sha, "HEAD") & tester_files
    if touched:
        return PublishResult("tests_touched", ", ".join(sorted(touched)))
    check = runner(shlex.split(repo.check), cwd=wt, capture_output=True, text=True)
    if check.returncode != 0:
        return PublishResult("check_failed", (check.stdout + check.stderr)[-_TAIL:])
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
        return PublishResult("opened", url=existing.stdout.strip())
    pr = runner(
        [
            "gh",
            "pr",
            "create",
            "--draft",
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
    return PublishResult("opened", url=pr.stdout.strip().splitlines()[-1])
