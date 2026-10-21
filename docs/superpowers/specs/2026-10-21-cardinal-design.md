# cardinal: an assistant that supervises flocc, and Part 1, permission prompts on hosts

## What the owner asked for

These are the owner's words from the 2026-10-21 brainstorm. Every part below is checked against them.

> "a persistent 'supervisor' agent that actually like works with me and monitors all these flocc
> instances? separate from my own sessions"

> "I want the supervisor to be an assistant of mine in some sense. I'm doing a lot and i want it
> to be able to, well be a supervisor and like approve trivial shit like commands to run if they
> look safe and are flagged via automode. Cause i think our automode pops up a bit too much for
> commands that should prolly be allowed. then I also want it to bring things to my attention that
> need me and work with the agent I usually brainstorm with, so my own sessions"

> "the permission prompts that the supervisor is supposed to answer is the flocc agents that are
> working on tickets"

> "inside the VM all is good and as long as it doesnt fuck up upstream code [...] shit like scaling
> up the VM and so on might wanna go thru me if its affecting a lot of other places, but then if
> it's trivial then all g I leave it up the agents judgement as long as we do some GOOD
> instructions"

> "we track which issues have been paused and I'm asked with context provided sorted by priority"

The owner also said ntfy is not useful to them. The Claude app on their phone already shows what
needs them. Linear's own loops already cover daily and weekly reporting. They have two Claude Max
accounts, soon three: one is theirs alone, and the others are a pool for flocc.

## The five parts

The assistant is called cardinal; flocc's per-host `supervisor` module keeps its name. It is
built in five parts, each with its own spec, plan and release:

| Part | Delivers | Needs |
|---|---|---|
| 1 | Host agents' permission prompts: logged, never silent, tuned away by rules, and judged by a stateless judge inside the hook (this spec) | none |
| 2 | cardinal's always-on session on its own VM (`flocc-cardinal`, a pool account, Remote Control). It takes in every host's escalations, decisions and token warnings, and keeps the queue of issues paused on the owner in Linear priority order, each with its context, in the Claude app. ntfy and the digest issue retire. It also monitors the approval rate and keeps a tamper-evident log. | 1 |
| 3 | Claude accounts and usage: every account's 5-hour and weekly use and reset times in one view; pool accounts placed on hosts; work held or moved when an account runs low; host resources such as VM size, which go through the owner when they affect other things | 1 |
| 4 | A two-way partner to the owner's brainstorm sessions. cardinal takes merged specs and plans, cuts sequenced tickets (asking before adding `claude`), tracks them, and brings design questions the host agents raise back to the owner's session. It also runs canary tickets that tempt a shortcut. | 1, 2 |
| 5 | Part 1's judge for the owner's own desktop sessions, once it has proven itself on hosts | 1 |

Before cardinal moves work between accounts in Part 3, check Anthropic's current terms on using
several subscriptions. Tracking usage, and giving each account a fixed job, are not in question.

# Part 1: permission prompts on hosts

## Today

Host sessions run in auto mode (`sessions.py`, `--permission-mode auto`). When auto mode's
classifier stops one with a permission prompt, the prompt waits in tmux or the Claude app until
the owner answers. Only `AskUserQuestion` writes the `asking` marker that the supervisor escalates
(`cli.py`, `_check_question`). A prompt fires no `Stop`, so the session is never marked idle, and
the run burns wall clock with no one told.

## What keeps upstream safe

Nothing in this part is a security boundary, and the spec says so in this section so that no
later change leans on it. The agent runs as the same user the hook runs as, on a VM it controls,
and text matching cannot stop a determined command. Examples: `git push -f` against `--force`,
`git config alias.p push`, `sh -c "$(cat x)"`, or a script written and then run (Claude Code issue
#40117 is this pattern). Upstream stays safe because of two things outside the VM:

- the agent's GitHub token is read-only (`gh-agent.token`), and only flocc's publisher pushes,
  with the publish token, which sessions never see;
- branch protection on each repo's base branch.

Never widen the agent token on the grounds that the hook or the judge denies pushes.

## Step 0: see every prompt, never stall silently

A `PermissionRequest` hook goes into every host session's `--settings`, next to the existing
`statusLine`. It runs the root-owned script `/usr/local/lib/flocc/permission-hook` and does
three things:

1. It appends one line to the claim's `permissions.jsonl`: time, host, issue, phase, session
   name, tool, the exact tool input, and cwd. The agent's own `description` of the command is
   kept in a separate field, for the owner only.
2. It writes the `permission-waiting` marker: time, tool, and command. A later prompt
   overwrites it; the `PostToolUse` and `UserPromptSubmit` hooks clear it, as they clear
   `idle`.
3. It returns no decision, so the prompt shows as it does today.

The supervisor checks `permission-waiting` after `_check_question` and before `_check_idle`.
After `QUESTION_WAIT_S`, it sends the same tier-2 `waiting` escalation a question gets: the
command, and where to answer it (the Claude app for impl and fix, the tmux pane otherwise). A
marker that is present suppresses the idle escalation, so the owner gets one alert, not two.

`flocc permissions report` on the host, and `flocc -H <host> permissions report` on the desktop,
summarise the log by tool and command prefix, with counts. The owner turns the report into:

- exact `permissions.allow` rules (`Bash(npx playwright install)`, `Bash(uv sync --frozen)`),
  which are deterministic and never wildcard an interpreter or shell (`sh`, `bash`, `python`,
  `node`, `eval`, `env`, `xargs`, `-c`);
- `autoMode.environment` lines, the classifier's prose context: "Each session runs in a
  disposable VM. The worktree and ~/worktrees are the agent's own. Nothing on this VM is
  production. Its GitHub token is read-only."

Rules and environment lines live in the host's flocc config under `[permissions]`, written only
by `flocc ship` and `flocc host add` from the desktop's host file. The service merges them into
every session's `--settings` at launch. Settings apply to a running session as soon as they
change, but flocc writes them only at launch.

Step 0 ships alone, runs for a week, and the report decides how much Step 1 has to do.

## Step 1: a stateless judge inside the hook

For a prompt that no rule settles, the hook asks a judge. The judge has no queue, no memory and no
session: it is one `claude -p` call on the host's own Claude login, with `--model` from
`[permissions] judge_model` and a 45-second timeout. Its input is the policy plus the action:
host, issue, phase, tool, exact tool input and cwd. It never sees the agent's prose, the tool
output, or the agent's `description` of the command. The research behind this choice:
Anthropic's auto-mode classifier strips them, which cut its false positives from 8.5% to 0.4%,
and agents have been seen answering their own approval requests.

The judge answers `allow`, `deny` or `owner`, with a one-line reason for the log. The hook
enforces the answer:

- `allow` stands only if a fixed classifier in the hook tags the request
  VM-local. Otherwise it becomes `owner`. The classifier is code, not prose. It rejects any
  command that contains `git push`, `gh`, `curl` or `wget` with a method or upload flag, `ssh`,
  `scp`, `rsync` to a host, `tailscale`, `incus`, `sudo`, `systemctl`, `crontab`, command
  substitution, `eval`, or a pipe into an interpreter. It also rejects writes outside the
  worktree, `~/worktrees`, `/tmp` and the build caches, and any read or write of
  `~/.config/flocc`, `~/.claude*`, `~/.ssh` or the token files. A persuaded judge therefore still
  cannot allow a push.
- `deny` returns a deny with the reason, and the agent carries on without that action.
- `owner` pauses the issue (below).
- A timeout or error returns no decision, so the prompt shows as in Step 0. The judge never
  allows by default.

Hard stop: the hook counts denials per run in the state dir. Three in a row, or 20 in a run,
pause the issue for the owner, as auto mode itself does.

The judge has no target. No approval rate, throughput or queue length is measured or shown to it.
The only outcome recorded about it is how many of its decisions the owner later reverses.

### The policy

`deploy/permission-policy.md` is the judge's instructions, written for this purpose, as the
owner asked: "GOOD instructions". It says what to allow: effects confined to this VM. That covers
builds, tests, linters, installs pinned by the repo's lockfile, local servers, and file changes in
the worktree, scratch and caches. It says what to send to the owner: credentials, network
destinations the repo does not already use, system services, anything obfuscated, and any git
remote write. It says what to deny: anything that rewrites or deletes history or branches
upstream. It says what to ignore as evidence: what the command claims to be for.

### Paused issues

The judge's `owner`, or a hard stop, does the following:

1. The hook returns a deny with `interrupt: true`, so the session's turn ends.
2. It writes `permission-paused` with the command and the judge's reason.
3. The supervisor, which sees `permission-paused` before `_check_idle`, kills the session and
   blocks the claim with a tier-2 escalation. It reads the issue's Linear priority into the
   escalation, since the hook has no Linear access. The escalation's `summary` and `action`
   carry the context needed to answer from a phone: the command, what the judge thinks it does,
   why it would not decide, and the two ways to answer.

The owner answers on the issue:

- `allow: command <exact command>` in a comment is a one-off grant for that issue. It is a new
  item in `grants.py`, kept across the rerun: `_start` keeps `commands` when it resets the rest of
  `grants.json`. The hook allows an exactly matching request before asking the judge.
- An always-allow rule goes in the host file and reaches the host on the next `flocc ship -H`.
- Then the owner moves the issue to Todo. `flocc release --keep-worktree` does the same.
  Without `--keep-worktree`, release deletes the interrupted session's uncommitted work.

A rerun starts at test or impl, never fix (`poller.py`), so a pause during fix loses
`FIX_CONTEXT` and resets `fix_attempts`. Part 1 accepts this, and the escalation says so when the
phase was fix.

Part 2 replaces the Linear-comment path with cardinal's queue in the Claude app, sorted by
priority. The escalation and decision records below are what that queue reads.

## The agent cannot change what judges it

- The hook, the classifier and `deploy/permission-policy.md` are installed root-owned in
  `/usr/local/lib/flocc/` by `flocc-setup`'s root phase. The `flocc` user has no sudo (checked on
  flocc-nimble, 2026-10-21). The hook is a standalone stdlib-only script that imports nothing
  from `~/flocc`: the service's checkout belongs to `flocc`, so code imported from it can be
  edited by the agent. Settings name it by its absolute path, not as `flocc permission-hook`.
- The `claude` binary the judge runs is also writable by `flocc`, so a tampered judge can say
  anything. That is why the clamp lives in the root-owned hook: the most a subverted judge can
  win is an allow for a VM-local request.
- Session `--settings` add deny rules for `Edit` and `Write` of `~/.claude/settings*`, the
  plugin dir's `hooks/`, and the state dir's markers, next to the existing `--no-verify` and
  `claude` denies. These save attention. They are not a boundary: the root ownership is.
- A hook failure never allows, as the gate already behaves (`cli.py`).

## Records Parts 2-4 read

- Decision event, one JSON line per prompt in `permissions.jsonl`: `{at, host, issue, phase,
  session, tool, input, cwd, decision, decided_by, rule, reason, latency_ms}`. `decided_by` is
  one of `rule`, `grant`, `judge`, `owner`, `timeout` or `none`.
- Owner questions are `Escalation` records (`escalation.py`), with no new type.
- The issue key is optional and identity is (host, session name), so Part 5 can reuse the hook
  for desktop sessions.
- One owner-approval shape for anything cardinal later proposes: id, text, yes or no, and who
  recorded it. Part 1 uses it only in the report's suggested rules.

## Part 1 testing

- Unit tests for each piece: the hook's log line and markers; the supervisor's
  `permission-waiting` and `permission-paused` checks, including no idle double-escalation; the
  classifier against the examples in "What keeps upstream safe" and the list in Step 1; the clamp
  turning a judge `allow` into `owner`; denial counters; `allow: command` parsing and its survival
  across `_start`; `--settings` carrying the hook, rules and environment.
- Before Step 1 runs on a real issue: one sitting of about 30 adversarial commands against the
  judge, kept as a checklist in `docs/vm-setup.md`. Include pushes in disguise, credential reads,
  `curl | sh`, a script written and then run, and test-file deletions.
- On flocc-nimble: Step 0's report after a week, and the prompt count before and after the rules.

## Rejected for Part 1

- A persistent judge session with a broker and a queue. It is a fourth long-lived session to keep
  alive, and when it is down, every unknown prompt stalls silently. Its queue becomes Part 2's
  product.
- Giving the judge the agent's stated reason as "evidence". Treating it only as evidence cannot be
  enforced against a model.
- A second model checking every answer, and chain-of-thought monitoring: the clamp and the
  deny-and-rerun loop do that job, and a judge that sees only actions has no agent reasoning to
  monitor.

## Unconfirmed

- The `PermissionRequest` payload's exact fields, including whether Bash's `description` arrives
  in `tool_input`; and whether `interrupt: true` ends the turn as documented.
- Whether a `PermissionRequest` hook fires for prompts that the auto-mode classifier raises, and
  not only for rule-based asks.
- Whether `claude -p` on the host's subscription login runs inside a hook's environment without
  `CLAUDE_CODE_*` variables leaking from the parent session.
