import shutil
import time
from pathlib import Path

from linear_agents import ledger
from linear_agents.config import RepoConfig
from linear_agents.context import state_dir
from linear_agents.publisher import PublishResult
from linear_agents.runtime import Runtime
from linear_agents.sessions import Sessions
from linear_agents.supervisor import launch, supervise
from linear_agents.worktree import git, prepare_worktree
from tests.fakes import FakeLinear, FakeNotifier
from tests.test_ledger import make
from tests.test_supervisor import current

FAKE = Path(__file__).parent / "bin" / "claude"


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
    exe = shutil.which("linear-agents")
    assert exe is not None, "run under uv run so the venv's linear-agents is on PATH"
    sessions = Sessions(socket=tmux_server)
    rt = Runtime(
        cfg=tmp_cfg,
        con=ledger.connect(tmp_cfg.ledger_path),
        linear=FakeLinear(),
        sessions=sessions,
        notifier=FakeNotifier(),
        now=time.time,
        exe=exe,
        claude_exe=str(FAKE),
        config_path=tmp_path / "c.toml",
        publish_fn=lambda *a: PublishResult("opened", url="u"),
        git_head=lambda p: git("rev-parse", "HEAD", cwd=Path(p)),
    )
    ledger.insert_claim(rt.con, c)
    state = state_dir(tmp_cfg, "NF-1")
    state.mkdir()
    (state / "fake-mode").write_text("handoff")
    launch(rt, c, "test")
    wait_for(lambda: (state / "handoff-ready").exists())
    (state / "fake-mode").write_text("crash")
    supervise(rt, current(rt))  # handoff: relaunched, and this one crashes
    assert not (state / "handoff-ready").exists()
    wait_for(lambda: not sessions.alive("NF-1-test"))
    (state / "fake-mode").write_text("done")
    supervise(rt, current(rt))  # dead: the one restart
    assert current(rt).restarts == 1
    wait_for(lambda: (state / "done.json").exists())
    supervise(rt, current(rt))
    c = current(rt)
    assert c.phase == "impl" and c.tester_sha != base and sessions.alive("NF-1")
