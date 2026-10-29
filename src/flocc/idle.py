import json
import os
from pathlib import Path

IDLE_MESSAGE_CHARS = 600
BACKGROUND = "background"


def _transcript_text(path: str) -> str:
    for line in reversed(Path(path).read_text().splitlines()):
        try:
            entry = json.loads(line)
        except ValueError:
            continue
        if not isinstance(entry, dict) or entry.get("type") != "assistant":
            continue
        content = (entry.get("message") or {}).get("content")
        if isinstance(content, str):
            text = content
        else:
            blocks = content if isinstance(content, list) else []
            texts = (
                b.get("text") for b in blocks if isinstance(b, dict) and b.get("type") == "text"
            )
            text = "\n".join(t for t in texts if isinstance(t, str))
        # A line holding only tool calls says nothing; the turn's last words are further back.
        if text:
            return text
    return ""


def note_idle(state: Path, payload: dict, now: float) -> None:
    message = payload.get("last_assistant_message")
    if not isinstance(message, str) or not message:
        message = ""
        transcript = payload.get("transcript_path")
        if isinstance(transcript, str) and transcript:
            # The marker matters more than the message: write it whatever the transcript holds.
            try:
                message = _transcript_text(transcript)
            except (OSError, ValueError, AttributeError):
                pass
    state.mkdir(parents=True, exist_ok=True)
    # Written whole by rename: the supervisor polls this file.
    tmp = state / "idle.tmp"
    tmp.write_text(json.dumps({"at": now, "message": message[:IDLE_MESSAGE_CHARS]}))
    os.replace(tmp, state / "idle")


def note_background(state: Path, payload: dict) -> None:
    """Records the background tasks a turn ended with; the supervisor treats them as work."""
    kinds = [
        str(t.get("type", "")) for t in payload.get("background_tasks") or [] if isinstance(t, dict)
    ]
    path = state / BACKGROUND
    if not kinds:
        path.unlink(missing_ok=True)
        return
    tmp = state / "background.tmp"
    tmp.write_text(json.dumps(kinds))
    os.replace(tmp, path)


def has_background(state: Path) -> bool:
    try:
        return bool(json.loads((state / BACKGROUND).read_text()))
    except (OSError, ValueError):
        return False


def clear_idle(state: Path) -> None:
    for name in ("idle", "idle-escalated"):
        (state / name).unlink(missing_ok=True)
