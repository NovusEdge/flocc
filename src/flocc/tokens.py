import json
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
from datetime import UTC, datetime

from flocc import ledger
from flocc.notice import Notice

WARN_DAYS = 14
PAGE_DAYS = 3
_DAY = 86400
LOGIN_REMEDY = "on the desktop, run flocc login -H <this host>"


class TokenRejected(Exception):
    pass


def token_expiry(token: str, opener=urllib.request.urlopen) -> float | None:
    """The token's expiry as a Unix time, or None for a token that never expires."""
    req = urllib.request.Request(
        "https://api.github.com/user",
        headers={"Authorization": f"Bearer {token}", "Accept": "application/vnd.github+json"},
    )
    try:
        with opener(req, timeout=10) as resp:
            header = resp.headers.get("GitHub-Authentication-Token-Expiration")
    except urllib.error.HTTPError as exc:
        if exc.code == 401:
            raise TokenRejected(f"HTTP {exc.code}") from exc
        raise
    if not header:
        return None
    return datetime.strptime(header, "%Y-%m-%d %H:%M:%S %Z").replace(tzinfo=UTC).timestamp()


def can_write_contents(token: str, slug: str, opener=urllib.request.urlopen) -> bool:
    """Whether the token may push to the repo, asked without changing anything."""
    # A ref at the all-zero SHA can never be created. GitHub checks permission before the
    # object, so 422 means the write itself was allowed. A refusal is 403, or 404 when the token
    # can read the repo but not write it, the same answer as for a repo it cannot see.
    headers = {"Authorization": f"Bearer {token}", "Accept": "application/vnd.github+json"}
    url = f"https://api.github.com/repos/{slug}"
    req = urllib.request.Request(
        f"{url}/git/refs",
        data=b'{"ref": "refs/heads/flocc-doctor-probe", "sha": "' + b"0" * 40 + b'"}',
        headers=headers,
        method="POST",
    )
    try:
        with opener(req, timeout=10):
            pass
    except urllib.error.HTTPError as exc:
        if exc.code == 403:
            return False
        if exc.code == 422:
            return True
        if exc.code != 404:
            raise
    else:
        raise RuntimeError(f"GitHub accepted a ref at the zero SHA in {slug}")
    try:
        with opener(urllib.request.Request(url, headers=headers), timeout=10):
            return False
    except urllib.error.HTTPError as exc:
        if exc.code == 404:
            raise LookupError(f"{slug} is not visible to this token") from exc
        raise


def _page(rt, name: str, path, summary: str) -> None:
    rt.notifier.send(
        Notice(
            f"gh-token-{name}",
            summary,
            f"Make a new {name} token and write it to {path}",
            event="service-error",
        )
    )


def check_tokens(rt, expiry=token_expiry) -> None:
    # An expired agent token is still a non-empty file, so sessions start and every gh call in
    # them fails without anything reaching the owner. Once a day is enough for a date.
    today = time.strftime("%Y-%m-%d", time.localtime(rt.now()))
    if ledger.meta_get(rt.con, "token_check_date") == today:
        return
    complete = True
    for name, path in (
        ("agent", rt.cfg.agent_gh_token_file),
        ("publish", rt.cfg.publish_gh_token_file),
    ):
        token = path.read_text().strip() if path.exists() else ""
        if not token:
            continue
        try:
            expires = expiry(token)
        except TokenRejected:
            ledger.meta_set(rt.con, f"token_expiry:{name}", "rejected")
            _page(rt, name, path, f"GitHub rejected the {name} token: it expired or was revoked.")
            continue
        except (OSError, ValueError) as exc:
            print(f"tokens: could not check the {name} token: {exc}", file=sys.stderr)
            complete = False
            continue
        # status reads this; it must not ask GitHub itself.
        ledger.meta_set(
            rt.con, f"token_expiry:{name}", "never" if expires is None else str(int(expires))
        )
        if expires is None:
            continue
        days = int((expires - rt.now()) // _DAY)
        if days < PAGE_DAYS:
            _page(rt, name, path, f"The GitHub {name} token expires in {max(days, 0)} days.")
        elif days < WARN_DAYS:
            line = f"GitHub {name} token expires in {days} days; replace {path}"
            ledger.add_digest(rt.con, None, line, rt.now())
    if complete:
        ledger.meta_set(rt.con, "token_check_date", today)


def claude_login(claude: list[str], run=subprocess.run, timeout: float = 30) -> str | None:
    """full, token-only (<method>) or none; None when claude auth status gave no answer."""
    try:
        out = run(
            [*claude, "auth", "status"],
            capture_output=True,
            text=True,
            check=False,
            timeout=timeout,
        )
        auth = json.loads(out.stdout)
    except (OSError, subprocess.SubprocessError, ValueError):
        return None
    # A CLI error comes back as JSON without loggedIn; only an explicit false is a logout.
    if not isinstance(auth, dict) or "loggedIn" not in auth:
        return None
    if not auth["loggedIn"]:
        return "none"
    method = auth.get("authMethod") or "unknown"
    # Only a claude.ai login opens Remote Control sessions; a setup-token login cannot.
    return "full" if method == "claude.ai" else f"token-only ({method})"


def check_claude_login(rt, run=subprocess.run) -> None:
    # Every new session fails without the login and running Remote Control sessions cannot
    # recover, so the first tick that sees it lapse reports once; the flag rearms when it is back.
    login = claude_login(rt.agent.claude_argv(), run)
    if login is None:
        return
    lapsed = ledger.meta_get(rt.con, "claude_login_lapsed") == "1"
    if login == "full":
        if lapsed:
            ledger.meta_set(rt.con, "claude_login_lapsed", "0")
        return
    if lapsed:
        return
    summary = f"The Claude login on {socket.gethostname()} lapsed ({login}); new sessions fail."
    action = "On the desktop, run flocc login -H <this host>"
    rt.notifier.send(Notice("claude-login", summary, action, event="service-error"))
    ledger.meta_set(rt.con, "claude_login_lapsed", "1")
