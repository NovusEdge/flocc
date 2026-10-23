import json
import re
import sqlite3
import time
from collections.abc import AsyncGenerator, Callable

from a2a.server.context import ServerCallContext
from a2a.server.events.event_queue import Event
from a2a.server.request_handlers.request_handler import RequestHandler
from a2a.server.routes import create_agent_card_routes, create_rest_routes
from a2a.types.a2a_pb2 import (
    AgentCapabilities,
    AgentCard,
    AgentInterface,
    Artifact,
    HTTPAuthSecurityScheme,
    Part,
    SecurityRequirement,
    SecurityScheme,
    SendMessageRequest,
    StringList,
    SubscribeToTaskRequest,
    Task,
    TaskState,
    TaskStatus,
)
from a2a.utils.errors import (
    A2A_ERROR_MAPPING,
    A2AError,
    ErrorMapping,
    InvalidParamsError,
    UnsupportedOperationError,
)
from google.protobuf.json_format import MessageToDict
from google.protobuf.struct_pb2 import Struct
from starlette.applications import Starlette

from flocc import inbox, ledger
from flocc.a2aconf import ISSUE_KEY, A2AConfig
from flocc.config import Config
from flocc.sessions import session_name
from flocc.usage import latest_usage

PORT = 7443
NUDGE = "You have a message from the owner; it arrives with this prompt."
_SESSION = re.compile(r"^[A-Za-z0-9._-]{1,64}$")
_STATES = {
    "SUBMITTED": TaskState.TASK_STATE_SUBMITTED,
    "COMPLETED": TaskState.TASK_STATE_COMPLETED,
    "FAILED": TaskState.TASK_STATE_FAILED,
    "REJECTED": TaskState.TASK_STATE_REJECTED,
}


class Unauthenticated(A2AError):
    message = "Unauthenticated"


class RateLimited(A2AError):
    message = "Rate limited"


# The SDK maps errors by exact type and answers anything unmapped with 500.
A2A_ERROR_MAPPING[Unauthenticated] = ErrorMapping(401, "UNAUTHENTICATED", "UNAUTHENTICATED")
A2A_ERROR_MAPPING[RateLimited] = ErrorMapping(429, "RESOURCE_EXHAUSTED", "RESOURCE_EXHAUSTED")


def _ledger_ro(cfg: Config) -> sqlite3.Connection:
    con = sqlite3.connect(f"file:{cfg.ledger_path}?mode=ro", uri=True, timeout=2)
    con.row_factory = sqlite3.Row
    return con


def _title(cfg: Config, key: str) -> str:
    try:
        first = (cfg.state_root / key / "context.md").read_text().splitlines()[0]
    except (OSError, IndexError):
        return key
    return first.removeprefix("# ").removeprefix(f"{key}: ")


def should_nudge(cfg: Config, key: str, now: float) -> bool:
    d = cfg.state_root / key
    if not (d / "idle").exists() or (d / "asking").exists():
        return False
    u = latest_usage(cfg.state_root, now)
    return u is None or (
        u.five_hour_pct < cfg.five_hour_max_pct and u.weekly_pct < cfg.weekly_max_pct
    )


def _task(msg: inbox.Message, sender: str | None = None) -> Task:
    task = Task(
        id=msg.task_id,
        context_id=msg.task_id,
        status=TaskStatus(state=_STATES[msg.state]),
    )
    if msg.reason:
        task.status.message.message_id = f"{msg.task_id}-reason"
        task.status.message.parts.add(text=msg.reason)
    if msg.reply is not None:
        task.artifacts.append(
            Artifact(artifact_id=f"{msg.task_id}-reply", name="reply", parts=[Part(text=msg.reply)])
        )
    if sender is not None:
        meta = Struct()
        meta.update({"from": sender})
        task.metadata.CopyFrom(meta)
    return task


class Handler(RequestHandler):
    def __init__(self, cfg: Config, a2a: Callable[[], A2AConfig], sessions, now, card: AgentCard):
        self.cfg, self.a2a, self.sessions, self.now, self.card = cfg, a2a, sessions, now, card

    def _identity(self, context) -> str:
        auth = context.state.get("headers", {}).get("authorization", "")
        token = auth.removeprefix("Bearer ").strip()
        # Read per request, so a ship's new token map applies without a restart.
        who = self.a2a().accept.get(token) if auth.startswith("Bearer ") else None
        if not who:
            raise Unauthenticated()
        return who

    def _inbox(self):
        return inbox.connect(inbox.db_path(self.cfg.state_root))

    async def on_message_send(self, params, context):
        who = self._identity(context)
        msg = params.message
        if not msg.message_id:
            raise InvalidParamsError(message="messageId is required")
        texts = [p.text for p in msg.parts if p.HasField("text")]
        datas = [MessageToDict(p.data) for p in msg.parts if p.HasField("data")]
        meta = next((d["flocc"] for d in datas if isinstance(d, dict) and "flocc" in d), None)
        if not texts or not isinstance(meta, dict):
            raise InvalidParamsError(message="a message needs a text part and a flocc data part")
        body = texts[0]
        if len(body.encode()) > inbox.BODY_LIMIT:
            raise InvalidParamsError(
                message=f"body is {len(body.encode())} bytes; the limit is {inbox.BODY_LIMIT}"
            )
        kind, to = str(meta.get("kind", "direct")), str(meta.get("to", ""))
        if kind not in inbox.KINDS or not ISSUE_KEY.match(to):
            raise InvalidParamsError(message=f"bad kind {kind!r} or recipient {to!r}")
        if who == "owner":
            session = str(meta.get("session") or "")
            if session and not _SESSION.match(session):
                raise InvalidParamsError(message=f"bad session name {session!r}")
            sender = f"owner:{session}" if session else "owner"
        else:
            as_ = str(meta.get("as") or "")
            if not ISSUE_KEY.match(as_):
                raise InvalidParamsError(message="a host token must name its issue in 'as'")
            sender = f"{as_}@{who.removeprefix('host:')}"
        now = self.now()
        con = self._inbox()
        reply_to = str(meta.get("reply_to") or "")
        if kind == "reply" and reply_to and inbox.get(con, reply_to) is not None:
            stored = inbox.store_reply(con, reply_to, sender.split("@")[0], body)
            if stored is None:
                raise InvalidParamsError(message=f"{reply_to} was not sent to {sender}")
            return _task(stored, sender=stored.sender)
        if inbox.sent_since(con, who, sender, now - 3600) >= inbox.RATE_PER_HOUR:
            raise RateLimited(
                message=f"{sender} sent {inbox.RATE_PER_HOUR} messages this hour; that is the cap"
            )
        extra = {k: v for k, v in meta.items() if k in ("repo", "paths", "pr", "reply_to")}
        task_id = inbox.add(
            con,
            self.cfg.state_root,
            to_issue=to,
            sender=sender,
            token_id=who,
            kind=kind,
            body=body,
            meta={"to": to, **extra},
            now=now,
        )
        if who == "owner" and kind == "direct":
            self._nudge(to, now)
        stored = inbox.get(con, task_id)
        assert stored is not None
        return _task(stored)

    def _nudge(self, key: str, now: float) -> None:
        try:
            claim = ledger.get_claim(_ledger_ro(self.cfg), key)
        except sqlite3.Error:
            return
        if claim is None or claim.state != "running" or not should_nudge(self.cfg, key, now):
            return
        name = session_name(key, claim.phase)
        if self.sessions.alive(name):
            self.sessions.send(name, NUDGE)

    async def on_get_task(self, params, context):
        who = self._identity(context)
        msg = inbox.get(self._inbox(), params.id)
        if msg is None or msg.token_id != who:
            return None
        return _task(msg)

    async def on_get_extended_agent_card(self, params, context):
        self._identity(context)
        card = AgentCard()
        card.CopyFrom(self.card)
        for c in ledger.live_claims(_ledger_ro(self.cfg)):
            live = c.state == "running" and self.sessions.alive(session_name(c.issue_key, c.phase))
            tags = [f"phase:{c.phase}", f"repo:{c.repo}", "live" if live else "waiting"]
            tags += [f"path:{p}" for p in c.paths]
            card.skills.add(
                id=c.issue_key,
                name=_title(self.cfg, c.issue_key),
                description=f"{c.phase} on {c.repo}",
                tags=tags,
            )
        return card

    async def on_list_tasks(self, params, context):
        raise UnsupportedOperationError()

    async def on_cancel_task(self, params, context):
        raise UnsupportedOperationError()

    async def on_message_send_stream(
        self, params: SendMessageRequest, context: ServerCallContext
    ) -> AsyncGenerator[Event]:
        raise UnsupportedOperationError()
        yield

    async def on_create_task_push_notification_config(self, params, context):
        raise UnsupportedOperationError()

    async def on_get_task_push_notification_config(self, params, context):
        raise UnsupportedOperationError()

    async def on_subscribe_to_task(
        self, params: SubscribeToTaskRequest, context: ServerCallContext
    ) -> AsyncGenerator[Event]:
        raise UnsupportedOperationError()
        yield

    async def on_list_task_push_notification_configs(self, params, context):
        raise UnsupportedOperationError()

    async def on_delete_task_push_notification_config(self, params, context):
        raise UnsupportedOperationError()


def public_card(a2a: A2AConfig) -> AgentCard:
    me = a2a.hosts.get(a2a.name)
    return AgentCard(
        name=a2a.name,
        description=f"flocc host {a2a.name}",
        version="1",
        supported_interfaces=[
            AgentInterface(
                url=me.url if me else "", protocol_binding="HTTP+JSON", protocol_version="1.0"
            )
        ],
        capabilities=AgentCapabilities(streaming=False, extended_agent_card=True),
        default_input_modes=["text/plain"],
        default_output_modes=["text/plain"],
        security_schemes={
            "bearer": SecurityScheme(
                http_auth_security_scheme=HTTPAuthSecurityScheme(scheme="Bearer")
            )
        },
        security_requirements=[SecurityRequirement(schemes={"bearer": StringList()})],
    )


def build_app(cfg: Config, a2a: Callable[[], A2AConfig], sessions, now=time.time) -> Starlette:
    card = public_card(a2a())
    handler = Handler(cfg, a2a, sessions, now, card)
    return Starlette(routes=[*create_agent_card_routes(card), *create_rest_routes(handler)])


def serve(cfg: Config, host: str, port: int) -> None:
    import uvicorn

    from flocc.a2aconf import load
    from flocc.sessions import Sessions

    uvicorn.run(build_app(cfg, load, Sessions()), host=host, port=port, log_level="warning")


def write_served(cfg: Config, head: str) -> None:
    (cfg.state_root / "a2a-serve.json").write_text(json.dumps({"head": head, "at": time.time()}))
