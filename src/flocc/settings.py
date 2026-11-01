import dataclasses
import json
import math
import os
import re
import shutil
import tempfile
import tomllib
from pathlib import Path

from flocc.config import Config, load

_SCALARS = (int, bool, float, str)
# Fixed by the two-user layout or written by setup and not read; neither is the owner's to change.
_NOT_SETTABLE = ("agent_user", "digest_issue")


class SettingError(ValueError):
    pass


def settable() -> dict[str, type]:
    """Top-level scalar Config fields, minus anything that names a token or key."""
    return {
        f.name: f.type
        for f in dataclasses.fields(Config)
        if f.type in _SCALARS
        and f.name not in _NOT_SETTABLE
        and "token" not in f.name
        and "key" not in f.name
    }


def check(key: str) -> type:
    kind = settable().get(key)
    if kind is None:
        raise SettingError(f"unknown setting {key}; flocc config list shows the settable ones")
    return kind


def parse(key: str, text: str):
    kind = check(key)
    try:
        if kind is bool:
            if text not in ("true", "false"):
                raise ValueError
            return text == "true"
        if kind is float:
            value = float(text)
            if not math.isfinite(value):
                raise ValueError
            return value
        return kind(text)
    except ValueError:
        raise SettingError(f"{key} takes {kind.__name__}, not {text!r}") from None


def render(value) -> str:
    """The TOML text of a scalar. Strings are basic strings, which JSON escaping also satisfies."""
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, str):
        # TOML forbids raw control characters in a basic string, and JSON would escape them to
        # \u00XX, but DEL it passes through.
        if any(ord(c) < 0x20 or ord(c) == 0x7F for c in value):
            raise SettingError("a string value cannot hold control characters")
        return json.dumps(value, ensure_ascii=False)
    return repr(value)


def _split(lines: list[str]) -> int:
    """Index of the first table header, or len(lines): the top-level section is lines[:index]."""
    return next((i for i, line in enumerate(lines) if line.lstrip().startswith("[")), len(lines))


def _key_line(key: str) -> re.Pattern[str]:
    return re.compile(rf"^\s*{re.escape(key)}\s*=")


def edit(text: str, key: str, value: str | None) -> str:
    """Set key's line in the top-level section, or with None remove it; tables are not touched."""
    lines = text.splitlines(keepends=True)
    if lines and not lines[-1].endswith("\n"):
        lines[-1] += "\n"
    end = _split(lines)
    pattern = _key_line(key)
    found = [i for i in range(end) if pattern.match(lines[i])]
    new = [] if value is None else [f"{key} = {value}\n"]
    if found:
        lines[found[0] : found[0] + 1] = new
    elif new:
        at = end
        while at > 0 and not lines[at - 1].strip():
            at -= 1
        lines[at:at] = new
    return "".join(lines)


def write(path: Path, key: str, value=None) -> None:
    """Edit path, check the result loads, and only then replace it, keeping config.toml.bak."""
    try:
        old = path.read_text()
    except OSError as exc:
        raise SettingError(f"{path}: {exc.strerror}") from exc
    new = edit(old, key, None if value is None else render(value))
    fd, tmp_name = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.")
    tmp = Path(tmp_name)
    try:
        with os.fdopen(fd, "w") as f:
            f.write(new)
        shutil.copymode(path, tmp)
        try:
            cfg = load(tmp)
        except (OSError, ValueError, TypeError) as exc:
            raise SettingError(str(exc).replace(str(tmp), str(path))) from exc
        if value is not None and getattr(cfg, key) != value:
            raise SettingError(f"{key} did not read back as {value!r}")
        shutil.copy2(path, path.with_name(path.name + ".bak"))
        os.replace(tmp, path)
    finally:
        tmp.unlink(missing_ok=True)


def in_file(path: Path) -> set[str]:
    return set(tomllib.loads(path.read_text()))
