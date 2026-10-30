import json
import os
import shlex
import subprocess
from pathlib import Path

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
    args = ["fix", "NF-3", "--note", "it's $(id) `x`\nline two; -- --config=/x"]
    out = subprocess.run(
        [PLUGIN / "bin" / "executor", *args], env=env, capture_output=True, text=True, check=True
    )
    assert shlex.split(out.stdout) == args


CARDINAL = ROOT / "deploy" / "cardinal"
EXECUTOR = "/opt/cardinal/plugin/bin/executor"


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
    for kind in ("ended", "cap", "base-broken", "bad-sequence", "service-error", "daily"):
        assert f"`{kind}`" in text
    assert '"link"' in text and f"{EXECUTOR} ack" in text and "release" not in text


def test_the_dispatch_skill_never_reruns_work():
    text = (PLUGIN / "skills" / "dispatch" / "SKILL.md").read_text()
    assert "lifts a hold and nothing else" in text and "tell the owner" in text


def test_managed_settings_allow_the_wrapper_and_skills_and_deny_reads():
    s = json.loads((CARDINAL / "managed-settings.json").read_text())["permissions"]
    assert s["defaultMode"] == "dontAsk"
    assert s["allow"] == [f"Bash({EXECUTOR}:*)", "Skill"]
    assert s["deny"] == ["Read", "Glob", "Grep", "WebFetch", "WebSearch", "Edit", "Write"]
    assert s["disableBypassPermissionsMode"] == "disable"


def test_the_unit_runs_sonnet_in_the_empty_session_directory():
    unit = (CARDINAL / "cardinal.service").read_text()
    for part in (
        "-c /var/lib/flocc-cardinal/session",
        "--model sonnet",
        "--remote-control cardinal",
        "--permission-mode dontAsk",
        "--plugin-dir /opt/cardinal/plugin",
        "{{executor}}",
    ):
        assert part in unit
    assert "%" not in unit.replace("%h", "")
    assert "OnCalendar=*-*-* {{hour}}:00:00" in (CARDINAL / "cardinal-renew.timer").read_text()
