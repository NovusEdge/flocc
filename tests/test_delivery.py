import io
import json
import time

import pytest

from flocc import delivery, inbox
from flocc.cli import main
from tests.test_cli import gate_env


@pytest.fixture
def state(tmp_path, monkeypatch):
    d = tmp_path / "NF-2"
    d.mkdir()
    gate_env(monkeypatch, d, "impl")
    return d


def _add(state, body="hello", now=100.0):
    con = inbox.connect(inbox.db_path(state.parent))
    return inbox.add(
        con,
        state.parent,
        to_issue=state.name,
        sender="NF-1@h1",
        token_id="host:h1",
        kind="notice",
        body=body,
        meta={},
        now=now,
    )


def _hook(monkeypatch, capsys, command, payload):
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    code = main([command])
    out = capsys.readouterr().out
    return code, (json.loads(out) if out.strip() else None)


def test_post_tool_use_delivers_and_clears_the_marker(state, monkeypatch, capsys):
    _add(state)
    code, out = _hook(monkeypatch, capsys, "inbox", {"hook_event_name": "PostToolUse"})
    assert code == 0 and out is not None
    ctx = out["hookSpecificOutput"]
    assert ctx["hookEventName"] == "PostToolUse" and "hello" in ctx["additionalContext"]
    assert not (state / inbox.MARKER).exists()


def test_post_tool_use_skips_subagent_calls(state, monkeypatch, capsys):
    _add(state)
    code, out = _hook(
        monkeypatch, capsys, "inbox", {"hook_event_name": "PostToolUse", "agent_id": "a1"}
    )
    assert (code, out) == (0, None)
    assert (state / inbox.MARKER).exists()


def test_a_long_queue_leaves_the_marker_for_the_rest(state, monkeypatch, capsys):
    for i in range(3):
        _add(state, body="x" * 4000, now=100.0 + i)
    _, out = _hook(monkeypatch, capsys, "inbox", {"hook_event_name": "PostToolUse"})
    assert out is not None
    assert len(out["hookSpecificOutput"]["additionalContext"]) <= inbox.BATCH_CHARS
    assert (state / inbox.MARKER).exists()


def test_a_broken_inbox_delivers_nothing_and_exits_0(state, monkeypatch, capsys):
    (state / inbox.MARKER).touch()
    inbox.db_path(state.parent).write_text("not a database")
    code, out = _hook(monkeypatch, capsys, "inbox", {"hook_event_name": "PostToolUse"})
    assert (code, out) == (0, None)
    assert (state / inbox.MARKER).exists(), "the rows stay announced for the next hook"


def test_stop_delivers_instead_of_going_idle(state, monkeypatch, capsys):
    _add(state)
    (state / "asking").write_text(json.dumps({"at": 1.0}))
    code, out = _hook(monkeypatch, capsys, "stop", {"hook_event_name": "Stop"})
    assert code == 0 and out is not None
    assert out["hookSpecificOutput"]["hookEventName"] == "Stop"
    assert "hello" in out["hookSpecificOutput"]["additionalContext"]
    assert not (state / "asking").exists(), "the gate's --answered check ran first"
    assert not (state / "idle").exists()


def test_stop_without_mail_goes_idle(state, monkeypatch, capsys):
    code, out = _hook(monkeypatch, capsys, "stop", {"last_assistant_message": "waiting"})
    assert (code, out) == (0, None)
    assert json.loads((state / "idle").read_text())["message"] == "waiting"


@pytest.mark.parametrize("ending", delivery.ENDING)
def test_stop_delivers_nothing_after_done(state, monkeypatch, capsys, ending):
    _add(state)
    (state / ending).write_text("{}")
    code, out = _hook(monkeypatch, capsys, "stop", {})
    assert (code, out) == (0, None)
    assert (state / inbox.MARKER).exists()


def test_session_start_gives_the_digest_and_pending_mail(state, monkeypatch, capsys):
    now = time.time()
    _add(state, body="old news", now=now - 60)
    delivery.take(state, now - 50)
    _add(state, body="fresh", now=now - 40)
    _, out = _hook(
        monkeypatch, capsys, "inbox", {"hook_event_name": "SessionStart", "source": "compact"}
    )
    assert out is not None
    text = out["hookSpecificOutput"]["additionalContext"]
    assert "## Messages delivered earlier" in text and "old news" in text
    assert "fresh" in text and text.index("old news") < text.index("fresh")


def test_every_stop_records_background_tasks(state, monkeypatch, capsys):
    payload = {"background_tasks": [{"id": "b1", "type": "shell"}, "junk"]}
    _hook(monkeypatch, capsys, "stop", payload)
    assert json.loads((state / "background").read_text()) == ["shell"]
    _add(state)
    _hook(monkeypatch, capsys, "stop", {"background_tasks": []})
    assert not (state / "background").exists(), "a stop that delivers mail records it too"
