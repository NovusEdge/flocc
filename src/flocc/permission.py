import json
import os
import socket
from pathlib import Path

from flocc.gate import note_question
from flocc.sessions import session_name

LOG = "permissions.jsonl"
MODEL_SWITCH = "refusal_fallback_preference"


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


def handle(
    payload: dict, state: Path, phase: str | None, run: str | None, now: float
) -> dict | None:
    line = entry(payload, state, phase, run, now)
    if is_model_switch(payload):
        # Nothing answers this dialog on a host; as an open question it reaches the owner. The
        # Stop hook clears `asking` when the turn ends.
        note_question(state, payload, now)
    record(state, line)
    return None
