import sqlite3
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from linear_agents.config import Config


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
