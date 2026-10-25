# Architecture

This page describes how flocc is built, for someone deciding whether to run it or change it.

## Two programs

| Program | Language | Runs on | Owns |
|---|---|---|---|
| `flocc` desktop command (`installer/`) | Go | Your desktop | Hosts and their answers, keys, shipping the service, the dashboard, `host create`, cardinal's setup |
| `flocc` service (`src/flocc`) | Python, standard library only | Each host | Polling, worktrees, sessions, publishing, escalation |

The desktop command also forwards any other command line to a host, over SSH, as the service user. On a host, the Python `flocc` is installed only for the service user. On the desktop, `flocc` on your `PATH` is the Go binary.

`flocc ship` builds the installer for the host's architecture from the checkout you run it in, and sends the checkout's commit as a git bundle, so a host needs no credential for the repository.

## What runs on a host

One Python program runs on a systemd timer, every 3 minutes, holding a file lock so two ticks never overlap. It starts one interactive Claude Code session per issue, in tmux, on the same machine. The tmux server runs under its own lingering systemd user unit, so sessions outlive each tick. On a host without systemd, `flocc run` in tmux ticks instead.

| Part | Does |
|---|---|
| Poller | Finds ready issues, applies admission checks, moves Linear status |
| Launcher | Makes the worktree, runs `./setup`, installs the pre-commit hook, starts a session in tmux with a clean environment |
| Supervisor | Restarts sessions from their handoff note, enforces wall-clock limits, cleans up crashed issues |
| Publisher | Runs the check, guards the tester's files, pushes, opens the draft PR, comments on Linear |
| Notifier | Routes escalations, dedupes them, sends ntfy pushes, applies Linear labels, posts the daily digest |
| Claims ledger | SQLite in WAL mode: issue, session, claimed paths, dependencies, state |
| Pre-commit hook | Rejects commits that touch paths another live issue has claimed |
| Agent plugin | The tester, implementer and reviewer skills, the escalation rubric, coordination rules and handoff hooks |

Agents write code and commit in their worktree. They never push, open PRs or change Linear. The service does that.

## One issue, start to finish

1. **Pick.** The poller reads the issues assigned to the Linear key's owner that carry the trigger label and are in an unstarted state. It maps each to a repository by label or project, and skips an issue without acceptance criteria.
2. **Admit.** A new issue starts only if usage is below the thresholds, fewer than `session_cap` sessions run, its claimed paths do not overlap a live claim and its `depends_on` issues are done.
3. **Prepare.** `git worktree add` on branch `agent/<key>`, reusing the branch of an earlier run, then `./setup`, then the pre-commit hook. The service then writes the claim and moves the issue to In Progress.
4. **Test.** A tester session writes tests that pass only on correct behavior, commits them, writes a handoff note and exits.
5. **Implement.** A fresh session works from the issue and the handoff note until the tests pass. Past 60% context it writes a handoff note, and the supervisor restarts it from that note under the same Remote Control name.
6. **Publish.** The service rejects the run if the implementer changed files the tester committed. It runs the repository's check, pushes, runs `gh pr create --draft` with `Fixes <KEY>` or reuses the open PR on a retry, and comments the PR link on the issue.
7. **Review.** A read-only session on the same worktree checks the diff against each acceptance criterion and the repository's `AGENTS.md` and `CLAUDE.md`. It writes findings that end in `Verdict: approve` or `Verdict: changes requested`. The service posts them as a PR comment headed `flocc review`, using the publish token, and comments the verdict on the issue.
8. **Review fix round.** On `changes requested`, once per claim, a fix session starts on the same branch with the review as its context. It fixes what is agent work and lists what needs you (a manual run, a product decision, a question for the lead) under "Needs the owner" in its summary. The service pushes to the open PR and the reviewer runs again; a second review is posted as a new PR comment. A second `changes requested` ends the claim, and the issue comment repeats the "Needs the owner" lines. Over the usage limits, the round is skipped. You merge by hand.

Sessions are named after the issue (`ENG-12`), with `-test` and `-review` suffixes for the tester and reviewer. A review that cannot start, dies twice, hits its wall clock or cannot be posted leaves a digest line and releases the claim. It never blocks the PR.

A session's state lives outside its context: commits in the worktree, a handoff note in its state directory, and the Linear issue. An interactive session does not exit when it finishes, so each phase ends with the agent running `flocc done`, which the supervisor picks up.

### Failures

| Failure | Response |
|---|---|
| Session process gone while its claim is live | Restart once from the latest handoff note. On a second failure, `needs-decision` and a tier 2 escalation |
| Wall-clock limit reached | Kill the session, keep the branch, report in the digest |
| The check fails | The same check runs on the base branch. If it fails the same way there, the repo is held as main-broken and no fix round starts. Otherwise up to `check_fix_rounds` (default 2) fresh implementer sessions, each with the failure output. After the last one fails, `needs-decision` |
| An open PR conflicts with its base | One session merges the base and resolves the conflicts, then pushes to the same PR. A second conflict on the same base commit blocks |
| Usage limit hit mid-run | The session waits for the reset. Nothing new is admitted until usage is below the thresholds |
| Agent needs a decision | It escalates with a default and a deadline and continues on the default, unless the decision is irreversible |
| A Linear, git or tmux call fails partway through a step | The claim's state changes only after the step's side effects succeed, so the next tick repeats the step |

A blocked or timed-out issue keeps its claim until you release it. Release moves it back to Todo, and the next tick continues on the kept branch.

## Coordination between issues

- An issue has at most one live claim, and the poller skips claimed issues.
- Claimed paths are checked at admission and again by the pre-commit hook. A file is claimed when it or a directory above it matches a claimed glob. An issue with no `paths` claims nothing up front, and its agent claims paths as it learns them with `flocc claim`.
- `depends_on` holds an issue until the issues it names are done in Linear.
- Agents message their peers with `SendMessage`. `flocc context` lists each agent's current peers in the same repository from the ledger.
- Each host keeps its own ledger. To use several hosts, map every repository on exactly one. See [Run a host](setup/running-a-host.md#running-more-than-one-host).

## A2A

Agents on different hosts, and the owner's desktop sessions, reach each other through A2A endpoints. Each host runs `flocc-a2a.service`, which serves the endpoint on the host's tailnet address, port 7443. A caller presents a bearer token. `flocc ship` mints the tokens and writes `~/.config/flocc/a2a.toml` on each host and on the desktop, listing the peers and, for each host, the digests of the tokens it accepts.

A message is stored in the receiving host's inbox, a SQLite file beside the ledger. It stays there until the issue's agent receives it or the issue closes. A message for an issue no host has claimed waits for the issue to start, and goes to the one host that serves the issue's repository.

The agent receives messages as `<flocc-message>` blocks, at one of three points:

- The `flocc inbox` hook, at a session's next tool call.
- The `flocc stop` hook, when a turn ends. It continues the turn with the message instead of letting the session go idle.
- The session's first context, when the issue next starts.

A message from the owner also types a one-line nudge into a session that is idle at its prompt, so it does not wait for a tool call.

The tick adds two kinds of message. When a PR opens or merges, each issue Linear lists as blocked by it gets a handoff naming the PR and its files, and a merge also notifies the repository's other live agents. The tick also drops pending messages whose issue has closed. See the [A2A design](../superpowers/specs/2026-10-24-a2a-layer-design.md) for the reasons behind these choices.

## Access and trust

| Holder | Linear | GitHub | MCP |
|---|---|---|---|
| Service | Personal API key | Fine-grained publish token with write to the mapped repositories | None |
| Agents | None. The issue text arrives in the prompt | Read-only fine-grained agent token for `gh` | Only servers named in the issue's `mcp` list |

The service and its agents run as one dedicated Unix user with no sudo and no groups, so your own account and its home are out of their reach. Agents share that user with the service, so they could read the Linear key and the publish token. The agent column in the table is behavioral, kept by the prompt, the skills, the pre-commit hook and a read-only token in `GH_TOKEN`. It is not an enforced boundary.

The launcher starts each session through `env -i` with only a few variables, then reads the agent's GitHub token from its file into `GH_TOKEN`. A session started from inside another Claude Code session would otherwise inherit its messaging socket, session ID and task list.

Because agents run unreviewed code, `flocc host create` puts them in a VM. The VM joins the tailnet tagged `tag:flocc-host`, and your policy grants that tag no destinations. See [Tailnet](tailnet.md).

## Desktop state

| What | Where |
|---|---|
| Hosts and their answers | `~/.config/flocc/hosts/<name>.toml` |
| Keys | OS keyring, else `~/.config/flocc/secrets.toml` (mode 600) |
| Host-side secrets | `~flocc/.config/flocc/`, mode 600 |
| Host-side ledger | `~flocc/.local/state/flocc/ledger.sqlite` |

The host file is the only source of a host's answers. `flocc ship` sends them, with the stored keys, as one TOML document on stdin over a session with no TTY. No secret is written to a file on the host except the service's own secret files, and none appears in a command line or log.

## Usage limits

Each session's status line command writes its rate-limit and context numbers to its state directory. The poller reads the newest values and admits no new issue at or above the thresholds. Extra usage stays off. Hosts share the owner's Claude login, so they share its limits.

## Why this shape

Sessions are interactive Claude Code sessions on a claude.ai subscription, not API-key or print-mode runs. Remote Control attaches only to interactive sessions, and `--max-turns` and `--max-budget-usd` work only in print mode. The supervisor therefore enforces limits on a session. The launcher strips `ANTHROPIC_API_KEY`, which would switch billing to the API.

Other ways to run Claude Code unattended exist, and flocc does not use them:

- A scheduled Anthropic routine needs no machine of yours, but it checks at most hourly unless the poller calls its API, plugins declared in the repository do not load, and routines were a research preview when this was written.
- A GitHub Action puts a token in a repository secret that the team can see.
- Self-hosted agent services such as Cyrus need a Linear admin to create an OAuth app and a public URL for webhooks, and the whole workspace sees the agent.
- Linear's own coding sessions need no runner, and the workspace pays for them in AI credits.

flocc needs no Linear admin rights: it polls with a personal API key, limited to the team, at 2,500 requests an hour.

{% hint style="warning" %}
Running a consumer subscription from a script on work repositories needs agreement from whoever owns the account and the code. The terms for consumer plans restrict automated access, apart from API keys and cases Anthropic permits. Check the current terms.
{% endhint %}

## cardinal

[cardinal](cardinal.md) is a separate session outside the VMs. It reads the fleet through the same `flocc -H all status` you run, and Linear through its MCP server, and reports to you in the Claude app. It has no more access to hosts than the key limited to the Incus bridge gives it, and the tailnet is blocked from its user.
