import json
import shutil
import subprocess
import time
from pathlib import Path

import pytest

from flocc.agentblock import IssueSettings
from flocc.config import load
from flocc.sessions import (
    Sessions,
    agent_env,
    allowed_plugins,
    claude_argv,
    installed_plugins,
    session_name,
    trust_folders,
)
from tests.paths import EXAMPLE

CFG = load(EXAMPLE)
STATE = {"FLOCC_STATE": "/s/NF-1"}


def test_the_pane_gets_flocc_agent_s_environment_and_nothing_else(monkeypatch):
    monkeypatch.setenv("SUDO_UID", "1000")
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk")
    env = agent_env(CFG, {"FLOCC_PHASE": "impl"})
    assert env["HOME"] == "/home/flocc-agent" and env["USER"] == "flocc-agent"
    assert env["PATH"].startswith("/opt/flocc/bin:/home/flocc-agent/.local/bin:")
    assert env["FLOCC_PHASE"] == "impl"
    assert not {"SUDO_UID", "ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR"} & set(env)


def test_argv_wraps_claude_in_a_clean_env():
    s = IssueSettings(model="opus", effort="high", mcp=("sentry",))
    argv = claude_argv(
        CFG,
        "NF-1",
        s,
        "do the thing",
        env={"HOME": "/h", "X": "1", **STATE},
        claude_exe="/opt/claude",
        exe="/opt/flocc",
    )
    assert argv[:4] == ["/usr/bin/env", "-i", "HOME=/h", "X=1"]
    i = argv.index("/opt/claude")
    assert argv[i - 1] == str(CFG.agent_token_file) and argv[i + 1] == "do the thing"
    assert argv[-2] == "--mcp-config"
    assert json.loads(argv[-1]) == {"mcpServers": {"sentry": CFG.mcp_servers["sentry"]}}
    assert argv[argv.index("--remote-control") + 1] == "NF-1"
    assert argv[argv.index("--model") + 1] == "opus"
    settings = json.loads(argv[argv.index("--settings") + 1])
    assert settings["statusLine"]["command"] == "/opt/flocc statusline"
    assert "--strict-mcp-config" in argv


def test_session_settings_drop_attribution_and_deny_no_verify_commits():
    argv = claude_argv(CFG, "NF-1", IssueSettings(), "p", env=STATE, claude_exe="c", exe="x")
    settings = json.loads(argv[argv.index("--settings") + 1])
    assert settings["attribution"] is False
    deny = settings["permissions"]["deny"]
    assert "Bash(git commit --no-verify:*)" in deny and "Bash(git commit -n:*)" in deny


def test_unknown_mcp_server_raises():
    with pytest.raises(KeyError, match="nope"):
        claude_argv(CFG, "NF-1", IssueSettings(mcp=("nope",)), "p", env={}, claude_exe="c", exe="x")


def test_trust_marks_onboarding_done_and_keeps_the_rest(tmp_path):
    cj = tmp_path / ".claude.json"
    cj.write_text(json.dumps({"oauthAccount": {"emailAddress": "a@b"}, "projects": {}}))
    trust_folders(cj, ["/wt/NF-1"])
    data = json.loads(cj.read_text())
    assert data["hasCompletedOnboarding"] is True
    assert data["oauthAccount"] == {"emailAddress": "a@b"}
    assert data["projects"]["/wt/NF-1"]["hasTrustDialogAccepted"] is True
    assert cj.stat().st_mode & 0o777 == 0o600


def test_trust_marks_onboarding_done_when_every_folder_is_trusted(tmp_path):
    cj = tmp_path / ".claude.json"
    cj.write_text(json.dumps({"projects": {"/wt/NF-1": {"hasTrustDialogAccepted": True}}}))
    trust_folders(cj, ["/wt/NF-1"])
    assert json.loads(cj.read_text())["hasCompletedOnboarding"] is True


def test_trust_leaves_an_already_trusted_file_untouched(tmp_path):
    cj = tmp_path / ".claude.json"
    cj.write_text(
        json.dumps(
            {
                "hasCompletedOnboarding": True,
                "projects": {"/wt/NF-1": {"hasTrustDialogAccepted": True}},
            }
        )
    )
    before = cj.stat()
    trust_folders(cj, ["/wt/NF-1"])
    after = cj.stat()
    assert (before.st_ino, before.st_mtime_ns) == (after.st_ino, after.st_mtime_ns)


def test_unreadable_claude_json_is_not_overwritten(tmp_path):
    cj = tmp_path / ".claude.json"
    cj.write_text("{half written")
    with pytest.raises(ValueError):
        trust_folders(cj, ["/wt/NF-1"])
    assert cj.read_text() == "{half written"


def test_session_names():
    assert session_name("NF-1", "test") == "NF-1-test"
    assert session_name("NF-1", "impl") == "NF-1"
    assert session_name("NF-1", "fix") == "NF-1"


def test_real_tmux_lifecycle(tmux_server, tmp_path):
    s = Sessions(socket=tmux_server)
    s.start("NF-12", tmp_path, ["sh", "-c", "cat > out.txt"])
    assert s.alive("NF-12") and not s.alive("NF-1")
    s.send("NF-12", "hello")
    time.sleep(0.5)
    s.kill("NF-12")
    assert not s.alive("NF-12")
    assert (tmp_path / "out.txt").read_text().startswith("hello")


@pytest.mark.skipif(not shutil.which("tmux"), reason="tmux not installed")
def test_no_server_is_an_error_not_a_new_server(tmp_path):
    s = Sessions(socket=f"la-none-{time.time_ns()}")
    with pytest.raises(subprocess.CalledProcessError):
        s.start("NF-1", tmp_path, ["true"])


def settings_of(argv):
    return json.loads(argv[argv.index("--settings") + 1])


def argv_with(**kw):
    return claude_argv(CFG, "NF-1", IssueSettings(), "p", env=STATE, claude_exe="c", exe="x", **kw)


def test_settings_deny_general_purpose_and_nested_claude_but_allow_questions():
    s = settings_of(argv_with())
    deny = s["permissions"]["deny"]
    assert "Agent(general-purpose)" in deny and "Bash(claude:*)" in deny
    assert "AskUserQuestion" not in deny
    assert s["disableWorkflows"] is True


def test_remote_control_can_be_left_off():
    assert "--remote-control" in argv_with()
    argv = argv_with(remote_control=False)
    assert "--remote-control" not in argv and argv[argv.index("--name") + 1] == "NF-1"


def test_installed_and_allowed_plugin_files(tmp_path):
    inst = tmp_path / "installed_plugins.json"
    inst.write_text(json.dumps({"plugins": {"a@m": [{"scope": "user"}], "b@m": []}}))
    assert installed_plugins(inst) == ("a@m", "b@m")
    assert installed_plugins(tmp_path / "none.json") == ()
    allow = tmp_path / "agent-plugins.txt"
    allow.write_text("# comment\na@m\n\n")
    assert allowed_plugins(allow) == ("a@m",)
    assert allowed_plugins(tmp_path / "none.txt") is None


def test_sessions_run_in_bypass_mode_without_the_warning_dialog():
    argv = claude_argv(CFG, "NF-1", IssueSettings(), "p", env=STATE, claude_exe="c", exe="x")
    assert argv[argv.index("--permission-mode") + 1] == "bypassPermissions"
    settings = json.loads(argv[argv.index("--settings") + 1])
    assert settings["skipDangerousModePermissionPrompt"] is True
    assert set(settings["permissions"]) == {"deny"} and "autoMode" not in settings


def test_trust_no_longer_writes_the_auto_mode_dialog_key(tmp_path):
    path = tmp_path / ".claude.json"
    trust_folders(path, [tmp_path / "wt"])
    assert "autoModeEnvSetup" not in json.loads(path.read_text())


def test_capture_asks_tmux_for_plain_text():
    calls = []

    def run(argv, **kw):
        calls.append(argv)
        return subprocess.CompletedProcess(argv, 0, stdout="a\nb\n\n\n", stderr="")

    out = Sessions(runner=run).capture("NF-240", 50)
    assert calls[-1] == [
        "tmux",
        "-N",
        "-L",
        "flocc",
        "capture-pane",
        "-p",
        "-J",
        "-t",
        "=NF-240:",
        "-S",
        "-50",
    ]
    assert out == "a\nb\n\n\n"


def test_capture_of_a_dead_session_is_empty():
    def run(argv, **kw):
        assert kw["check"] is False
        return subprocess.CompletedProcess(argv, 1, stdout="", stderr="no session")

    assert Sessions(runner=run).capture("NF-240", 5) == ""


def test_pane_pid_is_the_pane_s_own_process(tmux_server, tmp_path):
    s = Sessions(socket=tmux_server)
    s.start("NF-1", tmp_path, ["sleep", "30"])
    try:
        pid = s.pane_pid("NF-1")
        assert pid is not None
        # The pane's pid is a fork of the tmux server until it execs the command.
        for _ in range(50):
            if Path(f"/proc/{pid}/cmdline").read_bytes().startswith(b"sleep\0"):
                break
            time.sleep(0.1)
        assert Path(f"/proc/{pid}/cmdline").read_bytes().startswith(b"sleep\0")
    finally:
        s.kill("NF-1")
    assert s.pane_pid("NF-1") is None
