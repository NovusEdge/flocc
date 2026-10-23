import json
import urllib.error
import urllib.request
import uuid

from flocc.a2aconf import Peer

TIMEOUT_S = 3


class A2AError(RuntimeError):
    pass


class Unreachable(A2AError):
    pass


def _call(peer: Peer, token: str, method: str, path: str, body: dict | None = None) -> dict:
    req = urllib.request.Request(
        peer.url + path,
        method=method,
        data=None if body is None else json.dumps(body).encode(),
        headers={
            # a2a-sdk reads a missing version header as 0.3 and refuses the call.
            "A2A-Version": "1.0",
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            return json.loads(resp.read())
    except urllib.error.HTTPError as exc:
        try:
            message = json.loads(exc.read())["error"]["message"]
        except (ValueError, KeyError, TypeError):
            message = f"HTTP {exc.code}"
        raise A2AError(f"{peer.name}: {message}") from exc
    except (OSError, ValueError) as exc:
        raise Unreachable(f"{peer.name} did not answer: {exc}") from exc


def extended_card(peer: Peer, token: str) -> dict:
    return _call(peer, token, "GET", "/extendedAgentCard")


def send(
    peer: Peer,
    token: str,
    *,
    to: str,
    body: str,
    kind: str = "direct",
    sender_as: str | None = None,
    session: str | None = None,
    extra: dict | None = None,
) -> dict:
    meta = {"kind": kind, "to": to, **(extra or {})}
    if sender_as:
        meta["as"] = sender_as
    if session:
        meta["session"] = session
    message = {
        "messageId": str(uuid.uuid4()),
        "role": "ROLE_USER",
        "parts": [{"text": body}, {"data": {"flocc": meta}}],
    }
    return _call(peer, token, "POST", "/message:send", {"message": message})["task"]


def get_task(peer: Peer, token: str, task_id: str) -> dict:
    return _call(peer, token, "GET", f"/tasks/{task_id}")


def state(task: dict) -> str:
    return str((task.get("status") or {}).get("state", "")).removeprefix("TASK_STATE_")


def reply_text(task: dict) -> str | None:
    for art in task.get("artifacts") or []:
        if art.get("name") == "reply":
            for part in art.get("parts") or []:
                if "text" in part:
                    return part["text"]
    return None


def claims(card: dict) -> list[dict]:
    out = []
    for skill in card.get("skills") or []:
        tags = skill.get("tags") or []
        value = dict(t.split(":", 1) for t in tags if ":" in t and not t.startswith("path:"))
        out.append(
            {
                "issue": skill.get("id", ""),
                "title": skill.get("name", ""),
                "phase": value.get("phase", ""),
                "repo": value.get("repo", ""),
                "paths": [t.removeprefix("path:") for t in tags if t.startswith("path:")],
                "live": "live" in tags,
            }
        )
    return out
