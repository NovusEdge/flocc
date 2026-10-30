"""flocc-agent: what flocc-agent-run runs as flocc-agent. A subcommand that takes a request reads
it as one JSON object on stdin and prints its answer as one JSON object on stdout."""

import json
import os
import re
import subprocess
import sys
from collections.abc import Callable
from pathlib import Path

from flocc.sessions import SOCKET, Sessions, installed_plugins, trust_folders

SUBCOMMANDS = ("alive", "attach", "capture", "kill", "send", "start")
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
    argv = with_disabled_plugins([str(a) for a in req["argv"]], req.get("plugins"))
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


HANDLERS: dict[str, Callable[[dict], dict]] = {"start": start, "send": send, "capture": capture}


def _request() -> dict:
    data = json.loads(sys.stdin.read() or "{}")
    if not isinstance(data, dict):
        raise BadRequest("the request is not a JSON object")
    return data


def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    os.umask(0o027)
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
