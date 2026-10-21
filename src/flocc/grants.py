import json
import os
import re
from dataclasses import dataclass, replace
from pathlib import Path

_LINE = re.compile(r"^[ \t]*allow:[ \t]*(.+)$", re.IGNORECASE | re.MULTILINE)
_NUMBER = re.compile(r"[0-9]{1,6}")


class GrantError(ValueError):
    pass


@dataclass(frozen=True)
class Grants:
    launches: int | None = None
    agents: tuple[str, ...] = ()
    workflows: tuple[str, ...] = ()
    fix_rounds: int | None = None
    commands: tuple[str, ...] = ()


def one_line(command: str) -> str:
    # An allow: line cannot hold a newline, so grants and the commands checked against them are
    # compared with all whitespace runs collapsed to one space.
    return " ".join(command.split())


def parse_allow(text: str) -> Grants:
    # A command can hold commas, so it takes the rest of the line and shares it with nothing.
    word, _, rest = text.strip().partition(" ")
    if word == "command":
        if not rest.strip():
            raise GrantError("allow: command needs the exact command after it")
        return Grants(commands=(one_line(rest),))
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


def launch_flags(g: Grants) -> frozenset[str]:
    # The grants whose effect is fixed by the flags a session starts with: the general-purpose
    # deny rule and disableWorkflows. A grant that arrives later does not take effect until a
    # new session starts, so the gate allows only what the session was launched with.
    return frozenset(
        (["agent:general-purpose"] if "general-purpose" in g.agents else [])
        + [f"workflow:{w}" for w in g.workflows]
    )


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
        commands=_union(a.commands, b.commands),
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
    tmp = path.with_name(f"{path.name}.{os.getpid()}.tmp")
    tmp.write_text(json.dumps(raw))
    os.replace(tmp, path)


def _grants(raw: dict) -> Grants:
    try:
        return Grants(
            launches=raw.get("launches"),
            agents=tuple(raw.get("agents", ())),
            workflows=tuple(raw.get("workflows", ())),
            fix_rounds=raw.get("fix_rounds"),
            commands=tuple(raw.get("commands", ())),
        )
    except (AttributeError, TypeError):
        return Grants()


def _without_launches(g: Grants) -> Grants:
    return replace(g, launches=None)


def set_block(path: Path, g: Grants) -> None:
    raw = _load(path)
    raw["block"] = {
        "launches": g.launches,
        "agents": list(g.agents),
        "workflows": list(g.workflows),
        "fix_rounds": g.fix_rounds,
        "commands": list(g.commands),
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
        "commands": list(merged.commands),
    }
    _save(path, raw)


def add_judge(path: Path, command: str, prefix: str | None) -> None:
    exact, prefixes = judge_grants(path)
    raw = _load(path)
    raw["judge"] = {
        "commands": list(exact) + ([command] if command not in exact else []),
        "prefixes": list(prefixes) + ([prefix] if prefix and prefix not in prefixes else []),
    }
    _save(path, raw)


def judge_grants(path: Path) -> tuple[tuple[str, ...], tuple[str, ...]]:
    j = _load(path).get("judge")
    if not isinstance(j, dict):
        return (), ()

    def strings(key: str) -> tuple[str, ...]:
        v = j.get(key)
        return tuple(x for x in v if isinstance(x, str)) if isinstance(v, list) else ()

    return strings("commands"), strings("prefixes")


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
