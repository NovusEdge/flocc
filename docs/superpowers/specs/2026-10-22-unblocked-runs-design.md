# Unblocked runs: flocc finishes most issues without the owner

## Goal

The owner asked for flocc to be "90% of the time unblocked and just get shit done". Today it is
not close: of 11 NF issues with a run record since 2026-10-01, 2 reached a PR with no owner action
(3 counting NF-256, whose only stop was an idle notice). This spec removes the causes that the
record shows, in the order of stops removed per unit of work.

## What stopped runs

Every flocc comment on the 12 `claude` issues updated since 2026-10-01 was read on 2026-10-22.
Of the 27 stops:

| Cause | Stops | State on 2026-10-22 |
|---|---|---|
| `./setup` failed in the worktree: mise's `go` shim had no version, the disk filled, setup passed 900 s | 10 | Open. The disk was resized; the go shim last failed on 10-19 |
| The check failed again after one fix round; NF-240's Playwright tests died in 1-2 ms, and the alerts name missing chromium libraries and fonts | 5 | Open |
| A file claim held by an issue in another repo | 4 | Fixed 2026-10-20 (c6d74fc) |
| Tests amended: approved by the owner, then blocked as tests-touched or asked again | 3 | Open |
| No "Acceptance criteria" heading (all 2026-10-10) | 3 | Habit; no change |
| Idle notice (NF-256, no action needed) | 1 | Working as meant |
| Stuck at Claude Code's start screen (NF-260) | 1 | Fixed 2026-10-20 (onboarding written at launch) |

Two more stops on 2026-10-22, dialogs nobody answered, came after this count and are fixed by
PR #57.

Reviews did not stop anything: all six approved.

## Changes

### 1. A repo whose main is broken does not take issues

Ten stops came from a setup that would have failed on main too. Each one cost a claim, a
worktree and a session before the owner heard of it.

- The tick runs a canary for a repo when the repo has had a claim in the last 14 days and its base
  branch has moved since the last canary, at most once a day. The canary makes a worktree of the
  base branch, runs `./setup` with the usual timeout, then the repo's `check`.
- It runs only when no session is running on the host, so it never competes with work for the
  disk, the CPU or the setup timeout.
- A failure marks the repo `main-broken` in the ledger with the step and the last 40 lines of
  output, and sends one tier-1 escalation naming the repo. Admission then holds the repo's issues
  with a reason in `status`; it does not comment on each issue.
- The next canary that passes clears the mark. `flocc canary <repo>` on the host runs one now, so
  the owner can clear a mark after fixing main.

Rejected: running the canary before every claim. It doubles setup time for every issue to catch
a failure that happens a few times a week.

### 2. The host carries what the repos' checks need

- The Packages step installs Playwright's chromium dependencies and a base font set (the Debian
  package list `npx playwright install-deps chromium` prints, frozen in the step), because a
  service user cannot install them and e2e checks fail in milliseconds without them.
- A host-file `[host] tools` list (default empty) names mise tools to install globally for the
  service user, e.g. `go = "1.24"`, so a repo's mise shim resolves without its own `.mise.toml`
  pin. `flocc doctor` reports each one.

### 3. Two fix rounds before a block

`check_failed` launches a fix session while `fix_attempts < cfg.fix_rounds` (default 2, today
fixed at 1). The second fix round gets both failures' excerpts. The block text says how many
rounds ran.

### 4. A test amendment the owner approved does not block

The tests-touched check exists so an implementer cannot weaken a test to pass it. An approval in
the Claude app is the owner's answer; flocc should not ask again.

- The `flocc gate --answered` hook already sees every answered AskUserQuestion. It appends the
  question, the options and the chosen answer to the state dir's `answers.jsonl`.
- The implementer, after an approval, runs `flocc tests-amended <path>... --question <text>`. The
  command refuses unless `answers.jsonl` holds an answered question from this run whose text names
  every path.
- When the check finds only paths listed in `tests-amended`, the publisher opens the PR and adds
  an "Amended tests" section to its body naming each file, the question and the answer, and the
  reviewer's prompt tells it to check those amendments against the acceptance criteria.
- Any other touched tester file blocks as today.

This is not a security boundary; the agent can write `answers.jsonl` itself. It keeps honest
mistakes from slipping through and leaves a record the reviewer reads, which is what the check
was for (see the cardinal design, "What keeps upstream safe").

### 5. Auto mode tuned from the log

flocc-nimble's host file has no `[permissions]` table. Write the environment lines the cardinal
design proposes ("Each session runs in a disposable VM…") and ship them. After a week, turn
`flocc permissions report` into exact allow rules. This is configuration, not code.

## Order and measure

1 and 2 first (the 10 setup stops and the 3 e2e check failures, 13 of the 18 stops whose cause is
still open), then 3 and 4, then 5. After each ships, recount
the comment record the same way. The goal holds when 9 of 10 consecutive issues reach an approved
PR with no owner action beyond merging.

## Not in scope

- The judge in the permission hook (cardinal design, Step 1). The record shows no stop it would
  have removed; revisit when `flocc permissions report` shows blocks that cost runs.
- Issue-writing help for acceptance criteria.
