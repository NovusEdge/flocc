import json
import os
import re
import socket
import time
from collections import Counter
from pathlib import Path

from flocc import judge as judge_mod
from flocc.config import Config
from flocc.gate import note_question
from flocc.grants import add_judge, for_phase, judge_grants, one_line
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
    command = one_line(command)
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


ASKED = "permission-asked"
OWNER_ASKED = (
    "flocc: the owner has been asked about this command. Carry on with other work, or end your"
    " turn if you cannot."
)
_JUDGED_PHASES = ("test", "impl", "fix")
_JUDGE_CALLS = ("judge", "timeout")


def _judge_calls(state: Path, run: str | None) -> int:
    path = state / LOG
    if not path.exists():
        return 0
    n = 0
    for raw in path.read_text().splitlines():
        try:
            e = json.loads(raw)
        except ValueError:
            continue
        n += isinstance(e, dict) and e.get("run") == run and e.get("decided_by") in _JUDGE_CALLS
    return n


def _request(behavior: str, message: str | None = None) -> dict:
    decision = {"behavior": behavior} | ({"message": message} if message else {})
    return {"hookSpecificOutput": {"hookEventName": "PermissionRequest", "decision": decision}}


def ask_owner(state: Path, line: dict, verdict: judge_mod.Verdict, now: float) -> None:
    command = line["input"].get("command")
    asked = {
        "at": now,
        "tool": line["tool"],
        # One line, so the escalation's "allow: command ..." can be copied back as it stands.
        "command": one_line(command) if isinstance(command, str) else json.dumps(line["input"]),
        "reading": verdict.reading,
        "reason": verdict.reason,
    }
    state.mkdir(parents=True, exist_ok=True)
    # A fresh ask has not been escalated yet, whatever an earlier one was.
    (state / f"{ASKED}-escalated").unlink(missing_ok=True)
    tmp = state / f"{ASKED}.tmp"
    tmp.write_text(json.dumps(asked))
    os.replace(tmp, state / ASKED)


def _act(
    verdict: judge_mod.Verdict, event: str, line: dict, state: Path, now: float
) -> dict | None:
    line.update(
        decision=verdict.decision,
        reason=verdict.reason,
        rule=verdict.rule,
        decided_by="judge" if verdict.decision else "timeout",
    )
    command = line["input"].get("command")
    if verdict.decision == "allow" and event == "PermissionDenied" and isinstance(command, str):
        # The retry goes through the classifier again; the grant is what PreToolUse lets past it.
        add_judge(state / "grants.json", one_line(command), verdict.rule)
        return {"hookSpecificOutput": {"hookEventName": "PermissionDenied", "retry": True}}
    if verdict.decision == "allow" and event == "PermissionRequest":
        return _request("allow")
    if verdict.decision == "owner":
        ask_owner(state, line, verdict, now)
        return _request("deny", OWNER_ASKED) if event == "PermissionRequest" else None
    if verdict.decision == "deny" and event == "PermissionRequest":
        return _request("deny", f"flocc judge: {verdict.reason}")
    return None


def handle(
    payload: dict,
    state: Path,
    phase: str | None,
    run: str | None,
    now: float,
    cfg: Config | None = None,
    judge=None,
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
    if line["tool"] == "AskUserQuestion":
        # The agent's own questions reach the owner through the gate and _check_question. Only
        # the model-switch dialog, which nothing answers on a host, is made a question here; the
        # Stop hook clears `asking` when the turn ends.
        if is_model_switch(payload):
            note_question(state, payload, now)
        record(state, line)
        return None
    event = line["event"]
    out = None
    if (
        cfg is not None
        and cfg.judge_model
        and phase in _JUDGED_PHASES
        and (event == "PermissionRequest" or line["tool"] == "Bash")
    ):
        if _judge_calls(state, run) >= cfg.judge_max_per_run:
            line["decided_by"] = "cap"
        else:
            action = {
                k: line[k]
                for k in ("host", "issue", "phase", "tool", "input", "cwd", "classifier_reason")
            }
            started = time.monotonic()
            verdict = (judge or judge_mod.ask)(cfg, action)
            line["latency_ms"] = int((time.monotonic() - started) * 1000)
            out = _act(verdict, event, line, state, now)
    record(state, line)
    return out


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
