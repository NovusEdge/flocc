from dataclasses import dataclass, field
from pathlib import Path

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
