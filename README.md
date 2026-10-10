# linear-agents

A personal setup, parked for later: Claude Code picks up Linear issues that are assigned to
me and carry a trigger label, works on them in the right repo, and opens a draft PR. It is
for my own use, not team infrastructure.

Research by an Opus agent on 2026-10-08. The Claude Code docs it read match CLI v2.1.294.
Linear, Claude support and Merlin pages came through a fetch tool that returns a model's
summary, so their wording is not exact. Unconfirmed items say so.

## Before building

- **Terms on company code.** The Consumer Terms served in the EEA say "Non-commercial use
  only", and forbid scripted access "Except when you are accessing our Services via an
  Anthropic API Key or where we otherwise explicitly permit it" [T1]. The docs offer
  `claude setup-token` "for CI pipelines and scripts" [C2], and say plan limits "assume
  ordinary, individual usage" [C8]. For Nimble Fox repos, use a company API key, a Team seat,
  or Max's monthly API credit through a Console key; whether that credit is meant for work
  for an employer is unconfirmed. Agree it with the company first.
- **Never put my token in a repo secret** or run an agent teammates can delegate to: that
  shares my account [T1][C6].
- Consumer data can be used for training when that setting is on [C9].

## Linear

- Assigning an issue to an agent makes the agent its delegate; the human stays assignee. An
  agent is an OAuth app a workspace admin installs; the API is a Developer Preview [L1][L2].
- Webhooks need admin permissions. Members get personal API keys only if an admin allows
  it; keys can be limited to chosen teams and to read or write, at 2,500 requests an hour
  [L3][L4][L9]. Without admin rights, polling is the only trigger.
- Linear's own coding sessions run Claude Code or Codex in a sandbox Linear manages and draft
  a PR. They need Basic or higher and are paid from the workspace's AI credits: $0.25 per
  20 minutes of sandbox plus model tokens [L5][L6].
- Codex has an OpenAI-built Linear integration [L7]. Claude Code reaches Linear only through
  third parties such as Cyrus, Merlin and Boxes.dev [L8]; Anthropic's feature request is open
  [A1].

## Ways to run Claude Code unattended

| Mode | Started by | Runs on |
|---|---|---|
| `claude -p`, Agent SDK | My script | My machine or a container |
| `claude --cloud "task"` | Any CLI signed in to claude.ai | Anthropic VM [C4] |
| Routines | API `/fire` POST with a bearer token; a schedule, at most hourly; GitHub PR or release events | Anthropic cloud [C5] |
| GitHub Action | Any GitHub event, including dispatch | GitHub runner [C6] |

Routines are a research preview, have no permission-mode setting or per-run caps, and act as
me on GitHub and Linear [C5]. `claude -p`, the Agent SDK and routines use subscription
limits; Max includes $100 or $200 a month of API credit for a key from a linked Console org,
Pro does not [S1][S2]. Current limit numbers were not found; `/usage` shows mine.

| | My machine or a VM | GitHub Actions | Anthropic cloud |
|---|---|---|---|
| Secrets | Token on disk | Repo secret the team can see | None in the VM; a GitHub proxy holds the credential [C7] |
| Isolation | Container plus auto mode; skip permissions only in a container as non-root [C10] | Fresh runner each time | Anthropic VM with allowlisted network |
| Repo rules | AGENTS.md, hooks and plugins load unless `--bare` [C1] | Through the action's inputs | CLAUDE.md loads; plugins declared in the repo do not; `gh pr` is blocked [C7] |
| Reliability | Machine must stay on | Good | Preview [C5] |

## Current design

The design is in `docs/superpowers/specs/2026-10-09-linear-agents-design.md`, the build plan in
`docs/superpowers/plans/2026-10-09-linear-agents.md`, and host setup in `docs/vm-setup.md`. The
sections below are the original research.

## Recommended: a poller with headless Claude Code in a container

Needs no Linear admin and loads the full repo setup. Costs a short script to maintain.

1. A Linear personal API key limited to the NF team, and a fine-grained GitHub token [L4].
2. Auth: Claude Code signed in with my claude.ai subscription, no API key. In the container
   that means `claude setup-token` [C2]. For company repos, see Before building.
3. A systemd timer runs the script every 3 minutes. It finds issues assigned to me with the
   `claude` label that are not started and have the agent-ready acceptance criteria, moves
   each to In Progress, maps the label or project to a repo, runs `git worktree add` and
   `./setup`. An issue without acceptance criteria gets a comment and is skipped.
4. In the reference dev container, as a non-root user:
   `claude -p --permission-mode auto --permission-prompts none --max-turns 60 --max-budget-usd 6` [C3],
   twice: a tester session that first commits tests which pass only on correct behavior, then
   a fresh implementer session.
   The caps are suggested values, not tested.
5. The script, not the agent, runs `just check`, names the branch, pushes, runs
   `gh pr create --draft` with `Fixes NF-123` and the labels, and comments the PR link on the
   issue. A design question becomes a Linear comment and ends the run. Each issue is locked
   so it runs once.

## Alternatives

**An Anthropic routine.** No machine of mine. Checks at most hourly unless the poller calls
its API with the issue ID in the `text` field, and the routine's prompt must say to act on
it. Plugins declared in the repo do not load [C5][C7]. Steps: `/web-setup`; an environment
that runs `./setup` (whether default network access is enough is unconfirmed); a routine with
the repos, only the Linear connector, and a prompt that states the whole task.

**Self-hosted Cyrus** (`cyrusagents/cyrus`, Apache-2.0, v0.2.73 on 2026-09-30): a worktree
per issue, repo routing by label (`routingLabels`), a tool set per label, setup scripts and a
network-limited sandbox; uses an API key or `CLAUDE_CODE_OAUTH_TOKEN`. Needs an OAuth app a
Linear admin creates and a public URL for webhooks, and the whole workspace sees it, so it
should run on a company key [G1]. Steps: `npm i -g cyrus-ai`; the OAuth app with webhooks to
`/linear-webhook`; `cyrus self-auth-linear`; `cyrus self-add-repo`; `routingLabels` and
`allowedTools`; a systemd unit.

Merlin runs on my own machines with my own subscription, but launched 2026-09-25 and its
Linux support is unconfirmed [M1]. If the team pays, Linear's coding sessions need no runner
at all [L5].

## Sources

- Linear (linear.app): L1 `/developers/agents`, L2 `/developers/agent-interaction`,
  L3 `/developers/webhooks`, L4 `/docs/api-and-webhooks`, L5 `/docs/coding-sessions`,
  L6 `/docs/ai-credits`, L7 `/integrations/codex`, L8 `/integrations/agents`,
  L9 `/developers/rate-limiting`
- Claude Code docs (code.claude.com/docs/en/): C1 `headless`, C2 `authentication`,
  C3 `cli-reference`, C4 `claude-code-on-the-web`, C5 `routines`, C6 `github-actions`,
  C7 `cloud-environments`, C8 `legal-and-compliance`, C9 `data-usage`, C10 `permission-modes`
- Claude support: S1 support.claude.com/en/articles/15036540 (updated 2026-10-07),
  S2 support.claude.com/en/articles/17154008
- T1 anthropic.com/legal/consumer-terms (effective 2025-10-08)
- G1 github.com/cyrusagents/cyrus
- M1 usemerlin.ai/changelog
- A1 github.com/anthropics/claude-code/issues/12925
- Nimble Fox conventions the agent must follow: `nimblefox-docs` shared rules and the repo
  template, from the setup plan of 2026-10-08
