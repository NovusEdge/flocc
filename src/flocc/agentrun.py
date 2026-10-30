"""flocc-agent: what flocc-agent-run runs as flocc-agent. A subcommand that takes a request reads
it as one JSON object on stdin and prints its answer as one JSON object on stdout."""

import json
import os
import re
import shutil
import subprocess
import sys
from collections.abc import Callable
from pathlib import Path

from flocc.config import RepoConfig
from flocc.publisher import check_at_base, check_worktree
from flocc.sessions import SOCKET, Sessions, installed_plugins, trust_folders
from flocc.worktree import (
    BranchDiverged,
    DirtyWorktree,
    head_at_base,
    install_hook,
    prepare_worktree,
    setup_at_base,
)

SUBCOMMANDS = ("alive", "attach", "capture", "check", "kill", "prepare", "send", "start")
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


def with_container_env(argv: list[str]) -> list[str]:
    if argv[:2] != ["/usr/bin/env", "-i"]:
        return argv
    return [*argv[:2], *(f"{k}={v}" for k, v in container_env().items()), *argv[2:]]


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
    if mode == "worktree":
        return check_worktree(
            str(req["command"]), under_home(req["worktree"]), float(req["timeout_s"])
        )
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


HANDLERS: dict[str, Callable[[dict], dict]] = {
    "start": start,
    "send": send,
    "capture": capture,
    "prepare": prepare,
    "check": check,
}


def _request() -> dict:
    data = json.loads(sys.stdin.read() or "{}")
    if not isinstance(data, dict):
        raise BadRequest("the request is not a JSON object")
    return data


def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    os.umask(0o027)
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
