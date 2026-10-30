import json
from dataclasses import replace

from flocc import ledger
from flocc.cli import main
from flocc.context import agent_dir, phase_prompt, state_dir, write_context
from flocc.linear import Issue
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
        "flocc done",
        "flocc context",
    ):
        assert needle in text


def test_context_names_the_agents_state_dir_not_the_services(tmp_cfg, tmp_path):
    tmp_cfg = replace(tmp_cfg, agent_state_root=tmp_path / "agent-state")
    write_context(tmp_cfg, ISSUE, make("NF-1"))
    text = (state_dir(tmp_cfg, "NF-1") / "context.md").read_text()
    assert f"state directory is {agent_dir(tmp_cfg, 'NF-1')}." in text
    assert str(state_dir(tmp_cfg, "NF-1")) not in text


def test_context_cli_prints_context_and_the_last_summary(tmp_cfg, monkeypatch, capsys):
    con = ledger.connect(tmp_cfg.ledger_path)
    for claim in (make("NF-1"), make("NF-2"), replace(make("NF-3"), repo="web")):
        ledger.insert_claim(con, claim)
    state = state_dir(tmp_cfg, "NF-1")
    state.mkdir()
    (state / "context.md").write_text("CTX")
    (state / "last-summary.md").write_text("NOTE")
    monkeypatch.setenv("FLOCC_STATE", str(state))
    monkeypatch.setenv("FLOCC_LEDGER", str(tmp_cfg.ledger_path))
    assert main(["context"]) == 0
    out = capsys.readouterr().out
    assert "CTX" in out and "## The previous step's summary" in out and "NOTE" in out
    assert "NF-2" not in out


def test_done_writes_the_summary(tmp_path, monkeypatch):
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path))
    assert main(["done", "--summary", "tests written"]) == 0
    assert json.loads((tmp_path / "done.json").read_text())["summary"] == "tests written"


def test_phase_prompts_name_the_archetype_skill():
    for phase, skill in (("test", "tester"), ("impl", "executor"), ("fix", "fixer")):
        prompt = phase_prompt(phase, "NF-1")
        assert f"flocc {skill} skill" in prompt and "flocc done" in prompt
    review = phase_prompt("review", "NF-1")
    assert "flocc reviewer skill" in review and "review.md" in review
    assert phase_prompt("fix", "NF-1", extra="E1 boom").endswith("E1 boom")
