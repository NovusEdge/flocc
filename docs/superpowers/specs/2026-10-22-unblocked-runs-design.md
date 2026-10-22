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

Ten stops came from a setup that would have failed on main too: a missing tool, a full disk, a
setup past its timeout. `_start` already runs `./setup` before it claims anything, so a failure
costs no session, but every ready issue of that repo runs `./setup` again on every tick, under the
tick lock, and each gets its own tier-2 comment.

- When `./setup` fails, or leaves files git sees, in a worktree whose HEAD is still
  `origin/<base>`, the failure belongs to main or the host, not the issue. The poller marks the
  repo broken in the ledger with the base SHA, the time and the last 1500 characters of output,
  and sends one tier-1 escalation on the issue that hit it, saying the repo's issues are held.
- Admission holds every issue of a marked repo, without running `./setup`, while
  `origin/<base>` still has the marked SHA and the mark is under 24 hours old. The poller fetches
  the base of a marked repo once per tick to tell. A new commit on main, or the 24 hours, lets the
  next issue try again, and that try is the canary.
- `flocc canary <repo>` on the host runs `./setup` on a detached worktree of `origin/<base>`,
  removes it, and clears the mark when setup passes. It is for a fix made on the host, which
  moves no commit.
- A setup failure on an issue's own branch (a rerun with commits) is the issue's, and is handled
  as today.

Rejected: a scheduled canary of setup and check on main. It costs a setup and an e2e run per repo
per day whether or not work is waiting, and runs under the tick lock for up to 45 minutes. The
failure the poller already sees answers the same question for free.

### 2. The host carries what the repos' checks need

- A host-file `playwright = "<version>"` (default empty, off) makes a new root step, Browsers,
  run `npx -y playwright@<version> install-deps chromium` on apt hosts. That installs the
  system libraries and fonts chromium needs, which the service user cannot install, and without
  which e2e checks fail in milliseconds. The step's check runs the same command with `--dry-run`,
  which prints "Missing system dependencies (N):" and the packages, or nothing missing. Other
  package managers get the detail "apt hosts only" and no change. flocc-nimble is Debian 13, the
  repos pin 1.64.0, and on 2026-10-22 the dry run listed 31 missing packages, fonts among them.
- A host-file `[tools]` table (default empty) names mise tools and versions to set globally for
  the service user, e.g. `go = "1.27.1"`. A new service step, Tools, runs
  `mise use -g <tool>@<version>` for each one `mise current` does not report. On 2026-10-22
  flocc-nimble had go 1.27.1 installed under mise with no global version, which is the
  "No version is set for shim: go" failure.

- A host-file `docker = true` makes a root step, Docker, install `docker.io` on apt hosts,
  enable the daemon, and add the service user to the `docker` group. kairos's check uses
  testcontainers; on 2026-10-22 NF-321's check failed twice on the missing Docker socket, because
  flocc-nimble had no Docker. The group is root-equivalent on the VM, which the owner accepts
  because host VMs are disposable, and it reaches sessions after the service user's systemd
  manager restarts.

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
