import json
import sqlite3
from contextlib import contextmanager
from dataclasses import asdict, dataclass, fields
from pathlib import Path, PurePosixPath

# Version 0. A ledger moves forward through _MIGRATIONS only; PRAGMA user_version counts the
# entries applied. Never edit an entry once it has shipped: append a new one.
_SCHEMA = """
CREATE TABLE IF NOT EXISTS claims (
  issue_key TEXT PRIMARY KEY,
  issue_id TEXT NOT NULL,
  repo TEXT NOT NULL,
  worktree TEXT NOT NULL UNIQUE,
  branch TEXT NOT NULL,
  phase TEXT NOT NULL,
  state TEXT NOT NULL,
  paths TEXT NOT NULL,
  depends_on TEXT NOT NULL,
  settings_json TEXT NOT NULL,
  started_at REAL NOT NULL,
  wall_clock_s INTEGER NOT NULL,
  restarts INTEGER NOT NULL DEFAULT 0,
  fix_attempts INTEGER NOT NULL DEFAULT 0,
  base_sha TEXT,
  tester_sha TEXT,
  last_comment_at TEXT,
  escalation_offset INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS alerts (
  cause TEXT PRIMARY KEY,
  tier INTEGER NOT NULL,
  issue_key TEXT,
  last_seen REAL NOT NULL,
  count INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS digest (
  id INTEGER PRIMARY KEY,
  at REAL NOT NULL,
  issue_key TEXT,
  line TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS usage_log (
  id INTEGER PRIMARY KEY,
  issue_key TEXT NOT NULL,
  phase TEXT NOT NULL,
  event TEXT NOT NULL,
  at REAL NOT NULL,
  five_hour_pct REAL NOT NULL,
  weekly_pct REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
"""
_MIGRATIONS: tuple[tuple[str, ...], ...] = (
    (
        """CREATE TABLE events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,
  issue_key TEXT,
  repo TEXT,
  detail TEXT NOT NULL DEFAULT '',
  at REAL NOT NULL,
  printed_at REAL,
  acked_at REAL
)""",
        """CREATE TABLE holds (
  issue_key TEXT PRIMARY KEY,
  reason TEXT NOT NULL,
  at REAL NOT NULL
)""",
    ),
    ("ALTER TABLE claims DROP COLUMN escalation_offset",),
)
_JSON_FIELDS = ("paths", "depends_on")
LIVE_STATES = ("running", "blocked")


@dataclass
class Claim:
    issue_key: str
    issue_id: str
    repo: str
    worktree: str
    branch: str
    phase: str
    state: str
    paths: list[str]
    depends_on: list[str]
    settings_json: str
    started_at: float
    wall_clock_s: int
    restarts: int = 0
    fix_attempts: int = 0
    base_sha: str | None = None
    tester_sha: str | None = None
    last_comment_at: str | None = None


_CLAIM_FIELDS = frozenset(f.name for f in fields(Claim))


@contextmanager
def transaction(con: sqlite3.Connection):
    con.execute("BEGIN IMMEDIATE")
    try:
        yield
    except BaseException:
        con.execute("ROLLBACK")
        raise
    con.execute("COMMIT")


def connect(path: Path | str) -> sqlite3.Connection:
    # Autocommit plus explicit BEGIN IMMEDIATE: the tick and the pre-commit hook write from
    # separate processes, and a deferred transaction can deadlock upgrading to a write lock.
    con = sqlite3.connect(path, timeout=10, isolation_level=None)
    con.row_factory = sqlite3.Row
    con.execute("PRAGMA journal_mode=WAL")
    con.execute("PRAGMA busy_timeout=10000")
    con.executescript(_SCHEMA)
    _migrate(con)
    return con


def _version(con: sqlite3.Connection) -> int:
    return con.execute("PRAGMA user_version").fetchone()[0]


def _migrate(con: sqlite3.Connection) -> None:
    if _version(con) >= len(_MIGRATIONS):
        return
    # Read again under the write lock: the tick and a session's hook can open an old ledger at
    # the same moment, and the second must not apply a migration the first just applied.
    with transaction(con):
        start = _version(con)
        for n, statements in enumerate(_MIGRATIONS[start:], start + 1):
            for sql in statements:
                con.execute(sql)
            con.execute(f"PRAGMA user_version = {n}")


def _from_row(row: sqlite3.Row) -> Claim:
    # status reads the ledger read-only, so before the first tick after an upgrade it still sees
    # columns a migration has yet to drop.
    data = {k: v for k, v in dict(row).items() if k in _CLAIM_FIELDS}
    for key in _JSON_FIELDS:
        data[key] = json.loads(data[key])
    return Claim(**data)


def _encode(values: dict) -> dict:
    return {k: json.dumps(v) if k in _JSON_FIELDS else v for k, v in values.items()}


def insert_claim(con: sqlite3.Connection, claim: Claim) -> None:
    data = _encode(asdict(claim))
    cols = ", ".join(data)
    marks = ", ".join(f":{k}" for k in data)
    with transaction(con):
        con.execute(f"INSERT INTO claims ({cols}) VALUES ({marks})", data)


def get_claim(con: sqlite3.Connection, key: str) -> Claim | None:
    row = con.execute("SELECT * FROM claims WHERE issue_key = ?", (key,)).fetchone()
    return _from_row(row) if row else None


def live_claims(con: sqlite3.Connection) -> list[Claim]:
    rows = con.execute(
        "SELECT * FROM claims WHERE state IN (?, ?) ORDER BY started_at, issue_key", LIVE_STATES
    )
    return [_from_row(r) for r in rows]


def finished_claims(con: sqlite3.Connection) -> list[Claim]:
    rows = con.execute(
        "SELECT * FROM claims WHERE state IN ('done', 'timed_out') ORDER BY started_at, issue_key"
    )
    return [_from_row(r) for r in rows]


def claim_for_worktree(con: sqlite3.Connection, path: str) -> Claim | None:
    row = con.execute("SELECT * FROM claims WHERE worktree = ?", (path,)).fetchone()
    return _from_row(row) if row else None


def update_claim(con: sqlite3.Connection, key: str, **values) -> None:
    allowed = {f.name for f in fields(Claim)} - {"issue_key"}
    bad = values.keys() - allowed
    if bad:
        raise KeyError(f"unknown claim fields {sorted(bad)}")
    data = _encode(values)
    sets = ", ".join(f"{k} = :{k}" for k in data)
    with transaction(con):
        con.execute(f"UPDATE claims SET {sets} WHERE issue_key = :_key", {**data, "_key": key})


def in_repo(claims: list[Claim], repo: str) -> list[Claim]:
    """Paths are relative to a repo root, so claims in another repo never clash with them."""
    return [c for c in claims if c.repo == repo]


def is_shared(name: str, shared: tuple[str, ...]) -> bool:
    path = PurePosixPath(name)
    return any(name == g or path.full_match(g) for g in shared)


def unshared(globs: list[str], shared: tuple[str, ...]) -> list[str]:
    return [g for g in globs if not is_shared(g, shared)]


def add_paths(con: sqlite3.Connection, key: str, globs: list[str]) -> dict[str, str]:
    """Adds globs to the claim unless another live claim overlaps; returns the clashes."""
    # Read, check and write share one transaction so two agents claiming at once cannot both win.
    with transaction(con):
        mine = get_claim(con, key)
        if mine is None:
            raise KeyError(key)
        clash = conflicting_claims(globs, in_repo(live_claims(con), mine.repo), exclude=key)
        if not clash:
            paths = mine.paths + [g for g in globs if g not in mine.paths]
            con.execute("UPDATE claims SET paths = ? WHERE issue_key = ?", (json.dumps(paths), key))
    return clash


def delete_claim(con: sqlite3.Connection, key: str) -> None:
    with transaction(con):
        con.execute("DELETE FROM claims WHERE issue_key = ?", (key,))


def _literal_prefix(glob: str) -> str:
    cut = [i for i in (glob.find(c) for c in "*?[") if i >= 0]
    return glob[: min(cut)] if cut else glob


def globs_overlap(a: str, b: str) -> bool:
    # Conservative: compares the literal text before the first wildcard, so it can report an
    # overlap that real matching would not (a.py vs a.pyc) but never misses one.
    pa, pb = _literal_prefix(a), _literal_prefix(b)
    return pa.startswith(pb) or pb.startswith(pa)


def conflicting_claims(paths, claims, exclude: str | None = None) -> dict[str, str]:
    out: dict[str, str] = {}
    for glob in paths:
        for claim in claims:
            if claim.issue_key != exclude and any(globs_overlap(glob, g) for g in claim.paths):
                out[glob] = claim.issue_key
                break
    return out


def file_conflicts(files, claims, exclude: str | None) -> dict[str, str]:
    out: dict[str, str] = {}
    for name in files:
        path = PurePosixPath(name)
        # A directory match claims everything under it, so `src/b*` covers src/billing/tax.py as
        # globs_overlap already assumes at admission.
        candidates = (path, *path.parents[:-1])
        for claim in claims:
            if claim.issue_key != exclude and any(
                p.full_match(g) for g in claim.paths for p in candidates
            ):
                out[name] = claim.issue_key
                break
    return out


DEDUPE_S = 24 * 3600


def alert_seen(con: sqlite3.Connection, cause: str, within_s: float, now: float) -> bool:
    row = con.execute("SELECT last_seen FROM alerts WHERE cause = ?", (cause,)).fetchone()
    return bool(row) and now - row["last_seen"] <= within_s


def record_alert(con, cause: str, tier: int, issue_key: str | None, now: float) -> None:
    with transaction(con):
        con.execute(
            "INSERT INTO alerts (cause, tier, issue_key, last_seen, count) VALUES (?, ?, ?, ?, 1)"
            " ON CONFLICT(cause) DO UPDATE SET last_seen = excluded.last_seen, count = count + 1",
            (cause, tier, issue_key, now),
        )


def clear_alert(con, cause: str) -> None:
    with transaction(con):
        con.execute("DELETE FROM alerts WHERE cause = ?", (cause,))


def clear_issue_alerts(con, issue_key: str) -> None:
    prefix = f"{issue_key}:"
    with transaction(con):
        con.execute("DELETE FROM alerts WHERE substr(cause, 1, ?) = ?", (len(prefix), prefix))


def add_digest(con, issue_key: str | None, line: str, now: float) -> None:
    with transaction(con):
        con.execute(
            "INSERT INTO digest (at, issue_key, line) VALUES (?, ?, ?)", (now, issue_key, line)
        )


def pending_digest(con) -> list[tuple[int, str | None, str]]:
    rows = con.execute("SELECT id, issue_key, line FROM digest ORDER BY id").fetchall()
    return [(r["id"], r["issue_key"], r["line"]) for r in rows]


def clear_digest(con, upto_id: int) -> None:
    with transaction(con):
        con.execute("DELETE FROM digest WHERE id <= ?", (upto_id,))


def add_usage(
    con, issue_key: str, phase: str, event: str, five_hour_pct: float, weekly_pct: float, now: float
) -> None:
    with transaction(con):
        con.execute(
            "INSERT INTO usage_log (issue_key, phase, event, at, five_hour_pct, weekly_pct)"
            " VALUES (?, ?, ?, ?, ?, ?)",
            (issue_key, phase, event, now, five_hour_pct, weekly_pct),
        )


def usage_for(con, issue_key: str) -> list[tuple[str, str, float, float]]:
    rows = con.execute(
        "SELECT phase, event, five_hour_pct, weekly_pct FROM usage_log WHERE issue_key = ?"
        " ORDER BY id",
        (issue_key,),
    )
    return [(r["phase"], r["event"], r["five_hour_pct"], r["weekly_pct"]) for r in rows]


def meta_get(con, key: str) -> str | None:
    row = con.execute("SELECT value FROM meta WHERE key = ?", (key,)).fetchone()
    return row["value"] if row else None


# In meta, not on the claim: release deletes the claim, and a rerun on the kept branch needs the
# tester commit to resume at the implementer with the tests_touched guard intact.
def remember_tests(con, issue_key: str, base_sha: str, tester_sha: str) -> None:
    meta_set(con, f"tests:{issue_key}", json.dumps([base_sha, tester_sha]))
    # An acceptance approved the previous tester's files, not these.
    with transaction(con):
        con.execute("DELETE FROM meta WHERE key = ?", (f"tests-accepted:{issue_key}",))


# The owner's approval of the tester's files as they stand at `sha`: the publisher's guard then
# compares those files against `sha` instead of the tester's commit.
def accept_tests(con, issue_key: str, sha: str) -> None:
    meta_set(con, f"tests-accepted:{issue_key}", sha)


def accepted_tests(con, issue_key: str) -> str | None:
    return meta_get(con, f"tests-accepted:{issue_key}")


def remembered_tests(con, issue_key: str) -> tuple[str, str] | None:
    raw = meta_get(con, f"tests:{issue_key}")
    if raw is None:
        return None
    base_sha, tester_sha = json.loads(raw)
    return base_sha, tester_sha


def forget_tests(con, issue_key: str) -> None:
    with transaction(con):
        con.execute(
            "DELETE FROM meta WHERE key IN (?, ?)",
            (f"tests:{issue_key}", f"tests-accepted:{issue_key}"),
        )


def held_keys(con) -> frozenset[str]:
    """Issues whose finished work waits on a broken base branch (see supervisor.BASE_HELD)."""
    rows = con.execute("SELECT key FROM meta WHERE key LIKE 'base_held:%' AND value != ''")
    return frozenset(r["key"].split(":", 1)[1] for r in rows)


def meta_set(con, key: str, value: str) -> None:
    with transaction(con):
        con.execute(
            "INSERT INTO meta (key, value) VALUES (?, ?)"
            " ON CONFLICT(key) DO UPDATE SET value = excluded.value",
            (key, value),
        )
