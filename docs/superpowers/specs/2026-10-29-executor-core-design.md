# Executor core: archetype runs on one VM, an event-woken cardinal on another

Status: design, approved in conversation 2026-10-29; this file awaits the owner's review. It
replaces the fixed tester → implementer → reviewer pipeline, the escalation and ntfy path, A2A, and
cardinal as built in 0.9–0.12.

## What the owner asked for

The owner's words, 2026-10-29:

> "it feels like we've kinda handrolled a but too much ... there's just way too much like
> babysitting that I need to do, which was kind of the opposite of what I wanted"

> "I specifically wanted executors and not necessarily, you know, like agents that also pick up
> like planning ... that's the stuff that I do"

> "the whole like escalating stuff and the NTFY shit and whatnot, like, it's a bit all over the
> place right now"

> "usually a PR is a multi-step thing i want a remote control view too"

> "we should ideally have something like the cardinal system but i think the one we have atm needs
> to be purged completely"

> "I want the cardinal to be efficient with it's operations ... not have like weirdass polling ...
> it uses the native monitor tool proper instead of constant messages being sent to it because that
> eats up usage"

> "we want autocompact and compaction on idle to all agents, with each agent also handling cleanup
> ... (oh and a cleanup pass every day via the cardinal)"

From those, the system must deliver:

- **Execution only.** The owner writes specs, plans and the sequence. Agents carry them out and
  never plan, brainstorm or ask for design sign-off.
- **No dialogs, no pings.** A run cannot stop on a permission dialog, and nothing pushes to the
  owner's phone. The owner hears from one place: cardinal, in the Claude app.
- **Watchable runs.** Every session stays an interactive Claude Code session with Remote Control.
- **Cheap supervision.** Cardinal costs nothing while nothing needs a decision.

Tools considered instead of cutting flocc down (checked from source on 2026-10-29): Cyrus needs a
Linear OAuth app and a public webhook URL; no_human runs through the Agent SDK, outside Remote
Control; Paradev (formerly Superset) dispatches through its cloud relay and gates Linear
automations behind a paid plan; Linear's own agent bills Linear AI credits. None gives Remote
Control plus Linear intake on the owner's own login.

## Archetypes

Each archetype is a skill in the agent plugin. The service picks one per session from the issue's
sequence.

| Archetype | Input | Can do | Must not | Done when |
|---|---|---|---|---|
| Executor | Issue, the spec paths it names, acceptance criteria | Edit and commit in its worktree, run the repo's tools | Change the spec or plan, edit the tester's committed files, push, touch Linear | Commits made, repo check passes locally, summary written |
| Fixer | Existing branch plus the failed check's output or the review's findings | Same as executor | Same as executor | Check passes, or the summary lists what it could not fix under "Needs the owner" |
| Tester | Issue and spec | Write and commit tests | Write implementation code | Committed tests that fail now and pass on a correct implementation |
| Reviewer | PR diff, acceptance criteria, the repo's `AGENTS.md` and `CLAUDE.md` | Read only | Edit, commit | Findings ending in `Verdict: approve` or `Verdict: changes requested` |

Every archetype ends the same way: delete its scratchpad and any temp files it created, write its
summary, run `flocc done`. An interactive session does not exit on its own, so `flocc done` stays
the finish signal.

## Sequence

The default sequence is `executor, reviewer`. A line in the issue description overrides it:

```
Run: tester, executor, reviewer
```

The line names archetypes in order, comma-separated, case-insensitive. `fixer` is never listed: the
service inserts fixers itself, up to `check_fix_rounds` (default 2) after a failed check and one
round after `changes requested`. An unknown archetype name makes the issue ineligible, and cardinal
gets a `bad-sequence` event for it.

## Executor VM: the flocc service

The service does every mechanical step and makes no judgement calls.

| Keep | Change | Remove |
|---|---|---|
| Poller, Linear client, agent-block and acceptance-criteria parsing | Poller parses `Run:`; it lists eligible issues but starts none (cardinal does) | `escalation.py`, ntfy, flocc's own Linear labels, the service digest |
| `worktree.py`, `./setup` | Sessions start with `--permission-mode bypassPermissions` and one PreToolUse guard hook | `permission.py`, `grants.py`, auto-mode tuning, `press`, `peek`, `say` |
| tmux sessions with Remote Control | Supervisor keeps the wall clock, `flocc done` pickup, and one restart after a crash from the issue and the branch | Supervisor pane matching, dialog handling, the 60% handoff restart |
| `publisher.py`: check, push, draft PR, review comment | The fixed pipeline becomes the sequence above | `gate.py` and per-phase agent budgets |
| `usage.py`, `admission.py` thresholds | `canary.py` (main-broken) emits an event instead of only holding the repo | `a2a*.py`, `mcp_a2a.py`, `inbox.py`, `tokens.py`, `a2a.toml`, `flocc-a2a.service` |
| Ledger, one claim per issue | New: `events.py`, the `flocc events`, `start`, `fix`, `cleanup` and `digest-data` commands | Path claims, `flocc claim`, the pre-commit hook |

The guard hook denies, without asking: `git push`, `gh pr`, `gh issue`, `gh api` with a write
method, any write outside the session's worktree and its own state and scratch directories, and,
for the executor and fixer, edits to files the tester committed. The service checks the tester's
files again at publish time, as today. Bypass mode is acceptable because sessions run in a
disposable VM whose tailnet tag reaches nothing.

### Compaction

Sessions, cardinal's included, use Claude Code's own auto-compact and idle compaction at their
defaults. The launcher sets nothing, and `flocc doctor` checks for Claude Code 2.1.290 or later,
which idle compaction needs.

The handoff note at 60% context and the restart from it go: auto-compact keeps a long session
going in place, under the same Remote Control name. What carries between archetypes is the end-of-run
summary, which the next step in the sequence reads with the issue.

### Cleanup

| Who | When | Removes |
|---|---|---|
| Each archetype | Last step before `flocc done` | Its scratchpad and temp files it created |
| Service | PR merged or closed, or the issue released | The worktree; the branch stays on GitHub |
| `flocc cleanup`, run by cardinal | Daily, with the digest | Worktrees with no live claim, orphaned temp and scratch directories, session state older than 7 days |

The agent never removes its worktree: the publisher pushes from it after `flocc done`, and a fixer
or reviewer reuses it.

### Events

`flocc events --follow` prints one line per batch and never exits. Batches close 2 minutes after
their first event. It emits only what needs a decision:

| Event | When |
|---|---|
| `queued` | Eligible issues appeared or capacity freed |
| `ended` | A sequence step ended with no next step the service can start: PR ready for the owner, blocked, timed out |
| `cap` | A fix cap was reached |
| `base-broken` | A repo's base branch fails its check, once per break |
| `bad-sequence` | An issue's `Run:` line names an unknown archetype |
| `daily` | The digest hour |

A failed check under the cap, a fixer start, a reviewer start after a PR, session starts and ticks
emit nothing. Events persist in the ledger until cardinal acknowledges them with `flocc ack`, so an
event raised while cardinal is down is delivered when it reconnects.

## Cardinal VM

A small Incus VM on the same machine, separate from agent-run code. It holds one lingering tmux
session with Remote Control on Sonnet, started fresh daily at the renewal hour.

- **Waking.** Cardinal's plugin declares a plugin monitor with `when: "always"` that runs
  `flocc events --follow` over ssh. A plugin monitor runs for the whole session; a monitor started
  from a turn has a 30-minute deadline and would need re-arming, so it is not used. The command
  reconnects ssh in a loop and never exits.
- **Acting.** On an event, cardinal reads that run's summary and log tail from the service, both
  trimmed to a fixed size, then starts the next queued issue (`flocc start`), starts one more fixer
  (`flocc fix`, refused over the cap), or sends the owner one message saying what is needed.
- **Asking.** "What's running" and "what needs me" are answered from `flocc status`, a table the
  service builds.
- **Daily.** On `daily`, cardinal runs `flocc cleanup`, reads `flocc digest-data` and writes one
  digest message.
- **Access.** Its ssh key on the executor VM has a forced command in `authorized_keys` that runs
  only `flocc events`, `status`, `start`, `fix`, `ack`, `cleanup` and `digest-data`.

Authority: cardinal starts only issues in the owner's queue (assigned, `claude` label, Todo, repo
mapping, acceptance criteria, as today) and fixers within the cap. Everything else waits for the
owner.

A lazier variant exists: the service starts queued issues itself by Linear priority and capacity,
and cardinal sees only `ended`, `cap` and `base-broken`. It was not chosen because the owner wants
cardinal as dispatcher.

## Failures

| State | Service does | Reaches cardinal |
|---|---|---|
| PR opened or updated | Pushes, comments the PR link on the issue, starts the next step | Only as `ended` when no step follows |
| Check failed | Starts a fixer under the cap | As `cap` once the cap is used |
| Blocked (summary lists "Needs the owner", or the session died twice) | Keeps the branch and claim | `ended` |
| Wall clock reached | Kills the session, keeps the branch | `ended` |
| Base branch broken | Holds the repo, starts nothing there | `base-broken` |
| Cardinal down | Runs in progress finish, events queue in the ledger | On reconnect |

## Removed

- Cardinal 0.9–0.12: `installer/internal/cardinal`, `deploy/cardinal`, `steps/cardinal_user.go`,
  the cardinal steps in `flocc setup`, the CARDINAL table in `status -H all`, `docs/book/cardinal.md`.
  The `cardinal` user on zer0-day-nimble is deleted by hand with the owner's sudo.
- A2A: modules above, the a2a token minting in `flocc ship`, the tailnet peer URLs.
- Escalation and ntfy: the escalation rubric skill, the ntfy topic in `flocc keys`.
- The tester-file guard stays; path claims and the pre-commit hook go.

## Testing

- Tests of kept modules stay; tests of removed modules go with them.
- New unit tests: `Run:` parsing, the event filter and batching, `flocc ack` redelivery, the guard
  hook's deny list, the forced command rejecting anything outside its list, `flocc cleanup` never
  touching a live claim's worktree.
- One live run before merge: a test issue through executor, reviewer and PR, cardinal woken by its
  plugin monitor, and the wake visible in the Claude app.

## Order of work

Each step is one PR into a feature branch.

1. Purge cardinal 0.9–0.12.
2. Remove A2A, escalation, ntfy, broker and grants, the gate, path claims and the pre-commit hook.
3. Bypass mode with the guard hook, smaller supervisor without the handoff restart, archetype cleanup step.
4. `Run:` sequence and the four archetype skills.
5. Events, `ack`, `start`, `fix`, `cleanup`, `digest-data`.
6. Cardinal VM, its plugin and monitor.

## Not documented, checked in the live run

- What a plugin monitor does when its command exits. The command is written never to exit.
- Whether monitor wakes appear in the Claude app under Remote Control.
