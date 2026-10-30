from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from flocc.agentrun import apply_handoff
from flocc.linear import Issue

ISSUE = Issue("u1", "NF-1", "Fix rounding", "desc", "https://l/NF-1", "started", (), None, "t1")


@dataclass
class FakeSessions:
    alive_names: set = field(default_factory=set)
    started: list = field(default_factory=list)
    sent: list = field(default_factory=list)
    killed: list = field(default_factory=list)
    panes: dict = field(default_factory=dict)
    captured: list = field(default_factory=list)
    starts: list = field(default_factory=list)

    def capture(self, name, lines):
        self.captured.append((name, lines))
        return self.panes.get(name, "")

    def start(self, name, cwd, argv, *, state=None, files=None, clear=(), trust=(), plugins=None):
        # The real file handling, so tests see what flocc-agent would find in its directory.
        if state is not None:
            apply_handoff(Path(state), dict(files or {}), clear)
        self.started.append((name, argv))
        self.starts.append(
            {
                "name": name,
                "cwd": cwd,
                "argv": argv,
                "state": state,
                "files": dict(files or {}),
                "clear": list(clear),
                "trust": list(trust),
                "plugins": plugins,
            }
        )
        self.alive_names.add(name)

    def alive(self, name):
        return name in self.alive_names

    def kill(self, name):
        self.killed.append(name)
        self.alive_names.discard(name)

    def send(self, name, text):
        self.sent.append((name, text))


@dataclass
class FakeAgent:
    cfg: Any
    prepare_fn: Any = None
    removed: list = field(default_factory=list)
    swept: list = field(default_factory=list)
    probe_result: dict = field(
        default_factory=lambda: {"read": {}, "write": {}, "tmux": True, "groups": []}
    )

    def prepare(self, name, key):
        repo = self.cfg.repos[name]
        return self.prepare_fn(
            repo,
            self.cfg.worktree_root,
            key,
            setup_timeout_s=self.cfg.setup_timeout_s,
            shared_uv_cache=self.cfg.shared_uv_cache,
        )

    def remove(self, key, keep_dirty=False):
        self.removed.append((key, keep_dirty))
        return {"removed": True, "dirty": False}

    def sweep(self, keep, now, max_age_s, scratch_age_s):
        self.swept.append((keep, now, max_age_s, scratch_age_s))
        return {"removed": [], "kept": []}

    def probe(self, read, write):
        return self.probe_result

    def claude_argv(self):
        return ["/opt/claude"]


@dataclass
class FakeNotifier:
    sent: list = field(default_factory=list)

    def send(self, notice, issue=None):
        self.sent.append(notice)


@dataclass
class FakeLinear:
    comments: list = field(default_factory=list)

    def comment(self, issue_id, text):
        self.comments.append(text)

    def issue(self, key):
        return ISSUE
