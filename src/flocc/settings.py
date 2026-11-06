import json
import math
import os
import re
import shutil
import tempfile
import tomllib
from pathlib import Path

from flocc.config import load

# Operational knobs only. A Config field is not settable until it is listed here with its range,
# so a new one cannot become settable, and spend or trigger settings never are.
SETTABLE: dict[str, tuple[type, float, float]] = {
    "session_cap": (int, 1, 6),
    "check_timeout_s": (int, 60, 7200),
    "setup_timeout_s": (int, 60, 7200),
    "check_fix_rounds": (int, 0, 3),
    "default_wall_clock_s": (int, 600, 21600),
    "review_wall_clock_s": (int, 600, 21600),
    "min_available_mem_gb": (float, 0, 64),
    "min_free_disk_gb": (float, 0, 1000),
    "tmp_max_pct": (float, 1, 100),
    "digest_hour": (int, 0, 23),
    "shared_uv_cache": (bool, 0, 1),
    "skip_unmapped": (bool, 0, 1),
    "cardinal_file_on": (bool, 0, 1),
}
# Every other Config field. test_settings fails when a field is in neither table.
NOT_SETTABLE = frozenset(
    {
        "team_key",
        "trigger_label",
        "repos",
        "state_root",
        "ledger_path",
        "linear_key_file",
        "agent_gh_token_file",
        "publish_gh_token_file",
        "five_hour_max_pct",
        "weekly_max_pct",
        "review_model",
        "tester_effort",
        "impl_effort",
        "review_effort",
        "mcp_servers",
        "commit_trailer",
        "steer_authors",
        "github_steer_logins",
        "comment_fix_max",
        "digest_issue",
        "agent_user",
        "agent_home",
        "worktree_root",
        "agent_state_root",
        "plugin_dir",
        "agent_flocc",
        "agent_run",
        "agent_plugins_file",
    }
)


class SettingError(ValueError):
    pass


def check(key: str) -> tuple[type, float, float]:
    if key not in SETTABLE:
        raise SettingError(f"{key} is not settable; flocc config list shows the settable keys")
    return SETTABLE[key]


def parse(key: str, text: str):
    kind, lo, hi = check(key)
    try:
        if kind is bool:
            if text not in ("true", "false"):
                raise ValueError
            return text == "true"
        value = kind(text)
        if kind is float and not math.isfinite(value):
            raise ValueError
    except ValueError:
        raise SettingError(f"{key} takes {kind.__name__}, not {text!r}") from None
    if not lo <= value <= hi:
        raise SettingError(f"{key} {text} is outside {lo:g}..{hi:g}")
    return value


def render(value) -> str:
    """The TOML text of a scalar. Strings are basic strings, which JSON escaping also satisfies."""
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, str):
        # TOML forbids raw control characters in a basic string, and JSON would escape them to
        # \u00XX, but DEL it passes through.
        if any(ord(c) < 0x20 or ord(c) == 0x7F for c in value):
            raise SettingError("a string value cannot hold control characters")
        try:
            value.encode()
        except UnicodeEncodeError:
            raise SettingError("a string value must be valid UTF-8") from None
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
            f.flush()
            os.fsync(f.fileno())
        shutil.copymode(path, tmp)
        try:
            cfg = load(tmp)
        except (OSError, ValueError, TypeError) as exc:
            raise SettingError(str(exc).replace(str(tmp), str(path))) from exc
        if value is not None and getattr(cfg, key) != value:
            raise SettingError(f"{key} did not read back as {value!r}")
        # A temp file and rename, so a .bak that is a symlink is replaced, not written through.
        bak = path.with_name(f".{path.name}.bak.{os.getpid()}")
        try:
            shutil.copy2(path, bak)
            os.replace(bak, path.with_name(path.name + ".bak"))
        finally:
            bak.unlink(missing_ok=True)
        os.replace(tmp, path)
    finally:
        tmp.unlink(missing_ok=True)


def in_file(path: Path) -> set[str]:
    return set(tomllib.loads(path.read_text()))
