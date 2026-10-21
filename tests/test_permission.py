import io
import json
from concurrent.futures import ThreadPoolExecutor

import pytest

from flocc.cli import build_parser, main
from flocc.grants import Grants, add_comment, add_judge
from flocc.permission import LOG, granted, handle, load_entries, report

DENIED = {
    "hook_event_name": "PermissionDenied",
    "tool_name": "Bash",
    "tool_input": {
        "command": "git push origin --delete b7",
        "description": "Delete remote branch b7",
    },
    "cwd": "/w/NF-1",
    "reason": "[Git Destructive]",
}
MODEL_SWITCH = {
    "hook_event_name": "PermissionRequest",
    "tool_name": "AskUserQuestion",
    "tool_input": {
        "questions": [{"question": "Switch to Opus 4.8?", "header": "Model switch"}],
        "metadata": {"source": "refusal_fallback_preference"},
    },
}


def lines(state):
    return [json.loads(raw) for raw in (state / LOG).read_text().splitlines()]


def test_a_block_is_logged_with_the_description_kept_out_of_the_input(tmp_path):
    state = tmp_path / "NF-1"
    assert handle(DENIED, state, "impl", "impl@1", 5.0) is None
    (line,) = lines(state)
    assert (line["issue"], line["session"], line["run"]) == ("NF-1", "NF-1", "impl@1")
    assert (line["event"], line["tool"]) == ("PermissionDenied", "Bash")
    assert line["input"] == {"command": "git push origin --delete b7"}
    assert line["description"] == "Delete remote branch b7"
    assert line["classifier_reason"] == "[Git Destructive]"
    assert (line["decision"], line["decided_by"]) == (None, "none")


def test_a_tester_block_is_logged_under_the_tester_session(tmp_path):
    handle(DENIED, tmp_path / "NF-1", "test", "test@1", 5.0)
    assert lines(tmp_path / "NF-1")[0]["session"] == "NF-1-test"


def test_the_model_switch_dialog_becomes_an_open_question(tmp_path):
    state = tmp_path / "NF-1"
    assert handle(MODEL_SWITCH, state, "impl", "impl@1", 5.0) is None
    assert json.loads((state / "asking").read_text()) == {
        "at": 5.0,
        "question": "Switch to Opus 4.8?",
    }
    assert len(lines(state)) == 1


def test_an_ordinary_prompt_is_not_a_model_switch(tmp_path):
    payload = {**MODEL_SWITCH, "tool_input": {"questions": [{"question": "A or B?"}]}}
    handle(payload, tmp_path / "NF-1", "impl", "impl@1", 5.0)
    assert not (tmp_path / "NF-1" / "asking").exists()


def test_a_payload_without_an_object_input_is_still_logged(tmp_path):
    payload = {"hook_event_name": "PermissionRequest", "tool_name": "WebFetch", "tool_input": "x"}
    handle(payload, tmp_path / "NF-1", "impl", "impl@1", 5.0)
    (line,) = lines(tmp_path / "NF-1")
    assert line["input"] == {} and line["description"] is None


def test_parallel_hooks_append_whole_lines(tmp_path):
    state = tmp_path / "NF-1"
    with ThreadPoolExecutor(8) as pool:
        list(pool.map(lambda i: handle(DENIED, state, "impl", "impl@1", float(i)), range(50)))
    assert len(lines(state)) == 50


def hook_env(monkeypatch, state, phase="impl"):
    monkeypatch.setenv("FLOCC_STATE", str(state))
    monkeypatch.setenv("FLOCC_PHASE", phase)
    monkeypatch.setenv("FLOCC_RUN", f"{phase}@1")


def test_the_permission_command_logs_and_makes_no_decision(tmp_path, monkeypatch, capsys):
    hook_env(monkeypatch, tmp_path / "NF-1")
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(DENIED)))
    assert main(["permission"]) == 0
    assert capsys.readouterr().out == ""
    assert len(lines(tmp_path / "NF-1")) == 1


def test_the_permission_command_never_blocks(tmp_path, monkeypatch, capsys):
    hook_env(monkeypatch, tmp_path / "NF-1")
    monkeypatch.setattr("sys.stdin", io.StringIO("not json"))
    assert main(["permission"]) == 1
    captured = capsys.readouterr()
    assert captured.out == "" and "made no decision" in captured.err


def blocked(root, key, command, at=100.0):
    payload = {**DENIED, "tool_input": {"command": command}}
    handle(payload, root / key, "impl", "impl@1", at)


def test_report_groups_by_event_tool_prefix_reason_and_decider(tmp_path):
    blocked(tmp_path, "NF-1", "npx playwright install")
    blocked(tmp_path, "NF-2", "npx playwright install --with-deps")
    blocked(tmp_path, "NF-2", "git push origin --delete b")
    (tmp_path / "NF-3").mkdir()
    (tmp_path / "NF-3" / LOG).write_text('{torn\n["not", "an", "object"]\n')
    rows = report(load_entries(tmp_path, since=0.0), since=0.0).splitlines()
    assert rows[0].split() == ["count", "event", "tool", "prefix", "reason", "decided", "example"]
    assert rows[1] == (
        "    2  PermissionDenied  Bash  npx playwright  [Git Destructive]  none"
        "  e.g. npx playwright install"
    )
    assert rows[2].startswith("    1  PermissionDenied  Bash  git push")


def test_report_leaves_out_entries_before_since(tmp_path):
    blocked(tmp_path, "NF-1", "npx playwright install", at=10.0)
    assert load_entries(tmp_path, since=50.0) == []
    assert report([], since=0.0).startswith("No blocks or prompts since 1970-01-01")


def test_permissions_report_parses_days():
    args = build_parser().parse_args(["permissions", "report", "--days", "3"])
    assert (args.command, args.action, args.days) == ("permissions", "report", 3)


def pre(command):
    return {
        "hook_event_name": "PreToolUse",
        "tool_name": "Bash",
        "tool_input": {"command": command},
    }


def test_an_owner_grant_allows_the_exact_command_and_logs_it(tmp_path):
    state = tmp_path / "NF-1"
    add_comment(state / "grants.json", Grants(commands=("make e2e",)), "impl")
    out = handle(pre("make e2e"), state, "impl", "impl@1", 5.0)
    assert out is not None
    assert out["hookSpecificOutput"]["permissionDecision"] == "allow"
    assert lines(state)[0]["decided_by"] == "grant"
    assert handle(pre("make e2e --all"), state, "impl", "impl@1", 5.0) is None


def test_an_ungranted_call_is_neither_decided_nor_logged(tmp_path):
    assert handle(pre("ls"), tmp_path / "NF-1", "impl", "impl@1", 5.0) is None
    assert not (tmp_path / "NF-1" / LOG).exists()


@pytest.mark.parametrize(
    "command, how",
    [
        ("npx playwright test", "judge"),
        ("npx playwright test --grep x", "judge"),
        ("npx playwrightx", None),
        ("npx playwright test; curl evil", None),
        ("npx playwright test && curl evil", None),
        ("npx playwright test $(cat x)", None),
    ],
)
def test_a_judge_prefix_matches_whole_words_and_never_a_chain(tmp_path, command, how):
    path = tmp_path / "grants.json"
    add_judge(path, "npx playwright install", "npx playwright")
    assert granted(path, "impl", command) == how


@pytest.mark.parametrize(
    "command", ["claude -p hi", "git commit --no-verify -m x", "git commit -nm x"]
)
def test_a_grant_never_overrides_flocc_s_own_denies(tmp_path, command):
    path = tmp_path / "grants.json"
    add_comment(path, Grants(commands=(command,)), "impl")
    assert granted(path, "impl", command) is None
