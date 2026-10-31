import os

import pytest

from flocc import host
from flocc.host import GIB, Reading
from flocc.host import read as real_read

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
