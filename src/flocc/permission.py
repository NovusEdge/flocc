import json
import os
import re
import socket
import time
from collections import Counter
from pathlib import Path

from flocc.gate import note_question
from flocc.grants import for_phase, judge_grants
from flocc.sessions import session_name

LOG = "permissions.jsonl"
MODEL_SWITCH = "refusal_fallback_preference"
_CHAINED = re.compile(r"[;&|`\n]|\$\(")
_SHORT_N = re.compile(r"-[a-zA-Z]*n[a-zA-Z]*")


def _input(payload: dict) -> dict:
    args = payload.get("tool_input")
    return args if isinstance(args, dict) else {}


def entry(payload: dict, state: Path, phase: str | None, run: str | None, now: float) -> dict:
    args = _input(payload)
    issue = state.name
    return {
        "at": now,
        "host": socket.gethostname(),
        "issue": issue,
        "phase": phase,
        "run": run,
        "session": session_name(issue, phase or ""),
        "event": payload.get("hook_event_name"),
        "tool": payload.get("tool_name"),
        # The agent's own account of the command goes to the owner only, never to the judge.
        "input": {k: v for k, v in args.items() if k != "description"},
        "description": args.get("description"),
        "cwd": payload.get("cwd"),
        "classifier_reason": payload.get("reason"),
        "decision": None,
        "decided_by": "none",
        "rule": None,
        "reason": None,
        "latency_ms": 0,
    }


def is_model_switch(payload: dict) -> bool:
    metadata = _input(payload).get("metadata")
    return (
        payload.get("tool_name") == "AskUserQuestion"
        and isinstance(metadata, dict)
        and metadata.get("source") == MODEL_SWITCH
    )


def record(state: Path, line: dict) -> None:
    state.mkdir(parents=True, exist_ok=True)
    # One O_APPEND write per line: parallel subagents' hooks append to the same file.
    fd = os.open(state / LOG, os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600)
    try:
        os.write(fd, (json.dumps(line) + "\n").encode())
    finally:
        os.close(fd)


def bash_command(payload: dict) -> str | None:
    command = _input(payload).get("command")
    if payload.get("tool_name") != "Bash" or not isinstance(command, str):
        return None
    return command.strip()


def flocc_denied(command: str) -> bool:
    # A PreToolUse allow skips Claude Code's checks, so it must not reopen what the session's
    # deny rules close (sessions._DENY).
    words = command.split()
    if words[:1] == ["claude"]:
        return True
    return words[:2] == ["git", "commit"] and any(
        w == "--no-verify" or _SHORT_N.fullmatch(w) for w in words[2:]
    )


def granted(grants_path: Path, phase: str, command: str) -> str | None:
    if flocc_denied(command):
        return None
    if command in for_phase(grants_path, phase).commands:
        return "grant"
    exact, prefixes = judge_grants(grants_path)
    if command in exact:
        return "judge"
    if not _CHAINED.search(command) and any(
        command == p or command.startswith(p + " ") for p in prefixes
    ):
        return "judge"
    return None


def handle(
    payload: dict, state: Path, phase: str | None, run: str | None, now: float
) -> dict | None:
    if payload.get("hook_event_name") == "PreToolUse":
        command = bash_command(payload)
        how = granted(state / "grants.json", phase or "", command) if command else None
        if how is None:
            return None
        line = entry(payload, state, phase, run, now)
        line.update(decision="allow", decided_by=how)
        record(state, line)
        who = "the owner" if how == "grant" else "the permission judge"
        return {
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": "allow",
                "permissionDecisionReason": f"flocc: granted by {who} for this run",
            }
        }
    line = entry(payload, state, phase, run, now)
    if is_model_switch(payload):
        # Nothing answers this dialog on a host; as an open question it reaches the owner. The
        # Stop hook clears `asking` when the turn ends.
        note_question(state, payload, now)
    record(state, line)
    return None


def load_entries(state_root: Path, since: float) -> list[dict]:
    out = []
    for path in sorted(state_root.glob(f"*/{LOG}")):
        for raw in path.read_text().splitlines():
            try:
                e = json.loads(raw)
            except ValueError:  # a hook killed mid-write leaves a torn line
                continue
            if isinstance(e, dict) and isinstance(e.get("at"), int | float) and e["at"] >= since:
                out.append(e)
    return out


def _prefix(e: dict) -> str:
    args = e.get("input") if isinstance(e.get("input"), dict) else {}
    if e.get("tool") != "Bash":
        return "-"
    return " ".join(str(args.get("command", "")).split()[:2]) or "-"


def report(entries: list[dict], since: float) -> str:
    if not entries:
        return f"No blocks or prompts since {time.strftime('%Y-%m-%d', time.gmtime(since))}."
    keys = [
        (
            str(e.get("event") or "-"),
            str(e.get("tool") or "-"),
            _prefix(e),
            str(e.get("classifier_reason") or "-"),
            str(e.get("decided_by") or "none"),
        )
        for e in entries
    ]
    examples: dict[tuple, str] = {}
    for key, e in zip(keys, entries, strict=True):
        args = e.get("input") if isinstance(e.get("input"), dict) else {}
        examples.setdefault(key, str(args.get("command") or json.dumps(args)))
    rows = ["count  event  tool  prefix  reason  decided  example"]
    for key, n in sorted(Counter(keys).items(), key=lambda kv: (-kv[1], kv[0])):
        rows.append(f"{n:>5}  {'  '.join(key)}  e.g. {examples[key]}")
    return "\n".join(rows)
