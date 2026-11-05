import argparse
import fcntl
import io
import json
import os
import signal
import subprocess
import threading
import time
import types
from concurrent.futures import ThreadPoolExecutor
from dataclasses import replace
from pathlib import Path

import pytest

from flocc import canary, ledger, reach
from flocc.cli import build_parser, main
from flocc.config import load
from flocc.host import STOPPED
from flocc.linear import BOT_PREFIX, LinearError
from flocc.publisher import PublishResult
from flocc.runtime import Runtime
from flocc.tick import tick_lock
from tests.fakes import FakeAgent, FakeLinear, FakeNotifier, FakeSessions
from tests.paths import EXAMPLE
from tests.test_ledger import make
from tests.test_reach import detail


def _cfg_file(tmp_path):
    path = tmp_path / "c.toml"
    path.write_text(
        EXAMPLE.read_text()
        .replace('"~/.local/state/flocc/ledger.sqlite"', f'"{tmp_path / "l.sqlite"}"')
        .replace("~/.local/state/flocc", str(tmp_path / "state"))
    )
    return path


def test_hold_says_a_running_claim_goes_on(tmp_path, capsys):
    path = _cfg_file(tmp_path)
    con = ledger.connect(tmp_path / "l.sqlite")
    ledger.insert_claim(con, make("NF-1"))
    assert main(["--config", str(path), "hold", "NF-1", "--reason=owner asked"]) == 0
    assert "its run in progress goes on" in capsys.readouterr().out
    assert ledger.issue_holds(con)["NF-1"][0] == "owner asked"


def test_release_of_a_held_issue_without_a_claim_lifts_the_hold(tmp_path, capsys):
    path = _cfg_file(tmp_path)
    con = ledger.connect(tmp_path / "l.sqlite")
    ledger.add_hold(con, "NF-3", "wait", 1.0)
    assert main(["--config", str(path), "release", "NF-3"]) == 0
    assert "released the hold on NF-3" in capsys.readouterr().out
    assert ledger.issue_holds(con) == {}


@pytest.mark.parametrize("state", [None, "running"])
def test_hold_only_lifts_the_hold_and_leaves_the_claim(tmp_path, capsys, state):
    path = _cfg_file(tmp_path)
    con = ledger.connect(tmp_path / "l.sqlite")
    if state:
        ledger.insert_claim(con, make("NF-1", state=state))
    ledger.add_hold(con, "NF-1", "wait", 1.0)
    assert main(["--config", str(path), "release", "--hold-only", "NF-1"]) == 0
    assert "released the hold on NF-1" in capsys.readouterr().out
    assert ledger.issue_holds(con) == {}
    if state:
        assert ledger.get_claim(con, "NF-1").state == state  # type: ignore[union-attr]


@pytest.mark.parametrize("state", ["done", "blocked", "timed_out"])
def test_hold_only_refuses_stopped_or_finished_work(tmp_path, capsys, state):
    path = _cfg_file(tmp_path)
    con = ledger.connect(tmp_path / "l.sqlite")
    ledger.insert_claim(con, make("NF-1", state=state))
    ledger.add_hold(con, "NF-1", "wait", 1.0)
    assert main(["--config", str(path), "release", "--hold-only", "NF-1"]) == 1
    assert "only the owner reruns it" in capsys.readouterr().err
    assert "NF-1" in ledger.issue_holds(con)
    assert ledger.get_claim(con, "NF-1").state == state  # type: ignore[union-attr]


def test_hold_only_waits_for_a_running_tick(tmp_path, capsys):
    from flocc.config import load

    path = _cfg_file(tmp_path)
    con = ledger.connect(tmp_path / "l.sqlite")
    ledger.add_hold(con, "NF-1", "wait", 1.0)
    with tick_lock(load(path).tick_lock):
        assert main(["--config", str(path), "release", "--hold-only", "NF-1"]) == 1
    assert "a tick is running" in capsys.readouterr().err
    assert "NF-1" in ledger.issue_holds(con)


def test_hold_only_without_a_hold_is_refused(tmp_path, capsys):
    path = _cfg_file(tmp_path)
    assert main(["--config", str(path), "release", "--hold-only", "NF-1"]) == 1
    assert "has no hold" in capsys.readouterr().err


def test_a_failed_accept_tests_keeps_the_hold(tmp_path, monkeypatch, capsys):
    path = _cfg_file(tmp_path)
    con = ledger.connect(tmp_path / "l.sqlite")
    ledger.insert_claim(con, make("NF-1", state="blocked"))
    ledger.add_hold(con, "NF-1", "wait", 1.0)
    monkeypatch.setattr("flocc.tick.build_runtime", lambda cfg, p: types.SimpleNamespace(con=con))
    assert main(["--config", str(path), "release", "--accept-tests", "NF-1"]) == 1
    assert "NF-1" in ledger.issue_holds(con)


def test_a_malformed_key_is_refused_by_the_parser(tmp_path):
    path = _cfg_file(tmp_path)
    assert main(["--config", str(path), "hold", "nf-1"]) == 2
    assert main(["--config", str(path), "hold", "NF-1\n"]) == 2


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
        .replace('"~/.local/state/flocc/ledger.sqlite"', f'"{tmp_path / "l.sqlite"}"')
        .replace('"~/src/backend"', f'"{origin}"')
        .replace("~/.local/state/flocc", str(tmp_path / "state"))
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
        exe="/opt/flocc",
        claude_exe="/opt/claude",
        config_path=path,
        publish_fn=lambda *a: None,
        git_head=lambda p: "sha0",
        review_diff=lambda c: "",
        comment_pr=lambda c, u, b: None,
        agent=FakeAgent(cfg),
    )
    monkeypatch.setattr("flocc.tick.build_runtime", lambda cfg, path: rt)
    assert main(["--config", str(path), "release", "NF-1"]) == 0
    assert ledger.get_claim(con, "NF-1") is None and sessions.killed == ["NF-1-test"]
    captured = capsys.readouterr()
    assert "Entity not found" in captured.err
    assert "worktree removed" in captured.out
    ledger.insert_claim(con, make("NF-1", worktree=str(tmp_path / "wt" / "NF-1")))
    assert main(["--config", str(path), "release", "NF-1", "--keep-worktree"]) == 0
    assert "worktree kept" in capsys.readouterr().out
    ledger.insert_claim(con, make("NF-1", worktree=str(tmp_path / "wt" / "NF-1")))
    assert main(["--config", str(path), "release", "NF-1", "--accept-tests"]) == 1
    assert "no tester commit" in capsys.readouterr().err
    ledger.remember_tests(con, "NF-1", "sha-base", "sha-tests")
    assert main(["--config", str(path), "release", "NF-1", "--accept-tests"]) == 0
    assert "worktree kept" in capsys.readouterr().out
    assert ledger.accepted_tests(con, "NF-1") == "sha0"


def test_bad_config_fails_cleanly_for_every_command(tmp_path, capsys):
    bad = tmp_path / "bad.toml"
    bad.write_text("= not toml")
    for command in ("status", "tick", "release NF-1"):
        assert main(["--config", str(bad), *command.split()]) == 1
        assert "FAIL  config:" in capsys.readouterr().out


def test_subcommands_have_help(capsys):
    assert main(["--help"]) == 0
    assert "Show claims, last tick, usage and disk" in capsys.readouterr().out
    assert main(["status", "--help"]) == 0


def test_pre_commit_from_an_old_worktree_is_a_no_op():
    assert main(["hook", "pre-commit"]) == 0


@pytest.fixture
def claims_env(tmp_path, monkeypatch):
    config = tmp_path / "c.toml"
    config.write_text(EXAMPLE.read_text())
    state = tmp_path / "state" / "NF-1"
    state.mkdir(parents=True)
    monkeypatch.setenv("FLOCC_STATE", str(state))
    monkeypatch.setenv("FLOCC_LEDGER", str(tmp_path / "hook.sqlite"))
    monkeypatch.setenv("FLOCC_CONFIG", str(config))
    return ledger.connect(tmp_path / "hook.sqlite")


def session_env(monkeypatch, tmp_path, phase="review"):
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path))
    monkeypatch.setenv("FLOCC_PHASE", phase)


def test_done_in_the_review_phase_needs_the_review(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "review")
    assert main(["done"]) == 1
    (tmp_path / "review.md").write_text(" \n")
    assert main(["done"]) == 1 and not (tmp_path / "done.json").exists()
    (tmp_path / "review.md").write_text("Verdict: approve\n")
    assert main(["done"]) == 0
    assert json.loads((tmp_path / "done.json").read_text())["summary"] == "review"


def test_done_elsewhere_needs_a_summary(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    assert main(["done"]) == 2 and "--summary" in capsys.readouterr().err
    assert main(["done", "--summary", "did it"]) == 0
    assert main(["review-done"]) == 2


def test_resume_replaces_fix():
    from flocc.cli import COMMANDS

    assert "resume" in COMMANDS and "fix" not in COMMANDS


def test_done_records_each_question_for_the_owner(tmp_path, monkeypatch):
    session_env(monkeypatch, tmp_path, "impl")
    args = ["done", "--summary", "Did half.", "--needs-owner", "Which currency?"]
    assert main([*args, "--needs-owner", "Which region?"]) == 0
    data = json.loads((tmp_path / "done.json").read_text())
    assert data["questions"] == ["Which currency?", "Which region?"]
    assert data["outcome"] == "work" and data["summary"] == "Did half."


def test_done_refuses_the_old_owner_heading_and_names_the_flag(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    assert main(["done", "--summary", "Did half.\n\n## Needs the owner\n- which currency"]) == 2
    assert "--needs-owner" in capsys.readouterr().err
    assert not (tmp_path / "done.json").exists()


def test_done_refuses_an_empty_question(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    assert main(["done", "--summary", "Did it.", "--needs-owner", "  "]) == 2
    assert not (tmp_path / "done.json").exists()


@pytest.mark.parametrize("phase", ["test", "impl"])
def test_nothing_to_do_ends_with_its_reason_as_the_summary(tmp_path, monkeypatch, phase):
    session_env(monkeypatch, tmp_path, phase)
    assert main(["done", "--nothing-to-do", "Already fixed by NF-9."]) == 0
    data = json.loads((tmp_path / "done.json").read_text())
    assert data == {**data, "summary": "Already fixed by NF-9.", "outcome": "nothing"}
    assert data["questions"] == []


@pytest.mark.parametrize(
    "phase, extra",
    [
        ("fix", []),
        ("review", []),
        ("impl", ["--summary", "x"]),
        ("impl", ["--needs-owner", "q?"]),
        ("impl", []),
    ],
)
def test_nothing_to_do_is_refused_outside_its_use(tmp_path, monkeypatch, phase, extra):
    session_env(monkeypatch, tmp_path, phase)
    (tmp_path / "review.md").write_text("Verdict: approve\n")
    why = [] if phase == "impl" and not extra else ["Nothing."]
    args = ["done", "--nothing-to-do", *(why or ["  "]), *extra]
    assert main(args) == 2
    assert not (tmp_path / "done.json").exists()


def test_the_reviewer_cannot_pass_owner_questions(tmp_path, monkeypatch):
    session_env(monkeypatch, tmp_path, "review")
    (tmp_path / "review.md").write_text("Verdict: approve\n")
    assert main(["done", "--needs-owner", "q?"]) == 2


def commit_tester(tmp_path, monkeypatch):
    wt = tmp_path / "wt"
    wt.mkdir()
    for args in (["init", "-q"], ["config", "user.name", "t"], ["config", "user.email", "t@t"]):
        subprocess.run(["git", *args], cwd=wt, check=True)

    def commit(name, text):
        (wt / name).parent.mkdir(parents=True, exist_ok=True)
        (wt / name).write_text(text)
        subprocess.run(["git", "add", "."], cwd=wt, check=True)
        subprocess.run(["git", "commit", "-qm", name], cwd=wt, check=True)
        out = subprocess.run(["git", "rev-parse", "HEAD"], cwd=wt, check=True, capture_output=True)
        return out.stdout.decode().strip()

    base = commit("README", "x")
    tester = commit("gateway/tests/test_a.py", "def test_a(): ...\n")
    state = Path(os.environ["FLOCC_STATE"])
    (state / "tester.json").write_text(json.dumps({"base_sha": base, "tester_sha": tester}))
    monkeypatch.chdir(wt)


def test_tests_amended_records_paths_with_the_reason(claims_env, tmp_path, monkeypatch, capsys):
    commit_tester(tmp_path, monkeypatch)
    state = Path(os.environ["FLOCC_STATE"])
    cmd = ["tests-amended", "gateway/tests/test_a.py", "--reason", "contradicts the API"]
    assert main(cmd) == 0
    assert "recorded 1 amended test file" in capsys.readouterr().out
    recorded = json.loads((state / "tests-amended.json").read_text())
    assert recorded == {"gateway/tests/test_a.py": {"reason": "contradicts the API"}}


def test_tests_amended_merges_into_the_existing_file(claims_env, tmp_path, monkeypatch):
    commit_tester(tmp_path, monkeypatch)
    state = Path(os.environ["FLOCC_STATE"])
    (state / "tests-amended.json").write_text(json.dumps({"other.py": {"reason": "r"}}))
    assert main(["tests-amended", "gateway/tests/test_a.py", "--reason", "why"]) == 0
    recorded = json.loads((state / "tests-amended.json").read_text())
    assert set(recorded) == {"other.py", "gateway/tests/test_a.py"}


def test_tests_amended_refuses_a_file_the_tester_did_not_write(
    claims_env, tmp_path, monkeypatch, capsys
):
    commit_tester(tmp_path, monkeypatch)
    state = Path(os.environ["FLOCC_STATE"])
    assert main(["tests-amended", "src/app.py", "--reason", "why"]) == 2
    assert "src/app.py" in capsys.readouterr().err
    assert not (state / "tests-amended.json").exists()


def test_tests_amended_without_a_tester_is_refused(claims_env, tmp_path, capsys):
    assert main(["tests-amended", "a.py", "--reason", "why"]) == 1
    assert "no tester ran" in capsys.readouterr().err


def test_session_start_on_compaction_lists_the_scratchpad(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setenv("FLOCC_LEDGER", str(tmp_path / "l.sqlite"))
    payload = {"source": "compact", "scratchpad_dir": "/tmp/claude-1000/x/s1/scratchpad"}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    assert main(["session-start"]) == 0
    assert (tmp_path / "scratchpads").read_text().splitlines() == [payload["scratchpad_dir"]]
    assert not (tmp_path / "compacted").exists()


def test_session_start_on_startup_prints_the_context(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setenv("FLOCC_LEDGER", str(tmp_path / "l.sqlite"))
    (tmp_path / "context.md").write_text("CTX")
    monkeypatch.setattr("sys.stdin", io.StringIO('{"source": "startup"}'))
    assert main(["session-start"]) == 0
    assert "CTX" in capsys.readouterr().out
    assert not (tmp_path / "launches.jsonl").exists()


def test_session_start_prints_the_context_when_bookkeeping_fails(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setenv("FLOCC_LEDGER", str(tmp_path / "l.sqlite"))
    (tmp_path / "context.md").write_text("CTX")
    (tmp_path / "scratchpads").mkdir()
    payload = {"source": "startup", "scratchpad_dir": "/tmp/claude-1000/x/s1/scratchpad"}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    assert main(["session-start"]) == 0
    captured = capsys.readouterr()
    assert "CTX" in captured.out and "bookkeeping failed" in captured.err


def stop(monkeypatch, payload, *flags):
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    return main(["stop", *flags])


def test_stop_refuses_once_then_marks_the_session_idle(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    assert stop(monkeypatch, {"stop_hook_active": False, "last_assistant_message": "a?"}) == 0
    refusal = json.loads(capsys.readouterr().out)
    assert refusal["decision"] == "block" and "flocc done" in refusal["reason"]
    assert "--needs-owner" in refusal["reason"] and not (tmp_path / "idle").exists()
    assert stop(monkeypatch, {"stop_hook_active": True, "last_assistant_message": "x" * 700}) == 0
    assert capsys.readouterr().out == ""
    marker = json.loads((tmp_path / "idle").read_text())
    assert marker["message"] == "x" * 600 and marker["at"] > 0


def test_stop_falls_back_to_the_transcript_for_the_idle_message(tmp_path, monkeypatch):
    session_env(monkeypatch, tmp_path, "impl")
    transcript = tmp_path / "t.jsonl"
    text = {"type": "text", "text": "please run it for me"}
    transcript.write_text(json.dumps({"type": "assistant", "message": {"content": [text]}}))
    assert stop(monkeypatch, {"stop_hook_active": True, "transcript_path": str(transcript)}) == 0
    assert json.loads((tmp_path / "idle").read_text())["message"] == "please run it for me"


def test_stop_lets_a_finished_run_stop(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    (tmp_path / "done.json").write_text('{"summary": "ok"}')
    assert stop(monkeypatch, {"stop_hook_active": False}) == 0
    assert capsys.readouterr().out == "" and not (tmp_path / "idle").exists()


def test_stop_lets_a_session_waiting_on_background_work_stop(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    payload = {"stop_hook_active": False, "background_tasks": [{"id": "1", "type": "shell"}]}
    assert stop(monkeypatch, payload) == 0
    assert capsys.readouterr().out == "" and not (tmp_path / "idle").exists()
    assert json.loads((tmp_path / "background").read_text()) == ["shell"]


def test_a_scheduled_wakeup_counts_as_background(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    payload = {"stop_hook_active": True, "background_tasks": [], "session_crons": [{"id": "c"}]}
    assert stop(monkeypatch, payload) == 0
    assert capsys.readouterr().out == "" and not (tmp_path / "idle").exists()


def test_stop_failure_marks_idle_except_for_a_usage_limit(tmp_path, monkeypatch):
    session_env(monkeypatch, tmp_path, "impl")
    assert stop(monkeypatch, {"error": "rate_limit"}, "--failure") == 0
    assert not (tmp_path / "idle").exists()
    payload = {"error": "server_error", "last_assistant_message": "API Error: 500"}
    assert stop(monkeypatch, payload, "--failure") == 0
    assert json.loads((tmp_path / "idle").read_text())["message"] == "API Error: 500"


def test_stop_bookkeeping_failure_exits_1_never_2(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")

    def boom(*a, **kw):
        raise OSError("disk gone")

    monkeypatch.setattr("flocc.idle.note_idle", boom)
    assert stop(monkeypatch, {"stop_hook_active": True}) == 1
    assert "disk gone" in capsys.readouterr().err


def test_parallel_commits_take_only_their_own_paths(origin, monkeypatch):
    monkeypatch.chdir(origin)
    for name in ("a1.txt", "a2.txt", "b1.txt", "b2.txt"):
        (origin / name).write_text(name)
    jobs = [("A", ["a1.txt", "a2.txt"]), ("B", ["b1.txt", "b2.txt"])]
    with ThreadPoolExecutor(max_workers=2) as pool:
        codes = list(pool.map(lambda job: main(["commit", "-m", job[0], "--", *job[1]]), jobs))
    assert codes == [0, 0]
    for msg, files in (("A", {"a1.txt", "a2.txt"}), ("B", {"b1.txt", "b2.txt"})):
        log = ["git", "log", "-1", "--format=%H", f"--grep=^{msg}$"]
        sha = subprocess.run(log, cwd=origin, capture_output=True, text=True, check=True)
        show = ["git", "show", "--name-only", "--format=", sha.stdout.strip()]
        shown = subprocess.run(show, cwd=origin, capture_output=True, text=True, check=True)
        assert set(shown.stdout.split()) == files


def test_commit_needs_paths(capsys):
    assert main(["commit", "-m", "x", "--"]) == 2


@pytest.mark.parametrize(
    "argv",
    [["ship"], ["keys", "rotate", "github"], ["host", "add", "x"], ["--config", "c.toml", "login"]],
)
def test_desktop_verbs_are_refused_on_a_host(argv, capsys):
    assert main(argv) == 2
    assert "that is a desktop command" in capsys.readouterr().err


def test_no_host_command_takes_dash_h():
    # The desktop removes -H from the command line before it reaches the host.
    parser = build_parser()
    parsers = [parser]
    for action in parser._actions:
        if isinstance(action, argparse._SubParsersAction):
            parsers += action.choices.values()
    for p in parsers:
        for action in p._actions:
            assert not {"-H", "--host"} & set(action.option_strings), p.prog


class Exec(Exception):
    pass


@pytest.fixture
def host_cfg(tmp_path, monkeypatch):
    path = tmp_path / "c.toml"
    path.write_text(EXAMPLE.read_text().replace("~/.local/state/flocc", str(tmp_path / "state")))

    def fake_exec(file, argv):
        raise Exec(argv)

    monkeypatch.setattr(os, "execvp", fake_exec)
    return path


def test_logs_reads_the_journal_on_a_systemd_host(host_cfg, monkeypatch):
    monkeypatch.setattr("flocc.status.detect_mode", lambda: "systemd")
    with pytest.raises(Exec) as exc:
        main(["--config", str(host_cfg), "logs", "-n", "50", "-f"])
    assert exc.value.args[0] == [
        "journalctl",
        "--user",
        "-u",
        "flocc",
        "--no-pager",
        "-f",
        "-n",
        "50",
    ]


def test_logs_tails_run_log_on_a_loop_host(host_cfg, tmp_path, monkeypatch):
    monkeypatch.setattr("flocc.status.detect_mode", lambda: "loop")
    with pytest.raises(Exec) as exc:
        main(["--config", str(host_cfg), "logs"])
    assert exc.value.args[0] == ["tail", "-n", "100", str(tmp_path / "state" / "run.log")]


def test_logs_issue_prints_only_that_issues_lines(host_cfg, monkeypatch, capsys):
    monkeypatch.setattr("flocc.status.detect_mode", lambda: "systemd")
    journal = (
        "1767225600.5 host flocc[7]: NF-240 started\n"
        "1767225601.5 host flocc[7]: NF-2400 other\n"
        "1767225602.5 host flocc[7]: NF-24 other\n"
        "1767225603.5 host flocc[7]: NF-240 pushed"
    )

    def run(argv, **kw):
        return subprocess.CompletedProcess(argv, 0, journal + "\n", "")

    monkeypatch.setattr("subprocess.run", run)
    assert main(["--config", str(host_cfg), "logs", "--issue", "NF-240", "-n", "5"]) == 0
    stamps = [time.strftime("%H:%M:%S", time.localtime(t)) for t in (1767225600, 1767225603)]
    assert capsys.readouterr().out == f"{stamps[0]} NF-240 started\n{stamps[1]} NF-240 pushed\n"


def test_logs_issue_on_a_loop_host_prints_bare_text(host_cfg, tmp_path, monkeypatch, capsys):
    monkeypatch.setattr("flocc.status.detect_mode", lambda: "loop")
    (tmp_path / "state").mkdir()
    (tmp_path / "state" / "run.log").write_text("NF-240 a\nNF-2400 b\nNF-240 c\n")
    assert main(["--config", str(host_cfg), "logs", "--issue", "NF-240", "-n", "1"]) == 0
    assert capsys.readouterr().out == "NF-240 c\n"


def test_logs_issue_does_not_follow(host_cfg, capsys):
    assert main(["--config", str(host_cfg), "logs", "--issue", "NF-240", "-f"]) == 2
    assert capsys.readouterr().err == "--issue does not follow; drop -f\n"


def test_attach_goes_through_the_wrapper(host_cfg):
    with pytest.raises(Exec) as exc:
        main(["--config", str(host_cfg), "attach", "NF-12-review"])
    argv = exc.value.args[0]
    assert argv == [*load(host_cfg).agent_run, "attach", "NF-12-review"]


def _peek_host(host_cfg, monkeypatch, pane, alive=True, claim=True):
    from dataclasses import replace

    cfg = load(host_cfg)
    cfg.ledger_path.parent.mkdir(parents=True, exist_ok=True)
    if claim:
        con = ledger.connect(cfg.ledger_path)
        ledger.insert_claim(con, replace(make("NF-240"), phase="review"))
    fake = FakeSessions(
        alive_names={"NF-240-review"} if alive else set(), panes={"NF-240-review": pane}
    )
    monkeypatch.setattr("flocc.agent.Agent.sessions", lambda self: fake)
    return fake


def test_peek_prints_the_session_tail(host_cfg, monkeypatch, capsys):
    fake = _peek_host(host_cfg, monkeypatch, "one\ntwo\x1b[31m red\nthree\n\n\n  \n")
    assert main(["--config", str(host_cfg), "peek", "NF-240", "-n", "2"]) == 0
    assert capsys.readouterr().out == "two [31m red\nthree\n"
    assert fake.captured == [("NF-240-review", 202)]


def test_peek_exits_3_without_a_live_session(host_cfg, monkeypatch, capsys):
    _peek_host(host_cfg, monkeypatch, "x", alive=False)
    assert main(["--config", str(host_cfg), "peek", "NF-240"]) == 3
    assert capsys.readouterr().err == "peek: no live session for NF-240\n"


def test_peek_exits_3_without_a_claim(host_cfg, monkeypatch, capsys):
    _peek_host(host_cfg, monkeypatch, "x", claim=False)
    assert main(["--config", str(host_cfg), "peek", "NF-240"]) == 3
    assert capsys.readouterr().err == "peek: no live session for NF-240\n"


def test_peek_exits_3_when_the_session_dies_before_the_capture(host_cfg, monkeypatch, capsys):
    fake = _peek_host(host_cfg, monkeypatch, "")
    assert main(["--config", str(host_cfg), "peek", "NF-240"]) == 3
    assert fake.captured


def _canary(host_cfg, monkeypatch, result):
    monkeypatch.setattr("flocc.agent.Agent.setup_base", lambda self, repo: result)
    cfg = load(host_cfg)
    cfg.ledger_path.parent.mkdir(parents=True, exist_ok=True)
    return cfg, next(iter(cfg.repos))


@pytest.mark.parametrize("sig", [signal.SIGTERM, signal.SIGHUP])
def test_canary_turns_a_kill_signal_into_an_exit_and_restores_the_handlers(
    host_cfg, monkeypatch, sig
):
    _, repo = _canary(host_cfg, monkeypatch, None)
    before = signal.getsignal(sig)

    def killed(self, repo):
        signal.raise_signal(sig)

    monkeypatch.setattr("flocc.agent.Agent.setup_base", killed)
    assert main(["--config", str(host_cfg), "canary", repo]) == 128 + sig
    assert signal.getsignal(sig) == before


def test_canary_clears_the_mark_when_setup_passes(host_cfg, monkeypatch, capsys):
    cfg, repo = _canary(host_cfg, monkeypatch, (0, "ok", "abc123def456"))
    canary.mark(ledger.connect(cfg.ledger_path), repo, "abc123def456", 1.0, "old")
    assert main(["--config", str(host_cfg), "canary", repo]) == 0
    assert canary.get_mark(ledger.connect(cfg.ledger_path), repo) is None
    assert "no longer held" in capsys.readouterr().out


def test_canary_marks_the_repo_when_setup_fails(host_cfg, monkeypatch, capsys):
    cfg, repo = _canary(host_cfg, monkeypatch, (2, "go: not found", "abc123def456"))
    assert main(["--config", str(host_cfg), "canary", repo]) == 1
    mark = canary.get_mark(ledger.connect(cfg.ledger_path), repo)
    assert mark is not None and mark["output"] == "go: not found"
    assert "go: not found" in capsys.readouterr().err


def test_canary_reruns_the_check_for_a_check_mark(host_cfg, monkeypatch, capsys):
    cfg, repo = _canary(host_cfg, monkeypatch, None)
    con = ledger.connect(cfg.ledger_path)
    canary.mark(con, repo, "abc123def456", 1.0, "old", check="just check")
    monkeypatch.setattr("flocc.agent.Agent.check_base", lambda self, repo: ("def456abc123", None))
    assert main(["--config", str(host_cfg), "canary", repo]) == 0
    assert canary.get_mark(con, repo) is None
    canary.mark(con, repo, "abc123def456", 1.0, "old", check="just check")
    failure = PublishResult("check_failed", "E1 boom", log="E1 boom full")
    monkeypatch.setattr(
        "flocc.agent.Agent.check_base", lambda self, repo: ("def456abc123", failure)
    )
    assert main(["--config", str(host_cfg), "canary", repo]) == 1
    m = canary.get_mark(con, repo)
    assert m is not None and m["sha"] == "def456abc123" and m["check"] == "just check"
    assert "E1 boom" in capsys.readouterr().err


def test_canary_forget_clears_every_mark_and_counts_them(host_cfg, capsys):
    cfg = load(host_cfg)
    cfg.ledger_path.parent.mkdir(parents=True, exist_ok=True)
    con = ledger.connect(cfg.ledger_path)
    canary.mark(con, "backend", "abc", 1.0, "x")
    canary.mark(con, "web", "def", 1.0, "y")
    canary.clear(con, "docs")
    assert main(["--config", str(host_cfg), "canary", "--forget"]) == 0
    assert "cleared 2 main-broken holds" in capsys.readouterr().out
    assert canary.get_mark(con, "backend") is None and canary.get_mark(con, "web") is None
    assert main(["--config", str(host_cfg), "canary", "--forget"]) == 0
    assert "cleared 0 main-broken holds" in capsys.readouterr().out


def test_canary_needs_a_repo_or_forget(host_cfg, capsys):
    assert main(["--config", str(host_cfg), "canary"]) == 2
    assert "repo" in capsys.readouterr().err


def test_canary_refuses_an_unknown_repo(host_cfg, capsys):
    assert main(["--config", str(host_cfg), "canary", "nope"]) == 2
    assert "no repo nope" in capsys.readouterr().err


def test_canary_waits_for_a_running_tick(host_cfg, monkeypatch, capsys):
    cfg, repo = _canary(host_cfg, monkeypatch, (0, "", "abc"))
    with tick_lock(cfg.tick_lock):
        assert main(["--config", str(host_cfg), "canary", repo]) == 1
    assert "a tick is running" in capsys.readouterr().err


def test_canary_reports_a_base_it_cannot_fetch(host_cfg, monkeypatch, capsys):
    _, repo = _canary(host_cfg, monkeypatch, None)

    def unreachable(self, repo):
        raise subprocess.CalledProcessError(128, ["git", "fetch"], stderr="no remote")

    monkeypatch.setattr("flocc.agent.Agent.setup_base", unreachable)
    assert main(["--config", str(host_cfg), "canary", repo]) == 1
    assert "no remote" in capsys.readouterr().err


def test_host_report_prints_the_reading_and_flocc_agent_s_part(host_cfg, monkeypatch, capsys):
    load(host_cfg).ledger_path.parent.mkdir(parents=True, exist_ok=True)
    usage = {"largest": [], "issues": {"NF-5": 2**30}, "containers": []}
    monkeypatch.setattr("flocc.agent.Agent.usage", lambda self: usage)
    assert main(["--config", str(host_cfg), "host-report"]) == 0
    out = capsys.readouterr().out
    assert "/tmp 0% used" in out and "- NF-5: 1.0 GiB" in out and "Episode: none open" in out


def test_tick_lock_waits_up_to_its_limit(tmp_path):
    path = tmp_path / "tick.lock"
    with tick_lock(path) as first:
        assert first
        start = time.monotonic()
        with tick_lock(path, 0.3) as second:
            assert not second
        assert time.monotonic() - start >= 0.3
    blocker = path.open("w")
    fcntl.flock(blocker, fcntl.LOCK_EX)
    threading.Timer(0.2, blocker.close).start()
    with tick_lock(path, 5.0) as third:
        assert third


def test_cleanup_now_reports_busy_after_its_wait(tmp_path, monkeypatch, capsys):
    path = _cfg_file(tmp_path)
    monkeypatch.setattr("flocc.cleanup.NOW_LOCK_WAIT_S", 0.2)
    monkeypatch.setattr("flocc.cleanup.cleanup", lambda *a, **kw: pytest.fail("ran under a tick"))
    with tick_lock(load(path).tick_lock):
        assert main(["--config", str(path), "cleanup", "--now"]) == 1
    assert "busy" in capsys.readouterr().err


def test_cleanup_now_sweeps_with_the_short_ages(tmp_path, monkeypatch, capsys):
    path = _cfg_file(tmp_path)
    seen = {}

    def fake(cfg, con, agent, now, **ages):
        seen.update(ages)
        return {"removed": ["/tmp/pytest-of-flocc-agent/pytest-3"], "kept": [], "freed": 3 * 2**30}

    monkeypatch.setattr("flocc.cleanup.cleanup", fake)
    assert main(["--config", str(path), "cleanup", "--now"]) == 0
    assert seen == {"scratch_age_s": 600, "pytest_age_s": 0.0}
    assert "removed 1, freed 3.0 GiB in /tmp" in capsys.readouterr().out


def _running_claim(host_cfg, key="NF-5", phase="impl"):
    from dataclasses import replace

    cfg = load(host_cfg)
    cfg.ledger_path.parent.mkdir(parents=True, exist_ok=True)
    ledger.insert_claim(ledger.connect(cfg.ledger_path), replace(make(key), phase=phase))


def test_stop_tests_stops_the_run_s_tests_and_tells_the_session(host_cfg, monkeypatch, capsys):
    _running_claim(host_cfg)
    calls = []

    def stop(self, name, worktree):
        calls.append((name, worktree))
        return {"claude": 41, "killed": [42, 43], "containers": ["nf5-db"]}

    fake = FakeSessions()
    monkeypatch.setattr("flocc.agent.Agent.stop_tests", stop)
    monkeypatch.setattr("flocc.agent.Agent.sessions", lambda self: fake)
    assert main(["--config", str(host_cfg), "stop-tests", "NF-5"]) == 0
    assert calls == [("NF-5", "/wt/NF-5")] and fake.sent == [("NF-5", STOPPED)]
    assert "killed 2 processes, removed 1 containers (nf5-db)" in capsys.readouterr().out


def test_stop_tests_without_a_live_session_tells_nobody(host_cfg, monkeypatch, capsys):
    _running_claim(host_cfg)
    fake = FakeSessions()
    monkeypatch.setattr(
        "flocc.agent.Agent.stop_tests",
        lambda self, name, wt: {"claude": None, "killed": [], "containers": []},
    )
    monkeypatch.setattr("flocc.agent.Agent.sessions", lambda self: fake)
    assert main(["--config", str(host_cfg), "stop-tests", "NF-5"]) == 0
    assert fake.sent == [] and "no live session to tell" in capsys.readouterr().out


def test_stop_tests_refuses_an_issue_with_no_running_run(host_cfg, capsys):
    load(host_cfg).ledger_path.parent.mkdir(parents=True, exist_ok=True)
    assert main(["--config", str(host_cfg), "stop-tests", "NF-404"]) == 1
    assert "NF-404 has no running run" in capsys.readouterr().err


@pytest.fixture
def amend_env(tmp_path, monkeypatch):
    """A running NF-1 whose repo has an amended label and a PR; returns the runtime and the
    gh calls (argv, GH_TOKEN) the command made."""
    path = tmp_path / "c.toml"
    token = tmp_path / "publish.token"
    token.write_text("ghp_publish\n")
    path.write_text(
        EXAMPLE.read_text()
        .replace('"~/.local/state/flocc/ledger.sqlite"', f'"{tmp_path / "l.sqlite"}"')
        .replace("~/.config/flocc/gh-publish.token", str(token))
        .replace("~/.local/state/flocc", str(tmp_path / "state"))
        .replace("[repos.backend]", '[repos.backend]\namended_label = "tests-amended"')
    )
    cfg = load(path)
    con = ledger.connect(cfg.ledger_path)
    ledger.insert_claim(con, replace(make("NF-1"), phase="impl"))
    (cfg.state_root / "NF-1").mkdir(parents=True)
    (cfg.state_root / "NF-1" / "pr-url").write_text("https://github.com/o/r/pull/7")
    linear = FakeLinear()
    sessions = FakeSessions()
    rt = Runtime(
        cfg=cfg,
        con=con,
        linear=linear,
        sessions=sessions,
        notifier=FakeNotifier(),
        now=lambda: 5.0,
        exe="/opt/flocc",
        claude_exe="/opt/claude",
        config_path=path,
        publish_fn=lambda *a: None,
        git_head=lambda p: "sha0",
        review_diff=lambda c: "",
        comment_pr=lambda c, u, b: None,
        agent=FakeAgent(cfg),
    )
    monkeypatch.setattr("flocc.tick.build_runtime", lambda cfg, path: rt)
    gh = []

    def run(argv, **kw):
        gh.append((argv, kw["env"]["GH_TOKEN"]))
        return subprocess.CompletedProcess(argv, 0, "", "")

    monkeypatch.setattr(subprocess, "run", run)
    return types.SimpleNamespace(path=path, rt=rt, gh=gh, linear=linear, sessions=sessions)


def test_issue_prints_the_rendered_issue(amend_env, capsys):
    amend_env.linear.detail = detail()
    assert main(["--config", str(amend_env.path), "issue", "NF-1"]) == 0
    out = capsys.readouterr().out
    assert out.startswith(reach.DATA_NOTE) and "NF-1: Fix rounding" in out


@pytest.mark.parametrize(
    "argv",
    [
        ["issue", "ENG-4"],
        ["comment", "ENG-4", "--text", "hi"],
        ["pr", "ENG-4"],
        ["pr-comment", "ENG-4", "--text", "hi"],
        ["file", "ENG-4", "--title", "t", "--text", "x"],
    ],
)
def test_every_reach_verb_refuses_another_team_s_issue_before_any_call(amend_env, argv, capsys):
    assert main(["--config", str(amend_env.path), *argv]) == 1
    assert "ENG-4 is not an NF issue" in capsys.readouterr().err
    assert amend_env.gh == [] and amend_env.linear.comments == [] and amend_env.linear.created == []


def test_search_prints_the_team_s_matches(amend_env, capsys):
    amend_env.linear.found = [("NF-5", "Backlog", "Flaky export")]
    assert main(["--config", str(amend_env.path), "search", "--query", "export"]) == 0
    assert "- NF-5 (Backlog): Flaky export" in capsys.readouterr().out


def test_comment_posts_as_cardinal_and_logs_the_write(amend_env, capsys):
    assert main(["--config", str(amend_env.path), "comment", "NF-1", "--text", "Answered."]) == 0
    assert amend_env.linear.comments == ["cardinal: Answered."]
    [w] = reach.writes(amend_env.rt.con, 0.0)
    assert (w["kind"], w["key"], w["text"]) == ("comment", "NF-1", "Answered.")
    assert "commented on NF-1" in capsys.readouterr().out


def test_a_linear_failure_is_reported_and_logs_nothing(amend_env, monkeypatch, capsys):
    def fail(issue_id, text):
        raise LinearError("rate limited")

    monkeypatch.setattr(amend_env.linear, "comment", fail)
    assert main(["--config", str(amend_env.path), "comment", "NF-1", "--text", "x"]) == 1
    assert "comment: rate limited" in capsys.readouterr().err
    assert reach.writes(amend_env.rt.con, 0.0) == []


PR_FIELDS = (
    "state,mergeable,reviewDecision,additions,deletions,changedFiles,statusCheckRollup,"
    "reviews,comments"
)


def test_pr_prints_the_claim_s_pr_with_the_publish_token(amend_env, monkeypatch, capsys):
    calls = []

    def run(argv, **kw):
        calls.append((argv, kw["env"]["GH_TOKEN"]))
        return subprocess.CompletedProcess(argv, 0, json.dumps({"state": "OPEN"}), "")

    monkeypatch.setattr(subprocess, "run", run)
    assert main(["--config", str(amend_env.path), "pr", "NF-1"]) == 0
    url = "https://github.com/o/r/pull/7"
    assert calls == [(["gh", "pr", "view", url, "--json", PR_FIELDS], "ghp_publish")]
    assert "State: OPEN" in capsys.readouterr().out


@pytest.mark.parametrize("verb", [["pr"], ["pr-comment", "--text", "x"]])
def test_pr_verbs_without_a_pr_or_a_claim_are_refused_before_gh(amend_env, verb, capsys):
    (amend_env.rt.cfg.state_root / "NF-1" / "pr-url").unlink()
    assert main(["--config", str(amend_env.path), verb[0], "NF-1", *verb[1:]]) == 1
    assert "NF-1 has no PR" in capsys.readouterr().err
    assert main(["--config", str(amend_env.path), verb[0], "NF-404", *verb[1:]]) == 1
    assert "no claim for NF-404" in capsys.readouterr().err
    assert amend_env.gh == []


def test_pr_comment_posts_as_cardinal_and_logs_the_write(amend_env, monkeypatch, capsys):
    bodies = []

    def run(argv, **kw):
        bodies.append((argv, kw["input"], kw["env"]["GH_TOKEN"]))
        return subprocess.CompletedProcess(argv, 0, "", "")

    monkeypatch.setattr(subprocess, "run", run)
    assert main(["--config", str(amend_env.path), "pr-comment", "NF-1", "--text", "LGTM"]) == 0
    url = "https://github.com/o/r/pull/7"
    assert bodies == [
        (
            ["gh", "pr", "comment", url, "--body-file", "-"],
            f"{BOT_PREFIX} cardinal: LGTM",
            "ghp_publish",
        )
    ]
    assert [w["kind"] for w in reach.writes(amend_env.rt.con, 0.0)] == ["pr-comment"]


def test_a_failed_pr_comment_is_reported_and_logs_nothing(amend_env, monkeypatch, capsys):
    def fail(argv, **kw):
        raise subprocess.CalledProcessError(1, argv, stderr="HTTP 403")

    monkeypatch.setattr(subprocess, "run", fail)
    assert main(["--config", str(amend_env.path), "pr-comment", "NF-1", "--text", "x"]) == 1
    assert "pr-comment: HTTP 403" in capsys.readouterr().err
    assert reach.writes(amend_env.rt.con, 0.0) == []


def file_argv(e, key="NF-1"):
    return [
        "--config", str(e.path), "file", key,
        "--title", "Export rounds wrong", "--text", "Seen in review.",
    ]  # fmt: skip


def turn_filing_on(e):
    # The fixture's build_runtime patch returns this runtime, so the file on disk is not read.
    e.rt.cfg = replace(e.rt.cfg, cardinal_file_on=True)


def test_file_in_observe_mode_creates_nothing_and_records_the_draft(amend_env, capsys):
    amend_env.linear.detail = detail()
    assert main(file_argv(amend_env)) == 0
    assert amend_env.linear.created == []
    [w] = reach.writes(amend_env.rt.con, 0.0)
    assert (w["kind"], w["mode"], w["text"]) == ("file", "observe", "Export rounds wrong")
    assert "observe mode" in capsys.readouterr().out


def test_file_when_on_creates_a_backlog_draft_linked_to_the_source(amend_env, capsys):
    turn_filing_on(amend_env)
    amend_env.linear.detail = detail()
    assert main(file_argv(amend_env)) == 0
    [(team, title, body, labels, state)] = amend_env.linear.created
    assert (team, title, labels, state) == (
        "t1",
        "Export rounds wrong",
        ("lab-draft",),
        "state-backlog",
    )
    assert "Seen in review." in body and "NF-1" in body
    assert amend_env.linear.related == [("u9", "u1")]
    [w] = reach.writes(amend_env.rt.con, 0.0)
    assert (w["mode"], w["filed"]) == ("on", "NF-9")
    assert "filed NF-9" in capsys.readouterr().out


def test_file_never_sends_the_trigger_label(amend_env):
    turn_filing_on(amend_env)
    amend_env.linear.detail = detail()
    amend_env.linear.labels["claude"] = "lab-claude"
    assert main(file_argv(amend_env)) == 0
    assert all("lab-claude" not in c[3] for c in amend_env.linear.created)


def test_file_without_the_draft_label_in_linear_is_refused(amend_env, capsys):
    turn_filing_on(amend_env)
    amend_env.linear.detail = detail()
    amend_env.linear.labels.clear()
    assert main(file_argv(amend_env)) == 1
    assert "create the cardinal-draft label" in capsys.readouterr().err
    assert amend_env.linear.created == [] and reach.writes(amend_env.rt.con, 0.0) == []


@pytest.mark.parametrize("on", [False, True])
def test_file_past_the_cap_is_refused_in_either_mode(amend_env, on, capsys):
    if on:
        turn_filing_on(amend_env)
    amend_env.linear.detail = detail()
    for at in (1.0, 2.0):
        reach.record_write(amend_env.rt.con, at, "file", "NF-1", "t", mode="observe")
    assert main(file_argv(amend_env)) == 1
    assert "NF-1 already has 2" in capsys.readouterr().err
    assert amend_env.linear.created == []


def test_amend_tests_labels_the_pr_and_tells_the_running_session(amend_env, capsys):
    e = amend_env
    assert main(["--config", str(e.path), "amend-tests", "NF-1"]) == 0
    url = "https://github.com/o/r/pull/7"
    assert e.gh == [(["gh", "pr", "edit", url, "--add-label", "tests-amended"], "ghp_publish")]
    assert [n for n, _ in e.sessions.sent] == ["NF-1"]
    assert "tests-amended" in e.linear.comments[0] and url in e.linear.comments[0]
    assert [line for _, key, line in ledger.pending_digest(e.rt.con) if key == "NF-1"] == [
        f"cardinal added tests-amended to {url}"
    ]
    assert f"added tests-amended to {url}; told the live session" in capsys.readouterr().out


def test_amend_tests_on_a_stopped_run_tells_no_session(amend_env, capsys):
    e = amend_env
    ledger.update_claim(e.rt.con, "NF-1", state="blocked")
    assert main(["--config", str(e.path), "amend-tests", "NF-1"]) == 0
    assert len(e.gh) == 1 and e.sessions.sent == []
    assert "no live session to tell" in capsys.readouterr().out


def test_amend_tests_without_a_claim_is_refused(amend_env, capsys):
    assert main(["--config", str(amend_env.path), "amend-tests", "NF-404"]) == 1
    assert "amend-tests: no claim for NF-404" in capsys.readouterr().err
    assert amend_env.gh == []


def test_amend_tests_without_a_pr_is_refused(amend_env, capsys):
    (amend_env.rt.cfg.state_root / "NF-1" / "pr-url").unlink()
    assert main(["--config", str(amend_env.path), "amend-tests", "NF-1"]) == 1
    assert "amend-tests: NF-1 has no PR yet" in capsys.readouterr().err
    assert amend_env.gh == []


def test_amend_tests_without_a_configured_label_is_refused(amend_env, capsys):
    path = amend_env.path
    path.write_text(path.read_text().replace('amended_label = "tests-amended"\n', ""))
    assert main(["--config", str(amend_env.path), "amend-tests", "NF-1"]) == 1
    assert "backend has no amended_label" in capsys.readouterr().err
    assert amend_env.gh == []


def test_amend_tests_reports_a_failed_label_and_tells_nobody(amend_env, monkeypatch, capsys):
    def fail(argv, **kw):
        raise subprocess.CalledProcessError(1, argv, stderr="label not found")

    monkeypatch.setattr(subprocess, "run", fail)
    assert main(["--config", str(amend_env.path), "amend-tests", "NF-1"]) == 1
    assert "the label was not added: label not found" in capsys.readouterr().err
    assert amend_env.sessions.sent == [] and amend_env.linear.comments == []


def test_shell_records_the_call_before_and_after_it_runs(tmp_path, monkeypatch, capsys):
    from flocc import host

    path = _cfg_file(tmp_path)
    con = ledger.connect(tmp_path / "l.sqlite")
    during = []

    def fake_shell(self, command):
        during.append(host.shell_calls(con, 0.0))
        return {"code": 0, "output": "Filesystem  Size\ntmpfs 2.9G\n"}

    monkeypatch.setattr("flocc.agent.Agent.shell", fake_shell)
    assert main(["--config", str(path), "shell", "--command=df -h /tmp"]) == 0
    assert [(c, code) for _, c, code in during[0]] == [("df -h /tmp", None)]
    assert [(c, code) for _, c, code in host.shell_calls(con, 0.0)] == [("df -h /tmp", 0)]
    assert capsys.readouterr().out == "Filesystem  Size\ntmpfs 2.9G\nexit 0\n"


def test_shell_that_timed_out_exits_1_and_says_so(tmp_path, monkeypatch, capsys):
    path = _cfg_file(tmp_path)
    monkeypatch.setattr("flocc.agent.Agent.shell", lambda self, c: {"code": None, "output": ""})
    assert main(["--config", str(path), "shell", "--command=sleep 999"]) == 1
    assert capsys.readouterr().out == "timed out after 540 s\n"
