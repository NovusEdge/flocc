import json
import os
import sys
import time
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from flocc import a2aclient
from flocc.a2aconf import ISSUE_KEY, A2AConfig, NotConfigured, Peer, load, route
from flocc.inbox import BODY_LIMIT


class ToolError(Exception):
    pass


@dataclass
class Ctx:
    cfg: A2AConfig | None
    issue: str | None
    sent_log: Path
    now: Callable[[], float]


_SESSION_ARG = {
    "type": "string",
    "description": "Desktop sessions only: your session name. Leave it out only when the owner"
    " asked you to send these exact words.",
}
TOOLS = [
    {
        "name": "agents",
        "description": "List every flocc issue claimed on any host: host, phase, repo, claimed"
        " paths, and whether its session is live or waiting.",
        "inputSchema": {"type": "object", "properties": {"repo": {"type": "string"}}},
    },
    {
        "name": "send",
        "description": "Send a message to the agent working on an issue. It reaches the agent at"
        " its next tool call or turn end, or when the issue next starts. Pass reply_to with a"
        " task ID to answer a message you received.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "to": {"type": "string", "description": "issue key, e.g. NF-12"},
                "body": {"type": "string"},
                "kind": {"type": "string", "enum": ["direct", "notice", "handoff"]},
                "reply_to": {"type": "string"},
                "repo": {
                    "type": "string",
                    "description": "needed only for an issue no host has claimed",
                },
                "session": _SESSION_ARG,
            },
            "required": ["to", "body"],
        },
    },
    {
        "name": "notify",
        "description": "Tell every live agent in a repo (or all repos) that something shared"
        " changed: an API, a shared test helper, a schema, CI.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "repo": {"type": "string", "description": "a repo name, or all"},
                "body": {"type": "string"},
                "paths": {"type": "array", "items": {"type": "string"}},
                "session": _SESSION_ARG,
            },
            "required": ["repo", "body"],
        },
    },
    {
        "name": "replies",
        "description": "State and reply of messages this session sent.",
        "inputSchema": {
            "type": "object",
            "properties": {"tasks": {"type": "array", "items": {"type": "string"}}},
        },
    },
]


def _cfg(ctx: Ctx) -> A2AConfig:
    if ctx.cfg is None:
        raise ToolError("A2A is not configured on this host")
    return ctx.cfg


def _listing(ctx: Ctx) -> tuple[list[dict], dict[str, str]]:
    cfg = _cfg(ctx)
    rows, down = [], {}
    for peer in cfg.hosts.values():
        try:
            card = a2aclient.extended_card(peer, cfg.token)
        except a2aclient.A2AError as exc:
            down[peer.name] = str(exc)
            continue
        rows += [{**c, "host": peer.name} for c in a2aclient.claims(card)]
    return rows, down


def _log(ctx: Ctx, task: str, host: str, to: str) -> None:
    ctx.sent_log.parent.mkdir(parents=True, exist_ok=True)
    with ctx.sent_log.open("a") as f:
        f.write(json.dumps({"task": task, "host": host, "to": to, "at": ctx.now()}) + "\n")


def _send(ctx: Ctx, peer: Peer, to: str, body: str, kind: str, args: dict, extra: dict) -> dict:
    cfg = _cfg(ctx)
    try:
        task = a2aclient.send(
            peer,
            cfg.token,
            to=to,
            body=body,
            kind=kind,
            sender_as=ctx.issue,
            session=None if ctx.issue else args.get("session"),
            extra=extra,
        )
    except a2aclient.A2AError as exc:
        raise ToolError(str(exc)) from exc
    _log(ctx, task["id"], peer.name, to)
    return task


def _check_body(body: str) -> None:
    size = len(body.encode())
    if size > BODY_LIMIT:
        raise ToolError(f"body is {size} bytes; the limit is {BODY_LIMIT}")


def _agents(args: dict, ctx: Ctx) -> dict:
    rows, down = _listing(ctx)
    repo = args.get("repo")
    return {"agents": [r for r in rows if not repo or r["repo"] == repo], "unreachable": down}


def _target(ctx: Ctx, to: str, repo: str | None) -> Peer:
    cfg = _cfg(ctx)
    rows, down = _listing(ctx)
    holder = next((r["host"] for r in rows if r["issue"] == to), None)
    if holder:
        return cfg.hosts[holder]
    if repo:
        peer = route(cfg, [repo])
        if peer is None:
            raise ToolError(f"no host serves repo {repo}")
        return peer
    if len(cfg.hosts) == 1:
        return next(iter(cfg.hosts.values()))
    gone = f"; unreachable: {', '.join(down)}" if down else ""
    raise ToolError(f"{to} is not claimed on any host; pass repo to pick its host{gone}")


def _send_tool(args: dict, ctx: Ctx) -> dict:
    cfg = _cfg(ctx)
    to, body = str(args.get("to", "")), str(args.get("body", ""))
    if not ISSUE_KEY.match(to):
        raise ToolError(f"{to!r} is not an issue key")
    _check_body(body)
    reply_to = args.get("reply_to")
    if reply_to:
        # The original task lives on the host that delivered it to this session: its own.
        home = cfg.hosts.get(cfg.name)
        if home is None or ctx.issue is None:
            raise ToolError("only a host session can reply to a message it received")
        task = _send(ctx, home, to, body, "reply", args, {"reply_to": reply_to})
        sender = str((task.get("metadata") or {}).get("from", ""))
        issue, _, host = sender.partition("@")
        if host in cfg.hosts and ISSUE_KEY.match(issue):
            _send(ctx, cfg.hosts[host], issue, body, "reply", args, {"reply_to": reply_to})
        return {"task": task["id"], "state": a2aclient.state(task), "host": home.name}
    peer = _target(ctx, to, args.get("repo"))
    task = _send(ctx, peer, to, body, str(args.get("kind") or "direct"), args, {})
    return {"task": task["id"], "state": a2aclient.state(task), "host": peer.name}


def _notify(args: dict, ctx: Ctx) -> dict:
    cfg = _cfg(ctx)
    repo, body = str(args.get("repo", "")), str(args.get("body", ""))
    _check_body(body)
    paths = list(args.get("paths") or [])
    rows, down = _listing(ctx)
    out, failed = [], {}
    for r in rows:
        if not r["live"] or r["issue"] == ctx.issue or (repo != "all" and r["repo"] != repo):
            continue
        extra = {"repo": r["repo"], "paths": paths} if paths else {"repo": r["repo"]}
        try:
            task = _send(ctx, cfg.hosts[r["host"]], r["issue"], body, "notice", args, extra)
        except ToolError as exc:
            failed[r["issue"]] = str(exc)
            continue
        out.append({"issue": r["issue"], "task": task["id"]})
    return {"sent": out, "failed": failed, "unreachable": down}


def _replies(args: dict, ctx: Ctx) -> dict:
    cfg = _cfg(ctx)
    wanted = set(args.get("tasks") or [])
    try:
        lines = ctx.sent_log.read_text().splitlines()
    except FileNotFoundError:
        lines = []
    out = []
    for line in lines:
        try:
            e = json.loads(line)
        except ValueError:
            continue
        if wanted and e.get("task") not in wanted:
            continue
        peer = cfg.hosts.get(e.get("host", ""))
        row = {"task": e.get("task"), "to": e.get("to"), "host": e.get("host")}
        if peer is None:
            out.append({**row, "state": "UNKNOWN", "error": "host no longer configured"})
            continue
        try:
            task = a2aclient.get_task(peer, cfg.token, e["task"])
        except a2aclient.A2AError as exc:
            out.append({**row, "state": "UNKNOWN", "error": str(exc)})
            continue
        out.append({**row, "state": a2aclient.state(task), "reply": a2aclient.reply_text(task)})
    return {"replies": out}


_CALLS = {"agents": _agents, "send": _send_tool, "notify": _notify, "replies": _replies}


def call(name: str, args: dict, ctx: Ctx) -> dict:
    if name not in _CALLS:
        raise ToolError(f"no tool {name}")
    return _CALLS[name](args, ctx)


def _result(req_id, result) -> dict:
    return {"jsonrpc": "2.0", "id": req_id, "result": result}


def serve(stdin, stdout, ctx: Ctx) -> None:
    for line in stdin:
        if not line.strip():
            continue
        try:
            req = json.loads(line)
        except ValueError:
            continue
        method, req_id = req.get("method"), req.get("id")
        if req_id is None:
            continue
        if method == "initialize":
            version = (req.get("params") or {}).get("protocolVersion") or "2025-06-18"
            out = _result(
                req_id,
                {
                    "protocolVersion": version,
                    "capabilities": {"tools": {}},
                    "serverInfo": {"name": "flocc-a2a", "version": "1"},
                },
            )
        elif method == "tools/list":
            out = _result(req_id, {"tools": TOOLS})
        elif method == "tools/call":
            params = req.get("params") or {}
            try:
                body, err = call(params.get("name", ""), params.get("arguments") or {}, ctx), False
            except ToolError as exc:
                body, err = {"error": str(exc)}, True
            text = json.dumps(body)
            out = _result(req_id, {"content": [{"type": "text", "text": text}], "isError": err})
        elif method == "ping":
            out = _result(req_id, {})
        else:
            out = {
                "jsonrpc": "2.0",
                "id": req_id,
                "error": {"code": -32601, "message": f"no method {method}"},
            }
        stdout.write(json.dumps(out) + "\n")
        stdout.flush()


def main() -> int:
    state = os.environ.get("FLOCC_STATE")
    try:
        cfg = load()
    except (NotConfigured, ValueError):
        cfg = None
    if state:
        issue, log = Path(state).name, Path(state) / "a2a-sent.jsonl"
    else:
        issue, log = None, Path.home() / ".local" / "state" / "flocc" / "a2a-sent.jsonl"
    serve(sys.stdin, sys.stdout, Ctx(cfg, issue, log, time.time))
    return 0
