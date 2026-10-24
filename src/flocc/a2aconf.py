import os
import re
import tomllib
from collections.abc import Iterable
from dataclasses import dataclass
from pathlib import Path

ISSUE_KEY = re.compile(r"^[A-Z][A-Z0-9]*-\d+$")


class NotConfigured(Exception):
    pass


@dataclass(frozen=True)
class Peer:
    name: str
    url: str
    repos: tuple[str, ...]


@dataclass(frozen=True)
class A2AConfig:
    name: str
    token: str
    accept: dict[str, str]
    hosts: dict[str, Peer]


def default_path() -> Path:
    env = os.environ.get("FLOCC_A2A")
    return Path(env) if env else Path.home() / ".config" / "flocc" / "a2a.toml"


def load(path: Path | None = None) -> A2AConfig:
    path = path or default_path()
    try:
        raw = tomllib.loads(path.read_text())
    except FileNotFoundError as exc:
        raise NotConfigured(f"A2A is not configured here: {path} is missing") from exc
    try:
        hosts = {
            name: Peer(name, str(h["url"]).rstrip("/"), tuple(h.get("repos", [])))
            for name, h in raw.get("hosts", {}).items()
        }
        return A2AConfig(str(raw["name"]), str(raw["token"]), dict(raw.get("accept", {})), hosts)
    except (KeyError, TypeError, AttributeError) as exc:
        raise ValueError(f"{path}: {exc}") from exc


def route(cfg: A2AConfig, names: Iterable[str]) -> Peer | None:
    for name in names:
        for peer in cfg.hosts.values():
            if name in peer.repos:
                return peer
    return None
