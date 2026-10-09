import shutil
import subprocess
import time

import pytest

from linear_agents import ledger
from linear_agents.cli import main
from linear_agents.config import RepoConfig
from linear_agents.worktree import (
    changed_files,
    contains,
    git,
    install_hook,
    prepare_worktree,
    run_bounded,
)
from tests.paths import EXAMPLE
from tests.test_ledger import make


def test_prepare_worktree(origin, tmp_path):
    path, branch, base = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    assert path == tmp_path / "wt" / "NF-3" and branch == "agent/nf-3"
    assert (path / ".setup-ran").exists()
    assert git("rev-parse", "HEAD", cwd=path) == base


def test_prepare_reuses_a_kept_worktree_or_branch(origin, tmp_path):
    path, _, _ = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    (path / "t.txt").write_text("t\n")
    git("add", "t.txt", cwd=path)
    git("commit", "-qm", "earlier run", cwd=path)
    head = git("rev-parse", "HEAD", cwd=path)
    assert prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")[2] == head
    git("worktree", "remove", "--force", str(path), cwd=origin)
    assert prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")[2] == head


def test_prepare_recovers_a_worktree_whose_directory_was_deleted(origin, tmp_path):
    path, branch, _ = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    (path / "t.txt").write_text("t\n")
    git("add", "t.txt", cwd=path)
    git("commit", "-qm", "earlier run", cwd=path)
    head = git("rev-parse", "HEAD", cwd=path)
    shutil.rmtree(path)
    again, again_branch, base = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    assert (again, again_branch, base) == (path, branch, head)
    assert (path / "t.txt").exists()


def test_setup_that_outlives_its_timeout_fails(origin, tmp_path):
    (origin / "setup").write_text("#!/bin/sh\necho installing\nsleep 30\n")
    git("commit", "-qam", "slow setup", cwd=origin)
    git("push", "-q", "origin", "main", cwd=origin)
    start = time.monotonic()
    with pytest.raises(subprocess.TimeoutExpired) as exc:
        prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3", setup_timeout_s=0.5)
    assert time.monotonic() - start < 10
    assert "installing" in str(exc.value.stderr)


@pytest.mark.skipif(shutil.which("setsid") is None, reason="needs setsid")
def test_timeout_returns_while_an_escaped_child_holds_the_output(tmp_path):
    start = time.monotonic()
    code, out = run_bounded(
        ["sh", "-c", "echo started; setsid sleep 20 & sleep 30"], tmp_path, timeout_s=0.5
    )
    assert time.monotonic() - start < 10
    assert code is None and "started" in out


def test_failed_setup_carries_its_output(origin, tmp_path):
    (origin / "setup").write_text("#!/bin/sh\necho npm is missing >&2\nexit 3\n")
    git("commit", "-qam", "broken setup", cwd=origin)
    git("push", "-q", "origin", "main", cwd=origin)
    with pytest.raises(subprocess.CalledProcessError) as exc:
        prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    assert "npm is missing" in exc.value.stderr


def test_setup_that_leaves_untracked_files_fails(origin, tmp_path):
    path, _, _ = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    (path / "notes.txt").write_text("leftover work\n")
    (path / "setup").write_text("#!/bin/sh\ntouch .setup-ran build.log\n")
    with pytest.raises(subprocess.SubprocessError) as exc:
        prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    assert "build.log" in str(exc.value) and "notes.txt" not in str(exc.value)


def test_kept_worktree_with_uncommitted_work_prepares(origin, tmp_path):
    path, _, _ = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    (path / "notes.txt").write_text("leftover work\n")
    (path / "a.txt").write_text("changed\n")
    assert prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")[0] == path


def test_many_files_left_by_setup_are_listed_capped(origin, tmp_path):
    (origin / "setup").write_text(
        "#!/bin/sh\ntouch .setup-ran\nfor i in $(seq 100); do touch f$i; done\n"
    )
    git("commit", "-qam", "noisy setup", cwd=origin)
    git("push", "-q", "origin", "main", cwd=origin)
    with pytest.raises(subprocess.SubprocessError) as exc:
        prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    message = str(exc.value)
    assert message.count("?? f") == 20 and "80 more" in message


def test_hook_blocks_files_claimed_by_others(origin, tmp_path):
    _check_hook_blocks(origin, tmp_path, tmp_path / "wt")


def test_hook_blocks_when_worktree_root_is_a_symlink(origin, tmp_path):
    real = tmp_path / "real"
    real.mkdir()
    link = tmp_path / "link"
    link.symlink_to(real)
    _check_hook_blocks(origin, tmp_path, link / "wt")


def _check_hook_blocks(origin, tmp_path, root):
    path, _, base = prepare_worktree(RepoConfig(origin), root, "NF-3")
    db = tmp_path / "l.sqlite"
    con = ledger.connect(db)
    ledger.insert_claim(con, make("NF-3", (), worktree=str(path)))
    ledger.insert_claim(con, make("NF-4", ("src/billing/**",)))
    cfg = tmp_path / "c.toml"
    cfg.write_text(
        EXAMPLE.read_text().replace(
            'ledger_path = "~/.local/state/linear-agents/ledger.sqlite"', f'ledger_path = "{db}"'
        )
    )
    exe = shutil.which("linear-agents")
    assert exe
    install_hook(path, tmp_path / "hooks", exe, cfg)
    (path / "src" / "billing").mkdir(parents=True)
    (path / "src" / "billing" / "tax.py").write_text("x\n")
    git("add", ".", cwd=path)
    blocked = subprocess.run(
        ["git", "commit", "-qm", "x"], cwd=path, capture_output=True, text=True, check=False
    )
    assert blocked.returncode != 0 and "NF-4" in blocked.stderr
    git("rm", "-q", "--cached", "src/billing/tax.py", cwd=path)
    (path / "ok.txt").write_text("fine\n")
    git("add", "ok.txt", cwd=path)
    git("commit", "-qm", "ok", cwd=path)
    assert changed_files(path, base, "HEAD") == {"ok.txt"}


def test_reinstalled_hook_still_chains_to_the_repo_hook_once(origin, tmp_path):
    path, _, _ = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    repo_hook = origin / ".git" / "hooks" / "pre-commit"
    repo_hook.write_text(f"#!/bin/sh\necho ran >> {tmp_path / 'repo-hook.log'}\n")
    repo_hook.chmod(0o755)
    cfg = tmp_path / "c.toml"
    cfg.write_text(
        EXAMPLE.read_text().replace(
            'ledger_path = "~/.local/state/linear-agents/ledger.sqlite"',
            f'ledger_path = "{tmp_path / "l.sqlite"}"',
        )
    )
    exe = shutil.which("linear-agents")
    assert exe
    for _ in range(2):  # a rerun with --keep-worktree installs into the same worktree again
        install_hook(path, tmp_path / "hooks", exe, cfg)
    (path / "ok.txt").write_text("fine\n")
    git("add", "ok.txt", cwd=path)
    subprocess.run(
        ["git", "commit", "-qm", "ok"], cwd=path, check=True, capture_output=True, timeout=20
    )
    assert (tmp_path / "repo-hook.log").read_text() == "ran\n"


def test_contains(origin, tmp_path):
    path, _, base = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    (path / "t.txt").write_text("t\n")
    git("add", "t.txt", cwd=path)
    git("commit", "-qm", "tests", cwd=path)
    tests = git("rev-parse", "HEAD", cwd=path)
    assert contains(path, base) and contains(path, tests)
    assert contains(path, base, "origin/main") and not contains(path, tests, "origin/main")
    git("reset", "-q", "--hard", base, cwd=path)
    assert not contains(path, tests)
    assert not contains(path, "0" * 40)


def test_claim_cli(tmp_path, monkeypatch, capsys):
    db = tmp_path / "l.sqlite"
    con = ledger.connect(db)
    ledger.insert_claim(con, make("NF-3", ()))
    ledger.insert_claim(con, make("NF-4", ("src/billing/**",)))
    monkeypatch.setenv("LINEAR_AGENTS_STATE", str(tmp_path / "NF-3"))
    monkeypatch.setenv("LINEAR_AGENTS_LEDGER", str(db))
    assert main(["claim", "docs/**"]) == 0
    assert main(["claim", "src/b*"]) == 1
    assert "NF-4" in capsys.readouterr().err
    mine = ledger.get_claim(con, "NF-3")
    assert mine and mine.paths == ["docs/**"]
