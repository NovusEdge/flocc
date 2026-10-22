import subprocess
import time
from dataclasses import replace

import pytest

from flocc.config import RepoConfig
from flocc.publisher import (
    COMMENT_LIMIT,
    BranchMoved,
    comment_on_pr,
    failure_excerpt,
    publish,
    review_comment,
)
from flocc.worktree import git, prepare_worktree
from tests.test_ledger import make


def setup_claim(origin, tmp_path, check="true"):
    repo = RepoConfig(origin, check=check)
    path, branch, base = prepare_worktree(repo, tmp_path / "wt", "NF-5")
    (path / "test_x.py").write_text("assert True\n")
    git("add", ".", cwd=path)
    git("commit", "-qm", "tests", cwd=path)
    tester = git("rev-parse", "HEAD", cwd=path)
    claim = make("NF-5", worktree=str(path))
    claim.branch, claim.base_sha, claim.tester_sha = branch, base, tester
    return repo, path, claim


class Gh:
    def __init__(self, existing=""):
        self.calls = []
        self.existing = existing

    def __call__(self, argv, **kw):
        self.kw = kw
        if argv[0] != "gh":
            return subprocess.run(argv, check=kw.pop("check", False), **kw)
        self.calls.append(argv)
        out = "https://github.com/o/r/pull/9\n" if argv[2] == "create" else self.existing
        return subprocess.CompletedProcess(argv, 0, out, "")


def test_opens_draft_pr(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    (path / "impl.py").write_text("x = 1\n")
    git("add", ".", cwd=path)
    git("commit", "-qm", "impl", cwd=path)
    gh = Gh()
    result = publish(claim, repo, "Fix rounding", "Did it.", token="t", runner=gh)
    assert result.status == "opened" and result.url.endswith("/pull/9")
    pr = gh.calls[-1]
    assert pr[:3] == ["gh", "pr", "create"] and "--draft" in pr
    assert "Fixes NF-5" in pr[pr.index("--body") + 1]
    assert git("ls-remote", "origin", claim.branch, cwd=path)


def test_repo_without_drafts_opens_a_ready_pr(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path)
    gh = Gh()
    publish(claim, replace(repo, draft=False), "t", "s", token="t", runner=gh)
    pr = gh.calls[-1]
    assert pr[:3] == ["gh", "pr", "create"] and "--draft" not in pr


def test_retry_reuses_the_open_pr(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path)
    gh = Gh(existing="https://github.com/o/r/pull/9\n")
    result = publish(claim, repo, "t", "s", token="t", runner=gh)
    assert result.url.endswith("/pull/9") and not any(c[2] == "create" for c in gh.calls)


def test_a_push_rejected_because_the_branch_moved_says_so(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path)
    other = tmp_path / "other-host"
    subprocess.run(
        ["git", "clone", "-q", str(tmp_path / "origin.git"), str(other)],
        check=True,
        capture_output=True,
    )
    for k, v in (("user.name", "t"), ("user.email", "t@t")):
        git("config", k, v, cwd=other)
    git("checkout", "-qb", claim.branch, cwd=other)
    (other / "theirs.txt").write_text("x\n")
    git("add", ".", cwd=other)
    git("commit", "-qm", "pushed from elsewhere", cwd=other)
    git("push", "-q", "origin", claim.branch, cwd=other)
    with pytest.raises(BranchMoved) as exc:
        publish(claim, repo, "t", "s", token="t", runner=Gh())
    assert str(exc.value) == (
        "GitHub's copy of the branch has commits this one lacks"
        " (another host or a person pushed, or a pushed commit was rewritten)"
    )
    assert "[rejected]" in exc.value.stderr


def test_rejects_changed_tests(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    (path / "test_x.py").write_text("assert 1\n")
    git("commit", "-qam", "weaken", cwd=path)
    result = publish(claim, repo, "t", "s", token="t", runner=Gh())
    assert result.status == "tests_touched" and "test_x.py" in result.detail


def test_amended_tests_pass_the_guard_and_reach_the_pr_body(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    (path / "test_x.py").write_text("assert 1\n")
    git("commit", "-qam", "amend", cwd=path)
    gh = Gh()
    amended = {"test_x.py": {"reason": "asserts the old API"}}
    result = publish(claim, repo, "t", "Did it.", token="t", runner=gh, amended=amended)
    assert result.status == "opened"
    body = gh.calls[-1][gh.calls[-1].index("--body") + 1]
    assert "## Amended tests" in body and "- `test_x.py`: asserts the old API" in body
    assert body.startswith("Fixes NF-5\n\nDid it.")


@pytest.mark.parametrize(
    ("before", "after", "why"),
    [
        (
            "def test_a():\n    assert 1\n\ndef test_b():\n    assert 1\n",
            "def test_a():\n    assert 1\n",
            "removes a test",
        ),
        ("describe('a', () => {})\n", "", "removes a test"),
        (
            "def test_a():\n    assert 1\n",
            "import pytest\n\n@pytest.mark.skip\ndef test_a():\n    assert 1\n",
            "skips a test",
        ),
        (
            "def test_a():\n    assert 1\n",
            "def test_a():\n    pytest.skip('later')\n",
            "skips a test",
        ),
        ("it('a', () => {})\n", "it.only('a', () => {})\n", "skips a test"),
        (
            "func TestA(t *testing.T) {}\n",
            "func TestA(t *testing.T) {\n\tt.Skip()\n}\n",
            "skips a test",
        ),
        ("func TestA(t *testing.T) {}\n", "", "removes a test"),
        # Equal counts, different names: one test went and another came.
        ("def test_a():\n    assert 1\n", "def test_b():\n    assert 1\n", "removes a test"),
        ("test('adds', () => {})\n", "test('subtracts', () => {})\n", "removes a test"),
        (
            "func TestA(t *testing.T) {}\n",
            'func TestA(t *testing.T) {\n\tt.Skipf("x")\n}\n',
            "skips a test",
        ),
        (
            "func TestA(t *testing.T) {}\n",
            "func TestA(t *testing.T) {\n\tt.SkipNow()\n}\n",
            "skips a test",
        ),
        (
            "def test_a(self):\n    pass\n",
            "@unittest.skipIf(True, 'x')\ndef test_a(self):\n    pass\n",
            "skips a test",
        ),
        (
            "def test_a(self):\n    pass\n",
            "def test_a(self):\n    self.skipTest('x')\n",
            "skips a test",
        ),
        (
            "def test_a():\n    pass\n",
            "def test_a():\n    pytest.importorskip('x')\n",
            "skips a test",
        ),
        ("it('a', () => {})\n", "it.skipIf(true)('a', () => {})\n", "skips a test"),
        ("it('a', () => {})\n", "it.todo('a')\n", "skips a test"),
    ],
)
def test_an_amendment_that_weakens_a_test_still_blocks(origin, tmp_path, before, after, why):
    repo, path, claim = setup_claim(origin, tmp_path)
    (path / "test_w.py").write_text(before)
    git("add", ".", cwd=path)
    git("commit", "-qm", "more tests", cwd=path)
    claim.tester_sha = git("rev-parse", "HEAD", cwd=path)
    (path / "test_w.py").write_text(after)
    git("commit", "-qam", "weaken", cwd=path)
    amended = {"test_w.py": {"reason": "r"}}
    result = publish(claim, repo, "t", "s", token="t", runner=Gh(), amended=amended)
    assert (
        result.status == "tests_touched" and "test_w.py" in result.detail and why in result.detail
    )


def test_a_moved_or_resigned_test_is_not_removed(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    (path / "test_w.py").write_text("def test_a():\n    assert 1\n\ndef test_b():\n    assert 2\n")
    git("add", ".", cwd=path)
    git("commit", "-qm", "more tests", cwd=path)
    claim.tester_sha = git("rev-parse", "HEAD", cwd=path)
    (path / "test_w.py").write_text(
        "def test_b():\n    assert 2\n\ndef test_a(x=1):\n    assert x\n"
    )
    git("commit", "-qam", "reorder and re-sign", cwd=path)
    amended = {"test_w.py": {"reason": "r"}}
    result = publish(claim, repo, "t", "s", token="t", runner=Gh(), amended=amended)
    assert result.status == "opened"


def test_an_amendment_that_rewrites_a_test_passes(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    (path / "test_w.py").write_text("def test_a():\n    assert old()\n")
    git("add", ".", cwd=path)
    git("commit", "-qm", "more tests", cwd=path)
    claim.tester_sha = git("rev-parse", "HEAD", cwd=path)
    (path / "test_w.py").write_text("def test_a():\n    assert new()\n")
    git("commit", "-qam", "fix the test", cwd=path)
    amended = {"test_w.py": {"reason": "old() is gone"}}
    result = publish(claim, repo, "t", "s", token="t", runner=Gh(), amended=amended)
    assert result.status == "opened"


def test_an_unapproved_touched_test_still_blocks_beside_an_approved_one(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    (path / "test_b.py").write_text("assert True\n")
    git("add", ".", cwd=path)
    git("commit", "-qm", "more tests", cwd=path)
    claim.tester_sha = git("rev-parse", "HEAD", cwd=path)
    (path / "test_x.py").write_text("assert 1\n")
    (path / "test_b.py").write_text("assert 1\n")
    git("commit", "-qam", "amend both", cwd=path)
    amended = {"test_x.py": {"reason": "r"}}
    result = publish(claim, repo, "t", "s", token="t", runner=Gh(), amended=amended)
    assert result.status == "tests_touched" and result.detail == "test_b.py"


def test_accepted_tests_pass_until_changed_again(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    (path / "test_x.py").write_text("assert 1\n")
    git("commit", "-qam", "amend with approval", cwd=path)
    accepted = git("rev-parse", "HEAD", cwd=path)
    result = publish(claim, repo, "t", "s", token="t", runner=Gh(), accepted_sha=accepted)
    assert result.status == "opened"
    (path / "test_x.py").write_text("assert 2\n")
    git("commit", "-qam", "weaken", cwd=path)
    result = publish(claim, repo, "t", "s", token="t", runner=Gh(), accepted_sha=accepted)
    assert result.status == "tests_touched" and "test_x.py" in result.detail


def test_uncommitted_changes_fail_before_the_check(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path, check="sh -c 'echo ran > ran.txt'")
    (path / "impl.py").write_text("x = 1\n")
    gh = Gh()
    result = publish(claim, repo, "t", "s", token="t", runner=gh)
    assert result.status == "check_failed" and "impl.py" in result.detail
    assert not (path / "ran.txt").exists() and gh.calls == []


def test_check_that_outlives_its_timeout_fails(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path, check="sh -c 'sleep 30; echo late'")
    start = time.monotonic()
    result = publish(claim, repo, "t", "s", token="t", runner=Gh(), check_timeout_s=0.5)
    assert result.status == "check_failed" and "timed out after 0.5" in result.detail
    assert time.monotonic() - start < 10


def test_failed_check(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path, check="sh -c 'echo boom; exit 1'")
    result = publish(claim, repo, "t", "s", token="t", runner=Gh())
    assert result.status == "check_failed" and "boom" in result.detail


def test_failure_excerpt_keeps_an_early_error_over_a_passing_tail():
    out = "\n".join(
        ["lint web"] * 50
        + ["web/src/a.ts:3 error TS2304: Cannot find name 'x'"]
        + ["tests/t.py::test_x PytestWarning: not async"] * 20
        + ["test server ok"] * 400
        + ["error: recipe `check` failed on line 23 with exit code 1"]
    )
    excerpt = failure_excerpt(out)
    assert "TS2304" in excerpt and "recipe `check` failed" in excerpt
    assert "PytestWarning" not in excerpt and len(excerpt) <= 3000


def test_failed_check_keeps_the_full_output(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path, check="sh -c 'seq 5000; echo boom; exit 1'")
    result = publish(claim, repo, "t", "s", token="t", runner=Gh())
    assert result.log.startswith("1\n") and "boom" in result.detail


def test_review_comment_wraps_the_review_verbatim():
    assert review_comment("body\n") == "🤖 flocc review\n\nbody\n"


def test_review_comment_is_truncated_to_githubs_limit():
    body = review_comment("x" * (COMMENT_LIMIT + 500))
    assert len(body) == COMMENT_LIMIT and body.endswith("full text is on the host.]")
    assert review_comment("x" * 100).endswith("x" * 100)


def test_comment_uses_the_publish_token_and_stdin(origin, tmp_path):
    _, _, claim = setup_claim(origin, tmp_path)
    gh = Gh()
    comment_on_pr(claim, "https://github.com/o/r/pull/9", "hello", "pub-token", runner=gh)
    assert gh.calls == [
        ["gh", "pr", "comment", "https://github.com/o/r/pull/9", "--body-file", "-"]
    ]
    assert gh.kw["input"] == "hello" and gh.kw["env"]["GH_TOKEN"] == "pub-token"


def _trailed_commit(path, name, trailer):
    (path / name).write_text("x\n")
    git("add", ".", cwd=path)
    git("commit", "-qm", f"{name}\n\n{trailer}".strip(), cwd=path)


def test_commits_without_the_trailer_are_listed_not_blocked(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    _trailed_commit(path, "a.py", "Flocc: NF-5 impl")
    _trailed_commit(path, "b.py", "")
    bare = git("rev-parse", "--short", "HEAD", cwd=path)
    result = publish(claim, repo, "t", "s", token="t", runner=Gh())
    tests = git("rev-parse", "--short", str(claim.tester_sha), cwd=path)
    assert result.status == "opened"
    assert set(result.untrailed) == {bare, tests}


def test_trailer_check_is_skipped_when_the_trailer_is_off(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path)
    result = publish(claim, repo, "t", "s", token="t", runner=Gh(), trailer=False)
    assert result.status == "opened" and result.untrailed == ()
