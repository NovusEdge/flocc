import re
import subprocess
from pathlib import Path

ROOT = Path(__file__).parent.parent
HOST_VERBS = ("ship", "login", "keys", "host", "doctor", "status", "fleet-status")
VERBS = (*HOST_VERBS, "logs", "attach", "tick", "release", "daytona-start")
CALL = re.compile(r"\bjust (" + "|".join(re.escape(v) for v in VERBS) + r")\b")
# History and dated documents keep the commands they were written with.
KEEP = ("CHANGELOG.md", ".docket/", "docs/superpowers/", "docs/research/")


def output(*argv: str) -> str:
    return subprocess.run(argv, cwd=ROOT, capture_output=True, text=True, check=True).stdout


def tracked() -> list[str]:
    return [f for f in output("git", "ls-files").splitlines() if not f.startswith(KEEP)]


def test_justfile_has_no_wrappers_for_flocc_verbs():
    assert set(output("just", "--summary").split()) & set(VERBS) == set()


def test_nothing_tells_the_reader_to_run_a_removed_recipe():
    hits = [
        f"{name}:{n}: {line.strip()}"
        for name in tracked()
        if (ROOT / name).is_file()
        for n, line in enumerate((ROOT / name).read_text(errors="ignore").splitlines(), 1)
        if CALL.search(line)
    ]
    assert hits == []
