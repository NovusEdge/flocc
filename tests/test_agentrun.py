import io
import json
import os
import re
import shutil
import subprocess
from dataclasses import replace
from pathlib import Path

import pytest

from flocc import agentrun
from flocc.agent import Agent, AgentError
from flocc.agentrun import apply_handoff, restore, with_disabled_plugins
from flocc.publisher import check_worktree
from flocc.worktree import agent_ref, fetch_agent_branch, git

ROOT = Path(__file__).parent.parent
WRAPPER = ROOT / "deploy" / "flocc-agent-run"


def agent_cfg(tmp_cfg, origin, tmp_path):
    # The origin fixture's clone is tmp_path/src, so flocc-agent's home goes beside it.
    home = tmp_path / "agent"
    return replace(
        tmp_cfg,
        agent_home=home,
        worktree_root=home / "worktrees",
        agent_state_root=home / "state",
        repos={"backend": replace(tmp_cfg.repos["backend"], path=origin)},
    )


def test_prepare_clones_for_flocc_agent_and_adds_the_worktree(tmp_cfg, origin, tmp_path):
    cfg = agent_cfg(tmp_cfg, origin, tmp_path)
    path, branch, _ = Agent(cfg).prepare("backend", "NF-7")
    home = cfg.agent_home
    assert path == (home / "worktrees").resolve() / "NF-7" and branch == "agent/nf-7"
    assert (home / "src" / "backend" / ".git").is_dir()
    assert (path / ".setup-ran").exists()
    assert (home / "state" / "NF-7" / "hooks" / "commit-msg").exists()
    assert (home / ".config" / "flocc" / "gh-agent.token").read_text() == "t\n"


def test_a_failing_setup_comes_back_as_the_setup_error(tmp_cfg, origin, tmp_path):
    (origin / "setup").write_text("#!/bin/sh\necho broken >&2\nexit 3\n")
    git("commit", "-qam", "break setup", cwd=origin)
    git("push", "-q", "origin", "main", cwd=origin)
    cfg = agent_cfg(tmp_cfg, origin, tmp_path)
    with pytest.raises(subprocess.CalledProcessError) as exc:
        Agent(cfg).prepare("backend", "NF-7")
    assert exc.value.cmd == ["./setup"] and "broken" in exc.value.stderr
    assert exc.value.__dict__["at_base"] == git("rev-parse", "origin/main", cwd=origin)


def test_check_reports_dirty_failed_and_ok(origin):
    assert check_worktree("true", origin, 30)["status"] == "ok"
    assert check_worktree("false", origin, 30)["status"] == "failed"
    (origin / "stray.txt").write_text("x")
    dirty = check_worktree("true", origin, 30)
    assert dirty["status"] == "dirty" and "stray.txt" in dirty["output"]


def test_the_branch_is_fetched_into_the_service_clone(origin, tmp_path):
    service = tmp_path / "service"
    subprocess.run(["git", "clone", "-q", str(tmp_path / "origin.git"), str(service)], check=True)
    git("checkout", "-qb", "agent/nf-1", cwd=origin)
    (origin / "b.txt").write_text("b\n")
    git("add", ".", cwd=origin)
    git("commit", "-qm", "work", cwd=origin)
    head = fetch_agent_branch(service, origin, "agent/nf-1", "NF-1")
    assert head == git("rev-parse", "HEAD", cwd=origin)
    assert git("rev-parse", agent_ref("NF-1"), cwd=service) == head


def test_remove_takes_the_worktree_its_state_and_its_scratchpads(tmp_cfg, origin, tmp_path):
    cfg = agent_cfg(tmp_cfg, origin, tmp_path)
    agent = Agent(cfg)
    path, _, _ = agent.prepare("backend", "NF-7")
    pad = Path(f"/tmp/claude-{os.getuid()}/flocc-test-{os.getpid()}/session/scratchpad")
    pad.mkdir(parents=True, exist_ok=True)
    state = cfg.agent_state_root / "NF-7"
    (state / "scratchpads").write_text(f"{pad}\n/etc\n")
    assert agent.remove("NF-7") == {"removed": True, "dirty": False}
    assert not path.exists() and not state.exists() and not pad.exists()
    shutil.rmtree(pad.parent.parent, ignore_errors=True)


def test_remove_keeps_a_dirty_worktree_when_asked(tmp_cfg, origin, tmp_path):
    cfg = agent_cfg(tmp_cfg, origin, tmp_path)
    agent = Agent(cfg)
    path, _, _ = agent.prepare("backend", "NF-7")
    (path / "work.py").write_text("x\n")
    assert agent.remove("NF-7", keep_dirty=True) == {"removed": False, "dirty": True}
    assert path.exists()


def test_scratch_paths_outside_the_user_s_pads_are_refused():
    root = f"/tmp/claude-{os.getuid()}"
    assert agentrun.scratch_path(f"{root}/p/s/scratchpad") == Path(f"{root}/p/s/scratchpad")
    for line in ("/etc", f"{root}/p", f"{root}/p/s/../../../etc", ""):
        assert agentrun.scratch_path(line) is None


def test_scratch_paths_do_not_follow_links(tmp_path):
    root = Path(f"/tmp/claude-{os.getuid()}")
    base = root / f"flocc-link-test-{os.getpid()}"
    other = base / "s2" / "scratchpad"
    other.mkdir(parents=True)
    try:
        link = base / "s1" / "scratchpad"
        link.parent.mkdir()
        link.symlink_to(other)
        assert agentrun.scratch_path(str(link)) is None
    finally:
        shutil.rmtree(base, ignore_errors=True)


def test_remove_prunes_stopped_containers_only_with_a_podman_socket(monkeypatch):
    ran = []
    monkeypatch.setattr("flocc.agentrun.shutil.which", lambda name: "/usr/bin/podman")
    monkeypatch.setattr("flocc.agentrun.subprocess.run", lambda argv, **kw: ran.append(argv))
    monkeypatch.setattr("flocc.agentrun.container_env", dict)
    agentrun._prune_containers()
    assert ran == []
    monkeypatch.setattr("flocc.agentrun.container_env", lambda: {"DOCKER_HOST": "unix:///s"})
    agentrun._prune_containers()
    assert ran == [["podman", "container", "prune", "-f"]]


def test_prepare_and_check_clear_what_a_killed_setup_left(tmp_path):
    (tmp_path / ".uv-cache-NF-1" / "wheels").mkdir(parents=True)
    (tmp_path / ".canary-x").mkdir()
    (tmp_path / ".uv-cache").mkdir()
    agentrun._clean_stale(tmp_path)
    assert not (tmp_path / ".uv-cache-NF-1").exists() and not (tmp_path / ".canary-x").exists()
    assert (tmp_path / ".uv-cache").exists()


@pytest.mark.parametrize(
    "args",
    [["auth", "logout"], ["-p", "hi"], ["auth", "login", "--console"], ["plugin", "install", "x"]],
)
def test_claude_runs_only_the_listed_invocations(args, capsys):
    assert agentrun.main(["claude", *args]) == 2
    assert "not allowed" in capsys.readouterr().err


def test_probe_reports_what_flocc_agent_can_reach(tmp_path, monkeypatch):
    readable = tmp_path / "r"
    readable.write_text("x")
    request = {"read": [str(readable), str(tmp_path / "missing")], "write": [str(tmp_path)]}
    monkeypatch.setattr("flocc.agentrun._tmux_up", lambda: False)
    out = agentrun.probe(request)
    assert out["read"] == {str(readable): True, str(tmp_path / "missing"): False}
    assert out["write"] == {str(tmp_path): True} and out["tmux"] is False
    assert isinstance(out["groups"], list)


def test_container_env_points_at_the_podman_socket(tmp_path, monkeypatch):
    sock = tmp_path / "podman" / "podman.sock"
    sock.parent.mkdir()
    sock.touch()
    monkeypatch.setattr("flocc.agentrun._runtime_dir", lambda: tmp_path)
    assert agentrun.container_env() == {
        "XDG_RUNTIME_DIR": str(tmp_path),
        "DOCKER_HOST": f"unix://{sock}",
        "TESTCONTAINERS_RYUK_DISABLED": "true",
    }


def test_no_socket_means_no_container_env(tmp_path, monkeypatch):
    monkeypatch.setattr("flocc.agentrun._runtime_dir", lambda: tmp_path)
    assert agentrun.container_env() == {}


def test_the_pane_gets_the_container_env_after_env_i(monkeypatch):
    monkeypatch.setattr("flocc.agentrun.container_env", lambda: {"DOCKER_HOST": "unix:///s"})
    argv = ["/usr/bin/env", "-i", "HOME=/h", "claude", "--name", "NF-1"]
    assert agentrun.with_container_env(argv) == [
        "/usr/bin/env",
        "-i",
        "DOCKER_HOST=unix:///s",
        "HOME=/h",
        "claude",
        "--name",
        "NF-1",
    ]
    assert agentrun.with_container_env(["claude"]) == ["claude"]


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
