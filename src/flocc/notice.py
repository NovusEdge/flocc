import sys
from collections.abc import Callable
from dataclasses import dataclass

from flocc import ledger
from flocc.linear import Issue


@dataclass(frozen=True)
class Notice:
    cause: str
    summary: str
    action: str = ""
    issue_key: str | None = None

    def text(self) -> str:
        return f"{self.summary}\nAction: {self.action}" if self.action else self.summary


class Notifier:
    """Tells the owner about a problem: on its issue when it has one, else in the daily digest."""

    def __init__(self, con, linear, now: Callable[[], float]):
        self.con, self.linear, self.now = con, linear, now

    def send(self, notice: Notice, issue: Issue | None = None) -> None:
        print(f"{notice.issue_key or '-'} {notice.cause}: {notice.summary}", file=sys.stderr)
        if issue is not None:
            self.linear.comment(issue.id, notice.text())
        else:
            ledger.add_digest(self.con, notice.issue_key, notice.text(), self.now())
