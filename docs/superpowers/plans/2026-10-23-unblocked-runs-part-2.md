# Unblocked runs, part 2: two fix rounds and owner-approved test amendments

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A failed check gets a second fix round before it blocks, and a test amendment the owner approved through AskUserQuestion reaches a PR instead of blocking as tests-touched.

**Architecture:** The supervisor's `check_failed` branch counts rounds against a new config key. The existing `flocc gate --answered` hook appends each answered question to `answers.jsonl` in the state dir; a new `flocc tests-amended` command lets the implementer declare approved amendments, checked against that log, into `tests-amended.json`; the publisher subtracts those paths from the tester guard and lists them in the PR body, and the review input names them for the reviewer.

**Tech Stack:** Python 3.13, uv, pytest, ruff.

**Spec:** `docs/superpowers/specs/2026-10-22-unblocked-runs-design.md`, sections 3 and 4. Issues NovusEdge/flocc#61 and #62.

## Global Constraints

- Commits use `git commit -s`, no `Co-Authored-By` line.
- `uv run pytest -q`, `uv run ruff check src tests`, `uv run ruff format src tests`; restore `src/flocc/linear.py` after formatting (`git checkout -- src/flocc/linear.py`).
- Config key: `check_fix_rounds: int = 2`. The existing `fix-rounds` grant in `src/flocc/grants.py` is a different thing (rounds of the implementer's in-session fix-rereviewer) and stays as it is.
- State files: `answers.jsonl` (append-only, one JSON object per line) and `tests-amended.json` (one JSON object keyed by repo-relative path), both in the claim's state dir.
- The amendment record is not a security boundary: the agent can write the state dir. It keeps honest mistakes out and leaves the reviewer a record.

## Review Focus

- An answered question from an earlier run (a previous claim of the same issue) must not approve an amendment in this run: entries carry the run id, and `tests-amended` compares it with `FLOCC_RUN`.
- A touched tester file not in `tests-amended.json` still blocks, even when other touched files are approved; the block names only the unapproved ones.
- `tests-amended.json` must not leak into the next claim: `_start` clears it with the other per-run files.
- A torn last line in `answers.jsonl` (hook killed mid-write) is skipped, not fatal.
- `check_fix_rounds = 1` keeps today's behaviour exactly (one fix round, then a block).

---

### Task 1: Fix rounds from config

**Files:** `src/flocc/config.py`, `src/flocc/supervisor.py` (`_finish_phase`'s `check_failed` branches), `deploy/config.example.toml`, `tests/test_supervisor.py`.

- [ ] **Step 1: Failing tests** in `tests/test_supervisor.py`, beside the existing check-failed tests (find them with `grep -n check_failed tests/test_supervisor.py` and reuse their setup):
  - With the default config, a first `check_failed` launches fix round 1 (`fix_attempts == 1`), a second launches round 2 (`fix_attempts == 2`), and a third blocks with a summary starting `The check still fails after 2 fix rounds`.
  - Round 2's launch `extra` contains both failures' excerpts, the first under a line `An earlier fix round failed with:`.
  - With `check_fix_rounds = 1` (`dataclasses.replace(rt.cfg, check_fix_rounds=1)`), the second `check_failed` blocks, with summary `The check still fails after 1 fix round`.
- [ ] **Step 2: Run them**, expect failures.
- [ ] **Step 3: Implement.** `config.py`: `check_fix_rounds: int = 2` after `idle_escalate_s`. `supervisor.py`: the first branch becomes `elif result.status == "check_failed" and claim.fix_attempts < rt.cfg.check_fix_rounds:`. Build `extra` as today; when `claim.fix_attempts > 0` and `FIX_CONTEXT` exists, prefix it: `f"An earlier fix round failed with:\n\n{previous}\n\n"` where `previous` is the old file's text. Write the combined text to `FIX_CONTEXT`, launch, and update `fix_attempts=claim.fix_attempts + 1`. The block's summary becomes `f"The check still fails after {rounds}:\n{result.detail}"`, where `rounds` is "1 fix round" or "N fix rounds" for `claim.fix_attempts`. `deploy/config.example.toml`: after `idle_escalate_s`, `# Fix sessions a failed check gets before the claim blocks.` and `# check_fix_rounds = 2`.
- [ ] **Step 4: Run** `uv run pytest -q tests/test_supervisor.py tests/test_config.py`, expect pass.
- [ ] **Step 5: Commit** `supervisor: a failed check gets check_fix_rounds fix sessions, default 2`.

### Task 2: Log answered questions

**Files:** `src/flocc/gate.py`, `src/flocc/cli.py` (`cmd_gate`'s `--answered` path), `tests/test_gate.py`.

**Produces:** `gate.ANSWERS = "answers.jsonl"`; `gate.note_answer(state: Path, payload: dict, run: str | None, now: float) -> None`; `gate.answers(state: Path, run: str | None) -> list[dict]` (entries of that run, torn lines skipped).

- [ ] **Step 1: Failing tests** in `tests/test_gate.py`:
  - `note_answer` with a PostToolUse payload `{"tool_name": "AskUserQuestion", "tool_input": {"questions": [{"question": "Amend test_x.py to use input_schema?"}]}, "tool_response": {"answers": {"Amend test_x.py to use input_schema?": "Yes"}}}` appends `{"at", "run", "questions": ["Amend test_x.py to use input_schema?"], "response": <tool_response as given>}`.
  - `answers(state, "impl@1")` returns only that run's entries and skips a torn line.
  - `main(["gate", "--answered"])` with that payload on stdin and `FLOCC_STATE`/`FLOCC_RUN` set writes the line, and still clears `asking` as today.
- [ ] **Step 2: Run them**, expect failures.
- [ ] **Step 3: Implement.** In `gate.py`, `note_answer` appends one `json.dumps(...)+"\n"` with a single `os.write` on an `O_APPEND` descriptor (as `permission.record` does); `tool_response` is stored as received, since its shape is Claude Code's and may change. `answers` reads lines, `json.loads` each in a `try`, keeps dicts whose `run` equals `run`. In `cmd_gate`, on `--answered` for an `AskUserQuestion` payload, call `gate.note_answer(_state_dir(), payload, os.environ.get("FLOCC_RUN"), time.time())` before the existing clear; a failure there must not change the hook's exit code (print to stderr, as the surrounding code does).
- [ ] **Step 4: Run** `uv run pytest -q tests/test_gate.py tests/test_cli.py`, expect pass.
- [ ] **Step 5: Commit** `gate: record each answered question with its run`.

### Task 3: `flocc tests-amended`

**Files:** `src/flocc/cli.py` (new `cmd_tests_amended`, `COMMANDS`, parser), `tests/test_cli.py`.

**Consumes:** `gate.answers` (Task 2). **Produces:** `AMENDED = "tests-amended.json"` in `flocc.gate`, shape `{"<path>": {"question": str, "response": <tool_response>}}`.

- [ ] **Step 1: Failing tests** in `tests/test_cli.py` (set `FLOCC_STATE`, `FLOCC_RUN=impl@1`):
  - With an `answers.jsonl` entry of run `impl@1` whose question names `gateway/tests/test_nf321_neutral_names.py`, `flocc tests-amended gateway/tests/test_nf321_neutral_names.py --question "Amend"` exits 0, writes the file with that path, the full question text and the response, and prints `recorded 1 amended test file`.
  - A second call for another path merges into the same file.
  - Exit 2, nothing written, when: no answered question of this run contains the `--question` text; the matching question names neither the path nor its basename; the only matching entry is from run `impl@0`.
- [ ] **Step 2: Run them**, expect failures.
- [ ] **Step 3: Implement.** Find the newest entry of this run where some question contains `args.question`; require each path, or its basename, to appear in that question's text. On success, load the existing file (empty dict if missing or unreadable), set each path, write via a temp file and `os.replace`. Error messages name what is missing, for example `tests-amended: no answered question of this run contains "Amend"; ask the owner with AskUserQuestion first`. Parser: `add("tests-amended", "Record tester files the owner approved changing, from an answered question")`, positional `paths` (`nargs="+"`), required `--question`.
- [ ] **Step 4: Run** `uv run pytest -q tests/test_cli.py`, expect pass.
- [ ] **Step 5: Commit** `flocc tests-amended: record owner-approved changes to tester files`.

### Task 4: Publish and review approved amendments

**Files:** `src/flocc/publisher.py` (`publish`), `src/flocc/tick.py` (`publish_fn`), `src/flocc/supervisor.py` (`_start_review`), `src/flocc/poller.py` (`_start`'s stale list), `plugin/skills/implementer/SKILL.md`, `plugin/skills/reviewer/SKILL.md`, `tests/test_publisher.py`, `tests/test_supervisor.py`, `tests/test_poller.py`.

**Consumes:** `gate.AMENDED` (Task 3).

- [ ] **Step 1: Failing tests:**
  - `tests/test_publisher.py` (reuse its existing tests-touched setup): `publish(..., amended={"t/test_a.py": {...}})` with only `t/test_a.py` touched proceeds past the guard, and the `gh pr create` body contains `## Amended tests` and a line naming `t/test_a.py` and the question. With `t/test_a.py` and `t/test_b.py` touched and only `a` amended, the result is `tests_touched` with detail `t/test_b.py`.
  - `tests/test_supervisor.py`: when `tests-amended.json` exists, `review-input.md` has a `## Amended tests` section listing each path and question.
  - `tests/test_poller.py`: `_start` removes a leftover `tests-amended.json` and `answers.jsonl`.
- [ ] **Step 2: Run them**, expect failures.
- [ ] **Step 3: Implement.**
  - `publish` gains `amended: dict[str, dict] | None = None`. `touched -= set(amended or {})` before the guard. A helper `amended_section(amended) -> str` returns `""` for none, else `"\n\n## Amended tests\n\nThe owner approved these changes to the tester's files:\n\n" + one "- `path`: <question>" line each`. The PR body becomes `f"Fixes {key}\n\n{summary}{amended_section(amended)}"`. An existing PR (the `gh pr list` path) is left as it is.
  - `tick.py`'s `publish_fn` reads `state_dir(cfg, claim.issue_key) / gate.AMENDED` (missing or unreadable means none) and passes it.
  - `_start_review` appends `amended_section(...)` text, read the same way, to `review-input.md`. Import `amended_section` from `flocc.publisher`.
  - `poller._start`: add `"tests-amended.json"` and `"answers.jsonl"` to the stale-file tuple.
  - `plugin/skills/implementer/SKILL.md`, where it covers asking the owner: after the owner approves changing a tester file, run `flocc tests-amended <path>... --question "<words from your question>"` before committing the change; without it the run blocks at publish.
  - `plugin/skills/reviewer/SKILL.md`: if the review input has an "Amended tests" section, check each amendment against the acceptance criteria; an amendment that weakens what the test asserts is a finding and the verdict is changes requested.
- [ ] **Step 4: Run** `uv run pytest -q`, expect pass. `tests/test_plugin.py` may pin skill text; update only what the new lines change.
- [ ] **Step 5: Commit** `Publish owner-approved test amendments and show them to the reviewer`.

### Task 5: Docs and changelog

- [ ] In `docs/book/` find where tests-touched blocks and `--accept-tests` are described (`grep -rn "accept-tests" docs/book`) and add one paragraph: an amendment approved through the Claude app and recorded with `flocc tests-amended` goes into the PR's "Amended tests" section instead of blocking; anything else still blocks. Where check failures are described, say a failed check gets `check_fix_rounds` (default 2) fix sessions.
- [ ] `CHANGELOG.md` `## Unreleased` (create it above `## 0.9.0` if absent, with the summary line "Fewer runs stop for things a session can settle itself."): two bullets for the above.
- [ ] Commit `docs: fix rounds and approved test amendments`.
