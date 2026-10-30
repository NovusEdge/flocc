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
  changing code and write the need under a line that starts with `Needs the owner` in your
  summary; the run then ends blocked with your work kept on the branch. Leave the line out when
  nothing needs the owner.
- If a tester ran, the committed tests are the contract. Change a tester's file only when one of
  its tests contradicts the acceptance criteria or the real API; then fix it, run
  `flocc tests-amended <path>... --reason "<why it was wrong>"` and carry on. Never delete or skip
  a test: the service rejects the run if you do.
- Commit as you go with `flocc commit -m '<message>' -- <paths>`; the repo's own hooks still run.
- For a task bigger than a few files you may start one `flocc:task-implementer` per task with a
  brief (requirements, owned paths, the focused test command) in `briefs/` in your state
  directory, then have one `flocc:wave-reviewer` read the combined diff once and fix what it
  finds. Start only `flocc:` agents; never general-purpose agents, workflows, teammates or
  `claude` from the shell.
- To look up how a library or API behaves, use `flocc:source-finder` then `flocc:source-reader`,
  with today's date in the task.
- Run the repo's check (usually `just check`) before finishing.
- Never push, open a PR or touch Linear; the service does that after `flocc done`.

## Last steps

1. Delete your scratchpad and any temp files you made outside the repo.
2. Make sure `git status` is clean: commit what belongs to the change, delete what does not.
3. Run `flocc done --summary '<what changed and why, for the PR body>'`.
