import os
import re
import shlex

KEEP_MOVING = (
    "Nobody answers this session. Take the spec's default and carry on, or write what needs the"
    ' owner under a line that starts with "Needs the owner" in your summary and run flocc done.'
)
NO_APPROVER = (
    "Nobody is at this session to approve this call, so it was denied. Do it another way (for"
    " example the Read tool instead of cat, head or tail). If it truly needs the owner, list it"
    ' under a line that starts with "Needs the owner" in your summary and carry on.'
)
_DIALOGS = frozenset({"AskUserQuestion", "EnterPlanMode", "ExitPlanMode"})
_EDITS = frozenset({"Edit", "Write", "NotebookEdit"})
_LINEAR = re.compile(r"^mcp__.*linear", re.IGNORECASE)
# Command separators, subshells and backticks: each piece is judged as its own command.
_SEGMENTS = re.compile(r"&&|\|\||[;|&\n()`]|\$\(")
_ASSIGN = re.compile(r"^[A-Za-z_]\w*=")
_GIT_VALUE_OPTS = frozenset({"-C", "-c", "--git-dir", "--work-tree", "--namespace"})
_GH_READS = frozenset({"list", "view", "status", "diff", "checks"})
_GH_FIELDS = ("-f", "-F", "--field", "--raw-field", "--input")


def _words(segment: str) -> list[str]:
    try:
        words = shlex.split(segment)
    except ValueError:
        words = segment.split()
    while words and _ASSIGN.match(words[0]):
        words.pop(0)
    return words


def _git_subcommand(args: list[str]) -> str | None:
    it = iter(args)
    for arg in it:
        if arg in _GIT_VALUE_OPTS:
            next(it, None)
        elif not arg.startswith("-"):
            return arg
    return None


def _gh_api_writes(args: list[str]) -> bool:
    method = None
    fields = False
    for i, arg in enumerate(args):
        if arg in ("-X", "--method") and i + 1 < len(args):
            method = args[i + 1]
        elif arg.startswith("--method="):
            method = arg.split("=", 1)[1]
        elif arg.startswith("-X") and len(arg) > 2:
            method = arg[2:]
        elif (
            arg in _GH_FIELDS
            or arg.startswith(("--field=", "--raw-field=", "--input="))
            or len(arg) > 2
            and arg[:2] in ("-f", "-F")
        ):
            fields = True
    # gh api sends POST when it has fields and no method.
    return (method is not None and method.upper() != "GET") or (fields and method is None)


def _gh_writes(args: list[str]) -> bool:
    if not args:
        return False
    group = args[0]
    if group == "api":
        return _gh_api_writes(args[1:])
    if group in ("search", "status", "browse", "help", "version"):
        return False
    if group == "auth":
        return args[1:2] != ["status"]
    verb = next((a for a in args[1:] if not a.startswith("-")), None)
    return verb not in _GH_READS


def _bash(command: str, phase: str) -> str | None:
    for segment in _SEGMENTS.split(command):
        words = _words(segment)
        if not words:
            continue
        program, args = os.path.basename(words[0]), words[1:]
        if program == "git":
            sub = _git_subcommand(args)
            if sub == "push":
                return "Never push: the service pushes your commits after flocc done."
            if sub == "commit" and phase == "review":
                return "The reviewer changes nothing: write your findings in review.md."
        elif program == "flocc" and args[:1] == ["commit"] and phase == "review":
            return "The reviewer changes nothing: write your findings in review.md."
        elif program == "gh" and _gh_writes(args):
            return (
                "gh may only read here: the service opens and comments on PRs with its own token."
            )
    return None


_OWN = frozenset({"commit", "done"})
_PIPE_OK = frozenset({"tail", "head"})
_SEPARATORS = frozenset({";", "&&", "||", "|", "\n"})
# Anchored on the right: bash reads `>&1.docket/x` as a redirect to the file 1.docket/x.
_HARMLESS_REDIRECT = re.compile(r"(?:\d*>&\d+|\d*>\s*/dev/null)(?=[ \t\n|;]|$)")


def own_command(command: str) -> bool:
    """Whether a Bash command is only flocc commit or flocc done, with cd before and tail after.

    A plugin hook asks on these when the arguments name a guarded path (docket asks for any
    command naming .docket/ that is not git or docket), and the ask would become a deny.
    """
    text = _HARMLESS_REDIRECT.sub(" ", command)
    # An unquoted newline ends a command, so it is a separator token and not whitespace.
    lexer = shlex.shlex(text, posix=False, punctuation_chars="();<>|&\n")
    lexer.whitespace = " \t\r"
    lexer.whitespace_split = True
    try:
        tokens = list(lexer)
    except ValueError:
        return False
    segments: list[list[str]] = [[]]
    for token in tokens:
        if token in _SEPARATORS:
            segments.append([])
            continue
        operator = set(token) <= set("();<>|&")
        # posix=False keeps the quotes, so a single-quoted word is the only literal one.
        expands = not token.startswith("'") and ("`" in token or "$(" in token)
        if operator or expands:
            return False
        segments[-1].append(token)
    found = False
    for words in segments:
        program = os.path.basename(words[0]) if words else ""
        if program == "flocc" and words[1:2] and words[1] in _OWN:
            found = True
            continue
        cd = program == "cd" and len(words) == 2 and not found
        tail = (
            program in _PIPE_OK
            and found
            and all(w == "-n" or w.lstrip("-").isdigit() for w in words[1:])
        )
        if not (cd or tail):
            return False
    return found


def _inside(path: str, state: str) -> bool:
    if not path or not state:
        return False
    # Lexical, so ../ cannot climb out; a link inside the state directory only fools the guard,
    # which is a convenience and not a boundary.
    full, root = os.path.normpath(os.path.abspath(path)), os.path.normpath(state)
    return full == root or full.startswith(root + os.sep)


def decide(payload: dict, phase: str, state: str) -> str | None:
    """The reason to deny this tool call, or None to let it run."""
    tool = payload.get("tool_name")
    tool_input = payload.get("tool_input") or {}
    if not isinstance(tool, str) or not isinstance(tool_input, dict):
        return None
    if tool in _DIALOGS:
        return f"{tool} is not available in this session. {KEEP_MOVING}"
    if _LINEAR.match(tool):
        return "Linear is the service's: put what the owner should read in your summary."
    if tool in _EDITS and phase == "review":
        path = tool_input.get("file_path") or tool_input.get("notebook_path") or ""
        if not _inside(str(path), state):
            return (
                "The reviewer changes nothing in the repo; only review.md in your state directory."
            )
    if tool == "Bash" and isinstance(tool_input.get("command"), str):
        return _bash(tool_input["command"], phase)
    return None
