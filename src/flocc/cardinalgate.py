"""flocc-cardinal-gate: the forced command on cardinal's key in flocc's authorized_keys. sshd
runs it whatever cardinal asked for, with the request in SSH_ORIGINAL_COMMAND."""

import os
import re
import shlex
import sys

# Used with fullmatch: a $ anchor would let a trailing newline through.
KEY = re.compile(r"[A-Z]+-[0-9]+")
_ID = re.compile(r"[0-9]+")
NOTE_CHARS = 2000
TITLE_CHARS = 200
QUERY_CHARS = 200


class Refused(ValueError):
    pass


def _key(word: str) -> str:
    if not KEY.fullmatch(word):
        raise Refused(f"{word!r} is not an issue key")
    return word


def _text(word: str, what: str, limit: int = NOTE_CHARS) -> str:
    if not word.strip() or len(word) > limit or "\0" in word:
        raise Refused(f"the {what} must be 1 to {limit} characters")
    return word


def _ids(words: list[str]) -> bool:
    return 0 < len(words) <= 100 and all(_ID.fullmatch(w) for w in words)


def argv_for(command: str) -> list[str]:
    """flocc's argv for cardinal's request; Refused for anything outside the list. The words come
    from shlex and go to flocc as a list, so nothing in them reaches a shell. Free text goes as
    --opt=value, so text that starts with "-" cannot read as an option."""
    try:
        words = shlex.split(command)
    except ValueError as exc:
        raise Refused(f"cannot split the request: {exc}") from exc
    if not words:
        raise Refused("an empty request")
    verb, rest = words[0], words[1:]
    match verb, rest:
        case "events", ([] | ["--follow"] | ["--follow", "--hello"]):
            return words
        case "ack", _ if _ids(rest):
            return words
        case (("status" | "cleanup" | "digest-data"), []):
            return words
        case "cleanup", ["--now"]:
            return words
        case "digest-data", ["--done", n] if _ID.fullmatch(n):
            return words
        case "host", []:
            # flocc host is the desktop's command, which a host refuses.
            return ["host-report"]
        case "run-summary", [key]:
            return [verb, _key(key)]
        case (("stop-tests" | "amend-tests"), [key]):
            return [verb, _key(key)]
        case "shell", [command]:
            # Only bin/agent-shell sends this verb; bin/executor refuses it.
            return [verb, f"--command={_text(command, 'command')}"]
        case "resume", [key, "--note", note]:
            return [verb, _key(key), f"--note={_text(note, 'note')}"]
        case (("issue" | "pr"), [key]):
            return [verb, _key(key)]
        case (("comment" | "pr-comment"), [key, "--text", text]):
            return [verb, _key(key), f"--text={_text(text, 'text')}"]
        case "search", ["--query", query]:
            return [verb, f"--query={_text(query, 'query', QUERY_CHARS)}"]
        case "file", [key, "--title", title, "--text", text]:
            # No label option: the service adds cardinal-draft and nothing else.
            return [
                verb,
                _key(key),
                f"--title={_text(title, 'title', TITLE_CHARS)}",
                f"--text={_text(text, 'text')}",
            ]
        case "hold", [key]:
            return [verb, _key(key)]
        case "hold", [key, "--reason", reason]:
            return [verb, _key(key), f"--reason={_text(reason, 'reason')}"]
        case "release", [key]:
            # Lifting a hold is all cardinal may release: rerunning work is the owner's.
            return [verb, "--hold-only", _key(key)]
    raise Refused(f"{command.strip()} is not a command cardinal may run")


def main() -> int:
    try:
        argv = argv_for(os.environ.get("SSH_ORIGINAL_COMMAND", ""))
    except Refused as exc:
        print(f"flocc-cardinal-gate: {exc}", file=sys.stderr)
        return 2
    # The service's own config only; sshd passes no client environment, and nothing else may
    # point flocc at another ledger.
    os.environ.pop("FLOCC_CONFIG", None)
    from flocc import cli

    return cli.main(argv)


if __name__ == "__main__":
    raise SystemExit(main())
