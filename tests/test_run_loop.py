import subprocess

from flocc.tick import run_loop


class FullDisk:
    def write(self, text):
        raise OSError(28, "No space left on device")

    def flush(self):
        pass

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


def test_a_log_that_cannot_be_written_does_not_end_the_loop(tmp_path, monkeypatch, capsys):
    monkeypatch.setattr("pathlib.Path.open", lambda self, mode="r": FullDisk())
    calls, sleeps = [], []
    log = tmp_path / "run.log"
    run_loop(
        ["x"], log, 180, run=lambda *a, **k: calls.append(a), sleep=sleeps.append, iterations=2
    )

    assert calls == [] and sleeps == [180]
    assert capsys.readouterr().err.count("not written") == 2


def test_an_exec_failure_is_logged_and_the_loop_goes_on(tmp_path):
    calls, sleeps = [], []

    def run(argv, stdout, stderr, check):
        calls.append(argv)
        if len(calls) == 1:
            raise FileNotFoundError("flocc")
        return subprocess.CompletedProcess(argv, 0)

    log = tmp_path / "run.log"
    run_loop(["flocc", "tick"], log, 180, run=run, sleep=sleeps.append, iterations=2)

    assert len(calls) == 2
    assert sleeps == [180]
    text = log.read_text()
    assert "--- exit error: flocc" in text
    assert "--- exit 0" in text


def test_a_failing_tick_is_logged_and_the_loop_goes_on(tmp_path):
    codes = iter([1, 0])
    calls, sleeps = [], []

    def run(argv, stdout, stderr, check):
        calls.append(argv)
        stdout.write("tick output\n")
        return subprocess.CompletedProcess(argv, next(codes))

    log = tmp_path / "state" / "run.log"
    run_loop(
        ["flocc", "tick"],
        log,
        180,
        run=run,
        sleep=sleeps.append,
        now=lambda: 0,
        iterations=2,
    )

    assert calls == [["flocc", "tick"], ["flocc", "tick"]]
    assert sleeps == [180]
    text = log.read_text()
    assert text.count("--- tick 1970-01-01T00:00:00Z") == 2
    assert "--- exit 1" in text and "--- exit 0" in text
    assert text.count("tick output") == 2
