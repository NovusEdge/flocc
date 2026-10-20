import json
import re
from dataclasses import asdict, dataclass

from flocc.grants import GrantError, parse_allow

_OPEN = re.compile(r"^```agent[ \t]*$", re.MULTILINE)
_BLOCK = re.compile(r"^```agent[ \t]*\n(.*?)^```", re.MULTILINE | re.DOTALL)
_CRITERIA = re.compile(
    r"^(?:#{1,6}[ \t]*|\*\*)acceptance criteria[ \t]*:?(?:\*\*)?[ \t]*:?[ \t]*\n"
    r"(?:[ \t]*\n)*[ \t]*(?:[-*]|\d+\.)[ \t]+\S",
    re.IGNORECASE | re.MULTILINE,
)
_DURATION = re.compile(r"^(\d+)([smh])$")
_KEYS = {"model", "effort", "wall_clock", "paths", "depends_on", "mcp", "allow"}


class BlockError(ValueError):
    pass


@dataclass(frozen=True)
class IssueSettings:
    model: str | None = None
    effort: str | None = None
    wall_clock_s: int | None = None
    paths: tuple[str, ...] = ()
    depends_on: tuple[str, ...] = ()
    mcp: tuple[str, ...] = ()
    allow: str | None = None


def _normalise(text: str | None) -> str:
    return (text or "").replace("\r\n", "\n")


def parse_settings(description: str | None) -> IssueSettings:
    text = _normalise(description)
    match = _BLOCK.search(text)
    if not match:
        # Silently running with defaults would drop the paths and depends_on the owner wrote.
        if _OPEN.search(text):
            raise BlockError("agent block: no closing ``` fence")
        return IssueSettings()
    values: dict[str, str] = {}
    for n, line in enumerate(match.group(1).splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        key, sep, value = line.partition(":")
        if not sep:
            raise BlockError(f"agent block line {n}: expected 'key: value'")
        values[key.strip()] = value.strip()
    unknown = values.keys() - _KEYS
    if unknown:
        raise BlockError(f"agent block: unknown keys {sorted(unknown)}")
    allow = values.get("allow") or None
    if allow is not None:
        try:
            parse_allow(allow)
        except GrantError as exc:
            raise BlockError(f"agent block: {exc}") from exc
    return IssueSettings(
        model=values.get("model") or None,
        effort=values.get("effort") or None,
        wall_clock_s=_duration(values["wall_clock"]) if "wall_clock" in values else None,
        paths=_list(values.get("paths", "")),
        depends_on=_list(values.get("depends_on", "")),
        mcp=_list(values.get("mcp", "")),
        allow=allow,
    )


# Split on commas, so brace globs like src/{a,b}/** are unsupported; write two globs.
def _list(value: str) -> tuple[str, ...]:
    value = value.strip()
    if value.startswith("[") and value.endswith("]"):
        value = value[1:-1]
    return tuple(item.strip().strip("'\"") for item in value.split(",") if item.strip())


def _duration(value: str) -> int:
    match = _DURATION.match(value.strip())
    if not match:
        raise BlockError(f"agent block: wall_clock {value!r} is not like 90m or 2h")
    return int(match.group(1)) * {"s": 1, "m": 60, "h": 3600}[match.group(2)]


def has_acceptance_criteria(description: str | None) -> bool:
    return bool(_CRITERIA.search(_normalise(description)))


def settings_to_json(s: IssueSettings) -> str:
    return json.dumps(asdict(s))


def settings_from_json(text: str) -> IssueSettings:
    raw = json.loads(text)
    return IssueSettings(
        model=raw.get("model"),
        effort=raw.get("effort"),
        wall_clock_s=raw.get("wall_clock_s"),
        paths=tuple(raw.get("paths", ())),
        depends_on=tuple(raw.get("depends_on", ())),
        mcp=tuple(raw.get("mcp", ())),
        allow=raw.get("allow"),
    )
