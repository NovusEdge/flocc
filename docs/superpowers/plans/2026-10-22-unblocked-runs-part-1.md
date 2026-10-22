# Unblocked runs, part 1: broken-main hold and host dependencies

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A repo whose main fails `./setup` holds its issues instead of re-running setup every tick, and hosts carry Playwright's chromium dependencies and globally set mise tools.

**Architecture:** The poller already runs `./setup` before claiming. When it fails in a worktree still at `origin/<base>`, a new `flocc.canary` module records a per-repo mark in the ledger's `meta` table and the poller holds that repo's issues until main moves, 24 hours pass, or `flocc canary <repo>` passes. On the Go side, two new installer steps read new host-file keys: a root step `Browsers` (`playwright = "<version>"`) and a service step `Tools` (`[tools]`).

**Tech Stack:** Python 3.13 (uv, pytest, ruff), Go (installer, go-toml v2), git, mise, npx.

**Spec:** `docs/superpowers/specs/2026-10-22-unblocked-runs-design.md`, sections 1 and 2. Issues NovusEdge/flocc#59 and #60.

## Global Constraints

- Commits use `git commit -s`, no `Co-Authored-By` line.
- Python: `uv run pytest -q`, `uv run ruff check src tests`, `uv run ruff format src tests`. Do not reformat files the task does not touch (`ruff format` rewrites `src/flocc/linear.py`; restore it with `git checkout -- src/flocc/linear.py`).
- Go: from `installer/`, `go test ./...`, `gofmt -l .` prints nothing, `go vet ./...`.
- Hold window: 24 hours (`HOLD_S = 24 * 3600`). Output kept in a mark: last 1500 characters.
- Playwright on flocc-nimble: version `1.64.0`; Debian 13; `--dry-run` prints `Missing system dependencies (N):` then one package per line indented two spaces, and nothing of that when all are installed.
- `mise current <tool>` exits 0 and prints nothing on stdout when no version is set.
- Comments carry facts the code cannot show; match the surrounding files' density.

## Review Focus

- A setup failure on a rerun branch that has the issue's own commits must not mark the repo: HEAD differs from `origin/<base>`.
- `git fetch` failing while a repo is marked keeps the hold and does not raise out of `poll`.
- Two ready issues of one repo in the same poll: once the first marks the repo, the second is held in the same poll without running `./setup`.
- `flocc canary` while a tick holds the lock refuses with exit 1, like `release`.
- `Browsers.Check` before the Packages step has installed node: `npx` is missing, so Check reports not done instead of failing the whole run.

---

### Task 1: Worktree helpers for the base checkout

**Files:**
- Modify: `src/flocc/worktree.py` (add three functions after `prepare_worktree`)
- Test: `tests/test_worktree.py`

**Interfaces:**
- Produces:
  - `origin_base(repo: RepoConfig) -> str | None`: fetches `repo.base_branch` and returns `origin/<base>`'s SHA; `None` when fetch or rev-parse fails.
  - `head_at_base(repo: RepoConfig, path: Path) -> str | None`: the base SHA when the worktree at `path` has HEAD equal to `origin/<base>` (no fetch), else `None`, also when `path` is not a worktree.
  - `setup_at_base(repo: RepoConfig, worktree_root: Path, timeout_s: float, shared_uv_cache: bool) -> tuple[int | None, str, str]`: runs `./setup` on a detached worktree of `origin/<base>` and removes the worktree; returns `(code, output, sha)`, code `None` on timeout, `(0, "no ./setup", sha)` when the repo has none.

- [ ] **Step 1: Write the failing tests** (append to `tests/test_worktree.py`; add `head_at_base, origin_base, setup_at_base` to the import list)

```python
def test_head_at_base_names_the_base_only_for_an_untouched_checkout(origin, tmp_path):
    repo = RepoConfig(origin)
    path, _, base = prepare_worktree(repo, tmp_path / "wt", "NF-3")
    assert head_at_base(repo, path) == base
    _commit(path, "b.txt")
    assert head_at_base(repo, path) is None
    assert head_at_base(repo, tmp_path / "wt" / "NF-404") is None


def test_origin_base_fetches_a_new_commit_and_survives_a_dead_remote(origin, tmp_path):
    repo = RepoConfig(origin)
    other = tmp_path / "other"
    subprocess.run(
        ["git", "clone", "-q", tmp_path / "origin.git", other], check=True, capture_output=True
    )
    for k, v in (("user.name", "t"), ("user.email", "t@t")):
        git("config", k, v, cwd=other)
    _commit(other, "c.txt")
    git("push", "-q", "origin", "main", cwd=other)
    assert origin_base(repo) == git("rev-parse", "HEAD", cwd=other)
    git("remote", "set-url", "origin", str(tmp_path / "gone.git"), cwd=origin)
    assert origin_base(repo) is None


@pytest.mark.parametrize("body, code", [("touch .setup-ran", 0), ("exit 3", 3)])
def test_setup_at_base_runs_setup_on_a_throwaway_checkout(origin, tmp_path, body, code):
    push_setup(origin, body)
    got, out, sha = setup_at_base(RepoConfig(origin), tmp_path / "wt", 30, False)
    assert got == code and sha == git("rev-parse", "origin/main", cwd=origin)
    assert "canary" not in git("worktree", "list", cwd=origin)
    assert [p.name for p in (tmp_path / "wt").iterdir()] == []


def test_setup_at_base_reports_a_timeout(origin, tmp_path):
    push_setup(origin, "sleep 30")
    code, _, _ = setup_at_base(RepoConfig(origin), tmp_path / "wt", 0.5, False)
    assert code is None
```

`_commit(repo, name, trailer="")` already exists in this file (line 68) and commits a new file.

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_worktree.py -k "head_at_base or origin_base or setup_at_base"`
Expected: ImportError for `head_at_base`.

- [ ] **Step 3: Implement** (in `src/flocc/worktree.py`, after `prepare_worktree`; add `import tempfile` at the top)

```python
def origin_base(repo: RepoConfig) -> str | None:
    try:
        git("fetch", "-q", "origin", repo.base_branch, cwd=repo.path)
        return git("rev-parse", f"origin/{repo.base_branch}", cwd=repo.path)
    except (subprocess.CalledProcessError, OSError):
        return None


def head_at_base(repo: RepoConfig, path: Path) -> str | None:
    """The base SHA when the worktree at path is still exactly origin/<base>."""
    try:
        head = git("rev-parse", "HEAD", cwd=path)
        base = git("rev-parse", f"origin/{repo.base_branch}", cwd=repo.path)
    except (subprocess.CalledProcessError, OSError):
        return None
    return base if head == base else None


def setup_at_base(
    repo: RepoConfig, worktree_root: Path, timeout_s: float, shared_uv_cache: bool
) -> tuple[int | None, str, str]:
    git("fetch", "-q", "origin", repo.base_branch, cwd=repo.path)
    sha = git("rev-parse", f"origin/{repo.base_branch}", cwd=repo.path)
    worktree_root.mkdir(parents=True, exist_ok=True)
    root = worktree_root.resolve()
    # A fresh name each run: a killed canary leaves its directory, and reap_worktrees does not
    # know this prefix.
    path = Path(tempfile.mkdtemp(prefix=".canary-", dir=root))
    path.rmdir()
    git("worktree", "add", "-q", "--detach", str(path), sha, cwd=repo.path)
    try:
        if not (path / "setup").exists():
            return 0, "no ./setup", sha
        cache = root / (".uv-cache" if shared_uv_cache else ".uv-cache-canary")
        code, out = _run_setup(path, cache, timeout_s, keep_cache=shared_uv_cache)
        return code, out, sha
    finally:
        subprocess.run(
            ["git", "worktree", "remove", "--force", str(path)],
            cwd=repo.path,
            capture_output=True,
            check=False,
        )
        shutil.rmtree(path, ignore_errors=True)
```

- [ ] **Step 4: Run the tests**

Run: `uv run pytest -q tests/test_worktree.py`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/worktree.py tests/test_worktree.py
git commit -s -m "worktree: helpers to read and set up origin's base checkout"
```

---

### Task 2: Mark a broken main and hold its issues

**Files:**
- Create: `src/flocc/canary.py`
- Modify: `src/flocc/poller.py` (`poll`, `_start`, imports)
- Test: `tests/test_canary.py` (create), `tests/test_poller.py`

**Interfaces:**
- Consumes: `origin_base`, `head_at_base` from Task 1; `ledger.meta_get(con, key) -> str | None`, `ledger.meta_set(con, key, value)`.
- Produces (in `flocc.canary`):
  - `HOLD_S = 24 * 3600`
  - `mark(con, repo: str, sha: str, now: float, output: str) -> None`
  - `get_mark(con, repo: str) -> dict | None` (keys `sha`, `at`, `output`)
  - `clear(con, repo: str) -> None`
  - `hold_reason(con, repo: str, base_branch: str, current_sha: str | None, now: float) -> str | None`: `None` and the mark cleared when there is no mark, it is `HOLD_S` old, or `current_sha` is a different SHA; otherwise the reason. `current_sha=None` (fetch failed) keeps the hold.

- [ ] **Step 1: Write the failing module tests** (`tests/test_canary.py`)

```python
from flocc import canary, ledger


def con(tmp_path):
    return ledger.connect(tmp_path / "l.sqlite")


def test_a_mark_holds_while_main_stays_at_its_sha(tmp_path):
    c = con(tmp_path)
    canary.mark(c, "web", "abc123def456", 100.0, "x" * 2000 + "npm missing")
    m = canary.get_mark(c, "web")
    assert m["sha"] == "abc123def456" and len(m["output"]) == 1500
    reason = canary.hold_reason(c, "web", "main", "abc123def456", 200.0)
    assert reason == "./setup fails on main at abc123def; after fixing it, run flocc canary web"


def test_a_new_commit_on_main_clears_the_mark(tmp_path):
    c = con(tmp_path)
    canary.mark(c, "web", "abc", 100.0, "")
    assert canary.hold_reason(c, "web", "main", "def", 200.0) is None
    assert canary.get_mark(c, "web") is None


def test_a_mark_expires_after_a_day(tmp_path):
    c = con(tmp_path)
    canary.mark(c, "web", "abc", 100.0, "")
    assert canary.hold_reason(c, "web", "main", "abc", 100.0 + canary.HOLD_S) is None
    assert canary.get_mark(c, "web") is None


def test_an_unreachable_remote_keeps_the_hold(tmp_path):
    c = con(tmp_path)
    canary.mark(c, "web", "abc", 100.0, "")
    assert canary.hold_reason(c, "web", "main", None, 200.0) is not None


def test_no_mark_or_a_torn_one_holds_nothing(tmp_path):
    c = con(tmp_path)
    assert canary.hold_reason(c, "web", "main", "abc", 1.0) is None
    ledger.meta_set(c, "main_broken:web", "{torn")
    assert canary.get_mark(c, "web") is None
```

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_canary.py`
Expected: `ModuleNotFoundError: flocc.canary`.

- [ ] **Step 3: Implement `src/flocc/canary.py`**

```python
import json

from flocc import ledger

HOLD_S = 24 * 3600
OUTPUT_CHARS = 1500


def _key(repo: str) -> str:
    return f"main_broken:{repo}"


def mark(con, repo: str, sha: str, now: float, output: str) -> None:
    value = {"sha": sha, "at": now, "output": output[-OUTPUT_CHARS:]}
    ledger.meta_set(con, _key(repo), json.dumps(value))


def get_mark(con, repo: str) -> dict | None:
    raw = ledger.meta_get(con, _key(repo))
    try:
        m = json.loads(raw) if raw else None
    except ValueError:
        return None
    return m if isinstance(m, dict) and {"sha", "at"} <= m.keys() else None


def clear(con, repo: str) -> None:
    ledger.meta_set(con, _key(repo), "")


def hold_reason(con, repo: str, base_branch: str, current_sha: str | None, now: float) -> str | None:
    m = get_mark(con, repo)
    if m is None:
        return None
    # A fix on the host moves no commit, so a mark also lapses with time; the next issue's
    # ./setup then tests main again.
    if now - float(m["at"]) >= HOLD_S or (current_sha is not None and current_sha != m["sha"]):
        clear(con, repo)
        return None
    return (
        f"./setup fails on {base_branch} at {m['sha'][:9]}; after fixing it, run flocc canary {repo}"
    )
```

- [ ] **Step 4: Run the module tests**

Run: `uv run pytest -q tests/test_canary.py`
Expected: 5 passed.

- [ ] **Step 5: Write the failing poller tests** (append to `tests/test_poller.py`; it already imports `subprocess`, `ledger`, `Notifier`, `issue`, `poll`)

```python
def _broken_main(rt, monkeypatch, at_base="sha-main"):
    calls = []

    def failing(repo, root, key, setup_timeout_s, shared_uv_cache):
        calls.append(key)
        raise subprocess.CalledProcessError(1, ["./setup"], stderr="mise: no version set for go\n")

    monkeypatch.setattr("flocc.poller.prepare_worktree", failing)
    monkeypatch.setattr("flocc.poller.head_at_base", lambda repo, path: at_base)
    monkeypatch.setattr("flocc.poller.origin_base", lambda repo: "sha-main")
    return calls


def test_setup_failing_on_untouched_main_marks_the_repo_and_holds_its_issues(rt, monkeypatch):
    calls = _broken_main(rt, monkeypatch)
    rt.linear.ready = [issue("NF-9"), issue("NF-10")]
    poll(rt)
    poll(rt)
    assert calls == ["NF-9"]
    [esc] = rt.notifier.sent
    assert esc.tier == 1 and esc.issue_key == "NF-9" and esc.cause.endswith("main-broken")
    assert "mise: no version set for go" in esc.summary and "flocc canary" in esc.action
    assert ledger.get_claim(rt.con, "NF-10") is None


def test_setup_failing_on_the_issues_own_commits_is_the_issues(rt, monkeypatch):
    calls = _broken_main(rt, monkeypatch, at_base=None)
    rt.linear.ready = [issue("NF-9"), issue("NF-10")]
    poll(rt)
    assert calls == ["NF-9", "NF-10"]
    assert all(e.tier == 2 for e in rt.notifier.sent)


def test_a_new_commit_on_main_lets_the_next_issue_try_again(rt, monkeypatch):
    calls = _broken_main(rt, monkeypatch)
    rt.linear.ready = [issue("NF-9")]
    poll(rt)
    monkeypatch.setattr("flocc.poller.origin_base", lambda repo: "sha-fixed")
    poll(rt)
    assert calls == ["NF-9", "NF-9"]
```

Check `issue()` in `tests/test_poller.py` takes a key and gives the issue the test repo's label; if its signature differs, adapt the calls, not the assertions.

- [ ] **Step 6: Run them to see them fail**

Run: `uv run pytest -q tests/test_poller.py -k "main"`
Expected: AttributeError: `flocc.poller` has no `head_at_base`.

- [ ] **Step 7: Wire the poller** (`src/flocc/poller.py`)

Imports: add `from flocc import canary` beside `from flocc import ledger`, and `head_at_base, origin_base` to the `flocc.worktree` import list.

In `poll`, after the `for cause in ("no-repo", ...)` loop and before `live = ledger.live_claims(rt.con)`:

```python
        hold = _main_hold(rt, repo)
        if hold:
            print(f"{issue.key}: held, {hold}", file=sys.stderr)
            continue
```

New function after `poll`:

```python
def _main_hold(rt, repo: str) -> str | None:
    # Fetches only for a marked repo, so an unmarked one costs nothing per tick.
    if canary.get_mark(rt.con, repo) is None:
        return None
    cfg_repo = rt.cfg.repos[repo]
    return canary.hold_reason(
        rt.con, repo, cfg_repo.base_branch, origin_base(cfg_repo), rt.now()
    )
```

In `_start`'s `except (subprocess.SubprocessError, OSError) as exc:` block, after `detail = ...` and before the existing `rt.notifier.send(`:

```python
        # prepare_worktree fetched origin and cut the worktree at the base, so an untouched
        # checkout that fails is main's or the host's failure, and every issue would repeat it.
        base = head_at_base(cfg.repos[repo], cfg.worktree_root.resolve() / issue.key)
        if base is not None:
            base_branch = cfg.repos[repo].base_branch
            canary.mark(rt.con, repo, base, rt.now(), stderr or str(exc))
            rt.notifier.send(
                Escalation(
                    1,
                    f"{repo}:main-broken",
                    f"./setup fails on a clean checkout of {repo}'s {base_branch} at {base[:9]},"
                    f" so its issues are held: {exc}{detail}",
                    f"Fix ./setup on {base_branch} or on the host. Issues resume after a new"
                    f" commit on {base_branch}, after 24 hours, or once flocc canary {repo} passes",
                    issue_key=issue.key,
                ),
                issue,
                issue.team_id,
            )
            return
```

The early `if ledger.alert_seen(rt.con, cause, DEDUPE_S, rt.now()): return` above stays where it is.

- [ ] **Step 8: Run the poller and canary tests, then the suite**

Run: `uv run pytest -q tests/test_poller.py tests/test_canary.py` then `uv run pytest -q`
Expected: all pass, including the existing `test_prepare_failure_comments_once_with_stderr` (its fake worktree path does not exist, so `head_at_base` returns `None`).

- [ ] **Step 9: Commit**

```bash
git add src/flocc/canary.py src/flocc/poller.py tests/test_canary.py tests/test_poller.py
git commit -s -m "Hold a repo's issues while ./setup fails on its untouched main"
```

---

### Task 3: `flocc canary <repo>`

**Files:**
- Modify: `src/flocc/cli.py` (new `cmd_canary`, `COMMANDS`, parser)
- Modify: `docs/book/setup/running-a-host.md` (new section), `CHANGELOG.md` (Unreleased)
- Test: `tests/test_cli.py`

**Interfaces:**
- Consumes: `setup_at_base` (Task 1); `canary.mark`, `canary.clear` (Task 2); `tick_lock` from `flocc.tick`; `_config(args)` in `cli.py`.

- [ ] **Step 1: Write the failing tests** (append to `tests/test_cli.py`; `host_cfg` is the existing fixture)

```python
def _canary(host_cfg, monkeypatch, result):
    monkeypatch.setattr("flocc.worktree.setup_at_base", lambda *a: result)
    return load(host_cfg)


def test_canary_clears_the_mark_when_setup_passes(host_cfg, monkeypatch, capsys):
    from flocc import canary

    cfg = _canary(host_cfg, monkeypatch, (0, "ok", "abc123def456"))
    repo = next(iter(cfg.repos))
    cfg.ledger_path.parent.mkdir(parents=True, exist_ok=True)
    canary.mark(ledger.connect(cfg.ledger_path), repo, "abc123def456", 1.0, "old")
    assert main(["--config", str(host_cfg), "canary", repo]) == 0
    assert canary.get_mark(ledger.connect(cfg.ledger_path), repo) is None
    assert "no longer held" in capsys.readouterr().out


def test_canary_marks_the_repo_when_setup_fails(host_cfg, monkeypatch, capsys):
    from flocc import canary

    cfg = _canary(host_cfg, monkeypatch, (2, "go: not found", "abc123def456"))
    repo = next(iter(cfg.repos))
    cfg.ledger_path.parent.mkdir(parents=True, exist_ok=True)
    assert main(["--config", str(host_cfg), "canary", repo]) == 1
    assert canary.get_mark(ledger.connect(cfg.ledger_path), repo)["output"] == "go: not found"
    assert "go: not found" in capsys.readouterr().err


def test_canary_refuses_an_unknown_repo(host_cfg, capsys):
    assert main(["--config", str(host_cfg), "canary", "nope"]) == 2
    assert "no repo nope" in capsys.readouterr().err


def test_canary_waits_for_a_running_tick(host_cfg, monkeypatch, capsys):
    from flocc.tick import tick_lock

    cfg = _canary(host_cfg, monkeypatch, (0, "", "abc"))
    with tick_lock(cfg.tick_lock):
        assert main(["--config", str(host_cfg), "canary", next(iter(cfg.repos))]) == 1
    assert "a tick is running" in capsys.readouterr().err
```

If `test_cli.py` does not import `load` and `ledger` yet, add `from flocc import ledger` and `from flocc.config import load`.

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_cli.py -k canary`
Expected: `invalid choice: 'canary'` (exit 2 on the first test).

- [ ] **Step 3: Implement** (`src/flocc/cli.py`, after `cmd_release`)

```python
def cmd_canary(args) -> int:
    from flocc import canary, worktree
    from flocc.tick import tick_lock

    cfg, _ = _config(args)
    if args.repo not in cfg.repos:
        print(f"canary: no repo {args.repo} in the config ({', '.join(cfg.repos)})", file=sys.stderr)
        return 2
    # ./setup runs under the tick lock, as the poller's does, so the two never share a cache.
    with tick_lock(cfg.tick_lock) as held:
        if not held:
            print("canary: a tick is running; try again in a moment", file=sys.stderr)
            return 1
        con = ledger.connect(cfg.ledger_path)
        code, out, sha = worktree.setup_at_base(
            cfg.repos[args.repo], cfg.worktree_root, cfg.setup_timeout_s, cfg.shared_uv_cache
        )
        if code == 0:
            canary.clear(con, args.repo)
            print(f"canary: ./setup passes on {args.repo} at {sha[:9]}; its issues are no longer held")
            return 0
        canary.mark(con, args.repo, sha, time.time(), out)
        what = "timed out" if code is None else f"exited {code}"
        print(
            f"canary: ./setup {what} on {args.repo} at {sha[:9]}; its issues stay held\n"
            f"{out[-canary.OUTPUT_CHARS:]}",
            file=sys.stderr,
        )
        return 1
```

`COMMANDS`: add `"canary": cmd_canary,` after `"release"`. Parser, after the `rel` block:

```python
    can = add("canary", "Run ./setup on a repo's base branch; clears its broken-main hold on a pass")
    can.add_argument("repo", help="repo name from the config")
```

- [ ] **Step 4: Run the tests**

Run: `uv run pytest -q tests/test_cli.py tests/test_canary.py`
Expected: all pass.

- [ ] **Step 5: Document**

In `docs/book/setup/running-a-host.md`, add before `## Auto-mode blocks`:

```markdown
## A repo whose main fails setup

When `./setup` fails in a fresh checkout of a repo's base branch, the failure is main's or the host's, and every issue of that repo would repeat it. flocc comments once on the issue that hit it, pushes, and holds the repo's other issues. The hold ends when a new commit lands on the base branch, after 24 hours, or when `flocc -H <host> canary <repo>` passes; run that after fixing something on the host, which moves no commit.
```

In `CHANGELOG.md` under `## Unreleased`, add:

```markdown
- When `./setup` fails on an untouched checkout of a repo's base branch, the repo's issues are
  held instead of each running setup again every tick. A new commit on the base, 24 hours, or a
  passing `flocc canary <repo>` lifts the hold.
```

(If `## Unreleased` is absent on this branch, create it above the newest release heading with the one-line summary "Runs stop less often on the host's or main's problems.")

- [ ] **Step 6: Commit**

```bash
git add src/flocc/cli.py tests/test_cli.py docs/book/setup/running-a-host.md CHANGELOG.md
git commit -s -m "flocc canary: rerun setup on a repo's base and lift its hold"
```

---

### Task 4: Browsers root step

**Files:**
- Modify: `installer/internal/answers/answers.go` (field, `Overlay`)
- Modify: `installer/cmd/flocc-setup/main.go:241-243` (copy from the shipped document)
- Create: `installer/internal/steps/browsers.go`
- Modify: `installer/internal/steps/list.go` (`RootSteps`)
- Test: `installer/internal/steps/browsers_test.go` (create)

**Interfaces:**
- Produces: `answers.Answers.Playwright string` (`toml:"playwright,omitempty"`); `steps.Browsers` implementing `Step`.

- [ ] **Step 1: Write the failing tests** (`installer/internal/steps/browsers_test.go`)

```go
package steps

import (
	"context"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

const dryRun = "npx -y playwright@1.64.0 install-deps --dry-run chromium"

func browsersEnv(t *testing.T, f *host.Fake) *Env {
	e := testEnv(t, f)
	e.Distro.Manager = host.Apt
	e.Answers.Playwright = "1.64.0"
	return e
}

func TestBrowsersIsDoneWhenNoVersionIsAskedFor(t *testing.T) {
	e := testEnv(t, host.NewFake())
	if ok, detail, err := (Browsers{}).Check(context.Background(), e); !ok || err != nil || detail != "none asked for" {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
}

func TestBrowsersSkipsHostsWithoutApt(t *testing.T) {
	e := browsersEnv(t, host.NewFake())
	e.Distro.Manager = host.Pacman
	if ok, detail, _ := (Browsers{}).Check(context.Background(), e); !ok || detail != "apt hosts only" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestBrowsersCountsWhatTheDryRunListsAsMissing(t *testing.T) {
	f := host.NewFake().On(dryRun, 0, "Missing system dependencies (2):\n  fontconfig\n  libnss3\n")
	ok, detail, err := (Browsers{}).Check(context.Background(), browsersEnv(t, f))
	if ok || err != nil || detail != "2 missing packages" {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
}

func TestBrowsersIsDoneWhenNothingIsMissing(t *testing.T) {
	f := host.NewFake().On(dryRun, 0, "")
	if ok, _, _ := (Browsers{}).Check(context.Background(), browsersEnv(t, f)); !ok {
		t.Fatal("not done with nothing missing")
	}
}

// RunAll checks every step before Packages installs node, so a missing npx is not done, not an error.
func TestBrowsersCheckWithoutNpxIsNotDone(t *testing.T) {
	ok, detail, err := (Browsers{}).Check(context.Background(), browsersEnv(t, host.NewFake()))
	if ok || err != nil || detail == "" {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
}

func TestBrowsersApplyInstallsTheDeps(t *testing.T) {
	f := host.NewFake().On("npx -y playwright@1.64.0 install-deps chromium", 0, "")
	if err := (Browsers{}).Apply(context.Background(), browsersEnv(t, f)); err != nil {
		t.Fatal(err)
	}
	if !f.Ran("npx -y playwright@1.64.0 install-deps chromium") {
		t.Fatalf("calls %q", f.Calls)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `cd installer && go test ./internal/steps/ -run Browsers`
Expected: build failure, `undefined: Browsers` and `e.Answers.Playwright undefined`.

- [ ] **Step 3: Implement**

`installer/internal/answers/answers.go`, in `Answers` after `SharedTeam`:

```go
	// Playwright is the version whose chromium system libraries and fonts the Browsers step
	// installs; empty installs none.
	Playwright string `toml:"playwright,omitempty"`
```

In `Overlay`, beside the `o.Permissions` block:

```go
	if o.Playwright != "" {
		a.Playwright = o.Playwright
	}
```

`installer/cmd/flocc-setup/main.go`, extend the `if fromStdin {` block at line 241:

```go
	if fromStdin {
		a.Permissions = in.Permissions
		a.Playwright = in.Playwright
	}
```

`installer/internal/steps/browsers.go`:

```go
package steps

import (
	"context"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// Browsers installs the system libraries and fonts Playwright's chromium needs. The service user
// cannot install them, and without them every browser launch in an e2e check fails within
// milliseconds (NF-240, 2026-10-20 and 10-21).
type Browsers struct{}

func (Browsers) Name() string { return "Browsers" }

func (Browsers) npx(e *Env, dry bool) host.Cmd {
	args := []string{"-y", "playwright@" + e.Answers.Playwright, "install-deps"}
	if dry {
		args = append(args, "--dry-run")
	}
	return host.Cmd{Name: "npx", Args: append(args, "chromium"), Env: []string{"DEBIAN_FRONTEND=noninteractive"}}
}

// missing counts the packages under the dry run's "Missing system dependencies (N):" line.
func missing(out string) int {
	n, listing := 0, false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "Missing system dependencies"):
			listing = true
		case listing && strings.HasPrefix(line, "  ") && strings.TrimSpace(line) != "":
			n++
		}
	}
	return n
}

func (b Browsers) Check(ctx context.Context, e *Env) (bool, string, error) {
	switch {
	case e.Answers.Playwright == "":
		return true, "none asked for", nil
	case e.Distro.Manager != host.Apt:
		return true, "apt hosts only", nil
	}
	r, err := e.Run.Run(ctx, b.npx(e, true))
	if err != nil || r.Code != 0 {
		return false, "playwright " + e.Answers.Playwright + " dry run did not run", nil
	}
	if n := missing(r.Stdout); n > 0 {
		return false, Plural(n, "missing package"), nil
	}
	return true, "chromium deps for playwright " + e.Answers.Playwright, nil
}

func (b Browsers) Apply(ctx context.Context, e *Env) error {
	c := b.npx(e, false)
	return e.UI.Spin(c.String(), func() error { _, err := e.MustCmd(ctx, c); return err })
}
```

`installer/internal/steps/list.go`:

```go
func RootSteps() []Step { return []Step{Packages{}, Browsers{}, Tailscale{}, User{}, Scratch{}} }
```

- [ ] **Step 4: Run the tests**

Run: `cd installer && go test ./internal/steps/ ./internal/answers/ ./cmd/... && gofmt -l . && go vet ./...`
Expected: pass, gofmt prints nothing. A test that lists `RootSteps()` names may need `Browsers` added; update the expected list, nothing else.

- [ ] **Step 5: Commit**

```bash
git add installer/internal/answers/answers.go installer/cmd/flocc-setup/main.go installer/internal/steps/browsers.go installer/internal/steps/browsers_test.go installer/internal/steps/list.go
git commit -s -m "installer: Browsers step installs Playwright's chromium deps on apt hosts"
```

---

### Task 5: Tools service step

**Files:**
- Modify: `installer/internal/answers/answers.go` (field, `Overlay`)
- Modify: `installer/cmd/flocc-setup/main.go` (the same `if fromStdin {` block)
- Create: `installer/internal/steps/tools.go`
- Modify: `installer/internal/steps/list.go` (`ServiceSteps`)
- Modify: `docs/book/setup/running-a-host.md`, `CHANGELOG.md`
- Test: `installer/internal/steps/tools_test.go` (create)

**Interfaces:**
- Produces: `answers.Answers.Tools map[string]string` (`toml:"tools,omitempty"`); `steps.Tools` implementing `Step`.

- [ ] **Step 1: Write the failing tests** (`installer/internal/steps/tools_test.go`)

```go
package steps

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

func toolsEnv(t *testing.T, f *host.Fake, withMise bool) *Env {
	e := testEnv(t, f)
	e.Answers.Tools = map[string]string{"go": "1.27.1", "just": "1.58.0"}
	if withMise {
		p := e.HomePath(".local/bin/mise")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755)
	}
	return e
}

const miseBin = "/home/flocc/.local/bin/mise"

func TestToolsIsDoneWithNoneInTheHostFile(t *testing.T) {
	e := testEnv(t, host.NewFake())
	if ok, detail, _ := (Tools{}).Check(context.Background(), e); !ok || detail != "none in the host file" {
		t.Fatalf("%v %q", ok, detail)
	}
}

func TestToolsNamesTheOnesWithoutAGlobalVersion(t *testing.T) {
	f := host.NewFake().On(miseBin+" current go", 0, "").On(miseBin+" current just", 0, "1.58.0\n")
	ok, detail, err := (Tools{}).Check(context.Background(), toolsEnv(t, f, true))
	if ok || err != nil || detail != "not set: go 1.27.1" {
		t.Fatalf("%v %q %v", ok, detail, err)
	}
}

func TestToolsApplySetsOnlyTheMissingOnes(t *testing.T) {
	f := host.NewFake().On(miseBin+" current go", 0, "").On(miseBin+" current just", 0, "1.58.0").
		On(miseBin+" use -g go@1.27.1", 0, "")
	if err := (Tools{}).Apply(context.Background(), toolsEnv(t, f, true)); err != nil {
		t.Fatal(err)
	}
	if !f.Ran(miseBin+" use -g go@1.27.1") || f.Ran(miseBin+" use -g just@1.58.0") {
		t.Fatalf("calls %q", f.Calls)
	}
}

func TestToolsWithoutMiseSaysToInstallIt(t *testing.T) {
	e := toolsEnv(t, host.NewFake(), false)
	if ok, detail, _ := (Tools{}).Check(context.Background(), e); ok || detail != "mise is not installed for the service user" {
		t.Fatalf("%v %q", ok, detail)
	}
	if err := (Tools{}).Apply(context.Background(), e); err == nil {
		t.Fatal("applied without mise")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `cd installer && go test ./internal/steps/ -run Tools`
Expected: build failure, `undefined: Tools`.

- [ ] **Step 3: Implement**

`answers.go`, after `Playwright`:

```go
	// Tools are mise tools and versions the Tools step sets globally for the service user.
	Tools map[string]string `toml:"tools,omitempty"`
```

In `Overlay`:

```go
	if o.Tools != nil {
		a.Tools = o.Tools
	}
```

`main.go` `if fromStdin {` block gains `a.Tools = in.Tools`.

`installer/internal/steps/tools.go`:

```go
package steps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// Tools sets the host file's mise tools as the service user's global versions, so a repo's
// ./setup finds them through mise's shims without pinning them itself. NF-211, NF-212 and NF-260
// failed on "No version is set for shim: go" with go installed under mise and no global version.
type Tools struct{}

const miseRel = ".local/bin/mise"

func (Tools) Name() string { return "Tools" }

// unset lists "name version" for each tool whose global version is not the host file's. mise
// current prints nothing, and exits 0, for a tool with no version set.
func (Tools) unset(ctx context.Context, e *Env) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(e.Answers.Tools)) {
		want := e.Answers.Tools[name]
		r, err := e.Run.Run(ctx, host.Cmd{Name: e.Real(miseRel), Args: []string{"current", name}, Dir: e.Home})
		if err != nil || r.Code != 0 || strings.TrimSpace(r.Stdout) != want {
			out = append(out, name+" "+want)
		}
	}
	return out
}

func (t Tools) Check(ctx context.Context, e *Env) (bool, string, error) {
	if len(e.Answers.Tools) == 0 {
		return true, "none in the host file", nil
	}
	if _, err := os.Stat(e.HomePath(miseRel)); err != nil {
		return false, "mise is not installed for the service user", nil
	}
	if u := t.unset(ctx, e); len(u) > 0 {
		return false, "not set: " + strings.Join(u, ", "), nil
	}
	return true, Plural(len(e.Answers.Tools), "tool"), nil
}

func (t Tools) Apply(ctx context.Context, e *Env) error {
	if _, err := os.Stat(e.HomePath(miseRel)); err != nil {
		return errors.New("mise is not installed for the service user; install it with curl https://mise.run | sh as that user")
	}
	for _, nv := range t.unset(ctx, e) {
		name, version, _ := strings.Cut(nv, " ")
		c := host.Cmd{Name: e.Real(miseRel), Args: []string{"use", "-g", fmt.Sprintf("%s@%s", name, version)}, Dir: e.Home}
		if err := e.UI.Spin(c.String(), func() error { _, err := e.MustCmd(ctx, c); return err }); err != nil {
			return err
		}
	}
	return nil
}
```

Add `"maps"` to the imports. `ServiceSteps` in `list.go`:

```go
	return []Step{Repo{}, Identity{}, Claude{}, ClaudeLogin{}, Secrets{}, Permissions{}, Tools{}, Config{}, Run{}}
```

- [ ] **Step 4: Run the tests**

Run: `cd installer && go test ./... && gofmt -l . && go vet ./...`
Expected: pass. Update any test that lists `ServiceSteps()` names to include `Tools`.

- [ ] **Step 5: Document**

In `docs/book/setup/running-a-host.md`, after the new "A repo whose main fails setup" section:

````markdown
## What the host carries for the repos

Two host-file keys install what repos' setups and checks need and the service user cannot install itself:

```toml
playwright = "1.64.0"   # chromium's system libraries and fonts, on apt hosts

[tools]                 # mise global versions for the service user
go = "1.27.1"
```

Use the Playwright version the repos pin. A tool in `[tools]` must already be installable by mise; `flocc ship` sets it with `mise use -g`.
````

`CHANGELOG.md`, under `## Unreleased`:

```markdown
- Host file `playwright = "<version>"` installs chromium's system libraries and fonts on apt
  hosts, and `[tools]` sets mise tools' global versions for the service user.
```

- [ ] **Step 6: Commit**

```bash
git add installer docs/book/setup/running-a-host.md CHANGELOG.md
git commit -s -m "installer: Tools step sets the host file's mise tools globally"
```

---

### Task 6: Ship to flocc-nimble and verify (owner present)

**Files:** the owner's host file for flocc-nimble (on the desktop under `~/.config/flocc/`; find it with `flocc host` output or `ls ~/.config/flocc/hosts`).

- [ ] **Step 1:** Add `playwright = "1.64.0"` and `[tools]` with `go = "1.27.1"` to flocc-nimble's host file. Ask the owner before editing it.
- [ ] **Step 2:** From the branch checkout, `~/go/bin/flocc ship -H flocc-nimble --plain`, run in the background. Expect rows `Browsers  chromium deps for playwright 1.64.0` and `Tools  1 tool`.
- [ ] **Step 3:** On the host: `sudo -iu flocc sh -c 'cd ~ && mise current go'` prints `1.27.1`, and `npx -y playwright@1.64.0 install-deps --dry-run chromium` prints no "Missing system dependencies".
- [ ] **Step 4:** `flocc -H flocc-nimble canary nimblefox-web` exits 0.
