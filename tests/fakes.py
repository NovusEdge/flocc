from dataclasses import dataclass, field

from flocc.linear import Issue

ISSUE = Issue("u1", "NF-1", "Fix rounding", "desc", "https://l/NF-1", "started", (), None, "t1")


@dataclass
class FakeSessions:
    alive_names: set = field(default_factory=set)
    started: list = field(default_factory=list)
    sent: list = field(default_factory=list)
    killed: list = field(default_factory=list)

    def start(self, name, cwd, argv):
        self.started.append((name, argv))
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

    def send(self, esc, issue, team_id):
        self.sent.append(esc)


@dataclass
class FakeLinear:
    comments: list = field(default_factory=list)

    def comment(self, issue_id, text):
        self.comments.append(text)

    def issue(self, key):
        return ISSUE
