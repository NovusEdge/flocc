# Cardinal autonomy, part 2: typed stops, resume on comment, no `fix` alias

Status: drafted 2026-11-06 from part 1's Roadmap, revised after an Opus review against the code;
the owner asked for implementation to follow.

## Goal

Part 1 gave cardinal a capped `resume` and wider reach. cardinal still learns why a run stopped
by reading `blocked.md` prose. An owner comment on a finished run with an open PR also does
nothing until the owner moves the issue back to Todo. This part:

1. Gives every stop a cause and a type in code. Events carry both, the service retries the one
   transient stop itself, and cardinal decides from the type instead of from prose.
2. Starts a fix session on the same branch when the owner or a `steer_authors` id comments on a
   done issue whose PR is still open.
3. Removes the gate's `fix` alias and the legacy `cardinal_fix` cap key. Both machines have run
   part 1 since 2026-11-06.

Two PRs: (1) typed stops with the alias removal, (2) resume on comment. They touch different
code paths and can be built in parallel. The two shared files, `supervisor.py` and
`triage/SKILL.md`, are touched in different places.

Out of scope: GitHub PR comments as a trigger, `file` in `on` mode (it waits on observe-mode
drafts), and any new cardinal verb.

## 1. Typed stops

### Today

`_block` (`supervisor.py`) writes the cause as the first line of `blocked.md` and sends a
`Notice`. `Notifier.send` emits the event with detail `"{cause}: {summary}"` cut to 300
characters. A `needs-owner` event's detail is `"N questions"`. `control.RESUMABLE` lists the
causes `resume` accepts. The fix-cap block and the "failed the same way twice" block both
write `check-failed`, so `resume` tells them apart only by `fix_attempts`. Triage names the
causes in prose and reads `run-summary` to learn which one it has. A timeout writes no
`blocked.md`; its cause comes from the claim state.

### Cause table

A new module, `src/flocc/causes.py`, holds one table that maps each cause to its type.
`RESUMABLE` becomes the causes of type `agent`, plus `needs-owner`. `_block` looks its cause up
on its first line, before it kills the session, and an unknown cause raises there. The timeout
path does the same lookup.

| Cause | Type | Who acts |
|---|---|---|
| `publish-failed` | transient | The service retries once. A second failure is typed `owner` |
| `crash` | agent | cardinal may resume |
| `idle` | agent | cardinal may resume |
| `timed-out` | agent | cardinal may resume. The service's own restart of a timeout with commits is unchanged |
| `check-failed` (the same failure twice, with rounds left) | agent | cardinal may resume |
| `no-changes` | agent | cardinal may resume |
| `needs-owner` | owner | cardinal resumes only when the issue text settles every question. Otherwise it goes to the owner |
| `check-cap` | owner | The owner. This is any check-failed block with `fix_attempts >= check_fix_rounds` |
| `tests-touched` | owner | The owner (release or `--accept-tests`) |
| `no-tests` | owner | The owner |
| `branch-moved` | owner | The owner |
| `rebase-conflict` | owner | The owner |
| `base-broken` | repo | The service holds the repo, as it does today. The owner fixes the base |

`crash` is `agent`: `supervise` already relaunches a dead session once, so a crash block is
the second death.

### Changes

- Both check-failed blocks write `check-cap` when `fix_attempts >= check_fix_rounds`. That is
  the cap block, and the same-failure block on the last round. `resume` keeps its
  `fix_attempts` guard. A claim blocked before this change keeps `check-failed`, and that guard
  still refuses it.
- The `events` table gains nullable `cause` and `type` columns. Old rows and events that are
  not stops (`daily`, `service-error`, `done` ends) read null. `Notice` gains `cause` and
  `stop_type`. `_block` sets both, and `Notifier.send` passes them to `emit`, so notices without
  them emit nulls. The JSON line carries `"cause"` and `"type"` next to `"kind"`. `detail`
  keeps its current text, `"{cause}: "` prefix included, so the follow stream, acks and
  triage's `done` match do not change.
- The timeout path emits `cause="timed-out"`, `type="agent"`. `base-broken` emits
  `type="repo"`.
- Transient retry. Once `blocked.md`'s mtime is 30 minutes old, a claim blocked with
  `publish-failed` is set back to running once (meta `transient_retry:KEY`). The retry sets only
  `state=running`; `started_at`, `phase` and `restarts` stay, so `done.json` still reads as
  fresh. It sets `publish_failures:KEY` to `PUBLISH_TRIES - 1`, so it makes exactly one more
  try. A try is the repo's full check and then a push. The retry runs in the poller next to
  `restart_timed_out`, with the same hold and `still_wanted` checks. It comments on the issue
  that it is retrying. `_block` emits `type="owner"` for `publish-failed` when
  `transient_retry:KEY` is already set. `_start` clears the key.
- Triage decides from `type`, and its prose list of causes goes:
  - `agent`: resume when it can say what the next attempt does differently. Otherwise tell the
    owner.
  - `owner`: tell the owner. `needs-owner` is the exception, as in the table.
  - `transient`: nothing. The service retries, and a second failure arrives typed `owner`.
  - `repo`: tell the owner.
  - `null`: decide by `kind`, as today. This covers `done` ends and events unacked at deploy.

  The amended-tests flow is not a cause and stays as it is. `run-summary` stays as the way to
  read what happened.

### `fix` alias

`cardinalgate.py` drops `fix` from the `resume` case, so the gate refuses `fix` as an unknown
verb. `control._CAPS` drops `cardinal_fix`, and `_start` drops it from its clear list. Before
the merge, every host's ledger is checked for `cardinal_fix:` keys. A claim that has one gets
`cardinal_resume:KEY` in the same migration. Tests and the book drop the alias.

## 2. Resume on comment

### Today

`forward_comments` reads new comments only for running claims, and it sends them to any
running session, a reviewer or rebase session included. A comment on a done claim is never
read unless the owner moves the issue to Todo. `release_claim` then hands the claim's
`last_comment_at` to the rerun as its cursor. The rerun is a new executor session on the kept
branch, then the check and a review. `rebase.step` already handles a done claim with an open
PR, behind its guards (`rebase.py`). Cleanup never removes a claimed worktree.

### The scan

A new poller step runs each tick after `poll`, so a Todo move in the same tick wins, and before
`rebase.step`. It takes every claim that meets all of these:

- it is `done`, has a PR URL and a worktree
- its `started_at` is within `RERUN_COMMENTS_S` (7 days)
- it is not held
- it has `comment_fixes:KEY < comment_fix_max`, checked before reading, so a claim at the cap
  is never read

For each one it reads comments since `claim.last_comment_at` with `comments_since`. It ignores
any comment created before meta `comment_fix_since`, which is set once on the feature's first
tick, so old "thanks" comments do not fire on deploy. It keeps only steering comments, by
`_steering`. A batch with no steering comment moves the cursor past it.

If steering comments remain, the scan skips this tick without moving the cursor when any of
these hold:

- the newest steering comment is under 5 minutes old (the owner may be about to move the issue
  to Todo)
- the running count is at `max_concurrent`, or usage is over the limit
- one of `rebase.step`'s gates fails: `still_wanted`, `main_hold`, `admit_rerun`
- the PR is not open (by `pr_view`)
- the PR's `headRefOid` differs from `git_head`; on this mismatch it comments once (meta) and
  waits

A skipped claim's comments stay unread. A Todo rerun then picks them up through
`release_claim`, as today.

### Launch

The ordering follows `_start_review_fix`. Before `launch` it does these:

- writes the steering comments, oldest first with their authors, as `FIX_CONTEXT`
- unlinks `OWNER_ITEMS`
- sets the claim to running, phase `fix`, `fix_attempts=0`, `started_at=now`, `restarts=0`
- sets meta `comment_fix:KEY` to `1`, adds 1 to `comment_fixes:KEY`, and moves the cursor

If `launch` raises, it restores all of these. It then comments on the issue that a fix session
started for the comment. A `LinearError` on that comment goes to the digest.

While the session runs, `forward_comments` steers further comments into it, as for any fix.
`forward_comments` now skips claims in phase `review` or with `rebase:KEY` set, without moving
the cursor. Comments sent while those sessions run are picked up by the scan once the claim is
done.

### Finish

`_finish_phase` reads `comment_fix:KEY` beside `review_fix`:

- Publishing uses `reuse_only=True`, which pushes to the open PR. No review follows.
- `--needs-owner` items go to `OWNER_ITEMS` instead of blocking.
- `--nothing-to-do` is accepted for a comment fix. It comments the reason and ends done.
- On `opened`, it posts "Comment fix pushed to <URL>" plus any owner items. When this was the
  `comment_fix_max`th fix, it adds "further comments need a Todo rerun". Then it ends done and
  clears `comment_fix:KEY`.
- The `pr_closed` line names a comment fix, not a review fix.

Check fix rounds apply as for any fix. A block during a comment fix is an ordinary typed stop.
`_start` clears `comment_fix:KEY` and `comment_fixes:KEY`. The Linear issue's state does not
change.

### Config and cost

`comment_fix_max` is an int in the config file, default 3, valid 0–10, where 0 turns the scan
off. It starts paid sessions, so it is in `settings.NOT_SETTABLE`.

The scan makes one `comments_since` call per qualifying done claim per tick. The 7-day window
and the open-PR condition keep that to a handful. `pr_view` and the gates run only when a
steering comment is found.

## Decisions taken without the owner

Each is the cheaper choice to be wrong about. The reason follows each one.

- `crash` is typed `agent`, not `transient`, because the service already retries a death once.
- Only `publish-failed` is transient, with one try after 30 minutes. That try reruns the full
  check, so it is one try, not three.
- `check-cap` covers every check-failed block at the last round, so the type comes from the
  cause alone.
- A comment fix gets no second review. It is the owner or an agent acting for them, and the
  existing review already ran. If this is wrong, a small change lands unreviewed on a PR the
  owner is reading anyway.
- The cap is 3 comment fixes per claim, which bounds a `steer_authors` agent that loops. Past
  the cap the comments stay unread, so a Todo rerun gets them.
- GitHub PR comments are not a trigger yet. Linear comments are where the owner steers.

## Testing

- `test_causes.py`:
  - every `_block` cause in the source and the timeout cause is in the table (grep the calls)
  - the types are the four allowed
  - `RESUMABLE` matches the table
- `test_events.py`: the `cause`/`type` columns, old rows read as null, the JSON line carries
  both, and `detail` is unchanged.
- `test_supervisor.py`:
  - both last-round check blocks write `check-cap` with `type="owner"`
  - the timeout event carries `timed-out`/`agent`
  - an unknown cause raises before the kill
  - a second `publish-failed` is typed `owner`
- `test_poller.py`, transient retry:
  - it fires at 30 minutes by mtime, not before, and only once
  - none for a held or unwanted issue
  - the counter is left at `PUBLISH_TRIES - 1`
  - `started_at` does not change
- `test_poller.py`, the scan:
  - one test per skip condition
  - a claim at the cap is not read
  - comments before `comment_fix_since` are ignored
  - non-steering and bot comments are ignored, and the cursor moves past them
  - a skip leaves the cursor
  - a launch failure restores the state
  - the `FIX_CONTEXT` content
  - the end-to-end flow: done → fix → `reuse_only` publish → done, with no review
  - `forward_comments` skips review and rebase sessions
- `test_supervisor.py`, the comment-fix finish:
  - owner items go to the PR comment
  - nothing-to-do ends done
  - the cap note on the last fix
- `test_settings.py`: `comment_fix_max` is not settable. `test_config.py`: its range.
- `test_cardinalgate.py`: `fix` is refused. `test_control.py`: `cardinal_fix` no longer caps.
- `test_cardinal_plugin.py`: triage decides on `type` and on `kind` for null, the prose cause
  list is gone, and the amend-tests rule stays.
