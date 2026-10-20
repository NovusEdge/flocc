# flocc Part 1: rename and migration (0.3.0) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename linear-agents to flocc everywhere, keep branches and comments from la-nimble readable, and move the service from la-nimble to a new VM, flocc-nimble.

**Architecture:** One scripted, mechanical rename (Task 1) moves every name in the spec's Part 1 Names table at once and is pinned by a test that fails on any old name outside the exempt files. The behavioural changes then land as small TDD tasks against the renamed code: old-name readers confined to two `legacy` files (Tasks 2 and 5), branch recovery on a new host (Task 3), the branch-moved publish message (Task 4), the desktop hosts directory (Task 6) and the saved answers location (Task 7). Docs and the version bump close the code (Task 8); Tasks 9 to 15 are the controller's manual steps (GitHub, docket, files outside the repo, then the host migration), in the spec's order.

**Tech Stack:** Python 3.13 with uv (pytest, ruff, pyrefly), Go 1.27 (`installer/`, `deploy/`), `just`, git, Incus, Tailscale.

**Spec:** `docs/superpowers/specs/2026-10-20-flocc-design.md`, Part 1.

## Global Constraints

- Release version `0.3.0`: `pyproject.toml`, `plugin/.claude-plugin/plugin.json` and a `## 0.3.0 (<date>)` entry in `CHANGELOG.md` move together (`tests/test_plugin.py::test_the_plugin_and_the_package_share_one_version`).
- Names, verbatim from the spec: Python distribution, module and host CLI `flocc`; Go module `github.com/NovusEdge/flocc`; installer `flocc-setup`, root stage `/tmp/flocc-setup`, bundle `flocc.bundle`, desktop temp dir `flocc-ship-*`; host stage `~/.cache/flocc-setup/`, answers `~/.config/flocc/answers.toml`; desktop hosts `~/.config/flocc/hosts/<name>.toml` with `ssh = "root@<name>"` or `daytona = "<name>"`; plugin `flocc`, `gate.PREFIX` `flocc:`; env vars `FLOCC_{CONFIG,STATE,PHASE,RUN,LEDGER,LAUNCH_GRANTS}`; service user `flocc`, home `/home/flocc`; service repo `~/flocc`, worktrees `~/worktrees`, marketplaces `~/marketplaces`; config `~/.config/flocc/`, state `~/.local/state/flocc/`; tmux `-L flocc`, loop session `flocc-loop`; units `flocc.{service,timer}`, `flocc-tmux.service`; markers `.flocc-commit`, `flocc-commit.lock`, `.claude.json.flocc.tmp`, `refs/heads/flocc-doctor-probe`, ntfy topic prefix `flocc-`; Tailscale hostname default `flocc`; trailer `Flocc: NF-1 impl`; `🤖 flocc:`, `🤖 flocc review`, ntfy title `flocc`.
- Unchanged: Linear labels, branch names `agent/<key>`, session names, the digest issue, the `just` recipe names.
- Old names are read in exactly two files, `src/flocc/legacy.py` and `installer/internal/ship/legacy.go`, both removed in 0.5.0. Nothing writes an old name.
- Every task ends with `just fmt` then `just check` passing (ruff format and lint, pyrefly, shellcheck, gofmt, go vet, pytest, go test); the code below may need ruff's line wrapping.
- Commits: `git commit -s`, no `Co-Authored-By` line, no AI attribution anywhere.
- Comments carry only facts the code cannot show (why, constraints, past bugs); no narration.
- Only the controller touches a real host (zer0-day-nimble, la-nimble, flocc-nimble). Implementer subagents never run `ssh`, `incus` or `just ship`.
- A step that needs a terminal (a huh form, Claude's `/login`) is run by the owner in their own terminal. The controller asks for it and does not use a `!`-prefixed command, which has no TTY.
- From the start of Task 12 until la-nimble's trigger label is switched (Task 12, Step 5) and flocc-nimble is shipped (Task 12, Step 8), the owner adds `claude` to no new issue, so neither host can start one. No `claude`-labelled issue is in Todo today.
- During the drain, never move an issue la-nimble holds to Todo by hand.

## Review Focus

1. A branch whose implementer merged the base after the tests: the merge moves the fork point (the merge-base with the base branch) to the base's new tip, but the tester's commits are not ancestors of that tip, so `fork..branch --first-parent` still lists them and recovery still returns the right pair (the original fork, the tester's last commit). Test: Task 3, `test_recover_tests_after_the_base_was_merged_in`.
2. The trailer carries the upper-case key (`NF-3 test`) while the branch is lower-case (`agent/nf-3`); recovery must match the issue's own key exactly, never `NF-30`. Test: Task 3, `test_recover_tests_needs_the_issues_own_key`.
3. A host that still has a local `agent/<key>` while GitHub's copy has commits it lacks (someone else pushed, or a pushed commit was amended or rebased here): publish must block once, naming the moved branch, not retry three times as a token problem. Tests: Task 4, `test_a_push_rejected_because_the_branch_moved_says_so` and `test_a_branch_that_moved_on_github_blocks_at_once_and_says_so`.
4. An old hosts file holding `user@host` duplicates, `-` lines and names that cannot be file names: each is skipped with a line in the notice, and the file is converted only once. Test: Task 6, `TestConvertOldWritesOneFilePerHostOnce`.
5. A Daytona target (token inside the SSH destination) that runs the old service: refused before anything is staged, with no token in the error. Test: Task 5, `TestShipRefusesAHostThatRunsTheOldService`.

## Order and parallelism

| Task | Depends on | Runs in parallel with |
|---|---|---|
| 1 Mechanical rename | none | none |
| 10 Steps 1 to 3 Re-scope docket records (controller) | 1 | none; done before 2 to 7 are dispatched |
| 2 Old-name readers (Python) | 1, 10 Steps 1 to 3 | 5, 7 |
| 3 Branch recovery | 2 | 4, 6, 7 |
| 4 Branch-moved publish | 2 | 3, 6, 7 |
| 5 Ship refuses an old host | 1, 10 Steps 1 to 3 | 2, 7 |
| 6 Desktop hosts directory | 5 | 3, 4, 7 |
| 7 Saved answers location | 1, 10 Steps 1 to 3 | 2, 3, 4, 5, 6 |
| 8 Docs, changelog, 0.3.0 | 2 to 7 | none |
| 9, 10 Steps 4 and 5, 11 to 15 Controller | 8, in order | none |

Tasks listed as parallel share no files.

**Execution.** Tasks that run in parallel each run in their own git worktree on their own branch, cut from the flocc-spec commit that holds all of the task's dependencies. Each ends with `just fmt` and `just check` over its whole tree and makes a commit, so in one checkout a sibling's red step would fail another task's check and concurrent `git add` could sweep a sibling's files into the wrong commit. The controller merges the branches into flocc-spec in dependency order (task number order within a wave) with `git merge --no-ff --signoff`, and runs `just check` on flocc-spec after each merge. Tasks that do not run in parallel run one at a time on flocc-spec.

---

### Task 1: Mechanical rename

**Files:**
- Create: `tests/test_names.py`
- Move: `src/linear_agents/` to `src/flocc/`; `deploy/linear-agents.service` to `deploy/flocc.service`; `deploy/linear-agents.timer` to `deploy/flocc.timer`; `deploy/linear-agents-tmux.service` to `deploy/flocc-tmux.service`
- Modify: every tracked file except `CHANGELOG.md`, `.docket/*`, dated docs under `docs/` and `uv.lock` (by the script below); `uv.lock` (regenerated); `installer/internal/answers/topic_test.go`; `installer/main_test.go` (`TestStageNamesKeepsOnlyKnownSafeFiles`' `want` order)
- Not committed: the rename script, kept in the implementer's scratchpad

**Interfaces:**
- Consumes: nothing.
- Produces: the package `flocc` (`src/flocc/`), CLI `flocc`, Go module `github.com/NovusEdge/flocc`, and every name in Global Constraints. `tests/test_names.py::test_no_tracked_file_uses_the_old_names`, whose `COMPAT` set already exempts `src/flocc/legacy.py` (Task 2) and `installer/internal/ship/legacy.go` (Task 5). Known gap left for Task 6: the fleet's hosts file path becomes `.config/flocc-setup/hosts`, which Task 6 replaces.

- [ ] **Step 1: Write the failing name test**

Create `tests/test_names.py`:

```python
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
```

- [ ] **Step 2: Run it to see it fail**

Run: `uv run pytest tests/test_names.py -q`
Expected: FAIL, `assert [...] == []` listing roughly 700 lines (paths such as `src/linear_agents/cli.py`, `deploy/linear-agents.service`, lines such as `go.mod:1: module github.com/NovusEdge/linear-agents`).

- [ ] **Step 3: Write the rename script**

Save as `flocc_rename.py` in your scratchpad directory, not in the repo:

```python
"""Renames linear-agents to flocc in a checkout. Run once from the repo root: python3 -I flocc_rename.py ."""

import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(sys.argv[1]).resolve()
MOVES = {
    "src/linear_agents": "src/flocc",
    "deploy/linear-agents.service": "deploy/flocc.service",
    "deploy/linear-agents.timer": "deploy/flocc.timer",
    "deploy/linear-agents-tmux.service": "deploy/flocc-tmux.service",
}
# Order matters: layout paths and the module path come before the bare names they contain.
SUBS = [
    (r"github\.com/NovusEdge/linear-agents", "github.com/NovusEdge/flocc"),
    (r"NovusEdge/linear-agents", "NovusEdge/flocc"),
    (r"agents/linear-agents", "flocc"),
    (r"\bagents/(worktrees|marketplaces)\b", r"\1"),
    (r"LINEAR_AGENTS", "FLOCC"),
    (r"Linear-Agents", "Flocc"),
    (r"linear_agents", "flocc"),
    (r"linear-agents", "flocc"),
    (r"\bla-(setup|loop|commit|ship)\b", r"flocc-\1"),
    (r'"la-" \+', '"flocc-" +'),
    # Example names in docs and tests that the name test does not pin; (?![A-Za-z0-9]) lets
    # la-abc_123 through but not a longer name.
    (r"\bla-(smoke|abc|def|test)(?![A-Za-z0-9])", r"flocc-\1"),
]
DATED_DOC_PATH = re.compile(r"(docs/[\w/.-]*?\d{4}-\d{2}-\d{2}-[\w.-]+)")
DATED = re.compile(r"\d{4}-\d{2}-\d{2}-")
KEEP = {"CHANGELOG.md", "uv.lock", "tests/test_names.py"}


def kept(path: str) -> bool:
    if path in KEEP or path.startswith(".docket/"):
        return True
    return path.startswith("docs/") and DATED.match(Path(path).name) is not None


def rename(text: str) -> str:
    # Odd parts are paths to dated docs, whose file names keep the old name.
    parts = DATED_DOC_PATH.split(text)
    for i in range(0, len(parts), 2):
        for pattern, repl in SUBS:
            parts[i] = re.sub(pattern, repl, parts[i])
    return "".join(parts)


def main() -> None:
    for old, new in MOVES.items():
        subprocess.run(["git", "mv", old, new], cwd=ROOT, check=True)
    listed = subprocess.run(
        ["git", "ls-files", "-z"], cwd=ROOT, capture_output=True, text=True, check=True
    ).stdout
    changed = 0
    for path in filter(None, listed.split("\0")):
        if kept(path):
            continue
        file = ROOT / path
        try:
            text = file.read_bytes().decode()
        except (UnicodeDecodeError, FileNotFoundError, IsADirectoryError):
            continue
        new = rename(text)
        if new != text:
            file.write_bytes(new.encode())
            changed += 1
    print(f"rewrote {changed} files")


main()
```

- [ ] **Step 4: Run the script and regenerate what it does not edit**

Run from the repo root:

```bash
python3 -I "$SCRATCH/flocc_rename.py" .     # SCRATCH is your scratchpad directory
uv lock
uv sync
just fmt
```

Expected: `rewrote 1xx files` (107 for the name table, a few more for the example names); `uv lock` ends with `Resolved 9 packages` (an earlier `uv run` may already have relocked it), and `git diff uv.lock` shows the package renamed to `flocc`; `just fmt` exits 0. `git status --short | grep '^R'` lists the four moves above plus every file under `src/flocc/`.

- [ ] **Step 5: Pin the generated ntfy topic prefix**

Append to `installer/internal/answers/topic_test.go` (it already imports `strings` and `testing`):

```go
func TestRandomTopicHasTheFloccPrefix(t *testing.T) {
	if got := RandomTopic(); !strings.HasPrefix(got, "flocc-") {
		t.Fatal(got)
	}
}
```

- [ ] **Step 6: Reorder the expected stage names**

`os.ReadDir` sorts by name, and `flocc-setup` sorts before `identity.json` where `la-setup` sorted after it, so `TestStageNamesKeepsOnlyKnownSafeFiles` fails with `[e2e.toml flocc-setup flocc.bundle identity.json mp-X.tar plugins.json]`. The script cannot fix an order. In `installer/main_test.go`, replace

```go
	want := []string{"e2e.toml", "identity.json", "flocc-setup", "flocc.bundle", "mp-X.tar", "plugins.json"}
```

(the line as the script left it) with

```go
	want := []string{"e2e.toml", "flocc-setup", "flocc.bundle", "identity.json", "mp-X.tar", "plugins.json"}
```

- [ ] **Step 7: Run the name test and the whole suite**

Run: `uv run pytest tests/test_names.py -q && just check && just build-setup && ls build`
Expected: `1 passed`; `just check` exits 0; `ls build` prints `flocc-setup  flocc-setup-linux-amd64  flocc-setup-linux-arm64`.

- [ ] **Step 8: Read the diff for rewrites that are not names**

Run: `git diff -M HEAD --stat | tail -1` and `git diff -M HEAD --word-diff=porcelain -- . ':!uv.lock' | grep -E '^[-+][^-+]' | sort | uniq -c | sort -rn`
Expected: the second command lists removed and added words in pairs, each an old name and its flocc form from the Names table (for example `-~/agents/worktrees` and `+~/worktrees`), the example names (`la-smoke` to `flocc-smoke`), the reordered `want` list from Step 6, plus lines `just fmt` reflowed. Any other change means a pattern in `SUBS` matched something that is not a name: restore that hunk by hand and rerun Step 7.

- [ ] **Step 9: Commit**

Stage tracked files only: other plans may sit untracked under `docs/`.

```bash
git add -u
git add tests/test_names.py
git status --short --untracked-files=no | grep -v '^[MRAD] '   # prints nothing
git commit -s -m "Rename linear-agents to flocc

Every name in the flocc spec's Part 1 table, by script. A test fails on any old name outside
CHANGELOG.md, .docket/, dated docs and the two compatibility readers."
```

---

### Task 2: Old-name readers in the service

**Files:**
- Create: `src/flocc/legacy.py`
- Modify: `src/flocc/config.py` (add `TRAILER`), `src/flocc/cli.py` (`_commit_msg`, imports), `src/flocc/publisher.py` (untrailed check, imports), `src/flocc/poller.py` (`forward_comments`, imports)
- Test: `tests/test_publisher.py`, `tests/test_poller.py`

**Interfaces:**
- Consumes: Task 1's package `flocc`.
- Produces: `flocc.config.TRAILER = "Flocc"`; `flocc.legacy.TRAILER = "Linear-Agents"`; `flocc.legacy.BOT_PREFIX = "🤖 linear-agents:"`. Task 3 reads both trailer constants.

- [ ] **Step 1: Write the failing tests**

In `tests/test_publisher.py`, add to the imports `from flocc import legacy` and `from flocc.config import TRAILER, RepoConfig` (replacing `from flocc.config import RepoConfig`), then add:

```python
def test_commits_with_either_trailer_count_as_trailed(origin, tmp_path):
    repo, path, claim = setup_claim(origin, tmp_path)
    _trailed_commit(path, "a.py", f"{TRAILER}: NF-5 impl")
    # A rerun of a branch la-nimble started holds commits with its trailer.
    _trailed_commit(path, "b.py", f"{legacy.TRAILER}: NF-5 impl")
    result = publish(claim, repo, "t", "s", token="t", runner=Gh())
    tests = git("rev-parse", "--short", str(claim.tester_sha), cwd=path)
    assert result.untrailed == (tests,)
```

In `tests/test_poller.py`, change `from flocc import ledger` to `from flocc import ledger, legacy` and add:

```python
def test_comments_with_the_old_bot_prefix_are_not_forwarded(rt):
    ledger.insert_claim(rt.con, make("NF-1", ()))
    rt.linear.replies = {
        "id-NF-1": [
            Comment("c1", f"{legacy.BOT_PREFIX} note", "2026-10-09T10:00:00Z", mine=True),
            Comment("c2", "use option B", "2026-10-09T10:05:00Z", mine=True),
        ]
    }
    rt.sessions.alive_names.add("NF-1-test")
    forward_comments(rt)
    assert rt.sessions.sent == [("NF-1-test", "The owner replied on Linear: use option B")]
```

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest tests/test_publisher.py::test_commits_with_either_trailer_count_as_trailed tests/test_poller.py::test_comments_with_the_old_bot_prefix_are_not_forwarded -q`
Expected: both FAIL at collection with `ImportError: cannot import name 'legacy' from 'flocc'`.

- [ ] **Step 3: Add the constants**

Create `src/flocc/legacy.py`:

```python
# What la-nimble wrote before the rename. Its branches and issues move to flocc hosts with these
# in their history, so they are read (never written) until 0.5.0 removes this file.
TRAILER = "Linear-Agents"
BOT_PREFIX = "🤖 linear-agents:"
```

In `src/flocc/config.py`, above `def trailer_enabled`, add:

```python
# Branch recovery finds the tester's commits by this key, so renaming it needs the old key kept
# in legacy.py for branches already pushed.
TRAILER = "Flocc"
```

- [ ] **Step 4: Use them**

In `src/flocc/cli.py`, change `from flocc.config import default_config_path, load` to `from flocc.config import TRAILER, default_config_path, load`, and in `_commit_msg` replace

```python
    trailer = f"Flocc: {Path(key).name} {phase}"
```

with

```python
    trailer = f"{TRAILER}: {Path(key).name} {phase}"
```

In `src/flocc/publisher.py`, change `from flocc.config import RepoConfig` to

```python
from flocc import legacy
from flocc.config import TRAILER, RepoConfig
```

and replace the untrailed `git(...)` call with

```python
        out = git(
            "log",
            f"{claim.base_sha}..{claim.branch}",
            "--format=%h",
            "--invert-grep",
            f"--grep=^{TRAILER}: ",
            f"--grep=^{legacy.TRAILER}: ",
            cwd=wt,
        )
```

In `src/flocc/poller.py`, change `from flocc import ledger` to `from flocc import ledger, legacy`, and in `forward_comments` replace

```python
            if who and not comment.body.startswith(BOT_PREFIX):
```

with

```python
            if who and not comment.body.startswith((BOT_PREFIX, legacy.BOT_PREFIX)):
```

- [ ] **Step 5: Run the tests**

Run: `uv run pytest tests/test_publisher.py tests/test_poller.py tests/test_worktree.py tests/test_names.py -q && just check`
Expected: all pass; `just check` exits 0.

- [ ] **Step 6: Commit**

```bash
git add src/flocc/legacy.py src/flocc/config.py src/flocc/cli.py src/flocc/publisher.py src/flocc/poller.py tests/test_publisher.py tests/test_poller.py
git commit -s -m "Read la-nimble's trailer and bot prefix until 0.5.0

The publisher's untrailed check accepts Linear-Agents: as well as Flocc:, and forward_comments
skips comments with either bot prefix. Both old names live in flocc/legacy.py."
```

---

### Task 3: A branch survives a host move

**Files:**
- Modify: `src/flocc/worktree.py` (`prepare_worktree`, new `_has_ref`, new `recover_tests`, imports), `src/flocc/poller.py` (`_start`, imports)
- Test: `tests/test_worktree.py`, `tests/test_poller.py`

**Interfaces:**
- Consumes: `flocc.config.TRAILER`, `flocc.legacy.TRAILER` (Task 2); `flocc.ledger.remembered_tests`, `forget_tests`, `remember_tests` (existing).
- Produces: `flocc.worktree.recover_tests(worktree: Path, base_branch: str, branch: str, key: str) -> tuple[str, str] | None`, returning `(base_sha, tester_sha)`. `prepare_worktree` keeps its signature and return `tuple[Path, str, str]`.

- [ ] **Step 1: Write the failing worktree tests**

In `tests/test_worktree.py`, add `from flocc import legacy` and `from flocc.config import TRAILER, RepoConfig` (replacing `from flocc.config import RepoConfig`), add `recover_tests` to the `from flocc.worktree import (...)` list, then add:

```python
def _other_host(tmp_path, key="NF-3"):
    """A second clone of the remote, standing in for the host that ran the issue before."""
    other = tmp_path / "other-host"
    subprocess.run(
        ["git", "clone", "-q", str(tmp_path / "origin.git"), str(other)],
        check=True,
        capture_output=True,
    )
    for k, v in (("user.name", "t"), ("user.email", "t@t")):
        git("config", k, v, cwd=other)
    git("checkout", "-qb", f"agent/{key.lower()}", cwd=other)
    return other


def _commit(repo, name, trailer=""):
    (repo / name).write_text(f"{name}\n")
    git("add", name, cwd=repo)
    git("commit", "-qm", f"{name}\n\n{trailer}".strip(), cwd=repo)
    return git("rev-parse", "HEAD", cwd=repo)


def _push(other):
    git("push", "-qf", "origin", "HEAD", cwd=other)


def _recover(origin, tmp_path):
    path, branch, _ = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    return recover_tests(path, "main", branch, "NF-3")


def test_prepare_continues_a_branch_only_the_remote_has(origin, tmp_path):
    other = _other_host(tmp_path)
    head = _commit(other, "t.txt", f"{TRAILER}: NF-3 test")
    _push(other)
    path, branch, base = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    assert (branch, base) == ("agent/nf-3", head)
    assert (path / "t.txt").exists()


def test_prepare_without_a_remote_branch_cuts_one_from_the_base(origin, tmp_path):
    _, _, base = prepare_worktree(RepoConfig(origin), tmp_path / "wt", "NF-3")
    assert base == git("rev-parse", "origin/main", cwd=origin)


def test_recover_tests_takes_the_first_run_of_test_commits(origin, tmp_path):
    other = _other_host(tmp_path)
    fork = git("rev-parse", "HEAD", cwd=other)
    _commit(other, "t1.txt", f"{TRAILER}: NF-3 test")
    last = _commit(other, "t2.txt", f"{legacy.TRAILER}: NF-3 test")
    _commit(other, "impl.txt", f"{TRAILER}: NF-3 impl")
    _commit(other, "t3.txt", f"{TRAILER}: NF-3 test")
    _push(other)
    assert _recover(origin, tmp_path) == (fork, last)


def test_recover_tests_ignores_a_test_trailer_after_an_implementer_commit(origin, tmp_path):
    other = _other_host(tmp_path)
    _commit(other, "impl.txt", f"{TRAILER}: NF-3 impl")
    _commit(other, "t.txt", f"{TRAILER}: NF-3 test")
    _push(other)
    assert _recover(origin, tmp_path) is None


def test_recover_tests_follows_an_amended_tester_commit(origin, tmp_path):
    other = _other_host(tmp_path)
    fork = git("rev-parse", "HEAD", cwd=other)
    _commit(other, "t.txt", f"{TRAILER}: NF-3 test")
    _push(other)
    (other / "t.txt").write_text("amended\n")
    git("commit", "-qa", "--amend", "--no-edit", cwd=other)
    amended = git("rev-parse", "HEAD", cwd=other)
    _push(other)
    assert _recover(origin, tmp_path) == (fork, amended)


def test_recover_tests_needs_the_issues_own_key(origin, tmp_path):
    other = _other_host(tmp_path)
    _commit(other, "t.txt", f"{TRAILER}: NF-30 test")
    _push(other)
    assert _recover(origin, tmp_path) is None


def test_recover_tests_without_a_remote_branch_finds_nothing(origin, tmp_path):
    assert _recover(origin, tmp_path) is None


def test_recover_tests_after_the_base_was_merged_in(origin, tmp_path):
    other = _other_host(tmp_path)
    fork = git("rev-parse", "HEAD", cwd=other)
    tests = _commit(other, "t.txt", f"{TRAILER}: NF-3 test")
    git("checkout", "-q", "main", cwd=other)
    _commit(other, "main.txt")
    git("push", "-q", "origin", "main", cwd=other)
    git("checkout", "-q", "agent/nf-3", cwd=other)
    git("merge", "-q", "--no-edit", "main", cwd=other)
    _push(other)
    # The merge moves the merge-base to main's new tip, but the tester's commit is not an
    # ancestor of it, so the first-parent range still starts at the tests.
    assert _recover(origin, tmp_path) == (fork, tests)


def test_recover_tests_with_no_common_ancestor_finds_nothing(origin, tmp_path):
    other = _other_host(tmp_path)
    git("checkout", "-q", "--orphan", "lone", cwd=other)
    git("rm", "-rfq", ".", cwd=other)
    _commit(other, "t.txt", f"{TRAILER}: NF-3 test")
    git("push", "-qf", "origin", "HEAD:refs/heads/agent/nf-3", cwd=other)
    assert _recover(origin, tmp_path) is None
```

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest tests/test_worktree.py -q -k "remote or recover"`
Expected: FAIL at collection, `ImportError: cannot import name 'recover_tests' from 'flocc.worktree'`.

- [ ] **Step 3: Continue the remote branch in prepare_worktree**

In `src/flocc/worktree.py`, change `from flocc.config import RepoConfig` to

```python
from flocc import legacy
from flocc.config import TRAILER, RepoConfig
```

Add above `prepare_worktree`:

```python
def _has_ref(repo: Path, ref: str) -> bool:
    out = subprocess.run(
        ["git", "rev-parse", "--verify", "-q", ref], cwd=repo, capture_output=True, check=False
    )
    return out.returncode == 0
```

Replace the block from `if f"worktree {path}" not in listed:` through the end of its `else:` branch (the `git("worktree", "add", "-q", "-b", ...)` call) with:

```python
    if f"worktree {path}" not in listed:
        if _has_ref(repo.path, f"refs/heads/{branch}"):
            git("worktree", "add", "-q", str(path), branch, cwd=repo.path)
        else:
            start = f"origin/{repo.base_branch}"
            # Another host ran this issue before: a branch cut fresh from the base would be
            # rejected at the end of the run, after the sessions spent their usage.
            if git("ls-remote", "origin", f"refs/heads/{branch}", cwd=repo.path):
                refspec = f"+refs/heads/{branch}:refs/remotes/origin/{branch}"
                git("fetch", "-q", "origin", refspec, cwd=repo.path)
                start = f"origin/{branch}"
            git("worktree", "add", "-q", "-b", branch, str(path), start, cwd=repo.path)
```

- [ ] **Step 4: Add recover_tests**

Add below `contains` in `src/flocc/worktree.py`:

```python
def recover_tests(
    worktree: Path, base_branch: str, branch: str, key: str
) -> tuple[str, str] | None:
    """(base_sha, tester_sha) of the tester's commits on origin's copy of branch, or None."""
    remote = f"origin/{branch}"
    if not _has_ref(worktree, f"refs/remotes/{remote}"):
        return None
    # Exits 1 when the branches share no history; git() would raise and crash every poll of the issue.
    base = subprocess.run(
        ["git", "merge-base", f"origin/{base_branch}", remote],
        cwd=worktree,
        capture_output=True,
        text=True,
        check=False,
    )
    if base.returncode != 0:
        return None
    fork = base.stdout.strip()
    log = git(
        "log",
        "--reverse",
        "--first-parent",
        "--format=%H%x1f%(trailers:only,unfold)%x1e",
        f"{fork}..{remote}",
        cwd=worktree,
    )
    wanted = {f"{name}: {key} test" for name in (TRAILER, legacy.TRAILER)}
    run = []
    # Only the run that starts the branch: the implementer can write a test trailer itself.
    for record in log.split("\x1e"):
        sha, _, trailers = record.strip().partition("\x1f")
        if not sha:
            continue
        if not wanted & {line.strip() for line in trailers.splitlines()}:
            break
        run.append(sha)
    if not run:
        return None
    return git("rev-parse", f"{run[0]}^", cwd=worktree), run[-1]
```

- [ ] **Step 5: Run the worktree tests**

Run: `uv run pytest tests/test_worktree.py -q`
Expected: all pass.

- [ ] **Step 6: Write the failing poller tests**

In `tests/test_poller.py`, change `from flocc.config import RepoConfig, load` to `from flocc.config import TRAILER, RepoConfig, load` and `from flocc.worktree import prepare_worktree` to `from flocc.worktree import git, prepare_worktree, recover_tests`. In the `rt` fixture, after the `install_hook` monkeypatch, add:

```python
    monkeypatch.setattr("flocc.poller.recover_tests", lambda *a: None)
```

Then add:

```python
def test_tests_recovered_from_the_branch_are_remembered_and_resume_the_implementer(
    rt, monkeypatch
):
    monkeypatch.setattr("flocc.poller.recover_tests", lambda *a: ("sha-fork", "sha-tests"))
    monkeypatch.setattr("flocc.poller.contains", lambda path, sha, ref="HEAD": ref == "HEAD")
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None
    assert (c.phase, c.base_sha, c.tester_sha) == ("impl", "sha-fork", "sha-tests")
    assert ledger.remembered_tests(rt.con, "NF-9") == ("sha-fork", "sha-tests")


def test_remembered_tests_are_not_recovered_again(rt, monkeypatch):
    ledger.remember_tests(rt.con, "NF-9", "sha-old-base", "sha-tests")

    def recover(*a):
        raise AssertionError("recovery ran with tests already remembered")

    monkeypatch.setattr("flocc.poller.recover_tests", recover)
    monkeypatch.setattr("flocc.poller.contains", lambda path, sha, ref="HEAD": ref == "HEAD")
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None and c.phase == "impl"


def test_a_branch_from_another_host_resumes_at_the_implementer(rt, origin, tmp_path, monkeypatch):
    monkeypatch.setattr("flocc.poller.prepare_worktree", prepare_worktree)
    monkeypatch.setattr("flocc.poller.recover_tests", recover_tests)
    backend = replace(rt.cfg.repos["backend"], path=origin)
    rt.cfg = replace(rt.cfg, repos={**rt.cfg.repos, "backend": backend})
    other = tmp_path / "other-host"
    subprocess.run(
        ["git", "clone", "-q", str(tmp_path / "origin.git"), str(other)],
        check=True,
        capture_output=True,
    )
    for k, v in (("user.name", "t"), ("user.email", "t@t")):
        git("config", k, v, cwd=other)
    git("checkout", "-qb", "agent/nf-9", cwd=other)
    fork = git("rev-parse", "HEAD", cwd=other)
    (other / "test_x.py").write_text("assert True\n")
    git("add", ".", cwd=other)
    git("commit", "-qm", f"tests\n\n{TRAILER}: NF-9 test", cwd=other)
    tests = git("rev-parse", "HEAD", cwd=other)
    git("push", "-q", "origin", "agent/nf-9", cwd=other)
    rt.linear.ready = [issue()]
    poll(rt)
    c = ledger.get_claim(rt.con, "NF-9")
    assert c is not None
    assert (c.phase, c.base_sha, c.tester_sha) == ("impl", fork, tests)
    assert ledger.remembered_tests(rt.con, "NF-9") == (fork, tests)
```

- [ ] **Step 7: Run them to see them fail**

Run: `uv run pytest tests/test_poller.py -q`
Expected: FAIL; the fixture's monkeypatch raises `AttributeError: <module 'flocc.poller'> has no attribute 'recover_tests'`.

- [ ] **Step 8: Recover in _start**

In `src/flocc/poller.py`, add `recover_tests` to the `from flocc.worktree import (...)` list (after `prepare_worktree`). In `_start`, replace

```python
    phase, tester_sha = "test", None
    remembered = ledger.remembered_tests(rt.con, issue.key)
    # Tests already merged into the base belong to shipped work, and a reopened issue is new
    # work. After a merge commit, a branch cut fresh from the base contains them too.
    if remembered and contains(path, remembered[1], f"origin/{cfg.repos[repo].base_branch}"):
        ledger.forget_tests(rt.con, issue.key)
        remembered = None
    if remembered and contains(path, remembered[1]):
        (base, tester_sha), phase = remembered, "impl"
```

with

```python
    phase, tester_sha = "test", None
    base_branch = cfg.repos[repo].base_branch
    remembered = ledger.remembered_tests(rt.con, issue.key)
    # Tests already merged into the base belong to shipped work, and a reopened issue is new
    # work. After a merge commit, a branch cut fresh from the base contains them too.
    if remembered and contains(path, remembered[1], f"origin/{base_branch}"):
        ledger.forget_tests(rt.con, issue.key)
        remembered = None
    # A branch another host pushed holds its tester's commits, but this ledger has no record of
    # them, and the publisher's guard and --accept-tests compare against that record.
    if remembered is None:
        remembered = recover_tests(path, base_branch, branch, issue.key)
        if remembered:
            ledger.remember_tests(rt.con, issue.key, *remembered)
    if remembered and contains(path, remembered[1]):
        (base, tester_sha), phase = remembered, "impl"
```

- [ ] **Step 9: Run the tests**

Run: `uv run pytest tests/test_poller.py tests/test_worktree.py -q && just check`
Expected: all pass; `just check` exits 0.

- [ ] **Step 10: Commit**

```bash
git add src/flocc/worktree.py src/flocc/poller.py tests/test_worktree.py tests/test_poller.py
git commit -s -m "Continue a branch another host pushed and recover its tests

With no local agent/<key>, prepare_worktree starts from origin's copy when ls-remote finds one.
When the ledger has no tests for the issue, _start recovers them from the first run of commits
after the fork point carrying a <KEY> test trailer, either name, and remembers them."
```

---

### Task 4: Name a push rejected because the branch moved

**Files:**
- Modify: `src/flocc/publisher.py` (`BranchMoved`, push in `publish`), `src/flocc/supervisor.py` (`_publish_failed`, imports)
- Test: `tests/test_publisher.py`, `tests/test_supervisor.py`

**Interfaces:**
- Consumes: `publish` as Task 2 left it.
- Produces: `flocc.publisher.BranchMoved(subprocess.CalledProcessError)`, whose `str()` is `GitHub's copy of the branch has commits this one lacks (another host or a person pushed, or a pushed commit was rewritten)`; escalation cause `branch-moved`.

- [ ] **Step 1: Write the failing tests**

In `tests/test_publisher.py`, add `import pytest` and add `BranchMoved` to the `from flocc.publisher import (...)` list, then:

```python
def test_a_push_rejected_because_the_branch_moved_says_so(origin, tmp_path):
    repo, _, claim = setup_claim(origin, tmp_path)
    other = tmp_path / "other-host"
    subprocess.run(
        ["git", "clone", "-q", str(tmp_path / "origin.git"), str(other)],
        check=True,
        capture_output=True,
    )
    for k, v in (("user.name", "t"), ("user.email", "t@t")):
        git("config", k, v, cwd=other)
    git("checkout", "-qb", claim.branch, cwd=other)
    (other / "theirs.txt").write_text("x\n")
    git("add", ".", cwd=other)
    git("commit", "-qm", "pushed from elsewhere", cwd=other)
    git("push", "-q", "origin", claim.branch, cwd=other)
    with pytest.raises(BranchMoved) as exc:
        publish(claim, repo, "t", "s", token="t", runner=Gh())
    assert str(exc.value) == (
        "GitHub's copy of the branch has commits this one lacks"
        " (another host or a person pushed, or a pushed commit was rewritten)"
    )
    assert "[rejected]" in exc.value.stderr
```

In `tests/test_supervisor.py`, change `from flocc.publisher import PublishResult` to `from flocc.publisher import BranchMoved, PublishResult` and add:

```python
def test_a_branch_that_moved_on_github_blocks_at_once_and_says_so(rt):
    def moved(c, t, s):
        stderr = " ! [rejected]        agent/nf-1 -> agent/nf-1 (fetch first)\n"
        raise BranchMoved(1, ["git", "push"], "", stderr)

    rt.publish_fn = moved
    add(rt, phase="impl", tester_sha="sha-tests")
    done(rt)
    supervise(rt, current(rt))
    assert current(rt).state == "blocked"
    [esc] = rt.notifier.sent
    assert esc.cause == "NF-1:branch-moved"
    assert "GitHub's copy of the branch has commits this one lacks" in esc.summary
    assert "token" not in esc.action
    assert "flocc release --keep-worktree NF-1" in esc.action
```

In the same file, `test_publish_that_keeps_failing_blocks` uses a non-fast-forward stderr for the generic retry path; give it a token failure instead so it tests what its name says. Replace its `rejected` function and last assertions:

```python
    def rejected(c, t, s):
        raise subprocess.CalledProcessError(
            128, ["git", "push"], stderr="fatal: Authentication failed for 'https://github.com/o/r'"
        )
```

```python
    assert esc.cause == "NF-1:publish-failed" and "Authentication failed" in esc.summary
    assert "flocc release --keep-worktree NF-1" in esc.action
```

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest tests/test_publisher.py tests/test_supervisor.py -q`
Expected: FAIL at collection, `ImportError: cannot import name 'BranchMoved' from 'flocc.publisher'`.

- [ ] **Step 3: Raise BranchMoved from publish**

In `src/flocc/publisher.py`, below `_NOISE`, add:

```python
# git's words for a remote branch holding commits the push lacks; `[remote rejected]` (a hook or
# branch protection) is a different failure.
_MOVED = re.compile(r"\[rejected\].*\((non-fast-forward|fetch first)\)")


# The same push is rejected until someone reconciles the branches, so retrying cannot help.
class BranchMoved(subprocess.CalledProcessError):
    def __str__(self) -> str:
        return (
            "GitHub's copy of the branch has commits this one lacks"
            " (another host or a person pushed, or a pushed commit was rewritten)"
        )
```

Wrap the push `runner(...)` call in `publish`:

```python
    try:
        runner(
            [
                "git",
                "-c",
                "credential.helper=",
                "-c",
                "credential.helper=!gh auth git-credential",
                "push",
                "-q",
                "-u",
                "origin",
                claim.branch,
            ],
            cwd=wt,
            env=env,
            check=True,
            capture_output=True,
            text=True,
        )
    except subprocess.CalledProcessError as exc:
        if _MOVED.search(exc.stderr or ""):
            raise BranchMoved(exc.returncode, exc.cmd, exc.output, exc.stderr) from exc
        raise
```

- [ ] **Step 4: Block at once in the supervisor**

In `src/flocc/supervisor.py`, change `from flocc.publisher import review_comment` to `from flocc.publisher import BranchMoved, review_comment`. In `_publish_failed`, replace

```python
    key = claim.issue_key
    counter = f"publish_failures:{key}"
    fails = int(ledger.meta_get(rt.con, counter) or 0) + 1
    detail = (getattr(exc, "stderr", None) or "").strip()
```

with

```python
    key = claim.issue_key
    counter = f"publish_failures:{key}"
    detail = (getattr(exc, "stderr", None) or "").strip()
    if isinstance(exc, BranchMoved):
        print(f"{key}: publish failed: {exc} {detail}", file=sys.stderr)
        _block(
            rt,
            claim,
            "branch-moved",
            f"Publishing {claim.branch} failed: {exc}.\n{detail[-1500:]}".strip(),
            f"See who pushed {claim.branch}, merge origin/{claim.branch} into the worktree's"
            f" branch or reset it to that, then run flocc release --keep-worktree {key}",
        )
        ledger.meta_set(rt.con, counter, "0")
        return
    fails = int(ledger.meta_get(rt.con, counter) or 0) + 1
```

- [ ] **Step 5: Run the tests**

Run: `uv run pytest tests/test_publisher.py tests/test_supervisor.py -q && just check`
Expected: all pass; `just check` exits 0.

- [ ] **Step 6: Commit**

```bash
git add src/flocc/publisher.py src/flocc/supervisor.py tests/test_publisher.py tests/test_supervisor.py
git commit -s -m "Block at once when the branch moved on GitHub

A non-fast-forward push now raises BranchMoved, which blocks the claim with cause branch-moved
and says another host or a person pushed, instead of three retries that name the token."
```

---

### Task 5: Ship refuses a host that runs the old service

**Files:**
- Create: `installer/internal/ship/legacy.go`
- Modify: `installer/internal/ship/ship.go` (`shipTo`)
- Test: `installer/internal/ship/ship_test.go`

**Interfaces:**
- Consumes: `sshCmd`, `Dest`, `ship` (existing, renamed by Task 1).
- Produces: `const legacyUser = "linear-agents"` and `func refuseLegacy(ctx context.Context, r host.Runner, dest Dest) error` in package `ship`; Task 6 adds to `legacy.go`. The refusal names the `docs/vm-setup.md` section `Hosts from before 0.3.0`, which Task 8 writes.

- [ ] **Step 1: Write the failing test**

Append to `installer/internal/ship/ship_test.go`:

```go
func TestShipRefusesAHostThatRunsTheOldService(t *testing.T) {
	f := host.NewFake().
		On("ssh h uname -m", 0, "x86_64\n").
		On("ssh h id -u "+legacyUser, 0, "998\n")
	err := ship(context.Background(), f, t.TempDir(), Dest{SSH: "h"}, "", false)
	if err == nil || !strings.Contains(err.Error(), "a second poller") {
		t.Fatalf("%v", err)
	}
	if len(f.Calls) != 2 {
		t.Fatalf("ran past the refusal: %v", f.Calls)
	}
	dest := Dest{SSH: "tok9@ssh.example.test", Token: "tok9"}
	f = host.NewFake().
		On("ssh tok9@ssh.example.test uname -m", 0, "x86_64\n").
		On("ssh tok9@ssh.example.test id -u "+legacyUser, 0, "998\n")
	err = ship(context.Background(), f, t.TempDir(), dest, "", false)
	if err == nil || strings.Contains(err.Error(), "tok9") {
		t.Fatalf("%v", err)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./installer/internal/ship/ -run TestShipRefusesAHostThatRunsTheOldService`
Expected: FAIL to build, `undefined: legacyUser`.

- [ ] **Step 3: Add the refusal**

Create `installer/internal/ship/legacy.go`:

```go
package ship

import (
	"context"
	"fmt"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// The service user before the rename. Its timer is a user unit, so the user alone tells.
const legacyUser = "linear-agents"

// refuseLegacy stops a ship to a host still running the service under its old name: the new
// service would install beside it, and both pollers would claim the same issues.
func refuseLegacy(ctx context.Context, r host.Runner, dest Dest) error {
	res, err := r.Run(ctx, sshCmd(dest, false, "id -u "+legacyUser))
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return nil
	}
	return fmt.Errorf("the host has a %s user, so it runs the service from before 0.3.0, and a second poller there would claim the same issues; build a new host instead (docs/vm-setup.md, Hosts from before 0.3.0)", legacyUser)
}
```

In `installer/internal/ship/ship.go`, in `shipTo`, right after the `archFor` error check, add:

```go
	if err := refuseLegacy(ctx, r, dest); err != nil {
		return err
	}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./installer/... && just check`
Expected: `ok` for every package; `just check` exits 0. `TestShipClearsTheStageWithSudoOnATerminal` still passes because the Fake answers the unregistered `id -u` probe with code 127.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/ship/legacy.go installer/internal/ship/ship.go installer/internal/ship/ship_test.go
git commit -s -m "Refuse to ship to a host with the old service user

A second poller beside la-nimble's would claim the same issues. Probed with id -u over the same
SSH as uname, before anything is staged."
```

---

### Task 6: Desktop hosts directory

**Files:**
- Create: `installer/internal/hosts/hosts.go`, `installer/internal/hosts/hosts_test.go`
- Modify: `installer/internal/ship/legacy.go` (add `legacyHostsFile`), `installer/internal/ship/fleet.go` (`discover`, `FleetMain`; delete `readHostsFile` and the `bufio` import), `installer/internal/ship/fleet_test.go` (delete `TestReadHostsFileSkipsBlanksAndComments`, replace `TestDiscoverSkipsDaytonaWithoutItsConfigAndKeepsTheHostsFile`), `docs/vm-setup.md` (Fleet view section)

**Interfaces:**
- Consumes: `legacy.go` from Task 5; `FleetHost`, `discover`, `FleetMain` (existing).
- Produces, agreed with the Part 2b plan (which embeds `answers.Answers` in `Host`), package `github.com/NovusEdge/flocc/installer/internal/hosts`:
  - `type Host struct { Name string `toml:"-"`; SSH string `toml:"ssh,omitempty"`; Daytona string `toml:"daytona,omitempty"` }`
  - `func (h Host) Target() string`: `daytona:<Daytona>` or the SSH destination
  - `func Dir(home string) string`: `<home>/.config/flocc/hosts`
  - `func Load(dir, name string) (Host, error)`: exactly one of `ssh` and `daytona`; an ssh destination may not start with `-`
  - `func List(dir string) ([]Host, error)`: the hosts that load, sorted by name, and `errors.Join` of the files that do not; a missing dir is no hosts and no error
  - `func Save(dir string, h Host) error`: dir mode 700, file mode 600
  - `func ConvertOld(oldFile, dir string) (notice string, err error)`: one host per line of the old file, named by the destination without `user@` and from the first dot on; the old file renamed to `<oldFile>.converted`; `""` when there is no old file
  - in package `ship`: `const legacyHostsFile = ".config/la-setup/hosts"`; `discover(ctx, daytonaConfig, hostsDir string, client *http.Client) (found []FleetHost, notes []string)` reading `hosts.List`.

- [ ] **Step 1: Write the failing tests**

Create `installer/internal/hosts/hosts_test.go`:

```go
package hosts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSaveLoadAndTarget(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hosts")
	if err := Save(dir, Host{Name: "flocc-nimble", SSH: "root@flocc-nimble"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "flocc-nimble.toml"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info, err)
	}
	h, err := Load(dir, "flocc-nimble")
	if err != nil || h.Name != "flocc-nimble" || h.Target() != "root@flocc-nimble" {
		t.Fatalf("%+v %v", h, err)
	}
	if got := (Host{Name: "sb", Daytona: "sb"}).Target(); got != "daytona:sb" {
		t.Fatal(got)
	}
	if got := Dir("/home/u"); got != "/home/u/.config/flocc/hosts" {
		t.Fatal(got)
	}
	if err := Save(dir, Host{Name: "../x", SSH: "x"}); err == nil {
		t.Fatal("a name outside dir was saved")
	}
}

func TestListReturnsGoodHostsByNameAndNamesTheBadOnes(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "zed.toml"), `ssh = "root@zed"`+"\n")
	write(t, filepath.Join(dir, "sb.toml"), `daytona = "sb"`+"\n")
	write(t, filepath.Join(dir, "evil.toml"), `ssh = "-oProxyCommand=id"`+"\n")
	write(t, filepath.Join(dir, "both.toml"), "ssh = \"a\"\ndaytona = \"b\"\n")
	write(t, filepath.Join(dir, "broken.toml"), "ssh = \n")
	write(t, filepath.Join(dir, "notes.txt"), "not a host\n")
	list, err := List(dir)
	if len(list) != 2 || list[0].Name != "sb" || list[1].Name != "zed" {
		t.Fatalf("%+v", list)
	}
	for _, bad := range []string{"both.toml", "broken.toml", "evil.toml"} {
		if err == nil || !strings.Contains(err.Error(), bad) {
			t.Errorf("the error does not name %s: %v", bad, err)
		}
	}
	if list, err := List(filepath.Join(dir, "none")); list != nil || err != nil {
		t.Fatalf("%+v %v", list, err)
	}
}

func TestConvertOldWritesOneFilePerHostOnce(t *testing.T) {
	home := t.TempDir()
	old := filepath.Join(home, "old", "hosts")
	dir := Dir(home)
	write(t, old, "# tailnet\nroot@la-nimble\nbox.tail1.ts.net\n-oProxyCommand=id\nuser@box.tail1.ts.net\nroot@a/b\n")
	msg, err := ConvertOld(old, dir)
	if err != nil {
		t.Fatal(err)
	}
	list, err := List(dir)
	got := map[string]string{}
	for _, h := range list {
		got[h.Name] = h.SSH
	}
	if err != nil || len(got) != 2 || got["la-nimble"] != "root@la-nimble" || got["box"] != "box.tail1.ts.net" {
		t.Fatalf("%v %v", got, err)
	}
	for _, want := range []string{"-oProxyCommand=id", "user@box.tail1.ts.net", "root@a/b", old + ".converted"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the notice lacks %q:\n%s", want, msg)
		}
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the old file would be converted again")
	}
	if msg, err := ConvertOld(old, dir); msg != "" || err != nil {
		t.Fatalf("%q %v", msg, err)
	}
}
```

In `installer/internal/ship/fleet_test.go`, add the import `"github.com/NovusEdge/flocc/installer/internal/hosts"`, delete `TestReadHostsFileSkipsBlanksAndComments` (`TestConvertOldWritesOneFilePerHostOnce` covers that parsing now), and replace `TestDiscoverSkipsDaytonaWithoutItsConfigAndKeepsTheHostsFile` with:

```go
func TestDiscoverSkipsDaytonaWithoutItsConfigAndKeepsTheHostsDir(t *testing.T) {
	dir := t.TempDir()
	hostsDir := filepath.Join(dir, "hosts")
	for _, h := range []hosts.Host{{Name: "box1", SSH: "root@box1"}, {Name: "sb", Daytona: "sb"}} {
		if err := hosts.Save(hostsDir, h); err != nil {
			t.Fatal(err)
		}
	}
	got, notes := discover(context.Background(), filepath.Join(dir, "no-config.json"), hostsDir, http.DefaultClient)
	if len(got) != 1 || got[0].Name != "box1" || got[0].Target != "root@box1" || len(notes) != 1 || !strings.Contains(notes[0], "skipped") {
		t.Fatalf("%+v %q", got, notes)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/internal/hosts/ ./installer/internal/ship/`
Expected: FAIL to build: `undefined: Save`, `undefined: Load` and the others in package `hosts`; package `ship` cannot import `installer/internal/hosts`.

- [ ] **Step 3: Add the hosts package**

Create `installer/internal/hosts/hosts.go`:

```go
// Package hosts is the desktop's list of flocc hosts: one <name>.toml per host in Dir.
package hosts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Host is one host file. It sets exactly one of SSH and Daytona.
type Host struct {
	Name    string `toml:"-"`
	SSH     string `toml:"ssh,omitempty"`
	Daytona string `toml:"daytona,omitempty"`
}

func (h Host) Target() string {
	if h.Daytona != "" {
		return "daytona:" + h.Daytona
	}
	return h.SSH
}

func Dir(home string) string { return filepath.Join(home, ".config", "flocc", "hosts") }

// A host's name is also its file name.
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func checkName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("host name %q: start with a letter or digit, then use letters, digits, '.', '_' or '-'", name)
	}
	return nil
}

func Load(dir, name string) (Host, error) {
	if err := checkName(name); err != nil {
		return Host{}, err
	}
	b, err := os.ReadFile(filepath.Join(dir, name+".toml"))
	if err != nil {
		return Host{}, err
	}
	var h Host
	if err := toml.Unmarshal(b, &h); err != nil {
		return Host{}, fmt.Errorf("%s.toml: %w", name, err)
	}
	h.Name = name
	switch {
	case (h.SSH == "") == (h.Daytona == ""):
		return Host{}, fmt.Errorf("%s.toml: set exactly one of ssh and daytona", name)
	case strings.HasPrefix(h.SSH, "-"):
		// ssh would read it as an option (-oProxyCommand=... runs a local command).
		return Host{}, fmt.Errorf("%s.toml: an ssh destination cannot start with '-'", name)
	}
	return h, nil
}

// List returns the hosts in dir sorted by name. A file that does not load is left out and named in
// the error, so one bad file hides no other host.
func List(dir string) ([]Host, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, err
	}
	var list []Host
	var errs []error
	for _, p := range paths {
		h, err := Load(dir, strings.TrimSuffix(filepath.Base(p), ".toml"))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		list = append(list, h)
	}
	return list, errors.Join(errs...)
}

func Save(dir string, h Host) error {
	if err := checkName(h.Name); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := toml.Marshal(h)
	if err != nil {
		return err
	}
	p := filepath.Join(dir, h.Name+".toml")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return err
	}
	// WriteFile applies the mode only when it creates the file.
	return os.Chmod(p, 0o600)
}

// ConvertOld turns the pre-flocc hosts file, one ssh destination per line, into host files in
// dir, then renames it to <oldFile>.converted so it is converted once. A host is named by its
// destination without user@ and from the first dot on. It returns the notice for the user, or ""
// when there is no old file.
func ConvertOld(oldFile, dir string) (string, error) {
	b, err := os.ReadFile(oldFile)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var lines []string
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "-") {
			lines = append(lines, fmt.Sprintf("dropped %q: an ssh destination cannot start with '-'", t))
			continue
		}
		name, _, _ := strings.Cut(t[strings.LastIndex(t, "@")+1:], ".")
		if checkName(name) != nil {
			lines = append(lines, fmt.Sprintf("skipped %q: %q cannot be a host name", t, name))
			continue
		}
		p := filepath.Join(dir, name+".toml")
		if _, err := os.Stat(p); err == nil {
			lines = append(lines, fmt.Sprintf("skipped %q: %s exists", t, p))
			continue
		}
		if err := Save(dir, Host{Name: name, SSH: t}); err != nil {
			return "", err
		}
		lines = append(lines, fmt.Sprintf("%s -> %s", t, p))
	}
	if err := os.Rename(oldFile, oldFile+".converted"); err != nil {
		return "", err
	}
	return fmt.Sprintf("converted %s (kept as %s.converted) into %s:\n  %s", oldFile, oldFile, dir, strings.Join(lines, "\n  ")), nil
}
```

- [ ] **Step 4: Run the hosts tests**

Run: `go test ./installer/internal/hosts/`
Expected: `ok`.

- [ ] **Step 5: Read the directory in the fleet**

Append to `installer/internal/ship/legacy.go`:

```go
// The desktop's host list before the rename, one ssh destination per line.
const legacyHostsFile = ".config/la-setup/hosts"
```

In `installer/internal/ship/fleet.go`, add the import `"github.com/NovusEdge/flocc/installer/internal/hosts"`, delete `readHostsFile` with its comment, remove `"bufio"` from the imports, and replace `discover` with:

```go
// discover lists the Daytona org's sandboxes and the ssh hosts in hostsDir. Neither failing stops
// the other: the problems come back as notes for the view to print.
func discover(ctx context.Context, daytonaConfig, hostsDir string, client *http.Client) (found []FleetHost, notes []string) {
	auth, err := loadDaytona(daytonaConfig)
	switch {
	case errors.Is(err, os.ErrNotExist):
		notes = append(notes, "daytona: no CLI config, skipped")
	case err != nil:
		notes = append(notes, "daytona: "+err.Error())
	default:
		sbs, err := auth.sandboxes(ctx, client)
		if err != nil {
			notes = append(notes, "daytona: "+err.Error())
		}
		sort.Slice(sbs, func(i, j int) bool { return sbs[i].Name < sbs[j].Name })
		for _, s := range sbs {
			found = append(found, FleetHost{Name: s.Name, Target: "daytona:" + s.Name, State: strings.ToLower(s.State), ID: s.ID, CPU: s.CPU, MemGiB: s.Memory, DiskGiB: s.Disk, Daytona: true})
		}
	}
	list, err := hosts.List(hostsDir)
	if err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			notes = append(notes, "hosts: "+line)
		}
	}
	// Daytona hosts came from the API above.
	for _, h := range list {
		if h.SSH != "" {
			found = append(found, FleetHost{Name: h.Name, Target: h.SSH})
		}
	}
	return found, notes
}
```

In `FleetMain`, replace

```go
	hostsFile := filepath.Join(home, ".config/flocc-setup/hosts")
```

with

```go
	hostsDir := hosts.Dir(home)
	if msg, err := hosts.ConvertOld(filepath.Join(home, legacyHostsFile), hostsDir); err != nil {
		fmt.Fprintln(os.Stderr, "fleet: the old hosts file was not converted:", err)
	} else if msg != "" {
		fmt.Fprintln(os.Stderr, "fleet:", msg)
	}
```

and in the loop, rename the local `hosts` (it would shadow the package) and point the note at the directory:

```go
		found, notes := discover(ctx, daytonaConfig, hostsDir, client)
		rows := collect(ctx, host.Exec{}, cache, found)
```

```go
		if len(found) == 0 {
			notes = append(notes, "no hosts: log in with daytona login or add <name>.toml holding ssh = \"root@<name>\" to "+hostsDir)
		}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./installer/... && just check`
Expected: `ok` for every package; `just check` exits 0.

- [ ] **Step 7: Update the Fleet view docs**

In `docs/vm-setup.md`, section `### Fleet view`, replace the paragraph starting "The hosts are every sandbox" and its code block with:

````markdown
The hosts are every sandbox in the active Daytona organization, read with the Daytona CLI's login,
plus one file per ssh host in `~/.config/flocc/hosts/`, named `<name>.toml`:

```toml
ssh = "root@flocc-nimble"
```

A file sets `ssh` or, for a Daytona sandbox, `daytona = "<sandbox>"`; the fleet lists sandboxes
from the Daytona API either way. The first `just fleet-status` after 0.3.0 converts the old
one-destination-per-line hosts file into these files, naming each host by its destination without
`user@` and from the first dot on (`root@box.tail1.ts.net` becomes `box.toml`), renames the old file
to `hosts.converted` and prints what it did.
````

and replace "Lines in the hosts file starting with `-` are ignored, since ssh would read them as options." with "A host file whose `ssh` starts with `-` is skipped with a note, since ssh would read it as an option."

- [ ] **Step 8: Commit**

```bash
git add installer/internal/hosts installer/internal/ship/legacy.go installer/internal/ship/fleet.go installer/internal/ship/fleet_test.go docs/vm-setup.md
git commit -s -m "Read desktop hosts from ~/.config/flocc/hosts/<name>.toml

The fleet converts the old hosts file once, renames it to hosts.converted and prints what it
wrote and what it skipped."
```

---

### Task 7: Saved answers live in ~/.config/flocc

**Files:**
- Modify: `installer/main.go` (`answersRel`, `savedAnswersPath`, `dropAnswersCopy`, `run`)
- Test: `installer/main_test.go`

**Interfaces:**
- Consumes: Task 1's names.
- Produces: `func savedAnswersPath(home string) string` returning `<home>/.config/flocc/answers.toml`; `dropAnswersCopy(path string)` now removes any path. Part 2a reads saved answers from this path.

- [ ] **Step 1: Write the failing tests**

In `installer/main_test.go`, replace `TestDropAnswersCopyKeepsSavedState` with:

```go
func TestSavedAnswersLiveOutsideTheStage(t *testing.T) {
	got := savedAnswersPath("/home/flocc")
	if got != "/home/flocc/.config/flocc/answers.toml" || strings.HasPrefix(got, serviceStage) {
		t.Fatal(got)
	}
}

func TestHandoffRemovesAStagedAnswersToml(t *testing.T) {
	stage := fillStage(t, "flocc-setup", "answers.toml")
	f := host.NewFake().
		On("chown -R root:flocc "+stage, 0, "").
		On("chmod -R g+rX,o-rwx "+stage, 0, "").
		On("sudo -iu flocc sh -c "+copyScript(rootStage, []string{"answers.toml", "flocc-setup"}), 0, "")
	if err := handoff(context.Background(), f, stage, "answers.toml", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stage, "answers.toml")); err == nil {
		t.Fatal("a staged answers.toml, which carries tokens, was kept")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./installer/ -run 'TestSavedAnswersLiveOutsideTheStage|TestHandoffRemovesAStagedAnswersToml'`
Expected: FAIL to build, `undefined: savedAnswersPath`.

- [ ] **Step 3: Move the saved answers**

In `installer/main.go`, below `const serviceStage = ...`, add:

```go
// The service phase's saved answers: outside ~/.cache, which may be cleared, and outside the
// stage, where every --answers copy is removed after its run.
func savedAnswersPath(home string) string {
	return filepath.Join(home, ".config/flocc/answers.toml")
}
```

Replace `dropAnswersCopy` and its comment with:

```go
// dropAnswersCopy removes a --answers file, which carries tokens, once the run no longer needs it.
func dropAnswersCopy(path string) { os.Remove(path) }
```

In `run`, replace `env.AnswersPath = filepath.Join(stage, "answers.toml")` with `env.AnswersPath = savedAnswersPath(env.Home)`.

- [ ] **Step 4: Run the tests**

Run: `go test ./installer/... && just check`
Expected: `ok` for every package; `just check` exits 0. `answers.Save` creates `~/.config/flocc` with mode 700 itself (`os.MkdirAll(filepath.Dir(path), 0o700)`).

- [ ] **Step 5: Commit**

```bash
git add installer/main.go installer/main_test.go
git commit -s -m "Save the service phase's answers in ~/.config/flocc/answers.toml

The stage now holds only what a ship brought, so a staged copy named answers.toml is removed
after its run like any other --answers copy."
```

---

### Task 8: Docs, changelog and version 0.3.0

**Files:**
- Modify: `docs/vm-setup.md` (new sections `A VM on an Incus machine` and `Hosts from before 0.3.0`), `README.md` (Current design), `CHANGELOG.md`, `pyproject.toml`, `plugin/.claude-plugin/plugin.json`, `uv.lock`

**Interfaces:**
- Consumes: every earlier task; the section name `Hosts from before 0.3.0` used in Task 5's refusal.
- Produces: release 0.3.0; the Incus steps Task 12 follows.

- [ ] **Step 1: Add the Incus section to docs/vm-setup.md**

Insert after the Daytona paragraph that ends "pass `--cpu 2 --memory 4096` instead of `--snapshot`." (before `## What the installer cannot do`):

````markdown
## A VM on an Incus machine

The owner's host is an Incus VM on the laptop `zer0-day-nimble`. Agents run unreviewed code, so it
is a VM rather than a container. The machine is shared: never restart Docker or Incus on it, since
a Docker restart on 2026-10-20 killed another session's build.

The machine carries the bridge and forwarding settings applied by hand on 2026-10-19. Check them
before building a VM:

```bash
incus network get incusbr0 ipv4.nat                  # true
sudo firewall-cmd --get-zone-of-interface=incusbr0    # trusted
sudo iptables -S FORWARD | head -1                    # -P FORWARD ACCEPT
grep ip-forward-no-drop /etc/docker/daemon.json       # Docker leaves FORWARD alone
```

They are broad: the VM can reach every service on the machine. `flocc host create` (0.6.0)
replaces them with rules scoped to `incusbr0`.

Size a new VM like the existing one and check the machine has room for both:

```bash
incus config show la-nimble --expanded | grep -E 'limits\.(cpu|memory)|size:'
free -g
```

Then, with la-nimble's `limits.cpu`, `limits.memory` and root disk `size` in place of the example
values:

```bash
incus launch images:debian/13 flocc-nimble --vm -c boot.autostart=true \
  -c limits.cpu=4 -c limits.memory=8GiB -d root,size=40GiB
until incus exec flocc-nimble -- true 2>/dev/null; do sleep 2; done   # the Incus agent starts a few seconds after launch
incus exec flocc-nimble -- sh -c 'until ip -4 -o addr show scope global | grep -q inet; do sleep 2; done'
incus exec flocc-nimble -- sh -c 'apt-get update -q && apt-get install -yq curl && curl -fsSI https://pkgs.tailscale.com >/dev/null'
incus exec flocc-nimble -- sh -c 'curl -fsSL https://tailscale.com/install.sh | sh'
incus exec flocc-nimble -- tailscale up --ssh --hostname=flocc-nimble
```

`tailscale up` prints a login URL; open it and log in as the owner. In the Tailscale admin console,
disable key expiry for the node. `just ship root@flocc-nimble` then reaches it over Tailscale SSH.
````

- [ ] **Step 2: Add the section for old hosts**

Insert before `## Smoke test`:

```markdown
## Hosts from before 0.3.0

`just ship` refuses a host set up before 0.3.0: it has the service user of the old name, and a
second poller there would claim the same issues. Build a new host and drain the old one; the steps
used for la-nimble are tasks 12, 14 and 15 of
`docs/superpowers/plans/2026-10-20-flocc-part-1-rename.md`.

A branch the old host pushed continues on the new one: with no local `agent/<key>`, the new host
fetches it, finds the tester's commits on it and resumes at the implementer. An `--accept-tests`
grant made on the old host does not move; grant it again. A push the old host made after the new
one started is reported as "GitHub's copy of the branch has commits this one lacks".
```

- [ ] **Step 3: Point the README at the rename**

In `README.md`, section `## Current design`, after the sentence ending "and host setup in `docs/vm-setup.md`.", add: "The rename to flocc and the work after it are in `docs/superpowers/specs/2026-10-20-flocc-design.md`."

- [ ] **Step 4: Bump the version**

Set `version = "0.3.0"` in `pyproject.toml` and `"version": "0.3.0"` in `plugin/.claude-plugin/plugin.json`, then run `uv lock`.
Expected: `Updated flocc v0.2.0 -> v0.3.0`.

- [ ] **Step 5: Add the changelog entry**

Run `date +%F` for the date, then insert above `## 0.2.0 (2026-10-20)` in `CHANGELOG.md`:

```markdown
## 0.3.0 (<the date from date +%F>)

linear-agents is now flocc. A host set up before 0.3.0 is replaced by a new one, not upgraded.

- Every name changed: package, CLI and plugin `flocc`, env vars `FLOCC_*`, service user `flocc`
  with its repo in `~/flocc` and worktrees in `~/worktrees`, units `flocc.{service,timer}` and
  `flocc-tmux.service`, installer `flocc-setup`, commit trailer `Flocc:`, comments starting
  `🤖 flocc`. The full table is in `docs/superpowers/specs/2026-10-20-flocc-design.md`.
- `just ship` refuses a host that has a `linear-agents` user.
- A host with no local `agent/<key>` continues the branch from GitHub and recovers the tester's
  commits from it, so a rerun on another host resumes at the implementer. `--accept-tests` grants
  do not move with it.
- A push rejected because the branch moved on GitHub blocks at once and says so, instead of three
  retries that point at the token.
- `Linear-Agents:` trailers and `🤖 linear-agents:` comments are still read until 0.5.0.
- The desktop's hosts are `~/.config/flocc/hosts/<name>.toml`; `just fleet-status` converts
  `~/.config/la-setup/hosts` once. The service phase saves its answers in
  `~flocc/.config/flocc/answers.toml`.
```

- [ ] **Step 6: Run everything**

Run: `just check`
Expected: exits 0, including `test_the_plugin_and_the_package_share_one_version` and `test_no_tracked_file_uses_the_old_names`.

- [ ] **Step 7: Commit**

```bash
git add docs/vm-setup.md README.md CHANGELOG.md pyproject.toml plugin/.claude-plugin/plugin.json uv.lock
git commit -s -m "Release 0.3.0: flocc

Incus steps for a new VM, what happens to hosts from before 0.3.0, and the changelog."
```

---

## Controller tasks

Tasks 9 to 15 are for the controller, in this order, after Task 8 is reviewed, except Task 10 Steps 1 to 3 (the docket re-scope), which run right after Task 1 lands and before Tasks 2 to 7 are dispatched. Each touches GitHub, the docket ledger, files outside the repo or a real host. Ask the owner before any step marked **owner**. A step that needs a terminal is run by the owner in their own terminal (Global Constraints).

### Task 9: Rename the GitHub repo (controller)

- [ ] **Step 1: Rename and repoint the remote** (**owner** confirms first: outward-facing)

```bash
gh repo rename flocc --repo NovusEdge/linear-agents --yes
git remote set-url origin git@github.com:NovusEdge/flocc.git
git remote get-url origin
git ls-remote origin HEAD
```

Expected: `git@github.com:NovusEdge/flocc.git` and one `HEAD` line. GitHub redirects the old name.

- [ ] **Step 2: Find desktop references to the old repo**

Run: `grep -rn 'NovusEdge/linear-agents' ~/.claude/settings.json ~/.claude/plugins/known_marketplaces.json 2>/dev/null`
Expected: no output. Any hit is a marketplace or setting that names the repo; change it to `NovusEdge/flocc` with the owner.

### Task 10: Re-scope docket records (controller)

Steps 1 to 3 run right after Task 1 is committed and reviewed, before Tasks 2 to 7 are dispatched: until then every recorded decision is scoped to `src/linear_agents/...`, so the implementers' docket briefings for `src/flocc/poller.py`, `worktree.py`, `publisher.py` and `supervisor.py` would surface none of the decisions that govern those files. Steps 4 and 5 run after Task 8.

- [ ] **Step 1: List the records whose scope names the old module**

```bash
D=$(ls -d ~/.claude/plugins/cache/NovusEdge/docket/*/bin/docket | tail -1)
$D list --json --fields id,scope | python3 -c '
import json, shlex, sys
for r in json.load(sys.stdin):
    scope = r.get("scope") or []
    if any("linear_agents" in s or "linear-agents" in s for s in scope):
        new = [s.replace("src/linear_agents/", "src/flocc/").replace("deploy/linear-agents", "deploy/flocc") for s in scope]
        print("$D correct", r["id"], " ".join("--scope " + shlex.quote(s) for s in new),
              "--reason \"linear-agents was renamed to flocc in 0.3.0\"")'
```

Expected: about 30 `docket correct` lines, every scope under `src/flocc/`.

- [ ] **Step 2: Apply them**

Run each printed line (with `D` set as above), then `$D check`.
Expected: `$D check` reports nothing; `$D list --json --fields id,scope | grep -c linear_agents` prints `0`. Retired entries keep their old scopes on purpose.

- [ ] **Step 3: Commit the re-scope**

```bash
git add .docket/ledger.jsonl
git commit -s -m "docket: re-scope records to src/flocc"
```

- [ ] **Step 4: Record this part's decisions** (after Task 8)

Record as decisions, with `docket decision` (see `$D decision --help`): a push rejected because the branch moved blocks at once with cause `branch-moved`; recovery runs when the ledger has no tests for the issue and `origin/agent/<key>` exists, scope `src/flocc/poller.py` and `src/flocc/worktree.py`; old names are read only in `src/flocc/legacy.py` and `installer/internal/ship/legacy.go` until 0.5.0; during the move, la-nimble's trigger label changes before flocc-nimble is shipped.

- [ ] **Step 5: Commit the ledger**

```bash
git add .docket/ledger.jsonl
git commit -s -m "docket: record Part 1's decisions"
```

### Task 11: Merge and tag 0.3.0 (controller; spec migration step 1)

- [ ] **Step 1: Open the PR** (**owner** confirms first)

```bash
git push -u origin HEAD
gh pr create --base main --title "flocc Part 1: rename and migration (0.3.0)" --body "Implements Part 1 of docs/superpowers/specs/2026-10-20-flocc-design.md. Plan: docs/superpowers/plans/2026-10-20-flocc-part-1-rename.md."
```

- [ ] **Step 2: Merge and tag** (**owner** merges or confirms)

After the owner's approval: `gh pr merge --merge`, then

```bash
git checkout main && git pull
git tag -a v0.3.0 -m "flocc 0.3.0" && git push origin v0.3.0
```

- [ ] **Step 3: Check the probe's target exists**

Run: `ssh root@la-nimble id -u linear-agents`
Expected: a uid. That is the user Task 5's refusal looks for; `TestShipRefusesAHostThatRunsTheOldService` pins the refusal itself. Do not run `just ship root@la-nimble` as the check: had the refusal regressed, it would stage files on la-nimble and run the root phase there, creating the `flocc` user. If the owner wants the end-to-end check, the owner runs it and stops at once unless the refusal "the host has a linear-agents user ... build a new host instead" comes right after `uname -m`.

- [ ] **Step 4: Convert the desktop hosts file**

Run: `just fleet-status`
Expected: `hosts: converted /home/<user>/.config/la-setup/hosts (kept as ...hosts.converted) into ...` (the output names the absolute paths), listing `root@la-nimble -> .../la-nimble.toml`, then the table. The la-nimble row is an error, not a status: the 0.3.0 fleet runs `sudo -u flocc ... flocc status --json` there, and the host has no `flocc` user. The call is read-only.

### Task 12: Build flocc-nimble (controller and owner; spec migration step 2)

Read the memory note on shared laptop daemons first: never restart Docker or Incus on zer0-day-nimble.

- [ ] **Step 1: Check memory on the laptop**

On zer0-day-nimble, run the size and `free -g` commands from `docs/vm-setup.md`, section "A VM on an Incus machine".
Expected: free memory at least la-nimble's `limits.memory` plus 2 GiB. If not, stop and tell the owner.

The owner adds `claude` to no new issue from here until Step 8 is done (Global Constraints). Steps 1 to 3 take a while and do not need the drain, so la-nimble keeps polling through them; the switch in Step 5 comes right before the ship.

- [ ] **Step 2: Build the VM**

On zer0-day-nimble, run the "Check them" block, then the `incus launch` block from `docs/vm-setup.md` with la-nimble's values. The **owner** opens the `tailscale up` URL and disables key expiry.
Expected: `tailscale status | grep flocc-nimble` on the desktop shows the node.

- [ ] **Step 3: Build the answers file without printing secrets**

```bash
umask 077
A=<controller scratchpad>/flocc-nimble-answers.toml
ssh root@la-nimble 'grep -v "^hostname" /home/linear-agents/.cache/la-setup/answers.toml; d=/home/linear-agents/.config/linear-agents; printf "\n[secrets]\nlinear_key = \"%s\"\nagent_token = \"%s\"\npublish_token = \"%s\"\n" "$(cat $d/linear.key)" "$(cat $d/gh-agent.token)" "$(cat $d/gh-publish.token)"' > "$A"
grep -c '= "' "$A"; grep -E '^\[|^team_key|^digest_issue' "$A"
```

Expected: the `[secrets]` and `[[repos]]` headers, `team_key` and `digest_issue` print; no secret value is shown. Do not open the file with Read.

- [ ] **Step 4: Create the drain label** (**owner** confirms: it changes the Linear workspace)

`doctor.linear_checks` requires the trigger label to exist, so la-nimble's doctor fails until `claude-drained` does. Create it on the NF team with the linear-nimblefox MCP `create_issue_label` (name `claude-drained`), never linear-botspot. No issue carries it.
Expected: `list_issue_labels` on NF lists `claude-drained`.

- [ ] **Step 5: Stop la-nimble starting issues**

This is the drain's first action on the host, placed right before the ship so that the two hosts never both poll `claude`.

```bash
ssh root@la-nimble "sudo -iu linear-agents sed -i 's/^trigger_label = .*/trigger_label = \"claude-drained\"/' .config/linear-agents/config.toml"
ssh root@la-nimble sudo -iu linear-agents linear-agents doctor | grep -E 'FAIL|config'
ssh root@la-nimble sudo -iu linear-agents linear-agents status
```

Expected: no FAIL line; `status` lists la-nimble's claims (record the running count `R` and its `session_cap`, `C`, from `grep session_cap` on the same config, default 2).

- [ ] **Step 6: Ship, pass 1 (secrets, config and install; no logins)**

An interactive ship asks for the Linear key and both GitHub tokens in password fields and ignores the `[secrets]` block, which only a `--skip-interactive` run reads. GitHub shows a token once, so ship twice. Run from the checkout, with `A` the answers file from Step 3 (shell state does not persist between commands, so set it again):

```bash
just ship root@flocc-nimble --skip-interactive --answers "$A"
```

Expected: every step through Config ok, then a stop at the Run step with doctor's `FAIL claude login`, so the command exits non-zero and no timer is enabled. The stage reset needs no password: the target is `root@`. Both answers copies stay (`/tmp/flocc-setup/<A's name>` and `~flocc/.cache/flocc-setup/<A's name>`) until pass 2 removes them.

- [ ] **Step 7: Set flocc-nimble's session cap**

With `C` and `R` from Step 5, set `N = C - R` (at least 1). No tick can run yet, since the timer starts in pass 2:

```bash
ssh root@flocc-nimble "sudo -iu flocc sed -i '1i session_cap = N' .config/flocc/config.toml"
ssh root@flocc-nimble sudo -iu flocc flocc doctor | grep -E 'FAIL|config'
```

Expected: `FAIL claude login` is the only FAIL line.

- [ ] **Step 8: Ship, pass 2 (Claude login and timer)** (**owner** runs it in their own terminal: huh forms and `/login` need a TTY)

Ask the owner to run, in the checkout in their own terminal: `just ship root@flocc-nimble --answers <path of A>`
Expected: Secrets and Config pass their checks and are skipped, the Claude step runs `/login` in the terminal, doctor passes without FAIL and asks to start the timer, then `Done.`. Pass 2 overwrites `~flocc/.cache/flocc-setup/<A's name>` and drops it on success; its stage reset clears `/tmp/flocc-setup/<A's name>`.

Then, on the desktop, `rm "$A"`. On any path that abandons the ship, also remove the copies on the host: `ssh root@flocc-nimble "rm -f /tmp/flocc-setup/<A's name> /home/flocc/.cache/flocc-setup/<A's name>"`.

The no-new-`claude` rule ends here.

- [ ] **Step 9: Add the host to the desktop list**

Write `~/.config/flocc/hosts/flocc-nimble.toml` with the Write tool, content `ssh = "root@flocc-nimble"`, then `just fleet-status`.
Expected: the table with flocc-nimble, and an error row for la-nimble (see Task 11, Step 4).

### Task 13: Edits outside the repo (controller; owner confirms each)

- [ ] **Step 1: ~/.claude/CLAUDE.md**

In the section `# linear-agents (Nimblefox NF issues)`: rename the heading to `# flocc (Nimblefox NF issues)`; the service runs on `flocc-nimble` (`root@flocc-nimble`), with la-nimble draining until it is retired; GitHub repo `NovusEdge/flocc`, local checkout still `~/Projects/Personal/linear-agents`; `just release root@flocc-nimble NF-…`. Add that until la-nimble is retired it takes plain `ssh root@la-nimble sudo -iu linear-agents linear-agents <command>` commands, because after 0.3.0 `just release`, `just host` and `just status root@la-nimble` address the `flocc` user, which la-nimble lacks. Show the owner the diff and apply it with Edit on a yes.

- [ ] **Step 2: Memory files**

In `/home/novusedge/.claude/projects/-home-novusedge-Projects-Personal-linear-agents/memory/`, run `grep -ln 'linear-agents\|la-nimble\|Dispatch' *.md`. Update `project-direction-generalize-a2a.md`: the name is decided, flocc, on 2026-10-20 (it said the leaning name was "Dispatch"); update its line in `MEMORY.md` to match. Change any other hit that names the service, its paths or its host.

- [ ] **Step 3: NF-215**

Read NF-215 with the linear-nimblefox MCP (`get_issue`), never linear-botspot. If it carries the `claude` label, stop and ask the owner. Otherwise draft the description with every old name and command replaced (`flocc`, `root@flocc-nimble`, `~/.config/flocc/`, `🤖 flocc:`), show the owner the changed lines, and `save_issue` on a yes.

### Task 14: Drain la-nimble (controller; spec migration step 3)

la-nimble keeps supervising, publishing, reviewing and forwarding comments for its claims. Never move an issue it holds to Todo by hand: it no longer polls, so the claim would never clear, and its later `release` would move flocc-nimble's issue back to Todo.

- [ ] **Step 1: List la-nimble's claims**

Run: `ssh root@la-nimble sudo -iu linear-agents linear-agents status`
Expected: each claim with its state. Running claims finish on la-nimble; Steps 2 to 4 are for each `blocked` or `timed_out` claim.

- [ ] **Step 2: Settle the worktree** (**owner** decides commit or discard)

```bash
ssh root@la-nimble sudo -iu linear-agents git -C /home/linear-agents/agents/worktrees/<KEY> status --short
```

Commit what the owner keeps as the service user, with `git add -A` first (`commit -a` leaves untracked files out) and the trailer for the claim's phase, `test` or `impl`. flocc-nimble recovers the tester's commits from `origin/agent/<key>` by their `Linear-Agents: <KEY> test` trailers and resumes at `impl` when it finds any, so the trailer decides what counts as the tester's work.

```bash
ssh root@la-nimble "sudo -iu linear-agents sh -c 'cd agents/worktrees/<KEY> && git add -A && git commit -s -m \"<message>\" -m \"Linear-Agents: <KEY> <test|impl>\"'"
```

Or discard it (`git -C ... checkout -- . && git -C ... clean -fd`). `handoff.md` and the escalation history stay in la-nimble's state directory, so the resumed session starts without them; paste what matters into a comment on the issue.

- [ ] **Step 3: Check the branch, then push it with the publish token**

This push reaches GitHub without la-nimble's publisher guard (a timed-out claim never reached it), and flocc-nimble trusts the leading `test` commits it finds on the branch, so check first. The commands run on la-nimble as the `linear-agents` user, with the 0.2.0 names.

For a `test` claim, push only if the owner judges the tests complete; flocc-nimble then resumes at the implementer. Otherwise push nothing: with no `agent/<key>` on `origin`, flocc-nimble cuts a fresh branch and the tester starts over.

For an `impl` or `fix` claim, read the claim's `tester_sha` from la-nimble's ledger:

```bash
ssh root@la-nimble sudo -iu linear-agents python3 - <KEY> <<'PY'
import os, sqlite3, sys
db = sqlite3.connect(os.path.expanduser("~/.local/state/linear-agents/ledger.sqlite"))
print(db.execute("select phase, tester_sha from claims where issue_key = ?", (sys.argv[1],)).fetchone())
PY
```

Then check that it is an ancestor of the worktree's `HEAD` and that no `<KEY> test` trailer follows it:

```bash
ssh root@la-nimble "sudo -iu linear-agents git -C /home/linear-agents/agents/worktrees/<KEY> merge-base --is-ancestor <tester_sha> HEAD" && echo ancestor
ssh root@la-nimble "sudo -iu linear-agents git -C /home/linear-agents/agents/worktrees/<KEY> log --first-parent --format='%(trailers:only)' <tester_sha>..HEAD" | grep "<KEY> test"
```

Expected: `ancestor`, then no output from the `grep`. If the first command fails, if the `grep` prints a line, or if `tester_sha` is empty, stop and ask the owner: the implementer amended or forged the tester's commit, and the push would put it outside the guard.

Push:

```bash
ssh root@la-nimble "sudo -iu linear-agents sh -c 'cd agents/worktrees/<KEY> && GH_TOKEN=\$(cat ~/.config/linear-agents/gh-publish.token) git -c credential.helper= -c \"credential.helper=!gh auth git-credential\" push origin agent/<key>'"
```

Expected: the push succeeds (the agent token is read-only).

- [ ] **Step 4: Release it to flocc-nimble**

Run: `ssh root@la-nimble sudo -iu linear-agents linear-agents release --keep-worktree <KEY>`
Expected: the issue is back in Todo; within a tick or two `ssh root@flocc-nimble sudo -iu flocc flocc status` shows the claim, in phase `impl` when the tester's commits were on the branch.

Rollback until Task 15 deletes the VM: set la-nimble's `trigger_label` back to `claude` and stop flocc-nimble's timer (`ssh root@flocc-nimble "sudo -iu flocc bash -c 'XDG_RUNTIME_DIR=/run/user/\$(id -u) systemctl --user stop flocc.timer'"`). flocc-nimble re-posts `no-repo` or `no-criteria` notes once on skipped Todo issues; that is expected.

### Task 15: Retire la-nimble (controller; spec migration step 4)

- [ ] **Step 1: Wait until nothing runs there**

Run: `ssh root@la-nimble sudo -iu linear-agents linear-agents status --json | python3 -c 'import json,sys; print(sum(c["state"] == "running" for c in json.load(sys.stdin)["claims"]))'`
Expected: `0`. Then wait for that day's digest comment on the digest issue (posted after `digest_hour`, 18:00 host time by default).

- [ ] **Step 2: Copy its state to the desktop**

```bash
mkdir -p ~/archive/la-nimble
rsync -a root@la-nimble:/home/linear-agents/.local/state/linear-agents ~/archive/la-nimble/state
rsync -a root@la-nimble:/home/linear-agents/.claude/projects ~/archive/la-nimble/claude-projects
```

- [ ] **Step 3: Stop its timer and the VM**

```bash
ssh root@la-nimble "sudo -iu linear-agents bash -c 'XDG_RUNTIME_DIR=/run/user/\$(id -u) systemctl --user disable --now linear-agents.timer'"
```

On zer0-day-nimble: `incus stop la-nimble`.

- [ ] **Step 4: Remove it from the hosts and raise the cap**

Delete `~/.config/flocc/hosts/la-nimble.toml`; set flocc-nimble's `session_cap` to `C` with the same `sed` form as Task 12 Step 7 (`s/^session_cap = .*/session_cap = C/`); run `ssh root@flocc-nimble sudo -iu flocc flocc doctor`. Update `~/.claude/CLAUDE.md` and NF-215 to drop la-nimble (**owner** confirms).

- [ ] **Step 5: Delete the VM 7 days later** (**owner** confirms)

On or after the seventh day after Step 3, on zer0-day-nimble: `incus delete la-nimble`. Offer the owner a scheduled reminder for that date.
