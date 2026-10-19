# Agent loop: named subagents, budgets and grants

Status: draft for review, 2026-10-20, revised after an Opus review the same day. Amends the
session sections of `2026-10-09-linear-agents-design.md`. Applies to agent sessions on
linear-agents hosts only; interactive sessions on my own machines are unchanged.

## Goal

An agent session may delegate only to subagents defined ahead of time, each with a fixed prompt,
model and tool set, and only as often as its phase allows. Anything else (another model, more
launches, a workflow, an undefined agent) needs my grant on that issue. Doing the work itself
stays the default; a subagent is the exception.

### Success criteria

- A host session cannot launch the built-in general-purpose, Explore or Plan agents, a fork, a
  named teammate, a workflow, a nested subagent or a second `claude` process. A catalogue agent
  launched past its budget, or with more than four running, is refused with a reason that tells
  the agent how to ask for a grant.
- A catalogue agent always runs with the model in its file: a launch that passes its own model is
  refused.
- If the gate fails (crash, bad output, timeout), the launch is refused, not allowed.
- A grant I give on an issue lets that issue's run do the granted thing, and no other issue's.
- Every launch, allowed or refused, is in a per-issue log I can read after the run.
- A session compacts at most once; after that the existing handoff takes over.
- `just ship` installs only an allowlisted set of plugins on a host, agent sessions load only
  those, and a fresh host needs no interactive step to enable Remote Control.
- A finished run's Claude Code scratchpads are gone, and nothing in `/tmp/claude-<uid>` outlives
  two days untouched.

### Out of scope

- Sessions on my desktop or laptop. The catalogue is not loaded there.
- Stopping a deliberately hostile agent. Sessions run as the same Unix user as the service, so an
  agent set on it could edit the gate or its state files. This guards against drift: too many
  agents, the wrong model, a workflow for one check.
- Choosing the session backend (tmux or Herdr). Everything here sits above `Sessions`.

## Evidence

From 21 days of my transcripts (251 sessions, about 2,265 subagent calls); the full, dated
analysis is `docs/research/2026-10-20-dev-flow-patterns.md`:

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
- 629 of the 2,265 calls named no agent type, and so ran as general-purpose; 656 were named
  teammates. Both are routes past a catalogue that only checks named agents.

## Catalogue

Each entry is an agent file in `plugin/agents/`, invoked as `linear-agents:<name>`. The file fixes
the system prompt, `model`, `tools`, `maxTurns` and `effort`; the caller supplies only the task.
Its `description` says when it may be used, so the agent sees the condition before reaching for
it. Every file's prompt says not to spawn subagents and not to run heavy local jobs.

| Agent | Model | Tools | Used for |
|---|---|---|---|
| `source-finder` | Haiku | WebSearch, WebFetch | Finding dated, URL-cited sources on a library, API or service; no conclusions |
| `source-reader` | Sonnet | WebSearch, WebFetch, Read | Reading what `source-finder` returned and answering the stated question, with citations |
| `code-mapper` | Sonnet | Read, Grep, Glob | Mapping where something lives in a large repo before planning; returns paths and a short account |
| `plan-reviewer` | Opus | Read, Grep, Glob | One adversarial read of the implementer's plan against the acceptance criteria and the tester's tests |
| `task-implementer` | Sonnet | Read, Edit, Write, Bash | Implementing one task from its brief file, committing its own paths; also makes the fixes a review asks for |
| `wave-reviewer` | Opus | Read, Grep, Glob | Reviewing one wave against its briefs and the diff file the session wrote: requirements first, then quality; writes findings |
| `fix-rereviewer` | Sonnet | Read, Grep, Glob | Verdicting each finding of a fix round against the fix diff file, nothing else |

Reviewers and mappers get no Bash. The `tools` field cannot narrow Bash to read-only commands,
so the only way to keep a reviewer from editing is to leave Bash out; the implementer session
writes each wave's `git diff` and the test output to `<state_dir>/waves/` and passes the paths.

The tester and reviewer phases are already separate sessions, so the catalogue has no tester or
whole-branch reviewer agent.

## Phase budgets

A budget counts launches per run and phase: handoffs and crash relaunches of the same phase share
it. The supervisor already resets a phase's `started_at` when a phase begins
(`supervisor.py` at the test→impl, impl→review and check→fix transitions); it passes that value to
every session of the phase as `LINEAR_AGENTS_RUN`, and the gate counts only log lines carrying it.

| Phase | Allowed agents | Launches | Running at once |
|---|---|---|---|
| test | `source-finder`, `source-reader` | 2 | 2 |
| impl | `task-implementer`, `wave-reviewer`, `fix-rereviewer`, `plan-reviewer`, `code-mapper`, `source-finder`, `source-reader` | 10 | 4 |
| fix | `task-implementer`, `fix-rereviewer` | 2 | 1 |
| review | none | 0 | 0 |

Ten covers a typical implementer run in my pattern: a plan review, four task implementers in one
or two waves, a review per wave, one fix and its re-review, and a lookup or two. The `fix` phase is
the service's own: it starts when the repo's check fails after the implementer, and it is separate
from the fix round a `wave-reviewer` can ask for inside the implementer phase.

## Working style the agents carry

My way of working is specific, and the catalogue and skills encode it rather than leaving it to
the agent's defaults. Each rule below comes from the transcript analysis; the right-hand column
says where it lives.

| Rule | From | Lives in |
|---|---|---|
| Sonnet implements, Opus reviews; the implementer makes every edit, a reviewer only writes findings | "subagent driven using sonnet pls" (58 messages), "we always want the implementr model to do the edits review only generates a review" | Agent files' `model` and `tools` (reviewers have no Edit, Write or Bash) |
| Plan tasks go to `task-implementer`, even a single one | "even if its just one subagent sonnet does the implementation" | Implementer skill |
| Review per wave, not per task | "opus review agent on task waves if possible not on each task" | `wave-reviewer` description; implementer skill |
| One fix round, then escalate | "how many re-reviews and fix passes bro", "one fix round max" | Implementer skill; `fix-rounds` grant |
| A plan of four or more tasks gets one independent Opus read before building; a smaller plan is checked by the session itself | "opus review the spec then we do planning" (45 messages); workflows "only for plans with 4 or more tasks" | `plan-reviewer`; implementer skill |
| Research is a pair: Haiku finds dated, cited sources, Sonnet reads them | "haiku for finding sources, sonnet for reading" | `source-finder`, `source-reader` prompts, which also carry today's date |
| Subagent prompts point at a brief file, not a paraphrase | 1,145 of 2,265 prompts point at a contract or brief | Implementer skill writes each task's brief to `<state_dir>/briefs/task-N.md` and passes the path |
| No subagent spawns its own | "please make it so that agents dont spawn more subagents" | Spawn depth 1, the gate, and a line in every agent file |
| Do small work yourself; don't delegate or ask about what you can just do | "why didnt you just write it yourself?", "you can add tests-amended yourself why do you gotta ask me?" | Implementer skill: a change outside the plan's tasks that fits in a few lines is done inline |
| No heavy local runs from subagents (Unity, long builds, training) | "NEVER let subagetns run unity shit" | Agent files' prompts; `task-implementer` runs only the focused tests its brief names |
| Nothing idles; at most four agents at once | "shut down the agents that are done", "check for any other shit that's idle" | The gate's running count (below); teammates denied |
| Fable only when I ask, Haiku only for finding sources, no non-Claude models | "pls use fable only when i explicitly ask for it", "bruh dont do codex reviews" | Models pinned in agent files, a per-call `model` refused by the gate; `claude` from Bash denied |
| A workflow is for a big plan, never for a single check | "lets not do workflow for every small thing" | Workflows off; `workflow sdd-waves` grant |

### Parallel implementers in one worktree

Implementers commit their own work; the implementer session decides how tasks are distributed.
Its plan lists the paths each task touches. Tasks with disjoint paths may run in the same wave, up
to four; tasks that share a path run in sequence. Each `task-implementer` commits only its own
paths with `linear-agents commit -m <msg> -- <paths>`, which takes a lock on the worktree and runs
`git commit -- <paths>`, so two implementers never race on the git index or sweep up each other's
files. The worktree's pre-commit hook (path claims and the trailer) runs as before. No
`isolation: worktree`: a second worktree would fail the hook's check that commits come from the
claimed worktree.

## Enforcement

Three layers, cheapest first.

1. Launch flags and session settings, set in `claude_argv`:
   - env `CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH=1`: subagents cannot spawn subagents.
   - env `CLAUDE_CODE_DISABLE_EXPLORE_PLAN_AGENTS=1`.
   - settings `disableWorkflows: true`, unless a grant allows a named workflow.
   - settings `permissions.deny`, added to the existing commit rules: `Agent(general-purpose)`
     (the docs say a deny by name works for built-in agents), `AskUserQuestion` (unattended, it
     stalls until the wall clock; the agent escalates instead) and `Bash(claude:*)` (a second
     session that nothing meters).
   - `CLAUDE_CODE_SUBAGENT_MODEL_FORCE` is not set: it makes Claude Code ignore the agent file's
     model, the opposite of what is wanted. The gate refuses per-call models instead.
2. The gate: a `PreToolUse` hook in `plugin/hooks/hooks.json`, matcher `Agent|Task|Workflow`,
   command `linear-agents gate`, with `"onFailure": "block"` and a 10 s timeout, so a crash, bad
   output or timeout refuses the launch. That needs Claude Code 2.1.295; the installer's minimum
   rises from 2.1.293. The gate reads the hook input and its state files under a `flock` on
   `<state_dir>/launches.jsonl`, and refuses when any of these holds:
   - the call comes from inside a subagent (`agent_id` present);
   - `tool_input.model` is set;
   - `tool_input.team_name` or `tool_input.name` is set (a teammate);
   - `subagent_type` is outside the phase's allowed list, unless granted; a missing
     `subagent_type` counts as `general-purpose`;
   - the run and phase have used their launches, unless granted;
   - the phase's running count is at its limit;
   - it is a `Workflow` call that no grant names;
   - `LINEAR_AGENTS_PHASE` is unset or unknown.
   Otherwise it allows the launch and logs it. A missing or unparseable `grants.json` counts as no
   grants. The gate makes no network call and does not open the ledger.
3. Running count: a `SubagentStop` hook (`linear-agents gate --stopped`) logs each finished agent,
   and the running count is the run's allowed launches minus its stops. A launch the gate allowed
   but Claude Code never started would hold a slot; the count resets when a session of the phase
   starts, since nothing survives a session's end.
4. The log: every decision and stop is a line in `<state_dir>/launches.jsonl` (time, run, phase,
   agent or workflow, allowed or refused, rule). `linear-agents status` shows each live claim's
   launches against its budget.

A refusal returns `permissionDecision: "deny"` with a reason naming the rule and, the first time
that rule refuses in the run, the escalation command below. Later refusals for the same rule say
it has already been escalated and not to retry or escalate again, so a refused launch cannot loop.

## Grants

Grants reach a run three ways, all limited to me and `steer_authors`:

- Pre-planned, in the issue's `agent` block under a new key `allow`, read at launch like the other
  block keys:

  ```agent
  allow: launches 10, agent general-purpose, workflow sdd-waves, fix-rounds 2
  ```

- In a comment of mine on the issue, a line starting `allow:` with the same items. The supervisor
  already reads my comments on a running issue to forward them (`forward_comments`); it also
  parses `allow:` lines from them. No extra Linear call.
- As my reply to the agent's escalation, which is the same comment path. For anything larger than
  a few more launches, the agent escalates first and I answer with an `allow:` line or a no.

Items are comma-separated: `launches <n>` replaces the phase's launch budget for this run, `agent
<name>` adds an agent to the allowed list, `workflow <name>` allows one named workflow, and
`fix-rounds <n>` raises the implementer's fix rounds. The supervisor merges each source into
`<state_dir>/grants.json`, which the gate reads, and `linear-agents context` prints the current
grants so the skills see them. An unknown item in the `agent` block fails the block as now; in a
comment it is answered once with a comment naming the item, and the last good grants stay.

`agent general-purpose` and `workflow <name>` change launch flags (the deny rule,
`disableWorkflows`) and so apply from the next session start of the issue: a handoff, or
`linear-agents release`. The others reach the gate within one tick.

The escalation the gate points to is
`linear-agents escalate --tier 2 --cause grant:<what> --summary '<why it needs it>' --default
'<doing it without>' --deadline-min 120`, after which the agent carries on without the subagent.
A missing grant never stops a run; it makes it slower or asks me.

## Context: one compaction, then handoff

Auto-compaction stays on. The plugin's existing `SessionStart` hook runs `linear-agents context`;
when the hook input's `source` is `compact`, it also writes `compacted` in the issue's state
directory and deletes `status.json`, so the next status line reading is from after the
compaction. The supervisor's context check (`context_restart_pct`, default 60) applies only once
`compacted` exists; before it, the session is left to compact once. `compacted` joins the markers
`launch()` deletes, so a fresh session after a handoff starts without it. Writing the marker from
`PreCompact` instead would fire before compaction, while `status.json` still shows the old,
high reading, and the next tick would hand off at once.

## Host plugin set

`deploy/agent-plugins.txt` lists the plugins agent sessions get, one `name@marketplace` per line.
`just ship` (`la-setup ship`, through `planPlugins`) installs only those, and fails naming any
listed plugin that is not enabled on my desktop. At launch, `claude_argv` reads the host's
`~/.claude/plugins/installed_plugins.json` and sets every installed plugin missing from the list to
`false` under `enabledPlugins` in the session settings, so a plugin already on a host is disabled
for agents without an uninstall. This supersedes the earlier decision that sessions load all of
my user-scope plugins with no `enabledPlugins` override.

Initial list, for review: `docket@NovusEdge`, `superpowers@claude-plugins-official`,
`software-architecture@software-architecture`, `ponytail@ponytail`, `curt@curt`. glowup and the
product and design plugins are left out. superpowers' `subagent-driven-development` and
`dispatching-parallel-agents` skills tell an agent to spawn general-purpose subagents; on a host
the gate refuses those, and the implementer skill states the catalogue route instead. If that
proves noisy, superpowers comes off the list.

## No-TUI Remote Control

The Claude step of the installer writes `remoteControlAtStartup: true` into the service user's
`~/.claude/settings.json` with the push settings it already writes, and the login note drops its
"Accept Enable Remote Control" line. The documentation does not say whether this skips the
one-time consent prompt; the check is a fresh VM from images:debian/13 shipped with
`--skip-interactive` after a login, then one launched session showing in the Claude app.

## Scratchpad cleanup

- The `SessionStart` hook appends the hook input's `scratchpad_dir` (Claude Code 2.1.257 and
  later) to `<state_dir>/scratchpads`. When a claim leaves `running` (done, blocked, timed out), the
  service removes every directory listed there. Recording the path avoids re-deriving Claude Code's
  directory naming.
- On every systemd host, the installer writes `/etc/tmpfiles.d/claude-scratch.conf` with
  `e /tmp/claude-* - - - 2d`, so `systemd-tmpfiles-clean.timer` removes anything untouched for two
  days. Loop-mode hosts have no tmpfiles timer and rely on the first rule.
- On my desktop the same tmpfiles line is a manual step, documented in `docs/vm-setup.md`.

## Unconfirmed

These come from the docs as read on 2026-10-20 and are checked on the host before the code relies
on them:

- The `Workflow` tool's input fields; the gate needs the workflow's name.
- Whether `maxTurns`, `effort` and `background` are honoured for plugin agents (only `hooks`,
  `mcpServers` and `permissionMode` are documented as ignored).
- How `enabledPlugins` from `--settings` merges with user settings.
- Whether `remoteControlAtStartup` skips the consent prompt.
- Whether parallel `PreToolUse` calls run concurrently; the `flock` makes the gate correct either
  way.

## Testing

- `linear-agents gate`, from hook-input JSON: each refusal rule (subagent caller, per-call model,
  teammate, missing `subagent_type`, outside the list, over the launches, over the running count,
  ungranted workflow, unknown phase), each with its granted variant where one exists; a missing and
  a corrupt `grants.json`; a second refusal of the same rule saying not to retry; counts kept per
  `LINEAR_AGENTS_RUN`; and the `launches.jsonl` lines.
- `linear-agents gate --stopped` lowering the running count.
- `linear-agents commit`: two concurrent calls with disjoint paths produce two commits, each with
  only its own paths.
- `allow` parsing in the agent block and in comments, including an unknown item, and the merge
  into `grants.json`; only my and `steer_authors` comments count.
- `claude_argv`: the new environment, deny rules, `enabledPlugins` from the allowlist,
  `LINEAR_AGENTS_RUN`, and `disableWorkflows` lifted only by a workflow grant.
- Supervisor: no handoff before `compacted`; handoff at the threshold after it; `compacted`
  cleared at launch; the `SessionStart` compact branch deleting `status.json`.
- Cleanup: recorded scratchpads removed when a claim leaves `running`, and not while it runs.
- Installer: `planPlugins` with the allowlist, including a listed plugin that is not enabled; the
  2.1.295 minimum.
- On the host, once: a real implementer run that launches `task-implementer` and `wave-reviewer`,
  a refused general-purpose launch in `launches.jsonl`, and the checks under Unconfirmed.
