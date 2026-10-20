---
name: implementer
description: Use when you are the implementer or fixer session for a linear-agents issue
---

Make the committed tests pass by implementing the behavior.

- Do not edit, delete or skip any file the tester committed. The service rejects the run if you
  do. If you believe a test is wrong, do not edit it: ask the owner (see the escalate skill), state
  the test change you propose, and keep working only on parts that do not depend on that test.
- Claim the files you will edit before editing them: `linear-agents claim '<path>'`. Claim files,
  not trees: `**` is refused, a `*` within one directory is fine. A rejected claim names the issue
  that holds it; message that session or escalate. A file the repo lists as shared is edited
  without a claim.
- When a peer's claim blocks your commit, finish everything else first, then message that peer
  once. If the file is still held after the peer's next turn, escalate (tier 2,
  `--cause claim-held:<path>`) instead of waiting.
- Nobody reads your session's pane. Never end a turn waiting for the owner in plain text: to wait
  on the owner, ask with `AskUserQuestion` (see the escalate skill).
- When auto mode refuses a check command, do not ask for it to be run. Commit with
  `linear-agents commit`, whose git hook runs the repo's lint on the commit, and finish with
  `linear-agents done`, which runs the full check before the PR.
- Run `linear-agents context` and read "Grants" before planning; it lists what you may do beyond
  the defaults. Grants that change launch flags (general-purpose agents, workflows) apply from the
  next session start; until then the gate refuses them, even when "Grants" lists them.
- Plan first: write `plan.md` in your state directory with numbered tasks, the paths each task
  touches, and its tests. Tasks that share a path go in different waves; a wave holds at most four
  tasks. Fit the plan to your launch budget, the phase's total launches (10 unless a grant says
  otherwise; a workflow counts as one): one plan review,
  one implementer per task, one reviewer per wave, one fix and its re-review, plus any
  `code-mapper` launch and the `source-finder` / `source-reader` pair, which count too. If the plan
  cannot fit, ask for more before building: escalate with `--cause grant:launches` and the number
  you need, or ask the owner (implementer sessions can) for an `allow: launches N` comment.
- With four or more tasks, have `linear-agents:plan-reviewer` read the plan once before building,
  and fix what it finds. With fewer, check the plan against the acceptance criteria yourself.
- Write each task's brief to `briefs/task-N.md` in your state directory: its requirements, the
  paths it owns, the focused test command. Start one `linear-agents:task-implementer` per task with
  the brief's path, every plan task included, even if there is only one.
- After each wave, write its `git diff` and test output to `waves/N.diff` and `waves/N.tests`
  in your state directory, and have one `linear-agents:wave-reviewer` review the wave.
- Findings get one fix round per run, across all waves: the same `task-implementer` route, then
  one `linear-agents:fix-rereviewer` on the fix diff. A second round needs a `fix-rounds` grant;
  without one, escalate the findings left after the first round.
- When you start `source-finder` or `source-reader`, put today's date in the task.
- Do small work yourself: a change outside the plan's tasks that fits in a few lines is not a task.
- While a wave runs, commit your own work with `linear-agents commit -m '<message>' -- <paths>`,
  never a bare `git commit`, which would take the implementers' half-done files.
- Start general-purpose agents and workflows only when "Grants" lists them. Never start
  teammates, never pass `model`, `effort`, `isolation`, `name` or `team_name` when starting an
  agent, and never run `claude` from the shell; no grant allows these. The gate refuses agent and workflow launches it does not
  allow and says whether a grant can help; a permission rule, not the gate, blocks `claude` in
  the shell.
- Run the repo's check (usually `just check`) before finishing.
- Never push or open a PR; the service does that.
- Finish with `linear-agents done --summary '<what changed and why, for the PR body>'`.

## Fix phase

When the service starts you to fix a failed check, the plan, the plan review, waves and wave
review do not apply. Fix the failure inline; if it is more than a few lines, write the brief to
`briefs/fix.md` in your state directory and start one `linear-agents:task-implementer` on it. Then
write its diff to `waves/fix.diff` and start one `linear-agents:fix-rereviewer` on that file; this
is all the fix phase allows. Give `fix-rereviewer` the failed check output
(`check-failure.md` in your state directory) as the single finding. Then run the check and finish
with `linear-agents done`.
