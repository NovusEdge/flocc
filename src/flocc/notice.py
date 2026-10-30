import sys
from collections.abc import Callable
from dataclasses import dataclass

from flocc import events, ledger
from flocc.linear import Issue


@dataclass(frozen=True)
class Notice:
    cause: str
    summary: str
    action: str = ""
    issue_key: str | None = None
    # One of events.KINDS when cardinal must hear of it; "" for a comment or digest line only.
    event: str = ""

    def text(self) -> str:
        return f"{self.summary}\nAction: {self.action}" if self.action else self.summary


class Notifier:
    """Tells the owner about a problem: on its issue when it has one, else in the daily digest,
    and cardinal through an event when the notice names one."""

    def __init__(self, con, linear, now: Callable[[], float]):
        self.con, self.linear, self.now = con, linear, now

    def send(self, notice: Notice, issue: Issue | None = None) -> None:
        print(f"{notice.issue_key or '-'} {notice.cause}: {notice.summary}", file=sys.stderr)
        if issue is not None:
            self.linear.comment(issue.id, notice.text())
        else:
            ledger.add_digest(self.con, notice.issue_key, notice.text(), self.now())
        # After the comment: a Linear outage repeats the send every tick, and a retry must not
        # wake cardinal again.
        if notice.event:
            cause = notice.cause.rsplit(":", 1)[-1]
            events.emit(
                self.con,
                notice.event,
                self.now(),
                issue_key=notice.issue_key,
                detail=f"{cause}: {notice.summary}",
            )
