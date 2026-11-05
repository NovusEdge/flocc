import json
import os
import shlex
import subprocess
from pathlib import Path

import pytest

from flocc import events

ROOT = Path(__file__).parent.parent
PLUGIN = ROOT / "deploy" / "cardinal" / "plugin"
DOWN = '{"link": "down", "detail": "5 connections to the executor VM failed in a row"}'
UP = '{"link": "up"}'


def fake_link(tmp_path, durations):
    """A fake ssh and date: each ssh call moves the clock by its duration, and a call with
    --hello that stays connected (duration > 0) prints what the server says on connect."""
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir(exist_ok=True)
    (tmp_path / "now").write_text("1000\n")
    (tmp_path / "durations").write_text("".join(f"{d}\n" for d in durations))
    (bin_dir / "date").write_text(f'#!/bin/sh\ncat "{tmp_path}/now"\n')
    (bin_dir / "ssh").write_text(
        "#!/bin/sh\n"
        f'echo "$*" >> "{tmp_path}/ssh.log"\n'
        f'n=$(wc -l < "{tmp_path}/ssh.log")\n'
        f'd=$(sed -n "${{n}}p" "{tmp_path}/durations")\n'
        '[ -n "$d" ] || d=0\n'
        f'echo $(($(cat "{tmp_path}/now") + d)) > "{tmp_path}/now"\n'
        'if [ "$d" -gt 0 ]; then case "$*" in *--hello*) echo \'{"link": "up"}\';; esac; fi\n'
        "exit 255\n"
    )
    for name in ("date", "ssh"):
        (bin_dir / name).chmod(0o755)
    return {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}"}


def run_follow(env, rounds, up_s="60"):
    env = {
        **env,
        "FOLLOW_SSH_CONFIG": "/dev/null",
        "FOLLOW_PAUSE_S": "0",
        "FOLLOW_UP_S": up_s,
        "FOLLOW_ROUNDS": str(rounds),
    }
    out = subprocess.run(
        [PLUGIN / "bin" / "follow"],
        env=env,
        capture_output=True,
        text=True,
        timeout=20,
        check=False,
    )
    return out.stdout.splitlines()


def calls(tmp_path):
    return (tmp_path / "ssh.log").read_text().splitlines()


def test_the_plugin_declares_one_always_on_monitor():
    meta = json.loads((PLUGIN / ".claude-plugin" / "plugin.json").read_text())
    [mon] = json.loads((PLUGIN / "monitors" / "monitors.json").read_text())
    assert meta["name"] == "cardinal"
    assert mon["when"] == "always" and mon["command"] == '"${CLAUDE_PLUGIN_ROOT}"/bin/follow'


def test_unreachable_prints_link_down_once_and_keeps_trying(tmp_path):
    env = fake_link(tmp_path, [0] * 12)
    assert run_follow(env, 12) == [DOWN]
    sent = calls(tmp_path)
    assert [c.endswith("--hello") for c in sent] == [False] * 5 + [True] * 7


def test_a_failed_hello_is_retried_until_one_connects(tmp_path):
    env = fake_link(tmp_path, [0] * 7 + [100])
    assert run_follow(env, 8) == [DOWN, UP]
    assert [c.endswith("--hello") for c in calls(tmp_path)] == [False] * 5 + [True] * 3


def test_a_flapping_link_wakes_cardinal_twice_per_down_period(tmp_path):
    env = fake_link(tmp_path, [0] * 5 + [1] * 10)
    assert run_follow(env, 15) == [DOWN, UP]
    assert [c.endswith("--hello") for c in calls(tmp_path)].count(True) == 1


def test_a_connection_that_stayed_up_rearms_link_down(tmp_path):
    env = fake_link(tmp_path, [0] * 5 + [100] + [0] * 5)
    assert run_follow(env, 11) == [DOWN, UP, DOWN]


def test_follow_passes_the_server_lines_through(tmp_path):
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()
    (bin_dir / "ssh").write_text("#!/bin/sh\necho '{\"events\": []}'\nexit 0\n")
    (bin_dir / "ssh").chmod(0o755)
    env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}"}
    assert run_follow(env, 2, up_s="0") == ['{"events": []}', '{"events": []}']


def test_the_executor_wrapper_quotes_every_argument(tmp_path):
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()
    ssh = bin_dir / "ssh"
    ssh.write_text('#!/bin/sh\nfor last; do :; done\nprintf "%s" "$last"\n')
    ssh.chmod(0o755)
    env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}"}
    args = ["resume", "NF-3", "--note", "it's $(id) `x`\nline two; -- --config=/x"]
    out = subprocess.run(
        [PLUGIN / "bin" / "executor", *args], env=env, capture_output=True, text=True, check=True
    )
    assert shlex.split(out.stdout) == args


CARDINAL = ROOT / "deploy" / "cardinal"
EXECUTOR = "/opt/cardinal/plugin/bin/executor"
AGENT_SHELL = "/opt/cardinal/plugin/bin/agent-shell"
HOOK = PLUGIN / "bin" / "only-executor"


def fake_ssh(tmp_path, body='for last; do :; done\nprintf "%s" "$last"\n'):
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir(exist_ok=True)
    ssh = bin_dir / "ssh"
    ssh.write_text("#!/bin/sh\n" + body)
    ssh.chmod(0o755)
    return {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}"}


def hook(raw: str):
    return subprocess.run([HOOK], input=raw, capture_output=True, text=True, check=False)


def bash_call(command: str) -> str:
    return json.dumps({"tool_name": "Bash", "tool_input": {"command": command}})


def test_four_skills_each_with_a_name_and_a_description():
    names = []
    for skill in sorted((PLUGIN / "skills").glob("*/SKILL.md")):
        head = skill.read_text().split("---")[1]
        fields = dict(line.split(": ", 1) for line in head.strip().splitlines())
        assert fields["name"] == skill.parent.name and fields["description"].startswith("Use when")
        names.append(fields["name"])
    assert names == ["digest", "dispatch", "status", "triage"]


def test_the_skills_reach_the_executor_only_through_the_wrapper():
    for skill in (PLUGIN / "skills").glob("*/SKILL.md"):
        text = skill.read_text()
        assert EXECUTOR in text and "ssh " not in text


def test_the_triage_skill_covers_every_event_kind_and_the_link():
    text = (PLUGIN / "skills" / "triage" / "SKILL.md").read_text()
    for kind in sorted(events.KINDS):
        assert f"`{kind}`" in text
    assert '"link"' in text and f"{EXECUTOR} ack" in text and f"{EXECUTOR} release" not in text


def test_the_triage_skill_repairs_host_pressure_with_the_verbs_first():
    text = (PLUGIN / "skills" / "triage" / "SKILL.md").read_text()
    steps = [f"{EXECUTOR} host", f"{EXECUTOR} cleanup --now", f"{EXECUTOR} stop-tests KEY"]
    at = [text.index(s) for s in steps] + [text.index(f"{AGENT_SHELL} 'COMMAND'")]
    assert at == sorted(at)


def test_the_digest_lists_every_agent_shell_call():
    assert "agent-shell" in (PLUGIN / "skills" / "digest" / "SKILL.md").read_text()


def test_the_triage_skill_sets_a_long_timeout_for_the_slow_verbs():
    text = (PLUGIN / "skills" / "triage" / "SKILL.md").read_text()
    assert "600000 ms" in text
    assert all(verb in text for verb in ("cleanup --now", "stop-tests", "agent-shell"))


def test_agent_shell_times_out_before_the_two_minute_tool_timeout():
    from flocc import agentrun

    assert agentrun.SHELL_TIMEOUT_S < 120


def test_dispatch_restarts_work_only_through_resume():
    text = (PLUGIN / "skills" / "dispatch" / "SKILL.md").read_text()
    assert "lifts a hold and nothing else" in text and f"{EXECUTOR} resume KEY" in text
    assert "fresh" in text and "Todo" in text


def test_triage_resumes_and_answers_with_the_resume_verb():
    text = (PLUGIN / "skills" / "triage" / "SKILL.md").read_text()
    assert f"{EXECUTOR} resume KEY --note" in text and f"{EXECUTOR} fix" not in text
    assert "`needs-owner`" in text and "acceptance criteria" in text
    for owner_stop in ("tests-touched", "branch-moved", "publish-failed", "rebase-conflict"):
        assert owner_stop in text


def test_managed_settings_allow_the_two_binaries_and_hook_every_bash_call():
    s = json.loads((CARDINAL / "managed-settings.json").read_text())
    p = s["permissions"]
    assert p["defaultMode"] == "default"
    assert p["allow"] == [f"Bash({EXECUTOR}:*)", f"Bash({AGENT_SHELL}:*)", "Skill"]
    assert "ask" not in p
    assert p["deny"] == [
        "Read",
        "Glob",
        "Grep",
        "WebFetch",
        "WebSearch",
        "Edit",
        "Write",
        "NotebookEdit",
        "mcp__*",
        "Monitor",
    ]
    assert p["disableBypassPermissionsMode"] == "disable"
    command = {"type": "command", "command": "/opt/cardinal/plugin/bin/only-executor"}
    assert s["hooks"]["PreToolUse"] == [{"matcher": "Bash", "hooks": [command]}]


def test_the_unit_runs_sonnet_in_the_empty_session_directory():
    unit = (CARDINAL / "cardinal.service").read_text()
    for part in (
        "-c /var/lib/flocc-cardinal/session",
        "--model claude-opus-5-5 --effort medium",
        "--remote-control cardinal",
        "--plugin-dir /opt/cardinal/plugin",
        "{{executor}}",
    ):
        assert part in unit
    assert "--permission-mode" not in unit
    assert "%" not in unit.replace("%h", "")
    assert "OnCalendar=*-*-* {{hour}}:00:00" in (CARDINAL / "cardinal-renew.timer").read_text()


def test_the_executor_wrapper_refuses_shell_before_ssh(tmp_path):
    env = fake_ssh(tmp_path, f'touch "{tmp_path}/ssh-ran"\n')
    out = subprocess.run(
        [PLUGIN / "bin" / "executor", "shell", "id"],
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    assert out.returncode == 2 and "agent-shell" in out.stderr
    assert not (tmp_path / "ssh-ran").exists()


def test_agent_shell_sends_one_quoted_shell_request(tmp_path):
    env = fake_ssh(tmp_path)
    command = "du -sh '/tmp/it''s here' $(id)"
    out = subprocess.run(
        [PLUGIN / "bin" / "agent-shell", command],
        env=env,
        capture_output=True,
        text=True,
        check=True,
    )
    assert shlex.split(out.stdout) == ["shell", command]
    two = subprocess.run(
        [PLUGIN / "bin" / "agent-shell", "a", "b"],
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    assert two.returncode == 2


@pytest.mark.parametrize(
    "command",
    [
        f"{EXECUTOR} status",
        f"{EXECUTOR} resume NF-3 --note 'the import moved to flocc.events, update it'",
        f"{AGENT_SHELL} 'du -sh /tmp/pytest-of-flocc-agent'",
        EXECUTOR,
    ],
)
def test_the_hook_passes_the_two_wrappers(command):
    assert hook(bash_call(command)).returncode == 0


@pytest.mark.parametrize(
    "command",
    [
        "id",
        "cat /etc/flocc-cardinal/ssh_config",
        f" {EXECUTOR} status",
        f"FOO=1 {EXECUTOR} status",
        f"{EXECUTOR}-x status",
        f"{EXECUTOR}/../../../../bin/sh",
        "/opt/cardinal/plugin/bin/follow",
        f"sh {EXECUTOR} status",
    ],
)
def test_the_hook_refuses_any_other_first_word(command):
    out = hook(bash_call(command))
    assert out.returncode == 2 and "only-executor" in out.stderr


@pytest.mark.parametrize("op", [";", "&", "|", "`id`", "$(id)", "\n", "<", ">", "(", ")"])
def test_the_hook_refuses_a_control_operator_outside_quotes(op):
    out = hook(bash_call(f"{EXECUTOR} status {op} id"))
    assert out.returncode == 2 and "only-executor" in out.stderr


@pytest.mark.parametrize("comment", ["#'\nid #'", '#"\nid #"', "#"])
def test_the_hook_refuses_a_comment_that_hides_a_quote_from_it(comment):
    out = hook(bash_call(f"{EXECUTOR} status {comment}"))
    assert out.returncode == 2 and "only-executor" in out.stderr


@pytest.mark.parametrize("text", ["$(id)", "`id`", "$HOME", "${x}"])
def test_the_hook_refuses_expansion_inside_double_quotes(text):
    out = hook(bash_call(f'{EXECUTOR} resume NF-3 --note "see {text}"'))
    assert out.returncode == 2 and "only-executor" in out.stderr


def test_the_hook_passes_operators_inside_single_quotes():
    note = "line one; a | b & c < d > e $(f) `g`\nline two"
    assert hook(bash_call(f"{EXECUTOR} resume NF-3 --note {shlex.quote(note)}")).returncode == 0
    piped = f"{AGENT_SHELL} {shlex.quote('du -sh /tmp/* | sort -h | tail -5')}"
    assert hook(bash_call(piped)).returncode == 0


def test_the_hook_passes_plain_text_inside_double_quotes():
    assert (
        hook(bash_call(f'{EXECUTOR} hold NF-3 --reason "host memory; see digest"')).returncode == 0
    )


def test_the_hook_fails_closed_on_input_it_cannot_read():
    assert hook("not json").returncode == 2
    assert hook(bash_call(f"{EXECUTOR} resume NF-3 --note 'unclosed")).returncode == 2
