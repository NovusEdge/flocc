import io
import json
from concurrent.futures import ThreadPoolExecutor

from flocc.cli import main
from flocc.permission import LOG, handle

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
