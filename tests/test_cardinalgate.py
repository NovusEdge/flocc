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
        ("hold NF-12", ["hold", "NF-12"]),
        ("hold NF-12 --reason '-owner said wait'", ["hold", "NF-12", "--reason=-owner said wait"]),
        ("release NF-12", ["release", "--hold-only", "NF-12"]),
        ("cleanup", ["cleanup"]),
        ("digest-data", ["digest-data"]),
        ("digest-data --done 41", ["digest-data", "--done", "41"]),
    ],
)
def test_the_allowed_lines(line, argv):
    assert argv_for(line) == argv


def test_a_note_with_shell_text_stays_one_argument():
    note = "fix $(rm -rf ~); it's `x`\n-- and --config=/etc/x"
    quoted = "'" + note.replace("'", "'\\''") + "'"
    assert argv_for(f"fix NF-3 --note {quoted}") == ["fix", "NF-3", f"--note={note}"]


@pytest.mark.parametrize(
    "line",
    [
        "",
        "id",
        "tick",
        "release --keep-worktree NF-1",
        "release --if-stopped NF-1",
        "status --json",
        "events --follow; id",
        "--config /tmp/c.toml status",
        "run-summary nf-12",
        "run-summary NF-12a",
        "run-summary NF1-2",
        "run-summary 'NF-1\n'",
        "run-summary NF-12 NF-13",
        "ack",
        "ack 1 x",
        "ack '1\n'",
        "fix NF-1",
        "fix NF-1 --note ''",
        "hold NF-1 --why x",
        "digest-data --done -1",
        "events 'unbalanced",
    ],
)
def test_anything_else_is_refused(line):
    with pytest.raises(Refused):
        argv_for(line)


def test_a_note_over_the_limit_is_refused():
    with pytest.raises(Refused, match="note"):
        argv_for("fix NF-1 --note " + "x" * 2001)


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


def test_the_gate_is_a_console_script():
    scripts = tomllib.loads((ROOT / "pyproject.toml").read_text())["project"]["scripts"]
    assert scripts["flocc-cardinal-gate"] == "flocc.cardinalgate:main"
