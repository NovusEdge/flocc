# Dev-flow patterns from 21 days of Claude Code transcripts

Snapshot taken 2026-10-20. Window: 2026-09-28 to 2026-10-19. My way of working changes, so this
describes that window only; a later analysis gets its own dated file beside this one rather than
replacing it. The rules in `docs/superpowers/specs/2026-10-20-agent-loop-design.md` were drawn
from this snapshot.

Source: `~/.claude/projects/*/**.jsonl` (`~/.claude-nimble/projects` is a symlink to the same tree), 2,954 files: 393 main sessions, 2,561 subagent transcripts.

The extraction scripts (streaming the JSONL, aggregating, sampling owner messages) were throwaway
and are not kept; a rerun repeats the method below.

Filters on owner messages:
- Kept: `type=user`, `role=user`, string or text blocks, with `origin.kind` human or absent and `promptSource` typed, queued or absent.
- Dropped: tool results, meta, sidechain, system-reminder, `<command-…>` and hook text, pasted bodies, and anything that starts "Another Claude session sent a message" (teammate injection).
- Excluded: `-tmp-*` projects. These are scripted demo and test-harness sessions ("reply with the word ok").

After filtering, 3,536 owner messages remain across 251 real sessions. Counts below are for that set unless a line says otherwise.

Caveats:
- Subagent categories come from keyword heuristics on the description and prompt, so treat them as approximate.
- Agent calls made inside Workflow scripts are not in the Agent-call counts.

## 1. Session shape

- Size:
  - Owner messages per session: median 5, 75th percentile 21, 90th percentile 42, maximum 177.
  - Wall time: median 0.8 h, 75th percentile 4.7 h, 90th percentile 14.5 h.
  - Most sessions are short asks. A minority are long, multi-day threads that drive many agents.
- Openings (first owner message, 251 sessions):
  - 120 direct asks: "check the current repo and lmk what's kinda left on the blogs".
  - 69 resumes from a handoff file or memory: "~/.claude/handoffs/<proj>/current.md", "where'd we leave off?", "check the handoff doc pls". 68 messages mention handoff or "left off".
  - 47 open with discussion or brainstorming: "lets brainstorm sometjing", "I wanna think a bit more on the implications of …".
  - The rest are bug reports or pasted context from teammates.
  - 72 sessions begin with `/clear`. `/model` (21 uses) and `/effort` (17) are the other common pre-steps.
- Skill chain. Skills fired in 135 sessions:

  | Skill | Calls (main thread) |
  |---|---|
  | `superpowers:brainstorming` | 62 |
  | `docket:docket` | 62 |
  | `superpowers:writing-plans` | 56 |
  | `superpowers:subagent-driven-development` | 54 |
  | `superpowers:finishing-a-development-branch` | 16 |
  | `superpowers:systematic-debugging` | 15 |
  | `docket:docket-feature` | 13 |
  | `workflow-authoring` | 12 |

  - The full chain (brainstorm, then plan, then SDD or workflow) ran in 28 sessions.
  - Plan then SDD without a brainstorm ran in 11 sessions. SDD or workflow alone, usually resuming an existing plan, ran in 23.
  - Brainstorm only (discussion, no build) ran in 17 sessions.
- Gates. The gate is a spec or plan document plus an independent review, not plan mode. Plan mode was used twice (ExitPlanMode 2, EnterPlanMode 0).
  - The owner's most frequent delegation order is an Opus review of a spec, plan or PR (45 messages): "opus review the spec then we do planning", "do plan then have opus review both docs", "ryn an opus review on the PR pls before merging", "approved but please do a quick opus pass on it too".
  - AskUserQuestion was called 401 times in 102 sessions (median 2 per session). The agent asks before acting; the owner answers tersely ("yes pls", "lgtm", "1. yes 2. yes …").
- Merge and ship: 200 messages mention merge, push or PR. The common close is review, then merge, then a release.

## 2. Subagent use (2,265 Agent calls; 167 of 301 main sessions, 55%)

- Types:
  - general-purpose: 1,507
  - default, unspecified: 629
  - Explore: 92
  - fork: 21
  - claude-code-guide: 11
  - plugin agents: about 3
- Models:
  - sonnet: 1,551 (68%)
  - opus: 310
  - haiku: 186
  - inherit: 184
  - fable: 34
- What the calls are for, with model mix:

| Purpose | Calls | Model mix | Recurring description |
|---|---|---|---|
| Per-task review (spec + quality) | ~686 | sonnet 416, opus 212, fable 20 | "Review task N (spec + quality)" ×230 |
| Implementation of a plan task | ~589 | sonnet 529 | "Implement <plan> task N" |
| Web research | ~295 | haiku 136, sonnet 131 | "Research …", "Find sources: …" |
| Re-review of a fix round | ~222 | sonnet 190 | "Re-review task N fix round M" ×83 |
| Exploration and mapping | ~135 | sonnet or inherit | "Map where X happens", "Inventory …" |
| Fixes | ~92 | sonnet | "Fix final review findings" |
| Spec or plan drafting | ~67 | sonnet 30, opus 18 | "Write NF-… implementation plan" |
| Pixel art | ~51 | opus 19, sonnet 25 | "Art: stoat resting clips" |
| Blind tests from spec | ~42 | sonnet | "NF-… tester: write red tests" |
| Ops (rebase, dependency PRs, Linear) | ~40 | sonnet | "Merge low-risk Dependabot PRs" |

- Review-cycle descriptions: "final whole-branch review" ×25, "re-review final fix wave" ×25, "opus review of nf-# spec" ×16, "opus review of PR #" ×15.
- Fan-out per assistant message:

  | Agents in one message | Messages |
  |---|---|
  | 1 | 1,666 |
  | 2 | 169 |
  | 3 | 48 |
  | 4 | 20 |
  | 5 | 5 |
  | 6 | 2 |

  Width comes from many sequential agents, not wide bursts: median 7 agents per agent-using session, maximum 86.
- Background vs foreground: 954 background (42%), 1,311 foreground. Research and ops lean background; reviews lean foreground.
- Other modes:
  - Worktree isolation: 99 calls, all implementation or fix work.
  - Named teammates: 656 calls. Subagents sent 423 SendMessage calls to `team-lead`.
- Nesting: 53 Agent calls came from inside subagents (source finders, plan writers). The owner objected (section 4). 647 prompts now say "do not spawn/dispatch subagents".
- Prompt shape: median length is at least 2,485 characters (the extractor caps at 2,500).
  - 1,145 of 2,265 prompts point at a contract, brief or template file.
  - 1,026 declare the work read-only.
  - Most prompts already work as a parameterised template plus a brief file.
- Prompts that recur almost verbatim, and are candidates for named agents:
  - "You are reviewing one task's implementation: first whether it matches its requirements, then whether it is well-built. This is a task-scoped gate, not a merge review…" (117 or more, plus variants).
  - "You are re-reviewing one task's fix round… verdict each finding and inspect the fix diff — nothing else. Read-only. Do not dispatch subagents. Do not re-run the suite…" (more than 40 across variants).
  - "Review a design spec adversarially. Read-only: do not edit any file, do not commit…" (Opus reviews of specs and PRs).
  - "You are the tester for NF-… You write tests from the approved behaviour before any implementation exists; you must not read implementation code. Read only: specs/NF-….md …".
  - "Web-research task. Today is <date>. Use WebSearch and WebFetch only; do not write files. Return a concise bullet report with a URL and publication date for…".
  - "You are finding sources, not writing conclusions. Do not spawn subagents. Do not write any files…".
  - "You are implementing Task N of M in a plan… Read your task brief first, it is your requirements…".
- Research is almost fully delegated. Subagents made 5,154 WebFetch and 3,055 WebSearch calls; the main thread made 181 and 88.

## 3. Workflow (multi-agent script) use

68 Workflow calls: 49 inline scripts, 19 script paths, 20 resumes. They came from 4 project families: nimblefox-foundation (about 35), kairos (about 20), locus-core (10) and linear-agents (1). All took place from 2026-09-27 onward.

- Builds from a plan (most of them):
  - `nf-30x-*`, `nf310-context-store`, `nf322-implement`, `shopify-first-install-sdd`, `oculus-prefect-split`.
  - The fixed shape: blind tests, then three Sonnet build waves, each wave batch-reviewed by Opus, then the orchestrator commits, then an Opus or Fable branch review.
  - Variants cap fix loops: "one fix round max".
- Parallel ticket batches: `build-batch-1` built 4 tickets in parallel plus 2 stacked, in worktrees, tests first, one PR each. `build-nf353a-nf364-nf368` built 3 in parallel.
- Research: `agent-memory-research` ran 3 Haiku sweeps, a Sonnet shortlist of up to 5, one Sonnet verifier per candidate, then an Opus recommendation. `startup-landscape` covered 7 areas plus a synthesis.
- Repo-parallel maintenance: `lockfile-security-upgrades` (one agent per repo, then an Opus review) and `codex-plugin-support` (4 repos, one agent each, then a PR).
- Small checks:
  - `nf322-plan-review`, `spec-decision-check` and `nf322-map` (one reader per area). The owner rejected this use (section 4).
- Sizes:
  - Build workflows: 3 to 6 tasks per wave, 3 waves.
  - Research workflows: 3 sweeps, then 5 or fewer verifiers.
  - Repo-parallel workflows: 4 repos.
  - Scripts are 3.5 to 17.5 kB. The owner asked for workflows explicitly: "use workflow with sonnet subagetns", "subagent drivne sonnet within a workflow pls ideally", and "QoL in parallle with a workflow: opus for speccing + planning then another opus for plan+spec review and then sonnet for parallelized impl in task batches and one final opus review".

## 4. Authorization patterns: what the owner approves, refuses and corrects

Default model assignment, stated repeatedly and close to policy:
- Sonnet implements: "subagent driven using sonnet pls" and "start impl pls wiht sonnet", 58 messages.
- Opus reviews: "opus review the spec pls", 45 messages.
- "ideally i wanna use sonnet subagents for impl"
- "we always want the implementr model to do the edits review only generates a review"
- "for subagetns for research and shit use sonnet + haiku combos (haiku for finding sources, sonner for reading)"
- "we cna let the opus model use haiku for fetching sources an then sonnet to read"

Corrections, quoted (expletives kept where they carry the strength of the objection):
- Fan-out and nesting:
  - "too many subagents dawg"
  - "ok so cna we instruct the … review agenms to not spawn their own?"
  - "please make it so that agents dont spawn more subagents"
- Workflow scope:
  - "bruh wtf why a … workflow for the a … spec check?"
  - "i stopped the workflow u started how about just ONE opus agent for spec checks"
  - "lets not do workflow for every small thing?"
- Review loops:
  - "pls do make sure we dont have workflows spawning too many re-review + fix shit just try and be quick"
  - "how many re-reviews and fix passes bro"
  - "opus review agent on task waves if possible not on each task"
- Fable:
  - "pls use fable only when i explicitly ask for it"
  - "no not fable use opus dawg"
  - "lets not use fable anymore? … for this specific task … we have some huristics for when to use what model"
  - The owner does order Fable for one-shot unbiased second opinions: "ask fable to do a 1shot pass … unbiased not trying to criticize or praise", "fable advisor on this one pls". All 34 Fable calls are reviews or advice.
- Other providers and models:
  - "why are you using codex for the impl" / "use sonnet ffs"
  - "bruh dont do codex reviews"
  - "bruh use … sonnet"
  - On Haiku as a reviewer: "we dont use haiku … haiku is a glorified replacement for grep". This was about product model choice, but it matches how Haiku is actually used: source finding only.
- Delegating what the agent should do itself:
  - "why didnt you just write it yourself?"
  - "just execute the commands yourself"
  - "you can add tests-amended yourself why do you gotta ask me?"
- Heavy or idle work:
  - "NEVER let subagetns run unity shit please / this machine is very weak"
  - "lets hold off on these like heavy … runs / im stopping that general agent"
  - "shut down the agents that are done pls cause we have some subagents that are idle"
  - "the task agents are … idle"
  - "the workflow has been at it for an hour"
  - "check for any other shit that's idle and jsut wasting money"
  - Cost: "opus 5.5 for very high stuff as well but it costs like a mf"
- Explicit up-front approvals of fan-out. These were always tied to a named artifact or count:
  - "spawn like 3-4 opus subaegtns to think about it and formulate plans/specs"
  - "launch one opus agent for exploring each of those" (repos)
  - "use two Explore subagents in parallel"
  - "Once specs come back do like a fable review for each … followed by a workflow to impl" (an overnight hand-off)

## 5. Recurring task types an unattended agent could run from a fixed spec

1. Web research before a stack or vendor choice. This is already a global rule. 106 owner messages ask for research or "check online", and 186 research descriptions appear. The prompt is a stable template: date, context, "cite URL and date", no files.
2. Independent review of a spec, plan, PR or branch. This is the single most-requested delegation. The template is read-only, does not spawn, and returns verdicts.
3. Implementation of plan tasks with per-task review, from `docs/superpowers/plans/*.md`. One brief file per task in `.superpowers/sdd/…`.
4. Blind tests from an acceptance spec (`specs/NF-….md`). This already maps to the linear-agents `tester` skill.
5. Scoped re-review of one fix round, capped at one round.
6. Code mapping or exploration before planning ("map where localization happens", "inventory Dagster code split").
7. Repo-parallel mechanical maintenance: dependency or CVE bumps, manifest additions, triage of Dependabot PRs.
8. Transcript or log analysis (this task; also "gemini API logs" audits).

Two candidates have only weak evidence:
- A spike in a throwaway VM: there were 18 stoat MCP calls, and "your spike would also be good". Spikes mostly ran in scratch directories (`cat-spike/`).
- Pixel art: about 51 calls, owner-requested ("ask an opus agent that specializes in pixel art"). It is personal and taste-gated, so it should not be automated.

## 6. Proposed catalogue

Global rules apply to every entry:
- No nested spawning: the prompt says so and the tool is denied.
- Read-only unless the entry says otherwise.
- Stop and close agents when done.
- No Unity or other heavy local runs.
- A brief or contract file in the repo is the prompt source. The issue names its path.

| Name | Purpose | Model | Tools | Max fan-out | Runs without asking when |
|---|---|---|---|---|---|
| `source-finder` | Find dated, URL-cited sources; no judgement | haiku | WebSearch, WebFetch | 3 per question | Issue or plan has an open question or a dependency/stack choice |
| `source-reader` | Read and verify finder sources against the question; return a cited summary | sonnet | WebSearch, WebFetch, Read | 1 per shortlisted candidate, ≤5 | Same; only after `source-finder` |
| `code-mapper` | Map code areas relevant to a spec before planning | sonnet (Explore) | Read, Grep, Glob, read-only Bash | 1 per area, ≤4 | Spec exists; plan not written yet |
| `blind-tester` | Write failing tests from `specs/NF-*.md` only; must not read implementation | sonnet | Read, Write/Edit in test dirs, Bash (tests) | 1 per issue | Issue has acceptance criteria (existing `tester` skill) |
| `task-implementer` | Implement one plan task from its brief, in a worktree | sonnet | Read, Edit, Write, Bash; worktree isolation | ≤4 parallel, disjoint files only | A merged plan with task briefs exists |
| `task-reviewer` | Spec + quality gate for one task or one wave | sonnet per task, or opus per wave | Read, Grep, git diff, one focused test | 1 per wave (prefer over per task) | After each implementer wave |
| `fix-rereviewer` | Verdict each finding and the fix diff only | sonnet | Read, git diff, one focused test | 1 | At most one fix round per task, then escalate |
| `opus-reviewer` | Adversarial review of a spec, plan, PR or whole branch | opus | Read, Grep, `gh pr diff`, tests | 1 per artifact | Before a spec→plan step, before a PR is marked ready |

Named workflows:
- `sdd-waves`: blind tests, then up to 3 waves of `task-implementer` (≤4 each), with one `task-reviewer` (opus) per wave, one fix round, then one `opus-reviewer` on the branch, then a PR.
  - May run unattended only when the plan has 4 or more tasks.
  - With fewer tasks, the session runs the same steps inline with single agents. The owner rejected workflows "for every small thing".
- `research-sweep`: up to 3 `source-finder` agents, then a shortlist of 5 or fewer, then `source-reader` per candidate, then a single opus synthesis.
  - May run unattended when the issue asks a research question.
- `repo-parallel-chore`: one sonnet agent per listed repo (4 or fewer), then one `opus-reviewer`, then one PR per repo.
  - Only for repos and changes the issue names explicitly.

Always requires explicit authorization:
- Fable on any task: "use fable only when i explicitly ask".
- Opus for implementation. Haiku for anything except source finding.
- Non-Claude models or harnesses (Codex, GPT).
- Any subagent spawning its own subagents.
- A workflow for a single check, or for work outside a merged spec or plan.
- Fan-out above the caps, or a second fix or re-review round.
- More than one opus review per artifact.
- Agents that run Unity, heavy builds or training, or create VMs or cloud resources.
- Agents that merge, push to main, release, or edit Linear issues other than their own.
- Art or design agents.
- Long-running background agents with no completion condition.
- Any task type outside this catalogue.
