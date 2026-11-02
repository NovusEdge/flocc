import json
import subprocess
from dataclasses import replace
from pathlib import Path

import pytest

from flocc import checkjob
from flocc.agent import Agent, CheckLost
from flocc.worktree import git
from tests.test_ledger import make

UNIT = "flocc-check-NF-7"


class Systemd:
    """Stands in for systemd-run and systemctl --user."""

    def __init__(self):
        self.units: dict[str, dict] = {}
        self.runs: list[list[str]] = []
        self.stops: list[str] = []
        self.refuse = False
        self.bus_down = False

    def __call__(self, argv):
        def done(out="", code=0, err=""):
            return subprocess.CompletedProcess(argv, code, out, err)

        if argv[0] == "systemd-run":
            self.runs.append(argv)
            if self.refuse:
                return done(code=1, err="Failed to connect to bus")
            name = next(a.split("=", 1)[1] for a in argv if a.startswith("--unit="))
            self.units[name] = {
                "LoadState": "loaded",
                "ActiveState": "active",
                "SubState": "running",
                "Result": "success",
                "ExecMainStatus": "0",
            }
            return done()
        verb, name = argv[2], argv[3]
        if verb == "show":
            if self.bus_down:
                return done(code=1, err="Failed to connect to bus")
            props = self.units.get(name, {"LoadState": "not-found"})
            return done("".join(f"{k}={v}\n" for k, v in props.items()))
        if verb == "stop":
            self.stops.append(name)
        self.units.pop(name, None)
        return done()

    def log_path(self) -> Path:
        arg = next(a for a in self.runs[-1] if a.startswith("--property=StandardOutput="))
        return Path(arg.split("truncate:", 1)[1])

    def finish(self, name=UNIT, result="success", status="0", output=""):
        self.units[name].update(SubState="exited", Result=result, ExecMainStatus=status)
        self.log_path().write_text(output)


@pytest.fixture
def systemd(monkeypatch):
    fake = Systemd()
    monkeypatch.setattr(checkjob, "_run", fake)
    return fake


@pytest.fixture
def wt(origin):
    return origin


def step(wt, state, timeout_s=600, command="just check"):
    req = {"command": command, "timeout_s": timeout_s}
    return checkjob.step(req, "NF-7", wt, state)


def test_a_check_starts_as_a_user_unit_and_returns_at_once(systemd, wt, tmp_path):
    state = tmp_path / "state"
    assert step(wt, state) == {"status": "running"}
    [argv] = systemd.runs
    assert argv[:3] == ["systemd-run", "--user", "--quiet"] and f"--unit={UNIT}" in argv
    assert f"--property=RuntimeMaxSec={600 + checkjob.WAIT_MAX_S}" in argv
    assert "--collect" not in argv
    assert argv[argv.index("--") + 1 :] == ["just", "check"]
    job = json.loads((state / "check.json").read_text())
    assert job["head"] == git("rev-parse", "HEAD", cwd=wt)
    assert step(wt, state) == {"status": "running"} and len(systemd.runs) == 1


def test_a_check_that_ends_reports_its_output_and_stops_the_unit(systemd, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state)
    systemd.finish(output="all green\n")
    ran = step(wt, state)
    assert ran == {
        "status": "ok",
        "output": "all green\n",
        "head": git("rev-parse", "HEAD", cwd=wt),
    }
    # Once before the start, in case an earlier one left the name taken, and once to reap.
    assert systemd.stops == [UNIT, UNIT] and UNIT not in systemd.units
    assert not (state / "check.json").exists() and not (state / "check.log").exists()


def test_a_check_that_exits_nonzero_failed(systemd, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state)
    systemd.finish(result="exit-code", status="3", output="boom\n")
    ran = step(wt, state)
    assert ran["status"] == "failed" and ran["output"] == "boom\n"


def test_a_unit_stopped_by_runtime_max_is_a_timeout(systemd, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state)
    systemd.finish(result="timeout", status="0", output="slow\n")
    assert step(wt, state)["status"] == "timeout"


def test_a_check_past_its_time_is_stopped_by_the_poll(systemd, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state, timeout_s=60)
    job = json.loads((state / "check.json").read_text())
    job["started"] -= 61
    (state / "check.json").write_text(json.dumps(job))
    systemd.log_path().write_text("still going\n")
    ran = step(wt, state, timeout_s=60)
    assert ran["status"] == "timeout" and ran["output"] == "still going\n"
    assert systemd.stops == [UNIT, UNIT] and UNIT not in systemd.units


def test_a_dirty_worktree_starts_nothing(systemd, wt, tmp_path):
    (wt / "stray.txt").write_text("x")
    ran = step(wt, tmp_path / "state")
    assert ran["status"] == "dirty" and "stray.txt" in ran["output"] and systemd.runs == []


def test_a_check_of_older_commits_is_stopped_and_started_again(systemd, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state)
    (wt / "late.txt").write_text("x")
    git("add", ".", cwd=wt)
    git("commit", "-qm", "late", cwd=wt)
    assert step(wt, state) == {"status": "running"}
    assert len(systemd.runs) == 2 and systemd.stops.count(UNIT) >= 1
    assert json.loads((state / "check.json").read_text())["head"] == git(
        "rev-parse", "HEAD", cwd=wt
    )


def age(state, seconds):
    job = json.loads((state / "check.json").read_text())
    job["started"] -= seconds
    (state / "check.json").write_text(json.dumps(job))


def test_a_unit_not_there_yet_is_not_lost(systemd, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state)
    systemd.units.clear()
    assert step(wt, state) == {"status": "running"} and len(systemd.runs) == 1


def test_a_vanished_unit_is_rerun_once_then_reported(systemd, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state)
    for expected in ("running", "lost"):
        systemd.units.clear()
        age(state, checkjob.START_GRACE_S + 1)
        assert step(wt, state)["status"] == expected
    assert len(systemd.runs) == 2 and not (state / "check.json").exists()


def test_the_command_is_not_expanded_by_systemd(systemd, wt, tmp_path):
    step(wt, tmp_path / "state", command="sh -c 'echo $$ ${X}'")
    argv = systemd.runs[0]
    assert "--expand-environment=no" in argv
    assert argv[argv.index("--") + 1 :] == ["sh", "-c", "echo $$ ${X}"]


def test_a_bus_that_does_not_answer_is_an_error_not_a_lost_check(systemd, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state)
    age(state, checkjob.START_GRACE_S + 1)
    systemd.bus_down = True
    with pytest.raises(subprocess.CalledProcessError):
        step(wt, state)
    assert json.loads((state / "check.json").read_text())["lost"] == 0


def test_a_refused_rerun_keeps_the_lost_count(systemd, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state)
    systemd.units.clear()
    age(state, checkjob.START_GRACE_S + 1)
    systemd.refuse = True
    with pytest.raises(subprocess.CalledProcessError):
        step(wt, state)
    systemd.refuse = False
    systemd.units.clear()
    age(state, checkjob.START_GRACE_S + 1)
    assert step(wt, state)["status"] == "running"
    systemd.units.clear()
    age(state, checkjob.START_GRACE_S + 1)
    assert step(wt, state)["status"] == "lost"


def test_a_refused_start_raises_and_leaves_no_record(systemd, wt, tmp_path):
    systemd.refuse = True
    state = tmp_path / "state"
    with pytest.raises(subprocess.CalledProcessError):
        step(wt, state)
    assert not (state / "check.json").exists()


def test_stop_job_stops_the_unit_and_forgets_it(systemd, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state)
    checkjob.stop_job(state, "NF-7")
    assert UNIT not in systemd.units and not (state / "check.json").exists()


def test_a_lost_check_reaches_the_service_as_check_lost(tmp_cfg):
    def runner(argv, **kw):
        return subprocess.CompletedProcess(argv, 0, json.dumps({"status": "lost"}), "")

    claim = make("NF-7", worktree="/wt/NF-7")
    repo = replace(next(iter(tmp_cfg.repos.values())), check="true")
    with pytest.raises(CheckLost, match="vanished twice"):
        Agent(tmp_cfg, runner).check_worktree(claim, repo, 60)


def test_the_timeout_is_whole_seconds_of_at_least_one(systemd, wt, tmp_path):
    step(wt, tmp_path / "state", timeout_s=0.2)
    assert f"--property=RuntimeMaxSec={1 + checkjob.WAIT_MAX_S}" in systemd.runs[0]


@pytest.fixture
def lock(systemd, monkeypatch, tmp_path):
    """Puts the unit's processes 4242 and 4243 in a cgroup; lock.block(True) makes 4242 a blocked
    waiter in /proc/locks behind `holder`, False a holder itself."""
    cg = tmp_path / "cgroup"
    (cg / "app.slice" / f"{UNIT}.service").mkdir(parents=True)
    (cg / "app.slice" / f"{UNIT}.service" / "cgroup.procs").write_text("4242\n4243\n")
    locks = tmp_path / "locks"
    monkeypatch.setattr(checkjob, "CGROUPS", cg)
    monkeypatch.setattr(checkjob, "LOCKS", locks)

    class Lock:
        def block(self, blocked: bool, holder: str = "9999"):
            systemd.units[UNIT]["ControlGroup"] = f"/app.slice/{UNIT}.service"
            arrow = "-> " if blocked else ""
            locks.write_text(
                f"1: FLOCK  ADVISORY  WRITE {holder} 00:30:5 0 EOF\n"
                f"1: {arrow}FLOCK  ADVISORY  WRITE 4242 00:30:5 0 EOF\n"
                "2: POSIX  ADVISORY  WRITE 4243 00:30:6 0 EOF\n"
            )

    return Lock()


def rewind(state, seconds):
    job = json.loads((state / "check.json").read_text())
    job["started"] -= seconds
    job["polled"] -= seconds
    (state / "check.json").write_text(json.dumps(job))


def test_time_blocked_on_a_lock_does_not_count_against_the_limit(systemd, lock, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state, timeout_s=60)
    lock.block(True)
    assert step(wt, state, timeout_s=60) == {"status": "running"}
    for _ in range(5):
        rewind(state, 200)
        assert step(wt, state, timeout_s=60) == {"status": "running"}
    lock.block(False)
    assert step(wt, state, timeout_s=60) == {"status": "running"}
    rewind(state, 61)
    assert step(wt, state, timeout_s=60)["status"] == "timeout"


def test_a_check_that_blocks_after_running_is_not_credited_its_run(systemd, lock, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state, timeout_s=60)
    lock.block(False)
    step(wt, state, timeout_s=60)
    rewind(state, 50)
    lock.block(True)
    step(wt, state, timeout_s=60)
    lock.block(False)
    rewind(state, 11)
    assert step(wt, state, timeout_s=60)["status"] == "timeout"


def test_a_check_blocked_past_its_limit_is_not_stopped(systemd, lock, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state, timeout_s=60)
    rewind(state, 61)
    lock.block(True)
    assert step(wt, state, timeout_s=60) == {"status": "running"}
    assert systemd.stops == [UNIT]


def test_a_long_gap_between_polls_credits_at_most_one_tick(systemd, lock, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state, timeout_s=60)
    lock.block(True)
    step(wt, state, timeout_s=60)
    rewind(state, 3600)
    step(wt, state, timeout_s=60)
    lock.block(False)
    assert step(wt, state, timeout_s=60)["status"] == "timeout"


def test_a_lock_held_inside_the_unit_is_not_waiting(systemd, lock, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state, timeout_s=60)
    lock.block(True, holder="4243")
    rewind(state, 61)
    assert step(wt, state, timeout_s=60)["status"] == "timeout"


def test_a_record_from_before_lock_waits_still_times_out(systemd, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state, timeout_s=60)
    job = json.loads((state / "check.json").read_text())
    old = {"started": job["started"] - 61, "head": job["head"], "lost": 0}
    (state / "check.json").write_text(json.dumps(old))
    assert step(wt, state, timeout_s=60)["status"] == "timeout"


def test_a_unit_stopped_by_runtime_max_while_blocked_is_rerun_once(systemd, lock, wt, tmp_path):
    state = tmp_path / "state"
    step(wt, state)
    # A lost check's rerun is a separate budget.
    systemd.units.clear()
    age(state, checkjob.START_GRACE_S + 1)
    step(wt, state)
    for expected in ("running", "timeout"):
        lock.block(True)
        step(wt, state)
        systemd.finish(result="timeout", status="0", output="waiting for a test suite\n")
        ran = step(wt, state)
        assert ran["status"] == expected
    assert len(systemd.runs) == 3
    assert ran["output"].endswith("after more than 4 h of waiting.\n")
