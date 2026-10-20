import io
import json
import subprocess
from concurrent.futures import ThreadPoolExecutor

import pytest

from linear_agents import ledger
from linear_agents.cli import main
from linear_agents.config import load
from linear_agents.grants import Grants, set_block
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
    monkeypatch.setenv("LINEAR_AGENTS_LEDGER", str(con_path))
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
    monkeypatch.setenv("LINEAR_AGENTS_STATE", str(state))
    monkeypatch.setenv("LINEAR_AGENTS_LEDGER", str(tmp_path / "hook.sqlite"))
    monkeypatch.setenv("LINEAR_AGENTS_CONFIG", str(config))
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
    monkeypatch.setenv("LINEAR_AGENTS_STATE", str(tmp_path))
    assert main(["review-done"]) == 1
    (tmp_path / "review.md").write_text(" \n")
    assert main(["review-done"]) == 1
    assert not (tmp_path / "done.json").exists()
    (tmp_path / "review.md").write_text("Verdict: approve\n")
    assert main(["review-done"]) == 0 and (tmp_path / "done.json").exists()


def gate_env(monkeypatch, tmp_path, phase="review"):
    monkeypatch.setenv("LINEAR_AGENTS_STATE", str(tmp_path))
    monkeypatch.setenv("LINEAR_AGENTS_PHASE", phase)
    monkeypatch.setenv("LINEAR_AGENTS_RUN", f"{phase}@1")
    # The suite may run inside a host session, whose launch grants would leak into the gate.
    monkeypatch.delenv("LINEAR_AGENTS_LAUNCH_GRANTS", raising=False)


def test_gate_command_denies_with_hook_json(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path)
    payload = {"tool_name": "Agent", "tool_input": {"subagent_type": "linear-agents:source-finder"}}
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
    monkeypatch.setenv("LINEAR_AGENTS_LAUNCH_GRANTS", "agent:general-purpose")
    monkeypatch.setattr("sys.stdin", io.StringIO(payload))
    assert main(["gate"]) == 0
    assert capsys.readouterr().out == ""


def test_a_question_is_never_blocked_by_bookkeeping(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path, "impl")

    def boom(*a, **kw):
        raise OSError("disk gone")

    monkeypatch.setattr("linear_agents.gate.note_question", boom)
    q = {"tool_name": "AskUserQuestion", "tool_input": {"questions": [{"question": "Q?"}]}}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(q)))
    assert main(["gate"]) == 1
    assert "disk gone" in capsys.readouterr().err


def test_gate_refuses_with_exit_2_when_it_breaks(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path, "impl")

    def boom(*a, **kw):
        raise RuntimeError("disk gone")

    monkeypatch.setattr("linear_agents.gate.run_gate", boom)
    monkeypatch.setattr("sys.stdin", io.StringIO('{"tool_name": "Agent", "tool_input": {}}'))
    assert main(["gate"]) == 2
    assert "disk gone" in capsys.readouterr().err


def test_gate_without_a_state_dir_refuses(monkeypatch, capsys):
    monkeypatch.delenv("LINEAR_AGENTS_STATE", raising=False)
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
    monkeypatch.setenv("LINEAR_AGENTS_LEDGER", str(tmp_path / "l.sqlite"))
    (tmp_path / "status.json").write_text("{}")
    payload = {"source": "compact", "scratchpad_dir": "/tmp/claude-1000/x/s1/scratchpad"}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    assert main(["session-start"]) == 0
    assert (tmp_path / "compacted").exists() and not (tmp_path / "status.json").exists()
    assert (tmp_path / "scratchpads").read_text().splitlines() == [payload["scratchpad_dir"]]
    assert not (tmp_path / "launches.jsonl").exists(), "compaction is not a new session"


def test_session_start_on_startup_records_a_session(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setenv("LINEAR_AGENTS_LEDGER", str(tmp_path / "l.sqlite"))
    monkeypatch.setattr("sys.stdin", io.StringIO('{"source": "startup"}'))
    assert main(["session-start"]) == 0
    line = json.loads((tmp_path / "launches.jsonl").read_text())
    assert (line["event"], line["phase"], line["run"]) == ("session", "impl", "impl@1")
    assert "## Grants" in capsys.readouterr().out


def test_session_start_prints_the_context_when_bookkeeping_fails(tmp_path, monkeypatch, capsys):
    gate_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setenv("LINEAR_AGENTS_LEDGER", str(tmp_path / "l.sqlite"))

    def boom(*a, **kw):
        raise OSError("disk gone")

    monkeypatch.setattr("linear_agents.gate.record_session", boom)
    monkeypatch.setattr("sys.stdin", io.StringIO('{"source": "startup"}'))
    assert main(["session-start"]) == 0
    captured = capsys.readouterr()
    assert "## Grants" in captured.out and "disk gone" in captured.err


@pytest.mark.parametrize("flag", ["--stopped", "--answered"])
def test_bookkeeping_failures_never_exit_2(tmp_path, monkeypatch, capsys, flag):
    gate_env(monkeypatch, tmp_path, "impl")

    def boom(*a, **kw):
        raise OSError("disk gone")

    monkeypatch.setattr("linear_agents.gate.record_stop", boom)
    monkeypatch.setattr("linear_agents.gate.clear_question", boom)
    monkeypatch.setattr("sys.stdin", io.StringIO('{"agent_type": "linear-agents:code-mapper"}'))
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

    monkeypatch.setattr("linear_agents.gate.note_idle", boom)
    monkeypatch.setattr("sys.stdin", io.StringIO("{}"))
    assert main(["idle"]) == 1
    assert "disk gone" in capsys.readouterr().err


def test_stopped_records_the_agent_type_and_phase(tmp_path, monkeypatch):
    gate_env(monkeypatch, tmp_path, "impl")
    monkeypatch.setattr("sys.stdin", io.StringIO('{"agent_type": "linear-agents:code-mapper"}'))
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
    payload = {"tool_name": "Agent", "tool_input": {"subagent_type": "linear-agents:source-finder"}}
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
    monkeypatch.setenv("LINEAR_AGENTS_LEDGER", str(tmp_path / "l.sqlite"))
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
