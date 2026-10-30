import json

import pytest

from flocc.idle import clear_idle, has_background, note_background, note_idle


def _assistant(*blocks):
    return json.dumps({"type": "assistant", "message": {"content": list(blocks)}})


def test_idle_message_skips_assistant_lines_without_text(tmp_path):
    transcript = tmp_path / "t.jsonl"
    transcript.write_text(
        "\n".join(
            [
                _assistant({"type": "text", "text": "waiting on you"}),
                _assistant({"type": "tool_use", "name": "Bash"}),
                _assistant({"type": "thinking", "thinking": "hm"}),
            ]
        )
    )
    note_idle(tmp_path, {"transcript_path": str(transcript)}, 5.0)
    assert json.loads((tmp_path / "idle").read_text()) == {"at": 5.0, "message": "waiting on you"}


@pytest.mark.parametrize(
    "content",
    [None, b"\xff\xfe", json.dumps({"type": "assistant", "message": "text"}), "[1]"],
    ids=["missing", "undecodable", "message-not-a-dict", "line-not-an-object"],
)
def test_idle_marker_is_written_when_the_transcript_cannot_be_read(tmp_path, content):
    transcript = tmp_path / "t.jsonl"
    if isinstance(content, bytes):
        transcript.write_bytes(content)
    elif content is not None:
        transcript.write_text(content)
    note_idle(tmp_path, {"transcript_path": str(transcript)}, 5.0)
    marker = json.loads((tmp_path / "idle").read_text())
    assert marker["at"] == 5.0 and marker["message"] == ""


def test_background_marker_follows_the_last_stop(tmp_path):
    note_background(tmp_path, {"background_tasks": [{"type": "shell"}]})
    assert has_background(tmp_path)
    note_background(tmp_path, {"background_tasks": []})
    assert not has_background(tmp_path)


def test_a_scheduled_wakeup_is_recorded_as_background(tmp_path):
    note_background(tmp_path, {"background_tasks": [], "session_crons": [{"id": "c"}]})
    assert has_background(tmp_path)


def test_clear_idle_removes_the_marker_and_its_notice_mark(tmp_path):
    (tmp_path / "idle").write_text("{}")
    (tmp_path / "idle-escalated").write_text("1.0")
    clear_idle(tmp_path)
    assert not (tmp_path / "idle").exists() and not (tmp_path / "idle-escalated").exists()
