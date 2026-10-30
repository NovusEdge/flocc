import io
import json
import subprocess
import urllib.error
from dataclasses import replace
from pathlib import Path

import pytest

from flocc import doctor
from flocc.config import load
from flocc.doctor import Result, agent_checks, managed_check
from flocc.linear import Issue, LinearError
from flocc.tokens import TokenRejected, can_write_contents
from tests.fakes import FakeAgent
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


def test_claude_checks():
    status = json.dumps({"loggedIn": True, "authMethod": "claude.ai"})
    good = fake_run({"--version": ("2.1.295 (Claude Code)\n", 0), "auth status": (status, 0)})
    results = doctor.claude_checks(["/opt/claude"], good)
    assert [r.status for r in results] == ["ok", "ok"]
    old = fake_run({"--version": ("2.1.200 (Claude Code)\n", 0), "auth status": (status, 0)})
    assert doctor.claude_checks(["/opt/claude"], old)[0].status == "fail"
    out = json.dumps({"loggedIn": False})
    logged_out = fake_run({"--version": ("2.1.295 (Claude Code)\n", 0), "auth status": (out, 1)})
    assert doctor.claude_checks(["/opt/claude"], logged_out)[1].status == "fail"
    assert doctor.claude_checks(None, good)[0].status == "fail"


def test_claude_login_fail_names_the_kind_and_the_desktop_command():
    token = json.dumps({"loggedIn": True, "authMethod": "oauth_token"})
    run = fake_run({"--version": ("2.1.295 (Claude Code)\n", 0), "auth status": (token, 0)})
    login = doctor.claude_checks(["/opt/claude"], run)[1]
    assert login.status == "fail"
    assert login.detail.startswith("token-only (oauth_token); on the desktop, run flocc login -H")


def test_the_boundary_check_fails_when_flocc_agent_can_read_a_secret(tmp_cfg, tmp_path):
    agent = FakeAgent(tmp_cfg)
    key = str(tmp_cfg.linear_key_file)
    agent.probe_result = {"read": {key: True}, "write": {}, "tmux": True, "groups": []}
    results = agent_checks(tmp_cfg, tmp_path / "c.toml", agent)
    (boundary,) = [r for r in results if r.name == "agent boundary"]
    assert boundary.status == "fail" and key in boundary.detail


def test_the_boundary_check_fails_when_flocc_agent_can_write_the_flocc_binary(tmp_cfg, tmp_path):
    agent = FakeAgent(tmp_cfg)
    seen = {}
    real = agent.probe

    def probe(read, write):
        seen["write"] = write
        return real(read, write)

    agent.probe = probe
    agent_checks(tmp_cfg, tmp_path / "c.toml", agent)
    bin_dir = str(tmp_cfg.agent_flocc.parent)
    assert {bin_dir, str(tmp_cfg.state_root)} <= set(seen["write"])
    agent.probe_result = {"read": {}, "write": {bin_dir: True}, "tmux": True, "groups": []}
    results = agent_checks(tmp_cfg, tmp_path / "c.toml", agent)
    (boundary,) = [r for r in results if r.name == "agent boundary"]
    assert boundary.status == "fail" and bin_dir in boundary.detail


def test_the_docker_group_fails_the_boundary(tmp_cfg, tmp_path):
    agent = FakeAgent(tmp_cfg)
    agent.probe_result = {
        "read": {"/x": False},
        "write": {"/opt/flocc/plugin": False},
        "tmux": False,
        "groups": ["flocc-agent", "docker"],
        "containers": False,
    }
    by_name = {r.name: r for r in agent_checks(tmp_cfg, tmp_path / "c.toml", agent)}
    assert by_name["agent boundary"].status == "ok"
    docker = by_name["agent docker"]
    assert docker.status == "fail" and "root-equivalent" in docker.detail
    assert by_name["containers"].status == "ok"
    assert "no Podman socket" in by_name["containers"].detail
    assert by_name["tmux"].status == "fail"


def test_a_clean_agent_passes_every_line(tmp_cfg, tmp_path):
    agent = FakeAgent(tmp_cfg)
    agent.probe_result = {
        "read": {},
        "write": {},
        "tmux": True,
        "groups": ["flocc-agent"],
        "containers": True,
    }
    by_name = {r.name: r for r in agent_checks(tmp_cfg, tmp_path / "c.toml", agent)}
    assert by_name["agent docker"].status == "ok"
    assert "Podman socket" in by_name["containers"].detail


def test_managed_settings_must_carry_the_guard_and_the_dialog_denies(tmp_path):
    path = tmp_path / "managed-settings.json"
    assert managed_check(path).status == "fail"
    path.write_text("{}")
    assert managed_check(path).status == "fail"
    shipped = Path(__file__).parent.parent / "deploy" / "managed-settings.json"
    path.write_text(shipped.read_text())
    assert managed_check(path).status == "ok"


def test_an_unanswered_wrapper_fails_the_agent_check(tmp_cfg, tmp_path):
    agent = FakeAgent(tmp_cfg)

    def silent(read, write):
        raise subprocess.SubprocessError("sudo: a password is required")

    agent.probe = silent
    (result,) = agent_checks(tmp_cfg, tmp_path / "c.toml", agent)
    assert result.status == "fail" and "sudoers" in result.detail


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
    labels = doctor.linear_checks(tmp_cfg, DoctorLinear(missing=["claude"]))[0]
    assert labels.status == "fail" and "claude" in labels.detail
    fake = DoctorLinear(missing=["backend"])
    results = doctor.linear_checks(tmp_cfg, fake)
    assert fake.label_calls == [[tmp_cfg.trigger_label], list(tmp_cfg.repos)]
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


def test_doctor_json_prints_the_checks_and_keeps_the_exit_code(tmp_path, capsys, monkeypatch):
    from flocc.cli import main

    results = [
        Result("ok", "git", "/usr/bin/git"),
        Result("warn", "disk", "placeholder"),
        Result("fail", "tmux", "no server"),
    ]
    monkeypatch.setattr("flocc.doctor.collect_results", lambda cfg, path: results)
    assert main(["--config", str(EXAMPLE), "doctor", "--json"]) == 1
    out = json.loads(capsys.readouterr().out)
    assert out["v"] == 1
    assert out["checks"][0] == {"name": "config", "ok": True, "detail": str(EXAMPLE)}
    # A warning stays ok=true for older desktops and carries warn=true for newer ones.
    assert out["checks"][1:] == [
        {"name": "git", "ok": True, "detail": "/usr/bin/git"},
        {"name": "disk", "ok": True, "warn": True, "detail": "placeholder"},
        {"name": "tmux", "ok": False, "detail": "no server"},
    ]


def test_plain_doctor_output_is_unchanged(tmp_path, capsys, monkeypatch):
    from flocc.cli import main

    monkeypatch.setattr(
        "flocc.doctor.collect_results", lambda cfg, path: [Result("warn", "disk", "x")]
    )
    assert main(["--config", str(EXAMPLE), "doctor"]) == 0
    assert capsys.readouterr().out == (
        f"ok    config: {EXAMPLE}\nwarn  disk: x\n1 checks, 0 failed\n"
    )
