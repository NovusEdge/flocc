# Cardinal autonomy 1: structured outcomes — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Agents report owner questions and "nothing to do" through `flocc done` flags, the supervisor acts on those fields instead of a heading in the summary, and an executor that finishes with no commits blocks as `no-changes` instead of failing publish three times.

**Architecture:** `flocc done` writes `questions` and `outcome` into `done.json` and refuses the old heading. `supervise` reads them and passes them to `_finish_phase`, which blocks with a new `needs-owner` event kind, ends a no-op run done without a PR, or blocks `no-changes` before publish. The regex parser in `supervisor.py` goes away, and every skill, prompt and hook message names the flags.

**Tech Stack:** Python 3, argparse, pytest via `uv run pytest`.

**Spec:** `docs/superpowers/specs/2026-11-04-cardinal-autonomy-design.md`, section 1.

## Global Constraints

- Each question is one `--needs-owner` flag; `--nothing-to-do` is for the tester and executor only, replaces `--summary`, and cannot be combined with `--needs-owner`.
- A `--summary` with a line matching `^[\s#*-]*needs the owner` (case-insensitive, multiline) is refused; the error names `--needs-owner`.
- `done.json` keys: `summary`, `at`, `questions` (list of str), `outcome` (`"work"` or `"nothing"`).
- `questions.json` in the claim's state directory holds the questions as a JSON list; part 2's `resume` reads it.
- Event kind `needs-owner`, detail `"<n> question"` / `"<n> questions"`. Block cause `no-changes`.
- The reviewer keeps writing owner items under `## Important` in `review.md`; its skill does not change.
- Commits: `git commit -s`, no `Co-Authored-By` line, subject ≤ 72 characters.
- While iterating run only the touched tests: `uv run pytest tests/<file>::<name> -q`. Run `just check` once at the end, before push.
- Comments carry only facts the code cannot show; match the file's density.

## Decisions taken in this plan

- **No commits since the phase started** is detected for the executor only, as `rt.git_head(claim) == (claim.tester_sha or claim.base_sha)`: the branch holds nothing beyond the tester's commits or the base. Those are the two values the claim already records for the start of `impl`. A check fixer always runs on a branch with commits, so it never reaches the empty-PR failure; a fixer that commits nothing fails the check the same way and already blocks as `check-failed`. Recording a head at every fix launch would add a branch fetch per launch for no new behaviour.
- The check runs only when no check is in flight (`check_running:KEY` unset), because `done.json` stays while a check runs and `git_head` fetches the branch.
- The `no-changes` action is owner-facing for now ("comment what to do and move it back to Todo, or close the issue"). Part 2 adds `resume` and changes the text.
- `run-summary` needs no change: the numbered questions are in `blocked.md`, which it already prints under "Why it stopped".

## Review Focus

1. A `done.json` written by an old session (no `questions`/`outcome` keys) or with a malformed `questions` value (a string, a list with non-strings) must behave as plain work, not crash or iterate characters. Task 3 pins it.
2. An executor resumed on a kept branch that already holds its earlier commits, and finishes with no new ones, must still publish (its earlier commits are the work). Task 4 pins it.
3. `--nothing-to-do` from a fixer or reviewer, or combined with `--summary` or `--needs-owner`, must be refused without writing `done.json`. Task 2 pins it.
4. A whitespace-only `--needs-owner ' '` must be refused, not recorded as an empty question that blocks the run with nothing to ask. Task 2 pins it.
5. Publish retries on a pending check must not re-run the empty-branch fetch each tick. Task 4 pins it.

---

### Task 1: `needs-owner` event kind, a Notice detail override, and triage coverage

**Files:**
- Modify: `src/flocc/events.py:8-10`
- Modify: `src/flocc/notice.py` (the `Notice` dataclass and `Notifier.send`)
- Modify: `deploy/cardinal/plugin/skills/triage/SKILL.md` (add a `needs-owner` rule after the `ended` blocked rule; extend the `done` rule)
- Test: `tests/test_notice.py`, `tests/test_events.py`, `tests/test_cardinal_plugin.py:156` (existing test now covers the new kind)

**Interfaces:**
- Produces: `events.KINDS` contains `"needs-owner"`. `Notice(..., detail: str = "")`; when set, `Notifier.send` emits it as the event detail in place of `f"{cause}: {summary}"`.

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_notice.py`:

```python
def test_a_notice_detail_replaces_the_cause_and_summary_in_the_event(tmp_path):
    con = ledger.connect(tmp_path / "l.sqlite")
    Notifier(con, FakeLinear(), lambda: 5.0).send(
        Notice(
            "NF-1:needs-owner",
            "The executor needs you:\n1. Which currency?",
            issue_key="NF-1",
            event="needs-owner",
            detail="1 question",
        ),
        ISSUE,
    )
    [e] = events.unacked(con)
    assert (e.kind, e.detail) == ("needs-owner", "1 question")
```

Append to `tests/test_events.py`:

```python
def test_needs_owner_is_an_event_kind(con):
    assert "needs-owner" in events.KINDS
    events.emit(con, "needs-owner", 1.0, issue_key="NF-1", detail="2 questions")
    [e] = events.unacked(con)
    assert e.kind == "needs-owner"
```

- [ ] **Step 2: Run them to verify they fail**

Run: `uv run pytest tests/test_notice.py::test_a_notice_detail_replaces_the_cause_and_summary_in_the_event tests/test_events.py::test_needs_owner_is_an_event_kind -q`
Expected: FAIL — `Notice.__init__() got an unexpected keyword argument 'detail'` and `assert 'needs-owner' in frozenset(...)`.

- [ ] **Step 3: Implement**

In `src/flocc/events.py` replace the `KINDS` literal:

```python
KINDS = frozenset(
    {
        "ended",
        "cap",
        "needs-owner",
        "base-broken",
        "bad-sequence",
        "service-error",
        "daily",
        "host-pressure",
    }
)
```

In `src/flocc/notice.py`, add the field after `event` and use it in `send`:

```python
    # One of events.KINDS when cardinal must hear of it; "" for a comment or digest line only.
    event: str = ""
    # The event's detail when cause and summary are not what cardinal needs, e.g. a count.
    detail: str = ""
```

```python
        if notice.event:
            cause = notice.cause.rsplit(":", 1)[-1]
            events.emit(
                self.con,
                notice.event,
                self.now(),
                issue_key=notice.issue_key,
                detail=notice.detail or f"{cause}: {notice.summary}",
            )
```

In `deploy/cardinal/plugin/skills/triage/SKILL.md`, change the first bullet and add a bullet after the `ended`, blocked or timed out bullet (before the amended-tests paragraph's following `- A running session` bullet):

```markdown
- `ended`, detail starting `done`: tell the owner in one line: the issue, the PR link, and the
  review's verdict if the summary has one. When the detail says `nothing to do`, give its reason
  instead of a PR link.
```

```markdown
- `needs-owner`: an agent stopped with questions only the owner can answer. Read the run summary
  and tell the owner the issue and the numbered questions as written, nothing more. Start no
  fixer.
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_notice.py tests/test_events.py tests/test_cardinal_plugin.py -q`
Expected: PASS (the triage test at `test_cardinal_plugin.py:156` passes because the skill names `` `needs-owner` ``).

- [ ] **Step 5: Commit**

```bash
git add src/flocc/events.py src/flocc/notice.py deploy/cardinal/plugin/skills/triage/SKILL.md tests/test_notice.py tests/test_events.py
git commit -s -m "feat(events): needs-owner event kind with a count as its detail"
```

---

### Task 2: `flocc done --needs-owner` and `--nothing-to-do`

**Files:**
- Modify: `src/flocc/cli.py:205-209` (`STOP_REASON`), `:269-284` (`cmd_done`), `:969-970` (the `done` parser)
- Test: `tests/test_cli.py` (after `test_done_elsewhere_needs_a_summary`, `:224`; and `:328` in `test_stop_refuses_once_then_marks_the_session_idle`)

**Interfaces:**
- Produces: `done.json` = `{"summary": str, "at": float, "questions": list[str], "outcome": "work" | "nothing"}`. For `--nothing-to-do WHY`, `summary` is `WHY`.

- [ ] **Step 1: Write the failing tests**

Add to `tests/test_cli.py` after `test_done_elsewhere_needs_a_summary`:

```python
def test_done_records_each_question_for_the_owner(tmp_path, monkeypatch):
    session_env(monkeypatch, tmp_path, "impl")
    args = ["done", "--summary", "Did half.", "--needs-owner", "Which currency?"]
    assert main([*args, "--needs-owner", "Which region?"]) == 0
    data = json.loads((tmp_path / "done.json").read_text())
    assert data["questions"] == ["Which currency?", "Which region?"]
    assert data["outcome"] == "work" and data["summary"] == "Did half."


def test_done_refuses_the_old_owner_heading_and_names_the_flag(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    assert main(["done", "--summary", "Did half.\n\n## Needs the owner\n- which currency"]) == 2
    assert "--needs-owner" in capsys.readouterr().err
    assert not (tmp_path / "done.json").exists()


def test_done_refuses_an_empty_question(tmp_path, monkeypatch, capsys):
    session_env(monkeypatch, tmp_path, "impl")
    assert main(["done", "--summary", "Did it.", "--needs-owner", "  "]) == 2
    assert not (tmp_path / "done.json").exists()


@pytest.mark.parametrize("phase", ["test", "impl"])
def test_nothing_to_do_ends_with_its_reason_as_the_summary(tmp_path, monkeypatch, phase):
    session_env(monkeypatch, tmp_path, phase)
    assert main(["done", "--nothing-to-do", "Already fixed by NF-9."]) == 0
    data = json.loads((tmp_path / "done.json").read_text())
    assert data == {**data, "summary": "Already fixed by NF-9.", "outcome": "nothing"}
    assert data["questions"] == []


@pytest.mark.parametrize(
    "phase, extra",
    [
        ("fix", []),
        ("review", []),
        ("impl", ["--summary", "x"]),
        ("impl", ["--needs-owner", "q?"]),
        ("impl", []),
    ],
)
def test_nothing_to_do_is_refused_outside_its_use(tmp_path, monkeypatch, phase, extra):
    session_env(monkeypatch, tmp_path, phase)
    (tmp_path / "review.md").write_text("Verdict: approve\n")
    why = [] if phase == "impl" and not extra else ["Nothing."]
    args = ["done", "--nothing-to-do", *(why or ["  "]), *extra]
    assert main(args) == 2
    assert not (tmp_path / "done.json").exists()


def test_the_reviewer_cannot_pass_owner_questions(tmp_path, monkeypatch):
    session_env(monkeypatch, tmp_path, "review")
    (tmp_path / "review.md").write_text("Verdict: approve\n")
    assert main(["done", "--needs-owner", "q?"]) == 2
```

In `test_stop_refuses_once_then_marks_the_session_idle` (`:328`) change the assertion line to:

```python
    assert "--needs-owner" in refusal["reason"] and not (tmp_path / "idle").exists()
```

- [ ] **Step 2: Run them to verify they fail**

Run: `uv run pytest tests/test_cli.py -q -k "done or stop_refuses"`
Expected: FAIL — `unrecognized arguments: --needs-owner` (argparse exits 2 for some, but the json and refusal assertions fail), and the stop test fails on `--needs-owner`.

- [ ] **Step 3: Implement**

In `src/flocc/cli.py` replace `STOP_REASON`:

```python
STOP_REASON = (
    "flocc done has not run, so this run is not finished. Finish the work and run flocc done;"
    " pass each question only the owner can answer as its own --needs-owner '<question>'."
    " Nobody answers questions in this session."
)
```

Add next to `_KEY`:

```python
# The heading agents used before --needs-owner; a summary still using it would hide the question.
_OWNER_LINE = re.compile(r"^[\s#*-]*needs the owner", re.IGNORECASE | re.MULTILINE)
```

Replace `cmd_done`:

```python
def _done_refused(why: str) -> int:
    print(f"done: {why}", file=sys.stderr)
    return 2


def cmd_done(args) -> int:
    state = _state_dir()
    phase = os.environ.get("FLOCC_PHASE")
    questions = [q.strip() for q in args.needs_owner]
    nothing = args.nothing_to_do
    if not all(questions):
        return _done_refused("a --needs-owner question is empty")
    if phase == "review":
        if questions or nothing is not None:
            return _done_refused("the reviewer puts owner items in review.md under ## Important")
        review = state / "review.md"
        if not review.exists() or not review.read_text().strip():
            print(f"done: write {review} first; the service posts it", file=sys.stderr)
            return 1
        summary, outcome = "review", "work"
    elif nothing is not None:
        if phase not in ("test", "impl"):
            return _done_refused("--nothing-to-do is for the tester and the executor")
        if questions or args.summary is not None:
            return _done_refused("--nothing-to-do stands alone; its reason is the summary")
        if not nothing.strip():
            return _done_refused("--nothing-to-do needs the reason")
        summary, outcome = nothing.strip(), "nothing"
    elif not (args.summary or "").strip():
        print("done: pass --summary '<what you did, for the PR body>'", file=sys.stderr)
        return 2
    elif _OWNER_LINE.search(args.summary):
        return _done_refused(
            "pass each question for the owner as its own --needs-owner '<question>',"
            " not under a Needs the owner line in --summary"
        )
    else:
        summary, outcome = args.summary, "work"
    record = {"summary": summary, "at": time.time(), "questions": questions, "outcome": outcome}
    (state / "done.json").write_text(json.dumps(record))
    print("marked done; the service takes over on its next run")
    return 0
```

Replace the `done` parser lines (`:969-970`):

```python
    done = add("done", "Mark this step done (the reviewer's review.md, or --summary)")
    done.add_argument("--summary", default=None, help="what was done, for the PR body")
    done.add_argument(
        "--needs-owner",
        action="append",
        default=[],
        metavar="QUESTION",
        help="a question only the owner can answer; repeat per question; the run ends blocked",
    )
    done.add_argument(
        "--nothing-to-do",
        default=None,
        metavar="WHY",
        help="tester or executor: the work is not needed; ends the run without a PR",
    )
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_cli.py -q -k "done or stop_refuses"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/cli.py tests/test_cli.py
git commit -s -m "feat(cli): flocc done takes --needs-owner and --nothing-to-do"
```

---

### Task 3: The supervisor blocks on recorded questions

**Files:**
- Modify: `src/flocc/supervisor.py`: constants near `:41-46` (add `QUESTIONS`), remove `_OWNER_HEADING`/`_NOTHING` (`:73-75`), `_block` (`:151-160`), `_finish_phase` (`:426-478`), `supervise` (`:799-820`)
- Modify: `src/flocc/poller.py:397-399` (a fresh claim deletes `questions.json`)
- Test: `tests/test_supervisor.py` — `done` helper (`:60`), tests `:344-420`, `:992-1025`; `tests/test_poller.py` (fresh-claim cleanup)

**Interfaces:**
- Consumes: `done.json` keys from Task 2; `Notice.detail` and the `needs-owner` kind from Task 1.
- Produces: `supervisor.QUESTIONS = "questions.json"`; `_finish_phase(rt, claim, summary: str, questions: list[str], outcome: str) -> None`; `_block(rt, claim, cause, summary, action, kind="ended", detail="")`; `_owner_list(questions: list[str]) -> str` returning `"1. q\n2. q"`.

- [ ] **Step 1: Write the failing tests**

In `tests/test_supervisor.py` replace the `done` helper:

```python
def done(rt, summary="ok", questions=(), outcome="work"):
    (state_dir(rt.cfg, "NF-1") / "done.json").write_text(
        json.dumps({"summary": summary, "questions": list(questions), "outcome": outcome})
    )
```

Replace `test_fix_round_ends_in_a_second_review_posted_again` (`:344`) line 349 with
`done(rt, "Fixed a.", ["run AC-8 against the real server."])` and its last assertion (`:362`) with:

```python
    assert "Needs the owner:\n1. run AC-8 against the real server." in last
```

Replace `OWNER`, `after_fix` and the owner-item tests (`:365-418`):

```python
QUESTION = ["run AC-8 against the real server."]


def after_fix(rt, questions=QUESTION, summary="Fixed a."):
    review_session(rt, CHANGES)
    done(rt, "review")
    supervise(rt, current(rt))
    rt.sessions.alive_names.add("NF-1")
    done(rt, summary, questions)
    supervise(rt, current(rt))
    return state_dir(rt.cfg, "NF-1")


def test_owner_items_reach_the_comment_of_an_approving_second_review(rt):
    d = after_fix(rt)
    (d / "review.md").write_text("Verdict: approve\n")
    done(rt, "review")
    supervise(rt, current(rt))
    last = rt.linear.comments[-1]
    assert last.startswith("Review: approve — https://pr/1")
    assert "Needs the owner:\n1. run AC-8 against the real server." in last
```

Keep `test_owner_items_reach_the_comment_when_the_second_review_is_skipped` and `..._dies` as they are except their final assertion, which becomes:

```python
    assert "1. run AC-8 against the real server." in rt.linear.comments[-1]
```

Replace `test_a_sentence_mentioning_the_owner_is_not_an_owner_item` and `test_a_bulleted_owner_heading_is_an_owner_item` with:

```python
def test_a_summary_without_questions_leaves_no_owner_items(rt):
    d = after_fix(rt, questions=[], summary="Fixed a. Nothing here needs the owner today.")
    assert not (d / "owner-items.md").exists()
```

Replace `test_needs_the_owner_ends_the_run_blocked`, `test_a_none_line_under_needs_the_owner_does_not_block` and `test_a_review_fixer_s_owner_items_do_not_block` (`:992-1025`) with:

```python
@pytest.mark.parametrize("phase, alive", [("impl", "NF-1"), ("test", "NF-1-test"), ("fix", "NF-1")])
def test_questions_end_the_run_blocked_and_are_kept(rt, phase, alive):
    add(rt, phase=phase)
    rt.sessions.alive_names.add(alive)
    done(rt, "Did half.", ["Which currency is the default?", "Which region?"])
    published = []
    rt.publish_fn = lambda *a, **kw: published.append(a) or PublishResult("opened", url="u")
    supervise(rt, current(rt))
    assert current(rt).state == "blocked" and not published
    (notice,) = rt.notifier.sent
    assert notice.cause == "NF-1:needs-owner" and notice.event == "needs-owner"
    assert notice.detail == "2 questions"
    assert "1. Which currency is the default?\n2. Which region?" in notice.summary
    d = state_dir(rt.cfg, "NF-1")
    assert json.loads((d / "questions.json").read_text())[1] == "Which region?"


def test_one_question_is_counted_singular(rt):
    add(rt, phase="impl")
    done(rt, "Did half.", ["Which currency?"])
    supervise(rt, current(rt))
    assert rt.notifier.sent[0].detail == "1 question"


@pytest.mark.parametrize(
    "raw",
    [
        {"summary": "Done."},
        {"summary": "Done.", "questions": "Which currency?"},
        {"summary": "Done.", "questions": [3, None, " "]},
    ],
)
def test_a_done_file_without_usable_questions_is_plain_work(rt, raw):
    add(rt, phase="impl")
    rt.sessions.alive_names.add("NF-1")
    published = []
    rt.publish_fn = lambda *a, **kw: published.append(a) or PublishResult("opened", url="u")
    (state_dir(rt.cfg, "NF-1") / "done.json").write_text(json.dumps(raw))
    supervise(rt, current(rt))
    assert current(rt).state != "blocked" and published


def test_a_review_fixer_s_questions_do_not_block(rt):
    add(rt, phase="fix")
    ledger.meta_set(rt.con, "review_fix:NF-1", "1")
    rt.sessions.alive_names.add("NF-1")
    rt.publish_fn = lambda *a, **kw: PublishResult("opened", url="https://pr/1", existing=True)
    done(rt, "Fixed two.", ["run the migration by hand."])
    supervise(rt, current(rt))
    assert current(rt).state == "running" and current(rt).phase == "review"
    items = (state_dir(rt.cfg, "NF-1") / "owner-items.md").read_text()
    assert items == "Needs the owner:\n1. run the migration by hand.\n"
```

In `tests/test_poller.py`, add after `test_rerun_ignores_files_from_the_previous_claim` (`:193`):

```python
def test_a_fresh_claim_drops_the_previous_run_s_questions(rt):
    d = state_dir(rt.cfg, "NF-9")
    d.mkdir()
    (d / "questions.json").write_text(json.dumps(["Which currency?"]))
    rt.linear.ready = [issue()]
    poll(rt)
    assert not (d / "questions.json").exists()
```

- [ ] **Step 2: Run them to verify they fail**

Run: `uv run pytest tests/test_supervisor.py tests/test_poller.py -q -k "owner or question or fix_round or done_file or review_fixer"`
Expected: FAIL — `notice.event == "ended"`, missing `questions.json`, `owner-items.md` absent.

- [ ] **Step 3: Implement**

In `src/flocc/supervisor.py`:

Add after `BLOCKED = "blocked.md"`:

```python
# The questions of a needs-owner block, for cardinal's resume to put back in front of the agent.
QUESTIONS = "questions.json"
```

Delete `_OWNER_HEADING`, its comment line and `_NOTHING` (`:73-75`); drop the `re` import if `_VERDICT` was its only other user (it is not: keep `re`).

Replace `_block`:

```python
def _block(
    rt, claim: Claim, cause: str, summary: str, action: str, kind: str = "ended", detail: str = ""
) -> None:
    rt.sessions.kill(session_name(claim.issue_key, claim.phase))
    key = claim.issue_key
    ledger.meta_set(rt.con, f"{CHECK_RUNNING}:{key}", "")
    notice = Notice(f"{key}:{cause}", summary, action, issue_key=key, event=kind, detail=detail)
    d = state_dir(rt.cfg, key)
    d.mkdir(parents=True, exist_ok=True)
    (d / BLOCKED).write_text(f"{cause}\n\n{notice.text()}\n")
    rt.notifier.send(notice, rt.linear.issue(key))
    ledger.update_claim(rt.con, key, state="blocked")
```

Add before `_finish_phase`:

```python
def _owner_list(questions: list[str]) -> str:
    return "\n".join(f"{n}. {q}" for n, q in enumerate(questions, 1))


def _ask_owner(rt, claim: Claim, questions: list[str]) -> None:
    d = state_dir(rt.cfg, claim.issue_key)
    d.mkdir(parents=True, exist_ok=True)
    (d / QUESTIONS).write_text(json.dumps(questions))
    count = f"{len(questions)} question{'' if len(questions) == 1 else 's'}"
    _block(
        rt,
        claim,
        "needs-owner",
        f"The {ARCHETYPE[claim.phase]} needs you:\n{_owner_list(questions)}"[:3000],
        "Reply on this issue, then move it back to Todo; the rerun continues on the kept branch",
        kind="needs-owner",
        detail=count,
    )
```

In `_finish_phase`, change the signature and replace lines `:439-456` (from `found = _OWNER_HEADING...` through the `return` after the block) with:

```python
def _finish_phase(
    rt, claim: Claim, summary: str, questions: list[str] = (), outcome: str = "work"
) -> None:
```

```python
    # A review fixer's owner items ride to the final comment instead: the PR is open and a
    # second review follows.
    if questions and not review_fix and not rebasing:
        _ask_owner(rt, claim, list(questions))
        return
```

and replace `:476-478`:

```python
    if claim.phase == "fix" and review_fix and not rebasing and questions:
        # Kept apart from later check-fix summaries, which would overwrite it.
        items = f"Needs the owner:\n{_owner_list(list(questions))}\n"
        (state_dir(rt.cfg, key) / OWNER_ITEMS).write_text(items)
```

(`outcome` is used in Task 4; leave it unused here.)

In `supervise`, replace the parse inside `try:` and the call:

```python
            data = json.loads(raw_done)
            summary = str(data.get("summary", ""))
            raw = data.get("questions")
            questions = (
                [q.strip() for q in raw if isinstance(q, str) and q.strip()]
                if isinstance(raw, list)
                else []
            )
            outcome = str(data.get("outcome", "work"))
```

```python
            _finish_phase(rt, claim, summary, questions, outcome)
```

In `src/flocc/poller.py` after `(d / BLOCKED).unlink(missing_ok=True)` (`:399`):

```python
    (d / QUESTIONS).unlink(missing_ok=True)
```

and extend its import from `flocc.supervisor` (`:23`) to `BLOCKED, LAST_SUMMARY, QUESTIONS, launch`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_supervisor.py tests/test_poller.py -q`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/supervisor.py src/flocc/poller.py tests/test_supervisor.py tests/test_poller.py
git commit -s -m "feat(supervisor): block on recorded owner questions, not a summary heading"
```

---

### Task 4: Nothing to do, and `no-changes` for an empty executor

**Files:**
- Modify: `src/flocc/supervisor.py` — `_end_done` (`:163-172`), `_finish_phase` (after the questions branch, before `if claim.phase == "test":`, and before the publish)
- Test: `tests/test_supervisor.py` (append near the questions tests)

**Interfaces:**
- Consumes: `outcome` from Task 3's `_finish_phase` signature.
- Produces: `_end_done(rt, claim, *, emit=True, detail: str | None = None, **values)`; block cause `no-changes`.

- [ ] **Step 1: Write the failing tests**

Append to `tests/test_supervisor.py`:

```python
@pytest.mark.parametrize("phase, alive", [("impl", "NF-1"), ("test", "NF-1-test")])
def test_nothing_to_do_ends_done_without_a_pr(rt, phase, alive):
    add(rt, phase=phase)
    rt.sessions.alive_names.add(alive)
    published = []
    rt.publish_fn = lambda *a, **kw: published.append(a) or PublishResult("opened", url="u")
    done(rt, "Already fixed by NF-9.", outcome="nothing")
    supervise(rt, current(rt))
    assert current(rt).state == "done" and not published
    assert rt.linear.comments == ["Nothing to do: Already fixed by NF-9."]
    [e] = events.unacked(rt.con)
    assert (e.kind, e.detail) == ("ended", "done, nothing to do: Already fixed by NF-9.")


def test_an_executor_with_no_commits_blocks_as_no_changes(rt):
    add(rt, phase="impl", tester_sha="sha-tests")
    rt.sessions.alive_names.add("NF-1")
    published = []
    rt.publish_fn = lambda *a, **kw: published.append(a) or PublishResult("opened", url="u")
    done(rt, "Implemented.")
    supervise(rt, current(rt))
    assert current(rt).state == "blocked" and not published
    assert rt.notifier.sent[0].cause == "NF-1:no-changes"


def test_an_executor_on_a_branch_with_earlier_commits_still_publishes(rt):
    add(rt, phase="impl", tester_sha="sha-tests")
    rt.git_head = lambda claim: "sha-earlier-impl"
    rt.sessions.alive_names.add("NF-1")
    done(rt, "Nothing new; the earlier run finished it.")
    supervise(rt, current(rt))
    assert current(rt).state != "blocked"


def test_a_pending_check_does_not_fetch_the_branch_again(rt):
    add(rt, phase="impl")
    ledger.meta_set(rt.con, "check_running:NF-1", "1")
    heads = []
    rt.git_head = lambda claim: heads.append(claim) or "sha-tests"
    rt.publish_fn = lambda *a, **kw: PublishResult("check_running")
    done(rt, "Implemented.")
    supervise(rt, current(rt))
    assert heads == []
```

- [ ] **Step 2: Run them to verify they fail**

Run: `uv run pytest tests/test_supervisor.py -q -k "nothing_to_do or no_changes or earlier_commits or pending_check"`
Expected: FAIL — the nothing-to-do run publishes; the empty executor publishes; the pending check calls `git_head`.

- [ ] **Step 3: Implement**

Replace `_end_done`:

```python
def _end_done(rt, claim: Claim, *, emit: bool = True, detail: str | None = None, **values) -> None:
    if emit:
        if detail is None:
            url = _pr_url(rt, claim)
            detail = f"done: PR {url}" if url != "unknown" else "done, no PR"
        # Before the claim update: a tick that dies between the two finishes the claim again on
        # the next tick, and a duplicate event is acked away, where a lost one is never seen.
        events.emit(
            rt.con, "ended", rt.now(), issue_key=claim.issue_key, repo=claim.repo, detail=detail
        )
    ledger.update_claim(rt.con, claim.issue_key, state="done", **values)
```

In `_finish_phase`, directly after the questions branch from Task 3:

```python
    if outcome == "nothing" and claim.phase in ("test", "impl"):
        _comment_or_digest(rt, claim, f"Nothing to do: {summary}")
        _end_done(rt, claim, detail=f"done, nothing to do: {summary}")
        return
```

and directly before `issue = rt.linear.issue(key)` (the publish preamble):

```python
    # gh refuses a PR for a branch with nothing past its base, which would read as a publish
    # failure. Skipped while a check runs: done.json stays and git_head fetches the branch.
    if (
        claim.phase == "impl"
        and not ledger.meta_get(rt.con, f"{CHECK_RUNNING}:{key}")
        and rt.git_head(claim) == (claim.tester_sha or claim.base_sha)
    ):
        _block(
            rt,
            claim,
            "no-changes",
            f"The {ARCHETYPE['impl']} finished with no commits.",
            "Comment what it should do and move the issue back to Todo, or close the issue",
        )
        return
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_supervisor.py -q`
Expected: PASS. If an existing impl test now blocks `no-changes`, it set `tester_sha` equal to the fake head (`"sha-tests"`); give that test a distinct `rt.git_head`, since its subject is not an empty branch.

- [ ] **Step 5: Commit**

```bash
git add src/flocc/supervisor.py tests/test_supervisor.py
git commit -s -m "feat(supervisor): end a no-op run done and block an empty executor"
```

---

### Task 5: Every skill, prompt and hook message names the flags

**Files:**
- Modify: `plugin/skills/tester/SKILL.md:13-16`, `plugin/skills/executor/SKILL.md:11-15,20-22,45`, `plugin/skills/fixer/SKILL.md:15-20`
- Modify: `src/flocc/context.py:10-14` (`_COMMANDS`), `src/flocc/control.py:25-32` (`_fix_prompt`), `src/flocc/supervisor.py:337-343` (review-fix prompt), `src/flocc/guard.py:5-13` (`KEEP_MOVING`, `NO_APPROVER`)
- Modify: `docs/book/architecture.md:47,64,68,79`, `docs/book/setup/running-a-host.md:90`, `CHANGELOG.md` (Unreleased)
- Test: `tests/test_plugin.py:95`, `tests/test_guard.py:17`, and a new prompt test in `tests/test_plugin.py`

**Interfaces:**
- Consumes: the flags from Task 2.

- [ ] **Step 1: Write the failing tests**

In `tests/test_plugin.py` `test_the_four_archetype_skills_end_the_same_way`, replace `assert "Needs the owner" in text and "flocc done" in text` with:

```python
        assert "flocc done" in text
        if name != "reviewer":
            assert "--needs-owner" in text and "line that starts with `Needs the owner`" not in text
```

Append to `tests/test_plugin.py`:

```python
def test_no_prompt_asks_for_the_owner_heading():
    from flocc import cli, context, control, guard, supervisor

    texts = [
        context._COMMANDS,
        control._fix_prompt(make("NF-1", state="blocked"), "note"),
        guard.KEEP_MOVING,
        guard.NO_APPROVER,
        cli.STOP_REASON,
        inspect.getsource(supervisor._start_review_fix),
    ]
    for text in texts:
        assert "--needs-owner" in text
        assert 'a line that starts with "Needs the owner"' not in text
```

Add `import inspect` and `from tests.test_ledger import make` at the top of `tests/test_plugin.py` if absent. `_start_review_fix` (`supervisor.py:332`) holds the review-fix prompt.

In `tests/test_guard.py:17` change `"Needs the owner" in reason` to `"--needs-owner" in reason`.

- [ ] **Step 2: Run them to verify they fail**

Run: `uv run pytest tests/test_plugin.py tests/test_guard.py -q`
Expected: FAIL on every text still naming the heading.

- [ ] **Step 3: Implement**

`plugin/skills/tester/SKILL.md`, replace the bullet at `:13-16`:

```markdown
- Where the spec leaves a choice open, take its default. Nobody answers questions in this
  session; if a criterion cannot be tested without the owner, pass the question as
  `--needs-owner '<question>'` to `flocc done`, one flag per question, and the run ends blocked.
  If the issue needs no tests because the behaviour already exists, run
  `flocc done --nothing-to-do '<why>'` instead.
```

`plugin/skills/executor/SKILL.md`, replace the bullet at `:11-15`:

```markdown
- Where the spec leaves a choice open, take its default or the smallest choice that meets the
  criteria. Nobody answers questions in this session. If you cannot go on without the owner, stop
  changing code and pass each question as `--needs-owner '<question>'` to `flocc done`; the run
  then ends blocked with your work kept on the branch. If the work turns out to be unneeded (it
  does not reproduce, or another change already did it), run `flocc done --nothing-to-do '<why>'`
  instead of committing nothing.
```

and in the bullet at `:20-22` replace ``list the conflict under `Needs the owner`.`` with ``pass the conflict as a `--needs-owner` question.``; change Last steps step 3 to:

```markdown
3. Run `flocc done --summary '<what changed and why, for the PR body>'`, with a
   `--needs-owner '<question>'` per open question.
```

`plugin/skills/fixer/SKILL.md`, replace the two bullets at `:15-20`:

```markdown
- When the fix answers a review, fix what is agent work. For a finding that needs the owner (a
  manual run, a product decision), change nothing and pass it as `--needs-owner '<item>'` to
  `flocc done`; it goes into the final review comment.
- When you were started on a failed check and cannot make it pass, pass what you could not fix as
  `--needs-owner '<what and why>'`; the run then ends blocked.
```

`src/flocc/context.py` `_COMMANDS`, replace the last sentence of the first paragraph and the `flocc done` command line:

```python
_COMMANDS = """## Execution only

The issue and the specs it names are the approved design. Do not brainstorm, write a plan for
approval, or ask for design sign-off: nobody is at this session to approve it. Where the spec
leaves a choice open, take its default; pass each question only the owner can answer to
`flocc done` as its own `--needs-owner '<question>'`.

## Commands

- `flocc context` to see this text again.
- `flocc done --summary '<one paragraph>' [--needs-owner '<question>' ...]` when this step is
  finished; the reviewer runs `flocc done` after writing review.md.
- `flocc commit -m '<message>' -- <paths>` to commit only those paths.
"""
```

`src/flocc/control.py` `_fix_prompt`:

```python
        f" Its note:\n\n{note}\n\nThe previous step's summary is in `flocc context`. Fix what"
        " the note asks. Pass what needs the owner to flocc done as --needs-owner '<question>',"
        " one per question. Then finish as usual."
```

`src/flocc/supervisor.py` review-fix prompt (`:339-342`):

```python
        "Fix what the review asks that is agent work. For what needs the owner (a manual run, a"
        " product decision, a question for the lead), make no change and pass it to `flocc done`"
        " as `--needs-owner '<item>'`, one per item. Then finish as usual."
```

`src/flocc/guard.py`:

```python
KEEP_MOVING = (
    "Nobody answers this session. Take the spec's default and carry on, or pass what needs the"
    " owner to flocc done as --needs-owner '<question>' and run it."
)
NO_APPROVER = (
    "Nobody is at this session to approve this call, so it was denied. Do it another way (for"
    " example the Read tool instead of cat, head or tail). If it truly needs the owner, pass it"
    " as --needs-owner '<what>' when you run flocc done, and carry on."
)
```

Docs: in `docs/book/architecture.md` lines 47, 64, 68 and 79 and `docs/book/setup/running-a-host.md:90`, replace each "under "Needs the owner" in its/your summary" wording with "as `--needs-owner` questions to `flocc done`", and line 68's second sentence with: "Questions passed with `--needs-owner` end the run blocked, except in the review fix round; `--nothing-to-do` ends a tester or executor run done without a PR; an executor that finishes with no commits blocks as `no-changes`."

`CHANGELOG.md`, add under `## Unreleased`:

```markdown
- Agents ask the owner with `flocc done --needs-owner '<question>'`, one flag per question, and
  cardinal gets a `needs-owner` event with the count. A summary that still uses a "Needs the
  owner" heading is refused. `flocc done --nothing-to-do '<why>'` ends a tester or executor run
  done without a PR, and an executor that finishes with no commits now blocks as `no-changes`
  instead of failing publish three times with "Fix the push or the gh token".
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `uv run pytest tests/test_plugin.py tests/test_guard.py tests/test_control.py tests/test_context.py -q`
Expected: PASS.

- [ ] **Step 5: Run the full check once**

Run: `just check`
Expected: PASS. A failure in a file this plan did not touch that asserts "Needs the owner" text is a missed mention: change it to the flag wording above.

- [ ] **Step 6: Commit**

```bash
git add plugin/skills src/flocc/context.py src/flocc/control.py src/flocc/supervisor.py src/flocc/guard.py docs/book CHANGELOG.md tests/test_plugin.py tests/test_guard.py
git commit -s -m "docs(skills): agents ask the owner with flocc done --needs-owner"
```
