import io
import json

import pytest

from flocc import a2aclient, mcp_a2a
from flocc.a2aconf import A2AConfig, Peer

H1 = Peer("h1", "http://h1:7443", ("backend",))
H2 = Peer("h2", "http://h2:7443", ("web",))
CFG = A2AConfig("h1", "t-h1", {}, {"h1": H1, "h2": H2})
CARDS = {
    "h1": {
        "skills": [
            {"id": "NF-1", "name": "A", "tags": ["phase:impl", "repo:backend", "live"]},
            {"id": "NF-2", "name": "B", "tags": ["phase:test", "repo:backend", "live"]},
            {"id": "NF-3", "name": "C", "tags": ["phase:impl", "repo:backend", "waiting"]},
        ]
    },
    "h2": {"skills": [{"id": "NF-4", "name": "D", "tags": ["phase:impl", "repo:web", "live"]}]},
}


@pytest.fixture
def fake(monkeypatch, tmp_path):
    sent = []

    def card(peer, token):
        if peer.name == "down":
            raise a2aclient.Unreachable("down did not answer: timed out")
        return CARDS[peer.name]

    def send(peer, token, **kw):
        sent.append((peer.name, kw))
        return {
            "id": f"t{len(sent)}",
            "status": {"state": "TASK_STATE_SUBMITTED"},
            "metadata": {"from": "NF-9@h2"} if kw.get("kind") == "reply" else {},
        }

    def get_task(peer, token, task_id):
        return {
            "id": task_id,
            "status": {"state": "TASK_STATE_COMPLETED"},
            "artifacts": [{"name": "reply", "parts": [{"text": "ok"}]}],
        }

    monkeypatch.setattr(a2aclient, "extended_card", card)
    monkeypatch.setattr(a2aclient, "send", send)
    monkeypatch.setattr(a2aclient, "get_task", get_task)
    ctx = mcp_a2a.Ctx(CFG, "NF-1", tmp_path / "sent.jsonl", lambda: 100.0)
    return ctx, sent


def test_agents_lists_claims_across_hosts(fake):
    ctx, _ = fake
    out = mcp_a2a.call("agents", {}, ctx)
    assert [c["issue"] for c in out["agents"]] == ["NF-1", "NF-2", "NF-3", "NF-4"]
    assert out["agents"][3]["host"] == "h2"
    assert [c["issue"] for c in mcp_a2a.call("agents", {"repo": "web"}, ctx)["agents"]] == ["NF-4"]


def test_agents_reports_an_unreachable_host(fake):
    ctx, _ = fake
    ctx.cfg = A2AConfig("h1", "t", {}, {"h1": H1, "down": Peer("down", "http://x:1", ())})
    out = mcp_a2a.call("agents", {}, ctx)
    assert out["unreachable"] == {"down": "down did not answer: timed out"}
    assert [c["issue"] for c in out["agents"]] == ["NF-1", "NF-2", "NF-3"]


def test_send_goes_to_the_host_holding_the_claim(fake):
    ctx, sent = fake
    out = mcp_a2a.call("send", {"to": "NF-4", "body": "hi"}, ctx)
    assert out == {"task": "t1", "state": "SUBMITTED", "host": "h2"}
    assert sent[0][0] == "h2" and sent[0][1]["sender_as"] == "NF-1"
    assert json.loads(ctx.sent_log.read_text())["task"] == "t1"


def test_send_to_an_unclaimed_issue_needs_a_repo_with_several_hosts(fake):
    ctx, sent = fake
    with pytest.raises(mcp_a2a.ToolError, match="repo"):
        mcp_a2a.call("send", {"to": "NF-50", "body": "hi"}, ctx)
    assert mcp_a2a.call("send", {"to": "NF-50", "body": "hi", "repo": "web"}, ctx)["host"] == "h2"


def test_a_body_over_the_limit_is_refused_before_sending(fake):
    ctx, sent = fake
    with pytest.raises(mcp_a2a.ToolError, match="8192"):
        mcp_a2a.call("send", {"to": "NF-4", "body": "x" * 8193}, ctx)
    assert sent == []


def test_notify_reaches_live_claims_in_the_repo_and_skips_the_sender(fake):
    ctx, sent = fake
    out = mcp_a2a.call("notify", {"repo": "backend", "body": "moved", "paths": ["ci.yml"]}, ctx)
    assert [r["issue"] for r in out["sent"]] == ["NF-2"]
    assert sent[0][1]["kind"] == "notice"
    assert sent[0][1]["extra"] == {"repo": "backend", "paths": ["ci.yml"]}
    assert len(mcp_a2a.call("notify", {"repo": "all", "body": "x"}, ctx)["sent"]) == 2


def test_a_reply_is_stored_then_forwarded_to_the_sender(fake):
    ctx, sent = fake
    mcp_a2a.call("send", {"to": "NF-9", "body": "done", "reply_to": "orig"}, ctx)
    assert sent[0][0] == "h1" and sent[0][1]["extra"] == {"reply_to": "orig"}
    assert sent[1][0] == "h2" and sent[1][1]["to"] == "NF-9" and sent[1][1]["kind"] == "reply"


def test_replies_reads_tasks_this_session_sent(fake):
    ctx, _ = fake
    mcp_a2a.call("send", {"to": "NF-4", "body": "hi"}, ctx)
    (r,) = mcp_a2a.call("replies", {}, ctx)["replies"]
    assert (r["task"], r["state"], r["reply"]) == ("t1", "COMPLETED", "ok")


def test_tools_without_config_say_so(tmp_path):
    ctx = mcp_a2a.Ctx(None, None, tmp_path / "s.jsonl", lambda: 0.0)
    for name in ("agents", "send", "notify", "replies"):
        args = {"to": "NF-1", "body": "x", "repo": "all"}
        with pytest.raises(mcp_a2a.ToolError, match="A2A is not configured on this host"):
            mcp_a2a.call(name, args, ctx)


def test_the_stdio_protocol(fake):
    ctx, _ = fake
    lines = [
        {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2025-06-18",
                "capabilities": {},
                "clientInfo": {"name": "t"},
            },
        },
        {"jsonrpc": "2.0", "method": "notifications/initialized"},
        {"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
        {
            "jsonrpc": "2.0",
            "id": 3,
            "method": "tools/call",
            "params": {"name": "send", "arguments": {"to": "NF-50", "body": "x"}},
        },
    ]
    out = io.StringIO()
    mcp_a2a.serve(io.StringIO("\n".join(json.dumps(x) for x in lines) + "\n"), out, ctx)
    replies = [json.loads(x) for x in out.getvalue().splitlines()]
    assert [r["id"] for r in replies] == [1, 2, 3]
    assert replies[0]["result"]["protocolVersion"] == "2025-06-18"
    names = {t["name"] for t in replies[1]["result"]["tools"]}
    assert names == {"agents", "send", "notify", "replies"}
    assert replies[2]["result"]["isError"] is True
