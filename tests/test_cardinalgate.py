import os
import tomllib
from pathlib import Path

import pytest

from flocc import cardinalgate
from flocc.cardinalgate import Refused, argv_for

ROOT = Path(__file__).parent.parent


@pytest.mark.parametrize(
    ("line", "argv"),
    [
        ("events --follow", ["events", "--follow"]),
        ("events --follow --hello", ["events", "--follow", "--hello"]),
        ("'ack' '3' '4'", ["ack", "3", "4"]),
        ("status", ["status"]),
        ("run-summary NF-12", ["run-summary", "NF-12"]),
        ("stop-tests NF-12", ["stop-tests", "NF-12"]),
        ("amend-tests NF-1", ["amend-tests", "NF-1"]),
        ("hold NF-12", ["hold", "NF-12"]),
        ("hold NF-12 --reason '-owner said wait'", ["hold", "NF-12", "--reason=-owner said wait"]),
        ("release NF-12", ["release", "--hold-only", "NF-12"]),
        ("cleanup", ["cleanup"]),
        ("cleanup --now", ["cleanup", "--now"]),
        ("digest-data", ["digest-data"]),
        ("digest-data --done 41", ["digest-data", "--done", "41"]),
        ("host", ["host-report"]),
        ("shell 'df -h /tmp'", ["shell", "--command=df -h /tmp"]),
        ("issue NF-3", ["issue", "NF-3"]),
        ("pr NF-3", ["pr", "NF-3"]),
        (
            "comment NF-3 --text 'Answered in the spec'",
            ["comment", "NF-3", "--text=Answered in the spec"],
        ),
        ("pr-comment NF-3 --text '-LGTM'", ["pr-comment", "NF-3", "--text=-LGTM"]),
        ("search --query 'export rounding'", ["search", "--query=export rounding"]),
        (
            "file NF-3 --title 'Export rounds' --text 'From review'",
            ["file", "NF-3", "--title=Export rounds", "--text=From review"],
        ),
    ],
)
def test_the_allowed_lines(line, argv):
    assert argv_for(line) == argv


def test_a_note_with_shell_text_stays_one_argument():
    note = "fix $(rm -rf ~); it's `x`\n-- and --config=/etc/x"
    quoted = "'" + note.replace("'", "'\\''") + "'"
    assert argv_for(f"resume NF-3 --note {quoted}") == ["resume", "NF-3", f"--note={note}"]


@pytest.mark.parametrize(
    "line",
    [
        "",
        "id",
        "tick",
        "release --keep-worktree NF-1",
        "release --if-stopped NF-1",
        "status --json",
        "cleanup --now --all",
        "events --follow; id",
        "--config /tmp/c.toml status",
        "stop-tests",
        "stop-tests nf-1",
        "stop-tests NF-1 NF-2",
        "amend-tests",
        "amend-tests nf-1",
        "amend-tests NF-1 NF-2",
        "amend-tests NF-1 --label x",
        "run-summary nf-12",
        "run-summary NF-12a",
        "run-summary NF1-2",
        "run-summary 'NF-1\n'",
        "run-summary NF-12 NF-13",
        "fix NF-3 --note 'the old alias'",
        "ack",
        "ack 1 x",
        "ack '1\n'",
        "fix NF-1",
        "fix NF-1 --note ''",
        "resume NF-1",
        "resume NF-1 --note ''",
        "resume nf-1 --note x",
        "resume NF-1 --note x --force",
        "hold NF-1 --why x",
        "digest-data --done -1",
        "events 'unbalanced",
        "host --json",
        "shell",
        "shell ''",
        "shell a b",
        "issue",
        "issue nf-3",
        "issue NF-3 NF-4",
        "pr NF-3 --json",
        "comment NF-3",
        "comment NF-3 --text ''",
        "comment NF-3 --body x",
        "pr-comment NF-3 --text",
        "search",
        "search x",
        "search --query ''",
        "file NF-3 --title t",
        "file NF-3 --text x --title t",
        "file NF-3 --title t --text x --label claude",
        "file NF-3 --title '' --text x",
    ],
)
def test_anything_else_is_refused(line):
    with pytest.raises(Refused):
        argv_for(line)


def test_a_file_title_over_200_characters_is_refused():
    with pytest.raises(Refused, match="title"):
        argv_for("file NF-1 --title " + "t" * 201 + " --text x")


def test_a_search_query_over_200_characters_is_refused():
    with pytest.raises(Refused, match="query"):
        argv_for("search --query " + "q" * 201)


def test_comment_text_over_the_limit_is_refused():
    with pytest.raises(Refused, match="text"):
        argv_for("comment NF-1 --text " + "x" * 2001)


def test_a_note_over_the_limit_is_refused():
    with pytest.raises(Refused, match="note"):
        argv_for("resume NF-1 --note " + "x" * 2001)


def test_a_shell_command_over_the_limit_is_refused():
    with pytest.raises(Refused, match="command"):
        argv_for("shell " + "x" * 2001)


def test_main_runs_flocc_with_the_checked_argv_and_the_default_config(monkeypatch):
    got = []
    monkeypatch.setenv("SSH_ORIGINAL_COMMAND", "release NF-7")
    monkeypatch.setenv("FLOCC_CONFIG", "/tmp/other.toml")

    def run(argv):
        got.append((argv, os.environ.get("FLOCC_CONFIG")))
        return 0

    monkeypatch.setattr("flocc.cli.main", run)
    assert cardinalgate.main() == 0
    assert got == [(["release", "--hold-only", "NF-7"], None)]


def test_main_refuses_with_exit_2(monkeypatch, capsys):
    monkeypatch.setenv("SSH_ORIGINAL_COMMAND", "id")
    monkeypatch.setattr("flocc.cli.main", lambda argv: pytest.fail("ran flocc"))
    assert cardinalgate.main() == 2
    assert "id is not a command cardinal may run" in capsys.readouterr().err


def test_the_host_verb_reaches_a_host_command_not_the_desktop_one(monkeypatch, capsys):
    from flocc import cli

    monkeypatch.setenv("SSH_ORIGINAL_COMMAND", "host")
    monkeypatch.setitem(cli.COMMANDS, "host-report", lambda args: 0)
    assert cardinalgate.main() == 0
    assert "desktop command" not in capsys.readouterr().err


def test_the_gate_is_a_console_script():
    scripts = tomllib.loads((ROOT / "pyproject.toml").read_text())["project"]["scripts"]
    assert scripts["flocc-cardinal-gate"] == "flocc.cardinalgate:main"
