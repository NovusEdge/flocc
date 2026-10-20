import re
import subprocess
from pathlib import Path

ROOT = Path(__file__).parent.parent
OLD = re.compile(
    r'linear[-_ ]agents|\bla-(setup|loop|commit|ship)|\.la-commit|"la-"', re.IGNORECASE
)
# A dated doc keeps its file name, so a path naming one is not a use of the old name.
DATED_DOC_PATH = re.compile(r"docs/[\w/.-]*?\d{4}-\d{2}-\d{2}-[\w.-]+")
DATED = re.compile(r"\d{4}-\d{2}-\d{2}-")
# The readers that accept la-nimble's names until 0.5.0.
COMPAT = {"src/flocc/legacy.py", "installer/internal/ship/legacy.go"}
EXEMPT = COMPAT | {"CHANGELOG.md", "tests/test_names.py"}


def _exempt(path: str) -> bool:
    if path in EXEMPT or path.startswith(".docket/"):
        return True
    return path.startswith("docs/") and DATED.match(Path(path).name) is not None


def test_no_tracked_file_uses_the_old_names():
    listed = subprocess.run(
        ["git", "ls-files", "-z"], cwd=ROOT, capture_output=True, text=True, check=True
    ).stdout
    hits = []
    for path in filter(None, listed.split("\0")):
        if _exempt(path):
            continue
        if OLD.search(DATED_DOC_PATH.sub("", path)):
            hits.append(path)
        try:
            text = (ROOT / path).read_text()
        except (UnicodeDecodeError, FileNotFoundError, IsADirectoryError):
            continue
        for n, line in enumerate(text.splitlines(), 1):
            if OLD.search(DATED_DOC_PATH.sub("", line)):
                hits.append(f"{path}:{n}: {line.strip()}")
    assert hits == []
