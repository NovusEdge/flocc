"""flocc-agent: what flocc-agent-run runs as flocc-agent. A subcommand that takes a request reads
it as one JSON object on stdin and prints its answer as one JSON object on stdout."""

import contextlib
import grp
import json
import os
import pwd
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from collections.abc import Callable
from pathlib import Path
from typing import Any

from flocc import checkjob
from flocc.config import RepoConfig
from flocc.publisher import check_at_base
from flocc.sessions import SOCKET, Sessions, installed_plugins, trust_folders
from flocc.worktree import (
    BranchDiverged,
    DirtyWorktree,
    has_uncommitted_changes,
    head_at_base,
    install_hook,
    prepare_worktree,
    setup_at_base,
)

SUBCOMMANDS = (
    "alive",
    "attach",
    "capture",
    "check",
    "claude",
    "kill",
    "prepare",
    "probe",
    "remove",
    "send",
    "shell",
    "start",
    "stop-tests",
    "sweep",
    "usage",
)
_NAME = re.compile(r"^[A-Z][A-Z0-9]*-[0-9]+(?:-test|-review)?$")
_FILE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*$")
ATTACH_HINT = "read-only · Ctrl-b d detaches, the session keeps running"


class BadRequest(ValueError):
    pass


def _home() -> Path:
    return Path.home()


def under_home(raw) -> Path:
    if not isinstance(raw, str) or not raw:
        raise BadRequest(f"not a path: {raw!r}")
    path = Path(os.path.abspath(raw))
    if not path.is_relative_to(_home()):
        raise BadRequest(f"{raw} is outside {_home()}")
    return path


def _runtime_dir() -> Path:
    return Path(f"/run/user/{os.getuid()}")


def container_env() -> dict[str, str]:
    sock = _runtime_dir() / "podman" / "podman.sock"
    if not sock.exists():
        return {}
    return {
        "XDG_RUNTIME_DIR": str(_runtime_dir()),
        "DOCKER_HOST": f"unix://{sock}",
        # Ryuk needs a privileged container, which rootless Podman does not give it.
        "TESTCONTAINERS_RYUK_DISABLED": "true",
    }


def tmp_env() -> dict[str, str]:
    return {"TMPDIR": str(_tmp_root())}


def with_container_env(argv: list[str]) -> list[str]:
    if argv[:2] != ["/usr/bin/env", "-i"]:
        return argv
    env = {**tmp_env(), **container_env()}
    return [*argv[:2], *(f"{k}={v}" for k, v in env.items()), *argv[2:]]


def session_name(raw) -> str:
    if not isinstance(raw, str) or not _NAME.match(raw):
        raise BadRequest(f"not a session name: {raw!r}")
    return raw


def _file(raw) -> str:
    if not isinstance(raw, str) or not _FILE.match(raw):
        raise BadRequest(f"not a state file name: {raw!r}")
    return raw


def write_token(token) -> None:
    if not isinstance(token, str):
        raise BadRequest("token is not a string")
    path = _home() / ".config" / "flocc" / "gh-agent.token"
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(".tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write(token.strip() + "\n")
    os.replace(tmp, path)


def apply_handoff(state: Path, files: dict[str, str], clear) -> dict[str, bytes]:
    """Removes clear's files from state, then writes files there; returns what it removed."""
    names = [_file(n) for n in clear]
    for name in files:
        _file(name)
    state.mkdir(parents=True, exist_ok=True)
    saved = {}
    for name in names:
        path = state / name
        if path.is_file() and not path.is_symlink():
            saved[name] = path.read_bytes()
        path.unlink(missing_ok=True)
    for name, text in files.items():
        tmp = state / f".{name}.tmp"
        tmp.write_text(text)
        os.replace(tmp, state / name)
    return saved


def restore(state: Path, saved: dict[str, bytes]) -> None:
    for name, content in saved.items():
        (state / name).write_bytes(content)


def with_disabled_plugins(argv: list[str], allowed) -> list[str]:
    """argv with every installed plugin outside allowed turned off in its --settings JSON."""
    if allowed is None or "--settings" not in argv:
        return argv
    listed = _home() / ".claude" / "plugins" / "installed_plugins.json"
    disabled = [p for p in installed_plugins(listed) if p not in allowed]
    if not disabled:
        return argv
    i = argv.index("--settings") + 1
    settings = json.loads(argv[i])
    settings["enabledPlugins"] = {p: False for p in disabled}
    return [*argv[:i], json.dumps(settings), *argv[i + 1 :]]


def start(req: dict) -> dict:
    name = session_name(req["name"])
    cwd, state = under_home(req["cwd"]), under_home(req["state"])
    write_token(req["token"])
    saved = apply_handoff(state, dict(req.get("files") or {}), req.get("clear") or [])
    argv = with_container_env(
        with_disabled_plugins([str(a) for a in req["argv"]], req.get("plugins"))
    )
    trust = [cwd, *(under_home(p) for p in req.get("trust") or [])]
    trust_folders(_home() / ".claude.json", trust)
    try:
        Sessions().start(name, cwd, argv)
    except BaseException:
        # Without its marker the next tick would take the step for a crash instead of repeating
        # it: a tester rerun on its own commits, or a fix started without the check output.
        restore(state, saved)
        raise
    return {}


def send(req: dict) -> dict:
    Sessions().send(session_name(req["name"]), str(req["text"]))
    return {}


def capture(req: dict) -> dict:
    return {"text": Sessions().capture(session_name(req["name"]), int(req["lines"]))}


def attach(name: str) -> int:
    print(f"Attaching {name} {ATTACH_HINT}", file=sys.stderr)
    argv = ["tmux", "-L", SOCKET, "attach", "-r", "-t", f"={name}", ";"]
    os.execvp("tmux", [*argv, "display-message", "-d", "15000", ATTACH_HINT])


_KEY = re.compile(r"^[A-Z][A-Z0-9]*-[0-9]+$")
_REPO = re.compile(r"^[A-Za-z0-9._-]+$")
# Read on every fetch, so a replaced token takes effect without touching the clone. The same
# string as credentialHelper in installer/internal/steps/config.go.
CREDENTIAL_HELPER = (
    "!f() { test \"$1\" = get && printf 'username=x-access-token\\npassword=%s\\n'"
    ' "$(cat "$HOME/.config/flocc/gh-agent.token")"; }; f'
)


def _key(raw) -> str:
    if not isinstance(raw, str) or not _KEY.match(raw):
        raise BadRequest(f"not an issue key: {raw!r}")
    return raw


def _clone(name, url) -> Path:
    if not isinstance(name, str) or not _REPO.match(name) or name in (".", ".."):
        raise BadRequest(f"not a repo name: {name!r}")
    clone = _home() / "src" / name
    if not (clone / ".git").exists():
        clone.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(
            ["git", "-c", "credential.helper=", "-c", f"credential.helper={CREDENTIAL_HELPER}"]
            + ["clone", "-q", str(url), str(clone)],
            check=True,
            capture_output=True,
            text=True,
        )
        subprocess.run(
            ["git", "config", "--replace-all", "credential.helper", ""], cwd=clone, check=True
        )
        subprocess.run(
            ["git", "config", "--add", "credential.helper", CREDENTIAL_HELPER],
            cwd=clone,
            check=True,
        )
    return clone


def _clean_stale(root: Path) -> None:
    # Left by a killed tick; setup runs under the tick lock, so none is in use now.
    for pattern in (".uv-cache-*", ".canary-*"):
        for stale in root.glob(pattern):
            shutil.rmtree(stale, ignore_errors=True)


def _text(raw) -> str:
    if isinstance(raw, bytes):
        return raw.decode(errors="replace")
    return raw or ""


def _failed(kind: str, detail: str, at_base: str | None = None, **extra) -> dict:
    return {"error": kind, "detail": detail[-4000:], "at_base": at_base, **extra}


def prepare(req: dict) -> dict:
    key = _key(req["key"])
    write_token(req["token"])
    repo = RepoConfig(path=_clone(req["repo"], req["url"]), base_branch=str(req["base_branch"]))
    root = under_home(req["worktree_root"])
    _clean_stale(root)
    try:
        path, branch, base = prepare_worktree(
            repo, root, key, float(req["setup_timeout_s"]), bool(req["shared_uv_cache"])
        )
    except DirtyWorktree as exc:
        return _failed("dirty", str(exc), head_at_base(repo, root.resolve() / key))
    except BranchDiverged as exc:
        return _failed("diverged", str(exc))
    except subprocess.TimeoutExpired as exc:
        return _failed("timeout", _text(exc.stderr), timeout=exc.timeout)
    except subprocess.CalledProcessError as exc:
        if exc.cmd == ["./setup"]:
            at_base = head_at_base(repo, root.resolve() / key)
            return _failed("setup", _text(exc.stderr), at_base, code=exc.returncode)
        return _failed("git", _text(exc.stderr) or str(exc), code=exc.returncode)
    state = under_home(req["state_root"]) / key
    install_hook(path, state / "hooks", shutil.which("flocc") or "flocc", bool(req["trailer"]))
    return {"path": str(path), "branch": branch, "base": base}


def check(req: dict) -> dict:
    mode = req["mode"]
    if mode in ("background", "stop"):
        wt = under_home(req["worktree"])
        key = _key(wt.name)
        state = under_home(req["state_root"]) / key
        if mode == "stop":
            checkjob.stop_job(state, key)
            return {}
        return checkjob.step(req, key, wt, state)
    write_token(req["token"])
    clone = _clone(req["repo"], req["url"])
    repo = RepoConfig(path=clone, base_branch=str(req["base_branch"]), check=str(req["command"]))
    root = under_home(req["worktree_root"])
    _clean_stale(root)
    setup_s, shared = float(req["setup_timeout_s"]), bool(req["shared_uv_cache"])
    if mode == "base-check":
        sha, failure = check_at_base(repo, root, setup_s, float(req["check_timeout_s"]), shared)
        found = None if failure is None else {"detail": failure.detail, "log": failure.log}
        return {"sha": sha, "failure": found}
    if mode == "base-setup":
        code, out, sha = setup_at_base(repo, root, setup_s, shared)
        return {"sha": sha, "code": code, "output": out}
    raise BadRequest(f"unknown check mode {mode!r}")


def _scratch_root() -> Path:
    return Path(f"/tmp/claude-{os.getuid()}")


def scratch_path(line: str) -> Path | None:
    """A Claude Code scratchpad of this user's from a session's listing, or None."""
    if not line.strip():
        return None
    root = _scratch_root()
    path = Path(line.strip())
    try:
        rel = path.resolve().relative_to(root.resolve())
    except ValueError:
        return None
    # A link or ".." in the listed path would make the delete land on another session's pad.
    if path != root / rel:
        return None
    # <project>/<session>/<scratchpad>: anything shorter is shared by other sessions.
    return path if len(rel.parts) >= 3 else None


def _tmp_root() -> Path:
    # TMPDIR for sessions and checks. /tmp is a small tmpfs on flocc hosts, and one nimblefox
    # drift-test run holds 2.2 GiB of pytest copies at once. Claude Code's own scratchpads follow
    # CLAUDE_CODE_TMPDIR instead, so they stay under _scratch_root.
    return _home() / ".cache" / "flocc-tmp"


def _pytest_root() -> Path:
    return _tmp_root() / f"pytest-of-{pwd.getpwuid(os.getuid()).pw_name}"


def _du(path: Path) -> int:
    """Bytes allocated under path, links not followed; on a tmpfs these are the pages it holds."""
    try:
        total = path.lstat().st_blocks * 512
    except OSError:
        return 0
    for top, dirs, files in os.walk(path):
        for name in dirs + files:
            try:
                total += os.lstat(os.path.join(top, name)).st_blocks * 512
            except OSError:
                pass
    return total


def _procs() -> dict[int, tuple[int, list[str]]]:
    found: dict[int, tuple[int, list[str]]] = {}
    for d in Path("/proc").iterdir():
        if not d.name.isdigit():
            continue
        try:
            stat = (d / "stat").read_text()
            raw = (d / "cmdline").read_bytes()
        except OSError:
            continue
        # The command name, in parentheses, may itself hold spaces and parentheses.
        ppid = int(stat.rsplit(")", 1)[1].split()[1])
        found[int(d.name)] = (ppid, [a.decode(errors="replace") for a in raw.split(b"\0") if a])
    return found


def _path_issue(path: Path, root: Path) -> str | None:
    try:
        rel = path.relative_to(root)
    except ValueError:
        return None
    return rel.parts[0] if rel.parts and _KEY.match(rel.parts[0]) else None


def _cwd_issue(pid: int, root: Path) -> str | None:
    try:
        return _path_issue(Path(os.readlink(f"/proc/{pid}/cwd")), root)
    except OSError:
        return None


def _lock_pid(d: Path) -> int | None:
    try:
        pid = int((d / ".lock").read_text().strip())
    except (OSError, ValueError):
        return None
    return pid if pid > 0 else None


_PYTEST_DIR = re.compile(r"pytest-[0-9]+")


def _running(pid: int) -> bool:
    try:
        stat = Path(f"/proc/{pid}/stat").read_text()
    except OSError:
        return False
    return stat.rsplit(")", 1)[1].split()[0] not in ("Z", "X")


# The Bash tool runs each command through a shell that sources Claude Code's snapshot of the
# user's shell. MCP servers, also children of claude, never carry it.
SNAPSHOT_MARK = "/shell-snapshots/snapshot-"
KILL_GRACE_S = 5.0


def _tree(root: int, procs: dict[int, tuple[int, list[str]]]) -> list[int]:
    found, todo = [], [root]
    while todo:
        pid = todo.pop()
        found.append(pid)
        todo += [p for p, (ppid, _) in procs.items() if ppid == pid]
    return found


def _claude_pid(pane: int, procs: dict[int, tuple[int, list[str]]]) -> int | None:
    # env and sh exec into claude, so this is normally the pane's own pid.
    for pid in _tree(pane, procs):
        argv = procs.get(pid, (0, []))[1]
        if argv and Path(argv[0]).name == "claude":
            return pid
    return None


def _bash_tools(claude: int, procs: dict[int, tuple[int, list[str]]]) -> list[int]:
    return [
        p
        for p, (ppid, argv) in procs.items()
        if ppid == claude and any(SNAPSHOT_MARK in a for a in argv)
    ]


def _signal(pids: list[int], sig: int) -> None:
    for pid in pids:
        with contextlib.suppress(ProcessLookupError, PermissionError):
            os.kill(pid, sig)


def _kill_trees(roots: list[int], procs, grace_s: float) -> list[int]:
    pids = [p for root in roots for p in _tree(root, procs)]
    # SIGINT first: pytest turns it into KeyboardInterrupt and runs fixture teardown, which stops
    # the testcontainers it started. SIGTERM kills it without teardown.
    for sig in (signal.SIGINT, signal.SIGTERM):
        _signal([p for p in pids if _running(p)], sig)
        deadline = time.monotonic() + grace_s
        while any(_running(p) for p in pids) and time.monotonic() < deadline:
            time.sleep(0.1)
    _signal([p for p in pids if _running(p)], signal.SIGKILL)
    return pids


def _sweep_pytest(age_s: float, now: float) -> tuple[list[str], int]:
    """pytest's numbered runs no live process holds; pytest itself prunes only on its next run."""
    root = _pytest_root()
    if not root.is_dir() or root.is_symlink():
        return [], 0
    removed: list[str] = []
    freed = 0
    for d in sorted(root.iterdir()):
        # pytest-current is a link to the newest run.
        if d.is_symlink() or not d.is_dir() or not _PYTEST_DIR.fullmatch(d.name):
            continue
        pid = _lock_pid(d)
        if (pid is not None and _running(pid)) or not _older(d, age_s, now):
            continue
        freed += _du(d)
        shutil.rmtree(d, ignore_errors=True)
        removed.append(str(d))
    return removed, freed


def _tmp_entries() -> list[Path]:
    """flocc-agent's entries in /tmp, with pytest's base directory opened up into its runs."""
    uid, pytest_root, found = os.getuid(), _pytest_root(), []
    for p in sorted(_tmp_root().iterdir()):
        try:
            if p.lstat().st_uid != uid:
                continue
        except OSError:
            continue
        if p == pytest_root and p.is_dir() and not p.is_symlink():
            found += [d for d in sorted(p.iterdir()) if not d.is_symlink()]
        else:
            found.append(p)
    return found


def _rss(pid: int) -> int:
    try:
        return int(Path(f"/proc/{pid}/statm").read_text().split()[1]) * os.sysconf("SC_PAGE_SIZE")
    except (OSError, ValueError, IndexError):
        return 0


# The only claude invocations the service needs from flocc-agent's install.
CLAUDE_ALLOWED = (("--version",), ("auth", "status"), ("auth", "login"), ("remote-control",))
_EMAIL = re.compile(r"^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+$")


def claude(args: list[str]) -> int:
    allowed = next((p for p in CLAUDE_ALLOWED if tuple(args[: len(p)]) == p), None)
    rest = args[len(allowed) :] if allowed else args
    if allowed == ("auth", "login"):
        ok = rest in ([], ["--claudeai"]) or (
            len(rest) == 3
            and rest[0] == "--claudeai"
            and rest[1] == "--email"
            and bool(_EMAIL.match(rest[2]))
        )
    else:
        ok = allowed is not None and not rest
    if not ok:
        raise BadRequest(f"claude {' '.join(args)} is not allowed")
    if allowed == ("remote-control",):
        # The consent runs in flocc-agent's worktrees; an untrusted folder would sit at the dialog.
        trust_folders(_home() / ".claude.json", [Path.cwd()])
    exe = _home() / ".local" / "bin" / "claude"
    os.execv(exe, [str(exe), *args])


def _tmux_up() -> bool:
    try:
        probe = ["tmux", "-N", "-L", SOCKET, "list-sessions"]
        return subprocess.run(probe, capture_output=True, check=False).returncode == 0
    except OSError:
        return False


def probe(req: dict) -> dict:
    def readable(path: str) -> bool:
        try:
            with open(path, "rb") as f:
                f.read(1)
            return True
        except OSError:
            return False

    def writable(path: str) -> bool:
        try:
            with tempfile.NamedTemporaryFile(dir=path):
                return True
        except OSError:
            return False

    def group(gid: int) -> str:
        try:
            return grp.getgrgid(gid).gr_name
        except KeyError:
            return str(gid)

    return {
        "read": {str(p): readable(str(p)) for p in req.get("read") or []},
        "write": {str(p): writable(str(p)) for p in req.get("write") or []},
        "tmux": _tmux_up(),
        "groups": sorted({group(g) for g in os.getgroups()}),
        "containers": bool(container_env()),
    }


def _prune_containers() -> None:
    # Only stopped ones: a running container may belong to another issue's check. Ryuk is off,
    # so a check killed mid-run leaves its containers behind until this runs.
    if container_env() and shutil.which("podman"):
        subprocess.run(
            ["podman", "container", "prune", "-f"], capture_output=True, check=False, timeout=120
        )


def remove(req: dict) -> dict:
    key = _key(req["key"])
    wt = under_home(req["worktree_root"]) / key
    state = under_home(req["state_root"]) / key
    # Before the worktree goes: the check runs in it.
    checkjob.stop_job(state, key)
    if wt.exists():
        try:
            if req.get("keep_dirty") and has_uncommitted_changes(wt):
                return {"removed": False, "dirty": True}
            common = subprocess.run(
                ["git", "rev-parse", "--path-format=absolute", "--git-common-dir"],
                cwd=wt,
                capture_output=True,
                text=True,
                check=True,
            ).stdout.strip()
        except subprocess.CalledProcessError:
            common = ""
        clone = Path(common).parent if common else None
        if clone:
            subprocess.run(
                ["git", "worktree", "remove", "--force", str(wt)],
                cwd=clone,
                capture_output=True,
                check=False,
            )
        shutil.rmtree(wt, ignore_errors=True)
        if clone:
            subprocess.run(
                ["git", "worktree", "prune"], cwd=clone, capture_output=True, check=False
            )
    listing = state / "scratchpads"
    if listing.is_file() and not listing.is_symlink():
        for line in listing.read_text().splitlines():
            if (pad := scratch_path(line)) is not None:
                shutil.rmtree(pad, ignore_errors=True)
    shutil.rmtree(state, ignore_errors=True)
    _prune_containers()
    return {"removed": True, "dirty": False}


def _sessions_up() -> bool:
    out = subprocess.run(
        ["tmux", "-N", "-L", SOCKET, "list-sessions", "-F", "#{session_name}"],
        capture_output=True,
        text=True,
        check=False,
    )
    return out.returncode == 0 and any(_NAME.match(n) for n in out.stdout.split())


def _leftover_containers() -> None:
    # Ryuk is off, so a check killed mid-run leaves its containers running. cleanup holds the tick
    # lock, so no publish check runs; with no session up, no session's check runs either.
    if container_env() and shutil.which("podman") and not _sessions_up():
        subprocess.run(
            ["podman", "stop", "--all", "--time", "10"],
            capture_output=True,
            check=False,
            timeout=300,
        )
    _prune_containers()


def _orphans(root: Path, keep: set[str]) -> list[Path]:
    if not root.is_dir():
        return []
    return [
        p
        for p in sorted(root.iterdir())
        if _KEY.match(p.name) and p.name not in keep and p.is_dir() and not p.is_symlink()
    ]


def _older(path: Path, age_s: float, now: float) -> bool:
    return now - path.lstat().st_mtime >= age_s


def _idle(path: Path, age_s: float, now: float) -> bool:
    """Nothing below path changed in age_s; a directory's own mtime misses writes to its files.
    path itself is skipped; its entries, scratchpad included, carry the times that matter."""
    newest = 0.0
    for top, dirs, files in os.walk(path):
        for name in (os.path.join(top, n) for n in dirs + files):
            try:
                newest = max(newest, os.lstat(name).st_mtime)
            except OSError:
                pass
    return now - newest >= age_s


def _git_out(wt: Path, *args: str) -> str:
    out = subprocess.run(
        ["git", *args], cwd=wt, capture_output=True, text=True, check=False, timeout=60
    )
    return out.stdout.strip() if out.returncode == 0 else ""


def _last_change(wt: Path) -> float:
    """The newest of the last commit, the index and the top directory: an edit deep in the tree
    leaves the top directory's mtime alone, and an uncommitted one is caught as dirty anyway."""
    times = [wt.lstat().st_mtime]
    if commit := _git_out(wt, "log", "-1", "--format=%ct"):
        times.append(float(commit))
    if index := _git_out(wt, "rev-parse", "--git-path", "index"):
        path = Path(index) if Path(index).is_absolute() else wt / index
        if path.exists():
            times.append(path.stat().st_mtime)
    return max(times)


def _unpushed(wt: Path) -> bool:
    # Pushes go from the service's clone, so this clone's remote refs lag until it fetches.
    try:
        subprocess.run(
            ["git", "fetch", "-q", "origin"], cwd=wt, capture_output=True, check=False, timeout=120
        )
        out = subprocess.run(
            ["git", "rev-list", "--count", "HEAD", "--not", "--remotes"],
            cwd=wt,
            capture_output=True,
            text=True,
            check=False,
            timeout=60,
        )
    except subprocess.TimeoutExpired:
        return True
    return out.returncode != 0 or out.stdout.strip() != "0"


def _keep_reason(wt: Path, age_s: float, now: float) -> str:
    try:
        if now - _last_change(wt) < age_s:
            return "changed in the last 7 days"
        if has_uncommitted_changes(wt):
            return "uncommitted changes"
    except (subprocess.SubprocessError, OSError, ValueError):
        return "not a git worktree"
    return "commits not on GitHub" if _unpushed(wt) else ""


def sweep(req: dict) -> dict:
    """Removes what no claim owns: old clean worktrees whose commits are all on a remote, old
    state directories, old scratchpads no kept claim lists, and finished pytest runs."""
    keep = {_key(k) for k in req.get("keep") or []}
    root, states = under_home(req["worktree_root"]), under_home(req["state_root"])
    now, age_s = float(req["now"]), float(req["max_age_s"])
    scratch_age_s = float(req["scratch_age_s"])
    pytest_age_s = float(req["pytest_age_s"])
    freed = 0
    removed: list[str] = []
    kept: list[dict] = []
    if root.is_dir():
        _clean_stale(root)
    for wt in _orphans(root, keep):
        if why := _keep_reason(wt, age_s, now):
            kept.append({"path": str(wt), "why": why})
            continue
        remove({"key": wt.name, "worktree_root": str(root), "state_root": str(states)})
        removed.append(str(wt))
    for st in _orphans(states, keep):
        if _older(st, age_s, now) and not (root / st.name).exists():
            remove({"key": st.name, "worktree_root": str(root), "state_root": str(states)})
            removed.append(str(st))
    listed: set[str] = set()
    for k in keep:
        listing = states / k / "scratchpads"
        if listing.is_file() and not listing.is_symlink():
            listed |= {line.strip() for line in listing.read_text().splitlines()}
    # A session directory also holds tasks/ (background command output), which no listing names.
    live_sessions = {Path(line).parent for line in listed}
    pads = _scratch_root()
    for pad in sorted(pads.glob("*/*/scratchpad")) if pads.is_dir() else []:
        if str(pad) in listed or pad.parent in live_sessions:
            continue
        if scratch_path(str(pad)) != pad or not pad.is_dir():
            continue
        if _idle(pad.parent, scratch_age_s, now):
            freed += _du(pad)
            shutil.rmtree(pad, ignore_errors=True)
            removed.append(str(pad))
    runs, run_bytes = _sweep_pytest(pytest_age_s, now)
    _leftover_containers()
    return {"removed": removed + runs, "kept": kept, "freed": freed + run_bytes}


def _containers() -> list[dict]:
    if not (container_env() and shutil.which("podman")):
        return []
    out = subprocess.run(
        ["podman", "ps", "-a", "--format", "json"],
        capture_output=True,
        text=True,
        check=False,
        timeout=60,
    )
    try:
        data = json.loads(out.stdout or "[]")
    except ValueError:
        return []
    return [c for c in data if isinstance(c, dict)] if isinstance(data, list) else []


def _container_issue(container: dict, root: Path) -> str | None:
    # Compose records the project's directory in a label. A container's own working directory
    # is a path inside it and says nothing about the host.
    labels = container.get("Labels") or {}
    for value in labels.values() if isinstance(labels, dict) else []:
        if isinstance(value, str) and (key := _path_issue(Path(value), root)):
            return key
    return None


def _container_memory() -> dict[str, str]:
    out = subprocess.run(
        ["podman", "stats", "--no-stream", "--format", "{{.ID}}\t{{.MemUsage}}"],
        capture_output=True,
        text=True,
        check=False,
        timeout=60,
    )
    rows = (line.split("\t", 1) for line in out.stdout.splitlines() if "\t" in line)
    return {cid.strip(): mem.strip() for cid, mem in rows if cid.strip()}


def _container_name(c: dict) -> str:
    return ", ".join(c.get("Names") or []) or str(c.get("Id", ""))[:12]


def usage(req: dict) -> dict:
    """What flocc-agent's /tmp entries, processes and containers hold, by issue where one owns
    them: a pytest run's .lock pid, or a process's cwd, under worktree_root/KEY."""
    root = under_home(req["worktree_root"]).resolve()
    entries: list[dict[str, Any]] = []
    for p in _tmp_entries():
        pid = _lock_pid(p) if p.parent == _pytest_root() else None
        issue = _cwd_issue(pid, root) if pid else None
        entries.append({"path": str(p), "bytes": _du(p), "issue": issue})
    issues: dict[str, int] = {}
    for pid in _procs():
        if key := _cwd_issue(pid, root):
            issues[key] = issues.get(key, 0) + _rss(pid)
    found = _containers()
    memory = _container_memory() if found else {}

    def mem(c: dict) -> str:
        cid = str(c.get("Id", ""))
        return next((m for short, m in memory.items() if cid.startswith(short)), "not running")

    containers = [
        {"name": _container_name(c), "memory": mem(c), "issue": _container_issue(c, root)}
        for c in found
    ]
    largest = sorted(entries, key=lambda e: -e["bytes"])[:10]
    return {"largest": largest, "issues": issues, "containers": containers}


def stop_tests(req: dict) -> dict:
    """Ends what a session's Bash tool started and the issue's containers; claude and its MCP
    servers keep running. A process that left the tree through setsid escapes this."""
    name = session_name(req["name"])
    wt = under_home(req["worktree"]).resolve()
    procs = _procs()
    pane = Sessions().pane_pid(name)
    claude = _claude_pid(pane, procs) if pane else None
    killed = _kill_trees(_bash_tools(claude, procs), procs, KILL_GRACE_S) if claude else []
    removed, unowned = [], []
    for c in _containers():
        issue = _container_issue(c, wt.parent)
        if issue == wt.name:
            cid = str(c.get("Id", ""))
            subprocess.run(
                ["podman", "rm", "-f", cid], capture_output=True, check=False, timeout=120
            )
            removed.append(_container_name(c))
        elif issue is None and any(
            str(k).startswith("org.testcontainers") for k in c.get("Labels") or {}
        ):
            # Ryuk is off, and these labels name no worktree: they may be another issue's.
            unowned.append(_container_name(c))
    return {"claude": claude, "killed": killed, "containers": removed, "testcontainers": unowned}


# Under Claude Code's 2-minute tool timeout, so the command's own timeout reply arrives first.
SHELL_TIMEOUT_S = 90
SHELL_CHARS = 8000


def shell(req: dict) -> dict:
    """cardinal's agent-shell command."""
    command = req["command"]
    if not isinstance(command, str) or not command.strip():
        raise BadRequest("no command")
    # Its own session, so the timeout kills what the command started along with it.
    proc = subprocess.Popen(
        ["bash", "-c", command],
        cwd=_home(),
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        errors="replace",
        start_new_session=True,
    )
    code: int | None
    try:
        out, _ = proc.communicate(timeout=SHELL_TIMEOUT_S)
        code = proc.returncode
    except subprocess.TimeoutExpired:
        os.killpg(proc.pid, signal.SIGKILL)
        out, _ = proc.communicate()
        code = None
    if len(out) > SHELL_CHARS:
        out = "[...]\n" + out[-SHELL_CHARS:]
    return {"code": code, "output": out}


HANDLERS: dict[str, Callable[[dict], dict]] = {
    "start": start,
    "send": send,
    "capture": capture,
    "prepare": prepare,
    "check": check,
    "remove": remove,
    "probe": probe,
    "sweep": sweep,
    "usage": usage,
    "stop-tests": stop_tests,
    "shell": shell,
}


def _request() -> dict:
    data = json.loads(sys.stdin.read() or "{}")
    if not isinstance(data, dict):
        raise BadRequest("the request is not a JSON object")
    return data


def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    os.umask(0o027)
    _tmp_root().mkdir(mode=0o700, parents=True, exist_ok=True)
    os.environ.update(tmp_env())
    os.environ.update(container_env())
    if not argv or argv[0] not in SUBCOMMANDS:
        print(f"flocc-agent: unknown subcommand: {argv[0] if argv else 'none'}", file=sys.stderr)
        return 2
    sub, args = argv[0], argv[1:]
    try:
        if sub == "alive":
            return 0 if Sessions().alive(session_name(args[0])) else 1
        if sub == "kill":
            Sessions().kill(session_name(args[0]))
            return 0
        if sub == "attach":
            return attach(session_name(args[0]))
        if sub == "claude":
            return claude(args)
        out = HANDLERS[sub](_request())
    except (BadRequest, KeyError, IndexError, TypeError, ValueError) as exc:
        print(f"flocc-agent {sub}: bad request: {exc}", file=sys.stderr)
        return 2
    except subprocess.CalledProcessError as exc:
        print(f"flocc-agent {sub}: {exc} {exc.stderr or ''}".strip(), file=sys.stderr)
        return 1
    print(json.dumps(out))
    return 1 if "error" in out else 0


if __name__ == "__main__":
    raise SystemExit(main())
