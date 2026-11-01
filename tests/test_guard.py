import io
import json

import pytest

from flocc.cli import main
from flocc.guard import KEEP_MOVING, NO_APPROVER, decide, own_command


def bash(command):
    return {"tool_name": "Bash", "tool_input": {"command": command}}


@pytest.mark.parametrize("tool", ["AskUserQuestion", "EnterPlanMode", "ExitPlanMode"])
def test_dialog_tools_are_denied_with_the_way_forward(tool):
    reason = decide({"tool_name": tool, "tool_input": {}}, "impl", "/s")
    assert reason is not None and KEEP_MOVING in reason and "Needs the owner" in reason


@pytest.mark.parametrize(
    "command",
    [
        "git push origin HEAD",
        "cd x && git -C . push",
        "git -c core.x=y push --force",
        "echo ok; git push",
        "gh pr create --fill",
        "gh pr merge 3",
        "gh issue comment 4 -b hi",
        "gh api -X POST repos/o/r/issues",
        "gh api --method=PATCH repos/o/r",
        "gh api repos/o/r/issues -f title=x",
        "gh api repos/o/r/issues --raw-field title=x",
        "gh api graphql -F query=@q.graphql",
        "GH_TOKEN=x gh release create v1",
    ],
)
def test_pushes_and_github_writes_are_denied(command):
    assert decide(bash(command), "impl", "/s") is not None


@pytest.mark.parametrize(
    "command",
    [
        "git status && git log --oneline",
        "git commit -m 'x' -- a.py",
        "gh pr view 3 --json state",
        "gh pr diff 3",
        "gh api repos/o/r/pulls/3",
        "gh api -X GET search/issues -f q=repo:o/r",
        "gh search code foo",
        "just check",
        "echo 'git push is denied'",
    ],
)
def test_reads_and_ordinary_work_pass(command):
    assert decide(bash(command), "impl", "/s") is None


def test_gh_api_fields_without_a_method_are_a_post():
    assert decide(bash("gh api repos/o/r/issues -f title=x"), "impl", "/s") is not None
    assert decide(bash("gh api -X GET repos/o/r/issues -f state=open"), "impl", "/s") is None


def test_linear_mcp_tools_are_denied():
    payload = {"tool_name": "mcp__linear-nimblefox__save_issue", "tool_input": {}}
    assert decide(payload, "impl", "/s") is not None
    assert decide({"tool_name": "mcp__sentry__search", "tool_input": {}}, "impl", "/s") is None


def test_the_reviewer_may_write_only_in_its_state_directory(tmp_path):
    state = str(tmp_path / "state" / "NF-1")
    inside = {"tool_name": "Write", "tool_input": {"file_path": f"{state}/review.md"}}
    outside = {"tool_name": "Edit", "tool_input": {"file_path": str(tmp_path / "wt" / "a.py")}}
    sneaky = {"tool_name": "Write", "tool_input": {"file_path": f"{state}/../../wt/a.py"}}
    assert decide(inside, "review", state) is None
    assert decide(outside, "review", state) is not None
    assert decide(sneaky, "review", state) is not None
    assert decide(outside, "impl", state) is None


def test_the_reviewer_may_not_commit():
    assert decide(bash("git commit -am x"), "review", "/s") is not None
    assert decide(bash("flocc commit -m x -- a.py"), "review", "/s") is not None
    assert decide(bash("git log"), "review", "/s") is None


def test_guard_prints_the_deny_decision(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("FLOCC_PHASE", "impl")
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path))
    payload = {"tool_name": "AskUserQuestion", "tool_input": {"questions": []}}
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(payload)))
    assert main(["guard"]) == 0
    out = json.loads(capsys.readouterr().out)["hookSpecificOutput"]
    assert out["hookEventName"] == "PreToolUse" and out["permissionDecision"] == "deny"
    assert KEEP_MOVING in out["permissionDecisionReason"]


def test_permission_deny_denies_a_sample_and_garbage(monkeypatch, capsys):
    monkeypatch.setenv("FLOCC_PHASE", "impl")
    sample = {"tool_name": "Bash", "tool_input": {"command": "cat x"}}
    for raw in (json.dumps(sample), "not json", ""):
        monkeypatch.setattr("sys.stdin", io.StringIO(raw))
        assert main(["permission-deny"]) == 0
        out = json.loads(capsys.readouterr().out)["hookSpecificOutput"]
        assert out["hookEventName"] == "PermissionRequest"
        assert out["decision"] == {"behavior": "deny", "message": NO_APPROVER}
    monkeypatch.delenv("FLOCC_PHASE")
    assert main(["permission-deny"]) == 0
    assert capsys.readouterr().out == ""


@pytest.mark.parametrize(
    "command",
    [
        "flocc commit -m 'chore(docket): d4 and d5' -- .docket/ledger.jsonl .docket/features.jsonl",
        "cd /w/NF-1 && flocc commit -m 'x' -- server/src .docket 2>&1 | tail -5",
        "cd /w/NF-1 && /opt/flocc/bin/flocc done --summary 'records `d4` in .docket/ledger.jsonl'",
        'flocc done --summary "kept .docket" > /dev/null',
        "flocc done --summary 'line one\nline two in .docket' 2>&1 | tail -n 5",
    ],
)
def test_own_command_lets_flocc_commit_and_done_through(command):
    assert own_command(command)


@pytest.mark.parametrize(
    "command",
    [
        "cat .docket/ledger.jsonl",
        "flocc commit -m x -- a; echo y >> .docket/ledger.jsonl",
        "flocc commit -m x -- a > .docket/ledger.jsonl",
        'flocc commit -m "`rm -rf .docket`" -- a',
        'flocc commit -m "$(touch .docket/ledger.jsonl)" -- a',
        "flocc commit -m x -- a | tee .docket/ledger.jsonl",
        "flocc done --summary x\nrm -f .docket/ledger.jsonl",
        "flocc done --summary x\r\nrm -f .docket/ledger.jsonl",
        "flocc commit -m x -- a >&1.docket/ledger.jsonl",
        "flocc commit -m x -- a >/dev/null.docket",
        "flocc status",
        "flocc done --summary 'unclosed",
        "cd x",
        "",
    ],
)
def test_own_command_refuses_anything_else(command):
    assert not own_command(command)


def test_permission_deny_allows_flocc_commit(monkeypatch, capsys):
    monkeypatch.setenv("FLOCC_PHASE", "impl")
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(bash("flocc commit -m x -- .docket"))))
    assert main(["permission-deny"]) == 0
    out = json.loads(capsys.readouterr().out)["hookSpecificOutput"]
    assert out["decision"] == {"behavior": "allow"}


def test_guard_prints_nothing_for_an_allowed_call_or_bad_input(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("FLOCC_PHASE", "impl")
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path))
    for raw in (json.dumps(bash("ls")), "not json"):
        monkeypatch.setattr("sys.stdin", io.StringIO(raw))
        assert main(["guard"]) == 0
        assert capsys.readouterr().out == ""
    monkeypatch.delenv("FLOCC_PHASE")
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps({"tool_name": "AskUserQuestion"})))
    assert main(["guard"]) == 0
    assert capsys.readouterr().out == ""
