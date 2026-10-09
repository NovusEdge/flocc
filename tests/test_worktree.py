import shutil
import subprocess

from linear_agents import ledger
from linear_agents.cli import main
from linear_agents.config import RepoConfig
from linear_agents.worktree import changed_files, git, install_hook, prepare_worktree
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
