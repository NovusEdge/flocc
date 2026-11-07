import json
import subprocess
import time
from dataclasses import replace
from datetime import UTC, datetime
from pathlib import Path

import pytest

from flocc.config import TRAILER, RepoConfig
from flocc.publisher import (
    COMMENT_LIMIT,
    BranchMoved,
    branch_subjects,
    check_at_base,
    check_worktree,
    comment_on_pr,
    failure_excerpt,
    pr_comments,
    pr_title,
    pr_view,
    review_comment,
)
from flocc.publisher import publish as _publish
from flocc.worktree import git, prepare_worktree
from tests.test_ledger import make


def publish(*args, **kw):
    kw.setdefault("check", lambda c, r, t: check_worktree(r.check, Path(c.worktree), t))
    return _publish(*args, **kw)


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


def test_the_push_goes_from_the_service_clone_with_the_check_run_elsewhere(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    seen = []

    def remote_check(c, r, timeout_s):
        seen.append((c.issue_key, r.check))
        return check_worktree(r.check, Path(c.worktree), timeout_s)

    result = publish(claim, repo, "t", "s", token="t", runner=Gh(), check=remote_check)
    assert result.status == "opened" and seen == [("NF-5", "true")]
    assert git("rev-parse", "refs/flocc/NF-5", cwd=origin) == git("rev-parse", "HEAD", cwd=path)


def test_a_check_still_running_publishes_nothing(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path)
    gh = Gh()
    result = publish(
        claim, repo, "t", "s", token="t", runner=gh, check=lambda c, r, t: {"status": "running"}
    )
    assert result.status == "check_running" and gh.calls == []
    assert git("ls-remote", "origin", claim.branch, cwd=origin) == ""


def test_a_branch_that_moves_during_the_check_is_not_pushed(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)

    def moving_check(c, r, timeout_s):
        (path / "late.txt").write_text("x")
        git("add", ".", cwd=path)
        git("commit", "-qm", "late", cwd=path)
        return check_worktree("true", path, timeout_s)

    result = publish(claim, repo, "t", "s", token="t", runner=Gh(), check=moving_check)
    assert result.status == "check_failed" and "moved while the check ran" in result.detail


def test_pr_title_has_a_conventional_type_from_the_labels():
    assert pr_title("NF-1", "Fix rounding", ("v2", "Bug")) == "fix: Fix rounding (NF-1)"
    assert pr_title("NF-2", "Pool writes", ("Chore", "claude")) == "chore: Pool writes (NF-2)"
    assert pr_title("NF-3", "Web app: frames", ("Feature",)) == "feat: Web app: frames (NF-3)"
    assert pr_title("NF-4", "Unlabelled") == "feat: Unlabelled (NF-4)"


def test_pr_title_takes_the_type_the_commits_agree_on():
    docs = ("docs(agents): add the lookup", "docs: fix a link")
    assert pr_title("NF-6", "Docs", (), docs) == "docs: Docs (NF-6)"
    assert pr_title("NF-6", "Docs", ("Bug",), docs) == "docs: Docs (NF-6)"


def test_pr_title_falls_back_to_the_labels_unless_the_commits_agree():
    mixed = ("docs: a", "fix: b")
    assert pr_title("NF-7", "Mixed", ("Bug",), mixed) == "fix: Mixed (NF-7)"
    assert pr_title("NF-7", "Mixed", (), mixed) == "feat: Mixed (NF-7)"
    plain = ("update the docs", "docs: a")
    assert pr_title("NF-8", "Plain", ("Chore",), plain) == "chore: Plain (NF-8)"
    assert pr_title("NF-8", "None", ("Chore",), ()) == "chore: None (NF-8)"


def test_branch_subjects_skip_the_testers_commits(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    git("commit", "-q", "--amend", "-m", f"test: pin it\n\n{TRAILER}: NF-5 test", cwd=path)
    (path / "impl.py").write_text("x = 1\n")
    git("add", ".", cwd=path)
    git("commit", "-qm", f"docs(agents): explain\n\n{TRAILER}: NF-5 impl", cwd=path)
    assert branch_subjects(claim, repo) == ("docs(agents): explain",)


def test_branch_subjects_are_empty_when_the_branch_cannot_be_read(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path)
    assert branch_subjects(replace(claim, worktree=str(tmp_path / "gone")), repo) == ()


def test_pr_is_created_with_the_title_as_given(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path)
    gh = Gh()
    publish(claim, repo, "fix: Rounding (NF-5)", "s", token="t", runner=gh)
    pr = gh.calls[-1]
    assert pr[pr.index("--title") + 1] == "fix: Rounding (NF-5)"


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


def test_reused_pr_is_marked_existing_and_a_new_one_is_not(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path)
    assert publish(claim, repo, "t", "s", token="t", runner=Gh()).existing is False
    gh = Gh(existing="https://github.com/o/r/pull/9\n")
    assert publish(claim, repo, "t", "s", token="t", runner=gh).existing is True


def test_reuse_only_never_creates_a_pr(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path)
    gh = Gh()
    result = publish(claim, repo, "t", "s", token="t", runner=gh, reuse_only=True)
    assert result.status == "pr_closed" and not any(c[2] == "create" for c in gh.calls)


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


def _amend(path):
    (path / "test_x.py").write_text("assert 1\n")
    git("commit", "-qam", "amend", cwd=path)
    return {"test_x.py": {"reason": "asserts the old API"}}


def _labelled(repo):
    return replace(repo, amended_label="tests-amended")


def _label(n):
    return [
        "gh",
        "api",
        "-X",
        "POST",
        f"repos/o/r/issues/{n}/labels",
        "-f",
        "labels[]=tests-amended",
    ]


def _edits(gh):
    return [c for c in gh.calls if c[-1].startswith("labels[]=")]


def test_amended_label_is_added_to_a_new_pr(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    gh = Gh()
    publish(claim, _labelled(repo), "t", "s", token="t", runner=gh, amended=_amend(path))
    assert _edits(gh) == [_label(9)]
    assert gh.kw["env"]["GH_TOKEN"] == "t"


def test_amended_label_is_added_to_an_existing_pr(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    gh = Gh(existing="https://github.com/o/r/pull/4\n")
    amended = _amend(path)
    publish(
        claim, _labelled(repo), "t", "s", token="t", runner=gh, amended=amended, reuse_only=True
    )
    assert _edits(gh) == [_label(4)]


@pytest.mark.parametrize("existing", ["", "https://github.com/o/r/pull/4\n"])
def test_a_label_the_edit_rejects_does_not_fail_the_publish(origin, tmp_path, existing):
    repo, path, claim = setup_claim(origin, tmp_path)
    gh = Gh(existing=existing)

    def runner(argv, **kw):
        if argv[-1].startswith("labels[]="):
            raise subprocess.CalledProcessError(1, argv, stderr="could not add label: not found")
        return gh(argv, **kw)

    result = publish(
        claim, _labelled(repo), "t", "s", token="t", runner=runner, amended=_amend(path)
    )
    assert result.status == "opened" and result.url
    assert "not found" in result.label_error


def test_no_amendments_adds_no_label(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path)
    gh = Gh()
    publish(claim, _labelled(repo), "t", "s", token="t", runner=gh)
    assert _edits(gh) == []


def test_unset_amended_label_adds_no_label(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    gh = Gh()
    publish(claim, repo, "t", "s", token="t", runner=gh, amended=_amend(path))
    assert _edits(gh) == []


@pytest.mark.parametrize(
    ("before", "after", "why"),
    [
        (
            "def test_a():\n    assert 1\n\ndef test_b():\n    assert 1\n",
            "def test_a():\n    assert 1\n",
            "removes a test",
        ),
        ("describe('a', () => {\n  it('x', () => {})\n})\n", "", "removes a test"),
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


def test_without_a_tester_the_guard_is_skipped(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    claim.tester_sha = None
    (path / "test_x.py").write_text("assert 1\n")
    git("commit", "-qam", "rewrite the test file", cwd=path)
    assert publish(claim, repo, "t", "s", token="t", runner=Gh()).status == "opened"


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


@pytest.mark.parametrize(
    ("before", "after"),
    [
        # Code under test that happens to call skip or todo.
        ("def test_a():\n    assert f()\n", "def test_a():\n    assert list(xs.iter().skip(1))\n"),
        ("it('a', () => {})\n", "it('a', () => { client.todo(3) })\n"),
        # A group renamed around the same tests.
        (
            "describe('old', () => {\n  it('x', () => {})\n})\n",
            "describe('new', () => {\n  it('x', () => {})\n})\n",
        ),
    ],
)
def test_an_amendment_that_only_looks_like_a_skip_passes(origin, tmp_path, before, after):
    repo, path, claim = setup_claim(origin, tmp_path)
    (path / "test_w.py").write_text(before)
    git("add", ".", cwd=path)
    git("commit", "-qm", "more tests", cwd=path)
    claim.tester_sha = git("rev-parse", "HEAD", cwd=path)
    (path / "test_w.py").write_text(after)
    git("commit", "-qam", "amend", cwd=path)
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


def test_check_that_outlives_its_timeout_is_a_timeout_not_a_failure(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path, check="sh -c 'sleep 30; echo late'")
    start = time.monotonic()
    result = publish(claim, repo, "t", "s", token="t", runner=Gh(), check_timeout_s=0.5)
    assert result.status == "check_timeout" and "timed out after 0.5" in result.detail
    assert time.monotonic() - start < 10


def test_failed_check(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path, check="sh -c 'echo boom; exit 1'")
    result = publish(claim, repo, "t", "s", token="t", runner=Gh())
    assert result.status == "check_failed" and "boom" in result.detail


def test_check_at_base_runs_setup_then_the_check_on_a_throwaway_checkout(origin, tmp_path):
    repo = RepoConfig(origin, check="sh -c 'test -f .setup-ran && echo boom; exit 1'")
    sha, failure = check_at_base(repo, tmp_path / "wt", 30, 30, False)
    assert sha == git("rev-parse", "origin/main", cwd=origin)
    assert failure is not None
    assert failure.status == "check_failed" and "boom" in failure.detail
    assert list((tmp_path / "wt").iterdir()) == []
    passing = RepoConfig(origin, check="true")
    assert check_at_base(passing, tmp_path / "wt", 30, 30, False) == (sha, None)


def test_check_at_base_hides_the_random_checkout_path(origin, tmp_path):
    repo = RepoConfig(origin, check="sh -c 'pwd; exit 1'")
    _, failure = check_at_base(repo, tmp_path / "wt", 30, 30, False)
    assert failure is not None
    assert failure.log.strip() == "<checkout>" and failure.detail.strip() == "<checkout>"


def test_check_at_base_reports_a_failing_setup_as_a_failure(origin, tmp_path):
    (origin / "setup").write_text("#!/bin/sh\necho no toolchain; exit 3\n")
    git("commit", "-qam", "break setup", cwd=origin)
    git("push", "-q", "origin", "main", cwd=origin)
    _, failure = check_at_base(RepoConfig(origin, check="true"), tmp_path / "wt", 30, 30, False)
    assert failure is not None
    assert failure.status == "check_failed" and "no toolchain" in failure.log


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


def merged_claim(origin, tmp_path, base_text, resolution=None):
    """A branch whose tester edited a.txt, with a base commit to a.txt merged in."""
    repo = RepoConfig(origin, check="true")
    path, branch, base = prepare_worktree(repo, tmp_path / "wt", "NF-5")
    (path / "a.txt").write_text("a\nb\n")
    git("commit", "-qam", "tests", cwd=path)
    claim = make("NF-5", worktree=str(path))
    claim.branch, claim.base_sha, claim.tester_sha = (
        branch,
        base,
        git("rev-parse", "HEAD", cwd=path),
    )
    (origin / "a.txt").write_text(base_text)
    git("commit", "-qam", "base moves", cwd=origin)
    git("push", "-q", "origin", "main", cwd=origin)
    git("fetch", "-q", "origin", cwd=path)
    subprocess.run(
        ["git", "merge", "-q", "origin/main"], cwd=path, capture_output=True, check=False
    )
    if resolution is not None:
        (path / "a.txt").write_text(resolution)
        git("commit", "-qam", "merge", cwd=path)
    return repo, path, claim


def test_a_clean_merge_of_a_base_edit_to_a_tester_file_is_not_a_test_change(origin, tmp_path):
    repo, _, claim = merged_claim(origin, tmp_path, "c\na\n")
    result = publish(claim, repo, "t", "s", token="t", runner=Gh())
    assert result.status == "opened"


def test_a_conflict_resolution_in_a_tester_file_is_a_test_change(origin, tmp_path):
    repo, _, claim = merged_claim(origin, tmp_path, "x\n", resolution="x\nb\n")
    result = publish(claim, repo, "t", "s", token="t", runner=Gh())
    assert result.status == "tests_touched" and "a.txt" in result.detail


def test_an_amended_conflict_resolution_passes(origin, tmp_path):
    repo, _, claim = merged_claim(origin, tmp_path, "x\n", resolution="x\nb\n")
    amended = {"a.txt": {"reason": "kept both sides"}}
    result = publish(claim, repo, "t", "s", token="t", runner=Gh(), amended=amended)
    assert result.status == "opened"


def test_pr_view_asks_for_the_head_commit_too():
    seen = []

    def gh(argv, **kw):
        seen.append(argv)
        return subprocess.CompletedProcess(argv, 0, "{}", "")

    pr_view(make("NF-5", worktree="/gone"), "https://pr/1", "t", runner=gh)
    assert "headRefOid" in seen[0][seen[0].index("--json") + 1].split(",")


def test_pr_view_asks_for_the_mergeable_state():
    seen = []

    def gh(argv, **kw):
        seen.append(argv)
        return subprocess.CompletedProcess(argv, 0, '{"mergeable": "CONFLICTING"}', "")

    claim = make("NF-5", worktree="/gone")
    assert pr_view(claim, "https://pr/1", "t", runner=gh) == {"mergeable": "CONFLICTING"}
    assert "mergeable" in seen[0][seen[0].index("--json") + 1].split(",")


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


def test_publish_refreshes_the_service_clones_base_before_the_guard(origin, tmp_path, monkeypatch):
    repo, _, claim = setup_claim(origin, tmp_path)
    seen = []
    monkeypatch.setattr("flocc.publisher.origin_base", lambda r: seen.append(r))
    publish(claim, repo, "t", "s", token="t", runner=Gh())
    assert seen == [repo]


def test_trailer_check_is_skipped_when_the_trailer_is_off(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path)
    result = publish(claim, repo, "t", "s", token="t", runner=Gh(), trailer=False)
    assert result.status == "opened" and result.untrailed == ()


def test_review_diff_ignores_base_commits_the_service_clone_has_not_fetched(
    origin, tmp_cfg, tmp_path
):
    from flocc.tick import build_runtime

    bare = tmp_path / "origin.git"
    service = tmp_path / "service"
    agent = tmp_path / "agent"
    for clone in (service, agent):
        git("clone", "-q", str(bare), str(clone), cwd=tmp_path)
        git("config", "user.name", "t", cwd=clone)
        git("config", "user.email", "t@t", cwd=clone)
    (origin / "newer.txt").write_text("n\n")
    git("add", ".", cwd=origin)
    git("commit", "-qm", "newer", cwd=origin)
    git("push", "-q", "origin", "main", cwd=origin)
    git("pull", "-q", cwd=agent)
    git("checkout", "-qb", "agent/nf-5", cwd=agent)
    (agent / "mine.txt").write_text("m\n")
    git("add", ".", cwd=agent)
    git("commit", "-qm", "mine", cwd=agent)
    git("remote", "set-url", "origin", str(bare), cwd=service)
    repo = RepoConfig(service, check="true")
    for name in ("linear.key", "publish.token"):
        (tmp_path / name).write_text("k\n")
    cfg = replace(
        tmp_cfg,
        repos={"backend": repo},
        linear_key_file=tmp_path / "linear.key",
        publish_gh_token_file=tmp_path / "publish.token",
    )
    rt = build_runtime(cfg, tmp_path / "config.toml")
    claim = make("NF-5", worktree=str(agent))
    claim.branch = "agent/nf-5"
    diff = rt.review_diff(claim)
    assert "mine.txt" in diff and "newer.txt" not in diff


PR_URL = "https://github.com/o/r/pull/7"
OWNER = {"login": "NovusEdge", "type": "User"}


def gh_api(conversation=(), inline=(), reviews=()):
    """A runner serving the three endpoints, each split over two pages like --paginate prints."""
    served = {
        "repos/o/r/issues/7/comments?per_page=100": conversation,
        "repos/o/r/pulls/7/comments?per_page=100": inline,
        "repos/o/r/pulls/7/reviews?per_page=100": reviews,
    }

    def gh(argv, **kw):
        assert argv[:3] == ["gh", "api", "--paginate"]
        items = list(served[argv[3]])
        out = json.dumps(items[:1]) + "\n" + json.dumps(items[1:])
        return subprocess.CompletedProcess(argv, 0, out, "")

    return gh


def talk(id_, body, at="2026-11-06T10:00:00Z", user=OWNER, **kw):
    return {"id": id_, "body": body, "created_at": at, "updated_at": at, "user": user, **kw}


def review(id_, body="", at="2026-11-06T10:00:00Z", state="COMMENTED", user=OWNER):
    return {"id": id_, "body": body, "submitted_at": at, "state": state, "user": user}


def drafted(id_, review_id, body, at="2026-11-06T09:00:00Z", user=OWNER, **kw):
    return talk(id_, body, at, user, pull_request_review_id=review_id, path="a.py", line=3, **kw)


def read(logins=("NovusEdge",), **served):
    return pr_comments(PR_URL, "t", logins, runner=gh_api(**served))


def test_pr_comments_maps_the_three_endpoints_into_two_kinds_of_item():
    items = read(
        conversation=[talk(1, "/flocc rename the flag"), talk(2, "no prefix"), talk(3, "/flocc x")],
        inline=[drafted(10, 20, "/flocc use a set here"), drafted(11, 22, "plain")],
        reviews=[review(20), review(21, "/flocc and the docs"), review(22)],
    )
    assert [(key, text) for key, _, text in items] == [
        ("c:1", "NovusEdge on the PR: rename the flag"),
        ("c:3", "NovusEdge on the PR: x"),
        ("r:20", "NovusEdge on a.py:3: use a set here"),
        ("r:21", "NovusEdge in a review: and the docs"),
    ]


def test_pr_comments_skips_bots_unlisted_logins_and_the_services_own_text():
    bot = {"login": "NovusEdge", "type": "Bot"}
    stranger = {"login": "someone", "type": "User"}
    items = read(
        conversation=[
            talk(1, "/flocc from a bot", user=bot),
            talk(2, "/flocc from a stranger", user=stranger),
            talk(3, "/flocc 🤖 flocc: Comment fix pushed"),
            talk(4, "/flocc ok"),
        ]
    )
    assert [key for key, _, _ in items] == ["c:4"]


def test_pr_comments_reads_only_bodies_that_start_with_the_prefix():
    items = read(conversation=[talk(1, "  \n/flocc  trim me  "), talk(2, "see /flocc")])
    assert [(key, text) for key, _, text in items] == [("c:1", "NovusEdge on the PR: trim me")]


def test_the_prefix_is_a_word_and_a_bare_one_asks_for_nothing():
    items = read(
        conversation=[
            talk(1, "/floccx do it"),
            talk(2, "/flocc-x do it"),
            talk(3, "/flocc"),
            talk(4, "/flocc   \n"),
            talk(5, "/flocc\nmultiline"),
        ]
    )
    assert [(key, text) for key, _, text in items] == [("c:5", "NovusEdge on the PR: multiline")]


def test_pr_comments_skips_pending_reviews_and_empty_review_bodies():
    items = read(
        inline=[drafted(10, 20, "/flocc draft"), drafted(11, 21, "plain")],
        reviews=[review(20, state="PENDING"), review(21, "")],
    )
    assert items == []


def test_an_inline_comment_is_timed_by_its_reviews_submission():
    items = read(
        conversation=[talk(1, "/flocc first", "2026-11-06T09:30:00Z")],
        inline=[drafted(10, 20, "/flocc second", at="2026-11-06T09:00:00Z")],
        reviews=[review(20, at="2026-11-06T10:00:00Z")],
    )
    assert [key for key, _, _ in items] == ["c:1", "r:20"]


def test_a_prefixed_review_body_brings_in_every_inline_comment_of_its_review():
    items = read(
        inline=[drafted(10, 20, "plain one"), drafted(11, 20, "/flocc two"), drafted(12, 21, "x")],
        reviews=[review(20, "/flocc see the notes"), review(21)],
    )
    [(key, _, text)] = items
    assert key == "r:20"
    assert text.splitlines() == [
        "NovusEdge in a review: see the notes",
        "NovusEdge on a.py:3: plain one",
        "NovusEdge on a.py:3: two",
    ]


def test_an_inline_comment_without_a_line_falls_back_to_its_original_line():
    inline = drafted(10, 20, "/flocc here")
    inline["line"], inline["original_line"] = None, 9
    [(_, _, text)] = read(inline=[inline], reviews=[review(20)])
    assert text == "NovusEdge on a.py:9: here"


def test_an_edited_old_comment_keeps_its_creation_time():
    old = talk(1, "/flocc edited in later", "2026-11-01T10:00:00Z")
    old["updated_at"] = "2026-11-06T10:00:00Z"
    [(_, at, _)] = read(conversation=[old])
    assert at == datetime(2026, 11, 1, 10, tzinfo=UTC).timestamp()


def test_pr_comments_come_back_oldest_first():
    items = read(
        conversation=[
            talk(1, "/flocc b", "2026-11-06T11:00:00Z"),
            talk(2, "/flocc a", "2026-11-06T08:00:00Z"),
        ]
    )
    assert [key for key, _, _ in items] == ["c:2", "c:1"]
