# Cardinal autonomy part 2: `resume` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace cardinal's `fix` verb with `resume`, which restarts a stopped claim once on its kept branch, in the phase and with the framing its stop cause calls for.

**Architecture:** `control.resume` reads the stop cause from the claim (`timed_out` state, else the first line of `blocked.md`), refuses causes outside a fixed set, and relaunches the claim's own phase, or the fix phase for a failed check, with cardinal's note in the prompt. A resume note for the tester or executor is kept in the state directory so a session restart keeps it. The gate maps the old `fix` line to `resume` for one release; each resume is logged in `meta` and listed in the daily digest with the claim's current state.

**Tech Stack:** Python 3, argparse, SQLite (`flocc.ledger`), pytest via `uv run pytest`.

**Spec:** `docs/superpowers/specs/2026-11-04-cardinal-autonomy-design.md`, section 2.

## Global Constraints

- Part 1 has merged: `flocc done --needs-owner` writes `questions` into `done.json`; the supervisor keeps them in `questions.json` (a JSON list of strings) in the claim's state directory, exported as `supervisor.QUESTIONS`; block causes include `no-changes`; event kind `needs-owner` exists. If part 1 named the constant differently, use its name.
- One resume per claim. Meta key `cardinal_resume:KEY`; a set `cardinal_fix:KEY` (written by the old verb) also counts.
- Allowed stops: `needs-owner`, `check-failed` with fix rounds left, `no-changes`, `idle`, `crash`, timed out. Everything else is refused, `tests-touched` included.
- `check-failed` relaunches the fix phase and counts a fix round; every other cause relaunches the claim's own phase and counts none.
- Note: non-empty, at most 2,000 characters (`NOTE_CHARS`).
- Kept refusals from `fix`: no claim, a state other than `blocked` or `timed_out`, a held issue, a missing worktree, `admit_rerun` saying no.
- Linear comment: `cardinal resumed the <archetype> after <cause>:\n\n<note>`.
- `release` stays hold-only; cardinal never starts a fresh claim or touches a done one.
- Tests: `uv run pytest -q tests/<file>.py` while iterating; `just test-py` once before push.
- Commits: `git commit -s`, conventional prefixes as in `git log`, no Co-Authored-By trailer.

## Review Focus

- A claim blocked before this deploy whose `blocked.md` is missing or empty: refused with "no recorded cause", never resumed as some default.
- A `check-failed` block at the cap (`fix_attempts >= check_fix_rounds`): refused, though the cause string equals the resumable same-failure block.
- A claim the old `fix` verb already marked (`cardinal_fix:KEY`): refused as already resumed.
- A resumed executor whose session dies before `flocc done`: the relaunch keeps cardinal's note in its prompt.
- A launch that fails: claim stays blocked, `blocked.md` stays, no cap mark, no log row, no comment.

---

### Task 1: `control.resume` and the kept resume note

**Files:**
- Modify: `src/flocc/control.py` (whole module body below the imports)
- Modify: `src/flocc/supervisor.py:42-46` (constant), `:208-212` (`_relaunch`), `:426-430` (`_finish_phase` head)
- Modify: `src/flocc/poller.py:386-399` (`_start` meta and file clearing)
- Test: `tests/test_control.py` (replace the `start_fix` tests), `tests/test_supervisor.py`, `tests/test_poller.py`

**Interfaces:**
- Consumes: `supervisor.QUESTIONS` (part 1), `supervisor.BLOCKED`, `FIX_CONTEXT`, `launch(rt, claim, phase, extra="")`, `sequence.ARCHETYPE`.
- Produces: `control.resume(rt, key: str, note: str) -> Claim`; `control.stop_cause(rt, claim: Claim) -> str`; `control.RESUMABLE: frozenset[str]`; `control.CARDINAL_RESUME = "cardinal_resume"`; `control.RESUME_LOG = "resume_log"` (meta rows `resume_log:<at:.6f>` holding JSON `{"at", "key", "cause", "phase"}`); `control.resumes(con, since: float) -> list[tuple[float, str, str, str]]`; `supervisor.RESUME_NOTE = "resume-note.md"`; `control.Refused` unchanged.

- [ ] **Step 1: Replace the tests in `tests/test_control.py`**

Keep the imports and the `rt` fixture; change the `flocc.control` import to `from flocc.control import Refused, resume` and add `import json`, `from flocc.supervisor import RESUME_NOTE`. Replace `blocked()` and every test below it with:

```python
def blocked(rt, tmp_path, cause="idle", **kw):
    wt = tmp_path / "wt" / "NF-1"
    wt.mkdir(parents=True, exist_ok=True)
    c = make("NF-1", state="blocked", worktree=str(wt))
    c.phase = "impl"
    for k, v in kw.items():
        setattr(c, k, v)
    ledger.insert_claim(rt.con, c)
    d = state_dir(rt.cfg, "NF-1")
    d.mkdir(parents=True, exist_ok=True)
    if cause is not None:
        (d / BLOCKED).write_text(f"{cause}\n\nwhy it stopped")
    return c


def test_resume_after_idle_relaunches_the_executor_with_the_note(rt, tmp_path):
    blocked(rt, tmp_path)
    claim = resume(rt, "NF-1", "Finish the migration in db/schema.py, then run flocc done.")
    assert (claim.state, claim.phase, claim.fix_attempts, claim.started_at) == (
        "running",
        "impl",
        0,
        100.0,
    )
    name, argv = rt.sessions.started[-1]
    assert name == "NF-1" and any("db/schema.py" in a for a in argv)
    d = state_dir(rt.cfg, "NF-1")
    assert "db/schema.py" in (d / RESUME_NOTE).read_text() and not (d / BLOCKED).exists()
    assert rt.linear.comments[-1].startswith("cardinal resumed the executor after idle:")
    assert ledger.meta_get(rt.con, "cardinal_resume:NF-1") == "1"


def test_resume_answers_the_questions_in_the_tester_s_own_phase(rt, tmp_path):
    blocked(rt, tmp_path, cause="needs-owner", phase="test")
    d = state_dir(rt.cfg, "NF-1")
    (d / "questions.json").write_text(json.dumps(["Which timezone do dates use?"]))
    resume(rt, "NF-1", "UTC everywhere; the spec says so under Dates.")
    name, argv = rt.sessions.started[-1]
    prompt = " ".join(argv)
    assert name == "NF-1-test"
    assert "1. Which timezone do dates use?" in prompt and "UTC everywhere" in prompt
    assert "answered the questions" in prompt


def test_resume_after_a_repeated_check_failure_starts_a_fix_round_with_the_check(rt, tmp_path):
    blocked(rt, tmp_path, cause="check-failed", phase="fix", fix_attempts=1)
    d = state_dir(rt.cfg, "NF-1")
    (d / FIX_CONTEXT).write_text("The check failed with:\n\nFAILED tests/test_a.py::test_x")
    claim = resume(rt, "NF-1", "test_x needs the fixture from conftest, not a new one.")
    assert (claim.phase, claim.fix_attempts) == ("fix", 2)
    context = (d / FIX_CONTEXT).read_text()
    assert context.index("needs the fixture") < context.index("FAILED tests/test_a.py::test_x")
    assert not (d / RESUME_NOTE).exists()


def test_resume_on_a_timed_out_run_says_so(rt, tmp_path):
    blocked(rt, tmp_path, cause=None, state="timed_out")
    resume(rt, "NF-1", "finish the migration")
    assert "after it timed out" in (state_dir(rt.cfg, "NF-1") / RESUME_NOTE).read_text()
    assert "after timed-out" in rt.linear.comments[-1]


@pytest.mark.parametrize(
    "cause", ["tests-touched", "branch-moved", "publish-failed", "rebase-conflict", "no-tests"]
)
def test_the_owner_s_stops_are_refused(rt, tmp_path, cause):
    blocked(rt, tmp_path, cause=cause)
    with pytest.raises(Refused, match=f"stopped with {cause}"):
        resume(rt, "NF-1", "note")
    assert rt.sessions.started == []


def test_a_block_with_no_recorded_cause_is_refused(rt, tmp_path):
    blocked(rt, tmp_path, cause=None)
    with pytest.raises(Refused, match="no recorded cause"):
        resume(rt, "NF-1", "note")


def test_a_check_failure_at_the_fix_cap_is_refused(rt, tmp_path):
    blocked(rt, tmp_path, cause="check-failed", phase="fix", fix_attempts=2)
    with pytest.raises(Refused, match="used 2 of 2 fix rounds"):
        resume(rt, "NF-1", "note")


def test_a_second_resume_on_the_same_run_is_refused(rt, tmp_path):
    blocked(rt, tmp_path)
    resume(rt, "NF-1", "first")
    ledger.update_claim(rt.con, "NF-1", state="blocked")
    (state_dir(rt.cfg, "NF-1") / BLOCKED).write_text("idle\n\nagain")
    with pytest.raises(Refused, match="already resumed"):
        resume(rt, "NF-1", "second")


def test_a_run_the_old_fix_verb_marked_is_still_capped(rt, tmp_path):
    blocked(rt, tmp_path)
    ledger.meta_set(rt.con, "cardinal_fix:NF-1", "1")
    with pytest.raises(Refused, match="already resumed"):
        resume(rt, "NF-1", "note")


@pytest.mark.parametrize(
    ("setup", "match"), [({"state": "running"}, "is running"), ({"state": "done"}, "is done")]
)
def test_resume_is_refused_outside_its_window(rt, tmp_path, setup, match):
    blocked(rt, tmp_path, **setup)
    with pytest.raises(Refused, match=match):
        resume(rt, "NF-1", "note")
    assert rt.sessions.started == []


def test_resume_is_refused_for_a_held_issue_and_without_a_note(rt, tmp_path):
    blocked(rt, tmp_path)
    with pytest.raises(Refused, match="pass a note"):
        resume(rt, "NF-1", "  ")
    with pytest.raises(Refused, match="keep it under 2000"):
        resume(rt, "NF-1", "x" * 2001)
    ledger.add_hold(rt.con, "NF-1", "wait", 1.0)
    with pytest.raises(Refused, match="is held"):
        resume(rt, "NF-1", "note")


def test_a_launch_that_fails_leaves_the_run_as_it_was(rt, tmp_path):
    blocked(rt, tmp_path)
    rt.cfg.agent_gh_token_file.unlink()
    with pytest.raises(LaunchError):
        resume(rt, "NF-1", "note")
    assert ledger.get_claim(rt.con, "NF-1").state == "blocked"  # type: ignore[union-attr]
    assert ledger.meta_get(rt.con, "cardinal_resume:NF-1") is None
    assert (state_dir(rt.cfg, "NF-1") / BLOCKED).exists()
    assert rt.linear.comments == [] and resumes(rt.con, 0.0) == []


def test_resume_is_refused_while_the_host_is_under_pressure(rt, tmp_path):
    blocked(rt, tmp_path)
    host.open_episode(rt.con, 1.0, "/tmp 93% (2.8/3.0 GiB)")
    with pytest.raises(Refused, match="not now: host under pressure"):
        resume(rt, "NF-1", "note")
    assert rt.sessions.started == []


def test_each_resume_is_logged(rt, tmp_path):
    blocked(rt, tmp_path, cause="needs-owner")
    resume(rt, "NF-1", "answer")
    assert resumes(rt.con, 0.0) == [(100.0, "NF-1", "needs-owner", "impl")]
```

Also import `resumes`: `from flocc.control import Refused, resume, resumes`.

- [ ] **Step 2: Add the restart and fresh-claim tests**

In `tests/test_supervisor.py`, import `RESUME_NOTE` from `flocc.supervisor` and add:

```python
def test_a_dead_session_s_restart_keeps_cardinal_s_resume_note(rt):
    add(rt, phase="impl")
    (state_dir(rt.cfg, "NF-1") / RESUME_NOTE).write_text("cardinal's note: finish db/schema.py")
    supervise(rt, current(rt))
    name, argv = rt.sessions.started[-1]
    assert name == "NF-1" and any("finish db/schema.py" in a for a in argv)


def test_a_finished_step_drops_the_resume_note(rt):
    add(rt, phase="impl", tester_sha="sha-tests")
    rt.sessions.alive_names.add("NF-1")
    (state_dir(rt.cfg, "NF-1") / RESUME_NOTE).write_text("note")
    done(rt)
    supervise(rt, current(rt))
    assert not (state_dir(rt.cfg, "NF-1") / RESUME_NOTE).exists()
```

In `tests/test_poller.py`, extend `kept_keys` in `test_a_fresh_claim_forgets_the_last_run_s_rebase_and_base_hold` to `("rebase", "rebase_tried", "base_held", "base_released", "cardinal_fix", "cardinal_resume")`.

- [ ] **Step 3: Run the tests to see them fail**

Run: `uv run pytest -q tests/test_control.py tests/test_supervisor.py tests/test_poller.py`
Expected: FAIL: `ImportError: cannot import name 'resume'` and `RESUME_NOTE`.

- [ ] **Step 4: Add the note constant and keep the note across restarts in `supervisor.py`**

After `HOST_WAIT = "host_wait"`:

```python
# cardinal's resume note for a tester or executor; a fixer's rides in FIX_CONTEXT instead. Kept
# so a restart of the session gets it again, dropped when the step finishes.
RESUME_NOTE = "resume-note.md"
```

Replace `_relaunch`:

```python
def _relaunch(rt, claim: Claim) -> None:
    # The check output reaches a fix session only through its prompt; a restart needs it again.
    d = state_dir(rt.cfg, claim.issue_key)
    path = d / (FIX_CONTEXT if claim.phase == "fix" else RESUME_NOTE)
    extra = path.read_text() if path.exists() else ""
    launch(rt, claim, claim.phase, extra=f"{_RESTART}\n\n{extra}".strip())
```

At the top of `_finish_phase`, after `key = claim.issue_key`:

```python
    (state_dir(rt.cfg, key) / RESUME_NOTE).unlink(missing_ok=True)
```

- [ ] **Step 5: Clear the cap and the note on a fresh claim in `poller.py`**

In `_start`, add `"cardinal_resume",` to the tuple after `"cardinal_fix",`, and after `(d / BLOCKED).unlink(missing_ok=True)` add `(d / RESUME_NOTE).unlink(missing_ok=True)`, importing `RESUME_NOTE` from `flocc.supervisor` where `BLOCKED` is imported.

- [ ] **Step 6: Write `resume` in `control.py`**

Add `import json` to the stdlib imports at the top. Then replace the module from its
`from flocc.supervisor import ...` line to the end with:

```python
from flocc.supervisor import BLOCKED, FIX_CONTEXT, QUESTIONS, RESUME_NOTE, launch
from flocc.usage import latest_usage

# One cardinal resume per claim; poller._start clears both keys with the claim's other per-run
# meta. cardinal_fix is what the fix verb set before resume replaced it.
CARDINAL_RESUME = "cardinal_resume"
_CAPS = (CARDINAL_RESUME, "cardinal_fix")
RESUME_LOG = "resume_log"
NOTE_CHARS = 2000
# The stops a note can redirect. tests-touched guards the tester's work against the executor,
# the publish, branch and rebase stops need the host or the owner, and no-tests needs criteria.
RESUMABLE = frozenset({"needs-owner", "check-failed", "no-changes", "idle", "crash", "timed-out"})


class Refused(Exception):
    pass


def stop_cause(rt, claim: Claim) -> str:
    """Why the claim stopped: timed-out, or the cause _block wrote on blocked.md's first line."""
    if claim.state == "timed_out":
        return "timed-out"
    path = state_dir(rt.cfg, claim.issue_key) / BLOCKED
    return path.read_text().split("\n", 1)[0].strip() if path.exists() else ""


def _questions(rt, key: str) -> list[str]:
    path = state_dir(rt.cfg, key) / QUESTIONS
    try:
        asked = json.loads(path.read_text())
    except (OSError, ValueError):
        return []
    return [str(q) for q in asked] if isinstance(asked, list) else []


def _resume_prompt(rt, claim: Claim, cause: str, note: str) -> str:
    if cause == "needs-owner":
        asked = _questions(rt, claim.issue_key)
        listed = "\n".join(f"{n}. {q}" for n, q in enumerate(asked, 1)) or "(not kept)"
        head = (
            "cardinal, the owner's supervisor, answered the questions this step stopped on."
            f"\n\nThe questions:\n\n{listed}\n\nIts answer:\n\n{note}"
        )
    else:
        stopped = "timed out" if cause == "timed-out" else f"stopped ({cause})"
        head = (
            f"cardinal, the owner's supervisor, resumed this step after it {stopped}."
            f" Its note:\n\n{note}"
        )
    return (
        f"{head}\n\nThe previous step's summary is in `flocc context`; the branch holds what was"
        " committed. For anything that still needs the owner, use flocc done --needs-owner."
        " Then finish as usual."
    )


def admit_rerun(rt, claim: Claim):
    """Admission for another session on a finished claim, without its dependencies: they
    finished before the PR opened."""
    settings = replace(settings_from_json(claim.settings_json), depends_on=())
    return admit(
        claim.issue_key,
        claim.repo,
        settings,
        ledger.live_claims(rt.con),
        latest_usage(rt.cfg.agent_state_root, rt.now()),
        {},
        rt.cfg,
        rt.now(),
        rt.free_disk_gb(rt.cfg.worktree_root),
        ledger.held_keys(rt.con),
        host.pressure_detail(rt.con),
    )


def resume(rt, key: str, note: str) -> Claim:
    note = note.strip()
    if not note:
        raise Refused("pass a note saying what the next attempt does")
    if len(note) > NOTE_CHARS:
        raise Refused(f"the note is {len(note)} characters; keep it under {NOTE_CHARS}")
    claim = ledger.get_claim(rt.con, key)
    if claim is None:
        raise Refused(f"no claim for {key}")
    if claim.state not in ("blocked", "timed_out"):
        raise Refused(f"{key} is {claim.state}; cardinal resumes only a blocked or timed-out run")
    cause = stop_cause(rt, claim)
    if cause not in RESUMABLE:
        raise Refused(f"{key} stopped with {cause or 'no recorded cause'}; that stop is the owner's")
    cap = rt.cfg.check_fix_rounds
    # The same-failure block and the cap block share the cause; only the rounds tell them apart.
    if cause == "check-failed" and claim.fix_attempts >= cap:
        raise Refused(f"{key} used {claim.fix_attempts} of {cap} fix rounds; that stop is the owner's")
    if any(ledger.meta_get(rt.con, f"{k}:{key}") for k in _CAPS):
        raise Refused(f"cardinal already resumed this run of {key}")
    if key in ledger.issue_holds(rt.con):
        raise Refused(f"{key} is held; the owner's flocc release {key} lifts the hold")
    if not Path(claim.worktree).exists():
        raise Refused(f"{key}'s worktree is gone")
    decision = admit_rerun(rt, claim)
    if not decision.ok:
        raise Refused(f"not now: {decision.reason}")
    phase = "fix" if cause == "check-failed" else claim.phase
    extra = _resume_prompt(rt, claim, cause, note)
    d = state_dir(rt.cfg, key)
    d.mkdir(parents=True, exist_ok=True)
    if phase == "fix":
        # Before the launch, which hands the file to the session; a fixer keeps the check output
        # it was started with.
        context = d / FIX_CONTEXT
        if context.exists():
            extra = f"{extra}\n\n{context.read_text()}"
        context.write_text(extra)
    rt.sessions.kill(session_name(key, claim.phase))
    start = rt.now()
    launch(rt, claim, phase, extra=extra)
    if phase != "fix":
        (d / RESUME_NOTE).write_text(extra)
    ledger.meta_set(rt.con, f"{CARDINAL_RESUME}:{key}", "1")
    ledger.update_claim(
        rt.con,
        key,
        state="running",
        phase=phase,
        fix_attempts=claim.fix_attempts + (cause == "check-failed"),
        restarts=0,
        started_at=start,
    )
    (d / BLOCKED).unlink(missing_ok=True)
    row = {"at": start, "key": key, "cause": cause, "phase": phase}
    ledger.meta_set(rt.con, f"{RESUME_LOG}:{start:.6f}", json.dumps(row))
    try:
        rt.linear.comment(
            claim.issue_id, f"cardinal resumed the {ARCHETYPE[phase]} after {cause}:\n\n{note}"
        )
    except LinearError as exc:
        print(f"{key}: resume comment not posted: {exc}", file=sys.stderr)
    return ledger.get_claim(rt.con, key) or claim


def resumes(con, since: float) -> list[tuple[float, str, str, str]]:
    """cardinal's resumes since `since`: (at, key, cause, phase), oldest first."""
    rows = []
    for (value,) in con.execute("SELECT value FROM meta WHERE key LIKE ?", (f"{RESUME_LOG}:%",)):
        try:
            v = json.loads(value)
            row = (float(v["at"]), str(v["key"]), str(v["cause"]), str(v["phase"]))
        except (ValueError, KeyError, TypeError):
            continue
        if row[0] >= since:
            rows.append(row)
    return sorted(rows)
```

- [ ] **Step 7: Run the tests**

Run: `uv run pytest -q tests/test_control.py tests/test_supervisor.py tests/test_poller.py`
Expected: PASS. A failure in an existing `test_supervisor.py` restart test means `_relaunch` no longer passes the fix context: compare against Step 4.

- [ ] **Step 8: Commit**

```bash
git add src/flocc/control.py src/flocc/supervisor.py src/flocc/poller.py tests/test_control.py tests/test_supervisor.py tests/test_poller.py
git commit -s -m "feat(control): resume restarts a stopped claim once, by its stop cause"
```

### Task 2: The `resume` command and gate verb, with the `fix` alias

**Files:**
- Modify: `src/flocc/cli.py:397-419` (`cmd_fix` → `cmd_resume`), `:923` (COMMANDS), `:989-991` (parser)
- Modify: `src/flocc/cardinalgate.py:68-69`
- Test: `tests/test_cardinalgate.py`, `tests/test_cli.py`

**Interfaces:**
- Consumes: `control.resume`, `control.Refused`.
- Produces: `flocc resume KEY --note TEXT`; gate lines `resume KEY --note TEXT` and `fix KEY --note TEXT` both → `["resume", KEY, "--note=TEXT"]`.

- [ ] **Step 1: Write the failing tests**

In `tests/test_cardinalgate.py`, replace `test_a_note_with_shell_text_stays_one_argument` with:

```python
@pytest.mark.parametrize("verb", ["resume", "fix"])
def test_a_note_with_shell_text_stays_one_argument(verb):
    note = "fix $(rm -rf ~); it's `x`\n-- and --config=/etc/x"
    quoted = "'" + note.replace("'", "'\\''") + "'"
    assert argv_for(f"{verb} NF-3 --note {quoted}") == ["resume", "NF-3", f"--note={note}"]
```

Add to the refused list: `"resume NF-1"`, `"resume NF-1 --note ''"`, `"resume nf-1 --note x"`, `"resume NF-1 --note x --force"`. Change `test_a_note_over_the_limit_is_refused` to use `"resume NF-1 --note "`.

In `tests/test_cli.py` add:

```python
def test_resume_replaces_fix():
    from flocc.cli import COMMANDS

    assert "resume" in COMMANDS and "fix" not in COMMANDS
```

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_cardinalgate.py tests/test_cli.py -k "note or resume"`
Expected: FAIL: the gate returns `["fix", ...]`, and `resume` is not in `COMMANDS`.

- [ ] **Step 3: Change the gate**

In `argv_for`, replace the `fix` case:

```python
        case ("resume" | "fix"), [key, "--note", note]:
            # fix is the verb resume replaced; cardinal's VM and the executor deploy separately.
            return ["resume", _key(key), f"--note={_text(note, 'note')}"]
```

- [ ] **Step 4: Change the command**

Replace `cmd_fix` with:

```python
def cmd_resume(args) -> int:
    from flocc.control import Refused, resume
    from flocc.sequence import ARCHETYPE
    from flocc.supervisor import LaunchError
    from flocc.tick import build_runtime, tick_lock

    cfg, path = _config(args)
    with tick_lock(cfg.tick_lock) as held:
        if not held:
            print("resume: a tick is running; try again in a moment", file=sys.stderr)
            return 1
        rt = build_runtime(cfg, path)
        try:
            claim = resume(rt, args.key, args.note)
        except Refused as exc:
            print(f"resume: {exc}", file=sys.stderr)
            return 1
        except (LaunchError, subprocess.SubprocessError, OSError) as exc:
            print(f"resume: the session did not start: {exc}", file=sys.stderr)
            return 1
    print(f"resumed the {ARCHETYPE[claim.phase]} on {args.key}")
    return 0
```

In `COMMANDS`, replace `"fix": cmd_fix,` with `"resume": cmd_resume,`. Replace the parser lines:

```python
    res = add("resume", "Restart a blocked or timed-out run once, with a note (cardinal's)")
    res.add_argument("key", type=_issue_key, help="issue key, e.g. NF-12")
    res.add_argument("--note", required=True, help="the answer, or what the next attempt does")
```

- [ ] **Step 5: Run the tests**

Run: `uv run pytest -q tests/test_cardinalgate.py tests/test_cli.py`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/flocc/cli.py src/flocc/cardinalgate.py tests/test_cardinalgate.py tests/test_cli.py
git commit -s -m "feat(cardinal): resume verb, with fix as its alias for one release"
```

### Task 3: Resumes in the daily digest

**Files:**
- Modify: `src/flocc/summary.py:124-131` (after the agent-shell section)
- Modify: `deploy/cardinal/plugin/skills/digest/SKILL.md:10-14`
- Test: `tests/test_summary.py`

**Interfaces:**
- Consumes: `control.resumes(con, since)`, `ledger.get_claim`.
- Produces: a digest section `## Runs cardinal resumed in the last 7 days` with a count line `- N resumed, M ended done`.

- [ ] **Step 1: Write the failing test**

```python
def test_digest_data_lists_cardinal_s_resumes_and_how_they_ended(tmp_cfg, con):
    import json

    for key, state in (("NF-1", "done"), ("NF-2", "blocked")):
        ledger.insert_claim(con, make(key, state=state))
    now = 8 * 86_400.0
    rows = [(now - 100, "NF-1", "needs-owner", "impl"), (now - 50, "NF-2", "idle", "impl")]
    rows.append((50.0, "NF-1", "crash", "impl"))
    for at, key, cause, phase in rows:
        value = json.dumps({"at": at, "key": key, "cause": cause, "phase": phase})
        ledger.meta_set(con, f"resume_log:{at:.6f}", value)
    text, _ = digest_data(tmp_cfg, con, now)
    assert "## Runs cardinal resumed in the last 7 days" in text
    assert "- 2 resumed, 1 ended done" in text
    assert "- NF-1 after needs-owner: done" in text and "- NF-2 after idle: blocked" in text
    assert "after crash" not in text


def test_a_resumed_claim_the_owner_reran_says_so(tmp_cfg, con):
    import json

    ledger.meta_set(
        con,
        "resume_log:10.000000",
        json.dumps({"at": 10.0, "key": "NF-5", "cause": "idle", "phase": "impl"}),
    )
    text, _ = digest_data(tmp_cfg, con, 100.0)
    assert "- NF-5 after idle: no claim" in text
```

- [ ] **Step 2: Run it to see it fail**

Run: `uv run pytest -q tests/test_summary.py -k resume`
Expected: FAIL: the section is missing.

- [ ] **Step 3: Add the section**

In `digest_data`, after the agent-shell block and before `through = ...`:

```python
    out += ["", "## Runs cardinal resumed in the last 7 days"]
    lines, ended_done = [], 0
    for _, key, cause, _ in control.resumes(con, now - 7 * 86400):
        c = ledger.get_claim(con, key)
        state = c.state.replace("_", " ") if c else "no claim"
        ended_done += state == "done"
        lines.append(f"- {key} after {cause}: {state}")
    if lines:
        lines.insert(0, f"- {len(lines)} resumed, {ended_done} ended done")
    out += _capped(lines, LIST_ROWS)
```

Import `control` with the module's other `flocc` imports (`from flocc import control, ...`); if that makes an import cycle through `supervisor`, import it inside `digest_data` instead. A claim the owner reran is a fresh claim with the same key; its state then reads as that run's, which is what the owner needs from the line.

- [ ] **Step 4: Tell the digest skill**

In `digest/SKILL.md` step 3, after the agent-shell sentence, add: "The resumed-runs count, and each resumed run that is not done, always go in too."

- [ ] **Step 5: Run the tests**

Run: `uv run pytest -q tests/test_summary.py tests/test_cardinal_plugin.py`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/flocc/summary.py deploy/cardinal/plugin/skills/digest/SKILL.md tests/test_summary.py
git commit -s -m "feat(digest): list cardinal's resumes and how many ended done"
```

### Task 4: Triage, dispatch and the book

**Files:**
- Modify: `deploy/cardinal/plugin/skills/triage/SKILL.md:16, 22-35`
- Modify: `deploy/cardinal/plugin/skills/dispatch/SKILL.md:11-14`
- Modify: `docs/book/cardinal.md:5-7, 46-50`, `docs/book/commands.md:49`
- Test: `tests/test_cardinal_plugin.py:103-114, 186-188` and new tests

**Interfaces:**
- Consumes: the `resume` gate verb, event kind `needs-owner` (part 1).
- Produces: skill text naming `executor resume KEY --note`.

- [ ] **Step 1: Write the failing tests**

In `tests/test_cardinal_plugin.py`, change `args` in `test_the_executor_wrapper_quotes_every_argument` to start with `"resume"`, change the three `f"{EXECUTOR} fix NF-3 --note ..."` strings (`:267`, `:308`, `:314`, `:327`) to `resume`, and replace `test_the_dispatch_skill_never_reruns_work` with:

```python
def test_dispatch_restarts_work_only_through_resume():
    text = (PLUGIN / "skills" / "dispatch" / "SKILL.md").read_text()
    assert "lifts a hold and nothing else" in text and f"{EXECUTOR} resume KEY" in text
    assert "fresh" in text and "Todo" in text


def test_triage_resumes_and_answers_with_the_resume_verb():
    text = (PLUGIN / "skills" / "triage" / "SKILL.md").read_text()
    assert f"{EXECUTOR} resume KEY --note" in text and f"{EXECUTOR} fix" not in text
    assert "`needs-owner`" in text and "acceptance criteria" in text
    for owner_stop in ("tests-touched", "branch-moved", "publish-failed", "rebase-conflict"):
        assert owner_stop in text
```

- [ ] **Step 2: Run them to see them fail**

Run: `uv run pytest -q tests/test_cardinal_plugin.py`
Expected: FAIL on the two new tests.

- [ ] **Step 3: Rewrite the triage rules**

In `triage/SKILL.md` line 16 change "fix notes" to "resume notes". Replace the `ended`, blocked or timed out bullet (lines 24-35) with:

```markdown
- `needs-owner`: read the run summary; it lists the questions. Answer only when the issue
  description, or a spec it names, settles every question and the answer stays inside the
  acceptance criteria: `/opt/cardinal/plugin/bin/executor resume KEY --note '<the answers, one
  per question, with where each comes from>'`. A question about scope, the criteria,
  credentials, cost or a system outside the repo is the owner's: tell them in at most three
  lines, quoting it.
- `ended`, blocked or timed out: read the run summary. Resume when you can say what the next
  attempt does differently: a check failing for a clear cause in the code with fix rounds left,
  an unfinished step, a session that stopped or died, a timeout with commits, a step that
  finished with no commits. `/opt/cardinal/plugin/bin/executor resume KEY --note '<what to do
  and where, one paragraph>'`. You get one resume per run. The service refuses it for
  `tests-touched`, `branch-moved`, `publish-failed`, `rebase-conflict`, `no-tests` and a check
  at its last fix round; those, and any stop that needs a decision, access or a spec change,
  go to the owner in at most three lines: the issue, why it stopped, the one thing they need
  to do.
  When the stop is the PR needing the amended-tests label (`tests-amended`), don't ask the owner:
  run `/opt/cardinal/plugin/bin/executor amend-tests KEY`, then resume with a note that the
  label is on the PR and it should finish with `flocc done`. If resume refuses, tell the owner in
  one line that the label is on and `flocc release KEY` reruns it.
```

Keep the `cap` bullet as is.

- [ ] **Step 4: Rewrite dispatch's release bullet**

Replace lines 11-14 of `dispatch/SKILL.md` with:

```markdown
- Release: `/opt/cardinal/plugin/bin/executor release KEY` lifts a hold and nothing else. It is
  refused for an issue whose run is blocked, timed out or done.
- Restart: when the owner asks you to restart a blocked or timed-out run, use
  `/opt/cardinal/plugin/bin/executor resume KEY --note '<what the owner wants done>'`, once per
  run. A fresh rerun, and anything on a done run, is the owner's: tell them to move the issue
  back to Todo.
```

- [ ] **Step 5: Update the book**

`docs/book/cardinal.md` lines 5-7: "Then it triages the run, resumes it once with a note when the note can carry it on (an answer to the agent's questions, or what the next attempt does differently), holds new starts, and tells you, in the Claude app, only what needs you. It never starts a fresh run or touches a finished one." Line 47: replace `fix` with `resume` (and say "`fix`, an alias of `resume` for one release"). `docs/book/commands.md:49`: replace the row with

```markdown
| `resume <key> --note TEXT` | Restart a blocked or timed-out run once, on its branch, with the note: the answer to a `needs-owner` stop, or direction after `check-failed` with rounds left, `no-changes`, `idle`, `crash` or a timeout. Refused for other stops |
```

- [ ] **Step 6: Run the tests, then the suite**

Run: `uv run pytest -q tests/test_cardinal_plugin.py tests/test_cardinalgate.py`
Expected: PASS.
Run: `just test-py`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add deploy/cardinal/plugin/skills docs/book tests/test_cardinal_plugin.py
git commit -s -m "docs(cardinal): triage and dispatch resume runs; the book names resume"
```
