import json
import os
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path

from flocc import events, ledger
from flocc.sessions import session_name

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


EPISODE = "host_episode"
OOM_SEEN = "host_oom_kills"
# A value sitting at its limit would otherwise open and close an episode every tick.
CLOSE_TMP = 0.9
CLOSE_MEM = 1.1
RECOVERED = "The host has recovered; you may run the test suite again."
STOPPED = (
    "flocc stopped your test run because the host is under memory or /tmp pressure. Do not"
    " rerun the suite until flocc says the host has recovered."
)


def _episode(con) -> dict | None:
    raw = ledger.meta_get(con, EPISODE)
    try:
        value = json.loads(raw) if raw else None
    except ValueError:
        return None
    return value if isinstance(value, dict) else None


def open_episode(con, now: float, detail: str) -> None:
    ledger.meta_set(con, EPISODE, json.dumps({"at": now, "detail": detail}))


def pressure_detail(con) -> str:
    """The open episode's detail; "" when the host is not under pressure."""
    episode = _episode(con)
    if episode is None:
        return ""
    return str(episode.get("detail") or "host under pressure")


def under_pressure(con) -> bool:
    return bool(pressure_detail(con))


def _over(r: Reading, cfg, new_ooms: int) -> list[str]:
    parts = []
    if r.tmp_pct > cfg.tmp_max_pct:
        parts.append(f"/tmp {r.tmp_pct:.0f}% ({r.tmp_used / GIB:.1f}/{r.tmp_size / GIB:.1f} GiB)")
    if r.mem_available < cfg.min_available_mem_gb * GIB:
        parts.append(f"MemAvailable {r.mem_available / GIB:.1f} GiB")
    if new_ooms:
        parts.append(f"{new_ooms} OOM kill{'s' * (new_ooms != 1)}")
    return parts


def _recovered(r: Reading, cfg) -> bool:
    return (
        r.tmp_pct < CLOSE_TMP * cfg.tmp_max_pct
        and r.mem_available > CLOSE_MEM * cfg.min_available_mem_gb * GIB
    )


def _tell_recovered(rt) -> None:
    for claim in ledger.live_claims(rt.con):
        if claim.state != "running":
            continue
        name = session_name(claim.issue_key, claim.phase)
        # A session can end between the two calls; the others still get their message.
        try:
            if rt.sessions.alive(name):
                rt.sessions.send(name, RECOVERED)
        except (subprocess.SubprocessError, OSError) as exc:
            print(f"{claim.issue_key}: recovery message not sent: {exc}", file=sys.stderr)


def check(rt, reading: Reading | None = None) -> None:
    """Opens an episode with a host-pressure event when a value crosses its limit, and closes it
    once every value is back past its close threshold."""
    r = reading or read()
    con, now = rt.con, rt.now()
    seen = ledger.meta_get(con, OOM_SEEN)
    # The counter restarts at boot, so a drop means a reboot. A drop and the first reading only
    # set the baseline.
    new_ooms = r.oom_kills - int(seen) if seen and r.oom_kills >= int(seen) else 0
    over = _over(r, rt.cfg, new_ooms)
    episode = _episode(con)
    # A kill during an open episode is an event of its own: the repair did not hold.
    if over and (episode is None or new_ooms):
        detail = "; ".join(over)
        # Before the marks: a tick that dies in between emits again, and cardinal acks the
        # duplicate away, where a lost event is never seen.
        events.emit(con, "host-pressure", now, detail=detail)
        open_episode(con, now if episode is None else float(episode.get("at", now)), detail)
    elif episode is not None and _recovered(r, rt.cfg):
        # Before the messages, so starts resume even if every send fails.
        ledger.meta_set(con, EPISODE, "")
        _tell_recovered(rt)
    ledger.meta_set(con, OOM_SEEN, str(r.oom_kills))


SHELL_LOG = "shell"


def record_shell(con, at: float, command: str, code: int | None) -> None:
    """One agent-shell call; code stays None when the call never answered or timed out."""
    ledger.log_append(con, SHELL_LOG, at, {"command": command, "code": code})


def shell_calls(con, since: float) -> list[tuple[float, str, int | None]]:
    calls = []
    for v in ledger.log_since(con, SHELL_LOG, since):
        code = v.get("code")
        calls.append((v["at"], str(v.get("command", "")), code if isinstance(code, int) else None))
    return calls


def report_text(cfg, r: Reading, usage: dict, episode: str) -> str:
    """cardinal's host verb. tmpfs pages belong to no process, so /tmp shows only in its list."""

    def owner(issue) -> str:
        return f" ({issue})" if issue else ""

    tmp = [f"- {size_text(e['bytes'])} {e['path']}{owner(e['issue'])}" for e in usage["largest"]]
    by_size = sorted(usage["issues"].items(), key=lambda kv: -kv[1])
    procs = [f"- {key}: {size_text(n)}" for key, n in by_size]
    boxes = [f"- {c['name']}: {c['memory']}{owner(c['issue'])}" for c in usage["containers"]]
    return "\n".join(
        [
            (
                f"/tmp {r.tmp_pct:.0f}% used ({r.tmp_used / GIB:.1f} GiB of"
                f" {r.tmp_size / GIB:.1f} GiB); limit {cfg.tmp_max_pct:g}%"
            ),
            f"MemAvailable {r.mem_available / GIB:.1f} GiB; limit {cfg.min_available_mem_gb:g} GiB",
            f"OOM kills since boot: {r.oom_kills}",
            f"Episode open: {episode}" if episode else "Episode: none open",
            "",
            "Largest in /tmp owned by flocc-agent:",
            *(tmp or ["- none"]),
            "",
            "Process memory by issue (RSS):",
            *(procs or ["- none"]),
            "",
            "Containers:",
            *(boxes or ["- none"]),
        ]
    )
