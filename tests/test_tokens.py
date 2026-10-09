import email.message
import io
import urllib.error
from dataclasses import replace
from datetime import UTC, datetime
from types import SimpleNamespace

import pytest

from linear_agents import ledger
from linear_agents.tokens import TokenRejected, check_tokens, token_expiry
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
    assert (esc.tier, esc.scope, esc.cause) == (1, "global", "gh-token-agent")
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
