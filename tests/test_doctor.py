import io
import json
import subprocess
import urllib.error
from dataclasses import replace

import pytest

from flocc import doctor
from flocc.config import load
from flocc.doctor import Result
from flocc.linear import Issue, LinearError
from flocc.tokens import TokenRejected, can_write_contents
from tests.paths import EXAMPLE

DAY = 86400
NOW = 1_800_000_000.0


def secret(tmp_path, text, mode=0o600):
    p = tmp_path / "s"
    p.write_text(text)
    p.chmod(mode)
    return p


def test_scope_check_only_reports_when_skipping_unmapped_issues():
    cfg = load(EXAMPLE)
    assert doctor.scope_check(cfg) == []
    [r] = doctor.scope_check(replace(cfg, skip_unmapped=True))
    assert r.status == "ok" and "left to other hosts" in r.detail


def test_secret_file_checks(tmp_path):
    assert doctor.secret_file("key", secret(tmp_path, "lin_api_x\n")).status == "ok"
    assert doctor.secret_file("key", tmp_path / "nope").status == "fail"
    assert doctor.secret_file("key", secret(tmp_path, "\n")).status == "fail"
    prefixed = doctor.secret_file("key", secret(tmp_path, "ACCESS_TOKEN=lin_api_x\n"))
    assert prefixed.status == "fail" and "ACCESS_TOKEN=" in prefixed.detail
    loose = doctor.secret_file("key", secret(tmp_path, "lin_api_x", 0o644))
    assert loose.status == "fail" and "chmod 600" in loose.detail


def test_github_slug_from_remote_urls():
    assert doctor.github_slug("https://github.com/o/r.git") == "o/r"
    assert doctor.github_slug("git@github.com:o/r") == "o/r"
    assert doctor.github_slug("/srv/git/r.git") is None


def test_token_checks(tmp_path):
    def expiry(token):
        if token == "dead":
            raise TokenRejected("401")
        return NOW + (5 if token == "soon" else 60) * DAY

    def check(token, writable):
        return doctor.token_checks(
            "agent" if not writable else "publish",
            secret(tmp_path, token),
            "o/r",
            writable,
            NOW,
            expiry,
            lambda t, slug: t.startswith("w"),
        )

    assert [r.status for r in check("ro", False)] == ["ok", "ok", "ok"]
    assert [r.status for r in check("soon", False)][1] == "warn"
    assert check("dead", False)[1].status == "fail"
    assert check("wide", False)[2].status == "fail"
    assert check("ro", True)[2].status == "fail"
    assert check("wr", True)[2].status == "ok"


def fake_run(outputs):
    def run(argv, **kw):
        out, code = outputs[" ".join(argv[1:])]
        return subprocess.CompletedProcess(argv, code, out, "")

    return run


def test_disk_check(tmp_cfg):
    def at(gb):
        return doctor.disk_check(tmp_cfg, free=lambda path: gb)

    assert at(5.0).status == "ok"
    assert at(3.9).status == "warn"
    low = at(1.9)
    assert low.status == "fail" and "min_free_disk_gb" in low.detail


def test_claude_checks(monkeypatch):
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    status = json.dumps({"loggedIn": True, "authMethod": "claude.ai"})
    good = fake_run({"--version": ("2.1.295 (Claude Code)\n", 0), "auth status": (status, 0)})
    results = doctor.claude_checks("/opt/claude", good)
    assert [r.status for r in results] == ["ok", "ok", "ok"]
    old = fake_run({"--version": ("2.1.200 (Claude Code)\n", 0), "auth status": (status, 0)})
    assert doctor.claude_checks("/opt/claude", old)[0].status == "fail"
    out = json.dumps({"loggedIn": False})
    logged_out = fake_run({"--version": ("2.1.295 (Claude Code)\n", 0), "auth status": (out, 1)})
    assert doctor.claude_checks("/opt/claude", logged_out)[1].status == "fail"
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk")
    assert doctor.claude_checks("/opt/claude", good)[2].status == "fail"
    assert doctor.claude_checks(None, good)[0].status == "fail"


def test_claude_login_fail_names_the_kind_and_the_desktop_command(monkeypatch):
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    token = json.dumps({"loggedIn": True, "authMethod": "oauth_token"})
    run = fake_run({"--version": ("2.1.295 (Claude Code)\n", 0), "auth status": (token, 0)})
    login = doctor.claude_checks("/opt/claude", run)[1]
    assert login.status == "fail"
    assert login.detail.startswith("token-only (oauth_token); on the desktop, run flocc login -H")


def test_tmux_check(tmux_server):
    assert doctor.tmux_check(tmux_server).status == "ok"
    assert doctor.tmux_check("flocc-test-no-such-server").status == "fail"


class DoctorLinear:
    def __init__(self, missing=(), digest_state="backlog", down=False):
        self.missing, self.digest_state, self.down = list(missing), digest_state, down
        self.label_calls = []

    def missing_labels(self, team_key, names):
        self.label_calls.append(list(names))
        if self.down:
            raise LinearError("Authentication required")
        return [n for n in names if n in self.missing]

    def issue(self, key):
        return Issue("i", key, "digest", "", "u", self.digest_state, (), None, "t1")


def test_linear_checks(tmp_cfg):
    assert [r.status for r in doctor.linear_checks(tmp_cfg, DoctorLinear())] == ["ok", "ok"]
    labels = doctor.linear_checks(tmp_cfg, DoctorLinear(missing=["escalation"]))[0]
    assert labels.status == "fail" and "escalation" in labels.detail
    fake = DoctorLinear(missing=["backend"])
    results = doctor.linear_checks(tmp_cfg, fake)
    assert fake.label_calls == [
        [tmp_cfg.trigger_label, "escalation", "needs-decision"],
        list(tmp_cfg.repos),
    ]
    assert [r.status for r in results] == ["ok", "warn", "ok"]
    assert "backend" in results[1].detail and "never routed" in results[1].detail
    closed = doctor.linear_checks(tmp_cfg, DoctorLinear(digest_state="completed"))[1]
    assert closed.status == "warn"
    [down] = doctor.linear_checks(tmp_cfg, DoctorLinear(down=True))
    assert down.status == "fail" and "Authentication" in down.detail


def test_report_exits_non_zero_on_a_failure(capsys):
    assert doctor.report([Result("ok", "a", "fine"), Result("warn", "b", "hm")]) == 0
    ok_out = capsys.readouterr().out
    assert doctor.report([Result("fail", "c", "broken")]) == 1
    out = capsys.readouterr().out
    assert "FAIL  c: broken" in out and "ok    a: fine" in ok_out
    assert "2 checks, 0 failed" in ok_out and "1 checks, 1 failed" in out


def test_ntfy_check_warns_on_placeholder():
    cfg = load(EXAMPLE)
    assert doctor.ntfy_check(cfg).status == "warn"
    assert doctor.ntfy_check(replace(cfg, ntfy_url="https://ntfy.sh/realtopic")).status == "ok"


def http_error(code):
    def opener(req, timeout):
        raise urllib.error.HTTPError(req.full_url, code, "x", {}, io.BytesIO(b"{}"))

    return opener


def test_write_probe_reads_permission_from_the_status():
    assert can_write_contents("t", "o/r", http_error(403)) is False
    assert can_write_contents("t", "o/r", http_error(422)) is True
    with pytest.raises(LookupError):
        can_write_contents("t", "o/r", http_error(404))


def test_write_probe_404_on_a_readable_repo_means_read_only():
    # GitHub answers a write without permission with 404 on repos the token can read but not
    # administer, not only on repos it cannot see.
    def opener(req, timeout):
        if req.get_method() == "POST":
            raise urllib.error.HTTPError(req.full_url, 404, "x", {}, io.BytesIO(b"{}"))
        return io.BytesIO(b"{}")

    assert can_write_contents("t", "o/r", opener) is False
