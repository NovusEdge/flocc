import json
import os
import subprocess
from dataclasses import replace

import pytest

from flocc import events, ledger, usage
from flocc.cli import main
from flocc.config import load
from flocc.status import collect, issue_lines, last_tick, systemd_tick
from tests.fakes import FakeSessions
from tests.paths import EXAMPLE
from tests.test_ledger import make
from tests.test_usage import status


@pytest.fixture
def cfg(tmp_path):
    path = tmp_path / "c.toml"
    path.write_text(
        EXAMPLE.read_text()
        .replace('"~/.local/state/flocc/ledger.sqlite"', f'"{tmp_path / "l.sqlite"}"')
        .replace("~/.local/state/flocc", str(tmp_path / "state"))
    )
    loaded = load(path)
    return replace(loaded, agent_state_root=loaded.state_root)


def test_fresh_host_has_no_ledger_log_or_usage(cfg):
    report = collect(cfg, FakeSessions(), now=1000.0, mode="loop")
    assert (
        report["claims"] == []
        and report["ledger_error"] is None
        and report["usage"] is None
        and report["last_tick"] is None
    )
    assert report["session_cap"] == cfg.session_cap
    assert report["disk"]["free_bytes"] > 0
    assert not cfg.ledger_path.exists()


def test_status_names_the_tick_lock(cfg):
    report = collect(cfg, FakeSessions(), now=1000.0, mode="loop")
    assert report["tick_lock"] == str(cfg.ledger_path.with_suffix(".lock"))
    assert cfg.tick_lock == cfg.ledger_path.with_suffix(".lock")


def test_claims_carry_age_tmux_and_context(cfg):
    con = ledger.connect(cfg.ledger_path)
    ledger.insert_claim(con, make("NF-1"))
    ledger.insert_claim(con, make("NF-2", state="done"))
    usage.write_status(cfg.state_root / "NF-1", status(20, 30, ctx=42.0))
    os.utime(cfg.state_root / "NF-1" / "status.json", (900, 900))
    report = collect(cfg, FakeSessions(alive_names={"NF-1-test"}), now=1000.0, mode="loop")
    (row,) = report["claims"]
    assert row["issue"] == "NF-1" and row["phase"] == "test"
    assert (row["started_age_s"], row["updated_age_s"]) == (1000.0, 100.0)
    assert row["tmux_alive"] is True and row["context_pct"] == 42.0
    assert report["usage"]["five_hour_pct"] == 20 and report["usage"]["age_s"] == 100.0


def test_reading_leaves_the_ledger_file_unchanged(cfg):
    con = ledger.connect(cfg.ledger_path)
    ledger.insert_claim(con, make("NF-1"))
    con.execute("PRAGMA wal_checkpoint(TRUNCATE)")
    con.close()
    before = cfg.ledger_path.read_bytes()
    report = collect(cfg, FakeSessions(), now=1000.0, mode="loop")
    assert [c["issue"] for c in report["claims"]] == ["NF-1"]
    assert cfg.ledger_path.read_bytes() == before


def test_unreadable_ledger_is_reported_not_hidden(cfg):
    cfg.ledger_path.parent.mkdir(parents=True, exist_ok=True)
    cfg.ledger_path.write_bytes(b"not a database" * 100)
    report = collect(cfg, FakeSessions(), now=1000.0, mode="loop")
    assert report["claims"] == [] and report["ledger_error"]


class FakeSystemctl:
    def __init__(self, stdout):
        self.stdout = stdout

    def __call__(self, argv, **kwargs):
        assert argv[:3] == ["systemctl", "--user", "show"]
        assert kwargs["env"]["XDG_RUNTIME_DIR"].startswith("/run/user/")
        return type("R", (), {"stdout": self.stdout})()


def test_systemd_tick_reads_the_service_timestamps(cfg):
    props = (
        "ExecMainStartTimestamp=Thu 2026-01-01 00:03:00 UTC\nExecMainExitTimestamp=Thu 2026-01-01"
        " 00:03:20 UTC\nExecMainStatus=1\nActiveState=failed\n"
    )
    assert systemd_tick(FakeSystemctl(props)) == {"at": 1767225780.0, "exit": "1"}
    running = props.replace("failed", "activating")
    assert systemd_tick(FakeSystemctl(running))["exit"] is None  # type: ignore[index]
    assert systemd_tick(FakeSystemctl("ExecMainStartTimestamp=\n")) is None
    report = collect(
        cfg, FakeSessions(), now=1767225800.0, mode="systemd", run=FakeSystemctl(props)
    )
    assert report["last_tick"]["exit"] == "1" and report["last_tick"]["age_s"] == 20.0


def test_last_tick_reads_the_final_marker_pair(tmp_path):
    log = tmp_path / "run.log"
    log.write_text(
        "--- tick 2026-01-01T00:00:00Z\nnoise\n--- exit 0\n"
        "--- tick 2026-01-01T00:03:00Z\n--- exit 1\n--- tick 2026-01-01T00:06:00Z\n"
    )
    tick = last_tick(log)
    assert tick is not None and tick["exit"] is None
    assert tick["at"] == 1767225960.0
    log.write_text("--- tick 2026-01-01T00:00:00Z\n--- exit error: [Errno 2] boom\n")
    tick = last_tick(log)
    assert tick is not None and tick["exit"] == "error: [Errno 2] boom"
    assert last_tick(tmp_path / "missing.log") is None


def _report(**over):
    base = {
        "host": "h",
        "mode": "loop",
        "session_cap": 2,
        "claims": [],
        "ledger_error": None,
        "last_tick": None,
        "usage": None,
        "disk": None,
        "now": 0.0,
        "claude_login": None,
        "token_expiry": {},
    }
    return base | over


def test_status_lists_finished_runs_holds_and_waiting_events(cfg):
    con = ledger.connect(cfg.ledger_path)
    ledger.insert_claim(con, make("NF-1"))
    done = make("NF-2", state="done")
    done.phase = "review"
    ledger.insert_claim(con, done)
    (cfg.state_root / "NF-2").mkdir(parents=True)
    (cfg.state_root / "NF-2" / "pr-url").write_text("https://pr/2\n")
    ledger.add_hold(con, "NF-3", "owner said wait", 400.0)
    events.emit(con, "ended", 100.0, issue_key="NF-2")
    stuck = events.emit(con, "cap", 200.0, issue_key="NF-1")
    for _ in range(1 + events.REDELIVERIES):
        events.mark_printed(con, [stuck], 300.0)
    report = collect(cfg, FakeSessions(), now=10_000.0, mode="loop")
    assert report["claims"][0]["archetype"] == "tester" and report["check_fix_rounds"] == 2
    [fin] = report["finished"]
    assert (fin["issue"], fin["archetype"], fin["pr_url"]) == ("NF-2", "reviewer", "https://pr/2")
    assert report["holds"] == [{"issue": "NF-3", "reason": "owner said wait", "age_s": 9600.0}]
    assert report["events"] == {"unacked": 2, "oldest_age_s": 9900.0, "attention": 1}


def test_status_reads_a_ledger_without_holds_or_events(cfg):
    import sqlite3

    from tests.test_ledger import V0_CLAIM

    cfg.ledger_path.parent.mkdir(parents=True, exist_ok=True)
    raw = sqlite3.connect(cfg.ledger_path)
    raw.executescript(ledger._SCHEMA)
    raw.execute(V0_CLAIM.replace("'running'", "'done'"))
    raw.commit()
    raw.close()
    report = collect(cfg, FakeSessions(), now=1000.0, mode="loop")
    assert report["ledger_error"] is None and report["holds"] == []
    assert [f["issue"] for f in report["finished"]] == ["NF-9"]
    assert report["events"] == {"unacked": 0, "oldest_age_s": None, "attention": 0}


def test_status_reads_events_without_the_prints_column(cfg):
    import sqlite3

    from tests.test_ledger import V0_CLAIM

    cfg.ledger_path.parent.mkdir(parents=True, exist_ok=True)
    raw = sqlite3.connect(cfg.ledger_path)
    raw.executescript(ledger._SCHEMA)
    raw.execute(V0_CLAIM.replace("'running'", "'done'"))
    raw.execute(
        "CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL,"
        " issue_key TEXT, repo TEXT, detail TEXT NOT NULL DEFAULT '', at REAL NOT NULL,"
        " printed_at REAL, acked_at REAL)"
    )
    raw.execute("INSERT INTO events (kind, at) VALUES ('ended', 100.0)")
    raw.commit()
    raw.close()
    report = collect(cfg, FakeSessions(), now=1000.0, mode="loop")
    assert report["ledger_error"] is None
    assert [f["issue"] for f in report["finished"]] == ["NF-9"]
    assert report["events"]["unacked"] == 1


def test_human_status_prints_the_table(cfg, tmp_path, monkeypatch, capsys):
    path = str(tmp_path / "c.toml")
    row = {
        "issue": "NF-1",
        "state": "running",
        "phase": "impl",
        "archetype": "executor",
        "fix_attempts": 0,
        "started_age_s": 3600.0,
        "context_pct": 42.0,
        "tmux_alive": True,
    }
    fin = {
        "issue": "NF-2",
        "state": "done",
        "phase": "review",
        "archetype": "reviewer",
        "fix_attempts": 1,
        "started_age_s": 7200.0,
        "pr_url": "https://pr/2",
    }
    full = _report(
        claims=[row],
        finished=[fin],
        check_fix_rounds=2,
        holds=[{"issue": "NF-3", "reason": "wait", "age_s": 120.0}],
        events={"unacked": 2, "oldest_age_s": 840.0, "attention": 1},
    )
    monkeypatch.setattr("flocc.status.collect", lambda cfg, sessions, **kw: full)
    assert main(["--config", path, "status"]) == 0
    out = capsys.readouterr().out
    assert "  ISSUE    STEP      STATE      FIX  AGE   CTX   SESSION" in out
    assert "  NF-1     executor  running    0/2  1h    42%   up" in out
    assert "  NF-2     reviewer  done       1/2  2h    -     -  https://pr/2" in out
    assert "  held NF-3: wait (2m)" in out
    assert "  events waiting for cardinal: 2, oldest 14m; 1 needs attention" in out


def test_human_status_prints_tick_usage_and_disk(cfg, tmp_path, monkeypatch, capsys):
    path = str(tmp_path / "c.toml")
    full = _report(
        last_tick={"at": 1.0, "exit": "0", "age_s": 300.0},
        usage={"five_hour_pct": 20, "weekly_pct": 30},
        disk={"free_bytes": 5 * 2**30},
    )
    monkeypatch.setattr("flocc.status.collect", lambda cfg, sessions, **kw: full)
    assert main(["--config", path, "status"]) == 0
    out = capsys.readouterr().out
    assert "last tick 5m ago, ok" in out
    assert "usage 5h 20%  wk 30%" in out
    assert "disk 5.0 GiB free" in out

    empty = _report()
    monkeypatch.setattr("flocc.status.collect", lambda cfg, sessions, **kw: empty)
    assert main(["--config", path, "status"]) == 0
    out = capsys.readouterr().out
    assert "last tick: none" in out and "usage: none yet" in out and "disk" not in out

    noage = _report(last_tick={"at": None, "exit": "0"})
    monkeypatch.setattr("flocc.status.collect", lambda cfg, sessions, **kw: noage)
    assert main(["--config", path, "status"]) == 0
    out = capsys.readouterr().out
    assert "last tick ok" in out and "ago" not in out


def test_status_reports_the_claude_login_and_token_expiries(cfg):
    con = ledger.connect(cfg.ledger_path)
    ledger.meta_set(con, "token_expiry:agent", "1900000000")
    ledger.meta_set(con, "token_expiry:publish", "never")
    con.close()

    def run(argv, **kw):
        assert argv == ["/opt/claude", "auth", "status"]
        return subprocess.CompletedProcess(
            argv, 0, '{"loggedIn": true, "authMethod": "claude.ai"}', ""
        )

    report = collect(cfg, FakeSessions(), now=1000.0, mode="loop", run=run, claude=["/opt/claude"])
    assert report["token_expiry"] == {"agent": "1900000000", "publish": "never"}
    assert report["claude_login"] == "full"
    assert collect(cfg, FakeSessions(), now=1000.0, mode="loop")["claude_login"] is None


def test_a_login_the_host_cannot_read_is_unknown_not_none(cfg):
    def run(argv, **kw):
        return subprocess.CompletedProcess(argv, 1, '{"error": "rate limited"}', "")

    report = collect(cfg, FakeSessions(), now=1000.0, mode="loop", run=run, claude=["/opt/claude"])
    assert report["claude_login"] == "unknown"


def test_a_hung_claude_probe_is_cut_short_and_reads_unknown(cfg):
    timeouts = []

    def hangs(argv, **kw):
        timeouts.append(kw["timeout"])
        raise subprocess.TimeoutExpired(argv, kw["timeout"])

    report = collect(
        cfg, FakeSessions(), now=1000.0, mode="loop", run=hangs, claude=["/opt/claude"]
    )
    assert timeouts == [5] and report["claude_login"] == "unknown"


def test_human_status_prints_the_login_and_expiries(cfg, tmp_path, monkeypatch, capsys):
    report = _report(
        now=1_800_000_000.0,
        claude_login="none",
        token_expiry={"agent": str(1_800_000_000 + 5 * 86400), "publish": "rejected"},
    )
    monkeypatch.setattr("flocc.status.collect", lambda cfg, sessions, **kw: report)
    assert main(["--config", str(tmp_path / "c.toml"), "status"]) == 0
    out = capsys.readouterr().out
    assert "claude login none" in out
    assert "github agent token expires 2027-01-20 (5 days)" in out
    assert "github publish token rejected by GitHub" in out


def test_status_json_writes_nothing(cfg, tmp_path, capsys, monkeypatch):
    # The real claude CLI would be run otherwise.
    monkeypatch.setattr(
        "flocc.status.claude_login", lambda claude, run, timeout: f"full via {' '.join(claude)}"
    )
    path = tmp_path / "c.toml"
    assert main(["--config", str(path), "status", "--json"]) == 0
    report = json.loads(capsys.readouterr().out)
    assert report["claims"] == [] and report["mode"] in ("systemd", "loop")
    assert report["claude_login"] == f"full via {' '.join(cfg.agent_run)} claude"
    assert not cfg.ledger_path.exists() and not cfg.state_root.exists()


def _journal(*lines):
    def run(argv, **kw):
        if argv[0] == "systemctl":
            return subprocess.CompletedProcess(argv, 0, "", "")
        assert argv[:5] == ["journalctl", "--user", "-u", "flocc", "-n"]
        assert argv[5] == "2000" and "-o" in argv and "short-unix" in argv
        return subprocess.CompletedProcess(argv, 0, "\n".join(lines) + "\n", "")

    return run


def test_status_carries_the_version_and_each_claims_last_five_lines(cfg):
    con = ledger.connect(cfg.ledger_path)
    ledger.insert_claim(con, make("NF-1"))
    lines = [f"{1000 + i}.5 host flocc[7]: NF-1 step {i}" for i in range(7)]
    lines.insert(3, "1003.9 host flocc[7]: NF-12 belongs to another issue")
    run = _journal(*lines, "1100.0 host flocc[7]: tick done")
    report = collect(cfg, FakeSessions(), now=2000.0, mode="systemd", run=run)
    assert report["v"] == 1 and "activity_error" not in report
    (row,) = report["claims"]
    assert [a["text"] for a in row["activity"]] == [f"NF-1 step {i}" for i in range(2, 7)]
    assert row["activity"][-1]["at"] == 1006


def test_status_activity_falls_back_to_run_log_tail(cfg):
    con = ledger.connect(cfg.ledger_path)
    ledger.insert_claim(con, make("NF-1"))
    cfg.state_root.mkdir(parents=True, exist_ok=True)
    log = cfg.state_root / "run.log"
    log.write_text("NF-1 started\nother\nNF-1 pushed\n")
    os.utime(log, (500, 500))
    (row,) = collect(cfg, FakeSessions(), now=1000.0, mode="loop")["claims"]
    assert row["activity"] == [
        {"at": 500, "text": "NF-1 started"},
        {"at": 500, "text": "NF-1 pushed"},
    ]


def test_a_failing_journal_leaves_activity_empty_and_status_whole(cfg):
    con = ledger.connect(cfg.ledger_path)
    ledger.insert_claim(con, make("NF-1"))

    def run(argv, **kw):
        if argv[0] == "journalctl":
            raise subprocess.TimeoutExpired(argv, 10)
        return subprocess.CompletedProcess(argv, 0, "", "")

    report = collect(cfg, FakeSessions(), now=1000.0, mode="systemd", run=run)
    assert report["claims"][0]["activity"] == [] and report["activity_error"]


_NAMING = [
    "1000.5 host flocc[7]: NF-240 started",
    "1001.5 host flocc[7]: NF-2400 other",
    "1002.5 host flocc[7]: NF-24 other",
    "1003.5 host flocc[7]: NF-240 pushed",
    "1004.5 host flocc[7]: tick done",
]


def test_issue_lines_keeps_only_the_lines_naming_the_issue(cfg):
    got = issue_lines(cfg, "NF-240", 5, mode="systemd", run=_journal(*_NAMING))
    assert got == [(1000, "NF-240 started"), (1003, "NF-240 pushed")]
    assert issue_lines(cfg, "NF-240", 1, mode="systemd", run=_journal(*_NAMING)) == got[-1:]


def test_a_loop_host_without_run_log_has_no_lines(cfg):
    con = ledger.connect(cfg.ledger_path)
    ledger.insert_claim(con, make("NF-1"))
    assert issue_lines(cfg, "NF-1", 5, mode="loop") == []
    report = collect(cfg, FakeSessions(), now=1000.0, mode="loop")
    assert report["claims"][0]["activity"] == [] and "activity_error" not in report


def test_issue_lines_reads_run_log_on_a_loop_host(cfg):
    cfg.state_root.mkdir(parents=True, exist_ok=True)
    log = cfg.state_root / "run.log"
    log.write_text("NF-240 a\nNF-2400 b\nNF-24 c\nNF-240 d\n")
    assert [t for _, t in issue_lines(cfg, "NF-240", 5, mode="loop")] == ["NF-240 a", "NF-240 d"]
