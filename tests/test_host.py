import os
import types

import pytest

from flocc import events, host, ledger
from flocc.agent import AgentError
from flocc.host import GIB, Reading
from flocc.host import read as real_read
from tests.fakes import FakeSessions
from tests.test_ledger import make

MEMINFO = "MemTotal:        6082560 kB\nMemFree:          204800 kB\nMemAvailable:     629145 kB\n"
VMSTAT = "nr_free_pages 51200\noom_kill 2\nnuma_hit 1\n"


def test_mem_available_is_in_bytes():
    assert host.mem_available(MEMINFO) == 629145 * 1024


def test_a_meminfo_without_mem_available_is_an_error():
    with pytest.raises(ValueError):
        host.mem_available("MemTotal: 1 kB\n")


def test_oom_kills_reads_the_counter_and_is_zero_without_one():
    assert host.oom_kills(VMSTAT) == 2
    assert host.oom_kills("nr_free_pages 1\n") == 0


def test_read_measures_tmp_and_proc(tmp_path):
    proc = tmp_path / "proc"
    proc.mkdir()
    (proc / "meminfo").write_text(MEMINFO)
    (proc / "vmstat").write_text(VMSTAT)
    r = real_read(tmp_path, proc)
    st = os.statvfs(tmp_path)
    assert r.tmp_size == st.f_blocks * st.f_frsize and 0 <= r.tmp_used <= r.tmp_size
    assert (r.mem_available, r.oom_kills) == (629145 * 1024, 2)


def test_tmp_pct_and_an_empty_filesystem():
    assert Reading(3, 4, 0, 0).tmp_pct == 75.0
    assert Reading(0, 0, 0, 0).tmp_pct == 0.0


def test_size_text_switches_to_gib_at_one_gib():
    assert host.size_text(3 * GIB // 2) == "1.5 GiB"
    assert host.size_text(5 * 2**20) == "5 MiB"


SIZE = 3 * GIB


def reading(tmp_pct=10.0, mem_gb=4.0, ooms=0) -> Reading:
    return Reading(int(SIZE * tmp_pct / 100), SIZE, int(mem_gb * GIB), ooms)


def runtime(cfg):
    return types.SimpleNamespace(
        cfg=cfg, con=ledger.connect(cfg.ledger_path), now=lambda: 100.0, sessions=FakeSessions()
    )


def details(rt) -> list[str]:
    return [e.detail for e in events.unacked(rt.con) if e.kind == "host-pressure"]


def test_an_episode_opens_once_and_stays_open_between_the_thresholds(tmp_cfg):
    rt = runtime(tmp_cfg)
    host.check(rt, reading(93))
    host.check(rt, reading(93))
    host.check(rt, reading(75))
    assert details(rt) == ["/tmp 93% (2.8/3.0 GiB)"]
    assert host.under_pressure(rt.con)
    assert host.pressure_detail(rt.con) == "/tmp 93% (2.8/3.0 GiB)"


def test_an_episode_closes_once_every_value_is_past_its_close_threshold(tmp_cfg):
    rt = runtime(tmp_cfg)
    host.check(rt, reading(93, mem_gb=0.6))
    host.check(rt, reading(71, mem_gb=1.05))
    assert host.under_pressure(rt.con)
    host.check(rt, reading(71, mem_gb=1.2))
    assert not host.under_pressure(rt.con) and host.pressure_detail(rt.con) == ""
    host.check(rt, reading(93))
    assert len(details(rt)) == 2


def test_the_detail_names_every_value_over_its_limit(tmp_cfg):
    rt = runtime(tmp_cfg)
    host.check(rt, reading(ooms=3))
    host.check(rt, reading(93, mem_gb=0.6, ooms=5))
    assert details(rt) == ["/tmp 93% (2.8/3.0 GiB); MemAvailable 0.6 GiB; 2 OOM kills"]


def test_the_first_reading_and_a_counter_drop_only_set_the_baseline(tmp_cfg):
    rt = runtime(tmp_cfg)
    host.check(rt, reading(ooms=7))
    host.check(rt, reading(ooms=1))
    assert details(rt) == [] and not host.under_pressure(rt.con)
    assert ledger.meta_get(rt.con, host.OOM_SEEN) == "1"
    host.check(rt, reading(ooms=2))
    assert details(rt) == ["1 OOM kill"]


def test_a_new_kill_during_an_episode_emits_again_and_an_old_one_does_not(tmp_cfg):
    rt = runtime(tmp_cfg)
    host.check(rt, reading(ooms=5))
    host.check(rt, reading(93, ooms=5))
    host.check(rt, reading(93, ooms=6))
    host.check(rt, reading(93, ooms=6))
    host.check(rt, reading(93, ooms=6))
    assert details(rt) == ["/tmp 93% (2.8/3.0 GiB)", "/tmp 93% (2.8/3.0 GiB); 1 OOM kill"]


def test_an_oom_only_episode_closes_on_the_first_tick_without_a_new_kill(tmp_cfg):
    rt = runtime(tmp_cfg)
    host.check(rt, reading(ooms=1))
    host.check(rt, reading(ooms=2))
    assert host.under_pressure(rt.con)
    host.check(rt, reading(ooms=2))
    assert not host.under_pressure(rt.con)


def claim(rt, key, phase, state="running"):
    c = make(key, state=state)
    c.phase = phase
    ledger.insert_claim(rt.con, c)


def test_closing_the_episode_tells_each_running_session_once(tmp_cfg):
    rt = runtime(tmp_cfg)
    claim(rt, "NF-1", "impl")
    claim(rt, "NF-2", "review")
    claim(rt, "NF-3", "impl", state="blocked")
    claim(rt, "NF-4", "fix")
    rt.sessions.alive_names = {"NF-1", "NF-2-review", "NF-3"}
    host.check(rt, reading(93))
    assert rt.sessions.sent == []
    host.check(rt, reading(10))
    host.check(rt, reading(10))
    assert sorted(rt.sessions.sent) == [("NF-1", host.RECOVERED), ("NF-2-review", host.RECOVERED)]


def test_a_send_that_fails_does_not_cost_the_other_sessions_theirs(tmp_cfg, capsys):
    rt = runtime(tmp_cfg)
    claim(rt, "NF-1", "impl")
    claim(rt, "NF-2", "impl")
    rt.sessions.alive_names = {"NF-1", "NF-2"}
    sent = []

    def send(name, text):
        if name == "NF-1":
            raise AgentError("flocc-agent-run send exited 1: can't find session")
        sent.append(name)

    rt.sessions.send = send
    host.check(rt, reading(93))
    host.check(rt, reading(10))
    assert sent == ["NF-2"] and not host.under_pressure(rt.con)
    assert "NF-1: recovery message not sent" in capsys.readouterr().err
