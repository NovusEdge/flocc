import subprocess
import time

from linear_agents.config import RepoConfig
from linear_agents.publisher import publish
from linear_agents.worktree import git, prepare_worktree
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


def test_retry_reuses_the_open_pr(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path)
    gh = Gh(existing="https://github.com/o/r/pull/9\n")
    result = publish(claim, repo, "t", "s", token="t", runner=gh)
    assert result.url.endswith("/pull/9") and not any(c[2] == "create" for c in gh.calls)


def test_rejects_changed_tests(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    (path / "test_x.py").write_text("assert 1\n")
    git("commit", "-qam", "weaken", cwd=path)
    result = publish(claim, repo, "t", "s", token="t", runner=Gh())
    assert result.status == "tests_touched" and "test_x.py" in result.detail


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
