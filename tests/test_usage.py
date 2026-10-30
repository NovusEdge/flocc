import io
import json
import os
from pathlib import Path

import pytest

from flocc import agentfs, usage
from flocc.cli import main

FIX = Path(__file__).parent / "fixtures" / "status.json"


def status(five, week, resets=4_000_000_000, ctx=10.0):
    return {
        "rate_limits": {
            "five_hour": {"used_percentage": five, "resets_at": resets},
            "seven_day": {"used_percentage": week, "resets_at": resets},
        },
        "context_window": {"used_percentage": ctx},
    }


def test_latest_usage_uses_newest_file(tmp_path):
    for key, five, mtime in (("NF-1", 10, 100), ("NF-2", 55, 200)):
        d = tmp_path / key
        usage.write_status(d, status(five, 30))
        os.utime(d / "status.json", (mtime, mtime))
    u = usage.latest_usage(tmp_path, now=300)
    assert u is not None
    assert (u.five_hour_pct, u.weekly_pct, u.observed_at) == (55, 30, 200)


def test_expired_window_counts_as_zero(tmp_path):
    usage.write_status(tmp_path / "NF-1", status(90, 40, resets=1000))
    u = usage.latest_usage(tmp_path, now=2000)
    assert u is not None
    assert u.five_hour_pct == 0 and u.weekly_pct == 0


@pytest.mark.parametrize(
    "window",
    [
        {"used_percentage": None, "resets_at": 4_000_000_000},
        {"used_percentage": "lots", "resets_at": 4_000_000_000},
        {"used_percentage": 20},
        {"used_percentage": 20, "resets_at": None},
        {"used_percentage": 20, "resets_at": "tomorrow"},
    ],
)
def test_unreadable_window_is_unknown_usage(tmp_path, monkeypatch, capsys, window):
    data = status(20, 30)
    data["rate_limits"]["five_hour"] = window
    usage.write_status(tmp_path / "NF-1", data)
    assert usage.latest_usage(tmp_path, now=300) is None
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path / "NF-1"))
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(data)))
    assert main(["statusline"]) == 0
    assert "5h ?" in capsys.readouterr().out


@pytest.mark.parametrize("limits", [None, [], "x", {"five_hour": "x", "seven_day": None}])
def test_non_object_limits_are_unknown_usage(tmp_path, monkeypatch, capsys, limits):
    data = {**status(20, 30), "rate_limits": limits}
    usage.write_status(tmp_path / "NF-1", data)
    assert usage.latest_usage(tmp_path, now=300) is None
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path / "NF-1"))
    monkeypatch.setattr("sys.stdin", io.StringIO(json.dumps(data)))
    assert main(["statusline"]) == 0
    assert "5h ?" in capsys.readouterr().out


def test_unreadable_status_is_skipped(tmp_path):
    (tmp_path / "NF-1" / "status.json").mkdir(parents=True)
    usage.write_status(tmp_path / "NF-2", status(20, 30))
    u = usage.latest_usage(tmp_path, now=300)
    assert u is not None and u.five_hour_pct == 20


def test_status_deleted_mid_scan_is_skipped(tmp_path, monkeypatch):
    usage.write_status(tmp_path / "NF-1", status(20, 30))
    gone = tmp_path / "NF-1" / "status.json"
    real_mtime = agentfs.mtime

    def mtime(directory, name):
        found = real_mtime(directory, name)
        gone.unlink(missing_ok=True)
        return found

    monkeypatch.setattr(agentfs, "mtime", mtime)
    assert usage.latest_usage(tmp_path, now=300) is None


def test_no_files_is_none(tmp_path):
    assert usage.latest_usage(tmp_path, now=0) is None


def test_real_sample_parses():
    sample = json.loads(FIX.read_text())
    assert usage.context_pct(sample) is not None
    assert "five_hour" in sample["rate_limits"]


def test_statusline_cli(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("FLOCC_STATE", str(tmp_path / "NF-7"))
    monkeypatch.setattr("sys.stdin", __import__("io").StringIO(json.dumps(status(12, 40, ctx=34))))
    assert main(["statusline"]) == 0
    assert capsys.readouterr().out.strip() == "NF-7 · ctx 34% · 5h 12% · wk 40%"
    written = usage.read_status(tmp_path / "NF-7")
    assert written is not None
    assert written["context_window"]["used_percentage"] == 34
