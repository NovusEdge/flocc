import io
import json
import re
import subprocess
from pathlib import Path

import pytest

from flocc import agentrun
from flocc.agent import Agent, AgentError
from flocc.agentrun import apply_handoff, restore, with_disabled_plugins

ROOT = Path(__file__).parent.parent
WRAPPER = ROOT / "deploy" / "flocc-agent-run"


def test_the_wrapper_and_the_module_know_the_same_subcommands():
    listed = re.search(r"^  ([a-z|-]+)\) ;;$", WRAPPER.read_text(), re.MULTILINE)
    assert listed and sorted(listed.group(1).split("|")) == sorted(agentrun.SUBCOMMANDS)


def test_the_wrapper_refuses_an_unknown_subcommand():
    for argv in (["rm", "-rf", "/"], []):
        out = subprocess.run(
            ["sh", str(WRAPPER), *argv], capture_output=True, text=True, check=False
        )
        assert out.returncode == 2 and "unknown subcommand" in out.stderr


def test_the_sudoers_rule_allows_only_the_wrapper():
    rules = [
        line
        for line in (ROOT / "deploy" / "flocc-agent.sudoers").read_text().splitlines()
        if line.strip() and not line.startswith("#")
    ]
    assert rules == ["flocc ALL=(flocc-agent) NOPASSWD: /usr/local/libexec/flocc-agent-run"]


def test_the_module_refuses_unknown_subcommands_and_bad_names(capsys):
    assert agentrun.main(["shell"]) == 2
    assert agentrun.main(["kill", "NF-1; rm -rf ~"]) == 2
    assert "bad request" in capsys.readouterr().err


def test_handoff_clears_markers_writes_files_and_can_put_markers_back(tmp_path):
    (tmp_path / "done.json").write_text('{"summary": "old"}')
    saved = apply_handoff(tmp_path, {"context.md": "CTX"}, ["done.json", "idle"])
    assert not (tmp_path / "done.json").exists() and (tmp_path / "context.md").read_text() == "CTX"
    restore(tmp_path, saved)
    assert (tmp_path / "done.json").read_text() == '{"summary": "old"}'
    with pytest.raises(agentrun.BadRequest):
        apply_handoff(tmp_path, {"../x": "y"}, [])


def test_unlisted_plugins_are_disabled_in_the_settings(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    plugins = tmp_path / ".claude" / "plugins"
    plugins.mkdir(parents=True)
    (plugins / "installed_plugins.json").write_text(json.dumps({"plugins": {"a@m": [], "b@m": []}}))
    argv = ["claude", "--settings", json.dumps({"x": 1}), "--name", "NF-1"]
    out = with_disabled_plugins(argv, ["a@m"])
    assert json.loads(out[2]) == {"x": 1, "enabledPlugins": {"b@m": False}}
    assert with_disabled_plugins(argv, None) == argv


def test_start_through_the_module_writes_the_token_and_the_files(tmp_path, monkeypatch):
    monkeypatch.setattr("flocc.agentrun.Sessions.start", lambda self, name, cwd, argv: None)
    monkeypatch.setenv("HOME", str(tmp_path))
    state = tmp_path / "state" / "NF-1"
    request = {
        "name": "NF-1",
        "cwd": str(tmp_path / "wt" / "NF-1"),
        "argv": ["true"],
        "state": str(state),
        "files": {"context.md": "CTX"},
        "clear": ["done.json"],
        "trust": [],
        "plugins": None,
        "token": "ghp_x\n",
    }
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(request)))
    assert agentrun.main(["start"]) == 0
    assert (state / "context.md").read_text() == "CTX"
    token = tmp_path / ".config" / "flocc" / "gh-agent.token"
    assert token.read_text() == "ghp_x\n" and token.stat().st_mode & 0o777 == 0o600
    trusted = json.loads((tmp_path / ".claude.json").read_text())["projects"]
    assert trusted[str(tmp_path / "wt" / "NF-1")]["hasTrustDialogAccepted"] is True


def test_start_outside_the_agent_home_is_refused(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("HOME", str(tmp_path / "agent"))
    request = {"name": "NF-1", "cwd": "/etc", "argv": ["true"], "state": "/etc/x", "token": ""}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(request)))
    assert agentrun.main(["start"]) == 2 and "outside" in capsys.readouterr().err


def test_the_client_runs_the_module_and_maps_failures(tmp_cfg):
    agent = Agent(tmp_cfg)
    assert agent.sessions().alive("NF-999") is False
    with pytest.raises(AgentError):
        agent.call("send", payload={"name": "bad name", "text": "x"})


def test_the_client_sets_the_agent_home(tmp_cfg):
    seen = {}

    def runner(argv, **kw):
        seen.update(kw)
        return subprocess.CompletedProcess(argv, 0, "{}", "")

    Agent(tmp_cfg, runner=runner).call("send", payload={"name": "NF-1", "text": "hi"})
    assert seen["env"]["HOME"] == str(tmp_cfg.agent_home)
    assert json.loads(seen["input"]) == {"name": "NF-1", "text": "hi"}
