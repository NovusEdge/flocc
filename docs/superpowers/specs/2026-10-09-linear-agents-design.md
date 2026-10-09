# linear-agents design

Status: draft for review, 2026-10-09. Supersedes the "Recommended" section of the README, which
assumed headless `claude -p` workers in a container.

## Goal

Claude Code picks up Linear issues that are assigned to me and labelled `claude`, works on each
in its own git worktree, and opens a draft PR. I can watch and steer any running session from the
Claude app, agents working in the same repo coordinate instead of colliding, and only things that
need me reach my phone.

It is a personal tool on one VM, not team infrastructure.

### Success criteria

- An issue with acceptance criteria goes from Linear to a draft PR with no action from me.
- Every running session is visible and steerable in the Claude app.
- Two agents never work the same issue or edit the same claimed paths at once.
- An issue that depends on another does not start until that one has landed.
- I get at most one push per cause, and every push names one thing I can do.
- Agents stop starting new work before the subscription limits run low.

### Out of scope for now

- Splitting a large issue into disjoint tasks.
- Docker containers per session.
- Integrations beyond Linear, GitHub and ntfy (calendar free/busy, claude.ai connectors).
- Agents judging whether their task fits the remaining usage.
- Codex or any CLI other than Claude Code.
- Running agents as a separate Unix user, so the Linear key and GitHub write token are out of
  their reach. Revisit before running anywhere an agent reading either would matter.

## Constraints

- Agents are interactive Claude Code sessions on my claude.ai subscription. No Anthropic API key,
  no Agent SDK. The launcher strips `ANTHROPIC_API_KEY`, which would switch billing to the API.
- Remote Control attaches only to interactive sessions, and `--max-turns` and `--max-budget-usd`
  work only in print mode. Limits on a session are therefore enforced by the supervisor.
- Running my subscription from a script on Nimble Fox repos needs the company's agreement first;
  see the README.
- Linear access for the service is a personal API key: 2,500 requests an hour, and no
  notification to me for actions taken with my own key.

## Architecture

One Python program on a systemd timer, holding an `flock` so two runs never overlap, plus one
interactive Claude Code session per issue in tmux on the same VM. The tmux server runs under its
own lingering systemd user service, so sessions outlive each run of the program.

| Part | Owns |
|---|---|
| Poller | Finding ready issues, admission checks, moving Linear status |
| Launcher | Worktree, `./setup`, pre-commit hook, starting a session in tmux with a clean environment |
| Supervisor | Restarting sessions from their handoff note, wall-clock kills, cleaning up crashed issues |
| Publisher | `just check`, the test-file guard, push, draft PR, Linear comment |
| Notifier | Escalation routing, dedupe, ntfy pushes, Linear labels, the daily digest |
| Claims ledger | SQLite: issue, session, claimed paths, dependencies, state |
| Pre-commit hook | Rejects commits touching paths another live issue has claimed |
| Agent plugin | Tester and implementer skills, escalation rubric, coordination rules, handoff hooks |

Agents write code, commit in their worktree, message peers, and write escalations to a file.
They never push, open PRs, or change Linear.

### What Claude Code already provides

| Need | Provided by |
|---|---|
| Live steering | Remote Control (`claude --remote-control <name> "<prompt>"`) |
| Agent-to-agent messages | `ListAgents` and `SendMessage` between local sessions |
| Waiting out a usage limit | Interactive sessions continue on their own after the reset |
| Usage and context numbers | The status line's JSON input (`rate_limits`, context use) |
| Pushes for a session's own questions | The Claude app, "Push when actions required" on, "Push when Claude decides" off |
| Per-issue tools | `--mcp-config` with `--strict-mcp-config` |

## Issue flow

1. **Pick.** Issues assigned to me, labelled `claude`, not started, with acceptance criteria.
   An issue without criteria gets one comment and is skipped until it changes.
2. **Admit.** Start only if usage is below the thresholds, fewer than the session cap are running,
   the issue's claimed paths do not overlap a live claim, and its `depends_on` issues are done.
3. **Prepare.** `git worktree add` (reusing the branch of an earlier run of the issue), run
   `./setup`, install the pre-commit hook. Then write the claim and move the issue to In Progress.
4. **Tester session.** Writes tests that pass only on correct behavior, commits them, writes a
   handoff note, exits.
5. **Implementer session.** A fresh session works from the issue and the handoff note until the
   tests pass. Past 60% context it writes a handoff note and the supervisor restarts it under the
   same Remote Control name.
6. **Publish.** Reject the run if the implementer changed files the tester committed; `just check`;
   push; `gh pr create --draft` with `Fixes NF-123`, or reuse the open PR on a retry; comment the PR
   link on the issue; release the claim.

The session name is the issue key (`NF-123`); the tester and implementer use `NF-123-test` and
`NF-123`.

### Failure handling

| Failure | Response |
|---|---|
| Session process gone while its claim is live (crash, reboot) | Restart once from the latest handoff note; on a second failure, `needs-decision` and a tier 2 escalation |
| Wall-clock limit reached | Kill the session, keep the branch, report in the digest |
| `just check` fails | One fresh implementer session with the failure output; on a second failure, `needs-decision` |
| Usage limit hit mid-run | The session waits for the reset; nothing new is admitted until usage is below the thresholds |
| Agent needs a decision | It escalates with a default and a deadline and continues on the default, unless the decision is irreversible, in which case it waits |
| A Linear, git or tmux call fails partway through a step | The claim's state changes only after the step's side effects succeed, so the next run repeats the step |

A blocked or timed-out issue keeps its claim until I run `linear-agents release NF-123`, which
moves it back to Todo; the next run picks it up again and continues on the kept branch.

## Configuration

Later layers override earlier ones.

| Layer | Location | Holds |
|---|---|---|
| Defaults | `~/.config/linear-agents/config.toml` | Session cap, usage thresholds (60% of 5-hour, 50% of weekly), label or project to repo map, ntfy topic, digest time. The poll interval (3 min) is the systemd timer's `OnUnitActiveSec`, not a config value. |
| Per issue | An `agent` fenced block in the issue description | `model`, `effort`, `wall_clock`, `paths`, `depends_on`, `mcp` |
| Live | Remote Control | Anything said to the running session |

```agent
model: opus
effort: high
wall_clock: 2h
paths: [src/billing/**]
depends_on: [NF-120]
mcp: [sentry]
```

An issue with no `paths` claims nothing up front. Its agent claims paths as it learns them by
running `linear-agents claim`, which records them in the ledger, and the pre-commit hook enforces
whatever is claimed at commit time.

## Coordination

- Duplicate work: an issue has at most one live claim; the poller skips claimed issues.
- Overlapping edits: claimed paths are checked at admission and again by the pre-commit hook. A
  file is claimed when it or a directory above it matches a claimed glob.
- Dependencies: `depends_on` holds an issue until the issues it names are done in Linear. An issue
  whose `depends_on` cannot be read is held with one comment.
- Discoveries: agents message peers with `SendMessage`. `ListAgents` shows auto-generated names and
  every Claude session on the machine, so `linear-agents context` lists each agent's current peers
  in the same repo from the ledger, and the `coordinate` skill limits messages to those names.

## Escalation

| Tier | Examples | Linear record | Push |
|---|---|---|---|
| 1. Interrupt now | Imminent irreversible harm, a secret about to be pushed, a failure blocking every run | `escalation` label; one shared `escalation` issue per cross-cutting cause | ntfy, high priority |
| 2. Interrupt if idle | Irreversible decision blocking this agent, a conflict agents cannot settle | `needs-decision` label and a comment with question, default and deadline | ntfy, normal priority, only when the session would otherwise sit idle |
| 3. Digest | Reversible decisions made alone, draft PRs ready, failed or capped runs | One daily comment on a pinned digest issue | None |
| 4. Log only | Retries that succeeded, lint-level noise | None | None |

Rules: every push names one action; one push per cause in 24 hours, while the label and comment
still land on each issue; an agent's cause is scoped to its issue unless it marks the cause as
shared across issues; silence means the stated default applies at its deadline. I answer a tier 2
question in a Linear comment, which the poller forwards to the session, or directly through Remote
Control. Only my own comments are forwarded, and only those written after the issue started.

## Usage limits

Each session's status line command writes its `rate_limits` and context numbers to its state
directory. The poller reads the newest values and admits no new issue at or above the thresholds.
Extra usage stays off. The usage at the start and end of each phase is recorded per issue, so a
later version can estimate whether an issue fits the remaining budget.

## Handoff and compaction

A session's state lives outside its context: commits in the worktree, a handoff note in its state
directory, and the Linear issue. Auto-compaction handles ordinary growth. Past 60% context the
supervisor types an instruction into the session asking for a handoff note, waits for the agent to
signal that the note is written, and restarts the session fresh from it. A hook cannot write the
note itself, because only the model knows what it was in the middle of. The `SessionStart` hook
loads the issue, the peer list and the latest handoff note, so they come back after a restart or a
compaction.

An interactive session does not exit when it finishes; it waits for input. Each phase therefore
ends with the agent running `linear-agents done`, which the supervisor picks up. Each launch
deletes the previous session's markers and status file, so the new session is never mistaken for
one that has already finished or filled its context.

## Access

| Holder | Linear | GitHub | MCP |
|---|---|---|---|
| Service | Personal API key, limited to the NF team | Fine-grained token with write to the mapped repos | None |
| Agents | None; the issue text arrives in the prompt | Read-only fine-grained token for `gh` | Only servers named in the issue's `mcp` list, via `--strict-mcp-config` |

The machine is trusted, and agents run as my own Unix user, so they could read the Linear key and
the write token. The agent column is therefore behavioural, kept by the prompt, the skills, the
pre-commit hook and a read-only token in `GH_TOKEN`; it is not an enforced boundary.

The launcher starts each session through `env -i` with only `HOME`, `USER`, `PATH`, `TERM`,
`LANG` and the `LINEAR_AGENTS_*` variables, then reads the agent's GitHub token from its file into
`GH_TOKEN`; a launch fails when that file is missing. A session started from inside another Claude
Code session would otherwise inherit its messaging socket, session ID and task list.
`CLAUDE_CONFIG_DIR` stays unset, so sessions share my login and folder trust.

## Stack

Python on uv, with the nimblefox-template tooling: mise-pinned tools, ruff, pyrefly, pytest, a
justfile with `fmt`, `lint`, `test` and `check`, and a `./setup` script. Standard library only for
the service: `urllib` for Linear GraphQL and ntfy, `sqlite3` in WAL mode with a busy timeout and
`BEGIN IMMEDIATE` writes, `subprocess` for `git`, `gh` and `tmux`.

## Testing

- Unit tests: admission, ledger claims and conflicts, `agent` block parsing, escalation routing
  and dedupe, Linear responses from saved GraphQL fixtures.
- Process tests: a fake `claude` binary that writes a handoff note, exits, crashes or hangs on
  command, covering the launcher and supervisor without using the subscription. The publisher is
  tested against a real throwaway origin.
- Pre-commit hook: a throwaway git repo with two competing claims.
- Plugin: `claude plugin eval` cases for the escalation rubric, chiefly to catch over-asking.
- End to end, by hand: one real issue in a test Linear team against a scratch repo on the VM.

## Verified and unverified

Verified on CLI 2.1.293 on 2026-10-09: a script-started tmux session with `--remote-control` runs
its initial prompt and connects after a one-time folder-trust prompt; local sessions message each
other; `--strict-mcp-config` with an empty config hides claude.ai connectors in print mode; a
session in a new worktree under an already-trusted folder gets no folder-trust prompt. Worktrees
therefore live under one root that is trusted once by hand during setup.

Also verified on CLI 2.1.293: with `CLAUDE_CONFIG_DIR` set, Claude Code keeps its global config in
`$CLAUDE_CONFIG_DIR/.claude.json` instead of `~/.claude.json`, so login and folder trust must be
done with the same setting the sessions use, which is unset. Verified with tmux 3.7: `tmux -D`
keeps a server running with no sessions, and `-N` makes a client fail rather than start a server.

Unverified: `--strict-mcp-config` in interactive mode (the smoke test checks it before the
first real issue); the exact Linear behavior for actions taken with my own key; ntfy priority 5
getting through Do Not Disturb on my phone.
