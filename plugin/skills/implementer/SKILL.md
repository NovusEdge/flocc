---
name: implementer
description: Use when you are the implementer or fixer session for a linear-agents issue
---

Make the committed tests pass by implementing the behavior.

- Do not edit, delete or skip any file the tester committed. The service rejects the run if you
  do. If you believe a test is wrong, do not edit it: ask the owner (see the escalate skill), state
  the test change you propose, and keep working only on parts that do not depend on that test.
- Claim paths before editing them: `linear-agents claim '<glob>'`. A rejected claim names the
  issue that holds it; message that session or escalate.
- Run `linear-agents context` and read "Grants" before planning; it lists what you may do beyond
  the defaults.
- Plan first: write `plan.md` in your state directory with numbered tasks, the paths each task
  touches, and its tests. Tasks that share a path go in different waves; a wave holds at most four
  tasks. Fit the plan to your launch budget (10 unless a grant says otherwise: one plan review,
  one implementer per task, one reviewer per wave, one fix and its re-review); if it cannot fit,
  ask for `launches` before building.
- With four or more tasks, have `linear-agents:plan-reviewer` read the plan once before building,
  and fix what it finds. With fewer, check the plan against the acceptance criteria yourself.
- Write each task's brief to `briefs/task-N.md` in your state directory: its requirements, the
  paths it owns, the focused test command. Start one `linear-agents:task-implementer` per task with
  the brief's path, every plan task included, even if there is only one.
- After each wave, write its `git diff` and test output to `waves/N.diff` and `waves/N.tests`
  in your state directory, and have one `linear-agents:wave-reviewer` review the wave.
- Findings get one fix round: the same `task-implementer` route, then one
  `linear-agents:fix-rereviewer` on the fix diff. A second round needs a `fix-rounds` grant;
  without one, escalate what is left.
- When you start `source-finder` or `source-reader`, put today's date in the task.
- Do small work yourself: a change outside the plan's tasks that fits in a few lines is not a task.
- While a wave runs, commit your own work with `linear-agents commit -m '<message>' -- <paths>`,
  never a bare `git commit`, which would take the implementers' half-done files.
- Never pass `model` or `isolation` when starting an agent, and never start general-purpose agents,
  workflows or teammates unless "Grants" lists them. Never run `claude` from the shell. The gate
  refuses these and says how to ask.
- Run the repo's check (usually `just check`) before finishing.
- Never push or open a PR; the service does that.
- Finish with `linear-agents done --summary '<what changed and why, for the PR body>'`.

## Fix phase

When the service starts you to fix a failed check, the plan steps above do not apply. Fix the
failure inline; if it is more than a few lines, use one `linear-agents:task-implementer` with a
brief and one `linear-agents:fix-rereviewer` on its diff, which is all the fix phase allows. Then
run the check and finish with `linear-agents done`.
