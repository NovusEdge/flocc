---
name: implementer
description: Use when you are the implementer or fixer session for a flocc issue
---

Make the committed tests pass by implementing the behavior.

- Do not edit a file the tester committed unless one of its tests is wrong: it contradicts the
  acceptance criteria or the real API. Then fix the test, run
  `flocc tests-amended <path>... --reason "<why it was wrong>"`, and carry on; do not ask the
  owner. Never delete or skip a test to make it pass: the service rejects the run if you do. Any
  other change to a tester file also gets the run rejected.
- Claim the files you will edit before editing them: `flocc claim '<path>'`. Claim files,
  not trees: `**` and any glob that matches a directory are refused, a `*` within one directory
  is fine. A rejected claim names the issue that holds it; message that session or escalate. A
  file the repo lists as shared is edited without a claim.
- When a peer's claim blocks your commit, finish everything else first, then message that peer
  once. If the peer's reply does not free the file, or no reply has come when you would otherwise
  end your turn, escalate instead of waiting; the owner has to release or steer the peer, so this
  is not a question for `AskUserQuestion`:
  `flocc escalate --tier 2 --cause claim-held:<path> --summary '<peer> holds <path>' --action '<what the owner should do>' --waiting`
- Nobody reads your session's pane. Never end a turn waiting for the owner in plain text: to wait
  on the owner, ask with `AskUserQuestion` (see the escalate skill).
- When auto mode refuses a check command, do not ask for it to be run. Commit with
  `flocc commit`; the repo's own pre-commit hook still runs. Finish with
  `flocc done`; the service runs the full check after it.
- Run `flocc context` and read "Grants" before planning; it lists what you may do beyond
  the defaults. Grants that change launch flags (general-purpose agents, workflows) apply from the
  next session start; until then the gate refuses them, even when "Grants" lists them.
- Plan first: write `plan.md` in your state directory with numbered tasks, the paths each task
  touches, and its tests. Tasks that share a path go in different waves; a wave holds at most four
  tasks. Fit the plan to your launch budget, the phase's total launches (10 unless a grant says
  otherwise; a workflow counts as one): one plan review,
  one implementer per task, one reviewer per wave, one fix and its re-review, plus any
  `code-mapper` launch and the `source-finder` / `source-reader` pair, which count too. If the plan
  does not fit, shrink it: merge small tasks or do them inline. Escalate only when the issue
  itself is too large, and propose the split.
- With four or more tasks, have `flocc:plan-reviewer` read the plan once before building,
  and fix what it finds. With fewer, check the plan against the acceptance criteria yourself.
- Write each task's brief to `briefs/task-N.md` in your state directory: its requirements, the
  paths it owns, the focused test command. Start one `flocc:task-implementer` per task with
  the brief's path, every plan task included, even if there is only one.
- After each wave, write its `git diff` and test output to `waves/N.diff` and `waves/N.tests`
  in your state directory, and have one `flocc:wave-reviewer` review the wave.
- Findings get one fix round per run, across all waves: the same `task-implementer` route, then
  one `flocc:fix-rereviewer` on the fix diff. A second round needs a `fix-rounds` grant;
  without one, list the findings left after the first round in your `flocc done` summary and
  finish; the service's reviewer reads the PR.
- When you start `source-finder` or `source-reader`, put today's date in the task.
- Do small work yourself: a change outside the plan's tasks that fits in a few lines is not a task.
- While a wave runs, commit your own work with `flocc commit -m '<message>' -- <paths>`,
  never a bare `git commit`, which would take the implementers' half-done files.
- Start general-purpose agents and workflows only when "Grants" lists them. Never start
  teammates, never pass `model`, `effort`, `isolation`, `name` or `team_name` when starting an
  agent, and never run `claude` from the shell; no grant allows these. The gate refuses agent and workflow launches it does not
  allow and says whether a grant can help; a permission rule, not the gate, blocks `claude` in
  the shell.
- Run the repo's check (usually `just check`) before finishing.
- Never push or open a PR; the service does that.
- Finish with `flocc done --summary '<what changed and why, for the PR body>'`.

## Fix phase

When the service starts you to fix a failed check or a review's findings, the plan, the plan review, waves and wave
review do not apply. Fix the failure inline; if it is more than a few lines, write the brief to
`briefs/fix.md` in your state directory and start one `flocc:task-implementer` on it. Then
write its diff to `waves/fix.diff` and start one `flocc:fix-rereviewer` on that file, giving it the
failed check output (`check-failure.md` in your state directory) as the single finding; this
is all the fix phase allows. When the fix is a few lines, skip the `fix-rereviewer`. Then run
the check and finish with `flocc done`.

When the fix answers a review, fix only what the review asks that is agent work. For a finding
that needs the owner (a manual run, a product decision, a question for the lead), change nothing
and put it in the `flocc done` summary, in a paragraph that starts with `Needs the owner:`. Do
not ask the owner; the summary reaches them on the issue.
