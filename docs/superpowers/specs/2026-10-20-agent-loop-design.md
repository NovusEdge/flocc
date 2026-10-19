# Agent loop: named subagents, budgets and grants

Status: draft for review, 2026-10-20, revised after two Opus reviews the same day (the spec alone,
then the spec with its plan). Amends the session sections of `2026-10-09-linear-agents-design.md`.
Applies to agent sessions on linear-agents hosts only; interactive sessions on my own machines are
unchanged.

## Goal

An agent session may delegate only to subagents defined ahead of time, each with a fixed prompt,
model and tool set, and only as often as its phase allows. Anything else (another model, more
launches, a workflow, an undefined agent) needs my grant on that issue. Doing the work itself
stays the default; a subagent is the exception. When an agent cannot go on without my decision, it
asks me in the Claude app.

### Success criteria

- A host session cannot launch the built-in general-purpose, Explore or Plan agents, a fork, a
  named teammate, an isolated-worktree agent, a workflow, a nested subagent or a second `claude`
  process. A catalogue agent launched past its budget, or with more than the phase's running limit,
  is refused with a reason that tells the agent how to ask for a grant.
- A catalogue agent always runs with the model in its file: a launch that passes its own model is
  refused.
- If the gate fails (crash, bad output, timeout), the launch is refused, not allowed.
- A grant I give on an issue lets that issue's run do the granted thing, and no other issue's; a
  launch grant covers only the phase it was given for.
- Every launch, allowed or refused, is in a per-issue log, and `linear-agents status` shows each
  live claim's launches against its budget.
- A session compacts at most once; after that the existing handoff takes over.
- A blocking decision reaches me as a question in the Claude app; an unanswered one also becomes an
  escalation after two hours.
- `just ship` installs only an allowlisted set of plugins on a host, and agent sessions load only
  those.
- Only implementer and fix sessions register with Remote Control.
- A finished run's Claude Code scratchpads are gone, and nothing in `/tmp/claude-<uid>` outlives
  two days untouched.

### Out of scope

- Sessions on my desktop or laptop. The catalogue is not loaded there.
- Sessions that load the plugin without `LINEAR_AGENTS_STATE` (plugin evals, `--plugin-dir`
  development): the gate refuses every `Agent` call in them, because there is no state directory
  to count in. Plugin evals do not launch subagents, so nothing depends on it.
- Stopping a deliberately hostile agent. Sessions run as the same Unix user as the service, so an
  agent set on it could edit the gate or its state files. This guards against drift: too many
  agents, the wrong model, a workflow for one check.
- The session backend. tmux stays; Herdr was trialled and dropped.

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

| Agent | Model | Effort | Tools | Used for |
|---|---|---|---|---|
| `source-finder` | Haiku | low | WebSearch, WebFetch | Finding dated, URL-cited sources on a library, API or service; no conclusions |
| `source-reader` | Sonnet | medium | WebSearch, WebFetch, Read | Reading what `source-finder` returned and answering the stated question, with citations |
| `code-mapper` | Sonnet | medium | Read, Grep, Glob | Mapping where something lives in a large repo before planning; returns paths and a short account |
| `plan-reviewer` | Opus | high | Read, Grep, Glob | One adversarial read of the implementer's plan against the acceptance criteria and the tester's tests |
| `task-implementer` | Sonnet | medium | Read, Edit, Write, Bash, Grep, Glob | Implementing one task from its brief file, committing its own paths; also makes the fixes a review asks for |
| `wave-reviewer` | Opus | high | Read, Grep, Glob | Reviewing one wave against its briefs and the diff file the session wrote: requirements first, then quality; writes findings |
| `fix-rereviewer` | Sonnet | medium | Read, Grep, Glob | Verdicting each finding of a fix round against the fix diff file, nothing else |

Reviewers and mappers get no Bash. The `tools` field cannot narrow Bash to read-only commands,
so the only way to keep a reviewer from editing is to leave Bash out; the implementer session
writes each wave's `git diff` and the test output to `<state_dir>/waves/` and passes the paths.
`source-finder` and `source-reader` expect today's date in their task; the skills that start them
say to include it.

The tester and reviewer phases are already separate sessions, so the catalogue has no tester or
whole-branch reviewer agent.

## Phase budgets

A budget counts launches per run and phase: handoffs and crash relaunches of the same phase share
it. A phase starts at the test→impl, impl→review and check→fix transitions and at a claim's first
launch; the supervisor passes `<phase>@<start time>` to every session of the phase as
`LINEAR_AGENTS_RUN`, and the gate counts only log lines carrying it.

| Phase | Allowed agents | Launches | Running at once |
|---|---|---|---|
| test | `source-finder`, `source-reader` | 2 | 2 |
| impl | `task-implementer`, `wave-reviewer`, `fix-rereviewer`, `plan-reviewer`, `code-mapper`, `source-finder`, `source-reader` | 10 | 4 |
| fix | `task-implementer`, `fix-rereviewer` | 2 | 1 |
| review | none | 0 | 0 |

Ten covers a typical implementer run in my pattern: a plan review, four task implementers in one
or two waves, a review per wave, one fix and its re-review, and a lookup or two. A plan that needs
more asks for `launches` before building, not halfway. The `fix` phase is the service's own: it
starts when the repo's check fails after the implementer, it fixes inline or through one
`task-implementer` and one `fix-rereviewer`, and it is separate from the fix round a
`wave-reviewer` can ask for inside the implementer phase.

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
| Plan within the budget, or ask for more up front | "how many re-reviews and fix passes bro" | Implementer skill |
| Research is a pair: Haiku finds dated, cited sources, Sonnet reads them | "haiku for finding sources, sonnet for reading" | `source-finder`, `source-reader` prompts; callers pass today's date |
| Subagent prompts point at a brief file, not a paraphrase | 1,145 of 2,265 prompts point at a contract or brief | Implementer skill writes each task's brief to `<state_dir>/briefs/task-N.md` and passes the path |
| No subagent spawns its own | "please make it so that agents dont spawn more subagents" | Spawn depth 1, the gate, and a line in every agent file |
| Do small work yourself; don't delegate or ask about what you can just do | "why didnt you just write it yourself?", "you can add tests-amended yourself why do you gotta ask me?" | Implementer skill: a change outside the plan's tasks that fits in a few lines is done inline |
| No heavy local runs from subagents (Unity, long builds, training) | "NEVER let subagetns run unity shit" | Agent files' prompts; `task-implementer` runs only the focused tests its brief names |
| Nothing idles; a bounded number of agents at once | "shut down the agents that are done", "check for any other shit that's idle" | The gate's running count; teammates refused |
| Fable only when I ask, Haiku only for finding sources, no non-Claude models | "pls use fable only when i explicitly ask for it", "bruh dont do codex reviews" | Models pinned in agent files, a per-call `model` refused by the gate; `claude` from Bash denied |
| A workflow is for a big plan, never for a single check | "lets not do workflow for every small thing" | Workflows off; a named `workflow <name>` grant |

### Parallel implementers in one worktree

Implementers commit their own work; the implementer session decides how tasks are distributed.
Its plan lists the paths each task touches. Tasks with disjoint paths may run in the same wave, up
to four; tasks that share a path run in sequence. Each `task-implementer` commits only its own
paths with `linear-agents commit -m <msg> -- <paths>`, which takes a lock on the worktree and runs
`git add` and `git commit -- <paths>`, so two implementers never race on the git index or sweep up
each other's files. The worktree's pre-commit hook (path claims and the trailer) runs as before.

The implementer session follows the same rule for its own commits while a wave is running: it
commits through `linear-agents commit` with explicit paths, never a bare `git commit`. Before a
handoff it waits for running agents to finish, and its `handoff.md` records the plan, the waves
done, and the wave in progress, so the next session resumes there.

Agents are never started with `isolation: worktree`: the pre-commit hook finds no claim for a
second worktree and lets its commits through, onto a branch the service never publishes. The gate
refuses the parameter.

## Enforcement

1. Launch flags and session settings, set in `claude_argv`:
   - env `CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH=1`: subagents cannot spawn subagents.
   - env `CLAUDE_CODE_DISABLE_EXPLORE_PLAN_AGENTS=1`.
   - settings `disableWorkflows: true`, unless a grant allows a named workflow.
   - settings `permissions.deny`, added to the existing commit rules: `Agent(general-purpose)`
     (the docs say a deny by name works for built-in agents) and `Bash(claude:*)` (a second
     session that nothing meters).
   - `CLAUDE_CODE_SUBAGENT_MODEL_FORCE` is not set: it makes Claude Code ignore the agent file's
     model, the opposite of what is wanted. The gate refuses per-call models instead.
2. The gate: a `PreToolUse` hook in `plugin/hooks/hooks.json`, matcher `Agent|Task|Workflow`,
   command `linear-agents gate`, with a 10 s timeout. It fails closed on its own: any exception
   prints a refusal and exits 2, which blocks a `PreToolUse` call in every Claude Code version.
   `"onFailure": "block"` is also set, for a crash before Python starts; that key needs Claude
   Code 2.1.295, the installer's new minimum. A call whose `tool_name` is not `Agent`, `Task` or
   `Workflow` (if the matcher is an unanchored pattern, `TaskCreate` and the like reach the gate)
   is allowed without logging. Otherwise the gate reads its state under a `flock` on
   `<state_dir>/launches.jsonl.lock` and refuses when any of these holds:
   - the call comes from inside a subagent (`agent_id` present);
   - `tool_input.model` or `tool_input.effort` is set (the reason says to retry without it);
   - `tool_input.team_name` or `tool_input.name` is set (a teammate);
   - `tool_input.isolation` is set;
   - `subagent_type` is outside the phase's allowed list, unless granted; a missing
     `subagent_type` counts as `general-purpose`;
   - it is `general-purpose` or a workflow that is granted but was not in the grants the session
     was launched with (`LINEAR_AGENTS_LAUNCH_GRANTS`): rule `next-session`;
   - the run has used its launches, unless granted; a granted workflow spends one launch;
   - the phase's running count is at its limit;
   - it is a `Workflow` call that no grant names, or a granted workflow already started in this
     run;
   - `LINEAR_AGENTS_PHASE` is unset or unknown.
   Otherwise it allows the launch and logs it. A missing or unparseable `grants.json` counts as no
   grants. The gate makes no network call and does not open the ledger.
3. Running count: a `SubagentStop` hook (`linear-agents gate --stopped`) logs each finished agent
   with its `agent_type`, and the running count is the run's allowed launches since the session
   started minus the stops of catalogue or granted agents in that time. Stops from inside a granted
   workflow do not lower it. The count starts again at a session's `startup` or `resume`, since
   nothing outlives a session's end, but not at a `compact`: background agents survive compaction.
4. The log: every decision and stop is a line in `<state_dir>/launches.jsonl` (time, run, phase,
   event, agent or workflow, rule). A line torn by a killed hook is skipped, and the next write
   starts on a new line. `linear-agents status` shows each live claim's launches against its
   budget.

A refusal returns `permissionDecision: "deny"` with a reason naming the rule and, the first time
that rule refuses that agent in the run, the escalation command below. Later refusals for the same
rule and agent say it has already been escalated and not to retry or escalate again, so a refused
launch cannot loop.

## Grants

Grants reach a run three ways, all limited to me and `steer_authors`:

- Pre-planned, in the issue's `agent` block under a new key `allow`, read at launch like the other
  block keys:

  ```agent
  allow: launches 14, agent code-mapper, workflow <name>, fix-rounds 2
  ```

- In a comment of mine on the issue, a line starting `allow:` (any case, after leading spaces) with
  the same items. The supervisor already reads my comments on a running issue to forward them
  (`forward_comments`); it also applies `allow:` lines from them once it has delivered the comment
  to a live session. No extra Linear call.
- As my reply to the agent's escalation or question, which is the same comment path.

Items are comma-separated: `launches <n>` replaces a phase's launch budget, `agent <name>` adds an
agent to the allowed list, `workflow <name>` allows one named workflow, and `fix-rounds <n>`
raises the implementer's fix rounds. A launch grant is scoped: from a comment it applies to the
phase that is running when the comment arrives; from the agent block it applies to the implementer
phase. The other grants apply to the whole claim. A comment grant wins over the block for the same
item. `<state_dir>/grants.json` holds the merged result, the gate reads it, and
`linear-agents context` prints the grants that apply to the current phase so the skills see them.
An unknown item in the agent block fails the block as now; in a comment it is answered once with a
comment naming the item, and the last good grants stay. A new claim on the issue (a rerun) starts
with no grants but the block's.

`agent general-purpose` and `workflow <name>` change launch flags (the deny rule,
`disableWorkflows`) and so apply from the next session start of the issue: a handoff, or
`linear-agents release`. The others reach the gate within one tick.

The escalation the gate points to is
`linear-agents escalate --tier 2 --cause grant:<what> --summary '<why it needs it>' --default
'<doing it without>' --deadline-min 120`, after which the agent carries on without the subagent.
A missing grant never stops a run; it makes it slower or asks me.

## Questions to the owner

Implementer and fix sessions run with Remote Control, and the installer turns on "Push when actions
required", so an `AskUserQuestion` in one of them shows up in the Claude app with a push
notification and I can answer it there. Agents use it for decisions they cannot go on without (the cases that were
`--tier 2 --waiting` escalations); a reversible choice still goes through `escalate` with a
default, so the run does not stall.

- Before asking, the agent records `linear-agents escalate --tier 3 --cause asked:<topic>
  --summary '<the question>'`, so the question reaches the digest and the issue history.
- The gate's hook also matches `AskUserQuestion`: it writes `<state_dir>/asking` with the time and
  the question text, and a `PostToolUse` hook on `AskUserQuestion` removes it.
- The supervisor checks `asking` on each tick; a question older than two hours is sent once as a
  tier 2 escalation with the question text, in case the push did not reach me. My answer in the
  app or on Linear both unblock it. While `asking` exists, the supervisor neither forwards a
  Linear reply into the pane nor types the handoff request, since keystrokes could select an
  option in the dialog; a reply is held and delivered once the question is answered.
- The wall clock keeps running while a question waits.

## Context: one compaction, then handoff

Auto-compaction stays on. The plugin's `SessionStart` hook runs `linear-agents session-start`;
when the hook input's `source` is `compact`, it writes `compacted` in the issue's state directory
and deletes `status.json`, so the next status line reading is from after the compaction. The
supervisor's context check (`context_restart_pct`, default 60) applies only once `compacted`
exists; before it, the session is left to compact once. `compacted` joins the files `launch()`
deletes, so a fresh session after a handoff starts without it. Writing the marker from
`PreCompact` instead would fire before compaction, while `status.json` still shows the old,
high reading, and the next tick would hand off at once.

## Host plugin set

`deploy/agent-plugins.txt` lists the plugins agent sessions get, one `name@marketplace` per line.
`just ship` (through `planPlugins`) installs only those, and fails naming any listed plugin that is
not enabled on my desktop. At launch, `claude_argv` sets every plugin in the host's
`installed_plugins.json` that is missing from the list to `false` under `enabledPlugins` in the
session settings, so a plugin already on a host is disabled for agents without an uninstall. Both
file paths are config keys (`agent_plugins_file`, `installed_plugins_file`) with host defaults.
This supersedes the earlier decision that sessions load all of my user-scope plugins with no
`enabledPlugins` override.

Initial list, for review: `docket@NovusEdge`, `superpowers@claude-plugins-official`,
`software-architecture@software-architecture`, `ponytail@ponytail`, `curt@curt`. glowup and the
product and design plugins are left out. superpowers' `subagent-driven-development` and
`dispatching-parallel-agents` skills tell an agent to spawn general-purpose subagents; on a host
the gate refuses those, and the implementer skill states the catalogue route instead. If that
proves noisy, superpowers comes off the list.

## Remote Control for implementer and fix sessions only

Every session started with `--remote-control` registers in the Claude app, and the app announces
each one; with a tester, an implementer and a reviewer per issue, plus restarts, that was too many
notifications on the first night. `claude_argv` passes `--remote-control <name>` only for the
`impl` and `fix` phases, the long ones I watch and steer and the ones that ask blocking
questions. Tester and reviewer sessions run without it; `just attach` still shows them, and a
tester that needs a decision escalates with `--waiting` instead of asking.

`remoteControlAtStartup` is not used: it would connect every session on the host, the tester and
reviewer included. The one-time "Enable Remote Control" consent stays a step of the first
interactive login.

## Scratchpad cleanup

- The `SessionStart` hook appends the hook input's `scratchpad_dir` (Claude Code 2.1.257 and
  later) to `<state_dir>/scratchpads`. When a claim leaves `running` (done, blocked, timed out), the
  service removes every directory listed there. Recording the path avoids re-deriving Claude Code's
  directory naming.
- A listed path is removed only if it lies under `/tmp/claude-<uid>/` with at least three more
  levels (`<project>/<session>/<scratchpad>`), so a damaged record cannot remove the whole scratch
  tree or another session's directory.
- On every systemd host, the installer writes `/etc/tmpfiles.d/claude-scratch.conf` with
  `e /tmp/claude-* - - - 2d`, so `systemd-tmpfiles-clean.timer` removes anything untouched for two
  days. Loop-mode hosts have no tmpfiles timer and rely on the first rule.
- On my desktop the same tmpfiles line is a manual step, documented in `docs/vm-setup.md`.

## Unconfirmed

These come from the docs as read on 2026-10-20 and are checked on the host before the code relies
on them:

- The `Workflow` tool's input fields; the gate reads the workflow's name from `name`.
- Whether `maxTurns`, `effort` and `background` are honoured for plugin agents (only `hooks`,
  `mcpServers` and `permissionMode` are documented as ignored).
- How `enabledPlugins` from `--settings` merges with user settings.
- Whether an `AskUserQuestion` in a Remote Control session sends a push to my phone.
- The exact spelling of `onFailure`; the gate's own exit 2 does not depend on it.
- Whether parallel `PreToolUse` calls run concurrently; the `flock` makes the gate correct either
  way.
- Whether `SubagentStop` carries `agent_type`: a catalogue agent's `stop` line in `launches.jsonl`
  needs a non-empty `what` to free its slot.
- What keystrokes do in an open `AskUserQuestion` dialog in a host session. The hold on forwarded
  comments and handoff requests while `asking` exists assumes they can select an option; if they
  cannot, the hold can go.
- Whether a failed `Agent` call (refused by a permission rule after the gate allowed it) fires
  `PostToolUseFailure`.
- Whether the `Workflow` tool's `name` is a free label the agent chooses. If so, a `workflow
  <name>` grant does not restrict what runs.

## Testing

- `linear-agents gate`, from hook-input JSON: each refusal rule (subagent caller, per-call model,
  teammate, isolation, missing `subagent_type`, outside the list, over the launches, over the
  running count, ungranted workflow, unknown phase), each with its granted variant where one
  exists; a tool outside `Agent|Task|Workflow` allowed; a missing and a corrupt `grants.json`; an
  exception inside the gate refusing with exit 2; a repeated refusal of the same rule and agent
  saying not to retry; counts per `LINEAR_AGENTS_RUN`; a torn log line; separate processes racing
  for the last slot.
- `linear-agents gate --stopped` lowering the running count, and a workflow's internal stop not
  lowering it; `session-start` resetting it on `startup` but not on `compact`.
- `AskUserQuestion`: `asking` written and removed; the supervisor escalating a two-hour-old
  question once.
- `linear-agents commit`: two concurrent calls with disjoint paths produce two commits, each with
  only its own paths.
- `allow` parsing in the agent block and in comments (mine and `steer_authors`, not others),
  including an unknown item; launch grants scoped to a phase; a comment grant winning over the
  block; a rerun starting without the old comment grants.
- `claude_argv`: the new environment, deny rules, `enabledPlugins` from the allowlist,
  `LINEAR_AGENTS_RUN`, `disableWorkflows` lifted only by a workflow grant, and
  `--remote-control` only for `impl` and `fix`.
- Supervisor: no handoff before `compacted`; handoff at the threshold after it; `compacted`
  cleared at launch.
- Cleanup: recorded scratchpads removed when a claim leaves `running`, not while it runs, and never
  a path outside a full session scratch directory.
- `linear-agents status`: launches against budget for each live claim.
- Installer: `planPlugins` with the allowlist, including a listed plugin that is not enabled; the
  2.1.295 minimum; the tmpfiles step.
- On the host, once: a real implementer run that launches `task-implementer` and `wave-reviewer`,
  a refused general-purpose launch in `launches.jsonl`, and the checks under Unconfirmed.
