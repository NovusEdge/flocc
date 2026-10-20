import shutil
import subprocess
import time

import pytest

from flocc import ledger, legacy
from flocc.cli import main
from flocc.config import TRAILER, RepoConfig
from flocc.worktree import (
    changed_files,
    contains,
    free_disk_gb,
    git,
    install_hook,
    prepare_worktree,
    recover_tests,
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


def _other_host(tmp_path, key="NF-3"):
    """A second clone of the remote, standing in for the host that ran the issue before."""
    other = tmp_path / "other-host"
    subprocess.run(
        ["git", "clone", "-q", str(tmp_path / "origin.git"), str(other)],
        check=True,
        capture_output=True,
    )
    for k, v in (("user.name", "t"), ("user.email", "t@t")):
        git("config", k, v, cwd=other)
    git("checkout", "-qb", f"agent/{key.lower()}", cwd=other)
    return other


def _commit(repo, name, trailer=""):
    (repo / name).write_text(f"{name}\n")
    git("add", name, cwd=repo)
    git("commit", "-qm", f"{name}\n\n{trailer}".strip(), cwd=repo)
    return git("rev-parse", "HEAD", cwd=repo)


def _push(other):
    git("push", "-qf", "origin", "HEAD", cwd=other)


def _recover(origin, tmp_path):
    path, branch, _ = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    return recover_tests(path, "main", branch, "NF-3")


def test_prepare_continues_a_branch_only_the_remote_has(origin, tmp_path):
    other = _other_host(tmp_path)
    head = _commit(other, "t.txt", f"{TRAILER}: NF-3 test")
    _push(other)
    path, branch, base = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    assert (branch, base) == ("agent/nf-3", head)
    assert (path / "t.txt").exists()


def test_prepare_without_a_remote_branch_cuts_one_from_the_base(origin, tmp_path):
    _, _, base = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    assert base == git("rev-parse", "origin/main", cwd=origin)


def test_recover_tests_takes_the_first_run_of_test_commits(origin, tmp_path):
    other = _other_host(tmp_path)
    fork = git("rev-parse", "HEAD", cwd=other)
    _commit(other, "t1.txt", f"{TRAILER}: NF-3 test")
    last = _commit(other, "t2.txt", f"{legacy.TRAILER}: NF-3 test")
    _commit(other, "impl.txt", f"{TRAILER}: NF-3 impl")
    _commit(other, "t3.txt", f"{TRAILER}: NF-3 test")
    _push(other)
    assert _recover(origin, tmp_path) == (fork, last)


def test_recover_tests_ignores_a_test_trailer_after_an_implementer_commit(origin, tmp_path):
    other = _other_host(tmp_path)
    _commit(other, "impl.txt", f"{TRAILER}: NF-3 impl")
    _commit(other, "t.txt", f"{TRAILER}: NF-3 test")
    _push(other)
    assert _recover(origin, tmp_path) is None


def test_recover_tests_follows_an_amended_tester_commit(origin, tmp_path):
    other = _other_host(tmp_path)
    fork = git("rev-parse", "HEAD", cwd=other)
    _commit(other, "t.txt", f"{TRAILER}: NF-3 test")
    _push(other)
    (other / "t.txt").write_text("amended\n")
    git("commit", "-qa", "--amend", "--no-edit", cwd=other)
    amended = git("rev-parse", "HEAD", cwd=other)
    _push(other)
    assert _recover(origin, tmp_path) == (fork, amended)


def test_recover_tests_needs_the_issues_own_key(origin, tmp_path):
    other = _other_host(tmp_path)
    _commit(other, "t.txt", f"{TRAILER}: NF-30 test")
    _push(other)
    assert _recover(origin, tmp_path) is None


def test_recover_tests_without_a_remote_branch_finds_nothing(origin, tmp_path):
    assert _recover(origin, tmp_path) is None


def test_recover_tests_after_the_base_was_merged_in(origin, tmp_path):
    other = _other_host(tmp_path)
    fork = git("rev-parse", "HEAD", cwd=other)
    tests = _commit(other, "t.txt", f"{TRAILER}: NF-3 test")
    git("checkout", "-q", "main", cwd=other)
    _commit(other, "main.txt")
    git("push", "-q", "origin", "main", cwd=other)
    git("checkout", "-q", "agent/nf-3", cwd=other)
    git("merge", "-q", "--no-edit", "main", cwd=other)
    _push(other)
    # The merge moves the merge-base to main's new tip, but the tester's commit is not an
    # ancestor of it, so the first-parent range still starts at the tests.
    assert _recover(origin, tmp_path) == (fork, tests)


def test_recover_tests_with_no_common_ancestor_finds_nothing(origin, tmp_path):
    other = _other_host(tmp_path)
    git("checkout", "-q", "--orphan", "lone", cwd=other)
    git("rm", "-rfq", ".", cwd=other)
    _commit(other, "t.txt", f"{TRAILER}: NF-3 test")
    git("push", "-qf", "origin", "HEAD:refs/heads/agent/nf-3", cwd=other)
    assert _recover(origin, tmp_path) is None


def test_prepare_recreates_a_worktree_removed_with_git(origin, tmp_path):
    path, branch, _ = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    (path / "t.txt").write_text("t\n")
    git("add", "t.txt", cwd=path)
    git("commit", "-qm", "earlier run", cwd=path)
    head = git("rev-parse", "HEAD", cwd=path)
    git("worktree", "remove", "--force", str(path), cwd=origin)
    assert not path.exists()
    again, again_branch, base = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    assert (again, again_branch, base) == (path, branch, head)
    assert (path / ".setup-ran").exists()


def push_setup(origin, body):
    (origin / "setup").write_text(f"#!/bin/sh\n{body}\n")
    git("commit", "-qam", "setup", cwd=origin)
    git("push", "-q", "origin", "main", cwd=origin)


# The script records where its cache is and what link mode it got, and fills the cache.
RECORD = (
    'mkdir -p "$UV_CACHE_DIR"; echo pkg > "$UV_CACHE_DIR/pkg"; '
    'echo "$UV_CACHE_DIR $UV_LINK_MODE" > "$(dirname "$UV_CACHE_DIR")/seen.txt"'
)


@pytest.mark.parametrize("body, raises", [("", None), ("exit 1", subprocess.CalledProcessError)])
def test_setup_gets_a_throwaway_cache_beside_the_worktree(origin, tmp_path, body, raises):
    root = tmp_path / "wt"
    push_setup(origin, f"{RECORD}\n{body}")
    if raises:
        with pytest.raises(raises):
            prepare_worktree(RepoConfig(origin), root, "NF-3")
    else:
        prepare_worktree(RepoConfig(origin), root, "NF-3")
    cache = root.resolve() / ".uv-cache-NF-3"
    assert (root / "seen.txt").read_text().split() == [str(cache), "hardlink"]
    assert not cache.exists()


def test_shared_cache_is_kept_for_the_next_setup(origin, tmp_path):
    root = tmp_path / "wt"
    push_setup(origin, RECORD)
    prepare_worktree(RepoConfig(origin), root, "NF-3", shared_uv_cache=True)
    cache = root.resolve() / ".uv-cache"
    assert (root / "seen.txt").read_text().split() == [str(cache), "hardlink"]
    push_setup(origin, f'test -f "$UV_CACHE_DIR/pkg"\n{RECORD}')
    prepare_worktree(RepoConfig(origin), root, "NF-4", shared_uv_cache=True)
    assert (cache / "pkg").exists()


def test_cache_is_removed_when_setup_times_out(origin, tmp_path):
    push_setup(origin, 'mkdir -p "$UV_CACHE_DIR"; sleep 30')
    with pytest.raises(subprocess.TimeoutExpired):
        prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3", setup_timeout_s=0.5)
    assert not (tmp_path / "wt" / ".uv-cache-NF-3").exists()


def test_free_disk_gb_probes_the_nearest_existing_parent(tmp_path):
    assert free_disk_gb(tmp_path / "not" / "yet") > 0


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
            'ledger_path = "~/.local/state/flocc/ledger.sqlite"', f'ledger_path = "{db}"'
        )
    )
    exe = shutil.which("flocc")
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
            'ledger_path = "~/.local/state/flocc/ledger.sqlite"',
            f'ledger_path = "{tmp_path / "l.sqlite"}"',
        )
    )
    exe = shutil.which("flocc")
    assert exe
    for _ in range(2):  # a rerun with --keep-worktree installs into the same worktree again
        install_hook(path, tmp_path / "hooks", exe, cfg)
    (path / "ok.txt").write_text("fine\n")
    git("add", "ok.txt", cwd=path)
    subprocess.run(
        ["git", "commit", "-qm", "ok"], cwd=path, check=True, capture_output=True, timeout=20
    )
    assert (tmp_path / "repo-hook.log").read_text() == "ran\n"


def _commit_with_hooks(origin, tmp_path, trailer=True):
    path, _, _ = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    cfg = tmp_path / "c.toml"
    cfg.write_text(
        EXAMPLE.read_text().replace(
            'ledger_path = "~/.local/state/flocc/ledger.sqlite"',
            f'ledger_path = "{tmp_path / "l.sqlite"}"',
        )
    )
    exe = shutil.which("flocc")
    assert exe
    install_hook(path, tmp_path / "hooks", exe, cfg, trailer=trailer)
    return path


def _msg(path):
    return git("log", "-1", "--format=%B", cwd=path)


def test_commit_msg_hook_adds_the_trailer_once(origin, tmp_path, monkeypatch):
    path = _commit_with_hooks(origin, tmp_path)
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path / "state" / "NF-3"))
    monkeypatch.setenv("FLOCC_PHASE", "impl")
    (path / "ok.txt").write_text("fine\n")
    git("add", "ok.txt", cwd=path)
    git("commit", "-qm", "ok", cwd=path)
    assert _msg(path).count("Flocc: NF-3 impl") == 1
    git("commit", "-q", "--amend", "--no-edit", cwd=path)
    assert _msg(path).count("Flocc:") == 1


def test_commit_msg_hook_keeps_signoff_and_strips_claude_attribution(origin, tmp_path, monkeypatch):
    path = _commit_with_hooks(origin, tmp_path)
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path / "state" / "NF-3"))
    monkeypatch.setenv("FLOCC_PHASE", "test")
    (path / "ok.txt").write_text("fine\n")
    git("add", "ok.txt", cwd=path)
    message = (
        "ok\n\nSigned-off-by: Ada <a@example.test>\n"
        "co-authored-by: claude Sonnet <noreply@anthropic.com>\n"
        "Claude-Session: https://claude.ai/code/x\n"
        "Co-Authored-By: Human <h@example.test>\n"
    )
    git("commit", "-q", "-m", message, cwd=path)
    lines = _msg(path).splitlines()
    assert "Signed-off-by: Ada <a@example.test>" in lines
    assert "Co-Authored-By: Human <h@example.test>" in lines
    assert "Flocc: NF-3 test" in lines
    assert not [ln for ln in lines if "claude" in ln.lower()]


def test_commit_msg_hook_without_trailer_still_strips_attribution(origin, tmp_path, monkeypatch):
    path = _commit_with_hooks(origin, tmp_path, trailer=False)
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path / "state" / "NF-3"))
    monkeypatch.setenv("FLOCC_PHASE", "impl")
    (path / "ok.txt").write_text("fine\n")
    git("add", "ok.txt", cwd=path)
    git("commit", "-q", "-m", "ok\n\nClaude-Session: https://claude.ai/code/x", cwd=path)
    assert _msg(path).strip() == "ok"


def test_hooks_chain_to_the_repos_own_commit_msg_and_other_hooks(origin, tmp_path, monkeypatch):
    for name in ("commit-msg", "pre-push"):
        hook = origin / ".git" / "hooks" / name
        hook.write_text(f'#!/bin/sh\necho "$@" >> {tmp_path / (name + ".log")}\n')
        hook.chmod(0o755)
    path = _commit_with_hooks(origin, tmp_path)
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path / "state" / "NF-3"))
    monkeypatch.setenv("FLOCC_PHASE", "impl")
    (path / "ok.txt").write_text("fine\n")
    git("add", "ok.txt", cwd=path)
    git("commit", "-qm", "ok", cwd=path)
    assert "COMMIT_EDITMSG" in (tmp_path / "commit-msg.log").read_text()
    assert "Flocc: NF-3 impl" in _msg(path)
    git("push", "-q", "origin", "HEAD:refs/heads/pushed", cwd=path)
    assert "origin" in (tmp_path / "pre-push.log").read_text()


def test_reinstall_drops_stubs_for_hooks_the_repo_no_longer_has(origin, tmp_path):
    hook = origin / ".git" / "hooks" / "pre-push"
    hook.write_text("#!/bin/sh\n")
    hook.chmod(0o755)
    _commit_with_hooks(origin, tmp_path)
    assert (tmp_path / "hooks" / "pre-push").exists()
    hook.unlink()
    _commit_with_hooks(origin, tmp_path)
    assert not (tmp_path / "hooks" / "pre-push").exists()


def test_commit_msg_hook_adds_no_trailer_in_the_review_phase(origin, tmp_path, monkeypatch):
    path = _commit_with_hooks(origin, tmp_path)
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path / "state" / "NF-3"))
    monkeypatch.setenv("FLOCC_PHASE", "review")
    msg = tmp_path / "msg"
    msg.write_text("x\n")
    assert main(["hook", "commit-msg", str(msg)]) == 0
    assert msg.read_text() == "x\n"
    assert path.exists()


def test_commit_msg_hook_leaves_a_hand_made_commit_alone(origin, tmp_path, monkeypatch):
    path = _commit_with_hooks(origin, tmp_path)
    monkeypatch.delenv("FLOCC_STATE", raising=False)
    monkeypatch.delenv("FLOCC_PHASE", raising=False)
    (path / "ok.txt").write_text("fine\n")
    git("add", "ok.txt", cwd=path)
    git("commit", "-qm", "by hand", cwd=path)
    assert "Flocc" not in _msg(path)


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
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path / "NF-3"))
    monkeypatch.setenv("FLOCC_LEDGER", str(db))
    assert main(["claim", "docs/*.md"]) == 0
    assert main(["claim", "src/b*"]) == 1
    assert "NF-4" in capsys.readouterr().err
    mine = ledger.get_claim(con, "NF-3")
    assert mine and mine.paths == ["docs/*.md"]
