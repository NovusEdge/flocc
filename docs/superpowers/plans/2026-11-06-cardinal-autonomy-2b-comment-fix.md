# Cardinal autonomy part 2b: comment fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A steering comment (the owner's, or a `steer_authors` id's) on a done issue whose PR is still open starts one fix session on the same branch, which pushes to the open PR without a second review.

**Architecture:** A new tick step, `poller.comment_fixes`, runs after `poll` and before `rebase.step`. It reads new comments of each recent done claim with an open PR, keeps the steering ones, and, when every gate is open, calls `supervisor.start_comment_fix`, which puts the claim back to running in the fix phase with the comments as its context. `_finish_phase` reads meta `comment_fix:KEY`: it publishes with `reuse_only=True`, sends owner items to the issue comment, accepts `--nothing-to-do`, and ends the claim done. The finished-PR guards `rebase.step` uses move into `poller.py`, where `main_hold` and `still_wanted` already live, and both steps share them.

**Tech Stack:** Python 3.13 (stdlib only), SQLite (`flocc.ledger`), pytest via `uv run pytest`, ruff, pyrefly.

**Spec:** `docs/superpowers/specs/2026-11-06-cardinal-autonomy-2-design.md`, section 2 and its testing lines. Section 1 is plan 2a's.

## Global Constraints

- Commits use `git commit -s`, with no `Co-Authored-By` line and no AI attribution. Subject lines are 72 characters or fewer, with the conventional prefixes `git log` shows.
- While iterating, run the focused tests: `uv run pytest tests/<file>::<name> -q`, or the file. Run the full check once, at the end (Task 6):
  `PATH="$(dirname $(uvx --from shellcheck-py which shellcheck)):$PATH" just check`
- Comments in code carry only facts the code cannot show. No narration, no restated calls.
- Reuse the existing helpers named in each task. `rebase.step`'s guards are shared through `poller.finished_prs`, `poller.rerun_held` and `poller.pushed_elsewhere`, never copied.
- Stay out of plan 2a's code: `_block`, `events.py`, `notice.py`, `causes.py`, `control.py` and the triage skill. Plan 2a also edits `supervisor.py` (`_block`, `_timed_out`, the two check-failed `_block` calls in `_finish_phase`), the tuple of cleared meta keys in `poller._start` (it drops `"cardinal_fix"`) and `CHANGELOG.md`. If 2a merges first, rebase and keep both sides; the hunks are next to each other but do not change the same lines.
- Meta keys: `comment_fix:KEY` (`"1"` while a comment fix runs), `comment_fixes:KEY` (count per claim), `comment_fix_moved:KEY` (the head-mismatch comment was posted), `comment_fix_since` (epoch seconds, as a string, set by the first scan). `poller._start` clears the three per-claim keys.
- Comment text, exactly:
  - on start: `Started a fix session for the comment; it pushes to <URL>, with no new review.`
  - on push: `Comment fix pushed to <URL>.` plus `\n\n<owner items>` when there are any
  - on nothing to do: `Nothing to do for the comment: <reason>`
  - cap note, appended to the last two: `\n\nThat was this run's last comment fix; further comments need a Todo rerun.`
  - pr_closed: `The PR was closed or merged during the comment fix; nothing more was published.`
  - head mismatch: `The PR has commits this host's copy of the branch lacks, so no fix session started for the comment; it waits until they match. Move the issue back to Todo to rerun it with the comment instead.`

## Decisions taken in this plan

- The scan lives in `poller.py` as `comment_fixes`, and the shared guards move there too. `rebase.py` imports `poller`, so the guards cannot stay in `rebase.py` and be used by `poller`.
- The spec's "running count at `max_concurrent`, or usage over the limit" is not a separate check. There is no `max_concurrent`; `admit_rerun` already refuses at `session_cap` and over either usage limit, and the scan calls it through `rerun_held`.
- `flocc done` runs as `flocc-agent` and cannot read the ledger, so it learns that it is in a comment fix from a handoff file, `comment-fix`, in its state directory. The scan writes it, `launch` hands it over (it is in `_HANDOFF`, so a restart keeps it), and the first check round after the comment fix deletes it. `_finish_phase` also accepts `nothing` only while `fix_attempts == 0`.
- `review_fix:KEY` stays set for the rest of a claim once its round ran. `_finish_phase` therefore reads it as unset while `comment_fix:KEY` is set, so a comment fix on a reviewed-and-fixed claim neither says "reviewing again" nor starts a review.
- A check round inside a comment fix keeps the comments in its context, under "This session was started with:", as a rebase does.
- The cap note also goes on a nothing-to-do ending, since that fix used a slot too.
- The `ended` event of a comment fix reads `done, comment fix: PR <URL>`. It starts with `done`, so triage's match on `done` holds.
- A failed `comments_since` in the scan prints one line and skips the claim. The poll's own Linear calls already count toward the `linear-api` page.
- The head-mismatch comment is posted once per claim. `_start` clears the key.
- `comment_fix_since` holds epoch seconds. An empty value counts as unset, so the first scan sets it.
- A launch failure leaves one digest line per `DEDUPE_S`, under alert cause `KEY:comment-fix-start`, as the rebase does.
- A done claim keeps phase `fix` after a comment fix. Nothing reads a done claim's phase except the session name, which is the same for `impl` and `fix`.

## Review Focus

- A done claim whose review fix round already ran (`review_fix:KEY` is `1`): the comment fix publishes with `reuse_only`, posts "Comment fix pushed", and starts no review. Pinned in Task 4 and Task 5.
- A launch that fails: claim state, phase, counters, `started_at`, the cursor, `check-failure.md`, `owner-items.md`, the marker and both meta keys are as they were, and no comment is posted. The next tick reads the same comments again. Pinned in Task 5.
- A claim at `comment_fix_max`, held, older than 7 days, or with the feature off: `comments_since` is never called. Pinned in Task 5.
- Comments created before `comment_fix_since` never start a fix, and the first scan sets the key. Pinned in Task 5.
- A comment during a review or a rebase session: not forwarded, cursor unmoved, so the scan finds it once the claim is done. Pinned in Task 3.
- `--nothing-to-do` after a check round inside a comment fix: refused by `flocc done` (no marker) and, if it arrives anyway, published as work. Pinned in Task 4.

---

### Task 1: The `comment_fix_max` config key

**Files:**
- Modify: `src/flocc/config.py` (`Config` fields, after `steer_authors`; `load`, after the `min_available_mem_gb` check)
- Modify: `src/flocc/settings.py` (`NOT_SETTABLE`)
- Modify: `deploy/config.example.toml` (after the `steer_authors` lines)
- Test: `tests/test_config.py`, `tests/test_settings.py`

**Interfaces:**
- Produces: `Config.comment_fix_max: int = 3`, valid 0 to 10, 0 turns the scan off. Not settable.

- [ ] **Step 1: Write the failing tests**

In `tests/test_config.py`, add two lines to the `line` list of `test_out_of_range_number_is_an_error` (around line 64):

```python
        "comment_fix_max = 11",
        "comment_fix_max = -1",
```

and add below that test:

```python
def test_comment_fix_max_defaults_to_three():
    assert load(EXAMPLE).comment_fix_max == 3
```

In `tests/test_settings.py`, after `test_cardinal_file_on_is_a_settable_bool`:

```python
def test_comment_fix_max_starts_paid_sessions_so_it_is_not_settable():
    assert "comment_fix_max" in settings.NOT_SETTABLE
    with pytest.raises(settings.SettingError, match="not settable"):
        settings.check("comment_fix_max")
```

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest tests/test_config.py tests/test_settings.py -q`
Expected: FAIL: `TypeError: Config.__init__() got an unexpected keyword argument 'comment_fix_max'` and `AttributeError ... comment_fix_max`.

- [ ] **Step 3: Add the field and its range**

In `config.py`, in `Config` after the `steer_authors` field and its comment:

```python
    # Fix sessions that steering comments on a done issue's open PR may start per claim. Past it
    # the comments wait for a Todo rerun; 0 turns the scan off.
    comment_fix_max: int = 3
```

In `load`, after the `min_available_mem_gb` check and before `return cfg`:

```python
    if not 0 <= cfg.comment_fix_max <= 10:
        raise ValueError(f"{path}: comment_fix_max {cfg.comment_fix_max} is not in 0-10")
```

In `settings.py`, add `"comment_fix_max",` to `NOT_SETTABLE` after `"steer_authors",`.

In `deploy/config.example.toml`, after the `# steer_authors = [...]` line:

```toml
# Fix sessions your comments, or a steer_authors id's, may start on a done issue whose PR is
# still open, per run. They push to the PR with no new review; 0 turns them off.
# comment_fix_max = 3
```

- [ ] **Step 4: Run the tests**

Run: `uv run pytest tests/test_config.py tests/test_settings.py -q`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/config.py src/flocc/settings.py deploy/config.example.toml tests/test_config.py tests/test_settings.py
git commit -s -m "feat(config): comment_fix_max caps comment fixes per claim"
```

### Task 2: Share the finished-PR guards between the rebase step and the scan

A refactor with no change in behaviour; `tests/test_rebase.py` is its test.

**Files:**
- Modify: `src/flocc/poller.py` (imports; new functions after `main_hold`, around line 194)
- Modify: `src/flocc/rebase.py` (whole module)
- Test: `tests/test_rebase.py` (unchanged)

**Interfaces:**
- Produces, in `flocc.poller`:
  - `finished_prs(rt) -> Iterator[tuple[Claim, str]]`: done claims with a PR URL and an existing worktree whose issue is not held, with the URL.
  - `rerun_held(rt, claim, bases: dict[str, str | None]) -> str | None`: `main_hold`'s reason, else `admit_rerun`'s refusal reason, else None.
  - `pushed_elsewhere(rt, claim, view: dict) -> bool`: the PR's `headRefOid` differs from `rt.git_head(claim)`.
- Consumes: `control.admit_rerun`, `publisher.pr_url`, `poller.main_hold`.

- [ ] **Step 1: Run the rebase tests as the baseline**

Run: `uv run pytest tests/test_rebase.py -q`
Expected: PASS.

- [ ] **Step 2: Add the guards to `poller.py`**

Add to the imports: `from collections.abc import Iterator` with the stdlib imports, `from flocc.control import admit_rerun` and `from flocc.publisher import pr_url` with the `flocc` imports (alphabetical, as ruff's isort wants). `control` does not import `poller`, so there is no cycle.

After `main_hold`:

```python
def finished_prs(rt) -> Iterator[tuple[Claim, str]]:
    """Done claims with a PR and their worktree, whose issue is not held, with the PR's URL. A
    reaped worktree leaves its PR to the owner."""
    holds = ledger.issue_holds(rt.con)
    for claim in ledger.finished_claims(rt.con):
        url = pr_url(rt, claim)
        if claim.state != "done" or url is None or not Path(claim.worktree).exists():
            continue
        if claim.issue_key not in holds:
            yield claim, url


def rerun_held(rt, claim: Claim, bases: dict[str, str | None]) -> str | None:
    """Why no new session may start on a finished claim now, or None."""
    if reason := main_hold(rt, claim.repo, bases):
        return reason
    decision = admit_rerun(rt, claim)
    return None if decision.ok else decision.reason


def pushed_elsewhere(rt, claim: Claim, view: dict) -> bool:
    """Someone pushed to the PR after this host's branch: a session on the stale copy would only
    fail at the push."""
    return view.get("headRefOid") != rt.git_head(claim)
```

- [ ] **Step 3: Rewrite `rebase.py` on them**

Replace the whole module with:

```python
import sys

from flocc import ledger
from flocc.poller import finished_prs, pushed_elsewhere, rerun_held, still_wanted
from flocc.supervisor import REBASE_TRIED, block_unrebased, start_rebase
from flocc.worktree import origin_base


def step(rt) -> None:
    """Starts a fix session on a finished claim's PR when the base moved and it conflicts.

    Needs the claim's worktree, so a PR whose worktree was reaped is left to its owner."""
    if rt.pr_view is None:
        return
    bases: dict[str, str | None] = {}
    for claim, url in finished_prs(rt):
        key = claim.issue_key
        try:
            view = rt.pr_view(claim, url)
            if view.get("state") != "OPEN" or view.get("mergeable") != "CONFLICTING":
                continue
            if pushed_elsewhere(rt, claim, view):
                continue
            if not still_wanted(rt, key):
                continue
        except Exception as exc:  # noqa: BLE001 - one PR's failure must not stop the others
            print(f"{key}: PR not checked for conflicts: {exc}", file=sys.stderr)
            continue
        if claim.repo not in bases:
            bases[claim.repo] = origin_base(rt.cfg.repos[claim.repo])
        base = bases[claim.repo]
        if base is None:
            continue
        if ledger.meta_get(rt.con, f"{REBASE_TRIED}:{key}") == base:
            block_unrebased(rt, claim, base)
            continue
        if reason := rerun_held(rt, claim, bases):
            print(f"{key}: rebase held, {reason}", file=sys.stderr)
            continue
        start_rebase(rt, claim, base)
```

- [ ] **Step 4: Run the tests**

Run: `uv run pytest tests/test_rebase.py tests/test_poller.py -q`
Expected: PASS, the same count as Step 1 for `test_rebase.py`.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/poller.py src/flocc/rebase.py
git commit -s -m "refactor(rebase): share the finished-PR guards through poller"
```

### Task 3: `forward_comments` leaves review and rebase sessions alone

**Files:**
- Modify: `src/flocc/poller.py` (`forward_comments`, around line 478; the `flocc.supervisor` import line)
- Test: `tests/test_poller.py`

**Interfaces:**
- Consumes: `supervisor.REBASE` (`"rebase"`).

- [ ] **Step 1: Write the failing test**

In `tests/test_poller.py`, after `test_reply_to_a_dead_session_is_forwarded_after_the_restart`:

```python
@pytest.mark.parametrize(("phase", "rebasing"), [("review", ""), ("fix", "1")])
def test_comments_for_a_reviewer_or_a_rebase_wait_unread(rt, phase, rebasing):
    c = make("NF-1")
    c.phase = phase
    ledger.insert_claim(rt.con, c)
    ledger.meta_set(rt.con, "rebase:NF-1", rebasing)
    rt.linear.replies = {
        "id-NF-1": [Comment("c1", "use option B", "2026-10-09T10:05:00Z", mine=True)]
    }
    rt.sessions.alive_names.update({"NF-1", "NF-1-review"})
    forward_comments(rt)
    c = ledger.get_claim(rt.con, "NF-1")
    assert rt.sessions.sent == [] and c is not None and c.last_comment_at is None
```

- [ ] **Step 2: Run it to see it fail**

Run: `uv run pytest tests/test_poller.py::test_comments_for_a_reviewer_or_a_rebase_wait_unread -q`
Expected: FAIL: the comment is sent to `NF-1-review` and to `NF-1`.

- [ ] **Step 3: Skip them in `forward_comments`**

Add `REBASE` to the `from flocc.supervisor import ...` line (`BLOCKED, LAST_SUMMARY, QUESTIONS, REBASE, RESUME_NOTE, launch`). In `forward_comments`, after `if claim.state != "running": continue`:

```python
        # Direction is not a reviewer's or a merge's to act on. The cursor stays, so the
        # comment-fix scan reads these once the claim is done.
        if claim.phase == "review" or ledger.meta_get(rt.con, f"{REBASE}:{claim.issue_key}"):
            continue
```

- [ ] **Step 4: Run the tests**

Run: `uv run pytest tests/test_poller.py -q -k "forward or reply or wait_unread"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/poller.py tests/test_poller.py
git commit -s -m "feat(poller): leave comments for review and rebase sessions unread"
```

### Task 4: Start and finish a comment fix in the supervisor; `flocc done --nothing-to-do`

**Files:**
- Modify: `src/flocc/supervisor.py`: constants after `RESUME_NOTE` (around line 55); `_HANDOFF`; new `start_comment_fix` after `start_rebase` (around line 414); new `_clear_comment_fix` and `_end_comment_fix` after `_post_owner_items` (around line 334); `_finish_phase` (around lines 464-640, five places)
- Modify: `src/flocc/cli.py` (`cmd_done`, the `elif nothing is not None:` branch, around line 291)
- Modify: `plugin/skills/fixer/SKILL.md`
- Test: `tests/test_supervisor.py`, `tests/test_cli.py`

**Interfaces:**
- Produces: `supervisor.COMMENT_FIX = "comment_fix"`, `supervisor.COMMENT_FIXES = "comment_fixes"`, `supervisor.COMMENT_FIX_FILE = "comment-fix"`; `supervisor.start_comment_fix(rt, claim: Claim, url: str, replies: list[str], cursor: str) -> None`. `replies` are lines `"<who>: <body>"`, oldest first; `cursor` is the `created_at` of the last comment read.
- Consumes: `launch`, `_comment_or_digest`, `_owner_items`, `_end_done`, `FIX_CONTEXT`, `OWNER_ITEMS`, `ledger.alert_seen`/`record_alert`/`add_digest`, `Config.comment_fix_max` (Task 1).

- [ ] **Step 1: Write the failing supervisor tests**

In `tests/test_supervisor.py`, at the end:

```python
def comment_fix(rt, fixes="1"):
    """A claim whose comment fix session just ran, after its review fix round."""
    add(rt, phase="fix", sequence=["executor", "reviewer"])
    ledger.meta_set(rt.con, "comment_fix:NF-1", "1")
    ledger.meta_set(rt.con, "comment_fixes:NF-1", fixes)
    ledger.meta_set(rt.con, "review_fix:NF-1", "1")
    rt.sessions.alive_names.add("NF-1")
    rt.publish_fn = lambda *a, **kw: PublishResult("opened", url="https://pr/1", existing=bool(kw))


def test_a_comment_fix_pushes_to_the_open_pr_with_the_owner_items_and_no_review(rt):
    comment_fix(rt)
    done(rt, "Renamed the flag.", ["Is the old flag documented anywhere public?"])
    supervise(rt, current(rt))
    assert current(rt).state == "done" and not rt.notifier.sent
    assert rt.linear.comments[-1] == (
        "Comment fix pushed to https://pr/1.\n\n"
        "Needs the owner:\n1. Is the old flag documented anywhere public?"
    )
    assert all(name != "NF-1-review" for name, _ in rt.sessions.started)
    assert not ledger.meta_get(rt.con, "comment_fix:NF-1")
    [e] = events.unacked(rt.con)
    assert (e.kind, e.detail) == ("ended", "done, comment fix: PR https://pr/1")


def test_the_last_comment_fix_says_further_comments_need_a_rerun(rt):
    comment_fix(rt, fixes="3")
    done(rt, "Renamed the flag.")
    supervise(rt, current(rt))
    assert rt.linear.comments[-1].endswith("further comments need a Todo rerun.")


def test_an_earlier_comment_fix_has_no_cap_note(rt):
    comment_fix(rt, fixes="2")
    done(rt, "Renamed the flag.")
    supervise(rt, current(rt))
    assert "Todo rerun" not in rt.linear.comments[-1]


def test_a_comment_fix_with_nothing_to_do_ends_done_without_a_push(rt):
    comment_fix(rt)
    published = []
    rt.publish_fn = lambda *a, **kw: published.append(kw) or PublishResult("opened", url="u")
    done(rt, "It asks a question: the flag stays.", outcome="nothing")
    supervise(rt, current(rt))
    assert current(rt).state == "done" and not published
    why = "It asks a question: the flag stays."
    assert rt.linear.comments == [f"Nothing to do for the comment: {why}"]
    [e] = events.unacked(rt.con)
    assert e.detail == "done, nothing to do: It asks a question: the flag stays."


def test_nothing_to_do_after_a_check_round_still_publishes(rt):
    comment_fix(rt)
    ledger.update_claim(rt.con, "NF-1", fix_attempts=1)
    published = []
    rt.publish_fn = lambda *a, **kw: published.append(kw) or PublishResult(
        "opened", url="https://pr/1", existing=True
    )
    done(rt, "Nothing.", outcome="nothing")
    supervise(rt, current(rt))
    assert published == [{"reuse_only": True}]


def test_a_pr_closed_during_a_comment_fix_names_it(rt):
    comment_fix(rt)
    rt.publish_fn = lambda *a, **kw: PublishResult("pr_closed")
    done(rt, "Renamed the flag.")
    supervise(rt, current(rt))
    assert current(rt).state == "done" and not ledger.meta_get(rt.con, "comment_fix:NF-1")
    assert "closed or merged during the comment fix" in rt.linear.comments[-1]


def test_a_check_round_in_a_comment_fix_keeps_the_comments_and_drops_the_marker(rt):
    comment_fix(rt)
    d = state_dir(rt.cfg, "NF-1")
    (d / "check-failure.md").write_text("The owner: rename the flag to --dry-run")
    (d / "comment-fix").write_text("1\n")
    rt.publish_fn = lambda *a, **kw: PublishResult("check_failed", "FAILED tests/test_a.py::test_x")
    done(rt, "Renamed.")
    supervise(rt, current(rt))
    c = current(rt)
    assert (c.state, c.phase, c.fix_attempts) == ("running", "fix", 1)
    context = (d / "check-failure.md").read_text()
    assert "rename the flag to --dry-run" in context and "test_x" in context
    assert not (d / "comment-fix").exists()


def test_a_comment_fix_restart_keeps_its_comments_and_its_marker(rt):
    comment_fix(rt)
    d = state_dir(rt.cfg, "NF-1")
    (d / "check-failure.md").write_text("The owner: rename the flag to --dry-run")
    (d / "comment-fix").write_text("1\n")
    rt.sessions.alive_names.clear()
    supervise(rt, current(rt))
    name, argv = rt.sessions.started[-1]
    assert name == "NF-1" and any("rename the flag to --dry-run" in a for a in argv)
    assert rt.sessions.starts[-1]["files"]["comment-fix"] == "1\n"
```

- [ ] **Step 2: Write the failing `flocc done` test**

In `tests/test_cli.py`, after `test_nothing_to_do_is_refused_outside_its_use` (whose `("fix", [])` case stays: without the marker a fixer is refused):

```python
def test_a_comment_fix_may_end_with_nothing_to_do(tmp_path, monkeypatch):
    session_env(monkeypatch, tmp_path, "fix")
    (tmp_path / "comment-fix").write_text("1\n")
    assert main(["done", "--nothing-to-do", "The comment asks no change."]) == 0
    data = json.loads((tmp_path / "done.json").read_text())
    assert (data["outcome"], data["summary"]) == ("nothing", "The comment asks no change.")
```

- [ ] **Step 3: Run them to see them fail**

Run: `uv run pytest tests/test_supervisor.py tests/test_cli.py -q -k "comment_fix or comment fix or nothing"`
Expected: FAIL: the comment fix starts a review and says "Review fix pushed"; the restart's `files` has no `comment-fix`; `flocc done` refuses in the fix phase.

- [ ] **Step 4: Add the constants and the handoff file**

In `supervisor.py`, after `RESUME_NOTE = "resume-note.md"`:

```python
# Set while a fix session works on steering comments posted on a done claim's open PR;
# COMMENT_FIXES counts them per claim against comment_fix_max.
COMMENT_FIX = "comment_fix"
COMMENT_FIXES = "comment_fixes"
# Handed to the comment fix's own session only, so its flocc done --nothing-to-do is accepted. A
# check round after it must make the check pass.
COMMENT_FIX_FILE = "comment-fix"
```

Add `COMMENT_FIX_FILE,` as the last entry of `_HANDOFF`, after `CHECK_OUTPUT,`.

- [ ] **Step 5: Add `start_comment_fix`**

After `start_rebase` and before `block_unrebased`:

```python
def start_comment_fix(rt, claim: Claim, url: str, replies: list[str], cursor: str) -> None:
    """One fix session on a done claim's open PR for steering comments posted since it finished.
    A launch that fails leaves the claim as it was, cursor included, so a later tick reads the
    comments again."""
    key = claim.issue_key
    extra = (
        "Comments on the issue since its PR opened, oldest first:\n\n"
        + "\n\n".join(replies)
        + "\n\nMake the change they ask for that is agent work, on this branch; `flocc done` runs"
        " the full check and pushes to the open PR. For what needs the owner (a decision, access,"
        " a manual step), make no change and pass it to `flocc done` as `--needs-owner '<item>'`,"
        " one per item. If they ask for no change to the code, finish with"
        " `flocc done --nothing-to-do '<your answer, for the owner>'`."
    )
    d = state_dir(rt.cfg, key)
    d.mkdir(parents=True, exist_ok=True)
    kept = {n: (d / n).read_text() for n in (FIX_CONTEXT, OWNER_ITEMS) if (d / n).exists()}
    fixes = ledger.meta_get(rt.con, f"{COMMENT_FIXES}:{key}") or ""
    (d / OWNER_ITEMS).unlink(missing_ok=True)
    (d / FIX_CONTEXT).write_text(extra)
    (d / COMMENT_FIX_FILE).write_text("1\n")
    # Before the launch, like the rebase: a tick that dies after the session starts must find the
    # claim running in the fix phase, its publish reusing the open PR and the comments read.
    ledger.meta_set(rt.con, f"{COMMENT_FIX}:{key}", "1")
    ledger.meta_set(rt.con, f"{COMMENT_FIXES}:{key}", str(int(fixes or 0) + 1))
    ledger.update_claim(
        rt.con,
        key,
        state="running",
        phase="fix",
        fix_attempts=0,
        restarts=0,
        started_at=rt.now(),
        last_comment_at=cursor,
    )
    try:
        launch(rt, claim, "fix", extra=extra)
    except Exception as exc:  # noqa: BLE001 - the PR is open; nothing here may block the claim
        print(f"{key}: comment fix not started: {exc}", file=sys.stderr)
        cause = f"{key}:comment-fix-start"
        if not ledger.alert_seen(rt.con, cause, ledger.DEDUPE_S, rt.now()):
            ledger.add_digest(rt.con, key, f"comment fix not started ({exc})", rt.now())
            ledger.record_alert(rt.con, cause, 3, key, rt.now())
        (d / COMMENT_FIX_FILE).unlink(missing_ok=True)
        for name in (FIX_CONTEXT, OWNER_ITEMS):
            if name in kept:
                (d / name).write_text(kept[name])
            else:
                (d / name).unlink(missing_ok=True)
        ledger.meta_set(rt.con, f"{COMMENT_FIX}:{key}", "")
        ledger.meta_set(rt.con, f"{COMMENT_FIXES}:{key}", fixes)
        ledger.update_claim(
            rt.con,
            key,
            state=claim.state,
            phase=claim.phase,
            fix_attempts=claim.fix_attempts,
            restarts=claim.restarts,
            started_at=claim.started_at,
            last_comment_at=claim.last_comment_at,
        )
        return
    _comment_or_digest(
        rt, claim, f"Started a fix session for the comment; it pushes to {url}, with no new review."
    )
```

- [ ] **Step 6: Add the finish helpers**

After `_post_owner_items`:

```python
def _clear_comment_fix(rt, key: str) -> None:
    ledger.meta_set(rt.con, f"{COMMENT_FIX}:{key}", "")
    (state_dir(rt.cfg, key) / COMMENT_FIX_FILE).unlink(missing_ok=True)


def _end_comment_fix(rt, claim: Claim, line: str, detail: str) -> None:
    key = claim.issue_key
    if int(ledger.meta_get(rt.con, f"{COMMENT_FIXES}:{key}") or 0) >= rt.cfg.comment_fix_max:
        line += "\n\nThat was this run's last comment fix; further comments need a Todo rerun."
    _comment_or_digest(rt, claim, line)
    _clear_comment_fix(rt, key)
    _end_done(rt, claim, detail=detail)
```

- [ ] **Step 7: Read the comment fix in `_finish_phase`**

Five edits, in order.

(a) Replace the block from `review_fix = bool(...)` to the end of the existing `if outcome == "nothing" and claim.phase in ("test", "impl"):` branch:

```python
    review_fix = bool(ledger.meta_get(rt.con, f"review_fix:{key}"))
    rebasing = bool(ledger.meta_get(rt.con, f"{REBASE}:{key}"))
    # A review fixer's owner items ride to the final comment instead: the PR is open and a
    # second review follows.
    if questions and not review_fix and not rebasing:
        _ask_owner(rt, claim, list(questions))
        return
    if outcome == "nothing" and claim.phase in ("test", "impl"):
        _comment_or_digest(rt, claim, f"Nothing to do: {summary}")
        _end_done(rt, claim, detail=f"done, nothing to do: {summary}")
        return
```

with:

```python
    comment_fix = bool(ledger.meta_get(rt.con, f"{COMMENT_FIX}:{key}"))
    # review_fix stays set for the rest of the claim once its round ran, and a comment fix after
    # it gets no second review.
    review_fix = not comment_fix and bool(ledger.meta_get(rt.con, f"review_fix:{key}"))
    rebasing = bool(ledger.meta_get(rt.con, f"{REBASE}:{key}"))
    # A review or comment fixer's owner items ride to the final comment instead: the PR is open.
    if questions and not review_fix and not rebasing and not comment_fix:
        _ask_owner(rt, claim, list(questions))
        return
    if outcome == "nothing" and claim.phase in ("test", "impl"):
        _comment_or_digest(rt, claim, f"Nothing to do: {summary}")
        _end_done(rt, claim, detail=f"done, nothing to do: {summary}")
        return
    if outcome == "nothing" and comment_fix and claim.fix_attempts == 0:
        line = f"Nothing to do for the comment: {summary}"
        _end_comment_fix(rt, claim, line, f"done, nothing to do: {summary}")
        return
```

(b) The owner-items write: change `if claim.phase == "fix" and review_fix and not rebasing and questions:` to

```python
    if claim.phase == "fix" and (review_fix or comment_fix) and not rebasing and questions:
```

(c) The publish: replace

```python
        # After a review fix or a rebase the PR must still be open: a new one would reopen
        # finished work.
        result = (
            rt.publish_fn(claim, title, summary, reuse_only=True)
            if review_fix or rebasing
            else rt.publish_fn(claim, title, summary)
        )
```

with

```python
        # After a review fix, a comment fix or a rebase the PR must still be open: a new one
        # would reopen finished work.
        result = (
            rt.publish_fn(claim, title, summary, reuse_only=True)
            if review_fix or rebasing or comment_fix
            else rt.publish_fn(claim, title, summary)
        )
```

(d) The `pr_closed` branch: change `what = "rebase" if rebasing else "review fix"` to

```python
        what = "rebase" if rebasing else "comment fix" if comment_fix else "review fix"
```

and after `ledger.meta_set(rt.con, f"{REBASE}:{key}", "")` in that branch add `_clear_comment_fix(rt, key)`.

In the `opened` branch, change the first chain's final `else:` (the one that comments `PR for review:` and adds the digest line) to `elif not comment_fix:`. In the second chain, between the `if rebasing:` branch and `elif "reviewer" in claim.sequence:`, add:

```python
        elif comment_fix:
            owner = _owner_items(rt, claim)
            line = f"Comment fix pushed to {result.url}." + (f"\n\n{owner}" if owner else "")
            _end_comment_fix(rt, claim, line, f"done, comment fix: PR {result.url}")
```

(e) The check round (`elif result.status == "check_failed" and claim.fix_attempts < rt.cfg.check_fix_rounds:`): change `elif context.exists() and rebasing:` to `elif context.exists() and (rebasing or comment_fix):`, and after `(state_dir(rt.cfg, key) / FAILURE_HASH).write_text(_digest(result.detail))` add:

```python
        (state_dir(rt.cfg, key) / COMMENT_FIX_FILE).unlink(missing_ok=True)
```

- [ ] **Step 8: Accept `--nothing-to-do` in a comment fix**

In `cli.py` `cmd_done`, replace

```python
    elif nothing is not None:
        if phase not in ("test", "impl"):
            return _done_refused("--nothing-to-do is for the tester and the executor")
```

with

```python
    elif nothing is not None:
        from flocc.supervisor import COMMENT_FIX_FILE

        comment_fix = phase == "fix" and (state / COMMENT_FIX_FILE).exists()
        if phase not in ("test", "impl") and not comment_fix:
            return _done_refused(
                "--nothing-to-do is for the tester, the executor and a comment fix"
            )
```

- [ ] **Step 9: Tell the fixer skill**

In `plugin/skills/fixer/SKILL.md`, change the `description` to `Use when you are the fixer session for a flocc issue (a failed check, a review's findings, comments on an open PR, or a PR that no longer merges)`, and after the "When the fix answers a review" bullet add:

```markdown
- When you were started on comments posted after the PR opened, do what they ask that is agent
  work. For what needs the owner, change nothing and pass it as `--needs-owner '<item>'`; it
  goes into the issue comment. When they ask for no change to the code (a question, a thanks),
  run `flocc done --nothing-to-do '<your answer, for the owner>'` instead of a summary.
```

- [ ] **Step 10: Run the tests**

Run: `uv run pytest tests/test_supervisor.py tests/test_cli.py tests/test_rebase.py tests/test_plugin.py -q`
Expected: PASS. A failing review-fix test (`test_the_fix_push_comments_once_more_without_a_digest_line`, `test_a_review_fixer_s_questions_do_not_block`) means edit (a) or the opened-branch edit changed the path without `comment_fix`: compare against Step 7.

- [ ] **Step 11: Commit**

```bash
git add src/flocc/supervisor.py src/flocc/cli.py plugin/skills/fixer/SKILL.md tests/test_supervisor.py tests/test_cli.py
git commit -s -m "feat(supervisor): finish a comment fix on the open PR, no review"
```

### Task 5: The comment-fix scan and its place in the tick

**Files:**
- Modify: `src/flocc/poller.py`: constants after `RERUN_COMMENTS_S`; imports; `_start` (the cleared-keys tuple and the unlinks); new `_epoch`, `_comment_fix_waits` and `comment_fixes` after `forward_comments`
- Modify: `src/flocc/tick.py` (imports; `tick`, between `poll(rt)` and the `rebase.step` block)
- Test: `tests/test_poller.py`

**Interfaces:**
- Produces: `poller.comment_fixes(rt) -> None`; `poller.COMMENT_FIX_SINCE = "comment_fix_since"`, `poller.COMMENT_FIX_MOVED = "comment_fix_moved"`, `poller.COMMENT_SETTLE_S = 300`.
- Consumes: `finished_prs`, `rerun_held`, `pushed_elsewhere` (Task 2), `still_wanted`, `_steering`, `RERUN_COMMENTS_S`, `supervisor.start_comment_fix`, `COMMENT_FIXES`, `COMMENT_FIX_FILE` (Task 4), `Config.comment_fix_max` (Task 1).

- [ ] **Step 1: Write the failing tests**

In `tests/test_poller.py`, add `from datetime import UTC, datetime` to the stdlib imports, change `from flocc import events, host, ledger` to `from flocc import canary, events, host, ledger`, add `comment_fixes` to the `flocc.poller` import list, and add `from flocc.usage import write_status`. Then at the end of the file:

```python
NOW = 1_800_000_000.0
PR = "https://github.com/o/r/pull/1"


def iso(ago: float) -> str:
    return datetime.fromtimestamp(NOW - ago, UTC).isoformat()


def nf1(rt):
    c = ledger.get_claim(rt.con, "NF-1")
    assert c is not None
    return c


@pytest.fixture
def fixable(rt, tmp_path, monkeypatch):
    """A claim done an hour ago with an open PR, and an owner comment ten minutes old."""
    rt.now = lambda: NOW
    rt.view = {"state": "OPEN", "headRefOid": "sha0"}
    rt.pr_view = lambda claim, url: rt.view
    rt.linear.state_types = lambda keys: {k: "started" for k in keys}
    monkeypatch.setattr("flocc.poller.origin_base", lambda repo: "sha-main")
    wt = tmp_path / "wt-fix"
    wt.mkdir()
    c = make("NF-1", state="done", worktree=str(wt))
    c.phase, c.started_at, c.base_sha = "review", NOW - 3600, "sha-base"
    c.last_comment_at = iso(7200)
    ledger.insert_claim(rt.con, c)
    d = state_dir(rt.cfg, "NF-1")
    d.mkdir(parents=True, exist_ok=True)
    (d / "pr-url").write_text(PR)
    ledger.meta_set(rt.con, "comment_fix_since", str(NOW - 86_400))
    rt.linear.replies = {
        "id-NF-1": [Comment("c1", "rename the flag to --dry-run", iso(600), mine=True)]
    }
    return rt


def left_alone(rt):
    c = nf1(rt)
    assert rt.sessions.started == [] and c.state == "done"
    assert c.last_comment_at == iso(7200)


def test_an_owner_comment_on_a_done_pr_starts_one_fix_session(fixable):
    comment_fixes(fixable)
    c = nf1(fixable)
    assert (c.state, c.phase, c.fix_attempts, c.restarts, c.started_at) == (
        "running",
        "fix",
        0,
        0,
        NOW,
    )
    assert c.last_comment_at == iso(600)
    name, argv = fixable.sessions.started[-1]
    assert name == "NF-1" and any("The owner: rename the flag to --dry-run" in a for a in argv)
    assert ledger.meta_get(fixable.con, "comment_fix:NF-1") == "1"
    assert ledger.meta_get(fixable.con, "comment_fixes:NF-1") == "1"
    assert fixable.linear.comments[-1][1] == (
        f"Started a fix session for the comment; it pushes to {PR}, with no new review."
    )


def test_the_fix_context_lists_the_steering_comments_oldest_first(fixable):
    fixable.cfg = replace(fixable.cfg, steer_authors=("hermes-id",))
    fixable.linear.replies = {
        "id-NF-1": [
            Comment("c1", "rename the flag", iso(900), mine=True),
            Comment("c2", "a teammate's aside", iso(800), author="other"),
            Comment("c3", "and update the README", iso(700), author="hermes-id"),
        ]
    }
    comment_fixes(fixable)
    d = state_dir(fixable.cfg, "NF-1")
    text = (d / "check-failure.md").read_text()
    assert text.index("The owner: rename the flag") < text.index(
        "A steering agent: and update the README"
    )
    assert "teammate" not in text and "--needs-owner" in text and "--nothing-to-do" in text
    assert (d / "comment-fix").exists()


def _fresh_comment(rt):
    rt.linear.replies["id-NF-1"].append(Comment("c2", "one more thing", iso(120), mine=True))


def _session_cap(rt):
    for key in ("NF-7", "NF-8"):
        ledger.insert_claim(rt.con, make(key))


def _over_usage(rt):
    over = {"used_percentage": 95.0, "resets_at": 4_000_000_000}
    write_status(state_dir(rt.cfg, "NF-1"), {"rate_limits": {"five_hour": over, "seven_day": over}})


def _unwanted(rt):
    rt.linear.state_types = lambda keys: {k: "unstarted" for k in keys}


def _main_broken(rt):
    canary.mark(rt.con, "backend", "sha-main", NOW, "E1 boom", check="just check")


def _pr_merged(rt):
    rt.view = {**rt.view, "state": "MERGED"}


@pytest.mark.parametrize(
    "shut", [_fresh_comment, _session_cap, _over_usage, _unwanted, _main_broken, _pr_merged]
)
def test_a_comment_fix_waits_while_a_gate_is_shut(fixable, shut):
    shut(fixable)
    comment_fixes(fixable)
    left_alone(fixable)


def _held(rt):
    ledger.add_hold(rt.con, "NF-1", "wait", NOW)


def _at_cap(rt):
    ledger.meta_set(rt.con, "comment_fixes:NF-1", "3")


def _a_week_old(rt):
    ledger.update_claim(rt.con, "NF-1", started_at=NOW - 8 * 86_400)


def _turned_off(rt):
    rt.cfg = replace(rt.cfg, comment_fix_max=0)


@pytest.mark.parametrize("outside", [_held, _at_cap, _a_week_old, _turned_off])
def test_a_claim_outside_the_scan_is_not_read(fixable, outside):
    outside(fixable)

    def never(issue_id, after):
        raise AssertionError("comments read")

    fixable.linear.comments_since = never
    comment_fixes(fixable)
    left_alone(fixable)


def test_a_pr_someone_else_pushed_to_gets_one_comment_and_waits(fixable):
    fixable.view = {**fixable.view, "headRefOid": "pushed-by-the-owner"}
    comment_fixes(fixable)
    comment_fixes(fixable)
    left_alone(fixable)
    [(_, text)] = fixable.linear.comments
    assert "commits this host's copy of the branch lacks" in text and "Todo" in text


def test_comments_that_do_not_steer_move_the_cursor_past_them(fixable):
    fixable.linear.replies = {
        "id-NF-1": [
            Comment("c1", f"{BOT_PREFIX} PR for review", iso(900), mine=True),
            Comment("c2", "a teammate's aside", iso(800), author="other"),
        ]
    }
    comment_fixes(fixable)
    c = nf1(fixable)
    assert fixable.sessions.started == [] and c.state == "done"
    assert c.last_comment_at == iso(800)


def test_comments_from_before_the_first_scan_never_start_a_fix(fixable):
    ledger.meta_set(fixable.con, "comment_fix_since", "")
    comment_fixes(fixable)
    assert ledger.meta_get(fixable.con, "comment_fix_since") == str(NOW)
    assert fixable.sessions.started == [] and nf1(fixable).last_comment_at == iso(600)


def test_a_comment_fix_that_cannot_start_leaves_the_claim_as_it_was(fixable):
    d = state_dir(fixable.cfg, "NF-1")
    (d / "check-failure.md").write_text("the last check round's failure")
    (d / "owner-items.md").write_text("Needs the owner:\n1. an earlier item\n")
    fixable.cfg.agent_gh_token_file.write_text("")
    comment_fixes(fixable)
    c = nf1(fixable)
    assert (c.state, c.phase, c.started_at, c.last_comment_at) == (
        "done",
        "review",
        NOW - 3600,
        iso(7200),
    )
    assert not ledger.meta_get(fixable.con, "comment_fix:NF-1")
    assert not ledger.meta_get(fixable.con, "comment_fixes:NF-1")
    assert (d / "check-failure.md").read_text() == "the last check round's failure"
    assert (d / "owner-items.md").exists() and not (d / "comment-fix").exists()
    assert fixable.linear.comments == []
    lines = [line for _, _, line in ledger.pending_digest(fixable.con)]
    assert any("comment fix not started" in line for line in lines)


def test_a_comment_fix_pushes_to_the_open_pr_and_ends_done_without_a_review(fixable):
    calls = []

    def publish(claim, title, summary, **kw):
        calls.append(kw)
        return PublishResult("opened", url=PR, existing=True)

    fixable.publish_fn = publish
    ledger.meta_set(fixable.con, "review_fix:NF-1", "1")
    comment_fixes(fixable)
    (state_dir(fixable.cfg, "NF-1") / "done.json").write_text(
        json.dumps({"summary": "Renamed the flag.", "questions": [], "outcome": "work"})
    )
    supervise(fixable, nf1(fixable))
    assert calls == [{"reuse_only": True}] and nf1(fixable).state == "done"
    assert [name for name, _ in fixable.sessions.started] == ["NF-1"]
    assert fixable.linear.comments[-1][1] == f"Comment fix pushed to {PR}."
    assert not ledger.meta_get(fixable.con, "comment_fix:NF-1")
    assert ledger.meta_get(fixable.con, "comment_fixes:NF-1") == "1"


def test_a_fresh_claim_gets_its_own_comment_fixes(rt):
    for key in ("comment_fix", "comment_fixes", "comment_fix_moved"):
        ledger.meta_set(rt.con, f"{key}:NF-9", "1")
    d = state_dir(rt.cfg, "NF-9")
    d.mkdir(parents=True, exist_ok=True)
    (d / "comment-fix").write_text("1\n")
    rt.linear.ready = [issue()]
    poll(rt)
    for key in ("comment_fix", "comment_fixes", "comment_fix_moved"):
        assert not ledger.meta_get(rt.con, f"{key}:NF-9")
    assert not (d / "comment-fix").exists()


def test_the_scan_runs_after_poll_and_before_the_rebase_step(rt, tmp_path, monkeypatch):
    seen = []
    monkeypatch.setattr("flocc.tick.build_runtime", lambda cfg, path: rt)
    monkeypatch.setattr("flocc.tick.poll", lambda rt: seen.append("poll"))
    monkeypatch.setattr("flocc.tick.comment_fixes", lambda rt: seen.append("comment_fixes"))
    monkeypatch.setattr("flocc.rebase.step", lambda rt: seen.append("rebase"))
    tick(rt.cfg, tmp_path / "c.toml")
    assert seen == ["poll", "comment_fixes", "rebase"]
```

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest tests/test_poller.py -q -k "comment or scan or gate or outside or steer or pushed"`
Expected: FAIL: `ImportError: cannot import name 'comment_fixes' from 'flocc.poller'`.

- [ ] **Step 3: Add the constants and imports in `poller.py`**

Change the `flocc.supervisor` import to:

```python
from flocc.supervisor import (
    BLOCKED,
    COMMENT_FIX_FILE,
    COMMENT_FIXES,
    LAST_SUMMARY,
    QUESTIONS,
    REBASE,
    RESUME_NOTE,
    launch,
    start_comment_fix,
)
```

After `RERUN_COMMENTS_S = 7 * 24 * 3600`:

```python
# Epoch seconds of the first comment-fix scan; comments older than it never start a fix.
COMMENT_FIX_SINCE = "comment_fix_since"
COMMENT_FIX_MOVED = "comment_fix_moved"
# The owner may move the issue to Todo right after commenting, which reruns it with the comment.
COMMENT_SETTLE_S = 5 * 60
```

- [ ] **Step 4: Clear the comment fix on a fresh claim**

In `_start`, add three entries to the tuple of cleared keys, after `"host_wait",`:

```python
        "comment_fix",
        "comment_fixes",
        "comment_fix_moved",
```

and after `(d / RESUME_NOTE).unlink(missing_ok=True)`:

```python
    (d / COMMENT_FIX_FILE).unlink(missing_ok=True)
```

- [ ] **Step 5: Write the scan**

After `forward_comments`:

```python
def _epoch(iso: str) -> float:
    return datetime.fromisoformat(iso).timestamp()


def _comment_fix_waits(rt, claim: Claim) -> None:
    once = f"{COMMENT_FIX_MOVED}:{claim.issue_key}"
    if ledger.meta_get(rt.con, once):
        return
    # Recorded first: a comment that fails must not be retried every tick.
    ledger.meta_set(rt.con, once, "1")
    try:
        rt.linear.comment(
            claim.issue_id,
            "The PR has commits this host's copy of the branch lacks, so no fix session started"
            " for the comment; it waits until they match. Move the issue back to Todo to rerun it"
            " with the comment instead.",
        )
    except LinearError as exc:
        print(f"{claim.issue_key}: comment-fix wait not commented: {exc}", file=sys.stderr)


def comment_fixes(rt) -> None:
    """Starts a fix session on a done claim's open PR for steering comments posted since. A claim
    skipped this tick keeps its comments unread, for a later tick or a Todo rerun."""
    cap = rt.cfg.comment_fix_max
    if cap == 0 or rt.pr_view is None:
        return
    since = ledger.meta_get(rt.con, COMMENT_FIX_SINCE)
    if not since:
        since = str(rt.now())
        ledger.meta_set(rt.con, COMMENT_FIX_SINCE, since)
    bases: dict[str, str | None] = {}
    for claim, url in finished_prs(rt):
        key = claim.issue_key
        if rt.now() - claim.started_at >= RERUN_COMMENTS_S:
            continue
        # Before the read: past the cap the comments stay unread, so a Todo rerun gets them.
        if int(ledger.meta_get(rt.con, f"{COMMENT_FIXES}:{key}") or 0) >= cap:
            continue
        try:
            unread = rt.linear.comments_since(claim.issue_id, claim.last_comment_at)
        except LinearError as exc:
            print(f"{key}: comments not read for a comment fix: {exc}", file=sys.stderr)
            continue
        steering = [
            (who, c)
            for c in unread
            if _epoch(c.created_at) >= float(since) and (who := _steering(rt, c))
        ]
        if not steering:
            if unread:
                ledger.update_claim(rt.con, key, last_comment_at=unread[-1].created_at)
            continue
        if rt.now() - _epoch(steering[-1][1].created_at) < COMMENT_SETTLE_S:
            continue
        if reason := rerun_held(rt, claim, bases):
            print(f"{key}: comment fix held, {reason}", file=sys.stderr)
            continue
        try:
            if not still_wanted(rt, key):
                continue
            view = rt.pr_view(claim, url)
            if view.get("state") != "OPEN":
                continue
            moved = pushed_elsewhere(rt, claim, view)
        except Exception as exc:  # noqa: BLE001 - one PR's failure must not stop the others
            print(f"{key}: PR not checked for a comment fix: {exc}", file=sys.stderr)
            continue
        if moved:
            _comment_fix_waits(rt, claim)
            continue
        replies = [f"{who}: {c.body}" for who, c in steering]
        start_comment_fix(rt, claim, url, replies, unread[-1].created_at)
```

`datetime` is already imported in `poller.py`.

- [ ] **Step 6: Put it in the tick**

In `tick.py`, add `comment_fixes,` to the `from flocc.poller import (...)` list (first, alphabetically). In `tick`, between `poll(rt)` and the comment `# After poll, so an issue started this tick is not mistaken for a finished one.`:

```python
        # After poll, so an issue the owner moved to Todo this tick reruns with the comment
        # instead; before the rebase step, which would start a second session on the claim.
        try:
            comment_fixes(rt)
        except Exception:  # noqa: BLE001 - a comment fix that cannot start must not stop the digest
            traceback.print_exc()
```

- [ ] **Step 7: Run the tests**

Run: `uv run pytest tests/test_poller.py tests/test_rebase.py tests/test_supervisor.py -q`
Expected: PASS. If `_over_usage` starts a session, `latest_usage` did not find the status file: it reads `cfg.agent_state_root / KEY`, which equals `state_dir` in `tmp_cfg`.

- [ ] **Step 8: Commit**

```bash
git add src/flocc/poller.py src/flocc/tick.py tests/test_poller.py
git commit -s -m "feat(poller): start a fix session for comments on a done PR"
```

### Task 6: The book, the changelog, and the full check

**Files:**
- Modify: `docs/book/architecture.md` (the numbered list under "One issue, start to finish", around line 64; the paragraph at line 68; the paragraph at line 83)
- Modify: `docs/book/setup/running-a-host.md` (configuration table, around line 35; "When a run needs you", line 90)
- Modify: `docs/book/commands.md` (the `config set` paragraph, line 79)
- Modify: `CHANGELOG.md` (Unreleased)

- [ ] **Step 1: `architecture.md`**

After item 8 (**Review fix round.**) add:

```markdown
9. **Comment fix.** A comment from you or a `steer_authors` id on a finished issue whose PR is still open starts one fix session on the same branch, once five minutes pass with no newer comment. Moving the issue back to Todo within those five minutes reruns it instead. The session gets the comments as its context, passes what needs you as `--needs-owner`, or ends with `--nothing-to-do` when they ask for no change. The service pushes to the open PR and comments on the issue; no second review runs. Each claim gets `comment_fix_max` (default 3) of these within 7 days of its last session; later comments wait for a Todo rerun. A comment posted during a review or a rebase waits until the run is done.
```

In the paragraph at line 68, replace `Questions passed with `--needs-owner` end the run blocked, except in the review fix round; `--nothing-to-do` ends a tester or executor run done without a PR;` with `Questions passed with `--needs-owner` end the run blocked, except in a review or comment fix; `--nothing-to-do` ends a tester or executor run done without a PR, and a comment fix without a push;`.

At the end of the paragraph at line 83 (`A blocked or timed-out issue keeps its claim ...`), add: `A done issue keeps its claim too, so a comment on its open PR can start a comment fix.`

- [ ] **Step 2: `running-a-host.md`**

In the configuration table, change the `steer_authors` row's meaning to `Linear user or app ids, not names, whose comments steer a running session, or start a comment fix, like yours`, and add after it:

```markdown
| `comment_fix_max` | 3 | Fix sessions your comments may start on a done issue whose PR is open, per run; 0 turns them off |
```

At the end of the "When a run needs you" paragraph that ends `the service types the comment into the session.`, add: `A comment on a finished issue whose PR is still open starts a fix session on the same branch five minutes later, which pushes to the PR without a new review; see [Comment fix](../architecture.md#one-issue-start-to-finish).`

- [ ] **Step 3: `commands.md`**

In the `config set` paragraph, change `Models, efforts, usage limits, labels, tokens and repos are not settable with it.` to `Models, efforts, usage limits, `comment_fix_max`, labels, tokens and repos are not settable with it.`

- [ ] **Step 4: `CHANGELOG.md`**

Under `## Unreleased`, after the bullet that starts `- A rerun reads your comments:`, add:

```markdown
- A comment on a done issue whose PR is still open starts a fix session on the same branch: from
  you or a `steer_authors` id, five minutes after the last one, within 7 days of the run's last
  session. It pushes to the open PR without a new review and comments on the issue; owner items
  go in that comment, and `flocc done --nothing-to-do` ends it without a push. `comment_fix_max`
  (default 3, not settable) caps them per run; later comments wait for a Todo rerun. Comments
  posted during a review or a rebase are no longer typed into those sessions; they wait for the
  run to finish. Comments from before the upgrade never start one.
```

- [ ] **Step 5: Run the full check**

Run: `PATH="$(dirname $(uvx --from shellcheck-py which shellcheck)):$PATH" just check`
Expected: PASS. Fix any ruff, pyrefly or format finding in the files this plan touched, then run it again.

- [ ] **Step 6: Commit**

```bash
git add docs/book CHANGELOG.md
git commit -s -m "docs: comment fixes in the book and the changelog"
```
