import sqlite3
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from linear_agents.config import Config


@dataclass
class Runtime:
    cfg: Config
    con: sqlite3.Connection
    linear: object
    sessions: object
    notifier: object
    now: Callable[[], float]
    exe: str
    claude_exe: str
    config_path: Path
    publish_fn: Callable
    git_head: Callable[[str], str]
