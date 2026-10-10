import os
import signal
import subprocess
from pathlib import Path

from linear_agents.config import RepoConfig

_LISTED = 20
_DRAIN_S = 5


# A SubprocessError so the poller's prepare-failure handling covers it.
class DirtyWorktree(subprocess.SubprocessError):
    pass


def git(*args: str, cwd: Path) -> str:
    out = subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True)
    return out.stdout.strip()


def run_bounded(argv: list[str], cwd: Path, timeout_s: float) -> tuple[int | None, str]:
    """Runs argv with stdout and stderr merged; the code is None when it timed out."""
    # Its own process group, so a timeout kills the test runner or installer it started too;
    # killing only the direct child would leave it running and holding the output pipe open.
    proc = subprocess.Popen(
        argv,
        cwd=cwd,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        start_new_session=True,
    )
    try:
        out, _ = proc.communicate(timeout=timeout_s)
    except subprocess.TimeoutExpired:
        os.killpg(proc.pid, signal.SIGKILL)
        # A descendant that left the group (setsid, a daemonizing server) survives the kill and
        # keeps the pipe open; an unbounded read would wait on it under the tick lock. It stays in
        # the tick unit's cgroup, which systemd kills when the oneshot tick exits.
        try:
            out, _ = proc.communicate(timeout=_DRAIN_S)
        except subprocess.TimeoutExpired as exc:
            assert proc.stdout is not None
            proc.stdout.close()
            proc.wait()
            # Raw bytes even in text mode.
            out = (exc.output or b"").decode(errors="replace")
        return None, out
    return proc.returncode, out


def prepare_worktree(
    repo: RepoConfig, worktree_root: Path, key: str, setup_timeout_s: float = 900
) -> tuple[Path, str, str]:
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
        # A kept worktree may hold the previous run's uncommitted work; only what setup adds counts.
        before = _status(path)
        # It runs under the tick lock, which every later tick waits on.
        code, out = run_bounded(["./setup"], path, setup_timeout_s)
        if code is None:
            raise subprocess.TimeoutExpired(["./setup"], setup_timeout_s, stderr=out)
        if code != 0:
            raise subprocess.CalledProcessError(code, ["./setup"], stderr=out)
        # Publish refuses a dirty worktree, so a setup that leaves files git sees would fail every
        # run at the end, after the sessions have spent their usage.
        added = [line for line in _status(path) if line not in before]
        if added:
            listed = "\n".join(added[:_LISTED])
            more = f"\n... and {len(added) - _LISTED} more" if len(added) > _LISTED else ""
            raise DirtyWorktree(f"./setup left files git sees; gitignore them:\n{listed}{more}")
    return path, branch, base


def _status(path: Path) -> list[str]:
    # Not git(): its strip() would eat the first line's leading status column.
    out = subprocess.run(
        ["git", "status", "--porcelain"], cwd=path, check=True, capture_output=True, text=True
    )
    return out.stdout.splitlines()


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
    msg_hook = hooks_dir / "commit-msg"
    msg_hook.write_text(
        "#!/bin/sh\nset -e\n"
        f'"{exe}" hook commit-msg "$1"\n'
        f'if [ -x "{original}/commit-msg" ]; then exec "{original}/commit-msg" "$@"; fi\n'
    )
    msg_hook.chmod(0o755)
    # Worktree-scoped config so the main checkout and other worktrees keep their own hooks.
    git("config", "extensions.worktreeConfig", "true", cwd=worktree)
    git("config", "--worktree", "core.hooksPath", str(hooks_dir), cwd=worktree)


def contains(worktree: Path, sha: str, ref: str = "HEAD") -> bool:
    out = subprocess.run(
        ["git", "merge-base", "--is-ancestor", sha, ref],
        cwd=worktree,
        capture_output=True,
        check=False,
    )
    return out.returncode == 0


def changed_files(worktree: Path, a: str, b: str) -> set[str]:
    out = git("diff", "--name-only", f"{a}..{b}", cwd=worktree)
    return {line for line in out.splitlines() if line}
