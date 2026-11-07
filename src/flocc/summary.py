import time
from collections import Counter
from collections.abc import Callable
from pathlib import Path

from flocc import control, events, host, ledger, reach
from flocc.config import Config
from flocc.context import state_dir
from flocc.publisher import failure_excerpt
from flocc.sequence import ARCHETYPE
from flocc.status import age_text, pr_state
from flocc.supervisor import BLOCKED, CHECK_OUTPUT, LAST_SUMMARY, POSTED_REVIEW

# cardinal reads these whole on every wake they cover, so they are bounded.
RUN_SUMMARY_CHARS = 6000
_PARTS = (
    ("Summary", LAST_SUMMARY, 1800),
    ("Why it stopped", BLOCKED, 1500),
    ("Review", POSTED_REVIEW, 800),
)
CHECK_CHARS = 1000
LOG_LINES = 10
DIGEST_LINES = 100
LINE_CHARS = 300
LIST_ROWS = 50
ATTENTION_ROWS = 20


def _read(d: Path, name: str) -> str:
    path = d / name
    return path.read_text(errors="replace").strip() if path.is_file() else ""


def _tail(text: str, limit: int) -> str:
    return text if len(text) <= limit else "[...]\n" + text[-limit:]


def _step(phase: str) -> str:
    return ARCHETYPE.get(phase, phase)


def _capped(rows: list[str], limit: int) -> list[str]:
    if not rows:
        return ["- none"]
    more = [f"- ({len(rows) - limit} more)"] if len(rows) > limit else []
    return rows[:limit] + more


def run_summary(
    cfg: Config,
    con,
    key: str,
    now: float,
    log_lines: Callable[[str, int], list[tuple[int, str]]],
) -> str:
    claim = ledger.get_claim(con, key)
    d = state_dir(cfg, key)
    out = [f"# {key}"]
    if claim is None:
        out.append("No claim: the issue is not running and has no finished run on this host.")
    else:
        out.append(
            f"{_step(claim.phase)}, {claim.state.replace('_', ' ')};"
            f" sequence {', '.join(claim.sequence)};"
            f" fix rounds {claim.fix_attempts}/{cfg.check_fix_rounds};"
            f" started {age_text(max(0.0, now - claim.started_at))} ago; branch {claim.branch}"
        )
    url = _read(d, "pr-url")
    if url and url != "unknown":
        out.append(f"PR: {url}")
    if hold := ledger.issue_holds(con).get(key):
        out.append(f"Held: {hold[0]}")
    for title, name, limit in _PARTS:
        if text := _read(d, name):
            out.append(f"## {title}\n\n{_tail(text, limit)}")
    if check := _read(d, CHECK_OUTPUT):
        out.append(f"## Check output, excerpt\n\n{failure_excerpt(check, CHECK_CHARS)}")
    if lines := log_lines(key, LOG_LINES):
        out.append("## Service log\n\n" + "\n".join(text[:200] for _, text in lines))
    return "\n\n".join(out)[:RUN_SUMMARY_CHARS]


def _exit(code: int | None) -> str:
    return "no exit status" if code is None else f"exit {code}"


def digest_data(cfg: Config, con, now: float, pr_view=None) -> tuple[str, int]:
    out = [f"# Digest data, {time.strftime('%Y-%m-%d %H:%M', time.localtime(now))}", "", "## Runs"]
    runs = []
    for c in [*ledger.live_claims(con), *ledger.finished_claims(con)]:
        url = _read(state_dir(cfg, c.issue_key), "pr-url")
        pr = f", PR {url}" if url and url != "unknown" else ""
        cause = control.stop_cause(cfg, c) if c.state in ("blocked", "timed_out") else ""
        why = f" ({cause})" if cause and cause != "timed-out" else ""
        # A merged PR is finished work, not a PR waiting for review.
        merged = pr_state(pr_view, c, url) == "merged"
        state = "merged" if merged else c.state.replace("_", " ")
        runs.append(f"- {c.issue_key}: {_step(c.phase)}, {state}{why}{pr}")
    out += _capped(runs, LIST_ROWS)
    out += ["", "## Since the last digest"]
    rows = ledger.pending_digest(con)
    shown = rows[:DIGEST_LINES]
    out += [f"- {key or 'service'}: {line[:LINE_CHARS]}" for _, key, line in shown]
    if len(rows) > len(shown):
        out.append(f"- ({len(rows) - len(shown)} newer lines wait for the next digest)")
    if not rows:
        out.append("- nothing")
    out += ["", "## Holds"]
    holds = ledger.issue_holds(con)
    out += _capped(
        [f"- {k}: {reason}, {age_text(now - at)}" for k, (reason, at) in holds.items()], LIST_ROWS
    )
    out += ["", "## Events in the last 24 hours"]
    recent = events.since(con, now - 86400)
    kinds = Counter(e.kind for e in recent)
    waiting = Counter(e.kind for e in recent if e.acked_at is None)
    out += _capped(
        [f"- {k}: {n}, {waiting[k]} not acknowledged" for k, n in sorted(kinds.items())],
        LIST_ROWS,
    )
    out += ["", "## Events needing attention: printed and redelivered, never acknowledged"]
    out += _capped(
        [
            f"- event {e.id}, {e.kind}, {e.issue_key or e.repo or 'service'}: {e.detail}"
            for e in events.attention(con, now)
        ],
        ATTENTION_ROWS,
    )
    out += ["", "## Commands cardinal ran with agent-shell in the last 24 hours"]
    out += _capped(
        [
            f"- {time.strftime('%H:%M', time.localtime(at))}, {_exit(code)}: {command[:LINE_CHARS]}"
            for at, command, code in host.shell_calls(con, now - 86400)
        ],
        LIST_ROWS,
    )
    out += ["", "## Runs cardinal resumed in the last 7 days"]
    lines, ended_done = [], 0
    for _, key, cause, _ in control.resumes(con, now - 7 * 86400):
        c = ledger.get_claim(con, key)
        state = c.state.replace("_", " ") if c else "no claim"
        # _start clears the cap for a fresh claim, so a claim the owner reran is not cardinal's.
        ended_done += state == "done" and bool(ledger.meta_get(con, f"cardinal_resume:{key}"))
        lines.append(f"- {key} after {cause}: {state}")
    if lines:
        lines.insert(0, f"- {len(lines)} resumed, {ended_done} ended done")
    out += _capped(lines, LIST_ROWS)
    day = reach.writes(con, now - 86400)
    out += ["", "## What cardinal wrote in Linear and GitHub in the last 24 hours"]
    out += _capped(
        [
            f"- {w['kind']} {w['key']}"
            + (f" → {w['filed']}" if w.get("filed") else "")
            + f": {str(w['text'])[:LINE_CHARS]}"
            for w in day
            if w.get("mode") == "on"
        ],
        LIST_ROWS,
    )
    out += ["", "## Follow-ups cardinal would have filed in the last 24 hours"]
    out += _capped(
        [f"- {w['key']}: {str(w['text'])[:LINE_CHARS]}" for w in day if w.get("mode") == "observe"],
        LIST_ROWS,
    )
    through = shown[-1][0] if shown else 0
    out += ["", f"When the digest is sent: flocc digest-data --done {through}"]
    return "\n".join(out), through
