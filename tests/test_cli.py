from linear_agents import ledger
from linear_agents.cli import main
from linear_agents.config import load
from linear_agents.linear import LinearError
from linear_agents.runtime import Runtime
from tests.fakes import FakeNotifier, FakeSessions
from tests.paths import EXAMPLE
from tests.test_ledger import make


def test_unknown_subcommand_exits_nonzero(capsys):
    assert main(["nope"]) == 2
    assert "invalid choice" in capsys.readouterr().err


class GoneLinear:
    def issue(self, key):
        raise LinearError("Entity not found: Issue")

    def unstart(self, issue):
        raise AssertionError("no issue to move")


def test_release_clears_a_claim_whose_issue_and_worktree_are_gone(
    origin, tmp_path, monkeypatch, capsys
):
    path = tmp_path / "c.toml"
    path.write_text(
        EXAMPLE.read_text()
        .replace('"~/.local/state/linear-agents/ledger.sqlite"', f'"{tmp_path / "l.sqlite"}"')
        .replace('"~/src/backend"', f'"{origin}"')
    )
    cfg = load(path)
    con = ledger.connect(cfg.ledger_path)
    ledger.insert_claim(con, make("NF-1", worktree=str(tmp_path / "wt" / "NF-1")))
    sessions = FakeSessions()
    rt = Runtime(
        cfg=cfg,
        con=con,
        linear=GoneLinear(),
        sessions=sessions,
        notifier=FakeNotifier(),
        now=lambda: 0.0,
        exe="/opt/linear-agents",
        claude_exe="/opt/claude",
        config_path=path,
        publish_fn=lambda *a: None,
        git_head=lambda p: "sha0",
        review_diff=lambda c: "",
        comment_pr=lambda c, u, b: None,
    )
    monkeypatch.setattr("linear_agents.tick.build_runtime", lambda cfg, path: rt)
    assert main(["--config", str(path), "release", "NF-1"]) == 0
    assert ledger.get_claim(con, "NF-1") is None and sessions.killed == ["NF-1-test"]
    err = capsys.readouterr().err
    assert "worktree" in err and "Entity not found" in err


def test_commit_in_a_review_worktree_is_rejected(origin, tmp_path, monkeypatch, capsys):
    con_path = tmp_path / "hook.sqlite"
    claim = make("NF-1", (), worktree=str(origin.resolve()))
    claim.phase = "review"
    ledger.insert_claim(ledger.connect(con_path), claim)
    monkeypatch.setenv("LINEAR_AGENTS_LEDGER", str(con_path))
    monkeypatch.chdir(origin)
    assert main(["hook", "pre-commit"]) == 1
    assert "read-only" in capsys.readouterr().err
    ledger.update_claim(ledger.connect(con_path), "NF-1", phase="impl")
    assert main(["hook", "pre-commit"]) == 0


def test_review_done_rejects_a_missing_or_empty_review(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("LINEAR_AGENTS_STATE", str(tmp_path))
    assert main(["review-done"]) == 1
    (tmp_path / "review.md").write_text(" \n")
    assert main(["review-done"]) == 1
    assert not (tmp_path / "done.json").exists()
    (tmp_path / "review.md").write_text("Verdict: approve\n")
    assert main(["review-done"]) == 0 and (tmp_path / "done.json").exists()
