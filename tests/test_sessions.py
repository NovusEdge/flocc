import json
import shutil
import subprocess
import time
from dataclasses import replace

import pytest

from flocc.agentblock import IssueSettings
from flocc.config import load
from flocc.grants import Grants
from flocc.sessions import (
    A2A_TOOLS,
    Sessions,
    allowed_plugins,
    claude_argv,
    clean_env,
    installed_plugins,
    session_name,
    trust_folders,
)
from tests.paths import EXAMPLE

CFG = load(EXAMPLE)
STATE = {"FLOCC_STATE": "/s/NF-1"}


def a2a_server(exe):
    return {"type": "stdio", "command": exe, "args": ["mcp-a2a"], "env": STATE}


def test_clean_env_keeps_only_the_allowlist(monkeypatch):
    for key in ("HOME", "USER", "PATH"):
        monkeypatch.setenv(key, "/x")
    for key in ("ANTHROPIC_API_KEY", "CLAUDE_CODE_SESSION_ID", "CLAUDECODE", "CLAUDE_CONFIG_DIR"):
        monkeypatch.setenv(key, "x")
    env = clean_env({"FLOCC_STATE": "/s"})
    assert env.keys() == {"HOME", "USER", "PATH", "TERM", "LANG", "FLOCC_STATE"}


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
    assert argv[i - 1] == str(CFG.agent_gh_token_file) and argv[i + 1] == "do the thing"
    assert argv[-2] == "--mcp-config"
    assert json.loads(argv[-1]) == {
        "mcpServers": {
            "sentry": CFG.mcp_servers["sentry"],
            "flocc-a2a": a2a_server("/opt/flocc"),
        }
    }
    assert argv[argv.index("--remote-control") + 1] == "NF-1"
    assert argv[argv.index("--model") + 1] == "opus"
    settings = json.loads(argv[argv.index("--settings") + 1])
    assert settings["statusLine"]["command"] == "/opt/flocc statusline"
    assert "--strict-mcp-config" in argv


def test_session_settings_drop_attribution_and_deny_no_verify_commits():
    argv = claude_argv(CFG, "NF-1", IssueSettings(), "p", env=STATE, claude_exe="c", exe="x")
    settings =json.loads(argv[argv.index("--settings") + 1])
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


def test_trust_dismisses_the_auto_mode_setup_dialog_and_keeps_its_counts(tmp_path):
    cj = tmp_path / ".claude.json"
    cj.write_text(
        json.dumps(
            {
                "hasCompletedOnboarding": True,
                "projects": {"/wt/NF-1": {"hasTrustDialogAccepted": True}},
                "autoModeEnvSetup": {"denials": 5, "dismissedAt": 1792673965473},
            }
        )
    )
    trust_folders(cj, ["/wt/NF-1"])
    assert json.loads(cj.read_text())["autoModeEnvSetup"] == {
        "denials": 5,
        "dismissedAt": 1792673965473,
        "dismissed": True,
    }


def test_trust_leaves_an_already_trusted_file_untouched(tmp_path):
    cj = tmp_path / ".claude.json"
    cj.write_text(
        json.dumps(
            {
                "hasCompletedOnboarding": True,
                "projects": {"/wt/NF-1": {"hasTrustDialogAccepted": True}},
                "autoModeEnvSetup": {"dismissed": True},
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


def test_grants_lift_the_matching_launch_flags():
    s = settings_of(argv_with(grants=Grants(agents=("general-purpose",), workflows=("w",))))
    assert "Agent(general-purpose)" not in s["permissions"]["deny"]
    assert s["disableWorkflows"] is False


def test_unlisted_plugins_are_disabled():
    s = settings_of(argv_with(disabled_plugins=("glowup@glowup",)))
    assert s["enabledPlugins"] == {"glowup@glowup": False}


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


def test_the_hosts_rules_and_environment_reach_the_session():
    cfg = replace(
        CFG,
        permission_allow=("Bash(uv sync --frozen)",),
        permission_environment=("Nothing on this VM is production.",),
    )
    s = settings_of(claude_argv(cfg, "NF-1", IssueSettings(), "p", env=STATE, claude_exe="c", exe="x"))
    assert s["permissions"]["allow"] == ["Bash(uv sync --frozen)", *A2A_TOOLS]
    assert "Bash(claude:*)" in s["permissions"]["deny"]
    assert s["autoMode"] == {"environment": ["$defaults", "Nothing on this VM is production."]}


def test_no_rules_leave_auto_mode_alone_and_allow_only_the_a2a_tools():
    s = settings_of(argv_with())
    assert "autoMode" not in s and s["permissions"]["allow"] == list(A2A_TOOLS)


def test_sessions_get_the_a2a_server_and_its_tools():
    argv = argv_with()
    assert json.loads(argv[-1])["mcpServers"]["flocc-a2a"] == a2a_server("x")
    allow = settings_of(argv)["permissions"]["allow"]
    assert all(name in allow for name in A2A_TOOLS)


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
