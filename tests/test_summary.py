import json
import subprocess

import pytest

from flocc import events, host, ledger
from flocc.context import state_dir
from flocc.summary import DIGEST_LINES, LIST_ROWS, RUN_SUMMARY_CHARS, digest_data, run_summary
from flocc.supervisor import BLOCKED, CHECK_OUTPUT, LAST_SUMMARY
from tests.test_ledger import make


@pytest.fixture
def con(tmp_cfg):
    return ledger.connect(tmp_cfg.ledger_path)


def no_log(key, n):
    return []


def test_run_summary_has_the_step_the_block_and_the_check(tmp_cfg, con):
    c = make("NF-1", state="blocked")
    c.phase, c.fix_attempts, c.sequence = "fix", 1, ["executor", "reviewer"]
    ledger.insert_claim(con, c)
    d = state_dir(tmp_cfg, "NF-1")
    d.mkdir(parents=True)
    (d / LAST_SUMMARY).write_text("Fixed the parser.")
    (d / BLOCKED).write_text("check-failed\n\nThe check still fails")
    (d / CHECK_OUTPUT).write_text("ok\nFAILED tests/test_a.py::test_x - assert 1 == 2\n")
    (d / "pr-url").write_text("https://pr/1\n")
    text = run_summary(tmp_cfg, con, "NF-1", 3600.0, lambda k, n: [(1, f"{k}: blocked")])
    assert "fixer, blocked; sequence executor, reviewer; fix rounds 1/2" in text
    assert "PR: https://pr/1" in text and "Fixed the parser." in text
    assert "The check still fails" in text and "test_x" in text and "NF-1: blocked" in text


def test_run_summary_is_trimmed_to_a_fixed_size(tmp_cfg, con):
    ledger.insert_claim(con, make("NF-1", state="blocked"))
    d = state_dir(tmp_cfg, "NF-1")
    d.mkdir(parents=True)
    for name in (LAST_SUMMARY, BLOCKED, CHECK_OUTPUT):
        (d / name).write_text("error " * 50_000)
    assert len(run_summary(tmp_cfg, con, "NF-1", 0.0, no_log)) <= RUN_SUMMARY_CHARS


def test_run_summary_without_a_claim_says_so(tmp_cfg, con):
    assert "No claim" in run_summary(tmp_cfg, con, "NF-404", 0.0, no_log)


def test_digest_data_lists_runs_lines_holds_events_and_attention(tmp_cfg, con):
    ledger.insert_claim(con, make("NF-1", state="done"))
    ledger.add_digest(con, "NF-1", "review skipped", 10.0)
    ledger.add_digest(con, None, "GitHub agent token expires in 9 days", 11.0)
    ledger.add_hold(con, "NF-7", "owner said wait", 12.0)
    stuck = events.emit(con, "ended", 50_000.0, issue_key="NF-1", detail="blocked: idle")
    for _ in range(1 + events.REDELIVERIES):
        events.mark_printed(con, [stuck], 50_001.0)
    text, through = digest_data(tmp_cfg, con, 86_400.0 + 100.0)
    assert "- NF-1: tester, done" in text
    assert "- NF-1: review skipped" in text and "- service: GitHub agent token" in text
    assert "- NF-7: owner said wait" in text and "ended: 1, 1 not acknowledged" in text
    assert f"- event {stuck}, ended, NF-1: blocked: idle" in text
    assert text.rstrip().endswith(f"flocc digest-data --done {through}")


def test_digest_data_asks_github_and_marks_a_run_whose_pr_merged(tmp_cfg, con):
    from flocc.publisher import pr_states

    states = {"https://pr/1": "MERGED", "https://pr/2": "OPEN", "https://pr/3": "CLOSED"}

    def gh(argv, **kw):
        out = json.dumps({"state": states[argv[3]]})
        return subprocess.CompletedProcess(argv, 0, out, "")

    for key, n in (("NF-1", 1), ("NF-2", 2), ("NF-3", 3)):
        ledger.insert_claim(con, make(key, state="done"))
        d = state_dir(tmp_cfg, key)
        d.mkdir(parents=True)
        (d / "pr-url").write_text(f"https://pr/{n}\n")
    text, _ = digest_data(
        tmp_cfg, con, 86_400.0, pr_states_fn=lambda urls: pr_states(urls, "tok", runner=gh)
    )
    assert "- NF-1: tester, merged, PR https://pr/1" in text
    assert "- NF-2: tester, done, PR https://pr/2" in text
    assert "- NF-3: tester, closed, PR https://pr/3" in text


def test_digest_data_keeps_a_run_listed_when_github_is_down(tmp_cfg, con):
    ledger.insert_claim(con, make("NF-1", state="done"))
    d = state_dir(tmp_cfg, "NF-1")
    d.mkdir(parents=True)
    (d / "pr-url").write_text("https://pr/1\n")

    def down(urls):
        raise subprocess.CalledProcessError(1, "gh")

    text, _ = digest_data(tmp_cfg, con, 86_400.0, pr_states_fn=down)
    assert "- NF-1: tester, done, PR https://pr/1" in text


def test_digest_data_names_why_a_run_stopped(tmp_cfg, con):
    ledger.insert_claim(con, make("NF-1", state="blocked"))
    ledger.insert_claim(con, make("NF-2", state="timed_out"))
    d = state_dir(tmp_cfg, "NF-1")
    d.mkdir(parents=True)
    (d / BLOCKED).write_text("check-cap\n\nThe check still fails")
    text, _ = digest_data(tmp_cfg, con, 86_400.0)
    assert "- NF-1: tester, blocked (check-cap)" in text
    assert "- NF-2: tester, timed out\n" in text


def test_digest_data_lists_cardinal_s_writes_and_observe_drafts(tmp_cfg, con):
    from flocc import reach

    reach.record_write(con, 50.0, "comment", "NF-1", "too old")
    reach.record_write(con, 86_400.0 + 50.0, "comment", "NF-1", "Answered from the spec")
    reach.record_write(con, 86_400.0 + 60.0, "file", "NF-2", "Export rounds", filed="NF-9")
    reach.record_write(con, 86_400.0 + 70.0, "file", "NF-3", "Flaky test", mode="observe")
    text, _ = digest_data(tmp_cfg, con, 86_400.0 + 100.0)
    wrote = text.split("## What cardinal wrote in Linear and GitHub in the last 24 hours")[1]
    assert "too old" not in text
    assert "comment NF-1: Answered from the spec" in wrote
    assert "file NF-2 → NF-9: Export rounds" in wrote
    drafts = text.split("## Follow-ups cardinal would have filed in the last 24 hours")[1]
    assert "- NF-3: Flaky test" in drafts and "Export rounds" not in drafts.split("##")[0]


def test_digest_data_caps_every_list(tmp_cfg, con):
    for n in range(DIGEST_LINES + 5):
        ledger.add_digest(con, None, f"line {n}", float(n))
    for n in range(LIST_ROWS + 2):
        ledger.insert_claim(con, make(f"NF-{n + 1}", state="done"))
        ledger.add_hold(con, f"NF-{n + 100}", "wait", float(n))
    text, _ = digest_data(tmp_cfg, con, 1000.0)
    assert "(5 newer lines wait for the next digest)" in text and "line 104" not in text
    assert "line 99" in text
    assert text.count("(2 more)") == 2


def test_digest_data_through_is_the_last_line_shown(tmp_cfg, con):
    for n in range(DIGEST_LINES + 5):
        ledger.add_digest(con, None, f"line {n}", float(n))
    _, through = digest_data(tmp_cfg, con, 1000.0)
    assert through == ledger.pending_digest(con)[DIGEST_LINES - 1][0]


def test_digest_data_done_clears_through_the_id(tmp_cfg, tmp_path):
    from flocc.cli import main
    from tests.test_cli import _cfg_file

    path = _cfg_file(tmp_path)
    c = ledger.connect(tmp_path / "l.sqlite")
    ledger.add_digest(c, None, "a", 1.0)
    ledger.add_digest(c, None, "b", 2.0)
    [(first, _, _), _] = ledger.pending_digest(c)
    assert main(["--config", str(path), "digest-data", "--done", str(first)]) == 0
    assert [line for _, _, line in ledger.pending_digest(c)] == ["b"]


def test_digest_data_lists_the_day_s_agent_shell_calls(tmp_cfg, con):
    host.record_shell(con, 50.0, "old command", 0)
    host.record_shell(con, 86_400.0 + 50.0, "du -sh /tmp/pytest-of-flocc-agent", 0)
    host.record_shell(con, 86_400.0 + 60.0, "sleep 999", None)
    text, _ = digest_data(tmp_cfg, con, 86_400.0 + 100.0)
    assert "## Commands cardinal ran with agent-shell in the last 24 hours" in text
    assert "exit 0: du -sh /tmp/pytest-of-flocc-agent" in text
    assert "no exit status: sleep 999" in text and "old command" not in text


def test_digest_data_lists_cardinal_s_resumes_and_how_they_ended(tmp_cfg, con):
    for key, state in (("NF-1", "done"), ("NF-2", "blocked")):
        ledger.insert_claim(con, make(key, state=state))
        ledger.meta_set(con, f"cardinal_resume:{key}", "1")
    now = 8 * 86_400.0
    rows = [(now - 100, "NF-1", "needs-owner", "impl"), (now - 50, "NF-2", "idle", "impl")]
    rows.append((50.0, "NF-1", "crash", "impl"))
    for at, key, cause, phase in rows:
        value = json.dumps({"at": at, "key": key, "cause": cause, "phase": phase})
        ledger.meta_set(con, f"resume_log:{at:.6f}", value)
    text, _ = digest_data(tmp_cfg, con, now)
    assert "## Runs cardinal resumed in the last 7 days" in text
    assert "- 2 resumed, 1 ended done" in text
    assert "- NF-1 after needs-owner: done" in text and "- NF-2 after idle: blocked" in text
    assert "after crash" not in text


def test_a_done_claim_the_owner_reran_is_not_counted_as_cardinal_s(tmp_cfg, con):
    ledger.insert_claim(con, make("NF-1", state="done"))
    ledger.meta_set(
        con,
        "resume_log:10.000000",
        json.dumps({"at": 10.0, "key": "NF-1", "cause": "idle", "phase": "impl"}),
    )
    text, _ = digest_data(tmp_cfg, con, 100.0)
    assert "- 1 resumed, 0 ended done" in text and "- NF-1 after idle: done" in text


def test_a_resumed_claim_the_owner_reran_says_so(tmp_cfg, con):
    ledger.meta_set(
        con,
        "resume_log:10.000000",
        json.dumps({"at": 10.0, "key": "NF-5", "cause": "idle", "phase": "impl"}),
    )
    text, _ = digest_data(tmp_cfg, con, 100.0)
    assert "- NF-5 after idle: no claim" in text
