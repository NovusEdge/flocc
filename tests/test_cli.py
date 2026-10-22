import argparse
import io
import json
import os
import subprocess
import time
from concurrent.futures import ThreadPoolExecutor

import pytest

from flocc import ledger
from flocc.cli import build_parser, main
from flocc.config import load
from flocc.grants import Grants, set_block
from flocc.linear import LinearError
from flocc.runtime import Runtime
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
        .replace('"~/.local/state/flocc/ledger.sqlite"', f'"{tmp_path / "l.sqlite"}"')
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
        exe="/opt/flocc",
        claude_exe="/opt/claude",
        config_path=path,
        publish_fn=lambda *a: None,
        git_head=lambda p: "sha0",
        review_diff=lambda c: "",
        comment_pr=lambda c, u, b: None,
    )
    monkeypatch.setattr("flocc.tick.build_runtime", lambda cfg, path: rt)
    assert main(["--config", str(path), "release", "NF-1"]) == 0
    assert ledger.get_claim(con, "NF-1") is None and sessions.killed == ["NF-1-test"]
    captured = capsys.readouterr()
    assert "worktree" in captured.err and "Entity not found" in captured.err
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


def test_commit_in_a_review_worktree_is_rejected(origin, tmp_path, monkeypatch, capsys):
    con_path = tmp_path / "hook.sqlite"
    claim = make("NF-1", (), worktree=str(origin.resolve()))
    claim.phase = "review"
    ledger.insert_claim(ledger.connect(con_path), claim)
    monkeypatch.setenv("FLOCC_LEDGER", str(con_path))
    monkeypatch.chdir(origin)
    assert main(["hook", "pre-commit"]) == 1
    assert "read-only" in capsys.readouterr().err
    ledger.update_claim(ledger.connect(con_path), "NF-1", phase="impl")
    assert main(["hook", "pre-commit"]) == 0


@pytest.fixture
def claims_env(tmp_path, monkeypatch):
    config = tmp_path / "c.toml"
    config.write_text(
        EXAMPLE.read_text().replace(
            '[repos.backend]\npath = "~/src/backend"',
            '[repos.backend]\npath = "~/src/backend"\nshared_paths = ["docs/verification.md"]',
        )
    )
    state = tmp_path / "state" / "NF-1"
    state.mkdir(parents=True)
    monkeypatch.setenv("FLOCC_STATE", str(state))
    monkeypatch.setenv("FLOCC_LEDGER", str(tmp_path / "hook.sqlite"))
    monkeypatch.setenv("FLOCC_CONFIG", str(config))
    return ledger.connect(tmp_path / "hook.sqlite")


def test_claim_refuses_a_whole_tree(claims_env, capsys):
    ledger.insert_claim(claims_env, make("NF-1", ()))
    assert main(["claim", "src/a.py", "src/**"]) == 1
    assert "src/** claims a whole tree; claim the files you will edit" in capsys.readouterr().err
    claim = ledger.get_claim(claims_env, "NF-1")
    assert claim and claim.paths == []
    assert main(["claim", "src/*.py"]) == 0
    claim = ledger.get_claim(claims_env, "NF-1")
    assert claim and claim.paths == ["src/*.py"]


def test_claim_refuses_a_glob_that_matches_a_directory(claims_env, tmp_path, capsys):
    wt = tmp_path / "wt"
    (wt / "server/src/api").mkdir(parents=True)
    (wt / "server/src/main.rs").touch()
    ledger.insert_claim(claims_env, make("NF-1", (), worktree=str(wt)))
    for glob, directory in (
        ("server/src", "server/src"),
        ("server/src/", "server/src/"),
        ("server/src/*", "server/src/api"),
        ("server/s?c", "server/src"),
    ):
        assert main(["claim", "docs/a.md", glob]) == 1
        err = capsys.readouterr().err
        assert f"claim: {glob} matches the directory {directory}; claim the files in it" in err
    claim = ledger.get_claim(claims_env, "NF-1")
    assert claim and claim.paths == []
    # Files, globs over files, and a directory that does not exist yet are fine.
    assert main(["claim", "server/src/main.rs", "server/src/*.rs", "server/new/*.rs"]) == 0
    claim = ledger.get_claim(claims_env, "NF-1")
    assert claim and claim.paths == ["server/src/main.rs", "server/src/*.rs", "server/new/*.rs"]


@pytest.mark.parametrize("glob", ["", "/etc/passwd", "../other/a.py", "src/../../a.py"])
def test_claim_refuses_a_glob_outside_the_worktree(claims_env, capsys, glob):
    ledger.insert_claim(claims_env, make("NF-1", ()))
    assert main(["claim", glob]) == 1
    assert "is not a path inside the worktree" in capsys.readouterr().err
    claim = ledger.get_claim(claims_env, "NF-1")
    assert claim and claim.paths == []


def test_claim_skips_a_shared_path(claims_env, capsys):
    ledger.insert_claim(claims_env, make("NF-1", ()))
    assert main(["claim", "docs/verification.md"]) == 0
    assert "docs/verification.md is shared; edit it without claiming" in capsys.readouterr().out
    claim = ledger.get_claim(claims_env, "NF-1")
    assert claim and claim.paths == []
    assert main(["claim", "docs/verification.md", "src/a.py"]) == 0
    claim = ledger.get_claim(claims_env, "NF-1")
    assert claim and claim.paths == ["src/a.py"]


def test_claim_ignores_a_peer_in_another_repo(claims_env, capsys):
    ledger.insert_claim(claims_env, make("NF-1", ()))
    ledger.insert_claim(claims_env, make("NF-2", ("src/a.py",), repo="web"))
    assert main(["claim", "src/a.py"]) == 0
    ledger.insert_claim(claims_env, make("NF-3", ("src/b.py",)))
    assert main(["claim", "src/b.py"]) == 1
    assert "overlaps NF-3" in capsys.readouterr().err


def commit_hook(claims_env, origin, monkeypatch, peer, name):
    ledger.insert_claim(claims_env, make("NF-1", (), worktree=str(origin.resolve())))
    ledger.insert_claim(claims_env, peer)
    (origin / name).parent.mkdir(exist_ok=True)
    (origin / name).write_text("x")
    subprocess.run(["git", "add", name], cwd=origin, check=True)
    monkeypatch.chdir(origin)
    return main(["hook", "pre-commit"])


def test_commit_hook_ignores_a_peer_claim_in_another_repo(claims_env, origin, monkeypatch):
    peer = make("NF-2", ("server/x.py",), repo="web", worktree="/wt/NF-2")
    assert commit_hook(claims_env, origin, monkeypatch, peer, "server/x.py") == 0


def test_commit_hook_still_refuses_a_peer_claim_in_the_same_repo(
    claims_env, origin, monkeypatch, capsys
):
    peer = make("NF-2", ("server/x.py",), worktree="/wt/NF-2")
    assert commit_hook(claims_env, origin, monkeypatch, peer, "server/x.py") == 1
    assert "server/x.py is claimed by NF-2" in capsys.readouterr().err


def test_commit_hook_never_refuses_a_shared_path(claims_env, origin, monkeypatch):
    peer = make("NF-2", ("docs/**",), worktree="/wt/NF-2")
    assert commit_hook(claims_env, origin, monkeypatch, peer, "docs/verification.md") == 0


def test_review_done_rejects_a_missing_or_empty_review(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path))
    assert main(["review-done"]) == 1
    (tmp_path / "review.md").write_text(" \n")
    assert main(["review-done"]) == 1
    assert not (tmp_path / "done.json").exists()
    (tmp_path / "review.md").write_text("Verdict: approve\n")
    assert main(["review-done"]) == 0 and (tmp_path / "done.json").exists()


def gate_env(monkeypatch, tmp_path, phase="review"):
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path))
    monkeypatch.setenv("FLOCC_PHASE", phase)
    monkeypatch.setenv("FLOCC_RUN", f"{phase}@1")
    # The suite may run inside a host session, whose launch grants would leak into the gate.
    monkeypatch.delenv("FLOCC_LAUNCH_GRANTS", raising=False)


def test_gate_command_denies_with_hook_json(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path)
    payload = {"tool_name": "Agent", "tool_input": {"subagent_type": "flocc:source-finder"}}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    assert main(["gate"]) == 0
    out = json.loads(capsys.readouterr().out)
    assert out["hookSpecificOutput"]["permissionDecision"] == "deny"


def test_gate_allows_a_flag_grant_only_for_the_session_launched_with_it(
    tmp_path, monkeypatch, capsys
):
    gate_env(monkeypatch, tmp_path, "impl")
    set_block(tmp_path / "grants.json", Grants(agents=("general-purpose",)))
    payload = json.dumps({"tool_name": "Agent", "tool_input": {"prompt": "p"}})
    monkeypatch.setattr("sys.stdin", io.StringIO(payload))
    assert main(["gate"]) == 0
    assert (
        "next handoff"
        in json.loads(capsys.readouterr().out)["hookSpecificOutput"]["permissionDecisionReason"]
    )
    monkeypatch.setenv("FLOCC_LAUNCH_GRANTS", "agent:general-purpose")
    monkeypatch.setattr("sys.stdin", io.StringIO(payload))
    assert main(["gate"]) == 0
    assert capsys.readouterr().out == ""


def test_a_question_is_never_blocked_by_bookkeeping(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path, "impl")

    def boom(*a, **kw):
        raise OSError("disk gone")

    monkeypatch.setattr("flocc.gate.note_question", boom)
    q = {"tool_name": "AskUserQuestion", "tool_input": {"questions": [{"question": "Q?"}]}}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(q)))
    assert main(["gate"]) == 1
    assert "disk gone" in capsys.readouterr().err


def test_gate_refuses_with_exit_2_when_it_breaks(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path, "impl")

    def boom(*a, **kw):
        raise RuntimeError("disk gone")

    monkeypatch.setattr("flocc.gate.run_gate", boom)
    monkeypatch.setattr("sys.stdin", io.StringIO('{"tool_name": "Agent", "tool_input": {}}'))
    assert main(["gate"]) == 2
    assert "disk gone" in capsys.readouterr().err


def test_gate_without_a_state_dir_refuses(monkeypatch, capsys):
    monkeypatch.delenv("FLOCC_STATE", raising=False)
    monkeypatch.setattr("sys.stdin", io.StringIO('{"tool_name": "Agent", "tool_input": {}}'))
    assert main(["gate"]) == 2


def test_gate_records_and_clears_a_question(tmp_path, monkeypatch):
    gate_env(monkeypatch, tmp_path, "impl")
    q = {"tool_name": "AskUserQuestion", "tool_input": {"questions": [{"question": "Q?"}]}}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(q)))
    assert main(["gate"]) == 0 and (tmp_path / "asking").exists()
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(q)))
    assert main(["gate", "--answered"]) == 0 and not (tmp_path / "asking").exists()


def test_session_start_on_compaction(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setenv("FLOCC_LEDGER", str(tmp_path / "l.sqlite"))
    (tmp_path / "status.json").write_text("{}")
    payload = {"source": "compact", "scratchpad_dir": "/tmp/claude-1000/x/s1/scratchpad"}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    assert main(["session-start"]) == 0
    assert (tmp_path / "compacted").exists() and not (tmp_path / "status.json").exists()
    assert (tmp_path / "scratchpads").read_text().splitlines() == [payload["scratchpad_dir"]]
    assert not (tmp_path / "launches.jsonl").exists(), "compaction is not a new session"
    assert not (tmp_path / "last-stop").exists()


def test_session_start_on_startup_records_a_session(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setenv("FLOCC_LEDGER", str(tmp_path / "l.sqlite"))
    monkeypatch.setattr("sys.stdin", io.StringIO('{"source": "startup"}'))
    assert main(["session-start"]) == 0
    line = json.loads((tmp_path / "launches.jsonl").read_text())
    assert (line["event"], line["phase"], line["run"]) == ("session", "impl", "impl@1")
    assert "## Grants" in capsys.readouterr().out
    assert (tmp_path / "last-stop").exists()


def test_session_start_prints_the_context_when_bookkeeping_fails(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setenv("FLOCC_LEDGER", str(tmp_path / "l.sqlite"))

    def boom(*a, **kw):
        raise OSError("disk gone")

    monkeypatch.setattr("flocc.gate.record_session", boom)
    monkeypatch.setattr("sys.stdin", io.StringIO('{"source": "startup"}'))
    assert main(["session-start"]) == 0
    captured = capsys.readouterr()
    assert "## Grants" in captured.out and "disk gone" in captured.err


@pytest.mark.parametrize("flag", ["--stopped", "--answered"])
def test_bookkeeping_failures_never_exit_2(tmp_path, monkeypatch, capsys, flag):
    gate_env(monkeypatch, tmp_path, "impl")

    def boom(*a, **kw):
        raise OSError("disk gone")

    monkeypatch.setattr("flocc.gate.record_stop", boom)
    monkeypatch.setattr("flocc.gate.clear_question", boom)
    monkeypatch.setattr("sys.stdin", io.StringIO('{"agent_type": "flocc:code-mapper"}'))
    assert main(["gate", flag]) == 1
    err = capsys.readouterr().err
    assert "disk gone" in err and "refused" not in err


def test_idle_records_the_last_message(tmp_path, monkeypatch):
    gate_env(monkeypatch, tmp_path, "impl")
    payload = {"last_assistant_message": "x" * 700, "transcript_path": "/nonexistent"}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    assert main(["idle"]) == 0
    marker = json.loads((tmp_path / "idle").read_text())
    assert marker["message"] == "x" * 600 and marker["at"] > 0
    assert not (tmp_path / "idle.tmp").exists()


def test_idle_falls_back_to_the_transcript(tmp_path, monkeypatch):
    gate_env(monkeypatch, tmp_path, "impl")

    def line(kind, *blocks):
        return json.dumps({"type": kind, "message": {"content": list(blocks)}})

    text = {"type": "text", "text": "please run it for me"}
    transcript = tmp_path / "t.jsonl"
    transcript.write_text(
        "\n".join(
            [
                line("assistant", {"type": "text", "text": "older"}),
                line("assistant", text, {"type": "tool_use", "name": "Bash"}),
                line("user", {"type": "text", "text": "ignored"}),
            ]
        )
    )
    for payload in (
        {"transcript_path": str(transcript)},
        {"last_assistant_message": "", "transcript_path": str(transcript)},
    ):
        monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
        assert main(["idle"]) == 0
        assert json.loads((tmp_path / "idle").read_text())["message"] == "please run it for me"
    monkeypatch.setattr("sys.stdin", io.StringIO("{}"))
    assert main(["idle"]) == 0
    assert json.loads((tmp_path / "idle").read_text())["message"] == ""


def test_idle_bookkeeping_failure_exits_1_never_2(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path, "impl")

    def boom(*a, **kw):
        raise OSError("disk gone")

    monkeypatch.setattr("flocc.gate.note_idle", boom)
    monkeypatch.setattr("sys.stdin", io.StringIO("{}"))
    assert main(["idle"]) == 1
    assert "disk gone" in capsys.readouterr().err


def test_stopped_records_the_agent_type_and_phase(tmp_path, monkeypatch):
    gate_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setattr("sys.stdin", io.StringIO('{"agent_type": "flocc:code-mapper"}'))
    assert main(["gate", "--stopped"]) == 0
    line = json.loads((tmp_path / "launches.jsonl").read_text())
    assert (line["event"], line["what"], line["phase"]) == ("stop", "code-mapper", "impl")


@pytest.mark.parametrize("stdin", ["", "   ", "{not json", "[1, 2]", "null"])
def test_gate_refuses_input_it_cannot_read(tmp_path, monkeypatch, stdin):
    gate_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setattr("sys.stdin", io.StringIO(stdin))
    assert main(["gate"]) == 2


def test_gate_refuses_when_it_cannot_print_the_denial(tmp_path, monkeypatch):
    gate_env(monkeypatch, tmp_path)
    payload = {"tool_name": "Agent", "tool_input": {"subagent_type": "flocc:source-finder"}}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))

    real_print = print

    def broken_stdout(*a, file=None, **kw):
        if file is None:
            raise BrokenPipeError
        real_print(*a, file=file, **kw)

    monkeypatch.setattr("builtins.print", broken_stdout)
    assert main(["gate"]) == 2


def test_context_prints_the_phase_grants(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setenv("FLOCC_LEDGER", str(tmp_path / "l.sqlite"))
    set_block(tmp_path / "grants.json", Grants(launches=12, fix_rounds=2))
    main(["context"])
    out = capsys.readouterr().out
    assert "## Grants" in out and "launches 12" in out and "fix-rounds 2" in out


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


def test_attach_is_read_only_on_the_service_socket_and_says_how_to_leave(host_cfg, capsys):
    with pytest.raises(Exec) as exc:
        main(["--config", str(host_cfg), "attach", "NF-12-review"])
    argv = exc.value.args[0]
    assert argv[:7] == ["tmux", "-L", "flocc", "attach", "-r", "-t", "NF-12-review"]
    assert argv[7:10] == [";", "display-message", "-d"] and "Ctrl-b d" in argv[-1]
    assert "Ctrl-b d detaches" in capsys.readouterr().err


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
    monkeypatch.setattr("flocc.sessions.Sessions", lambda: fake)
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


def test_press_sends_dialog_keys_to_the_issue_session(host_cfg, monkeypatch, capsys):
    fake = _peek_host(host_cfg, monkeypatch, "x")
    assert main(["--config", str(host_cfg), "press", "NF-240", "Down", "Enter", "2"]) == 0
    assert fake.sent == [("NF-240-review", ["Down", "Enter", "2"])]
    assert "pressed Down Enter 2 in NF-240-review" in capsys.readouterr().out


@pytest.mark.parametrize("key", ["C-c", ";", "-", "Enter;kill-server"])
def test_press_refuses_keys_that_could_end_the_session(host_cfg, monkeypatch, capsys, key):
    fake = _peek_host(host_cfg, monkeypatch, "x")
    assert main(["--config", str(host_cfg), "press", "NF-240", key]) == 2
    assert fake.sent == [] and "not a key flocc sends" in capsys.readouterr().err


def test_press_exits_3_without_a_live_session(host_cfg, monkeypatch, capsys):
    _peek_host(host_cfg, monkeypatch, "x", alive=False)
    assert main(["--config", str(host_cfg), "press", "NF-240", "Enter"]) == 3
    assert capsys.readouterr().err == "press: no live session for NF-240\n"


def test_say_types_the_message_and_enter(host_cfg, monkeypatch):
    fake = _peek_host(host_cfg, monkeypatch, "x")
    assert main(["--config", str(host_cfg), "say", "NF-240", "fix", "the", "nit"]) == 0
    assert fake.sent == [("NF-240-review", "fix the nit")]


def test_peek_exits_3_when_the_session_dies_before_the_capture(host_cfg, monkeypatch, capsys):
    fake = _peek_host(host_cfg, monkeypatch, "")
    assert main(["--config", str(host_cfg), "peek", "NF-240"]) == 3
    assert fake.captured
