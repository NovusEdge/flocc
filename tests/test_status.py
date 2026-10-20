import json
import os

import pytest

from flocc import ledger, usage
from flocc.cli import main
from flocc.config import load
from flocc.gate import run_gate
from flocc.status import collect, last_tick, systemd_tick
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
    return load(path)


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
        assert argv[:4] == ["systemctl", "--user", "show", "flocc.service"]
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
    }
    return base | over


def test_human_status_prints_tick_usage_and_disk(cfg, tmp_path, monkeypatch, capsys):
    path = str(tmp_path / "c.toml")
    full = _report(
        last_tick={"at": 1.0, "exit": "0", "age_s": 300.0},
        usage={"five_hour_pct": 20, "weekly_pct": 30},
        disk={"free_bytes": 5 * 2**30},
    )
    monkeypatch.setattr("flocc.status.collect", lambda cfg, sessions: full)
    assert main(["--config", path, "status"]) == 0
    out = capsys.readouterr().out
    assert "last tick 5m ago, ok" in out
    assert "usage 5h 20%  wk 30%" in out
    assert "disk 5.0 GiB free" in out

    empty = _report()
    monkeypatch.setattr("flocc.status.collect", lambda cfg, sessions: empty)
    assert main(["--config", path, "status"]) == 0
    out = capsys.readouterr().out
    assert "last tick: none" in out and "usage: none yet" in out and "disk" not in out

    noage = _report(last_tick={"at": None, "exit": "0"})
    monkeypatch.setattr("flocc.status.collect", lambda cfg, sessions: noage)
    assert main(["--config", path, "status"]) == 0
    out = capsys.readouterr().out
    assert "last tick ok" in out and "ago" not in out


def test_status_json_writes_nothing(cfg, tmp_path, capsys):
    path = tmp_path / "c.toml"
    assert main(["--config", str(path), "status", "--json"]) == 0
    report = json.loads(capsys.readouterr().out)
    assert report["claims"] == [] and report["mode"] in ("systemd", "loop")
    assert not cfg.ledger_path.exists() and not cfg.state_root.exists()


def test_claims_carry_launches_against_budget(cfg):
    con = ledger.connect(cfg.ledger_path)
    ledger.insert_claim(con, make("NF-1"))
    state = cfg.state_root / "NF-1"
    run_gate(
        {"tool_name": "Agent", "tool_input": {"subagent_type": "source-finder"}},
        state,
        "test",
        "test@0.0",
        1.0,
    )
    (row,) = collect(cfg, FakeSessions(), now=1000.0, mode="loop")["claims"]
    assert (row["launches"], row["launch_limit"]) == (1, 2)
