import json
import subprocess
from dataclasses import dataclass, field, replace

import pytest

from flocc import events, host, ledger
from flocc.config import TRAILER, load
from flocc.context import state_dir
from flocc.host import Reading
from flocc.linear import BOT_PREFIX, Comment, Issue, LinearError
from flocc.notice import Notifier
from flocc.poller import (
    forward_comments,
    maybe_daily,
    poll,
    reap_worktrees,
    release_claim,
    repo_for,
    restart_timed_out,
)
from flocc.publisher import PublishResult
from flocc.runtime import Runtime
from flocc.supervisor import LaunchError, supervise
from flocc.tick import tick, tick_lock
from flocc.worktree import (
    BranchDiverged,
    fetch_agent_branch,
    git,
    prepare_worktree,
    recover_tests,
)
from tests.fakes import FakeAgent, FakeNotifier, FakeSessions
from tests.paths import EXAMPLE
from tests.test_ledger import make

CFG = load(EXAMPLE)
GOOD = "Do it.\n\nRun: tester, executor, reviewer\n\n## Acceptance criteria\n\n- works\n"
PLAIN = "Do it.\n\n## Acceptance criteria\n\n- works\n"
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

    monkeypatch.setattr("flocc.poller.recover_tests", lambda *a: None)
    monkeypatch.setattr("flocc.poller.fetch_agent_branch", lambda *a: "sha0")
    return Runtime(
        agent=FakeAgent(tmp_cfg, fake_prepare),
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


def test_the_default_sequence_starts_at_the_executor_with_no_tester(rt):
    rt.linear.ready = [issue(desc=PLAIN)]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None and c.sequence == ["executor", "reviewer"]
    assert (c.phase, c.tester_sha) == ("impl", None) and rt.sessions.started[0][0] == "NF-9"


def test_a_start_refreshes_the_service_clones_base(rt, monkeypatch):
    seen = []
    monkeypatch.setattr("flocc.poller.origin_base", lambda repo: seen.append(repo))
    rt.linear.ready = [issue(desc=PLAIN)]
    poll(rt)
    assert seen == [next(iter(rt.cfg.repos.values()))]


def test_a_run_line_is_recorded_on_the_claim(rt):
    rt.linear.ready = [issue(desc=PLAIN.replace("Do it.", "Do it.\n\nRun: tester, executor"))]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None and c.sequence == ["tester", "executor"] and c.phase == "test"


def test_an_unknown_archetype_skips_the_issue_with_one_comment(rt):
    rt.linear.ready = [issue(desc=PLAIN + "\nRun: executor, planner\n")]
    poll(rt)
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is None
    (comment,) = rt.linear.comments
    assert "unknown archetype: planner" in comment[1] and comment[1].startswith("Skipping:")


def test_unreadable_dependency_holds_only_that_issue(rt):
    rt.linear.ready = [issue("NF-8", desc=DEPENDS), issue()]
    poll(rt)
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-8") is None
    assert ledger.get_claim(rt.con, "NF-9") is not None
    assert len(rt.linear.comments) == 1 and "NF-404" in rt.linear.comments[0][1]


@pytest.mark.parametrize("line", ["paths: src/**", "allow: Agent"])
def test_a_retired_agent_block_key_skips_the_issue_with_one_comment(rt, line):
    rt.linear.ready = [issue(desc=GOOD + f"\n```agent\n{line}\n```\n")]
    poll(rt)
    poll(rt)
    assert len(rt.linear.comments) == 1
    assert rt.linear.comments[0][1].startswith("Skipping: agent block: unknown keys")
    assert ledger.get_claim(rt.con, "NF-9") is None


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
        lambda path, sha, ref="": sha == "sha-tests" and ref == "refs/flocc/NF-9",
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
        lambda path, sha, ref="": sha == "sha-tests" and ref == "refs/flocc/NF-9",
    )
    d = state_dir(rt.cfg, "NF-9")
    d.mkdir()
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None
    assert (c.phase, c.base_sha, c.tester_sha) == ("impl", "sha-old-base", "sha-tests")
    assert rt.sessions.started[0][0] == "NF-9" and rt.linear.started == ["NF-9"]


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
    monkeypatch.setattr("flocc.poller.contains", lambda path, sha, ref="": False)
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None
    assert (c.phase, c.base_sha, c.tester_sha) == ("test", "sha0", None)
    assert rt.sessions.started[0][0] == "NF-9-test"


def test_rerun_after_the_tests_were_merged_starts_at_the_tester(rt, monkeypatch):
    ledger.remember_tests(rt.con, "NF-9", "sha-old-base", "sha-tests")
    monkeypatch.setattr("flocc.poller.contains", lambda path, sha, ref="": True)
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None
    assert (c.phase, c.base_sha, c.tester_sha) == ("test", "sha0", None)
    assert ledger.remembered_tests(rt.con, "NF-9") is None


def test_tests_recovered_from_the_branch_are_remembered_and_resume_the_implementer(rt, monkeypatch):
    monkeypatch.setattr("flocc.poller.recover_tests", lambda *a: ("sha-fork", "sha-tests"))
    monkeypatch.setattr("flocc.poller.contains", lambda path, sha, ref="": ref == "refs/flocc/NF-9")
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
        lambda path, sha, ref="": sha == "sha-tests" and ref == "refs/flocc/NF-9",
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
    monkeypatch.setattr("flocc.poller.contains", lambda path, sha, ref="": ref == "refs/flocc/NF-9")
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None and c.phase == "impl"


def test_a_branch_from_another_host_resumes_at_the_implementer(rt, origin, tmp_path, monkeypatch):
    rt.agent.prepare_fn = prepare_worktree
    monkeypatch.setattr("flocc.poller.recover_tests", recover_tests)
    monkeypatch.setattr("flocc.poller.fetch_agent_branch", fetch_agent_branch)
    backend = replace(rt.cfg.repos["backend"], path=origin)
    rt.cfg = replace(rt.cfg, repos={**rt.cfg.repos, "backend": backend})
    rt.agent.cfg = rt.cfg
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
    rt.agent.prepare_fn = prepare_worktree
    monkeypatch.setattr("flocc.poller.recover_tests", recover_tests)
    monkeypatch.setattr("flocc.poller.fetch_agent_branch", fetch_agent_branch)
    backend = replace(rt.cfg.repos["backend"], path=origin)
    rt.cfg = replace(rt.cfg, repos={**rt.cfg.repos, "backend": backend})
    rt.agent.cfg = rt.cfg
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
def test_finished_or_blocked_claim_back_in_todo_is_released_and_rerun(rt, state):
    ledger.insert_claim(rt.con, make("NF-9", state=state))
    rt.linear.ready = [issue()]
    poll(rt)
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None and c.state == "running" and c.started_at == 1000.0
    assert rt.agent.removed == []
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
        lambda path, sha, ref="": sha == "sha-tests" and ref == "refs/flocc/NF-9",
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


RERUN_REPLIES = [
    Comment("c1", "fix the query guard", "2026-10-09T10:05:00Z", mine=True),
    Comment("c2", "a teammate's aside", "2026-10-09T10:06:00Z", mine=False),
    Comment("c3", f"{BOT_PREFIX} released", "2026-10-09T10:07:00Z", mine=True),
]


def past_claim(state="done"):
    old = make("NF-9", state=state)
    old.last_comment_at = "2026-10-09T10:00:00Z"
    return old


@pytest.mark.parametrize("release", ["todo", "flocc release"])
def test_a_rerun_starts_with_the_owners_comments_since_the_last_claim(rt, release):
    old = past_claim()
    ledger.insert_claim(rt.con, old)
    if release == "flocc release":
        release_claim(rt, old, keep_worktree=True)
    rt.linear.replies = {"id-NF-9": RERUN_REPLIES}
    rt.linear.ready = [issue()]
    poll(rt)
    text = (state_dir(rt.cfg, "NF-9") / "context.md").read_text()
    assert "The owner: fix the query guard" in text
    assert "aside" not in text and "released" not in text
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None and c.last_comment_at == "2026-10-09T10:07:00Z"


def test_a_held_rerun_keeps_the_comments_for_its_start(rt):
    ledger.insert_claim(rt.con, past_claim("blocked"))
    for key in ("NF-1", "NF-2"):
        ledger.insert_claim(rt.con, make(key))
    rt.linear.replies = {"id-NF-9": RERUN_REPLIES}
    rt.linear.ready = [issue()]
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is None
    for key in ("NF-1", "NF-2"):
        ledger.delete_claim(rt.con, key)
    poll(rt)
    text = (state_dir(rt.cfg, "NF-9") / "context.md").read_text()
    assert "The owner: fix the query guard" in text


def test_a_second_start_does_not_repeat_the_comments(rt):
    ledger.insert_claim(rt.con, past_claim())
    rt.linear.replies = {"id-NF-9": RERUN_REPLIES}
    rt.linear.ready = [issue()]
    poll(rt)
    ledger.delete_claim(rt.con, "NF-9")
    poll(rt)
    text = (state_dir(rt.cfg, "NF-9") / "context.md").read_text()
    assert "fix the query guard" not in text


def test_a_rerun_waits_when_its_comments_cannot_be_read(rt, capsys):
    ledger.insert_claim(rt.con, past_claim())
    rt.linear.deleted = ("NF-9",)
    rt.linear.ready = [issue()]
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is None
    assert "comments not read" in capsys.readouterr().err
    rt.linear.deleted = ()
    rt.linear.replies = {"id-NF-9": RERUN_REPLIES}
    poll(rt)
    text = (state_dir(rt.cfg, "NF-9") / "context.md").read_text()
    assert "The owner: fix the query guard" in text


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


def test_a_new_issue_starts_in_the_tick_that_finds_another_check_running(rt):
    claim = make("NF-1")
    claim.phase, claim.sequence, claim.base_sha = "impl", ["executor"], "sha0"
    ledger.insert_claim(rt.con, claim)
    d = state_dir(rt.cfg, "NF-1")
    d.mkdir(parents=True, exist_ok=True)
    (d / "done.json").write_text(json.dumps({"summary": "ok"}))
    rt.publish_fn = lambda c, t, s, **kw: PublishResult("check_running")
    rt.linear.ready = [issue(desc=PLAIN)]
    supervise(rt, claim)
    poll(rt)
    nf1 = ledger.get_claim(rt.con, "NF-1")
    assert nf1 is not None and nf1.state == "running"
    assert ledger.get_claim(rt.con, "NF-9") is not None


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
    names = ("tests-amended.json", "last-summary.md")
    for name in names:
        (d / name).write_text("x")
    rt.linear.ready = [issue()]
    poll(rt)
    for name in names:
        assert not (d / name).exists()


def test_the_daily_event_is_emitted_once_per_day(rt, monkeypatch):
    monkeypatch.setattr("flocc.poller._local_hour_and_date", lambda now: (19, "2026-10-09"))
    maybe_daily(rt)
    maybe_daily(rt)
    assert [(e.kind, e.detail) for e in events.unacked(rt.con)] == [("daily", "2026-10-09")]
    assert rt.linear.comments == []


def test_no_daily_event_before_the_digest_hour(rt, monkeypatch):
    monkeypatch.setattr("flocc.poller._local_hour_and_date", lambda now: (7, "2026-10-09"))
    maybe_daily(rt)
    assert events.unacked(rt.con) == []


def test_a_bad_run_line_is_one_bad_sequence_event(rt):
    bad = "Run: planner, executor\n\n## Acceptance criteria\n\n- works\n"
    rt.linear.ready = [issue(desc=bad)]
    poll(rt)
    poll(rt)
    [e] = events.unacked(rt.con)
    assert e.kind == "bad-sequence" and e.issue_key == "NF-9" and "planner" in e.detail


def test_linear_is_a_service_error_after_three_failed_ticks(rt):
    from flocc.tick import LINEAR_FAILS_BEFORE_EVENT, _page_after_repeats

    for _ in range(LINEAR_FAILS_BEFORE_EVENT):
        _page_after_repeats(
            rt.con,
            rt.notifier,
            "linear_failures",
            "linear-api",
            "Linear",
            LinearError("down"),
            "Check the key",
            LINEAR_FAILS_BEFORE_EVENT,
        )
    [page] = rt.notifier.sent
    assert page.event == "service-error" and "3 ticks" in page.summary


def test_a_held_issue_is_not_started(rt, capsys):
    ledger.add_hold(rt.con, "NF-9", "waiting on NF-2", 1.0)
    rt.linear.ready = [issue()]
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is None and rt.sessions.started == []
    assert "held by cardinal: waiting on NF-2" in capsys.readouterr().err


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

    rt.agent.prepare_fn = failing
    rt.notifier = Notifier(rt.con, rt.linear, rt.now)
    rt.linear.ready = [issue()]
    poll(rt)
    poll(rt)
    assert len(rt.linear.comments) == 1 and "setup: npm is missing" in rt.linear.comments[0][1]


def test_prepare_action_blames_the_repo_for_setup_and_flocc_for_the_rest(rt):
    def failing(repo, root, key, setup_timeout_s, shared_uv_cache):
        if key == "NF-9":
            raise subprocess.CalledProcessError(1, ["./setup"], stderr="npm is missing")
        raise subprocess.CalledProcessError(128, ["git", "fetch"], stderr="not a repository")

    rt.agent.prepare_fn = failing
    rt.linear.ready = [issue("NF-9"), issue("NF-10")]
    poll(rt)
    actions = {n.issue_key: n.action for n in rt.notifier.sent}
    assert actions["NF-9"] == "Fix the repo or ./setup"
    assert "flocc error" in actions["NF-10"] and "flocc logs" in actions["NF-10"]


@pytest.mark.parametrize(
    "exc",
    [
        subprocess.TimeoutExpired(["./setup"], 900),
        PermissionError(13, "Permission denied", "./setup"),
        BranchDiverged("agent/nf-9 diverged"),
    ],
)
def test_prepare_action_blames_the_repo_for_a_slow_or_unrunnable_setup(rt, exc):
    def failing(repo, root, key, setup_timeout_s, shared_uv_cache):
        raise exc

    rt.agent.prepare_fn = failing
    rt.linear.ready = [issue("NF-9")]
    poll(rt)
    [esc] = rt.notifier.sent
    assert esc.action == "Fix the repo or ./setup"


def test_a_repeating_tick_failure_is_one_capped_line_without_url_credentials(
    rt, tmp_path, monkeypatch
):
    monkeypatch.setattr("flocc.tick.build_runtime", lambda cfg, path: rt)

    def broken(rt):
        raise RuntimeError("clone https://x-token:s3cret@github.com/o/r failed\n" + "y" * 400)

    monkeypatch.setattr("flocc.tick.poll", broken)
    with pytest.raises(RuntimeError):
        tick(rt.cfg, tmp_path / "c.toml")
    [esc] = rt.notifier.sent
    assert "s3cret" not in esc.summary and "://***@github.com" in esc.summary
    assert "\n" not in esc.summary and len(esc.summary) < 300


def test_setup_timeout_is_a_prepare_failure(rt, monkeypatch):
    def slow(repo, root, key, setup_timeout_s, shared_uv_cache):
        raise subprocess.TimeoutExpired(["./setup"], setup_timeout_s, stderr="still installing")

    rt.agent.prepare_fn = slow
    rt.linear.ready = [issue()]
    poll(rt)
    [esc] = rt.notifier.sent
    assert esc.cause == "NF-9:prepare" and "still installing" in esc.summary
    assert ledger.get_claim(rt.con, "NF-9") is None


def _broken_main(rt, monkeypatch, at_base="sha-main"):
    calls = []

    rt.agent.at_base = at_base

    def failing(repo, root, key, setup_timeout_s, shared_uv_cache):
        calls.append(key)
        exc = subprocess.CalledProcessError(1, ["./setup"], stderr="mise: no version set for go\n")
        exc.__dict__["at_base"] = rt.agent.at_base
        raise exc

    rt.agent.prepare_fn = failing
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

    rt.agent.prepare_fn = fetch_fails
    rt.linear.ready = [issue("NF-9"), issue("NF-10")]
    poll(rt)
    assert canary.get_mark(rt.con, next(iter(rt.cfg.repos))) is None


def test_a_failed_notice_leaves_no_silent_hold(rt, monkeypatch):
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

    rt.agent.prepare_fn = slow
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
    rt.agent.at_base = "sha-main"
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

    def broken(_key):
        raise RuntimeError("agent unreachable")

    monkeypatch.setattr("flocc.tick.http_transport", broken)
    for _ in range(6):
        with pytest.raises(RuntimeError, match="agent unreachable"):
            tick(cfg, tmp_path / "c.toml")
    [(_, key, line)] = ledger.pending_digest(ledger.connect(cfg.ledger_path))
    assert key is None and "agent unreachable" in line


def test_tick_pages_once_non_linear_failures_repeat(rt, tmp_path, monkeypatch):
    monkeypatch.setattr("flocc.tick.build_runtime", lambda cfg, path: rt)

    def tmux_down(rt):
        raise RuntimeError("tmux: no server running")

    monkeypatch.setattr("flocc.tick.poll", tmux_down)
    for _ in range(5):
        with pytest.raises(RuntimeError):
            tick(rt.cfg, tmp_path / "c.toml")
    [esc] = rt.notifier.sent
    assert esc.issue_key is None and "tmux: no server" in esc.summary
    assert esc.event == "service-error"
    monkeypatch.setattr("flocc.tick.poll", lambda rt: None)
    assert tick(rt.cfg, tmp_path / "c.toml") == 0
    assert ledger.meta_get(rt.con, "tick_failures") == "0"


def test_supervisor_error_event_names_the_exception(rt, tmp_path, monkeypatch):
    ledger.insert_claim(rt.con, make("NF-1"))

    def supervise(rt, claim):
        raise RuntimeError("first   line " + "x" * 300 + "\nsecond line")

    monkeypatch.setattr("flocc.tick.build_runtime", lambda cfg, path: rt)
    monkeypatch.setattr("flocc.tick.supervise", supervise)
    monkeypatch.setattr("flocc.tick.poll", lambda rt: None)
    tick(rt.cfg, tmp_path / "c.toml")
    [esc] = rt.notifier.sent
    assert esc.event == "service-error" and esc.issue_key == "NF-1"
    assert "(RuntimeError: first line x" in esc.summary and "\n" not in esc.summary
    assert "second" not in esc.summary and len(esc.summary) < 300


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


FULL_TMP = Reading(29 * 2**30 // 10, 3 * 2**30, 4 * 2**30, 0)


def test_the_tick_opens_a_host_episode_before_it_supervises(rt, tmp_path, monkeypatch):
    ledger.insert_claim(rt.con, make("NF-1"))
    seen = []
    monkeypatch.setattr("flocc.host.read", lambda: FULL_TMP)
    monkeypatch.setattr("flocc.tick.build_runtime", lambda cfg, path: rt)
    monkeypatch.setattr(
        "flocc.tick.supervise", lambda rt, claim: seen.append(host.under_pressure(rt.con))
    )
    monkeypatch.setattr("flocc.tick.poll", lambda rt: None)
    tick(rt.cfg, tmp_path / "c.toml")
    assert seen == [True]
    assert [e.kind for e in events.unacked(rt.con) if e.kind == "host-pressure"] == [
        "host-pressure"
    ]


def test_a_host_that_cannot_be_measured_does_not_stop_the_tick(rt, tmp_path, monkeypatch):
    def unreadable():
        raise OSError("no /proc/meminfo")

    monkeypatch.setattr("flocc.host.read", unreadable)
    monkeypatch.setattr("flocc.tick.build_runtime", lambda cfg, path: rt)
    monkeypatch.setattr("flocc.tick.poll", lambda rt: None)
    assert tick(rt.cfg, tmp_path / "c.toml") == 0


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


def test_host_pressure_holds_a_start(rt, capsys):
    host.open_episode(rt.con, 1.0, "MemAvailable 0.6 GiB")
    rt.linear.ready = [issue()]
    poll(rt)
    assert ledger.get_claim(rt.con, "NF-9") is None and rt.linear.started == []
    assert "host under pressure: MemAvailable 0.6 GiB" in capsys.readouterr().err


def finished(rt, key, state="done"):
    wt = rt.cfg.worktree_root / key
    wt.mkdir()
    ledger.insert_claim(rt.con, make(key, state=state, worktree=str(wt)))
    return wt


@pytest.mark.parametrize(
    "state_type, removed",
    [("completed", True), ("canceled", True), ("unstarted", False), ("started", False)],
)
def test_reap_removes_worktrees_of_closed_issues_only(rt, state_type, removed):
    finished(rt, "NF-5", state="timed_out")
    rt.linear.state_types = lambda keys: {k: state_type for k in keys}
    reap_worktrees(rt)
    assert rt.agent.removed == ([("NF-5", True)] if removed else [])
    assert (ledger.get_claim(rt.con, "NF-5") is None) is removed


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


def test_a_held_timed_out_run_is_not_restarted(rt, monkeypatch):
    timed_out(rt, monkeypatch, [1500, 500])
    ledger.add_hold(rt.con, "NF-5", "owner said wait", 1.0)
    restart_timed_out(rt)
    assert ledger.get_claim(rt.con, "NF-5") is not None and rt.linear.unstarted == []


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


def test_reap_leaves_live_claims_alone(rt):
    for key, state in (("NF-1", "running"), ("NF-2", "blocked")):
        (rt.cfg.worktree_root / key).mkdir()
        ledger.insert_claim(
            rt.con, make(key, state=state, worktree=str(rt.cfg.worktree_root / key))
        )
    asked = []
    rt.linear.state_types = lambda keys: asked.append(keys) or {k: "started" for k in keys}
    reap_worktrees(rt)
    assert rt.agent.removed == [] and asked == []


def test_reap_survives_a_linear_error_and_a_failed_removal(rt, capsys):
    finished(rt, "NF-5")
    finished(rt, "NF-6")

    def state_types(keys):
        if keys == ["NF-5"]:
            raise LinearError("rate limited")
        return {"NF-6": "completed"}

    def failing(key, keep_dirty=False):
        raise subprocess.CalledProcessError(1, "sudo")

    rt.linear.state_types = state_types
    rt.agent.remove = failing
    reap_worktrees(rt)
    err = capsys.readouterr().err
    assert "NF-5 state not read" in err and "NF-6" in err
    assert ledger.get_claim(rt.con, "NF-6") is not None


def test_reap_treats_a_deleted_issue_as_closed(rt):
    finished(rt, "NF-5")

    def gone(keys):
        raise LinearError("Entity not found: Issue")

    rt.linear.state_types = gone
    reap_worktrees(rt)
    assert rt.agent.removed == [("NF-5", False)]


@pytest.mark.parametrize(
    "claim_state, dirty, removed",
    [
        ("timed_out", True, False),
        ("timed_out", False, True),
        ("done", True, True),
    ],
)
def test_reap_keeps_a_timed_out_worktree_with_uncommitted_changes(
    rt, capsys, claim_state, dirty, removed
):
    finished(rt, "NF-5", state=claim_state)
    rt.linear.state_types = lambda keys: {k: "completed" for k in keys}

    def remove(key, keep_dirty=False):
        kept = keep_dirty and dirty
        return {"removed": not kept, "dirty": kept}

    rt.agent.remove = remove
    reap_worktrees(rt)
    assert (ledger.get_claim(rt.con, "NF-5") is None) is removed
    assert ("uncommitted" in capsys.readouterr().err) is (not removed)
    reap_worktrees(rt)
    assert "uncommitted" not in capsys.readouterr().err


def test_reap_does_not_read_a_wrong_workspace_error_as_a_deleted_issue(rt):
    finished(rt, "NF-5")

    def wrong(keys):
        raise LinearError("Team not found")

    rt.linear.state_types = wrong
    reap_worktrees(rt)
    assert rt.agent.removed == []


def test_done_or_canceled_removes_everything_but_the_branch(rt):
    claim = make("NF-9", state="done")
    ledger.insert_claim(rt.con, claim)
    d = state_dir(rt.cfg, "NF-9")
    d.mkdir(parents=True)
    (d / "pr-url").write_text("https://pr/9")
    reap_worktrees(rt)
    assert rt.agent.removed == [("NF-9", False)]
    assert ledger.get_claim(rt.con, "NF-9") is None and not d.exists()


def test_a_timed_out_run_keeps_its_dirty_worktree(rt):
    ledger.insert_claim(rt.con, make("NF-9", state="timed_out"))
    rt.agent.remove = lambda key, keep_dirty=False: {"removed": False, "dirty": True}
    reap_worktrees(rt)
    assert ledger.get_claim(rt.con, "NF-9") is not None


def test_release_removes_the_worktree_and_both_state_directories(rt):
    claim = make("NF-9")
    ledger.insert_claim(rt.con, claim)
    state_dir(rt.cfg, "NF-9").mkdir(parents=True)
    release_claim(rt, claim)
    assert rt.agent.removed == [("NF-9", False)] and not state_dir(rt.cfg, "NF-9").exists()
    ledger.insert_claim(rt.con, claim)
    release_claim(rt, claim, keep_worktree=True)
    assert rt.agent.removed == [("NF-9", False)]
    assert rt.agent.stopped_checks == ["NF-9"]
