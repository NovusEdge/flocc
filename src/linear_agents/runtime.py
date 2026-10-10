import sqlite3
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from linear_agents.config import Config
from linear_agents.ledger import Claim


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
    git_head: Callable[[str], str]
    # The PR's diff against its base, and a comment on the PR made with the publish token.
    review_diff: Callable[[Claim], str]
    comment_pr: Callable[[Claim, str, str], None]
