import re
import shutil

from flocc import ledger
from flocc.config import Config

STATE_AGE_S = 7 * 86400
SCRATCH_AGE_S = 86400
PYTEST_AGE_S = 86400
# cleanup --now, cardinal's repair for a full /tmp. Every claim's scratchpads stay whatever their
# age, so one idle this long belongs to a session that has ended.
NOW_SCRATCH_AGE_S = 600
NOW_LOCK_WAIT_S = 60.0
_KEY = re.compile(r"[A-Z][A-Z0-9]*-[0-9]+")


def cleanup(
    cfg: Config,
    con,
    agent,
    now: float,
    scratch_age_s: float = SCRATCH_AGE_S,
    pytest_age_s: float = PYTEST_AGE_S,
) -> dict:
    """The daily pass: what no claim owns, here and in flocc-agent's home. Run under the tick
    lock, so no claim appears or goes while it runs."""
    claims = [*ledger.live_claims(con), *ledger.finished_claims(con)]
    keep = sorted({c.issue_key for c in claims})
    removed: list[str] = []
    root = cfg.state_root
    for d in sorted(root.iterdir()) if root.is_dir() else []:
        if not _KEY.fullmatch(d.name) or d.name in keep or not d.is_dir() or d.is_symlink():
            continue
        if now - d.lstat().st_mtime >= STATE_AGE_S:
            shutil.rmtree(d, ignore_errors=True)
            removed.append(str(d))
    swept = agent.sweep(keep, now, STATE_AGE_S, scratch_age_s, pytest_age_s)
    return {
        "removed": removed + list(swept["removed"]),
        "kept": list(swept["kept"]),
        "freed": int(swept["freed"]),
    }
