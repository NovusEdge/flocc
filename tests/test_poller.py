import json
import subprocess
from dataclasses import dataclass, field, replace

import pytest

from linear_agents import ledger
from linear_agents.config import load
from linear_agents.context import state_dir
from linear_agents.escalation import Notifier
from linear_agents.linear import BOT_PREFIX, Comment, Issue, LinearError
from linear_agents.poller import forward_comments, maybe_digest, poll, repo_for, route_escalations
from linear_agents.runtime import Runtime
from linear_agents.supervisor import LaunchError
from linear_agents.tick import tick, tick_lock
from tests.fakes import FakeNotifier, FakeSessions
from tests.paths import EXAMPLE
from tests.test_ledger import make

CFG = load(EXAMPLE)
GOOD = "Do it.\n\n## Acceptance criteria\n\n- works\n"
DEPENDS = "```agent\ndepends_on: [NF-404]\n```\n\n" + GOOD


def issue(key="NF-9", desc=GOOD, labels=("claude", "backend")):
    return Issue(
        f"id-{key}", key, "Title", desc, f"https://l/{key}", "unstarted", labels, None, "t1"
    )


@dataclass
class PollLinear:
    ready: list
    comments: list = field(default_factory=list)
    started: list = field(default_factory=list)
    replies: dict = field(default_factory=dict)
    comment_fails: bool = False

    def ready_issues(self, team, label):
        return self.ready

    def state_types(self, keys):
        if "NF-404" in keys:
            raise LinearError("Entity not found: Issue")
        return {k: "completed" for k in keys}

    def start(self, i):
        self.started.append(i.key)

    def comment(self, issue_id, text):
        if self.comment_fails:
            raise LinearError("down")
        self.comments.append((issue_id, text))

    def comments_since(self, issue_id, after):
        return self.replies.get(issue_id, [])

    def issue(self, key):
        return issue(key)

    def add_label(self, i, name):
        pass


@pytest.fixture
def rt(tmp_cfg, tmp_path, monkeypatch):
    def fake_prepare(repo, root, key):
        path = root / key
        path.mkdir(parents=True, exist_ok=True)
        return path, f"agent/{key.lower()}", "sha0"

    monkeypatch.setattr("linear_agents.poller.prepare_worktree", fake_prepare)
    monkeypatch.setattr("linear_agents.poller.install_hook", lambda *a: None)
    return Runtime(
        cfg=tmp_cfg,
        con=ledger.connect(tmp_cfg.ledger_path),
        linear=PollLinear([]),
        sessions=FakeSessions(),
        notifier=FakeNotifier(),
        now=lambda: 1000.0,
        exe="/opt/linear-agents",
        claude_exe="/opt/claude",
        config_path=tmp_path / "c.toml",
        publish_fn=lambda claim, title, summary: None,
        git_head=lambda p: "sha0",
    )


def test_repo_for_prefers_labels_then_project():
    assert repo_for(issue(), CFG) == "backend"
    proj = Issue("i", "NF-1", "t", "", "u", "unstarted", ("claude",), "backend", "t1")
    assert repo_for(proj, CFG) == "backend"
    assert repo_for(issue(labels=("claude",)), CFG) is None


def test_poll_launches_tester(rt):
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None
    assert c.phase == "test" and c.state == "running" and c.base_sha == "sha0"
    assert c.last_comment_at == "1970-01-01T00:16:40+00:00"
    assert rt.linear.started == ["NF-9"] and rt.sessions.started[0][0] == "NF-9-test"
    assert (state_dir(rt.cfg, "NF-9") / "context.md").exists()


def test_unreadable_dependency_holds_only_that_issue(rt):
    rt.linear.ready = [issue("NF-8", desc=DEPENDS), issue()]
    poll(rt)
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-8") is None
    assert ledger.get_claim(rt.con, "NF-9") is not None
    assert len(rt.linear.comments) == 1 and "NF-404" in rt.linear.comments[0][1]


def test_rerun_ignores_files_from_the_previous_claim(rt):
    d = state_dir(rt.cfg, "NF-9")
    d.mkdir()
    old = json.dumps({"tier": 2, "cause": "x", "summary": "s", "action": "a", "waiting": True})
    (d / "escalations.jsonl").write_text(old + "\n")
    (d / "done.json").write_text(json.dumps({"summary": "the previous claim's"}))
    rt.linear.ready = [issue()]
    poll(rt)
    route_escalations(rt)
    assert rt.notifier.sent == []
    assert not (d / "done.json").exists()


def test_poll_comments_once_then_admits_after_fix(rt):
    rt.linear.ready = [issue(desc="no criteria")]
    poll(rt)
    poll(rt)
    assert len(rt.linear.comments) == 1 and "Acceptance criteria" in rt.linear.comments[0][1]
    rt.linear.ready = [issue()]
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is not None


def test_poll_respects_cap(rt):
    for key in ("NF-1", "NF-2"):
        ledger.insert_claim(rt.con, make(key, ()))
    rt.linear.ready = [issue()]
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is None


def test_forward_only_owner_comments(rt):
    ledger.insert_claim(rt.con, make("NF-1", ()))
    rt.linear.replies = {
        "id-NF-1": [
            Comment("c1", f"{BOT_PREFIX} question", "2026-10-09T10:00:00Z", mine=True),
            Comment("c2", "use option B", "2026-10-09T10:05:00Z", mine=True),
            Comment("c3", "a teammate's aside", "2026-10-09T10:06:00Z", mine=False),
        ]
    }
    rt.sessions.alive_names.add("NF-1-test")
    forward_comments(rt)
    assert rt.sessions.sent == [("NF-1-test", "The owner replied on Linear: use option B")]
    c = ledger.get_claim(rt.con, "NF-1")
    assert c is not None and c.last_comment_at == "2026-10-09T10:06:00Z"


def test_route_escalations_reads_only_new_lines(rt):
    ledger.insert_claim(rt.con, make("NF-1", ()))
    d = state_dir(rt.cfg, "NF-1")
    d.mkdir(parents=True)
    line = json.dumps({"tier": 3, "cause": "c", "summary": "s", "issue_key": "NF-1"})
    (d / "escalations.jsonl").write_text(line + "\nnot json\n")
    route_escalations(rt)
    route_escalations(rt)
    assert [e.cause for e in rt.notifier.sent] == ["NF-1:c", "NF-1:malformed-escalation"]


def test_digest_posts_once_per_day(rt, monkeypatch):
    ledger.add_digest(rt.con, "NF-1", "draft PR ready", 1)
    monkeypatch.setattr("linear_agents.poller._local_hour_and_date", lambda now: (19, "2026-10-09"))
    maybe_digest(rt)
    maybe_digest(rt)
    assert len(rt.linear.comments) == 1 and "draft PR ready" in rt.linear.comments[0][1]


def test_digest_is_kept_when_posting_fails(rt, monkeypatch):
    ledger.add_digest(rt.con, "NF-1", "draft PR ready", 1)
    monkeypatch.setattr("linear_agents.poller._local_hour_and_date", lambda now: (19, "2026-10-09"))
    rt.linear.comment_fails = True
    with pytest.raises(LinearError):
        maybe_digest(rt)
    assert len(ledger.pending_digest(rt.con)) == 1
    assert ledger.meta_get(rt.con, "digest_date") is None


def test_tick_lock_is_exclusive(tmp_path):
    with tick_lock(tmp_path / "t.lock") as first, tick_lock(tmp_path / "t.lock") as second:
        assert first and not second
    with tick_lock(tmp_path / "t.lock") as again:
        assert again


def test_launch_failure_leaves_the_issue_unclaimed(rt):
    rt.cfg.agent_gh_token_file.unlink()
    rt.linear.ready = [issue()]
    with pytest.raises(LaunchError):
        poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is None and rt.linear.started == []


def test_prepare_failure_comments_once_with_stderr(rt, monkeypatch):
    def failing(repo, root, key):
        raise subprocess.CalledProcessError(1, ["./setup"], stderr="setup: npm is missing\n")

    monkeypatch.setattr("linear_agents.poller.prepare_worktree", failing)
    rt.notifier = Notifier(rt.cfg, rt.con, rt.linear, rt.now, push=lambda *a: None)
    rt.linear.ready = [issue()]
    poll(rt)
    poll(rt)
    assert len(rt.linear.comments) == 1 and "setup: npm is missing" in rt.linear.comments[0][1]


def test_tick_without_agent_token_fails_before_any_work(tmp_cfg, tmp_path, monkeypatch):
    for name in ("linear.key", "gh-publish.token"):
        (tmp_path / name).write_text("k\n")
    cfg = replace(
        tmp_cfg,
        linear_key_file=tmp_path / "linear.key",
        publish_gh_token_file=tmp_path / "gh-publish.token",
    )
    cfg.agent_gh_token_file.unlink()
    monkeypatch.setattr("linear_agents.tick._which", lambda name: f"/opt/{name}")
    transports = []
    monkeypatch.setattr("linear_agents.tick.http_transport", transports.append)
    with pytest.raises(RuntimeError, match="gh-agent.token"):
        tick(cfg, tmp_path / "c.toml")
    assert transports == [] and not cfg.ledger_path.exists()
