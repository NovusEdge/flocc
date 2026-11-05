import json
from collections.abc import Callable
from dataclasses import dataclass
from itertools import batched

from flocc import ledger

KINDS = frozenset(
    {
        "ended",
        "cap",
        "needs-owner",
        "base-broken",
        "bad-sequence",
        "service-error",
        "daily",
        "host-pressure",
    }
)
BATCH_S = 120
REDELIVER_S = 30 * 60
REDELIVERIES = 3
POLL_S = 5.0
DETAIL_CHARS = 300
# Bounds one monitor notification, which cardinal reads whole.
PER_LINE = 50
KEEP_ACKED_S = 30 * 86400
_COLUMNS = "id, kind, issue_key, repo, detail, at, printed_at, acked_at, prints"


@dataclass(frozen=True)
class Event:
    id: int
    kind: str
    issue_key: str | None
    repo: str | None
    detail: str
    at: float
    printed_at: float | None
    acked_at: float | None
    prints: int


def emit(
    con,
    kind: str,
    now: float,
    *,
    issue_key: str | None = None,
    repo: str | None = None,
    detail: str = "",
) -> int:
    if kind not in KINDS:
        raise ValueError(f"unknown event kind {kind!r}")
    with ledger.transaction(con):
        cur = con.execute(
            "INSERT INTO events (kind, issue_key, repo, detail, at) VALUES (?, ?, ?, ?, ?)",
            (kind, issue_key, repo, detail[:DETAIL_CHARS], now),
        )
    return int(cur.lastrowid or 0)


def unacked(con) -> list[Event]:
    rows = con.execute(f"SELECT {_COLUMNS} FROM events WHERE acked_at IS NULL ORDER BY id")
    return [Event(*r) for r in rows]


def since(con, t: float) -> list[Event]:
    rows = con.execute(f"SELECT {_COLUMNS} FROM events WHERE at >= ? ORDER BY id", (t,))
    return [Event(*r) for r in rows]


ATTENTION_S = BATCH_S + REDELIVERIES * REDELIVER_S


def attention(con, now: float) -> list[Event]:
    """Printed events still unacknowledged after the batch close and every redelivery. Judged by
    age, not by the print count, which also grows with each reconnect of a flapping link."""
    rows = con.execute(
        f"SELECT {_COLUMNS} FROM events WHERE acked_at IS NULL AND printed_at IS NOT NULL"
        " AND at <= ? ORDER BY id",
        (now - ATTENTION_S,),
    )
    return [Event(*r) for r in rows]


def line(batch, now: float) -> str:
    return json.dumps(
        {
            "events": [
                {
                    "id": e.id,
                    "kind": e.kind,
                    "issue": e.issue_key,
                    "repo": e.repo,
                    "detail": e.detail,
                    "age_s": int(max(0.0, now - e.at)),
                }
                for e in batch
            ]
        }
    )


def lines(batch, now: float) -> list[str]:
    return [line(chunk, now) for chunk in batched(batch, PER_LINE)]


def mark_printed(con, ids: list[int], now: float) -> None:
    if not ids:
        return
    marks = ", ".join("?" * len(ids))
    with ledger.transaction(con):
        con.execute(
            f"UPDATE events SET printed_at = ?, prints = prints + 1 WHERE id IN ({marks})",
            (now, *ids),
        )


def ack(con, ids: list[int], now: float) -> tuple[int, list[int]]:
    """The count newly acknowledged and the ids that name no event; acking twice changes nothing."""
    wanted = sorted(set(ids))
    marks = ", ".join("?" * len(wanted))
    with ledger.transaction(con):
        known = {r[0] for r in con.execute(f"SELECT id FROM events WHERE id IN ({marks})", wanted)}
        cur = con.execute(
            f"UPDATE events SET acked_at = ? WHERE acked_at IS NULL AND id IN ({marks})",
            (now, *wanted),
        )
    return cur.rowcount, [i for i in wanted if i not in known]


def prune(con, now: float) -> int:
    with ledger.transaction(con):
        cur = con.execute(
            "DELETE FROM events WHERE acked_at IS NOT NULL AND acked_at <= ?",
            (now - KEEP_ACKED_S,),
        )
    return cur.rowcount


class Follower:
    """One connection's record of what it printed and when. Followers never share it: after a
    reconnect the old connection can linger on the executor, and an event it printed into a dead
    socket must still be fresh to the live one. Overlap only duplicates, which ack absorbs."""

    def __init__(self, con, write: Callable[[str], None]):
        self.con, self.write = con, write
        self.printed: dict[int, tuple[float, int]] = {}

    def due(
        self, now: float, batch_s: float = BATCH_S, redeliver_s: float = REDELIVER_S
    ) -> list[Event]:
        pending = unacked(self.con)
        self.printed = {e.id: self.printed[e.id] for e in pending if e.id in self.printed}
        fresh = [e for e in pending if e.id not in self.printed]
        stale = [
            e
            for e in pending
            if e.id in self.printed
            and now - self.printed[e.id][0] >= redeliver_s
            and self.printed[e.id][1] <= REDELIVERIES
        ]
        closed = bool(fresh) and now - min(e.at for e in fresh) >= batch_s
        return sorted([*(fresh if closed else []), *stale], key=lambda e: e.id)

    def connect(self, now: float) -> None:
        self._print(unacked(self.con), now)

    def poll(self, now: float, batch_s: float = BATCH_S, redeliver_s: float = REDELIVER_S) -> None:
        self._print(self.due(now, batch_s, redeliver_s), now)

    def _print(self, batch: list[Event], now: float) -> None:
        for chunk in batched(batch, PER_LINE):
            self.write(line(chunk, now))
            # After the write: a line that never left counts as not printed.
            mark_printed(self.con, [e.id for e in chunk], now)
            for e in chunk:
                self.printed[e.id] = (now, self.printed.get(e.id, (0.0, 0))[1] + 1)


def follow(
    con,
    write: Callable[[str], None],
    now: Callable[[], float],
    sleep: Callable[[float], None],
    *,
    alive: Callable[[], bool],
    hello: bool = False,
    poll_s: float = POLL_S,
    batch_s: float = BATCH_S,
    redeliver_s: float = REDELIVER_S,
) -> None:
    if hello:
        write(json.dumps({"link": "up"}))
    follower = Follower(con, write)
    follower.connect(now())
    while alive():
        sleep(poll_s)
        follower.poll(now(), batch_s, redeliver_s)
