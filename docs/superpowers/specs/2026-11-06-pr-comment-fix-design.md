# GitHub PR comments start a comment fix

Status: drafted 2026-11-06 after the owner asked for it, to be reviewed by Opus before
implementation. It builds on the comment-fix scan from cardinal autonomy part 2
(`poller.comment_fixes`, `supervisor.start_comment_fix`).

## Goal

A review comment on a flocc PR on GitHub starts the same fix session that a Linear comment on
the issue starts today. The fix runs on the same branch, pushes to the open PR, and gets no new
review. The owner can then review on GitHub, where the diff is, instead of copying the request
to Linear.

## Who counts

flocc posts its PR comments through the publish token, which is the owner's account
(`NovusEdge`). Those comments are flocc's review (`🤖 flocc review`), cardinal's `pr-comment`
(the `BOT_PREFIX`), and comments from the owner's other Claude sessions. CodeRabbit and
`linear-code[bot]` also comment on every PR. An author filter alone would therefore start fixes
from flocc's own review. The trigger needs all of these:

- The author's login is in a new config key, `github_steer_logins`, a list of strings. It
  defaults to `[]`, which turns the feature off. The key is in `settings.NOT_SETTABLE`, because a
  match starts paid sessions.
- The author's `type` is not `Bot`.
- The body, after leading whitespace, starts with the word `/flocc`, followed by whitespace
  (`/floccx` and `/flocc-x` do not count; a bare `/flocc` asks for nothing). The rest of the body is the
  request. A comment that does not address flocc is never read as one. This also keeps out
  flocc's own comments and stray comments from other sessions.

## Sources

There are three GitHub endpoints per PR, all read with the publish token through `gh api`:

| Source | Endpoint | Text handed to the session |
|---|---|---|
| Conversation comment | `issues/{n}/comments` | `<login> on the PR: <request>` |
| Inline review comment | `pulls/{n}/comments` | `<login> on <path>:<line>: <request>` (`line`, else `original_line`) |
| Review body | `pulls/{n}/reviews` | `<login> in a review: <request>` |

Every inline comment belongs to a review, a single one included (`pull_request_review_id`), and
it carries the time it was drafted, not the time its review was submitted. So the reader makes
two kinds of item:

- A conversation comment. Its key is `c:<id>` and its time is `created_at`.
- A submitted review. Its key is `r:<id>` and its time is the review's `submitted_at`. Reviews
  in state `PENDING` are skipped: the owner's token sees the owner's own unsubmitted draft.
  - If the review body has the prefix, the item holds the body and every inline comment of that
    review.
  - Otherwise it holds only the inline comments that have the prefix.
  - A review with neither, an empty approval body included, makes no item.

Only `created_at` and `submitted_at` count, never `updated_at`. Editing `/flocc` into a comment
from before `comment_fix_since` does nothing, because its time puts it before the cutoff. A later
comment without the prefix is never marked seen, so adding the prefix to it later does fire.
Paging uses `--paginate` with `per_page=100`. The reader is one function in `publisher.py`,
next to `pr_view`, and it returns `(key, time, text)` items oldest first.

`build_runtime` (tick.py) adds `rt.pr_comments` next to `rt.pr_view`, closed over the publish
token, so tests can replace it. With `github_steer_logins` empty, the scan never calls it.

Agent sessions get `agent_gh_token_file`, which may also be the owner's account. To keep an
agent from starting the next fix:

- A body that contains `BOT_PREFIX` anywhere is dropped.
- The comment-fix and executor prompts say not to post PR comments.
- `comment_fix_max` bounds whatever gets past both.

## The scan

`comment_fixes` reads both sources for each qualifying claim. Qualifying is unchanged: done,
open PR, within 7 days, not at `comment_fix_max`, and gated by the existing guards.

- There is no time cursor. Meta `gh_comments_seen:KEY` holds a JSON list of the item keys
  already handled, matched or not. Same-second items and late-submitted drafts are not lost.
  Items timed before `comment_fix_since` are marked seen and ignored.
- The GitHub read runs after the cap check and before the settle check. A no-match result marks
  its items seen on its own path, not on the Linear `unread[-1]` path. A Linear `LinearError`
  skips only the Linear half, so a GitHub-only batch can still start.
- Linear steering comments and GitHub items merge into one batch, sorted by time. One batch
  starts at most one fix session, and it counts once against `comment_fix_max`.
- The 5-minute settle applies to the newest item from either source.
- `start_comment_fix` takes a new `gh_seen: list[str] | None` argument. Like the other
  pre-launch writes, it writes `gh_comments_seen:KEY` and a meta flag `comment_fix_gh:KEY`
  before the launch, and restores both if the launch raises. The poller never writes them after
  the call.
- A GitHub read failure (a `CalledProcessError`, or bad JSON) is printed and skips the GitHub
  half for this tick, with nothing marked seen. One PR's failure must not stop the scan.
- `start_comment_fix`'s preamble changes from "Comments on the issue since its PR opened" to
  "Comments on the issue and the PR since its PR opened".

Only done claims are read. A `/flocc` comment posted while a session runs waits until the claim
is done, as Linear comments do during review and rebase. Forwarding into live sessions is out
of scope.

`_start` keeps `gh_comments_seen:KEY` and clears `comment_fix_gh:KEY`. A Todo rerun hands the
Linear comments to the new session, but it does not read GitHub comments, so clearing the seen
list would let old `/flocc` comments fire again after the rerun. The 7-day window still runs
from the rerun's `started_at`.

## Reply

On `opened`, the existing Linear comment ("Comment fix pushed to <URL>") stays. When
`comment_fix_gh:KEY` is set, `_finish_phase` also posts the same line on the PR through the
existing `rt.comment_pr` hook, prefixed with `BOT_PREFIX`, so the reviewer sees it where they
asked. A failure there is printed and does not fail the finish. `_clear_comment_fix` clears the
flag.

## Cost

When the key is set, each tick makes three `gh api` calls per qualifying claim. Pages count as
more calls. The claims are
done, have an open PR and are under 7 days old, which is a handful. At 20 ticks an hour that is
about 60 calls an hour per claim, well inside GitHub's 5,000 an hour.

## Decisions taken without the owner

- The `/flocc` prefix is required. It is the cheapest guard against flocc reading its own
  comments, and it costs the reviewer five characters.
- The login list defaults to empty, not to the owner, so nothing changes until the owner sets
  it.
- No forwarding into running sessions, which keeps one path.

## Testing

- `test_publisher.py`, the reader:
  - maps the three endpoints into the two kinds of item
  - skips bots, logins that are not listed, and bodies that contain `BOT_PREFIX`
  - keeps only bodies that start with the word `/flocc`, rejects `/floccx` and a bare `/flocc`, and
    strips the prefix
  - skips `PENDING` reviews and empty review bodies
  - an inline comment drafted before a later item, but submitted after it, is timed by its
    review's `submitted_at`
  - a `/flocc` review body brings in every inline comment of its review
  - an edited old comment keeps its `created_at`
  - items come back oldest first
- `test_poller.py`:
  - with the key empty, no `pr_comments` call happens
  - a GitHub item alone starts a fix, and its text is in `FIX_CONTEXT`
  - a Linear item and a GitHub item make one session and count once
  - a GitHub-only batch starts even when the Linear read fails
  - the settle uses the newest item from either source
  - a GitHub read failure leaves the Linear half working and marks nothing seen
  - items before `comment_fix_since` are ignored
  - two same-second items are both handled, and a seen item is never handled twice
  - a launch failure restores the seen list and the flag
  - after a fix restarts the claim, a later `/flocc` comment is still scanned
  - after a Todo rerun, an old `/flocc` comment does not fire again
- `test_supervisor.py`: the PR reply is posted only when the batch held a GitHub item, and its
  failure does not fail the finish.
- `test_config.py` and `test_settings.py`: the list type, and that the key is not settable.
