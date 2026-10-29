import json
import subprocess
from dataclasses import dataclass, field, replace

import pytest

from flocc import ledger
from flocc.config import TRAILER, RepoConfig, load
from flocc.context import state_dir
from flocc.linear import BOT_PREFIX, Comment, Issue, LinearError
from flocc.notice import Notifier
from flocc.poller import (
    clean_scratchpads,
    forward_comments,
    maybe_digest,
    poll,
    reap_worktrees,
    repo_for,
    restart_timed_out,
)
from flocc.runtime import Runtime
from flocc.supervisor import LaunchError
from flocc.tick import tick, tick_lock
from flocc.worktree import git, prepare_worktree, recover_tests
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
    unstarted: list = field(default_factory=list)
    replies: dict = field(default_factory=dict)
    comment_fails: bool = False
    start_fails: bool = False
    deleted: tuple = ()

    def ready_issues(self, team, label):
        return self.ready

    def state_types(self, keys):
        if "NF-404" in keys:
            raise LinearError("Entity not found: Issue")
        return {k: "completed" for k in keys}

    def blocked_issues(self, key):
        return []

    def start(self, i):
        if self.start_fails:
            raise LinearError("down")
        self.started.append(i.key)

    def comment(self, issue_id, text):
        if self.comment_fails:
            raise LinearError("down")
        self.comments.append((issue_id, text))

    def comments_since(self, issue_id, after):
        if issue_id.removeprefix("id-") in self.deleted:
            raise LinearError("Entity not found: Issue")
        return self.replies.get(issue_id, [])

    def issue(self, key):
        if key in self.deleted:
            raise LinearError("Entity not found: Issue")
        return issue(key)

    def unstart(self, i):
        self.unstarted.append(i.key)


@pytest.fixture
def rt(tmp_cfg, tmp_path, monkeypatch):
    def fake_prepare(repo, root, key, setup_timeout_s, shared_uv_cache):
        path = root / key
        path.mkdir(parents=True, exist_ok=True)
        return path, f"agent/{key.lower()}", "sha0"

    monkeypatch.setattr("flocc.poller.prepare_worktree", fake_prepare)
    monkeypatch.setattr("flocc.poller.install_hook", lambda *a, **kw: None)
    monkeypatch.setattr("flocc.poller.recover_tests", lambda *a: None)
    return Runtime(
        cfg=tmp_cfg,
        con=ledger.connect(tmp_cfg.ledger_path),
        linear=PollLinear([]),
        sessions=FakeSessions(),
        notifier=FakeNotifier(),
        now=lambda: 1000.0,
        exe="/opt/flocc",
        claude_exe="/opt/claude",
        config_path=tmp_path / "c.toml",
        publish_fn=lambda claim, title, summary: None,
        git_head=lambda p: "sha0",
        review_diff=lambda c: "",
        comment_pr=lambda c, u, b: None,
        # The real check reads tmp_path's filesystem, and a nearly full /tmp holds every issue.
        free_disk_gb=lambda path: 100.0,
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
    (d / "done.json").write_text(json.dumps({"summary": "the previous claim's"}))
    rt.linear.ready = [issue()]
    poll(rt)
    assert not (d / "done.json").exists()


def test_a_resume_at_the_implementer_keeps_the_amended_tests_record(rt, monkeypatch):
    ledger.remember_tests(rt.con, "NF-9", "sha-old-base", "sha-tests")
    monkeypatch.setattr(
        "flocc.poller.contains",
        lambda path, sha, ref="HEAD": sha == "sha-tests" and ref == "HEAD",
    )
    d = state_dir(rt.cfg, "NF-9")
    d.mkdir()
    (d / "tests-amended.json").write_text('{"t.py": {"reason": "r"}}')
    rt.linear.ready = [issue()]
    poll(rt)
    assert (d / "tests-amended.json").exists()


def test_rerun_on_a_branch_with_the_tests_resumes_at_the_implementer(rt, monkeypatch):
    ledger.remember_tests(rt.con, "NF-9", "sha-old-base", "sha-tests")
    monkeypatch.setattr(
        "flocc.poller.contains",
        lambda path, sha, ref="HEAD": sha == "sha-tests" and ref == "HEAD",
    )
    d = state_dir(rt.cfg, "NF-9")
    d.mkdir()
    (d / "handoff.md").write_text("the previous claim's notes")
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None
    assert (c.phase, c.base_sha, c.tester_sha) == ("impl", "sha-old-base", "sha-tests")
    assert rt.sessions.started[0][0] == "NF-9" and rt.linear.started == ["NF-9"]
    assert not (d / "handoff.md").exists()
    assert (d / "handoff.prev.md").read_text() == "the previous claim's notes"


def test_a_fresh_claim_gets_its_own_review_fix_round(rt):
    ledger.meta_set(rt.con, "review_fix:NF-9", "1")
    ledger.meta_set(rt.con, "review_posted:NF-9:1", "1")
    ledger.meta_set(rt.con, "review_posted:NF-9:2", "1")
    rt.linear.ready = [issue()]
    poll(rt)
    assert not ledger.meta_get(rt.con, "review_fix:NF-9")
    assert not ledger.meta_get(rt.con, "review_posted:NF-9:1")
    assert not ledger.meta_get(rt.con, "review_posted:NF-9:2")


def test_a_fresh_claim_forgets_the_last_run_s_rebase_and_base_hold(rt):
    kept_keys = ("rebase", "rebase_tried", "base_held", "base_released")
    for kept in kept_keys:
        ledger.meta_set(rt.con, f"{kept}:NF-9", "1")
    rt.linear.ready = [issue()]
    poll(rt)
    for kept in kept_keys:
        assert not ledger.meta_get(rt.con, f"{kept}:NF-9")


def test_rerun_without_the_tests_on_the_branch_starts_at_the_tester(rt, monkeypatch):
    ledger.remember_tests(rt.con, "NF-9", "sha-old-base", "sha-tests")
    monkeypatch.setattr("flocc.poller.contains", lambda path, sha, ref="HEAD": False)
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None
    assert (c.phase, c.base_sha, c.tester_sha) == ("test", "sha0", None)
    assert rt.sessions.started[0][0] == "NF-9-test"


def test_rerun_after_the_tests_were_merged_starts_at_the_tester(rt, monkeypatch):
    ledger.remember_tests(rt.con, "NF-9", "sha-old-base", "sha-tests")
    monkeypatch.setattr("flocc.poller.contains", lambda path, sha, ref="HEAD": True)
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None
    assert (c.phase, c.base_sha, c.tester_sha) == ("test", "sha0", None)
    assert ledger.remembered_tests(rt.con, "NF-9") is None


def test_tests_recovered_from_the_branch_are_remembered_and_resume_the_implementer(rt, monkeypatch):
    monkeypatch.setattr("flocc.poller.recover_tests", lambda *a: ("sha-fork", "sha-tests"))
    monkeypatch.setattr("flocc.poller.contains", lambda path, sha, ref="HEAD": ref == "HEAD")
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None
    assert (c.phase, c.base_sha, c.tester_sha) == ("impl", "sha-fork", "sha-tests")
    assert ledger.remembered_tests(rt.con, "NF-9") == ("sha-fork", "sha-tests")


def test_a_rebased_branch_recovers_the_tests_and_updates_the_record(rt, monkeypatch):
    ledger.remember_tests(rt.con, "NF-9", "sha-old-base", "sha-rewritten")
    monkeypatch.setattr("flocc.poller.recover_tests", lambda *a: ("sha-new-base", "sha-tests"))
    monkeypatch.setattr(
        "flocc.poller.contains",
        lambda path, sha, ref="HEAD": sha == "sha-tests" and ref == "HEAD",
    )
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None
    assert (c.phase, c.base_sha, c.tester_sha) == ("impl", "sha-new-base", "sha-tests")
    assert ledger.remembered_tests(rt.con, "NF-9") == ("sha-new-base", "sha-tests")


def test_remembered_tests_are_not_recovered_again(rt, monkeypatch):
    ledger.remember_tests(rt.con, "NF-9", "sha-old-base", "sha-tests")

    def recover(*a):
        raise AssertionError("recovery ran with tests already remembered")

    monkeypatch.setattr("flocc.poller.recover_tests", recover)
    monkeypatch.setattr("flocc.poller.contains", lambda path, sha, ref="HEAD": ref == "HEAD")
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None and c.phase == "impl"


def test_a_branch_from_another_host_resumes_at_the_implementer(rt, origin, tmp_path, monkeypatch):
    monkeypatch.setattr("flocc.poller.prepare_worktree", prepare_worktree)
    monkeypatch.setattr("flocc.poller.recover_tests", recover_tests)
    backend = replace(rt.cfg.repos["backend"], path=origin)
    rt.cfg = replace(rt.cfg, repos={**rt.cfg.repos, "backend": backend})
    other = tmp_path / "other-host"
    subprocess.run(
        ["git", "clone", "-q", str(tmp_path / "origin.git"), str(other)],
        check=True,
        capture_output=True,
    )
    for k, v in (("user.name", "t"), ("user.email", "t@t")):
        git("config", k, v, cwd=other)
    git("checkout", "-qb", "agent/nf-9", cwd=other)
    fork = git("rev-parse", "HEAD", cwd=other)
    (other / "test_x.py").write_text("assert True\n")
    git("add", ".", cwd=other)
    git("commit", "-qm", f"tests\n\n{TRAILER}: NF-9 test", cwd=other)
    tests = git("rev-parse", "HEAD", cwd=other)
    git("push", "-q", "origin", "agent/nf-9", cwd=other)
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None
    assert (c.phase, c.base_sha, c.tester_sha) == ("impl", fork, tests)
    assert ledger.remembered_tests(rt.con, "NF-9") == (fork, tests)


def test_a_branch_rebased_on_the_host_resumes_at_the_implementer(rt, origin, tmp_path, monkeypatch):
    monkeypatch.setattr("flocc.poller.prepare_worktree", prepare_worktree)
    monkeypatch.setattr("flocc.poller.recover_tests", recover_tests)
    backend = replace(rt.cfg.repos["backend"], path=origin)
    rt.cfg = replace(rt.cfg, repos={**rt.cfg.repos, "backend": backend})
    path, _, fork = prepare_worktree(backend, rt.cfg.worktree_root, "NF-9")
    (path / "test_x.py").write_text("assert True\n")
    git("add", ".", cwd=path)
    git("commit", "-qm", f"tests\n\n{TRAILER}: NF-9 test", cwd=path)
    ledger.remember_tests(rt.con, "NF-9", fork, git("rev-parse", "HEAD", cwd=path))
    git("checkout", "-q", "--detach", "main", cwd=origin)
    (origin / "main.txt").write_text("m\n")
    git("add", ".", cwd=origin)
    git("commit", "-qm", "main moved", cwd=origin)
    git("push", "-q", "origin", "HEAD:main", cwd=origin)
    git("fetch", "-q", "origin", "main", cwd=path)
    # Never pushed: origin has no copy of the branch, so only HEAD holds the rebased tests.
    git("rebase", "-q", "origin/main", cwd=path)
    new_base = git("rev-parse", "origin/main", cwd=path)
    new_tests = git("rev-parse", "HEAD", cwd=path)
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None and (c.phase, c.tester_sha) == ("impl", new_tests)
    assert ledger.remembered_tests(rt.con, "NF-9") == (new_base, new_tests)


@pytest.mark.parametrize("state", ["done", "timed_out", "blocked"])
def test_finished_or_blocked_claim_back_in_todo_is_released_and_rerun(rt, monkeypatch, state):
    removed = []
    monkeypatch.setattr("flocc.poller.git", lambda *a, cwd: removed.append(a))
    ledger.insert_claim(rt.con, make("NF-9", state=state))
    rt.linear.ready = [issue()]
    poll(rt)
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None and c.state == "running" and c.started_at == 1000.0
    assert removed == []
    assert [text for _, text in rt.linear.comments] == [
        "moved back to Todo; restarting from a fresh claim."
    ]
    assert rt.linear.started == ["NF-9"]


@pytest.mark.parametrize("phase", ["test", "impl", "review"])
def test_running_claim_in_todo_is_left_alone(rt, phase):
    claim = make("NF-9")
    claim.phase = phase
    ledger.insert_claim(rt.con, claim)
    rt.linear.ready = [issue()]
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") == claim
    assert rt.sessions.killed == [] and rt.sessions.started == []
    assert rt.linear.comments == [] and rt.linear.started == []


def test_rerun_resumes_at_the_implementer_and_clears_the_old_alerts(rt, monkeypatch):
    monkeypatch.setattr(
        "flocc.poller.contains",
        lambda path, sha, ref="HEAD": sha == "sha-tests" and ref == "HEAD",
    )
    ledger.remember_tests(rt.con, "NF-9", "sha-old-base", "sha-tests")
    ledger.insert_claim(rt.con, make("NF-9", state="blocked"))
    ledger.record_alert(rt.con, "NF-9:crash", 2, "NF-9", 1.0)
    ledger.record_alert(rt.con, "NF-90:crash", 2, "NF-90", 1.0)
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None and (c.phase, c.tester_sha) == ("impl", "sha-tests")
    assert not ledger.alert_seen(rt.con, "NF-9:crash", 10, 2.0)
    assert ledger.alert_seen(rt.con, "NF-90:crash", 10, 2.0)


def test_failing_rerun_comment_does_not_abort_the_tick(rt, capsys):
    ledger.insert_claim(rt.con, make("NF-9", state="done"))
    rt.linear.comment_fails = True
    rt.linear.ready = [issue(), issue("NF-8")]
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is not None
    assert "NF-8: held" in capsys.readouterr().err


def test_rerun_comment_waits_for_the_start(rt):
    ledger.insert_claim(rt.con, make("NF-9", state="done"))
    rt.linear.ready = [issue(desc="no criteria")]
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is None
    assert all("restarting" not in text for _, text in rt.linear.comments)


def test_start_failure_kills_the_session_and_drops_the_claim(rt):
    rt.linear.start_fails = True
    rt.linear.ready = [issue()]
    with pytest.raises(LinearError):
        poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is None
    assert rt.sessions.killed == ["NF-9-test"]


def test_poll_comments_once_then_admits_after_fix(rt):
    rt.linear.ready = [issue(desc="no criteria")]
    poll(rt)
    poll(rt)
    assert len(rt.linear.comments) == 1 and "Acceptance criteria" in rt.linear.comments[0][1]
    rt.linear.ready = [issue()]
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is not None


def test_unmapped_issue_gets_one_comment_by_default(rt):
    rt.linear.ready = [issue(labels=("claude",))]
    poll(rt)
    poll(rt)
    assert len(rt.linear.comments) == 1 and "No configured repo" in rt.linear.comments[0][1]
    assert ledger.get_claim(rt.con, "NF-9") is None


def test_skip_unmapped_leaves_other_hosts_issues_alone(rt, capsys):
    rt.cfg = replace(rt.cfg, skip_unmapped=True)
    rt.linear.ready = [issue(labels=("claude", "elsewhere")), issue("NF-10")]
    poll(rt)
    assert rt.linear.comments == [] and capsys.readouterr().err == ""
    assert ledger.get_claim(rt.con, "NF-9") is None
    assert ledger.get_claim(rt.con, "NF-10") is not None


def test_skip_unmapped_says_so_once_when_a_relabelled_rerun_releases_its_claim(rt, capsys):
    rt.cfg = replace(rt.cfg, skip_unmapped=True)
    ledger.insert_claim(rt.con, make("NF-9", state="done"))
    rt.linear.ready = [issue(labels=("claude", "elsewhere"))]
    poll(rt)
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is None
    assert capsys.readouterr().err.count("no mapped repo matches") == 1


def test_poll_respects_cap(rt):
    for key in ("NF-1", "NF-2"):
        ledger.insert_claim(rt.con, make(key))
    rt.linear.ready = [issue()]
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is None


def test_forward_only_owner_comments(rt):
    ledger.insert_claim(rt.con, make("NF-1"))
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


def test_forward_steer_author_comments(rt):
    rt.cfg = replace(rt.cfg, steer_authors=("hermes-id",))
    ledger.insert_claim(rt.con, make("NF-1"))
    rt.linear.replies = {
        "id-NF-1": [
            Comment("c1", "owner says hi", "2026-10-09T10:00:00Z", mine=True),
            Comment("c2", "hermes nudge", "2026-10-09T10:05:00Z", mine=False, author="hermes-id"),
            Comment("c3", "teammate aside", "2026-10-09T10:06:00Z", mine=False, author="other"),
            Comment("c4", f"{BOT_PREFIX} own note", "2026-10-09T10:07:00Z", author="hermes-id"),
        ]
    }
    rt.sessions.alive_names.add("NF-1-test")
    forward_comments(rt)
    assert rt.sessions.sent == [
        ("NF-1-test", "The owner replied on Linear: owner says hi"),
        ("NF-1-test", "A steering agent replied on Linear: hermes nudge"),
    ]
    c = ledger.get_claim(rt.con, "NF-1")
    assert c is not None and c.last_comment_at == "2026-10-09T10:07:00Z"


def test_reply_to_a_dead_session_is_forwarded_after_the_restart(rt):
    ledger.insert_claim(rt.con, make("NF-1"))
    rt.linear.replies = {
        "id-NF-1": [
            Comment("c1", "a teammate's aside", "2026-10-09T10:00:00Z", mine=False),
            Comment("c2", "use option B", "2026-10-09T10:05:00Z", mine=True),
        ]
    }
    forward_comments(rt)
    c = ledger.get_claim(rt.con, "NF-1")
    assert rt.sessions.sent == [] and c is not None
    assert c.last_comment_at == "2026-10-09T10:00:00Z"
    rt.sessions.alive_names.add("NF-1-test")
    forward_comments(rt)
    assert rt.sessions.sent == [("NF-1-test", "The owner replied on Linear: use option B")]


def test_a_new_claim_starts_without_the_last_claims_state(rt):
    d = state_dir(rt.cfg, "NF-9")
    d.mkdir(parents=True)
    names = ("scratchpads", "compacted", "idle", "tests-amended.json")
    for name in names:
        (d / name).write_text("x")
    rt.linear.ready = [issue()]
    poll(rt)
    for name in names:
        assert not (d / name).exists()


def test_digest_posts_once_per_day(rt, monkeypatch):
    ledger.add_digest(rt.con, "NF-1", "draft PR ready", 1)
    monkeypatch.setattr("flocc.poller._local_hour_and_date", lambda now: (19, "2026-10-09"))
    maybe_digest(rt)
    maybe_digest(rt)
    assert len(rt.linear.comments) == 1 and "draft PR ready" in rt.linear.comments[0][1]


def test_digest_is_kept_when_posting_fails(rt, monkeypatch):
    ledger.add_digest(rt.con, "NF-1", "draft PR ready", 1)
    monkeypatch.setattr("flocc.poller._local_hour_and_date", lambda now: (19, "2026-10-09"))
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
    def failing(repo, root, key, setup_timeout_s, shared_uv_cache):
        raise subprocess.CalledProcessError(1, ["./setup"], stderr="setup: npm is missing\n")

    monkeypatch.setattr("flocc.poller.prepare_worktree", failing)
    rt.notifier = Notifier(rt.con, rt.linear, rt.now)
    rt.linear.ready = [issue()]
    poll(rt)
    poll(rt)
    assert len(rt.linear.comments) == 1 and "setup: npm is missing" in rt.linear.comments[0][1]


def test_setup_timeout_is_a_prepare_failure(rt, monkeypatch):
    def slow(repo, root, key, setup_timeout_s, shared_uv_cache):
        raise subprocess.TimeoutExpired(["./setup"], setup_timeout_s, stderr="still installing")

    monkeypatch.setattr("flocc.poller.prepare_worktree", slow)
    rt.linear.ready = [issue()]
    poll(rt)
    [esc] = rt.notifier.sent
    assert esc.cause == "NF-9:prepare" and "still installing" in esc.summary
    assert ledger.get_claim(rt.con, "NF-9") is None


def _broken_main(rt, monkeypatch, at_base="sha-main"):
    calls = []

    def failing(repo, root, key, setup_timeout_s, shared_uv_cache):
        calls.append(key)
        raise subprocess.CalledProcessError(1, ["./setup"], stderr="mise: no version set for go\n")

    monkeypatch.setattr("flocc.poller.prepare_worktree", failing)
    monkeypatch.setattr("flocc.poller.head_at_base", lambda repo, path: at_base)
    monkeypatch.setattr("flocc.poller.origin_base", lambda repo: "sha-main")
    return calls


def test_a_kept_worktree_or_a_git_failure_never_marks_the_repo(rt, monkeypatch):
    from flocc import canary

    _broken_main(rt, monkeypatch)
    (rt.cfg.worktree_root / "NF-9").mkdir(parents=True)

    def fetch_fails(repo, root, key, setup_timeout_s, shared_uv_cache):
        if key == "NF-10":
            raise subprocess.CalledProcessError(128, ["git", "fetch"], stderr="network down")
        raise subprocess.CalledProcessError(1, ["./setup"], stderr="uncommitted junk\n")

    monkeypatch.setattr("flocc.poller.prepare_worktree", fetch_fails)
    rt.linear.ready = [issue("NF-9"), issue("NF-10")]
    poll(rt)
    assert canary.get_mark(rt.con, next(iter(rt.cfg.repos))) is None


def test_a_failed_escalation_leaves_no_silent_hold(rt, monkeypatch):
    from flocc import canary

    _broken_main(rt, monkeypatch)

    def down(notice, issue=None):
        raise LinearError("down")

    rt.notifier.send = down
    rt.linear.ready = [issue("NF-9")]
    with pytest.raises(LinearError):
        poll(rt)
    assert canary.get_mark(rt.con, next(iter(rt.cfg.repos))) is None


def test_setup_failing_on_untouched_main_marks_the_repo_and_holds_its_issues(rt, monkeypatch):
    calls = _broken_main(rt, monkeypatch)
    rt.linear.ready = [issue("NF-9"), issue("NF-10")]
    poll(rt)
    poll(rt)
    assert calls == ["NF-9"]
    [esc] = rt.notifier.sent
    assert esc.issue_key == "NF-9" and esc.cause.endswith("main-broken")
    assert "mise: no version set for go" in esc.summary and "flocc canary" in esc.action
    assert ledger.get_claim(rt.con, "NF-10") is None


def test_a_setup_timeout_on_untouched_main_never_marks_the_repo(rt, monkeypatch):
    from flocc import canary

    _broken_main(rt, monkeypatch)

    def slow(repo, root, key, setup_timeout_s, shared_uv_cache):
        raise subprocess.TimeoutExpired(["./setup"], setup_timeout_s, stderr="still installing")

    monkeypatch.setattr("flocc.poller.prepare_worktree", slow)
    rt.linear.ready = [issue("NF-9")]
    poll(rt)
    assert canary.get_mark(rt.con, next(iter(rt.cfg.repos))) is None
    [esc] = rt.notifier.sent
    assert esc.cause == "NF-9:prepare"


def test_setup_failing_on_the_issues_own_commits_is_the_issues(rt, monkeypatch):
    calls = _broken_main(rt, monkeypatch, at_base=None)
    rt.linear.ready = [issue("NF-9"), issue("NF-10")]
    poll(rt)
    assert calls == ["NF-9", "NF-10"]


def test_a_new_commit_on_main_lets_the_next_issue_try_again(rt, monkeypatch):
    calls = _broken_main(rt, monkeypatch)
    rt.linear.ready = [issue("NF-9")]
    poll(rt)
    monkeypatch.setattr("flocc.poller.origin_base", lambda repo: "sha-fixed")
    poll(rt)
    assert calls == ["NF-9", "NF-9"]


def test_a_dead_remote_keeps_the_hold_without_failing_the_poll(rt, monkeypatch):
    calls = _broken_main(rt, monkeypatch)
    rt.linear.ready = [issue("NF-9")]
    poll(rt)
    monkeypatch.setattr("flocc.poller.origin_base", lambda repo: None)
    poll(rt)
    assert calls == ["NF-9"]


def test_main_broken_marks_even_when_the_issues_prepare_alert_is_recent(rt, monkeypatch):
    calls = _broken_main(rt, monkeypatch, at_base=None)
    rt.linear.ready = [issue("NF-9")]
    poll(rt)
    monkeypatch.setattr("flocc.poller.head_at_base", lambda repo, path: "sha-main")
    poll(rt)
    poll(rt)
    assert calls == ["NF-9", "NF-9"]


def test_tick_pages_once_runtime_setup_failures_repeat(tmp_cfg, tmp_path, monkeypatch):
    for name in ("linear.key", "gh-publish.token"):
        (tmp_path / name).write_text("k\n")
    cfg = replace(
        tmp_cfg,
        linear_key_file=tmp_path / "linear.key",
        publish_gh_token_file=tmp_path / "gh-publish.token",
    )

    def missing(name):
        raise RuntimeError(f"{name} is not on PATH")

    monkeypatch.setattr("flocc.tick._which", missing)
    for _ in range(6):
        with pytest.raises(RuntimeError, match="not on PATH"):
            tick(cfg, tmp_path / "c.toml")
    [(_, key, line)] = ledger.pending_digest(ledger.connect(cfg.ledger_path))
    assert key is None and "not on PATH" in line


def test_tick_pages_once_non_linear_failures_repeat(rt, tmp_path, monkeypatch):
    monkeypatch.setattr("flocc.tick.build_runtime", lambda cfg, path: rt)

    def tmux_down(rt):
        raise RuntimeError("tmux: no server running")

    monkeypatch.setattr("flocc.tick.poll", tmux_down)
    for _ in range(4):
        with pytest.raises(RuntimeError):
            tick(rt.cfg, tmp_path / "c.toml")
    assert rt.notifier.sent == []
    with pytest.raises(RuntimeError):
        tick(rt.cfg, tmp_path / "c.toml")
    [esc] = rt.notifier.sent
    assert esc.issue_key is None and "tmux: no server" in esc.summary
    monkeypatch.setattr("flocc.tick.poll", lambda rt: None)
    assert tick(rt.cfg, tmp_path / "c.toml") == 0
    assert ledger.meta_get(rt.con, "tick_failures") == "0"


def test_tick_linear_error_on_one_claim_still_runs_the_rest(rt, tmp_path, monkeypatch):
    for key in ("NF-1", "NF-2"):
        ledger.insert_claim(rt.con, make(key))
    supervised, polled = [], []

    def supervise(rt, claim):
        if claim.issue_key == "NF-1":
            raise LinearError("Entity not found: Issue")
        supervised.append(claim.issue_key)

    monkeypatch.setattr("flocc.tick.build_runtime", lambda cfg, path: rt)
    monkeypatch.setattr("flocc.tick.supervise", supervise)
    monkeypatch.setattr("flocc.tick.poll", polled.append)
    assert tick(rt.cfg, tmp_path / "c.toml") == 1
    assert supervised == ["NF-2"] and polled == [rt]
    assert ledger.meta_get(rt.con, "linear_failures") == "1"


def test_tick_with_a_deleted_issue_still_polls_and_pages_naming_it(rt, tmp_path, monkeypatch):
    for key in ("NF-1", "NF-2"):
        ledger.insert_claim(rt.con, make(key))
        d = state_dir(rt.cfg, key)
        d.mkdir(parents=True)
    rt.linear.deleted = ("NF-1",)
    rt.sessions.alive_names = {"NF-1-test", "NF-2-test"}
    rt.linear.replies = {"id-NF-2": [Comment("c1", "go", "2026-10-09T10:00:00Z", mine=True)]}
    polled = []
    monkeypatch.setattr("flocc.tick.build_runtime", lambda cfg, path: rt)
    monkeypatch.setattr("flocc.tick.supervise", lambda rt, claim: None)
    monkeypatch.setattr("flocc.tick.poll", polled.append)
    for _ in range(5):
        assert tick(rt.cfg, tmp_path / "c.toml") == 1
    assert len(polled) == 5
    assert rt.sessions.sent[0] == ("NF-2-test", "The owner replied on Linear: go")
    [page] = [e for e in rt.notifier.sent if e.cause == "linear-api"]
    assert "NF-1" in page.summary and "flocc release NF-1" in page.action


def test_tick_without_agent_token_fails_before_any_work(tmp_cfg, tmp_path, monkeypatch):
    for name in ("linear.key", "gh-publish.token"):
        (tmp_path / name).write_text("k\n")
    cfg = replace(
        tmp_cfg,
        linear_key_file=tmp_path / "linear.key",
        publish_gh_token_file=tmp_path / "gh-publish.token",
    )
    cfg.agent_gh_token_file.unlink()
    monkeypatch.setattr("flocc.tick._which", lambda name: f"/opt/{name}")
    transports = []
    monkeypatch.setattr("flocc.tick.http_transport", transports.append)
    with pytest.raises(RuntimeError, match="gh-agent.token"):
        tick(cfg, tmp_path / "c.toml")
    assert transports == []
    con = ledger.connect(cfg.ledger_path)
    assert ledger.live_claims(con) == [] and ledger.meta_get(con, "tick_failures") == "1"


def test_low_disk_holds_a_start(rt):
    rt.free_disk_gb = lambda path: 0.4
    rt.linear.ready = [issue()]
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is None and rt.linear.started == []


def finished(rt, key, state="done"):
    wt = rt.cfg.worktree_root / key
    wt.mkdir()
    ledger.insert_claim(rt.con, make(key, state=state, worktree=str(wt)))
    return wt


@pytest.mark.parametrize(
    "state_type, removed",
    [("completed", True), ("canceled", True), ("unstarted", False), ("started", False)],
)
def test_reap_removes_worktrees_of_closed_issues_only(rt, monkeypatch, state_type, removed):
    wt = finished(rt, "NF-5", state="timed_out")
    rt.linear.state_types = lambda keys: {k: state_type for k in keys}
    monkeypatch.setattr("flocc.poller.has_uncommitted_changes", lambda path: False)
    calls = []
    monkeypatch.setattr("flocc.poller.git", lambda *a, cwd: calls.append(a))
    reap_worktrees(rt)
    assert calls == ([("worktree", "remove", "--force", str(wt))] if removed else [])


def timed_out(rt, monkeypatch, commit_times, state_type="started", key="NF-5"):
    wt = finished(rt, key, state="timed_out")
    ledger.update_claim(rt.con, key, base_sha="sha-base", started_at=1000.0, phase="impl")
    rt.linear.state_types = lambda keys: {k: state_type for k in keys}
    monkeypatch.setattr("flocc.poller.git", lambda *a, cwd: "\n".join(str(t) for t in commit_times))
    return wt


def test_a_timeout_with_new_commits_restarts_once(rt, monkeypatch):
    timed_out(rt, monkeypatch, [1500, 500])
    restart_timed_out(rt)
    assert ledger.get_claim(rt.con, "NF-5") is None
    assert rt.linear.unstarted == ["NF-5"]
    [(_, text)] = rt.linear.comments
    assert text == "stopped at the wall-clock limit with new commits; restarting once"
    # The rerun times out again: it stays as it is.
    finished_again = rt.cfg.worktree_root / "NF-5"
    ledger.insert_claim(rt.con, make("NF-5", state="timed_out", worktree=str(finished_again)))
    ledger.update_claim(rt.con, "NF-5", base_sha="sha-base", started_at=1000.0, phase="impl")
    restart_timed_out(rt)
    assert ledger.get_claim(rt.con, "NF-5") is not None and rt.linear.unstarted == ["NF-5"]


@pytest.mark.parametrize(
    ("times", "state_type"),
    [
        ([500], "started"),
        ([], "started"),
        ([1500], "completed"),
        ([1500], "canceled"),
        ([1500], "unstarted"),
        ([1500], "backlog"),
    ],
)
def test_a_timeout_without_new_commits_or_an_open_issue_stays(rt, monkeypatch, times, state_type):
    timed_out(rt, monkeypatch, times, state_type)
    restart_timed_out(rt)
    assert ledger.get_claim(rt.con, "NF-5") is not None and rt.linear.unstarted == []


@pytest.mark.parametrize("phase", ["test", "review"])
def test_only_implementer_and_fix_timeouts_restart(rt, monkeypatch, phase):
    timed_out(rt, monkeypatch, [1500])
    ledger.update_claim(rt.con, "NF-5", phase=phase)
    restart_timed_out(rt)
    assert ledger.get_claim(rt.con, "NF-5") is not None and rt.linear.unstarted == []


def test_a_fix_timeout_restarts(rt, monkeypatch):
    timed_out(rt, monkeypatch, [1500])
    ledger.update_claim(rt.con, "NF-5", phase="fix")
    restart_timed_out(rt)
    assert ledger.get_claim(rt.con, "NF-5") is None


def test_an_issue_without_the_trigger_label_is_not_restarted(rt, monkeypatch):
    timed_out(rt, monkeypatch, [1500])
    rt.linear.issue = lambda key: issue(key, labels=("backend",))
    restart_timed_out(rt)
    assert ledger.get_claim(rt.con, "NF-5") is not None and rt.linear.unstarted == []


def test_a_blocked_claim_is_not_restarted(rt, monkeypatch):
    timed_out(rt, monkeypatch, [1500])
    ledger.update_claim(rt.con, "NF-5", state="blocked")
    restart_timed_out(rt)
    assert ledger.get_claim(rt.con, "NF-5") is not None


def test_reap_leaves_live_claims_and_skips_missing_worktrees(rt, monkeypatch):
    for key, state in (("NF-1", "running"), ("NF-2", "blocked")):
        (rt.cfg.worktree_root / key).mkdir()
        ledger.insert_claim(
            rt.con, make(key, state=state, worktree=str(rt.cfg.worktree_root / key))
        )
    finished(rt, "NF-3").rmdir()
    asked = []
    rt.linear.state_types = lambda keys: asked.append(keys) or {k: "completed" for k in keys}
    calls = []
    monkeypatch.setattr("flocc.poller.git", lambda *a, cwd: calls.append(a))
    reap_worktrees(rt)
    assert calls == [] and asked == []


def test_reap_survives_a_linear_error_and_a_failed_removal(rt, monkeypatch, capsys):
    finished(rt, "NF-5")
    finished(rt, "NF-6")

    def state_types(keys):
        if keys == ["NF-5"]:
            raise LinearError("rate limited")
        return {"NF-6": "completed"}

    def failing(*a, cwd):
        raise subprocess.CalledProcessError(1, "git")

    rt.linear.state_types = state_types
    monkeypatch.setattr("flocc.poller.git", failing)
    reap_worktrees(rt)
    err = capsys.readouterr().err
    assert "NF-5 state not read" in err and "NF-6" in err


def test_reap_treats_a_deleted_issue_as_closed(rt, monkeypatch):
    wt = finished(rt, "NF-5")

    def gone(keys):
        raise LinearError("Entity not found: Issue")

    rt.linear.state_types = gone
    calls = []
    monkeypatch.setattr("flocc.poller.git", lambda *a, cwd: calls.append(a))
    reap_worktrees(rt)
    assert calls == [("worktree", "remove", "--force", str(wt))]


@pytest.mark.parametrize(
    "claim_state, dirty, removed",
    [
        ("timed_out", True, False),
        ("timed_out", False, True),
        ("done", True, True),
    ],
)
def test_reap_keeps_a_timed_out_worktree_with_uncommitted_changes(
    rt, monkeypatch, capsys, claim_state, dirty, removed
):
    finished(rt, "NF-5", state=claim_state)
    rt.linear.state_types = lambda keys: {k: "completed" for k in keys}
    monkeypatch.setattr("flocc.poller.has_uncommitted_changes", lambda path: dirty)
    calls = []
    monkeypatch.setattr("flocc.poller.git", lambda *a, cwd: calls.append(a))
    reap_worktrees(rt)
    assert bool(calls) is removed
    assert ("uncommitted" in capsys.readouterr().err) is (not removed)
    reap_worktrees(rt)
    assert "uncommitted" not in capsys.readouterr().err


def test_reap_keeps_a_timed_out_worktree_holding_only_an_untracked_file(origin, rt):
    path, _, _ = prepare_worktree(RepoConfig(origin), rt.cfg.worktree_root, "NF-5")
    (path / "new.py").write_text("work\n")
    ledger.insert_claim(rt.con, make("NF-5", state="timed_out", worktree=str(path)))
    rt.cfg.repos["backend"] = RepoConfig(origin)
    rt.linear.state_types = lambda keys: {k: "completed" for k in keys}
    reap_worktrees(rt)
    assert (path / "new.py").exists()
    ledger.update_claim(rt.con, "NF-5", state="done")
    reap_worktrees(rt)
    assert not path.exists()


def test_reap_clears_caches_a_killed_setup_left(rt):
    stale = rt.cfg.worktree_root / ".uv-cache-NF-1"
    (stale / "wheels").mkdir(parents=True)
    shared = rt.cfg.worktree_root / ".uv-cache"
    (shared / "wheels").mkdir(parents=True)
    reap_worktrees(rt)
    assert not stale.exists()
    assert shared.exists()


def test_reap_removes_stray_canary_dirs_and_prunes_the_repos(rt, monkeypatch):
    stray = rt.cfg.worktree_root / ".canary-x"
    stray.mkdir(parents=True)
    (stray / "file").write_text("x")
    ran = []
    next(iter(rt.cfg.repos.values())).path.mkdir(parents=True, exist_ok=True)
    monkeypatch.setattr(
        "flocc.poller.subprocess.run", lambda argv, **kw: ran.append((argv, kw["cwd"]))
    )
    reap_worktrees(rt)
    assert not stray.exists()
    paths = {repo.path for repo in rt.cfg.repos.values() if repo.path.exists()}
    assert paths and {cwd for _, cwd in ran} == paths
    assert all(argv == ["git", "worktree", "prune"] for argv, _ in ran)


def test_reap_prune_failure_is_tolerated(rt, monkeypatch):
    def broken(argv, **kw):
        raise OSError("no git")

    monkeypatch.setattr("flocc.poller.subprocess.run", broken)
    reap_worktrees(rt)


def test_reap_does_not_read_a_wrong_workspace_error_as_a_deleted_issue(rt, monkeypatch):
    finished(rt, "NF-5")

    def wrong(keys):
        raise LinearError("Team not found")

    rt.linear.state_types = wrong
    calls = []
    monkeypatch.setattr("flocc.poller.git", lambda *a, cwd: calls.append(a))
    reap_worktrees(rt)
    assert calls == []


def pads(rt, key, lines):
    d = state_dir(rt.cfg, key)
    d.mkdir(parents=True, exist_ok=True)
    (d / "scratchpads").write_text("".join(f"{x}\n" for x in lines))
    return d


def test_cleanup_removes_a_finished_runs_scratchpads_only(rt, tmp_path, monkeypatch):
    root = tmp_path / "claude-1000"
    monkeypatch.setattr("flocc.poller.scratch_root", lambda: root)
    done_pad = root / "proj" / "s1" / "scratchpad"
    live_pad = root / "proj" / "s2" / "scratchpad"
    done_pad.mkdir(parents=True)
    live_pad.mkdir(parents=True)
    ledger.insert_claim(rt.con, make("NF-1", state="blocked"))
    ledger.insert_claim(rt.con, make("NF-2"))
    d1 = pads(rt, "NF-1", [done_pad])
    pads(rt, "NF-2", [live_pad])
    clean_scratchpads(rt)
    assert not done_pad.exists() and live_pad.exists()
    assert not (d1 / "scratchpads").exists()


def test_cleanup_only_removes_full_session_paths(rt, tmp_path, monkeypatch):
    root = tmp_path / "claude-1000"
    monkeypatch.setattr("flocc.poller.scratch_root", lambda: root)
    project = root / "proj"
    (project / "s1" / "scratchpad").mkdir(parents=True)
    outside = tmp_path / "keep"
    outside.mkdir()
    ledger.insert_claim(rt.con, make("NF-1", state="done"))
    (project / "s2" / "scratchpad").mkdir(parents=True)
    listed = [root, project, project / "s1", outside, "", "/", f"{project}/../.."]
    pads(rt, "NF-1", [*listed, project / "s2" / "scratchpad"])
    clean_scratchpads(rt)
    assert root.exists() and (project / "s1" / "scratchpad").exists() and outside.exists()
    assert not (project / "s2" / "scratchpad").exists()


def test_cleanup_does_not_follow_links(rt, tmp_path, monkeypatch):
    root = tmp_path / "claude-1000"
    monkeypatch.setattr("flocc.poller.scratch_root", lambda: root)
    other = root / "proj" / "s2" / "scratchpad"
    other.mkdir(parents=True)
    outside = tmp_path / "keep"
    outside.mkdir()
    s1 = root / "proj" / "s1"
    s1.mkdir(parents=True)
    in_tree = s1 / "scratchpad"
    in_tree.symlink_to(other)
    out_tree = s1 / "elsewhere"
    out_tree.symlink_to(outside)
    via_parent = s1 / "up"
    via_parent.symlink_to(root / "proj" / "s2")
    ledger.insert_claim(rt.con, make("NF-1", state="done"))
    pads(rt, "NF-1", [in_tree, out_tree, via_parent / "scratchpad"])
    clean_scratchpads(rt)
    assert other.exists() and outside.exists()


def test_cleanup_covers_a_listing_with_no_claim(rt, tmp_path, monkeypatch):
    root = tmp_path / "claude-1000"
    monkeypatch.setattr("flocc.poller.scratch_root", lambda: root)
    pad = root / "proj" / "s1" / "scratchpad"
    pad.mkdir(parents=True)
    d = pads(rt, "NF-7", [pad])
    clean_scratchpads(rt)
    assert not pad.exists() and not (d / "scratchpads").exists()


def test_cleanup_skips_an_unreadable_listing(rt, tmp_path, monkeypatch, capsys):
    root = tmp_path / "claude-1000"
    monkeypatch.setattr("flocc.poller.scratch_root", lambda: root)
    pad = root / "proj" / "s1" / "scratchpad"
    pad.mkdir(parents=True)
    (state_dir(rt.cfg, "NF-1") / "scratchpads").mkdir(parents=True)
    bad = state_dir(rt.cfg, "NF-2")
    bad.mkdir(parents=True)
    (bad / "scratchpads").write_bytes(b"\xff\xfe\n")
    pads(rt, "NF-3", [pad])
    clean_scratchpads(rt)
    assert not pad.exists()
    assert capsys.readouterr().err.count("not cleaned") == 2
