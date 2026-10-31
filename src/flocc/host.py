import os
from dataclasses import dataclass
from pathlib import Path

GIB = 2**30


@dataclass(frozen=True)
class Reading:
    tmp_used: int
    tmp_size: int
    mem_available: int
    oom_kills: int

    @property
    def tmp_pct(self) -> float:
        return 100 * self.tmp_used / self.tmp_size if self.tmp_size else 0.0


def mem_available(meminfo: str) -> int:
    for line in meminfo.splitlines():
        if line.startswith("MemAvailable:"):
            return int(line.split()[1]) * 1024
    raise ValueError("no MemAvailable line in /proc/meminfo")


def oom_kills(vmstat: str) -> int:
    for line in vmstat.splitlines():
        name, _, value = line.partition(" ")
        if name == "oom_kill":
            return int(value)
    # Kernels before 4.13 keep no counter.
    return 0


def read(tmp: Path = Path("/tmp"), proc: Path = Path("/proc")) -> Reading:
    st = os.statvfs(tmp)
    return Reading(
        tmp_used=(st.f_blocks - st.f_bfree) * st.f_frsize,
        tmp_size=st.f_blocks * st.f_frsize,
        mem_available=mem_available((proc / "meminfo").read_text()),
        oom_kills=oom_kills((proc / "vmstat").read_text()),
    )


def size_text(n: int) -> str:
    return f"{n / GIB:.1f} GiB" if n >= GIB else f"{n / 2**20:.0f} MiB"
