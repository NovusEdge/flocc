---
name: executor
description: Use when you are the executor session for a flocc issue
---

Carry out the issue as written. The issue, and the specs and plans it names, are the approved
design: do not redesign, re-plan or ask for sign-off.

- Read the issue's acceptance criteria and every spec or plan path it names before changing code.
  When it names a plan, its tasks are your tasks, in its order.
- Where the spec leaves a choice open, take its default or the smallest choice that meets the
  criteria. Nobody answers questions in this session. If you cannot go on without the owner, stop
  changing code and pass each question as `--needs-owner '<question>'` to `flocc done`; the run
  then ends blocked with your work kept on the branch. If the work turns out to be unneeded (it
  does not reproduce, or another change already did it), run `flocc done --nothing-to-do '<why>'`
  instead of committing nothing.
- If a tester ran, the committed tests are the contract. Change a tester's file only when one of
  its tests contradicts the acceptance criteria or the real API; then fix it, run
  `flocc tests-amended <path>... --reason "<why it was wrong>"` and carry on. Never delete or skip
  a test: the service rejects the run if you do.
- When the issue's wording would remove or contradict a fact the existing code or docs still
  state (an exception, a condition), keep that fact unless the issue explicitly says to remove it;
  if keeping it breaks an acceptance criterion, pass the conflict as a `--needs-owner` question.
- Commit as you go with `flocc commit -m '<message>' -- <paths>`; the repo's own hooks still run.
  Keep each subject to 72 characters.
- For a task bigger than a few files you may start one `flocc:task-implementer` per task with a
  brief (requirements, owned paths, the focused test command) in `briefs/` in your state
  directory, then have one `flocc:wave-reviewer` read the combined diff once and fix what it
  finds. Start only `flocc:` agents; never general-purpose agents, workflows, teammates or
  `claude` from the shell.
- To look up how a library or API behaves, use `flocc:source-finder` then `flocc:source-reader`,
  with today's date in the task.
- While working, run only the tests your change touches, by file or test id
  (`pytest tests/test_x.py::test_y`, `pytest -k name`, or the runner's equivalent). Before
  finishing, run those again with the repo's lint and format checks. Do
  not run the full check (usually `just check`) yourself: `flocc done` runs it, and a failure
  comes back to a fixer with its output. A full run here repeats it, and queues on the suite lock
  behind other issues' runs until your Bash call times out. A repo instruction to run the full
  check before pushing, or to record its counts, is met by that run: flocc pushes, not you.
- Never push, open a PR or touch Linear; the service does that after `flocc done`.

## Last steps

1. Delete your scratchpad and any temp files you made outside the repo.
2. Make sure `git status` is clean: commit what belongs to the change, delete what does not.
3. Run `flocc done --summary '<what changed and why, for the PR body>'`, with a
   `--needs-owner '<question>'` per open question.
