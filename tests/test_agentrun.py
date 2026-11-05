import io
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import time
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


def test_sessions_and_checks_get_a_tmpdir_on_disk_under_home(tmp_path, monkeypatch):
    monkeypatch.setattr("flocc.agentrun._home", lambda: tmp_path)
    assert agentrun.tmp_env() == {"TMPDIR": str(tmp_path / ".cache" / "flocc-tmp")}
    assert agentrun._pytest_root().parent == tmp_path / ".cache" / "flocc-tmp"


def test_main_creates_the_tmpdir_private_and_exports_it(tmp_path, monkeypatch):
    monkeypatch.setattr("flocc.agentrun._home", lambda: tmp_path)
    monkeypatch.setattr("flocc.agentrun.container_env", dict)
    monkeypatch.setenv("TMPDIR", "/tmp")
    assert agentrun.main(["rm"]) == 2
    made = tmp_path / ".cache" / "flocc-tmp"
    assert made.is_dir() and made.stat().st_mode & 0o777 == 0o700
    assert os.environ["TMPDIR"] == str(made)


def test_no_socket_means_no_container_env(tmp_path, monkeypatch):
    monkeypatch.setattr("flocc.agentrun._runtime_dir", lambda: tmp_path)
    assert agentrun.container_env() == {}


def test_the_pane_gets_the_container_env_after_env_i(monkeypatch):
    monkeypatch.setattr("flocc.agentrun.container_env", lambda: {"DOCKER_HOST": "unix:///s"})
    monkeypatch.setattr("flocc.agentrun._tmp_root", lambda: Path("/h/.cache/flocc-tmp"))
    argv = ["/usr/bin/env", "-i", "HOME=/h", "claude", "--name", "NF-1"]
    assert agentrun.with_container_env(argv) == [
        "/usr/bin/env",
        "-i",
        "TMPDIR=/h/.cache/flocc-tmp",
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


@pytest.mark.parametrize("sub", ["usage", "stop-tests", "shell"])
def test_the_wrapper_passes_the_host_repair_subcommands_on(sub):
    out = subprocess.run(
        ["sh", str(WRAPPER), sub], input="{}", capture_output=True, text=True, check=False
    )
    assert "unknown subcommand" not in out.stderr


def test_the_sudoers_rule_allows_only_the_wrapper():
    rules = [
        line
        for line in (ROOT / "deploy" / "flocc-agent.sudoers").read_text().splitlines()
        if line.strip() and not line.startswith("#")
    ]
    assert rules == ["flocc ALL=(flocc-agent) NOPASSWD: /usr/local/libexec/flocc-agent-run"]


def test_the_module_refuses_unknown_subcommands_and_bad_names(capsys):
    assert agentrun.main(["rm"]) == 2
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


def _sweep(cfg, keep, now, scratch_age_s=86400.0, pytest_age_s=86400.0):
    return agentrun.sweep(
        {
            "keep": keep,
            "worktree_root": str(cfg.worktree_root),
            "state_root": str(cfg.agent_state_root),
            "now": now,
            "max_age_s": 7 * 86400.0,
            "scratch_age_s": scratch_age_s,
            "pytest_age_s": pytest_age_s,
        }
    )


def _age(path, now, days):
    os.utime(path, (now - days * 86400, now - days * 86400))


NOW = 2_000_000_000.0


@pytest.fixture
def pads(tmp_path, monkeypatch):
    # NOW is years ahead, so every real scratchpad would be old, and a real podman socket would
    # get its containers stopped: every sweep test points both away.
    root = tmp_path / "pads"
    root.mkdir()
    (tmp_path / "tmp").mkdir()
    monkeypatch.setattr("flocc.agentrun._tmp_root", lambda: tmp_path / "tmp")
    monkeypatch.setattr("flocc.agentrun._scratch_root", lambda: root)
    monkeypatch.setattr("flocc.agentrun.container_env", dict)
    return root


def test_sweep_removes_an_old_clean_pushed_orphan(tmp_cfg, origin, tmp_path, monkeypatch, pads):
    cfg = agent_cfg(tmp_cfg, origin, tmp_path)
    monkeypatch.setenv("HOME", str(cfg.agent_home))
    path, _, _ = Agent(cfg).prepare("backend", "NF-7")
    _age(path, NOW, 8)
    out = _sweep(cfg, [], NOW)
    assert str(path) in out["removed"] and not path.exists()


def test_sweep_never_touches_a_kept_key(tmp_cfg, origin, tmp_path, monkeypatch, pads):
    cfg = agent_cfg(tmp_cfg, origin, tmp_path)
    monkeypatch.setenv("HOME", str(cfg.agent_home))
    path, _, _ = Agent(cfg).prepare("backend", "NF-7")
    state = cfg.agent_state_root / "NF-7"
    _age(path, NOW, 30)
    _age(state, NOW, 30)
    assert _sweep(cfg, ["NF-7"], NOW) == {"removed": [], "kept": [], "freed": 0}
    assert path.exists() and state.exists()


def _bare_cfg(tmp_cfg, tmp_path, monkeypatch):
    home = tmp_path / "agent"
    monkeypatch.setenv("HOME", str(home))
    return replace(
        tmp_cfg, agent_home=home, worktree_root=home / "worktrees", agent_state_root=home / "state"
    )


def test_sweep_removes_dead_pytest_runs_and_keeps_live_ones(tmp_cfg, tmp_path, monkeypatch, pads):
    cfg = _bare_cfg(tmp_cfg, tmp_path, monkeypatch)
    root = agentrun._pytest_root()
    live, dead, fresh = root / "pytest-1", root / "pytest-2", root / "pytest-3"
    for d in (live, dead, fresh):
        d.mkdir(parents=True)
        (d / "copy").write_bytes(b"x" * 100_000)
    gone = subprocess.Popen(["true"])
    gone.wait()
    (live / ".lock").write_text(str(os.getpid()))
    (dead / ".lock").write_text(str(gone.pid))
    (root / "pytest-current").symlink_to(dead)
    for d in (live, dead):
        _age(d, NOW, 2)
    _age(fresh, NOW, 0.5)
    out = _sweep(cfg, [], NOW)
    assert out["removed"] == [str(dead)] and not dead.exists() and out["freed"] >= 100_000
    assert live.exists() and fresh.exists() and (root / "pytest-current").is_symlink()
    now = _sweep(cfg, [], NOW, scratch_age_s=600, pytest_age_s=0)
    assert now["removed"] == [str(fresh)] and live.exists()


def test_sweep_now_leaves_a_worktree_younger_than_7_days(
    tmp_cfg, origin, tmp_path, monkeypatch, pads
):
    cfg = agent_cfg(tmp_cfg, origin, tmp_path)
    monkeypatch.setenv("HOME", str(cfg.agent_home))
    path, _, _ = Agent(cfg).prepare("backend", "NF-7")
    _age(path, NOW, 1)
    out = _sweep(cfg, [], NOW, scratch_age_s=600, pytest_age_s=0)
    assert out["kept"] == [{"path": str(path), "why": "changed in the last 7 days"}]
    assert path.exists()


def test_sweep_now_takes_unlisted_scratchpads_idle_ten_minutes(
    tmp_cfg, tmp_path, monkeypatch, pads
):
    cfg = _bare_cfg(tmp_cfg, tmp_path, monkeypatch)
    idle, listed = pads / "p" / "s1" / "scratchpad", pads / "p" / "s2" / "scratchpad"
    for pad in (idle, listed):
        pad.mkdir(parents=True)
        os.utime(pad, (NOW - 900, NOW - 900))
    kept = cfg.agent_state_root / "NF-9"
    kept.mkdir(parents=True)
    (kept / "scratchpads").write_text(f"{listed}\n")
    out = _sweep(cfg, ["NF-9"], NOW, scratch_age_s=600, pytest_age_s=0)
    assert out["removed"] == [str(idle)] and listed.exists()


def test_sweep_now_spares_the_tasks_of_a_listed_session_and_a_busy_pad(
    tmp_cfg, tmp_path, monkeypatch, pads
):
    cfg = _bare_cfg(tmp_cfg, tmp_path, monkeypatch)
    listed = pads / "p" / "s1" / "scratchpad"
    tasks = pads / "p" / "s1" / "tasks"
    busy = pads / "p" / "s2" / "scratchpad"
    for d in (listed, tasks, busy):
        d.mkdir(parents=True)
    (tasks / "out.output").write_text("x")
    (busy / "f").write_text("x")
    for d in (listed, tasks, busy, tasks / "out.output"):
        os.utime(d, (NOW - 900, NOW - 900))
    os.utime(busy / "f", (NOW - 5, NOW - 5))
    kept = cfg.agent_state_root / "NF-9"
    kept.mkdir(parents=True)
    (kept / "scratchpads").write_text(f"{listed}\n")
    out = _sweep(cfg, ["NF-9"], NOW, scratch_age_s=600, pytest_age_s=0)
    assert out["removed"] == []
    assert tasks.exists() and busy.exists()


def test_sweep_keeps_an_orphan_with_unpushed_commits(tmp_cfg, origin, tmp_path, monkeypatch, pads):
    cfg = agent_cfg(tmp_cfg, origin, tmp_path)
    monkeypatch.setenv("HOME", str(cfg.agent_home))
    path, _, _ = Agent(cfg).prepare("backend", "NF-7")
    for k, v in (("user.name", "t"), ("user.email", "t@t")):
        git("config", k, v, cwd=path)
    (path / "b.txt").write_text("b\n")
    git("add", "b.txt", cwd=path)
    git("commit", "-qm", "only here", "--no-verify", cwd=path)
    _age(path, NOW, 8)
    out = _sweep(cfg, [], NOW)
    assert out["kept"] == [{"path": str(path), "why": "commits not on GitHub"}] and path.exists()


def test_sweep_keeps_a_dirty_or_recent_orphan(tmp_cfg, origin, tmp_path, monkeypatch, pads):
    cfg = agent_cfg(tmp_cfg, origin, tmp_path)
    monkeypatch.setenv("HOME", str(cfg.agent_home))
    dirty, _, _ = Agent(cfg).prepare("backend", "NF-7")
    recent, _, _ = Agent(cfg).prepare("backend", "NF-8")
    (dirty / "work.py").write_text("x\n")
    _age(dirty, NOW, 8)
    _age(recent, NOW, 1)
    whys = {k["path"]: k["why"] for k in _sweep(cfg, [], NOW)["kept"]}
    assert whys == {str(dirty): "uncommitted changes", str(recent): "changed in the last 7 days"}


def test_a_recent_commit_counts_as_a_change(tmp_cfg, origin, tmp_path, monkeypatch, pads):
    cfg = agent_cfg(tmp_cfg, origin, tmp_path)
    monkeypatch.setenv("HOME", str(cfg.agent_home))
    path, _, _ = Agent(cfg).prepare("backend", "NF-7")
    now = os.path.getmtime(path) + 8 * 86400
    _age(path, now, 8)
    commit_time = git("log", "-1", "--format=%ct", cwd=path)
    assert agentrun._last_change(path) >= float(commit_time)


def test_sweep_removes_old_orphan_state_and_unlisted_old_scratchpads(
    tmp_cfg, tmp_path, monkeypatch, pads
):
    home = tmp_path / "agent"
    cfg = replace(
        tmp_cfg, agent_home=home, worktree_root=home / "worktrees", agent_state_root=home / "state"
    )
    monkeypatch.setenv("HOME", str(home))
    old_state = cfg.agent_state_root / "NF-3"
    old_state.mkdir(parents=True)
    _age(old_state, NOW, 8)
    orphan, listed = pads / "p" / "s1" / "scratchpad", pads / "p" / "s2" / "scratchpad"
    orphan.mkdir(parents=True)
    listed.mkdir(parents=True)
    kept_state = cfg.agent_state_root / "NF-9"
    kept_state.mkdir(parents=True)
    (kept_state / "scratchpads").write_text(f"{listed}\n")
    for pad in (orphan, listed):
        _age(pad, NOW, 2)
    out = _sweep(cfg, ["NF-9"], NOW)
    assert str(old_state) in out["removed"] and str(orphan) in out["removed"]
    assert not old_state.exists() and not orphan.exists() and listed.exists()


def test_sweep_stops_running_containers_only_with_no_session_up(monkeypatch, tmp_path):
    ran = []
    monkeypatch.setattr("flocc.agentrun.container_env", lambda: {"DOCKER_HOST": "unix:///s"})
    monkeypatch.setattr("flocc.agentrun.shutil.which", lambda name: "/usr/bin/podman")
    monkeypatch.setattr("flocc.agentrun.subprocess.run", lambda argv, **kw: ran.append(argv))
    monkeypatch.setattr("flocc.agentrun._sessions_up", lambda: True)
    agentrun._leftover_containers()
    assert ran == [["podman", "container", "prune", "-f"]]
    ran.clear()
    monkeypatch.setattr("flocc.agentrun._sessions_up", lambda: False)
    agentrun._leftover_containers()
    assert ran == [
        ["podman", "stop", "--all", "--time", "10"],
        ["podman", "container", "prune", "-f"],
    ]


def _agent_home(tmp_path, monkeypatch):
    home = tmp_path / "agent"
    (home / "worktrees").mkdir(parents=True)
    monkeypatch.setenv("HOME", str(home))
    tmp = tmp_path / "tmp"
    tmp.mkdir()
    monkeypatch.setattr("flocc.agentrun._tmp_root", lambda: tmp)
    return home, tmp


def test_usage_names_the_issue_behind_a_pytest_run_and_its_memory(tmp_path, monkeypatch):
    home, tmp = _agent_home(tmp_path, monkeypatch)
    monkeypatch.setattr("flocc.agentrun.container_env", dict)
    wt = home / "worktrees" / "NF-5"
    wt.mkdir()
    run = agentrun._pytest_root() / "pytest-7"
    run.mkdir(parents=True)
    (run / "copy").write_bytes(b"x" * 300_000)
    (tmp / "small").write_bytes(b"x" * 10)
    proc = subprocess.Popen(["sleep", "30"], cwd=wt)
    try:
        (run / ".lock").write_text(str(proc.pid))
        out = agentrun.usage({"worktree_root": str(home / "worktrees")})
    finally:
        proc.kill()
        proc.wait()
    first = out["largest"][0]
    assert first["path"] == str(run) and first["issue"] == "NF-5" and first["bytes"] >= 300_000
    assert {e["path"] for e in out["largest"]} == {str(run), str(tmp / "small")}
    assert out["issues"]["NF-5"] > 0 and out["containers"] == []


def test_usage_lists_containers_with_their_memory_and_issue(tmp_path, monkeypatch):
    home, _ = _agent_home(tmp_path, monkeypatch)
    root = (home / "worktrees").resolve()
    monkeypatch.setattr("flocc.agentrun.container_env", lambda: {"DOCKER_HOST": "unix:///s"})
    monkeypatch.setattr("flocc.agentrun.shutil.which", lambda name: "/usr/bin/podman")
    label = "com.docker.compose.project.working_dir"
    ps = [
        {
            "Id": "abc123def456" + "0" * 52,
            "Names": ["nf-db-1"],
            "Labels": {label: str(root / "NF-5")},
        },
        {"Id": "f" * 64, "Names": ["other"], "Labels": {}},
    ]

    def run(argv, **kw):
        if argv[:2] == ["podman", "ps"]:
            return subprocess.CompletedProcess(argv, 0, json.dumps(ps), "")
        return subprocess.CompletedProcess(argv, 0, "abc123def456\t120MB / 5.8GB\n", "")

    monkeypatch.setattr("flocc.agentrun.subprocess.run", run)
    out = agentrun.usage({"worktree_root": str(home / "worktrees")})
    assert out["containers"] == [
        {"name": "nf-db-1", "memory": "120MB / 5.8GB", "issue": "NF-5"},
        {"name": "other", "memory": "not running", "issue": None},
    ]


FAKE_CLAUDE = """
import signal, subprocess, time
signal.signal(signal.SIGCHLD, signal.SIG_IGN)
bash = subprocess.Popen(
    ["/bin/sh", "-c", "sleep 300; : /home/u/.claude/shell-snapshots/snapshot-bash-1.sh"]
)
mcp = subprocess.Popen(["sleep", "301"])
print(bash.pid, mcp.pid, flush=True)
time.sleep(300)
"""


def _child_of(pid: int) -> int:
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        kids = [p for p, (ppid, _) in agentrun._procs().items() if ppid == pid]
        if kids:
            return kids[0]
        time.sleep(0.05)
    raise AssertionError(f"{pid} started no child")


def test_stop_tests_kills_the_bash_tool_tree_and_leaves_claude_and_mcp(tmp_path, monkeypatch):
    claude = tmp_path / "claude"
    claude.symlink_to(sys.executable)
    fake = subprocess.Popen([str(claude), "-c", FAKE_CLAUDE], stdout=subprocess.PIPE, text=True)
    mcp = 0
    try:
        assert fake.stdout is not None
        bash, mcp = map(int, fake.stdout.readline().split())
        sleeper = _child_of(bash)
        monkeypatch.setattr("flocc.agentrun.Sessions.pane_pid", lambda self, name: fake.pid)
        monkeypatch.setattr("flocc.agentrun.container_env", dict)
        monkeypatch.setenv("HOME", str(tmp_path))
        out = agentrun.stop_tests({"name": "NF-1", "worktree": str(tmp_path / "wt" / "NF-1")})
        assert out["claude"] == fake.pid and sorted(out["killed"]) == sorted([bash, sleeper])
        assert not agentrun._running(bash) and not agentrun._running(sleeper)
        assert agentrun._running(fake.pid) and agentrun._running(mcp)
    finally:
        fake.kill()
        fake.wait()
        if mcp:
            os.kill(mcp, signal.SIGKILL)


def test_stop_tests_removes_only_the_issue_s_containers(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    wt = tmp_path / "wt" / "NF-5"
    wt.mkdir(parents=True)
    monkeypatch.setattr("flocc.agentrun.Sessions.pane_pid", lambda self, name: None)
    monkeypatch.setattr("flocc.agentrun.container_env", lambda: {"DOCKER_HOST": "unix:///s"})
    monkeypatch.setattr("flocc.agentrun.shutil.which", lambda name: "/usr/bin/podman")
    label = "com.docker.compose.project.working_dir"
    ps = [
        {"Id": "a" * 64, "Names": ["nf5-db"], "Labels": {label: str(wt.resolve())}},
        {"Id": "b" * 64, "Names": ["nf6-db"], "Labels": {label: str(wt.resolve().parent / "NF-6")}},
    ]
    ran = []

    def run(argv, **kw):
        ran.append(argv)
        listing = json.dumps(ps) if argv[:2] == ["podman", "ps"] else ""
        return subprocess.CompletedProcess(argv, 0, listing, "")

    monkeypatch.setattr("flocc.agentrun.subprocess.run", run)
    out = agentrun.stop_tests({"name": "NF-5", "worktree": str(wt)})
    assert out == {"claude": None, "killed": [], "containers": ["nf5-db"], "testcontainers": []}
    assert ["podman", "rm", "-f", "a" * 64] in ran
    assert not any("b" * 64 in arg for argv in ran for arg in argv)


def test_stop_tests_reports_testcontainers_it_cannot_attribute(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    wt = tmp_path / "wt" / "NF-5"
    wt.mkdir(parents=True)
    monkeypatch.setattr("flocc.agentrun.Sessions.pane_pid", lambda self, name: None)
    monkeypatch.setattr("flocc.agentrun.container_env", lambda: {"DOCKER_HOST": "unix:///s"})
    monkeypatch.setattr("flocc.agentrun.shutil.which", lambda name: "/usr/bin/podman")
    ps = [{"Id": "c" * 64, "Names": ["pg"], "Labels": {"org.testcontainers": "true"}}]
    ran = []

    def run(argv, **kw):
        ran.append(argv)
        return subprocess.CompletedProcess(argv, 0, json.dumps(ps), "")

    monkeypatch.setattr("flocc.agentrun.subprocess.run", run)
    out = agentrun.stop_tests({"name": "NF-5", "worktree": str(wt)})
    assert out["containers"] == [] and out["testcontainers"] == ["pg"]
    assert not any(argv[:2] == ["podman", "rm"] for argv in ran)


def test_kill_trees_interrupts_before_terminating(tmp_path):
    marker = tmp_path / "teardown"
    code = (
        "import signal, sys, time\n"
        f"signal.signal(signal.SIGINT, lambda *a: (open({str(marker)!r}, 'w').close(), sys.exit(0)))\n"
        "print('ready', flush=True)\n"
        "time.sleep(60)\n"
    )
    proc = subprocess.Popen([sys.executable, "-c", code], stdout=subprocess.PIPE, text=True)
    try:
        assert proc.stdout is not None and proc.stdout.readline().strip() == "ready"
        agentrun._kill_trees([proc.pid], agentrun._procs(), 5.0)
        proc.wait(timeout=5)
        assert marker.exists()
    finally:
        proc.kill()


def test_shell_runs_bash_in_the_agent_home_and_caps_the_output(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    out = agentrun.shell({"command": "pwd; echo err >&2; exit 3"})
    assert out == {"code": 3, "output": f"{tmp_path}\nerr\n"}
    big = agentrun.shell({"command": "head -c 30000 /dev/zero | tr '\\0' x"})
    assert big["code"] == 0 and big["output"] == "[...]\n" + "x" * agentrun.SHELL_CHARS


def test_shell_kills_what_a_command_started_at_the_timeout(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setattr("flocc.agentrun.SHELL_TIMEOUT_S", 0.5)
    start = time.monotonic()
    out = agentrun.shell({"command": "sleep 30 & sleep 30; echo never"})
    assert out["code"] is None and "never" not in out["output"]
    assert time.monotonic() - start < 10


def test_shell_refuses_an_empty_command(monkeypatch, capsys):
    monkeypatch.setattr("sys.stdin", io.StringIO('{"command": "  "}'))
    assert agentrun.main(["shell"]) == 2 and "bad request" in capsys.readouterr().err
