import subprocess
from pathlib import Path

from linear_agents.config import RepoConfig


def git(*args: str, cwd: Path) -> str:
    out = subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True)
    return out.stdout.strip()


def prepare_worktree(repo: RepoConfig, worktree_root: Path, key: str) -> tuple[Path, str, str]:
    branch = f"agent/{key.lower()}"
    worktree_root.mkdir(parents=True, exist_ok=True)
    # Resolved because git reports symlink-free paths: the stored claim path is compared with
    # `rev-parse --show-toplevel` by the pre-commit hook, and with `worktree list` below.
    path = worktree_root.resolve() / key
    git("fetch", "-q", "origin", repo.base_branch, cwd=repo.path)
    # Drops registrations whose directory was deleted without `worktree remove`, which would
    # otherwise look live below and leave no directory to run in.
    git("worktree", "prune", cwd=repo.path)
    listed = git("worktree", "list", "--porcelain", cwd=repo.path).splitlines()
    # A released run leaves its branch, and with --keep-worktree its worktree; a rerun continues
    # on them, where `worktree add -b` would fail.
    if f"worktree {path}" not in listed:
        has_branch = (
            subprocess.run(
                ["git", "rev-parse", "--verify", "-q", f"refs/heads/{branch}"],
                cwd=repo.path,
                capture_output=True,
                check=False,
            ).returncode
            == 0
        )
        if has_branch:
            git("worktree", "add", "-q", str(path), branch, cwd=repo.path)
        else:
            git(
                "worktree",
                "add",
                "-q",
                "-b",
                branch,
                str(path),
                f"origin/{repo.base_branch}",
                cwd=repo.path,
            )
    base = git("rev-parse", "HEAD", cwd=path)
    if (path / "setup").exists():
        subprocess.run(["./setup"], cwd=path, check=True, capture_output=True, text=True)
    return path, branch, base


def install_hook(worktree: Path, hooks_dir: Path, exe: str, config_path: Path) -> None:
    # The repo's own hooks dir, read with this worktree's core.hooksPath unset so the generated
    # hook can chain to it. A kept worktree still points at the previous install, and chaining to
    # that would make the hook exec itself forever. Exit 5 (not set) and the error before
    # extensions.worktreeConfig is on both mean there is nothing to unset.
    subprocess.run(
        ["git", "config", "--worktree", "--unset", "core.hooksPath"],
        cwd=worktree,
        capture_output=True,
        check=False,
    )
    original = Path(git("rev-parse", "--git-path", "hooks", cwd=worktree))
    if not original.is_absolute():
        original = (worktree / original).resolve()
    hooks_dir.mkdir(parents=True, exist_ok=True)
    hook = hooks_dir / "pre-commit"
    hook.write_text(
        "#!/bin/sh\nset -e\n"
        f'LINEAR_AGENTS_CONFIG="{config_path}" "{exe}" hook pre-commit\n'
        f'if [ -x "{original}/pre-commit" ]; then exec "{original}/pre-commit" "$@"; fi\n'
    )
    hook.chmod(0o755)
    # Worktree-scoped config so the main checkout and other worktrees keep their own hooks.
    git("config", "extensions.worktreeConfig", "true", cwd=worktree)
    git("config", "--worktree", "core.hooksPath", str(hooks_dir), cwd=worktree)


def contains(worktree: Path, sha: str) -> bool:
    out = subprocess.run(
        ["git", "merge-base", "--is-ancestor", sha, "HEAD"],
        cwd=worktree,
        capture_output=True,
        check=False,
    )
    return out.returncode == 0


def changed_files(worktree: Path, a: str, b: str) -> set[str]:
    out = git("diff", "--name-only", f"{a}..{b}", cwd=worktree)
    return {line for line in out.splitlines() if line}
