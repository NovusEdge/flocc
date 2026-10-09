import json
import sys
import urllib.request
from collections.abc import Callable
from dataclasses import asdict, dataclass, replace

from linear_agents import ledger
from linear_agents.linear import Issue

DEDUPE_S = 24 * 3600


@dataclass(frozen=True)
class Escalation:
    tier: int
    cause: str
    summary: str
    action: str = ""
    issue_key: str | None = None
    default: str | None = None
    deadline_min: int | None = None
    waiting: bool = False
    scope: str = "issue"

    def to_json(self) -> str:
        return json.dumps(asdict(self))

    @classmethod
    def from_json(cls, text: str) -> "Escalation":
        return cls(**json.loads(text))


def validate(esc: Escalation) -> None:
    if esc.tier not in (1, 2, 3, 4):
        raise ValueError(f"tier must be 1-4, got {esc.tier}")
    if esc.tier <= 2 and not esc.action:
        raise ValueError("tier 1 and 2 escalations need --action: the one thing the owner does")
    if esc.tier == 2 and not esc.waiting and not (esc.default and esc.deadline_min):
        raise ValueError("a tier 2 escalation needs --default and --deadline-min, or --waiting")


def scoped(esc: Escalation, issue_key: str) -> Escalation:
    # Agents pick causes like "schema" that recur across issues; only a global cause is shared.
    if esc.scope == "global":
        return esc
    return replace(esc, cause=f"{issue_key}:{esc.cause}", issue_key=issue_key)


def route(esc: Escalation, seen_recently: bool) -> list[str]:
    # Dedupe holds back the push only. The label and comment always land on the issue, so a
    # repeated cause still reaches Linear.
    if esc.tier == 4:
        return ["log"]
    if esc.tier == 1 and esc.scope == "global":
        return ["log"] if seen_recently else ["ntfy:5", "create-issue"]
    if esc.tier == 1:
        return ([] if seen_recently else ["ntfy:5"]) + ["label:escalation", "comment"]
    if esc.tier == 2:
        # Push only when the agent has stopped; with a default it keeps working.
        push = esc.waiting and not seen_recently
        return ["label:needs-decision", "comment"] + (["ntfy:3"] if push else [])
    return ["digest"]


def ntfy_push(url, title, message, priority, click=None, opener=urllib.request.urlopen) -> None:
    # HTTP headers are latin-1; non-ASCII text travels in the UTF-8 body only.
    safe_title = title.encode("ascii", "replace").decode()
    headers = {"Title": safe_title, "Priority": str(priority)}
    if click:
        headers["Click"] = click
    body = f"{title}\n{message}".encode()
    req = urllib.request.Request(url, data=body, headers=headers, method="POST")
    with opener(req, timeout=10):
        pass


def _text(esc: Escalation) -> str:
    lines = [esc.summary]
    if esc.action:
        lines.append(f"Action: {esc.action}")
    if esc.default:
        lines.append(f"Default if no answer within {esc.deadline_min} min: {esc.default}")
    return "\n".join(lines)


class Notifier:
    def __init__(self, cfg, con, linear, now: Callable[[], float], push=ntfy_push):
        self.cfg, self.con, self.linear, self.now, self.push = cfg, con, linear, now, push

    def send(self, esc: Escalation, issue: Issue | None, team_id: str | None) -> None:
        now = self.now()
        seen = ledger.alert_seen(self.con, esc.cause, DEDUPE_S, now)
        actions = route(esc, seen)
        title = f"{esc.issue_key or 'linear-agents'} T{esc.tier}"
        pushes = [a for a in actions if a.startswith("ntfy:")]
        for action in actions:
            if action in pushes:
                continue
            if action.startswith("label:") and issue:
                self.linear.add_label(issue, action[6:])
            elif action == "comment" and issue:
                self.linear.comment(issue.id, _text(esc))
            elif action == "create-issue" and team_id:
                # The spec wants one shared issue per cross-cutting cause, not one per 24 hours.
                shared = f"shared-issue:{esc.cause}"
                if ledger.meta_get(self.con, shared) is None:
                    url = self.linear.create_issue(team_id, f"Escalation: {esc.cause}", _text(esc))
                    ledger.meta_set(self.con, shared, url)
            elif action == "digest":
                ledger.add_digest(self.con, esc.issue_key, esc.summary, now)
            else:
                print(
                    f"[T{esc.tier}] {esc.issue_key or '-'} {esc.cause}: {esc.summary}",
                    file=sys.stderr,
                )
        # The push goes after the Linear actions: a Linear failure raises before anything is
        # sent or recorded, so the retry tick pushes once. A push failure must not undo them.
        for action in pushes:
            click = issue.url if issue else None
            try:
                self.push(self.cfg.ntfy_url, title, _text(esc), int(action[5:]), click)
            except OSError as exc:
                print(f"ntfy push failed for {esc.cause}: {exc}", file=sys.stderr)
        # Only a push opens the 24 h window; recording suppressed repeats would push it out
        # forever for a cause that recurs more often than daily.
        if pushes:
            ledger.record_alert(self.con, esc.cause, esc.tier, esc.issue_key, now)
