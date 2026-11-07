import sqlite3
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from flocc.config import Config
from flocc.ledger import Claim
from flocc.worktree import free_disk_gb


@dataclass
class Runtime:
    cfg: Config
    con: sqlite3.Connection
    # Any, not object: the tick calls methods on these, and the tests pass duck-typed fakes.
    linear: Any
    sessions: Any
    notifier: Any
    now: Callable[[], float]
    exe: str
    claude_exe: str
    config_path: Path
    publish_fn: Callable
    # The claim's branch head, fetched from flocc-agent's worktree into the service clone.
    git_head: Callable[[Claim], str]
    # The PR's diff against its base, and a comment on the PR made with the publish token.
    review_diff: Callable[[Claim], str]
    comment_pr: Callable[[Claim, str, str], None]
    free_disk_gb: Callable[[Path], float] = free_disk_gb
    # The PR's state, head and mergeability; None in tests that never reach the rebase step.
    pr_view: Callable[[Claim, str], dict] | None = None
    # PR URLs to their state (open, merged, closed), bounded by one deadline; None in tests that
    # never reach the reap.
    pr_states: Callable[[list[str]], dict[str, str]] | None = None
    # The PR's `/flocc` requests as (key, epoch time, text) oldest first; None in tests that never
    # reach the GitHub scan.
    pr_comments: Callable[[Claim, str], list[tuple[str, float, str]]] | None = None
    # Runs the repo's check on its base branch: (base SHA, failure or None). None skips the
    # base comparison.
    base_check: Callable[[str], tuple[str, Any]] | None = None
    # The service's side of flocc-agent-run; None in tests that never reach flocc-agent.
    agent: Any = None
