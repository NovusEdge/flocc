import json
from dataclasses import replace

from linear_agents import ledger
from linear_agents.cli import main
from linear_agents.context import phase_prompt, state_dir, write_context
from linear_agents.linear import Issue
from tests.test_ledger import make

ISSUE = Issue(
    "u1", "NF-1", "Fix rounding", "Round half-even.", "https://l/NF-1", "started", (), None, "t1"
)


def test_context_lists_issue_and_commands(tmp_cfg):
    write_context(tmp_cfg, ISSUE, make("NF-1"))
    text = (state_dir(tmp_cfg, "NF-1") / "context.md").read_text()
    for needle in (
        "Fix rounding",
        "Round half-even.",
        "linear-agents done",
        "linear-agents escalate",
        "linear-agents claim",
        "linear-agents context",
        "SendMessage",
        "coordinate skill",
    ):
        assert needle in text


def test_context_cli_prints_context_live_peers_and_handoff(tmp_cfg, monkeypatch, capsys):
    con = ledger.connect(tmp_cfg.ledger_path)
    for claim in (make("NF-1"), make("NF-2", ("x/**",)), replace(make("NF-3"), repo="web")):
        ledger.insert_claim(con, claim)
    state = state_dir(tmp_cfg, "NF-1")
    state.mkdir()
    (state / "context.md").write_text("CTX")
    (state / "handoff.md").write_text("NOTE")
    monkeypatch.setenv("LINEAR_AGENTS_STATE", str(state))
    monkeypatch.setenv("LINEAR_AGENTS_LEDGER", str(tmp_cfg.ledger_path))
    assert main(["context"]) == 0
    out = capsys.readouterr().out
    assert "CTX" in out and "NOTE" in out and "NF-2-test" in out and "NF-3" not in out


def test_done_and_handoff_ready(tmp_path, monkeypatch):
    monkeypatch.setenv("LINEAR_AGENTS_STATE", str(tmp_path))
    assert main(["done", "--summary", "tests written"]) == 0
    assert json.loads((tmp_path / "done.json").read_text())["summary"] == "tests written"
    assert main(["handoff-ready"]) == 1
    (tmp_path / "handoff.md").write_text("note")
    assert main(["handoff-ready"]) == 0 and (tmp_path / "handoff-ready").exists()


def test_phase_prompts_name_the_skill():
    assert "tester skill" in phase_prompt("test", "NF-1")
    assert "handoff.md" in phase_prompt("test", "NF-1")
    assert "implementer skill" in phase_prompt("impl", "NF-1")
    fix = phase_prompt("fix", "NF-1", extra="E1 boom")
    assert "check failed" in fix and fix.endswith("E1 boom")
