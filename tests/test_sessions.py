import json
import shutil
import subprocess
import time

import pytest

from flocc.agentblock import IssueSettings
from flocc.config import load
from flocc.grants import Grants
from flocc.sessions import (
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
        env={"HOME": "/h", "X": "1"},
        claude_exe="/opt/claude",
        exe="/opt/flocc",
    )
    assert argv[:4] == ["/usr/bin/env", "-i", "HOME=/h", "X=1"]
    i = argv.index("/opt/claude")
    assert argv[i - 1] == str(CFG.agent_gh_token_file) and argv[i + 1] == "do the thing"
    assert argv[-2] == "--mcp-config"
    assert json.loads(argv[-1]) == {"mcpServers": {"sentry": CFG.mcp_servers["sentry"]}}
    assert argv[argv.index("--remote-control") + 1] == "NF-1"
    assert argv[argv.index("--model") + 1] == "opus"
    settings = json.loads(argv[argv.index("--settings") + 1])
    assert settings["statusLine"]["command"] == "/opt/flocc statusline"
    assert "--strict-mcp-config" in argv


def test_session_settings_drop_attribution_and_deny_no_verify_commits():
    argv = claude_argv(CFG, "NF-1", IssueSettings(), "p", env={}, claude_exe="c", exe="x")
    settings = json.loads(argv[argv.index("--settings") + 1])
    assert settings["attribution"] is False
    deny = settings["permissions"]["deny"]
    assert "Bash(git commit --no-verify:*)" in deny and "Bash(git commit -n:*)" in deny


def test_unknown_mcp_server_raises():
    with pytest.raises(KeyError, match="nope"):
        claude_argv(CFG, "NF-1", IssueSettings(mcp=("nope",)), "p", env={}, claude_exe="c", exe="x")


def test_trust_leaves_an_already_trusted_file_untouched(tmp_path):
    cj = tmp_path / ".claude.json"
    cj.write_text(json.dumps({"projects": {"/wt/NF-1": {"hasTrustDialogAccepted": True}}}))
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
    return claude_argv(CFG, "NF-1", IssueSettings(), "p", env={}, claude_exe="c", exe="x", **kw)


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
