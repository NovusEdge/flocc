# Cardinal autonomy, part 2: typed stops, resume on comment, no `fix` alias

Status: drafted 2026-11-06 from part 1's Roadmap; awaiting the owner's review.

## Goal

Part 1 gave cardinal a capped `resume` and wider reach, but it still learns why a run stopped
by reading `blocked.md` prose, and an owner comment on a finished run with an open PR does
nothing until the owner moves the issue back to Todo. This part:

1. Gives every stop a cause and a type in code. Events carry both, the service retries
   transient stops itself, and cardinal decides from the type instead of from prose.
2. Starts a fix session on the same branch when the owner or a `steer_authors` id comments on
   a done issue whose PR is still open.
3. Removes the gate's `fix` alias and the legacy `cardinal_fix` cap key. Both machines run
   part 1 since 2026-11-06.

Two PRs: (1) typed stops with the alias removal, (2) resume on comment. They touch different
code paths and can be built in parallel.

Out of scope: GitHub PR comments as a trigger, `file` in `on` mode (it waits on observe-mode
drafts), and any new cardinal verb.

## 1. Typed stops

### Today

`_block` (`supervisor.py`) writes the cause as the first line of `blocked.md`, and the event
detail is `"{cause}: {summary}"` cut to 300 characters. `control.RESUMABLE` lists the causes
`resume` accepts. A check failure at the fix cap and one with rounds left share the cause
`check-failed`, so `resume` tells them apart only by `fix_attempts`. Triage names the causes
in prose and reads `run-summary` to find which one it has. A timeout writes no `blocked.md`.
Its cause is derived from the claim state.

### Cause table

A new module, `src/flocc/causes.py`, holds one table: cause → type. `RESUMABLE` becomes the
causes of type `agent`, plus `needs-owner`. Every `_block` call and the timeout path look
their cause up in the table. An unknown cause raises, so a new stop cannot ship untyped.

| Cause | Type | Who acts |
|---|---|---|
| `publish-failed` | transient | The service retries once (below), then cardinal tells the owner |
| `crash` | agent | cardinal may resume |
| `idle` | agent | cardinal may resume |
| `timed-out` | agent | cardinal may resume. The service's own restart of a timeout with commits is unchanged |
| `check-failed` | agent | cardinal may resume. Only with fix rounds left |
| `no-changes` | agent | cardinal may resume |
| `needs-owner` | owner | cardinal resumes only when the issue text settles every question. Otherwise it goes to the owner |
| `check-cap` | owner | The owner. This is new: the check still fails after every fix round |
| `tests-touched` | owner | The owner (release or `--accept-tests`) |
| `no-tests` | owner | The owner |
| `branch-moved` | owner | The owner |
| `rebase-conflict` | owner | The owner |
| `base-broken` | repo | The service holds the repo, as it does today. The owner fixes the base |

`crash` is `agent`, not `transient`. The service already relaunches a dead session once
before it blocks, so a crash block is the second death.

### Changes

- `check-cap` replaces `check-failed` on the cap path (the `cap` event). `resume` still checks
  `fix_attempts` against the cap as a second guard. A claim blocked before this change keeps
  `check-failed` on its first line, and that guard still refuses it.
- The `events` table gains `cause` and `type` columns, nullable for old rows and for kinds
  that are not stops. `emit` takes them. The JSON line carries `"cause"` and `"type"` next to
  `"kind"`, and `detail` keeps the summary.
- The timeout path emits with `cause="timed-out"`. `base-broken` emits with `type="repo"`.
  `needs-owner` and `cap` events carry their cause like any other stop.
- Transient retry: a claim blocked with `publish-failed` for 30 minutes or more is set back to
  running once (meta `transient_retry:KEY`), with its publish-failure counter at zero.
  `done.json` is still there, so the next tick publishes again, three tries as before. A
  second failure blocks for good. The retry runs in the poller next to `restart_timed_out`,
  under the same hold and wanted-issue checks, and comments on the issue that it is retrying.
  `_start` clears the key.
- Triage reads `type` and `cause` from the event and drops its prose list of causes:
  - `agent`: resume when it can say what the next attempt does differently, else tell the owner.
  - `owner`: tell the owner, except `needs-owner` as in the table.
  - `transient`: nothing. The service retries, and a second failure arrives as a new event.
  - `repo`: tell the owner.

  `run-summary` stays the way to read what happened. It no longer decides which way to go.

### `fix` alias

`cardinalgate.py` drops `fix` from the `resume` case, and the gate refuses `fix` as an unknown
verb. `control._CAPS` drops `cardinal_fix`. Before the merge, the plan checks that no claim on
flocc-nimble has `cardinal_fix:KEY` set. If one has, the plan sets `cardinal_resume:KEY` for
it in the same migration. Tests and the book drop the alias.

## 2. Resume on comment

### Today

`forward_comments` reads new comments only for running claims. A comment on a done claim is
never read unless the owner moves the issue to Todo, which starts a fresh claim with the
comments as context. A fresh claim reruns the tester and the executor from the base.

### Behaviour

Each tick, for every claim that meets all of these:

- it is `done` and has a PR URL
- it finished within the last 7 days (`RERUN_COMMENTS_S`)
- it is not held

the poller reads comments since `claim.last_comment_at` with the existing `comments_since`. It
keeps only steering comments, using the same `_steering` test (owner, or a `steer_authors` id;
never a `🤖 flocc:` comment). If any remain:

1. It skips the claim this tick, without moving the cursor, when any of these hold: the running
   count is at `max_concurrent`, usage is over the limit, or the issue is held.
2. It asks GitHub whether the PR is open (`pr_view`). It skips a merged or closed PR and moves
   the cursor past those comments.
3. It refuses at the cap. That is `comment_fix_max` comment fixes per claim, a new config key,
   default 3, range 0–10, where 0 turns the feature off. At the cap it posts one comment ("this
   PR has had N comment fixes; move the issue to Todo for a fresh run") and moves the cursor.
4. Otherwise it writes the comments, oldest first with their authors, as `FIX_CONTEXT`. It sets
   the claim to running, phase `fix`, `fix_attempts` to 0, and adds 1 to meta
   `comment_fixes:KEY`. It launches the fix phase on the kept branch, comments on the issue
   that a fix session started for the comment, and moves the cursor.

The fix phase ends the way a review fix does. The check runs, and fix rounds apply. Publishing
uses `reuse_only=True`, so it pushes to the open PR. Then the claim ends done, with no second
review. The comment is the owner reviewing. While the session runs, a further comment is
steered into it by `forward_comments`, as for any running claim.

`_start` clears `comment_fixes:KEY`. The Linear issue's state is not changed.

The cleanup pass may already have removed a done claim's worktree. The fix launch then
recreates it from the pushed branch, `origin/<branch>`. A PR that is open has that branch.

### Cost

The work is one `comments_since` call per qualifying done claim per tick. That is a handful of
claims, because the 7-day window and open PRs bound it. `pr_view` runs only when a steering
comment is found.

## Decisions taken without the owner

Each is the cheaper choice to be wrong about. The reason follows each one.

- `crash` is typed `agent`, not `transient`, because the service already retries a death once.
- Only `publish-failed` is transient. It gets one retry after 30 minutes, which is enough for a
  GitHub outage. Each retry costs no session, only a push.
- `check-cap` is split out of `check-failed`, so the type comes from the cause alone.
- A comment fix gets no second review. It is the owner or an agent acting for them, and the
  existing review already ran. If this is wrong, a small change lands unreviewed on a PR the
  owner is reading anyway.
- The cap is 3 comment fixes per claim, which bounds a `steer_authors` agent that loops. The
  owner can still rerun from Todo.
- GitHub PR comments are not a trigger yet. Linear comments are the place the owner steers.

## Testing

- `test_causes.py`: every `_block` and timeout cause in the source is in the table (grep the
  calls), the types are the four allowed, and `RESUMABLE` matches the table.
- `test_events.py`: `cause`/`type` columns, old rows read as null, and the JSON line carries
  both.
- `test_supervisor.py`: the cap path writes `check-cap` and emits `cap` with `type="owner"`;
  the timeout event carries `timed-out`/`agent`.
- `test_poller.py`, transient retry: a retry after 30 min, none before, once only, none
  for a held issue, and the counter reset.
- `test_poller.py`, resume on comment, one test per bullet above: running count at
  `max_concurrent`, over usage, held, closed PR, at the cap, non-steering and bot comments
  ignored, the window, the cursor, and the `FIX_CONTEXT` content. It also covers the end-to-end
  flow: done → fix → reuse_only publish → done with no review.
- `test_cardinalgate.py`: `fix` refused. `test_control.py`: `cardinal_fix` no longer caps.
- `test_cardinal_plugin.py`: triage decides on `type`, and the prose cause list is gone.
