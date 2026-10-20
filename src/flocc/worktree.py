import os
import shlex
import shutil
import signal
import subprocess
from pathlib import Path

from flocc.config import TRAILER, RepoConfig

_LISTED = 20
_DRAIN_S = 5
_OWN_HOOKS = ("pre-commit", "commit-msg")


# A SubprocessError so the poller's prepare-failure handling covers it.
class DirtyWorktree(subprocess.SubprocessError):
    pass


def git(*args: str, cwd: Path) -> str:
    out = subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True)
    return out.stdout.strip()


def run_bounded(
    argv: list[str], cwd: Path, timeout_s: float, env: dict[str, str] | None = None
) -> tuple[int | None, str]:
    """Runs argv with stdout and stderr merged; the code is None when it timed out."""
    # Its own process group, so a timeout kills the test runner or installer it started too;
    # killing only the direct child would leave it running and holding the output pipe open.
    proc = subprocess.Popen(
        argv,
        cwd=cwd,
        env=env,
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


def free_disk_gb(path: Path) -> float:
    """Free space on the filesystem that holds path, or would once it is created."""
    probe = path
    while not probe.exists() and probe != probe.parent:
        probe = probe.parent
    return shutil.disk_usage(probe).free / 1e9


def _has_ref(repo: Path, ref: str) -> bool:
    out = subprocess.run(
        ["git", "rev-parse", "--verify", "-q", ref], cwd=repo, capture_output=True, check=False
    )
    return out.returncode == 0


def prepare_worktree(
    repo: RepoConfig,
    worktree_root: Path,
    key: str,
    setup_timeout_s: float = 900,
    shared_uv_cache: bool = False,
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
        if _has_ref(repo.path, f"refs/heads/{branch}"):
            git("worktree", "add", "-q", str(path), branch, cwd=repo.path)
        else:
            start = f"origin/{repo.base_branch}"
            # Another host ran this issue before: a branch cut fresh from the base would be
            # rejected at the end of the run, after the sessions spent their usage.
            if git("ls-remote", "origin", f"refs/heads/{branch}", cwd=repo.path):
                refspec = f"+refs/heads/{branch}:refs/remotes/origin/{branch}"
                git("fetch", "-q", "origin", refspec, cwd=repo.path)
                start = f"origin/{branch}"
            git("worktree", "add", "-q", "-b", branch, str(path), start, cwd=repo.path)
    base = git("rev-parse", "HEAD", cwd=path)
    if (path / "setup").exists():
        # A kept worktree may hold the previous run's uncommitted work; only what setup adds counts.
        before = _status(path)
        # It runs under the tick lock, which every later tick waits on.
        # The shared name must not match reap_worktrees' ".uv-cache-*" glob, or every tick deletes it.
        cache = worktree_root.resolve() / (".uv-cache" if shared_uv_cache else f".uv-cache-{key}")
        code, out = _run_setup(path, cache, setup_timeout_s, keep_cache=shared_uv_cache)
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


def _run_setup(
    path: Path, cache: Path, timeout_s: float, keep_cache: bool = False
) -> tuple[int | None, str]:
    # By default a cache per setup, deleted after it, because uv's shared ~/.cache/uv grew to 5.6 GB
    # on a 10 GB disk and cannot be pruned while any session's `uv run` holds its lock
    # (UV_LOCK_TIMEOUT does not shorten that wait). It sits beside the worktree so uv can still link
    # rather than copy. The venv survives the deletion because hardlinked files keep their data;
    # uv's symlink mode would not, so the mode is pinned.
    env = {**os.environ, "UV_CACHE_DIR": str(cache), "UV_LINK_MODE": "hardlink"}
    if keep_cache:
        return run_bounded(["./setup"], path, timeout_s, env)
    shutil.rmtree(cache, ignore_errors=True)
    try:
        return run_bounded(["./setup"], path, timeout_s, env)
    finally:
        shutil.rmtree(cache, ignore_errors=True)


def _status(path: Path) -> list[str]:
    # Not git(): its strip() would eat the first line's leading status column.
    out = subprocess.run(
        ["git", "status", "--porcelain"], cwd=path, check=True, capture_output=True, text=True
    )
    return out.stdout.splitlines()


def has_uncommitted_changes(path: Path) -> bool:
    return bool(_status(path))


def install_hook(
    worktree: Path, hooks_dir: Path, exe: str, config_path: Path, trailer: bool = True
) -> None:
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
    # core.hooksPath replaces the repo's whole hooks dir, so every other hook it has gets a stub.
    # A stub left from an earlier install of a hook the repo has since dropped would run nothing.
    for old in hooks_dir.iterdir():
        old.unlink()
    if original.is_dir():
        for src in original.iterdir():
            if src.suffix == ".sample" or src.name in _OWN_HOOKS or not os.access(src, os.X_OK):
                continue
            stub = hooks_dir / src.name
            stub.write_text(f'#!/bin/sh\nexec {shlex.quote(str(src))} "$@"\n')
            stub.chmod(0o755)
    hook = hooks_dir / "pre-commit"
    hook.write_text(
        "#!/bin/sh\nset -e\n"
        f'FLOCC_CONFIG="{config_path}" "{exe}" hook pre-commit\n'
        f'if [ -x "{original}/pre-commit" ]; then exec "{original}/pre-commit" "$@"; fi\n'
    )
    hook.chmod(0o755)
    msg_hook = hooks_dir / "commit-msg"
    msg_hook.write_text(
        "#!/bin/sh\nset -e\n"
        f'"{exe}" hook commit-msg{"" if trailer else " --no-trailer"} "$1"\n'
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


def recover_tests(
    worktree: Path, base_branch: str, branch: str, key: str
) -> tuple[str, str] | None:
    """(base_sha, tester_sha) of the tester's commits on origin's copy of branch, or None."""
    remote = f"origin/{branch}"
    if not _has_ref(worktree, f"refs/remotes/{remote}"):
        return None
    # Exits 1 when the branches share no history; git() would raise and crash every poll of the issue.
    base = subprocess.run(
        ["git", "merge-base", f"origin/{base_branch}", remote],
        cwd=worktree,
        capture_output=True,
        text=True,
        check=False,
    )
    if base.returncode != 0:
        return None
    fork = base.stdout.strip()
    log = git(
        "log",
        "--reverse",
        "--first-parent",
        "--format=%H%x1f%(trailers:only,unfold)%x1e",
        f"{fork}..{remote}",
        cwd=worktree,
    )
    wanted = f"{TRAILER}: {key} test"
    run = []
    # Only the run that starts the branch: the implementer can write a test trailer itself.
    for record in log.split("\x1e"):
        sha, _, trailers = record.strip().partition("\x1f")
        if not sha:
            continue
        if wanted not in {line.strip() for line in trailers.splitlines()}:
            break
        run.append(sha)
    if not run:
        return None
    return git("rev-parse", f"{run[0]}^", cwd=worktree), run[-1]


def changed_files(worktree: Path, a: str, b: str) -> set[str]:
    out = git("diff", "--name-only", f"{a}..{b}", cwd=worktree)
    return {line for line in out.splitlines() if line}
