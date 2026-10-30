import shutil
import subprocess
import sys
import time
from dataclasses import replace

import pytest

from flocc.config import load
from flocc.worktree import git
from tests.paths import EXAMPLE


@pytest.fixture(autouse=True)
def no_token_check(monkeypatch):
    # The tick's token and login checks call api.github.com and claude; test_tokens.py covers
    # them with fakes.
    monkeypatch.setattr("flocc.tick.check_tokens", lambda rt: None)
    monkeypatch.setattr("flocc.tick.check_claude_login", lambda rt: None)


@pytest.fixture
def tmp_cfg(tmp_path):
    for name in ("state", "wt"):
        (tmp_path / name).mkdir()
    token = tmp_path / "gh-agent.token"
    token.write_text("t\n")
    return replace(
        load(EXAMPLE),
        state_root=tmp_path / "state",
        worktree_root=tmp_path / "wt",
        agent_home=tmp_path,
        agent_state_root=tmp_path / "state",
        ledger_path=tmp_path / "l.sqlite",
        agent_gh_token_file=token,
        agent_run=(sys.executable, "-m", "flocc.agentrun"),
        agent_plugins_file=tmp_path / "agent-plugins.txt",
    )


@pytest.fixture
def origin(tmp_path):
    bare = tmp_path / "origin.git"
    subprocess.run(["git", "init", "-q", "--bare", "-b", "main", bare], check=True)
    src = tmp_path / "src"
    subprocess.run(["git", "clone", "-q", bare, src], check=True, capture_output=True)
    for k, v in (("user.name", "t"), ("user.email", "t@t")):
        git("config", k, v, cwd=src)
    (src / "a.txt").write_text("a\n")
    (src / ".gitignore").write_text(".setup-ran\n")
    (src / "setup").write_text("#!/bin/sh\ntouch .setup-ran\n")
    (src / "setup").chmod(0o755)
    git("add", ".", cwd=src)
    git("commit", "-qm", "init", cwd=src)
    git("push", "-q", "origin", "main", cwd=src)
    return src


@pytest.fixture
def tmux_server():
    if not shutil.which("tmux"):
        pytest.skip("tmux not installed")
    socket = f"flocc-test-{time.time_ns()}"
    proc = subprocess.Popen(
        ["tmux", "-L", socket, "-D"],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    for _ in range(50):
        probe = ["tmux", "-N", "-L", socket, "start-server"]
        if subprocess.run(probe, capture_output=True, check=False).returncode == 0:
            break
        time.sleep(0.1)
    yield socket
    subprocess.run(["tmux", "-L", socket, "kill-server"], capture_output=True, check=False)
    proc.wait(5)
