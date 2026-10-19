# Agent loop: named subagents, budgets and grants

Status: draft for review, 2026-10-20. Amends the session sections of
`2026-10-09-linear-agents-design.md`. Applies to agent sessions on linear-agents hosts only;
interactive sessions on my own machines are unchanged.

## Goal

An agent session may delegate only to subagents defined ahead of time, each with a fixed prompt,
model and tool set, and only as often as its phase allows. Anything else (another model, more
launches, a workflow, an undefined agent) needs my grant on that issue. Doing the work itself
stays the default; a subagent is the exception.

### Success criteria

- A host session cannot launch the built-in general-purpose, Explore or Plan agents, a workflow,
  or a nested subagent, and a launch of a catalogue agent past its phase budget is refused with a
  reason that tells the agent how to ask for a grant.
- A catalogue agent always runs with the model in its file, whatever the caller passes.
- A grant I add to an issue lets that issue's run do the granted thing, and no other issue's.
- Every launch, allowed or refused, is in a per-issue log I can read after the run.
- A session compacts at most once; after that the existing handoff takes over.
- `just ship` installs only an allowlisted set of plugins on a host, and a fresh host needs no
  interactive step to enable Remote Control.
- A finished run's Claude Code scratchpad is gone, and nothing in `/tmp/claude-<uid>` outlives two
  days untouched.

### Out of scope

- Sessions on my desktop or laptop. The catalogue is not loaded there.
- Stopping a deliberately hostile agent. Sessions run as the same Unix user as the service, so an
  agent set on it could edit the gate or its grants. This guards against drift: too many agents,
  the wrong model, a workflow for one check.
- Choosing the session backend (tmux or Herdr). Everything here sits above `Sessions`.

## Evidence

From 21 days of my transcripts (251 sessions, about 2,265 subagent calls):

- Prompts are already templates. Half point at a contract or brief file, and 647 forbid nested
  spawning. Recurring prompts: per-task implementer, per-task reviewer, fix re-reviewer, a tester
  that writes tests from the spec without reading the code, web research (Haiku finds sources,
  Sonnet reads them), and an Opus adversarial review of a spec or PR.
- Fan-out is narrow: most messages spawn one agent, the most at once was six.
- My implementation pattern: the main session orchestrates, a Sonnet subagent implements even a
  single task, and a review subagent (or a fix pass) follows.
- My corrections are about scale and models: "too many subagents", "lets not do workflow for
  every small thing", "how many re-reviews and fix passes", "use fable only when i explicitly ask
  for it", "NEVER let subagents run unity", "why didnt you just write it yourself?".

## Catalogue

Each entry is an agent file in `plugin/agents/`, invoked as `linear-agents:<name>`. The file fixes
the system prompt, `model`, `tools`, `maxTurns` and `effort`; the caller supplies only the task.
Its `description` says when it may be used, so the agent sees the condition before reaching for
it.

| Agent | Model | Tools | Used for |
|---|---|---|---|
| `source-finder` | Haiku | WebSearch, WebFetch | Finding sources on a library, API or service before relying on it or adding it |
| `source-reader` | Sonnet | WebFetch, Read | Reading the sources `source-finder` returned and answering a stated question |
| `code-mapper` | Sonnet | Read, Grep, Glob, read-only Bash | Mapping where something lives in a large repo; returns paths and a short account |
| `task-implementer` | Sonnet | Read, Edit, Write, Bash | Implementing one task from the implementer's plan, with tests first |
| `task-reviewer` | Opus | Read, Grep, Glob, read-only Bash | Reviewing one implemented task against its task text and the acceptance criteria |

The tester and reviewer phases are already separate sessions, so the catalogue has no tester or
whole-branch reviewer agent.

## Phase budgets

`LINEAR_AGENTS_PHASE` is already in each session's environment. A budget counts launches per
session (a handoff starts a new count).

| Phase | Allowed agents | Launches | Workflows |
|---|---|---|---|
| test | `source-finder`, `source-reader` | 2 | none |
| impl | `task-implementer` (at most 4 running at once), `task-reviewer`, `code-mapper`, `source-finder`, `source-reader` | 6 | none |
| review | none | 0 | none |

The implementer skill changes to match my pattern: implement each task through
`task-implementer`, then `task-reviewer`; on a failed review, one fix by the same route. A second
fix round needs a grant.

## Enforcement

Three layers, cheapest first.

1. Launch flags and session settings, set in `claude_argv`:
   - env `CLAUDE_CODE_SUBAGENT_MODEL_FORCE=1`: the agent file's model wins over the caller's.
   - env `CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH=1`: subagents cannot spawn subagents.
   - env `CLAUDE_CODE_DISABLE_EXPLORE_PLAN_AGENTS=1`.
   - settings `disableWorkflows: true`, unless a grant allows a named workflow.
   - settings `permissions.deny`: `Agent(general-purpose)` added to the existing commit rules.
     Whether a deny matches a built-in agent by name is unconfirmed; the gate below covers it
     either way.
2. The gate: a `PreToolUse` hook in `plugin/hooks/hooks.json`, matcher `Agent|Workflow`,
   command `linear-agents gate`. It reads the hook input (`tool_name`, `tool_input.subagent_type`,
   `tool_input.model`, `agent_id`) and decides:
   - deny any `Agent` or `Workflow` call made from inside a subagent (`agent_id` present);
   - deny a `subagent_type` outside the phase's allowed list, unless granted;
   - deny past the phase's launch count, unless granted;
   - deny any `Workflow` unless a grant names it;
   - allow otherwise, and count the launch.
   A denial returns `permissionDecision: "deny"` with a reason naming the rule and the command to
   ask for a grant (below). The gate reads and writes only the issue's state directory and the
   ledger; it makes no network call, so it adds little latency.
3. The log: every decision is appended to `<state_dir>/launches.jsonl` (time, phase, agent or
   workflow, model, allowed or denied, rule). `linear-agents status` shows the per-issue counts.

## Grants

A grant lives on the issue, in the existing `agent` block, under a new key `allow`:

```agent
allow: launches 10, agent general-purpose, workflow sdd-waves, fix-rounds 2
```

Items are comma-separated: `launches <n>` replaces the phase's launch budget, `agent <name>`
adds an agent to every phase's allowed list, `workflow <name>` allows one named workflow (and
lifts `disableWorkflows` for that run), `fix-rounds <n>` is read by the implementer skill. An
unknown item fails the block like any other agent-block error, which already comments on the
issue.

The supervisor writes the parsed grants to `<state_dir>/grants.json` at launch, and rewrites the
file on each tick from the issue's current description, so a grant I add mid-run reaches the gate
within one tick. Two grants change launch flags and so apply only from the next session launch
(a handoff or a release): `agent general-purpose` (it lifts the deny rule) and `workflow <name>`
(it lifts `disableWorkflows`).

When the gate denies a launch, its reason tells the agent to escalate:
`linear-agents escalate --tier 2 --cause grant:<what> --summary '<why it needs it>' --default
'<doing it without>' --deadline-min 120`, then continue without the subagent. So a missing grant
never stops a run; it makes it slower or asks me.

## Context: one compaction, then handoff

Auto-compaction stays on. A `PreCompact` hook (`linear-agents compacted`) writes `compacted` in
the issue's state directory. The supervisor's context check (`context_restart_pct`, default 60)
applies only once that marker exists; before it, the session is left to compact once. After a
handoff the fresh session starts without the marker. Whether `PreCompact` fires for auto
compaction in a session started this way is to be checked on the host before relying on it; if
it does not, the fallback is to read compaction from the transcript in the existing status
check.

## Host plugin set

`deploy/agent-plugins.txt` lists the plugins agent sessions get, one `name@marketplace` per line.
`la-setup ship` installs only those that are enabled on my desktop, and fails naming any listed
plugin that is not. Initial list, for review: `docket@NovusEdge`, `superpowers@claude-plugins-official`,
`software-architecture@software-architecture`, `ponytail@ponytail`, `curt@curt`. glowup and the
product and design plugins are left out. A plugin already installed on a host and not listed is
disabled for agent sessions with `enabledPlugins: {"<id>": false}` in the session settings, so an
existing host needs no uninstall.

superpowers' `subagent-driven-development` and `dispatching-parallel-agents` skills tell an agent
to spawn general-purpose subagents. On a host the gate refuses those launches, and the implementer
skill states the catalogue route instead; if that proves noisy, superpowers comes off the list.

## No-TUI Remote Control

The Claude step of the installer writes `remoteControlAtStartup: true` into the service user's
`~/.claude/settings.json` with the push settings it already writes, and the login note drops its
"Accept Enable Remote Control" line. The documentation does not say whether this skips the one-time
consent prompt; the check is a fresh VM from images:debian/13 shipped with `--skip-interactive`
after a login, then one launched session showing in the Claude app.

## Scratchpad cleanup

- On a host, when a claim leaves `running` (done, blocked, timed out) the service removes
  `/tmp/claude-<uid>/<slug>`, where `<slug>` is the worktree path with `/` replaced by `-`, the
  directory Claude Code makes for every session started in that worktree. A rerun gets a new one.
- On every host, the installer writes `/etc/tmpfiles.d/claude-scratch.conf` with
  `e /tmp/claude-* - - - 2d`, so `systemd-tmpfiles-clean.timer` removes anything untouched for two
  days. Loop-mode hosts have no tmpfiles timer and rely on the first rule.
- On my desktop the same tmpfiles line is a manual step, documented in `docs/vm-setup.md`.

## Testing

- `linear-agents gate`: unit tests feeding hook-input JSON for each rule (outside the list, past
  the count, from a subagent, a workflow, granted variants) and checking the decision JSON and the
  `launches.jsonl` line.
- Agent-block parsing of `allow`, including an unknown item.
- `claude_argv`: the new environment and settings keys, and `disableWorkflows` lifted only by a
  workflow grant.
- Supervisor: no handoff before `compacted`, handoff at the threshold after it.
- Cleanup: the scratchpad directory removed when a claim finishes, and not while it runs.
- Installer: `planPlugins` with the allowlist, including a listed plugin that is not enabled.
- On the host, once: a real session whose implementer launches `task-implementer` and
  `task-reviewer`, a denied general-purpose launch visible in `launches.jsonl`, and the
  `PreCompact` and Remote Control checks above.
