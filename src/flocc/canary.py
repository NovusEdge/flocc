import json

from flocc import ledger

HOLD_S = 24 * 3600
OUTPUT_CHARS = 1500


def _key(repo: str) -> str:
    return f"main_broken:{repo}"


def mark(con, repo: str, sha: str, now: float, output: str) -> None:
    value = {"sha": sha, "at": now, "output": output[-OUTPUT_CHARS:]}
    ledger.meta_set(con, _key(repo), json.dumps(value))


def get_mark(con, repo: str) -> dict | None:
    raw = ledger.meta_get(con, _key(repo))
    try:
        m = json.loads(raw) if raw else None
    except ValueError:
        return None
    return m if isinstance(m, dict) and {"sha", "at"} <= m.keys() else None


def clear(con, repo: str) -> None:
    ledger.meta_set(con, _key(repo), "")


def hold_reason(
    con, repo: str, base_branch: str, current_sha: str | None, now: float
) -> str | None:
    m = get_mark(con, repo)
    if m is None:
        return None
    # A fix on the host moves no commit, so a mark also lapses with time; the next issue's
    # ./setup then tests main again.
    if now - float(m["at"]) >= HOLD_S or (current_sha is not None and current_sha != m["sha"]):
        clear(con, repo)
        return None
    return f"./setup fails on {base_branch} at {m['sha'][:9]}; after fixing it, run flocc canary {repo}"
