import shutil
import time
from dataclasses import replace
from pathlib import Path

from flocc import ledger
from flocc.agentrun import apply_handoff
from flocc.config import RepoConfig
from flocc.context import state_dir
from flocc.publisher import PublishResult
from flocc.runtime import Runtime
from flocc.sessions import Sessions
from flocc.supervisor import launch, supervise
from flocc.worktree import git, prepare_worktree
from tests.fakes import FakeLinear, FakeNotifier
from tests.test_ledger import make
from tests.test_supervisor import current

FAKE = Path(__file__).parent / "bin" / "claude"


class HandoffSessions:
    """Sessions with flocc-agent's start request applied, minus sudo."""

    def __init__(self, socket):
        self.tmux = Sessions(socket=socket)

    def start(self, name, cwd, argv, *, state, files=None, clear=(), trust=(), plugins=None):
        apply_handoff(Path(state), dict(files or {}), clear)
        self.tmux.start(name, cwd, argv)

    def alive(self, name):
        return self.tmux.alive(name)

    def kill(self, name):
        self.tmux.kill(name)


def wait_for(check, timeout=10.0):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        if check():
            return
        time.sleep(0.1)
    raise AssertionError("timed out")


def test_fake_claude_through_tmux(tmp_cfg, tmux_server, origin, tmp_path):
    path, branch, base = prepare_worktree(RepoConfig(origin), tmp_cfg.worktree_root, "NF-1")
    c = make("NF-1", worktree=str(path))
    c.branch, c.base_sha = branch, base
    exe = shutil.which("flocc")
    assert exe is not None, "run under uv run so the venv's flocc is on PATH"
    # The pane's HOME is the agent's, so it needs an identity of its own to commit.
    (tmp_path / ".gitconfig").write_text("[user]\n\tname = t\n\temail = t@t\n")
    tmp_cfg.agent_token_file.parent.mkdir(parents=True)
    tmp_cfg.agent_token_file.write_text("t\n")
    sessions = HandoffSessions(socket=tmux_server)
    rt = Runtime(
        cfg=replace(tmp_cfg, agent_flocc=Path(exe)),
        con=ledger.connect(tmp_cfg.ledger_path),
        linear=FakeLinear(),
        sessions=sessions,
        notifier=FakeNotifier(),
        now=time.time,
        exe=exe,
        claude_exe=str(FAKE),
        config_path=tmp_path / "c.toml",
        publish_fn=lambda *a: PublishResult("opened", url="u"),
        git_head=lambda claim: git("rev-parse", "HEAD", cwd=Path(claim.worktree)),
        review_diff=lambda c: "",
        comment_pr=lambda c, u, b: None,
    )
    ledger.insert_claim(rt.con, c)
    state = state_dir(tmp_cfg, "NF-1")
    state.mkdir()
    (state / "fake-mode").write_text("crash")
    launch(rt, c, "test")
    wait_for(lambda: not sessions.alive("NF-1-test"))
    (state / "fake-mode").write_text("done")
    supervise(rt, current(rt))  # dead: the one restart
    assert current(rt).restarts == 1
    wait_for(lambda: (state / "done.json").exists())
    supervise(rt, current(rt))
    c = current(rt)
    assert c.phase == "impl" and c.tester_sha != base and sessions.alive("NF-1")
