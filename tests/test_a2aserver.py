import hashlib
import json
from dataclasses import replace

import pytest
from starlette.testclient import TestClient

from flocc import inbox, ledger
from flocc.a2aconf import A2AConfig, Peer
from flocc.a2aserver import NUDGE, build_app
from tests.fakes import FakeSessions
from tests.test_ledger import make

def _id(token):
    return hashlib.sha256(token.encode()).hexdigest()


V = {"A2A-Version": "1.0"}
OWNER = {**V, "Authorization": "Bearer t-owner"}
HOST = {**V, "Authorization": "Bearer t-h1"}
PEER = {**V, "Authorization": "Bearer t-h2"}


@pytest.fixture
def env(tmp_cfg):
    a2a = A2AConfig(
        "h1",
        "t-h1",
        {_id("t-owner"): "owner", _id("t-h1"): "host:h1", _id("t-h2"): "host:h2"},
        {"h1": Peer("h1", "http://h1:7443", ("backend",))},
    )
    con = ledger.connect(tmp_cfg.ledger_path)
    ledger.insert_claim(
        con, replace(make("NF-1"), repo="backend", phase="impl", paths=["src/a.py"])
    )
    d = tmp_cfg.state_root / "NF-1"
    d.mkdir()
    (d / "context.md").write_text("# NF-1: Fix rounding\n\nbody\n")
    sessions = FakeSessions(alive_names={"NF-1"})
    client = TestClient(build_app(tmp_cfg, lambda: a2a, sessions, lambda: 1000.0))
    return client, tmp_cfg, sessions


def _send(client, headers, to="NF-1", body="hello", **flocc):
    msg = {
        "messageId": "m1",
        "role": "ROLE_USER",
        "parts": [{"text": body}, {"data": {"flocc": {"kind": "direct", "to": to, **flocc}}}],
    }
    return client.post("/message:send", headers=headers, json={"message": msg})


def test_the_public_card_names_no_issue(env):
    client, _, _ = env
    card = client.get("/.well-known/agent-card.json").json()
    assert card["name"] == "h1" and not card.get("skills")
    assert card["capabilities"]["extendedAgentCard"] is True


def test_the_extended_card_needs_a_token_and_lists_claims(env):
    client, _, _ = env
    assert client.get("/extendedAgentCard", headers=V).status_code == 401
    (skill,) = client.get("/extendedAgentCard", headers=OWNER).json()["skills"]
    assert (skill["id"], skill["name"]) == ("NF-1", "Fix rounding")
    assert {"phase:impl", "repo:backend", "live", "path:src/a.py"} <= set(skill["tags"])


def test_send_stores_the_row_and_sets_the_sender_from_the_token(env):
    client, cfg, _ = env
    r = _send(client, HOST, **{"as": "NF-2", "from": "owner"})
    assert r.status_code == 200
    task = r.json()["task"]
    assert task["status"]["state"] == "TASK_STATE_SUBMITTED"
    con = inbox.connect(inbox.db_path(cfg.state_root))
    msg = inbox.get(con, task["id"])
    assert msg is not None
    assert (msg.sender, msg.token_id, msg.body) == ("NF-2@h1", "host:h1", "hello")


def test_a_host_sender_must_name_its_issue(env):
    client, _, _ = env
    r = _send(client, HOST)
    assert r.status_code == 400 and "as" in r.json()["error"]["message"]


def test_unknown_tokens_get_401(env):
    client, _, _ = env
    # The accept map holds digests; a digest is not a token.
    assert _send(client, {**V, "Authorization": f"Bearer {_id('t-owner')}"}).status_code == 401
    bad = {**V, "Authorization": "Bearer nope"}
    assert _send(client, bad).status_code == 401
    assert _send(client, V).status_code == 401


def test_the_body_limit(env):
    client, _, _ = env
    r = _send(client, OWNER, body="x" * (inbox.BODY_LIMIT + 1))
    assert r.status_code == 400 and "8192" in r.json()["error"]["message"]


def test_the_rate_cap(env):
    client, _, _ = env
    for _ in range(inbox.RATE_PER_HOUR):
        assert _send(client, HOST, **{"as": "NF-2"}).status_code == 200
    r = _send(client, HOST, **{"as": "NF-2"})
    assert r.status_code == 429 and "20" in r.json()["error"]["message"]


def test_get_task_answers_only_its_sender(env):
    client, _, _ = env
    task_id = _send(client, OWNER).json()["task"]["id"]
    assert client.get(f"/tasks/{task_id}", headers=OWNER).status_code == 200
    assert client.get(f"/tasks/{task_id}", headers=PEER).status_code == 404


def test_a_reply_is_stored_on_the_original_task(env):
    client, _, _ = env
    task_id = _send(client, PEER, **{"as": "NF-7"}).json()["task"]["id"]
    r = _send(
        client, HOST, to="NF-7", body="done", kind="reply", reply_to=task_id, **{"as": "NF-1"}
    )
    assert r.status_code == 200
    assert r.json()["task"]["metadata"]["from"] == "NF-7@h2"
    got = client.get(f"/tasks/{task_id}", headers=PEER).json()
    assert got["artifacts"][0]["parts"][0]["text"] == "done"


def test_an_owner_message_nudges_an_idle_session(env):
    client, cfg, sessions = env
    (cfg.state_root / "NF-1" / "idle").write_text(json.dumps({"at": 1.0}))
    _send(client, OWNER)
    assert sessions.sent == [("NF-1", NUDGE)]


@pytest.mark.parametrize("blocker", ["asking", "not-idle", "agent-sender", "desktop-agent"])
def test_no_nudge(env, blocker):
    client, cfg, sessions = env
    d = cfg.state_root / "NF-1"
    if blocker != "not-idle":
        (d / "idle").write_text(json.dumps({"at": 1.0}))
    if blocker == "asking":
        (d / "asking").write_text("{}")
    if blocker == "agent-sender":
        _send(client, HOST, **{"as": "NF-2"})
    elif blocker == "desktop-agent":
        _send(client, OWNER, session="kairos-voice")
    else:
        _send(client, OWNER)
    assert sessions.sent == []


def test_a_same_host_reply_is_queued_to_the_sender(env):
    client, cfg, _ = env
    task_id = _send(client, HOST, to="NF-7", **{"as": "NF-1"}).json()["task"]["id"]
    r = _send(
        client, HOST, to="NF-1", body="done", kind="reply", reply_to=task_id, **{"as": "NF-7"}
    )
    assert r.status_code == 200 and r.json()["task"]["metadata"]["from"] == "NF-1@h1"
    con = inbox.connect(inbox.db_path(cfg.state_root))
    batch, _ = inbox.claim(con, "NF-1", 2000.0)
    assert [(m.sender, m.kind, m.body) for m in batch] == [("NF-7@h1", "reply", "done")]
