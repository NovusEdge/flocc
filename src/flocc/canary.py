import json

from flocc import events, ledger

HOLD_S = 24 * 3600
OUTPUT_CHARS = 1500


def _key(repo: str) -> str:
    return f"main_broken:{repo}"


# The commit last reported broken. A mark lapses after HOLD_S and comes back at the same commit
# while the break lasts; only a pass (clear) or forget_all ends the break.
def _event_key(repo: str) -> str:
    return f"base_broken_event:{repo}"


def mark(con, repo: str, sha: str, now: float, output: str, check: str | None = None) -> None:
    value = {"sha": sha, "at": now, "output": output[-OUTPUT_CHARS:]}
    if check:
        value["check"] = check
    ledger.meta_set(con, _key(repo), json.dumps(value))
    if ledger.meta_get(con, _event_key(repo)) != sha:
        what = f"`{check}`" if check else "./setup"
        events.emit(con, "base-broken", now, repo=repo, detail=f"{what} fails at {sha[:9]}")
        ledger.meta_set(con, _event_key(repo), sha)


def get_mark(con, repo: str) -> dict | None:
    raw = ledger.meta_get(con, _key(repo))
    try:
        m = json.loads(raw) if raw else None
    except ValueError:
        return None
    return m if isinstance(m, dict) and {"sha", "at"} <= m.keys() else None


def clear(con, repo: str) -> None:
    ledger.meta_set(con, _key(repo), "")
    ledger.meta_set(con, _event_key(repo), "")


def forget_all(con) -> int:
    rows = con.execute(
        "SELECT key FROM meta WHERE key LIKE 'main_broken:%' AND value != ''"
    ).fetchall()
    for row in rows:
        ledger.meta_set(con, row["key"], "")
    for row in con.execute("SELECT key FROM meta WHERE key LIKE 'base_broken_event:%'").fetchall():
        ledger.meta_set(con, row["key"], "")
    return len(rows)


def hold_reason(
    con, repo: str, base_branch: str, current_sha: str | None, now: float
) -> str | None:
    m = get_mark(con, repo)
    if m is None:
        return None
    # A fix on the host moves no commit, so a mark also lapses with time; the next issue's
    # ./setup then tests main again.
    if now - float(m["at"]) >= HOLD_S or (current_sha is not None and current_sha != m["sha"]):
        ledger.meta_set(con, _key(repo), "")
        return None
    what = f"`{m['check']}`" if m.get("check") else "./setup"
    return (
        f"{what} fails on {base_branch} at {m['sha'][:9]}; after fixing it, run flocc canary {repo}"
    )
