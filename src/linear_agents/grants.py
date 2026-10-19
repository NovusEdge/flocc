import json
import os
import re
from dataclasses import dataclass
from pathlib import Path

_LINE = re.compile(r"^[ \t]*allow:[ \t]*(.+)$", re.IGNORECASE | re.MULTILINE)
_NUMBER = re.compile(r"[0-9]+")


class GrantError(ValueError):
    pass


@dataclass(frozen=True)
class Grants:
    launches: int | None = None
    agents: tuple[str, ...] = ()
    workflows: tuple[str, ...] = ()
    fix_rounds: int | None = None


def parse_allow(text: str) -> Grants:
    launches = fix_rounds = None
    agents: list[str] = []
    workflows: list[str] = []
    for item in (i.strip() for i in text.split(",")):
        if not item:
            continue
        word, _, value = item.partition(" ")
        value = value.strip()
        if word in ("launches", "fix-rounds"):
            if not _NUMBER.fullmatch(value):
                raise GrantError(f"allow: {item!r} needs a whole number")
            if word == "launches":
                launches = int(value)
            else:
                fix_rounds = int(value)
        elif word in ("agent", "workflow") and value and " " not in value:
            (agents if word == "agent" else workflows).append(value)
        else:
            raise GrantError(f"allow: unknown item {item!r}")
    return Grants(launches, tuple(agents), tuple(workflows), fix_rounds)


def allow_lines(body: str) -> list[str]:
    return [m.group(1).strip() for m in _LINE.finditer(body)]


def _union(a: tuple[str, ...], b: tuple[str, ...]) -> tuple[str, ...]:
    return a + tuple(x for x in b if x not in a)


def merge(a: Grants, b: Grants) -> Grants:
    return Grants(
        launches=b.launches if b.launches is not None else a.launches,
        agents=_union(a.agents, b.agents),
        workflows=_union(a.workflows, b.workflows),
        fix_rounds=b.fix_rounds if b.fix_rounds is not None else a.fix_rounds,
    )


def _load(path: Path) -> dict:
    # The gate refuses on its own errors, so a damaged file reads as "no grants", never raises.
    try:
        raw = json.loads(path.read_text())
    except (OSError, ValueError):
        return {}
    return raw if isinstance(raw, dict) else {}


def _save(path: Path, raw: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f"{path.name}.tmp")
    tmp.write_text(json.dumps(raw))
    os.replace(tmp, path)


def _grants(raw: dict) -> Grants:
    try:
        return Grants(
            launches=raw.get("launches"),
            agents=tuple(raw.get("agents", ())),
            workflows=tuple(raw.get("workflows", ())),
            fix_rounds=raw.get("fix_rounds"),
        )
    except (AttributeError, TypeError):
        return Grants()


def _without_launches(g: Grants) -> Grants:
    return Grants(None, g.agents, g.workflows, g.fix_rounds)


def set_block(path: Path, g: Grants) -> None:
    raw = _load(path)
    raw["block"] = {
        "launches": g.launches,
        "agents": list(g.agents),
        "workflows": list(g.workflows),
        "fix_rounds": g.fix_rounds,
    }
    _save(path, raw)


def add_comment(path: Path, g: Grants, phase: str) -> None:
    raw = _load(path)
    c = raw.get("comments") if isinstance(raw.get("comments"), dict) else {}
    launches = c.get("launches") if isinstance(c.get("launches"), dict) else {}
    if g.launches is not None:
        launches[phase] = g.launches
    merged = merge(_grants({**c, "launches": None}), _without_launches(g))
    raw["comments"] = {
        "launches": launches,
        "agents": list(merged.agents),
        "workflows": list(merged.workflows),
        "fix_rounds": merged.fix_rounds,
    }
    _save(path, raw)


def for_phase(path: Path, phase: str) -> Grants:
    raw = _load(path)
    block = _grants(raw.get("block") or {})
    comments_raw = raw.get("comments") if isinstance(raw.get("comments"), dict) else {}
    launches = comments_raw.get("launches")
    per_phase = launches if isinstance(launches, dict) else {}
    comments = _grants({**comments_raw, "launches": per_phase.get(phase)})
    # A launch grant in the agent block is for the implementer, which spends the budget.
    base = block if phase == "impl" else _without_launches(block)
    return merge(base, comments)
