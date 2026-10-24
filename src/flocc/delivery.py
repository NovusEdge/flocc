from pathlib import Path

from flocc import inbox

# The tick is about to end a session that wrote either; a message delivered now would die with it.
ENDING = ("done.json", "handoff-ready")
_DIGEST_CHARS = 1500


def take(state: Path, now: float, limit: int = inbox.BATCH_CHARS) -> str:
    marker = state / inbox.MARKER
    # Unlinked before the claim: a row the server adds meanwhile writes the marker again.
    marker.unlink(missing_ok=True)
    try:
        con = inbox.connect(inbox.db_path(state.parent))
        batch, more = inbox.claim(con, state.name, now, limit)
    except Exception:
        marker.touch()
        raise
    if more:
        marker.touch()
    return "\n\n".join(inbox.wrap(m) for m in batch)


def session_text(state: Path, now: float) -> str:
    con = inbox.connect(inbox.db_path(state.parent))
    lines = inbox.digest(con, state.name, now)
    head = ""
    if lines:
        head = "## Messages delivered earlier\n\n" + "\n".join(lines)
        head = head[:_DIGEST_CHARS] + "\n\n"
    mail = take(state, now, inbox.BATCH_CHARS - len(head))
    return head + mail if mail else head.rstrip()
