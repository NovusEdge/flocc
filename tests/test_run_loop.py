import subprocess

from linear_agents.tick import run_loop


def test_a_failing_tick_is_logged_and_the_loop_goes_on(tmp_path):
    codes = iter([1, 0])
    calls, sleeps = [], []

    def run(argv, stdout, stderr, check):
        calls.append(argv)
        stdout.write("tick output\n")
        return subprocess.CompletedProcess(argv, next(codes))

    log = tmp_path / "state" / "run.log"
    run_loop(
        ["linear-agents", "tick"],
        log,
        180,
        run=run,
        sleep=sleeps.append,
        now=lambda: 0,
        iterations=2,
    )

    assert calls == [["linear-agents", "tick"], ["linear-agents", "tick"]]
    assert sleeps == [180]
    text = log.read_text()
    assert text.count("--- tick 1970-01-01T00:00:00Z") == 2
    assert "--- exit 1" in text and "--- exit 0" in text
    assert text.count("tick output") == 2
