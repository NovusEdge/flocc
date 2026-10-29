# Executor core: archetype runs on one VM, an event-woken cardinal on another

Status: design, approved in conversation 2026-10-29 and revised after an Opus review the same day;
this file awaits the owner's review. It replaces the fixed tester → implementer → reviewer
pipeline, the escalation and ntfy path, A2A, and cardinal as built in 0.9–0.12.

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
- **No dialogs, no pings.** A run cannot stop on a dialog or a question, and nothing pushes to the
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
| Executor | Issue, the spec paths it names, acceptance criteria | Edit and commit in its worktree, run the repo's tools | Change the spec or plan, push, touch Linear | Commits made, repo check passes locally, summary written |
| Fixer | Existing branch plus the failed check's output, the review's findings, or cardinal's note | Same as executor | Same as executor | Check passes, or the summary lists what it could not fix under "Needs the owner" |
| Tester | Issue and spec | Write and commit tests | Write implementation code | Committed tests that fail now and pass on a correct implementation |
| Reviewer | PR diff, acceptance criteria, the repo's `AGENTS.md` and `CLAUDE.md` | Read | Edit, write, commit | Findings ending in `Verdict: approve` or `Verdict: changes requested` |

When a tester ran, the executor and fixer may amend a test that contradicts the spec through
`flocc tests-amended`, with a reason, as today. The publisher still rejects an amendment that
weakens a test.

Every archetype ends the same way: delete its scratchpad and any temp files it created, write its
summary (the reviewer writes its findings), run `flocc done`. `flocc done` replaces `review-done`.
An agent that needs the owner writes the need under "Needs the owner" in its summary and runs
`flocc done`; the run ends blocked.

### Keeping sessions moving

- The guard hook denies `AskUserQuestion`, `EnterPlanMode` and `ExitPlanMode`. Its deny reason
  tells the agent to pick the spec's default, or write "Needs the owner" and run `flocc done`.
- A Stop hook refuses to end the turn until `done.json` exists, with the same instruction. When
  `stop_hook_active` shows it already refused once in this turn, it lets the stop through and
  writes an `idle` marker; the supervisor ends that run as blocked on its next tick.
- The implementer skill's instructions to ask with `AskUserQuestion` and the escalate skill go.

## Sequence

The default sequence is `executor, reviewer`. A line in the issue description overrides it:

```
Run: tester, executor, reviewer
```

The line names archetypes in order, comma-separated, case-insensitive. `fixer` is never listed. The
service inserts fixers itself, up to `check_fix_rounds` (default 2) after a failed check and one
round after `changes requested`; cardinal may start one fixer on a blocked or timed-out run while
the check-fix rounds are not used up. An unknown archetype name makes the issue ineligible, and
cardinal gets a `bad-sequence` event for it.

When no tester ran, the claim records `tester_sha` as none, and the publisher skips the tester-file
guard instead of raising.

## Executor VM: the flocc service

The service does every mechanical step and makes no judgement calls.

### Two users

Sessions run in `bypassPermissions`, so the guard hook is a convenience, not a security boundary.
The boundary is a second Unix user, which supersedes d30's single user:

| User | Owns | Can read |
|---|---|---|
| `flocc` | The timer and tick, the ledger, the Linear key, the publish token, the plugin directory, the service's bare clones | Agent worktrees, through the group `flocc-work` |
| `flocc-agent` | The tmux server, Claude Code's login and `~/.claude`, the agent's read-only GitHub token, worktrees and scratch | Nothing of `flocc`'s; the plugin directory read-only |

- `flocc` reaches `flocc-agent` through one sudoers rule that allows only
  `/usr/local/libexec/flocc-agent-run`. That wrapper takes a fixed set of subcommands: start a
  session, send keys to a session, kill a session, run the repo's check in a worktree, prepare a
  worktree, report the Claude login state.
- Worktrees are clones owned by `flocc-agent`. The publisher fetches the branch from the agent's
  clone into the service's bare clone and pushes from there, so the publish token never enters the
  agent's environment. The repo's check, which runs untrusted code, runs as `flocc-agent`.
- An agent can still edit its own `~/.claude/settings.json` and hooks, which only weakens its own
  guard; it cannot reach the secrets, the ledger or the plugin directory.

### Keep, change, remove

| Area | Kept as is | Changed | Removed |
|---|---|---|---|
| Intake | Poller, Linear client, agent-block and acceptance-criteria parsing, comment steering into a live session | Poller parses `Run:`; starts the owner's queue by Linear priority and blocked-by under the tick lock, as today, except issues cardinal holds | flocc's own Linear labels |
| Sessions | tmux with Remote Control on every archetype | `bypassPermissions`, the guard and Stop hooks, run as `flocc-agent` | `permission.py`, `grants.py`, auto-mode tuning and `permissions.toml`, `press`, `peek`, `say` |
| Supervisor | Wall clock, `flocc done` pickup | One restart after a crash, from the issue, the branch and the last summary; `idle` marker ends the run blocked | Pane matching, dialog and stall handling, the 60% handoff restart |
| Publish | `publisher.py`, `rebase.py` (conflict resolution on an open PR), `tests-amended` | Optional tester guard; push from the service's bare clone; `AMENDED` moves out of `gate.py` into `publisher.py`; `rebase.py` takes `pr_url` from `publisher.py` instead of `a2atick` | `gate.py`, per-phase agent budgets |
| Health | `usage.py`, `admission.py`, `tokens.py` (GitHub token expiry, Claude login) | `canary.py` and `tokens.py` warnings become events instead of ntfy pages | `escalation.py`, ntfy, the service digest |
| Ledger | One claim per issue | Migration adds `events` and `holds`, drops path claims | Path claims, `flocc claim`, the pre-commit hook's claim check |
| A2A | | | `a2aclient.py`, `a2aconf.py`, `a2aserver.py`, `a2atick.py`, `mcp_a2a.py`, `inbox.py`, `a2a.toml`, `flocc-a2a.service` |

The commit-msg trailer hook stays. The catalogue agents in `plugin/agents/` stay as subagent types,
without the gate's budgets.

### Compaction

Sessions, cardinal's included, use Claude Code's own auto-compact and idle compaction at their
defaults. The launcher sets nothing, and `flocc doctor` checks for Claude Code 2.1.290 or later,
which idle compaction needs.

The handoff note at 60% context and the restart from it go: auto-compact keeps a long session
going in place, under the same Remote Control name. What carries between archetypes is the
end-of-run summary, which the next step in the sequence reads with the issue.

### Cleanup

| Who | When | Removes |
|---|---|---|
| Each archetype | Last step before `flocc done` | Its scratchpad and temp files it created |
| Service tick | The issue reaches Done or Canceled in Linear (a merged PR with `Fixes` moves it), or the owner releases it | The worktree and its state; the branch stays on GitHub |
| `flocc cleanup`, run by cardinal | Daily, with the digest | Worktrees with no claim, orphaned temp and scratch directories under `flocc-agent`, session state older than 7 days |

The agent never removes its worktree: the publisher fetches from it after `flocc done`, and a fixer
or reviewer reuses it. `flocc cleanup` never touches the worktree of a live claim.

### Linear status

The service moves an issue to In Progress when its first step starts, as today. A PR ready for the
owner, a blocked run and a timed-out run stay In Progress, with a comment on the issue saying which
and why. A release moves the issue back to Todo, and the next tick restarts it on the kept branch.

### Events

Every event has an ID and stays in the ledger until acknowledged.

| Event | When |
|---|---|
| `ended` | A sequence ended with no next step the service can start: PR ready for the owner, blocked, timed out |
| `cap` | A fix cap was reached |
| `base-broken` | A repo's base branch fails its check, once per break |
| `bad-sequence` | An issue's `Run:` line names an unknown archetype |
| `service-error` | A tick raised, Linear failed three ticks in a row, or a token or the Claude login is expiring or rejected |
| `daily` | The digest hour |

A failed check under the cap, fixer and reviewer starts, routine issue starts, session starts and
ticks emit nothing.

`flocc events --follow` prints, on connect, one line holding every unacknowledged event, then one
line per batch. A batch closes 2 minutes after its first event. An event unacknowledged 30 minutes
after it was printed is printed again. `flocc ack ID...` is idempotent.

## Cardinal VM

A small Incus VM on the same machine, separate from agent-run code, reaching the executor VM over
the Incus bridge by ssh and nothing else (d117). It holds one lingering tmux session with Remote
Control on Sonnet, started fresh daily at the renewal hour.

- **Waking.** Cardinal's plugin declares a plugin monitor with `when: "always"` that runs a loop:
  ssh to the executor VM and run `flocc events --follow`, reconnecting on exit. After 5 failed
  reconnects in a row it prints one `link-down` line, then stays silent until the link returns.
- **Acting.** On an event, cardinal reads the run with `flocc run-summary KEY`, which prints the
  summary and a log tail trimmed to a fixed size. It then starts one fixer with a note
  (`flocc fix KEY`, refused over the cap), holds or releases an issue (`flocc hold KEY`,
  `flocc release KEY`), or sends the owner one message saying what is needed. It acknowledges each
  event it handled.
- **Asking.** "What's running" and "what needs me" are answered from `flocc status`, a table the
  service builds.
- **Daily.** On `daily`, cardinal runs `flocc cleanup`, reads `flocc digest-data` and writes one
  digest message.
- **Access.** Its ssh key on the executor VM has a forced command in `authorized_keys`. The command
  reads `SSH_ORIGINAL_COMMAND`, splits it without a shell, allows only `events`, `ack`, `status`,
  `run-summary`, `fix`, `hold`, `release`, `cleanup` and `digest-data`, and checks every issue key
  against `^[A-Z]+-[0-9]+$`.

Authority: the owner's queue is what the poller admits today (assigned, `claude` label, Todo, repo
mapping, acceptance criteria). The service starts it; cardinal can hold any of it and can add one
fixer within the cap. Nothing else starts without the owner.

## Failures

| State | Service does | Reaches cardinal |
|---|---|---|
| PR opened or updated | Pushes, comments the PR link on the issue, starts the next step | `ended` when no step follows |
| Check failed | Starts a fixer under the cap | `cap` once the cap is used |
| Blocked: "Needs the owner", an `idle` marker, or the session died twice | Keeps the branch and claim, comments on the issue | `ended` |
| Wall clock reached | Kills the session, keeps the branch, comments on the issue | `ended` |
| Base branch broken | Holds the repo, starts nothing there | `base-broken` |
| Service failing | Keeps ticking | `service-error` |
| Cardinal down | Runs in progress finish, events wait in the ledger | Every unacknowledged event on reconnect |
| Link to the executor VM down | | `link-down` from cardinal's own monitor loop |

## Removed

| Area | Files |
|---|---|
| Cardinal 0.9–0.12 | `deploy/cardinal/`, `docs/book/cardinal.md`, `installer/cmd/flocc-setup/cardinal{,_test}.go`, `installer/internal/cardinal/`, `installer/internal/cli/cardinal{,_test}.go`, `installer/internal/dash/cardinal.go` and `testdata/cardinal-80x24.golden`, `installer/internal/steps/cardinal_{doc,files,host,login,root,status,user}{,_test}.go`, the cardinal branches of `rootpass` and `hosts`, the CARDINAL table in `status -H all` |
| A2A | `src/flocc/{a2aclient,a2aconf,a2aserver,a2atick,mcp_a2a,inbox}.py` and their tests, `deploy/flocc-a2a.service`, `installer/internal/cli/a2a{,_test}.go`, `installer/internal/steps/a2a{,_test}.go`, token minting in `flocc ship` |
| Escalation and ntfy | `src/flocc/escalation.py` and its test, `plugin/skills/escalate/`, `plugin/evals/escalate-*`, the ntfy topic in `flocc keys` |
| Permissions | `src/flocc/{permission,grants,gate}.py` and their tests, `installer/internal/steps/permissions{,_test}.go` |
| Coordination | `plugin/skills/coordinate/`, `plugin/skills/handoff/`, path claims and the claim check in the pre-commit hook |

The `cardinal` user on zer0-day-nimble goes through `flocc setup`'s existing old-pieces pass
(d126), not by hand.

## Testing

- Tests of kept modules stay; tests of removed modules go with them.
- New unit tests: `Run:` parsing; publishing with and without a tester; the guard hook's deny list,
  including `gh api` with `-f` or `-F` and no `-X`, which `gh` sends as POST; the Stop hook's
  refuse-once behaviour; event IDs, batching, redelivery on connect and after 30 minutes, and
  idempotent `ack`; the forced command rejecting anything outside its list and any malformed key;
  `flocc cleanup` never touching a live claim's worktree; `flocc-agent-run` rejecting unknown
  subcommands.
- A check that `flocc-agent` cannot read `flocc`'s secret files or write the plugin directory.
- One live run before merge: a test issue through executor, reviewer and PR, cardinal woken by its
  plugin monitor, the wake visible in the Claude app, and no phone notification for each new
  Remote Control session.

## Order of work

Each step is one PR into a feature branch.

0. Spike, thrown away afterwards: a plugin monitor with `when: "always"` in an idle Remote Control
   session, checking that it runs for hours, wakes the session on a line, shows in the Claude app,
   and what happens when its command exits. If it fails, this design stops and returns to the
   owner.
1. Drain: flocc-nimble stays stopped; release every live claim. Ledger migration for `events`,
   `holds` and dropping path claims.
2. Purge cardinal 0.9–0.12.
3. Remove A2A, escalation, ntfy, the broker and grants, the gate, path claims, and the escalate,
   coordinate and handoff skills.
4. Two users, `flocc-agent-run`, bypass mode with the guard and Stop hooks, smaller supervisor,
   optional tester guard, archetype cleanup step.
5. `Run:` sequence and the four archetype skills.
6. Events, `ack`, `run-summary`, `fix`, `hold`, `cleanup`, `digest-data`.
7. Cardinal VM, its plugin, monitor loop and forced command.
