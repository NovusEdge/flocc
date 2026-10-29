import json
from dataclasses import replace

from flocc import ledger
from flocc.cli import main
from flocc.context import phase_prompt, state_dir, write_context
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


def test_context_cli_prints_context_and_handoff(tmp_cfg, monkeypatch, capsys):
    con = ledger.connect(tmp_cfg.ledger_path)
    for claim in (make("NF-1"), make("NF-2"), replace(make("NF-3"), repo="web")):
        ledger.insert_claim(con, claim)
    state = state_dir(tmp_cfg, "NF-1")
    state.mkdir()
    (state / "context.md").write_text("CTX")
    (state / "handoff.md").write_text("NOTE")
    monkeypatch.setenv("FLOCC_STATE", str(state))
    monkeypatch.setenv("FLOCC_LEDGER", str(tmp_cfg.ledger_path))
    assert main(["context"]) == 0
    out = capsys.readouterr().out
    assert "CTX" in out and "NOTE" in out and "NF-2" not in out


def test_done_writes_the_summary(tmp_path, monkeypatch):
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path))
    assert main(["done", "--summary", "tests written"]) == 0
    assert json.loads((tmp_path / "done.json").read_text())["summary"] == "tests written"


def test_phase_prompts_name_the_skill():
    assert "tester skill" in phase_prompt("test", "NF-1")
    assert "handoff.md" in phase_prompt("test", "NF-1")
    assert "implementer skill" in phase_prompt("impl", "NF-1")
    impl = phase_prompt("impl", "NF-1")
    assert "without changing them" not in impl
    assert "flocc tests-amended" in impl and "never delete or skip" in impl
    fix = phase_prompt("fix", "NF-1", extra="E1 boom")
    assert "check failed" in fix and fix.endswith("E1 boom")
