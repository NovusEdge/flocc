import sys
import traceback
from collections.abc import Callable

from flocc import a2aclient, inbox, ledger
from flocc.a2aconf import A2AConfig, NotConfigured, load, route
from flocc.context import state_dir
from flocc.ledger import Claim
from flocc.linear import LinearError
from flocc.sessions import session_name

RETRIES = 3
SEEDED = "a2a_seeded"
_FILES = 30


def step(rt, a2a: Callable[[], A2AConfig] = load) -> None:
    """The tick's A2A work; a failure here is logged and never fails the tick."""
    try:
        cfg = a2a()
    except NotConfigured:
        return
    except Exception:  # noqa: BLE001
        traceback.print_exc()
        return
    for part in (_seed, _settle, _record, _opened, _merged):
        try:
            part(rt, cfg)
        except Exception:  # noqa: BLE001 - one part's failure must not stop the others
            traceback.print_exc()


def _seed(rt, cfg: A2AConfig) -> None:
    """Marks the PRs that exist when A2A first runs as announced.

    Done claims stay in the ledger until their issue reruns, so without this the first tick sends
    handoffs and merge notices for every historical PR.
    """
    if ledger.meta_get(rt.con, SEEDED):
        return
    for claim in ledger.live_claims(rt.con):
        if _pr_url(rt, claim) is not None:
            ledger.meta_set(rt.con, f"a2a_sent:{claim.issue_key}:open", "seeded")
    for claim in ledger.finished_claims(rt.con):
        if _pr_url(rt, claim) is not None:
            for event in ("open", "merged"):
                ledger.meta_set(rt.con, f"a2a_sent:{claim.issue_key}:{event}", "seeded")
    ledger.meta_set(rt.con, SEEDED, "1")


def _inbox(rt):
    return inbox.connect(inbox.db_path(rt.cfg.state_root))


def _settle(rt, cfg: A2AConfig) -> None:
    con = _inbox(rt)
    closed = {}
    for key in inbox.pending_issues(con):
        try:
            closed[key] = rt.linear.state_types([key])[key]
        except LinearError as exc:
            print(f"a2a: {key} state not read: {exc}", file=sys.stderr)
    inbox.settle(con, rt.now(), closed)


def _live(rt, key: str) -> bool:
    claim = ledger.get_claim(rt.con, key)
    return (
        claim is not None
        and claim.state == "running"
        and rt.sessions.alive(session_name(key, claim.phase))
    )


def _record(rt, cfg: A2AConfig) -> None:
    con = _inbox(rt)
    for msg in inbox.unrecorded(con):
        if _live(rt, msg.to_issue):
            # A live session gets it from its hooks; marked so a later stop does not record it.
            inbox.mark_recorded(con, msg.task_id, rt.now())
            continue
        first = (msg.body.splitlines() or [""])[0][:200]
        try:
            target = rt.linear.issue(msg.to_issue)
            rt.linear.comment(
                target.id,
                f"A2A message waiting for this issue's next run: from {msg.sender}, {msg.kind}:"
                f" {first} (task {msg.task_id})",
            )
        except LinearError as exc:
            print(f"a2a: record for {msg.to_issue} not posted: {exc}", file=sys.stderr)
            continue
        inbox.mark_recorded(con, msg.task_id, rt.now())


def _pr_url(rt, claim: Claim) -> str | None:
    path = state_dir(rt.cfg, claim.issue_key) / "pr-url"
    url = path.read_text().strip() if path.exists() else ""
    return url if url and url != "unknown" else None


def _files(view: dict) -> list[str]:
    return [f.get("path", "") for f in view.get("files") or []][:_FILES]


def _body(what: str, url: str, files: list[str]) -> str:
    listed = "\n".join(f"- {f}" for f in files) or "- (no files listed)"
    return f"{what}: {url}\n\nChanged files:\n{listed}"


def _once(
    rt, cfg: A2AConfig, key: str, event: str, build: Callable[[], list[tuple] | None]
) -> None:
    """Sends what build returns, each (peer, to, kind, body, extra) once; None means not yet.

    A failure in build or in a send counts toward RETRIES for this key and event, then gives up.
    """
    done = f"a2a_sent:{key}:{event}"
    if ledger.meta_get(rt.con, done):
        return
    counter = f"a2a_retry:{key}:{event}"
    failed = False
    try:
        sends = build()
    except Exception as exc:  # noqa: BLE001 - one claim's failure must not stop the others
        print(f"a2a: {event} for {key} not prepared: {exc}", file=sys.stderr)
        sends, failed = None, True
    for peer, to, kind, body, extra in sends or []:
        sub = f"{done}:{to}"
        if ledger.meta_get(rt.con, sub):
            continue
        try:
            a2aclient.send(peer, cfg.token, to=to, body=body, kind=kind, sender_as=key, extra=extra)
        except a2aclient.A2AError as exc:
            print(f"a2a: {event} {kind} from {key} to {to} not sent: {exc}", file=sys.stderr)
            failed = True
            continue
        ledger.meta_set(rt.con, sub, "1")
    if sends is None and not failed:
        return
    if not failed:
        ledger.meta_set(rt.con, done, "1")
        return
    tries = int(ledger.meta_get(rt.con, counter) or 0) + 1
    ledger.meta_set(rt.con, counter, str(tries))
    if tries >= RETRIES:
        print(f"a2a: {event} sends for {key} failed {tries} times; giving up", file=sys.stderr)
        ledger.meta_set(rt.con, done, "gave-up")


def _handoffs(rt, cfg: A2AConfig, claim: Claim, url: str, files: list[str], what: str) -> list:
    out = []
    for blocked in rt.linear.blocked_issues(claim.issue_key):
        peer = route(cfg, [*blocked.labels, *([blocked.project] if blocked.project else [])])
        if peer is None:
            print(f"a2a: no host serves {blocked.key}; handoff skipped", file=sys.stderr)
            continue
        out.append((peer, blocked.key, "handoff", _body(what, url, files), {"pr": url}))
    return out


def _opened(rt, cfg: A2AConfig) -> None:
    for claim in [*ledger.live_claims(rt.con), *ledger.finished_claims(rt.con)]:
        url = _pr_url(rt, claim)
        if url is None:
            continue

        def build(claim=claim, url=url) -> list[tuple]:
            view = rt.pr_view(claim, url)
            return _handoffs(rt, cfg, claim, url, _files(view), f"{claim.issue_key} opened a PR")

        _once(rt, cfg, claim.issue_key, "open", build)


def _merged(rt, cfg: A2AConfig) -> None:
    me = cfg.hosts.get(cfg.name)
    for claim in ledger.finished_claims(rt.con):
        url = _pr_url(rt, claim)
        if claim.state != "done" or url is None:
            continue

        def build(claim=claim, url=url) -> list[tuple] | None:
            key = claim.issue_key
            view = rt.pr_view(claim, url)
            if view.get("state") == "CLOSED":
                ledger.meta_set(rt.con, f"a2a_sent:{key}:merged", "closed")
                return None
            if view.get("state") != "MERGED":
                return None
            files = _files(view)
            sends = _handoffs(rt, cfg, claim, url, files, f"{key} merged")
            if me is not None:
                for peer in ledger.in_repo(ledger.live_claims(rt.con), claim.repo):
                    if peer.issue_key != key:
                        body = _body(f"{key} merged into {claim.repo}", url, files)
                        sends.append(
                            (me, peer.issue_key, "notice", body, {"pr": url, "repo": claim.repo})
                        )
            return sends

        _once(rt, cfg, claim.issue_key, "merged", build)
