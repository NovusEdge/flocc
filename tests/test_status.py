import json
import os

import pytest

from linear_agents import ledger, usage
from linear_agents.cli import main
from linear_agents.config import load
from linear_agents.status import collect, last_tick
from tests.fakes import FakeSessions
from tests.paths import EXAMPLE
from tests.test_ledger import make
from tests.test_usage import status


@pytest.fixture
def cfg(tmp_path):
    path = tmp_path / "c.toml"
    path.write_text(
        EXAMPLE.read_text()
        .replace('"~/.local/state/linear-agents/ledger.sqlite"', f'"{tmp_path / "l.sqlite"}"')
        .replace("~/.local/state/linear-agents", str(tmp_path / "state"))
    )
    return load(path)


def test_fresh_host_has_no_ledger_log_or_usage(cfg):
    report = collect(cfg, FakeSessions(), now=1000.0)
    assert report["claims"] == [] and report["usage"] is None and report["last_tick"] is None
    assert report["session_cap"] == cfg.session_cap
    assert report["disk"]["free_bytes"] > 0
    assert not cfg.ledger_path.exists()


def test_claims_carry_age_tmux_and_context(cfg):
    con = ledger.connect(cfg.ledger_path)
    ledger.insert_claim(con, make("NF-1"))
    ledger.insert_claim(con, make("NF-2", state="done"))
    usage.write_status(cfg.state_root / "NF-1", status(20, 30, ctx=42.0))
    os.utime(cfg.state_root / "NF-1" / "status.json", (900, 900))
    report = collect(cfg, FakeSessions(alive_names={"NF-1-test"}), now=1000.0)
    (row,) = report["claims"]
    assert row["issue"] == "NF-1" and row["phase"] == "test"
    assert (row["started_age_s"], row["updated_age_s"]) == (1000.0, 100.0)
    assert row["tmux_alive"] is True and row["context_pct"] == 42.0
    assert report["usage"]["five_hour_pct"] == 20 and report["usage"]["age_s"] == 100.0


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


def test_status_json_writes_nothing(cfg, tmp_path, capsys):
    path = tmp_path / "c.toml"
    assert main(["--config", str(path), "status", "--json"]) == 0
    report = json.loads(capsys.readouterr().out)
    assert report["claims"] == [] and report["mode"] in ("systemd", "loop")
    assert not cfg.ledger_path.exists() and not cfg.state_root.exists()
