import email.message
import io
import subprocess
import urllib.error
from dataclasses import replace
from datetime import UTC, datetime
from types import SimpleNamespace

import pytest

from flocc import ledger
from flocc.tick import tick
from flocc.tokens import TokenRejected, check_claude_login, check_tokens, claude_login, token_expiry
from tests.fakes import FakeNotifier

NOW = datetime(2027, 1, 10, 12, tzinfo=UTC).timestamp()
DAY = 86400


class Response:
    def __init__(self, headers):
        self.headers = email.message.Message()
        for k, v in headers.items():
            self.headers[k] = v

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


def opener_with(headers=None, status=200):
    seen = []

    def opener(req, timeout):
        seen.append(req)
        if status != 200:
            raise urllib.error.HTTPError(req.full_url, status, "x", {}, io.BytesIO(b"{}"))
        return Response(headers or {})

    return opener, seen


def test_token_expiry_reads_the_github_header():
    opener, seen = opener_with(
        {"GitHub-Authentication-Token-Expiration": "2027-01-31 22:00:00 UTC"}
    )
    assert token_expiry("tok", opener) == datetime(2027, 1, 31, 22, tzinfo=UTC).timestamp()
    assert seen[0].get_header("Authorization") == "Bearer tok"


def test_token_without_expiry_header_never_expires():
    assert token_expiry("tok", opener_with()[0]) is None


def test_rejected_token_raises():
    with pytest.raises(TokenRejected):
        token_expiry("tok", opener_with(status=401)[0])


@pytest.fixture
def rt(tmp_cfg, tmp_path):
    publish = tmp_path / "gh-publish.token"
    publish.write_text("p\n")
    cfg = replace(tmp_cfg, publish_gh_token_file=publish)
    return SimpleNamespace(
        cfg=cfg, con=ledger.connect(cfg.ledger_path), notifier=FakeNotifier(), now=lambda: NOW
    )


def digest_lines(rt):
    return [line for _, _, line in ledger.pending_digest(rt.con)]


def test_far_expiry_reports_nothing(rt):
    check_tokens(rt, lambda token: NOW + 60 * DAY)
    assert digest_lines(rt) == [] and rt.notifier.sent == []


def test_expiry_within_two_weeks_goes_to_the_digest(rt):
    check_tokens(rt, lambda token: NOW + 10 * DAY)
    lines = digest_lines(rt)
    assert len(lines) == 2 and all("expires in 10 days" in line for line in lines)
    assert rt.notifier.sent == []


def test_expiry_within_three_days_pages(rt):
    check_tokens(rt, lambda token: NOW + 2 * DAY if token == "t" else NOW + 60 * DAY)
    [esc] = rt.notifier.sent
    assert esc.cause == "gh-token-agent"
    assert str(rt.cfg.agent_gh_token_file) in esc.action


def test_rejected_token_pages(rt):
    def expiry(token):
        if token == "p":
            raise TokenRejected("401")

    check_tokens(rt, expiry)
    [esc] = rt.notifier.sent
    assert esc.cause == "gh-token-publish" and "rejected" in esc.summary


def test_checks_once_a_day_and_retries_after_a_network_error(rt):
    calls = []

    def down(token):
        calls.append(token)
        raise urllib.error.URLError("no route")

    check_tokens(rt, down)
    check_tokens(rt, down)
    assert len(calls) == 4
    check_tokens(rt, lambda token: calls.append(token) or None)
    check_tokens(rt, lambda token: calls.append(token) or None)
    assert len(calls) == 6


def auth_run(stdout, code=0):
    def run(argv, **kw):
        assert argv[1:] == ["auth", "status"]
        return subprocess.CompletedProcess(argv, code, stdout, "")

    return run


FULL = '{"loggedIn": true, "authMethod": "claude.ai"}'
LOGGED_OUT = '{"loggedIn": false}'


@pytest.mark.parametrize(
    ("stdout", "want"),
    [
        (FULL, "full"),
        ('{"loggedIn": true, "authMethod": "oauth_token"}', "token-only (oauth_token)"),
        (LOGGED_OUT, "none"),
        ("Error: unexpected", None),
    ],
)
def test_claude_login_kinds(stdout, want):
    assert claude_login("/opt/claude", auth_run(stdout)) == want


def test_claude_login_that_cannot_run_is_unknown():
    def hangs(argv, **kw):
        raise subprocess.TimeoutExpired(argv, 30)

    assert claude_login("/opt/claude", hangs) is None


def test_lapsed_login_pages_once_and_rearms(rt):
    rt.claude_exe = "/opt/claude"
    out = auth_run(LOGGED_OUT, 1)
    check_claude_login(rt, out)
    check_claude_login(rt, out)
    [esc] = rt.notifier.sent
    assert esc.cause == "claude-login"
    assert "flocc login -H" in esc.action
    check_claude_login(rt, auth_run(FULL))
    check_claude_login(rt, out)
    assert len(rt.notifier.sent) == 2


def test_unanswered_login_probe_does_not_page(rt):
    rt.claude_exe = "/opt/claude"
    check_claude_login(rt, auth_run("not json"))
    assert rt.notifier.sent == []


def test_error_json_without_logged_in_is_no_answer(rt):
    rt.claude_exe = "/opt/claude"
    assert claude_login("/opt/claude", auth_run('{"error": "rate limited"}')) is None
    check_claude_login(rt, auth_run('{"error": "rate limited"}'))
    assert rt.notifier.sent == []
    assert ledger.meta_get(rt.con, "claude_login_lapsed") is None


def test_tick_runs_the_login_check(rt, tmp_path, monkeypatch):
    # conftest stubs the check out of every other tick test.
    rt.claude_exe = "/opt/claude"
    monkeypatch.setattr("flocc.tick.check_claude_login", lambda r: check_claude_login(r, out))
    out = auth_run(LOGGED_OUT)
    monkeypatch.setattr("flocc.tick.build_runtime", lambda cfg, path: rt)
    for name in (
        "forward_comments",
        "reap_worktrees",
        "clean_scratchpads",
        "poll",
        "maybe_digest",
    ):
        monkeypatch.setattr(f"flocc.tick.{name}", lambda r: set())
    assert tick(rt.cfg, tmp_path / "c.toml") == 0
    [esc] = rt.notifier.sent
    assert esc.cause == "claude-login"


def test_check_tokens_records_each_expiry_for_status(rt):
    def expiry(token):
        if token == "p":
            raise TokenRejected("401")
        return NOW + 10 * DAY

    check_tokens(rt, expiry)
    assert ledger.meta_get(rt.con, "token_expiry:agent") == str(int(NOW + 10 * DAY))
    assert ledger.meta_get(rt.con, "token_expiry:publish") == "rejected"
    rt.con.execute("DELETE FROM meta WHERE key = 'token_check_date'")
    check_tokens(rt, lambda token: None)
    assert ledger.meta_get(rt.con, "token_expiry:agent") == "never"
