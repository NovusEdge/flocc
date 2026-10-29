# Architecture

This page describes how flocc is built, for someone deciding whether to run it or change it.

## Two programs

| Program | Language | Runs on | Owns |
|---|---|---|---|
| `flocc` desktop command (`installer/`) | Go | Your desktop | Hosts and their answers, keys, shipping the service, the dashboard, `host create` |
| `flocc` service (`src/flocc`) | Python, standard library only | Each host | Polling, worktrees, sessions, publishing, the digest |

The desktop command also forwards any other command line to a host, over SSH, as the service user. On a host, the Python `flocc` is installed only for the service user. On the desktop, `flocc` on your `PATH` is the Go binary.

`flocc ship` builds the installer for the host's architecture from the checkout you run it in, and sends the checkout's commit as a git bundle, so a host needs no credential for the repository.

## What runs on a host

One Python program runs on a systemd timer, every 3 minutes, holding a file lock so two ticks never overlap. It starts one interactive Claude Code session per issue, in tmux, on the same machine. The tmux server runs under its own lingering systemd user unit, so sessions outlive each tick. On a host without systemd, `flocc run` in tmux ticks instead.

| Part | Does |
|---|---|
| Poller | Finds ready issues, applies admission checks, moves Linear status |
| Launcher | Makes the worktree, runs `./setup`, installs the pre-commit hook, starts a session in tmux with a clean environment |
| Supervisor | Restarts a crashed session once, enforces wall-clock limits, cleans up crashed issues |
| Publisher | Runs the check, guards the tester's files, pushes, opens the draft PR, comments on Linear |
| Digest | Posts the daily digest comment, which also carries problems no issue owns: a failed tick, an expiring token, a lapsed Claude login |
| Claims ledger | SQLite in WAL mode: issue, session, dependencies, state |
| Pre-commit hook | Rejects commits made by the review session |
| Agent plugin | The tester, implementer and reviewer skills and the hooks sessions run |

Agents write code and commit in their worktree. They never push, open PRs or change Linear. The service does that.

## One issue, start to finish

1. **Pick.** The poller reads the issues assigned to the Linear key's owner that carry the trigger label and are in an unstarted state. It maps each to a repository by label or project, and skips an issue without acceptance criteria.
2. **Admit.** A new issue starts only if usage is below the thresholds, fewer than `session_cap` sessions run and its `depends_on` issues are done.
3. **Prepare.** `git worktree add` on branch `agent/<key>`, reusing the branch of an earlier run, then `./setup`, then the pre-commit hook. The service then writes the claim and moves the issue to In Progress.
4. **Test.** A tester session writes tests that pass only on correct behavior, commits them, writes a handoff note and exits.
5. **Implement.** A fresh session works from the issue and the handoff note until the tests pass.
6. **Publish.** The service rejects the run if the implementer changed files the tester committed. It runs the repository's check, pushes, runs `gh pr create --draft` with `Fixes <KEY>` or reuses the open PR on a retry, and comments the PR link on the issue.
7. **Review.** A read-only session on the same worktree checks the diff against each acceptance criterion and the repository's `AGENTS.md` and `CLAUDE.md`. It writes findings that end in `Verdict: approve` or `Verdict: changes requested`. The service posts them as a PR comment headed `flocc review`, using the publish token, and comments the verdict on the issue.
8. **Review fix round.** On `changes requested`, once per claim, a fix session starts on the same branch with the review as its context. It fixes what is agent work and lists what needs you (a manual run, a product decision, a question for the lead) under "Needs the owner" in its summary. The service pushes to the open PR and the reviewer runs again; a second review is posted as a new PR comment. A second `changes requested` ends the claim, and the issue comment repeats the "Needs the owner" lines. Over the usage limits, the round is skipped. You merge by hand.

Sessions are named after the issue (`ENG-12`), with `-test` and `-review` suffixes for the tester and reviewer. A review that cannot start, dies twice, hits its wall clock or cannot be posted leaves a digest line and releases the claim. It never blocks the PR.

A session's state lives outside its context: commits in the worktree, a handoff note in its state directory, and the Linear issue. An interactive session does not exit when it finishes, so each phase ends with the agent running `flocc done`, which the supervisor picks up.

### Failures

| Failure | Response |
|---|---|
| Session process gone while its claim is live | Restart once. On a second failure the claim blocks and the service comments on the issue |
| Wall-clock limit reached | Kill the session, keep the branch, report in the digest |
| The check fails | The same check runs on the base branch. If it fails the same way there, the repo is held as main-broken and no fix round starts. Otherwise up to `check_fix_rounds` (default 2) fresh implementer sessions, each with the failure output. After the last one fails, the claim blocks and the service comments on the issue |
| An open PR conflicts with its base | One session merges the base and resolves the conflicts, then pushes to the same PR. A second conflict on the same base commit blocks |
| Usage limit hit mid-run | The session waits for the reset. Nothing new is admitted until usage is below the thresholds |
| Agent needs a decision | It takes the spec's default and lists what needs the owner under "Needs the owner" in its summary. An idle session gets an idle notice on the issue |
| A Linear, git or tmux call fails partway through a step | The claim's state changes only after the step's side effects succeed, so the next tick repeats the step |

A blocked or timed-out issue keeps its claim until you release it. Release moves it back to Todo, and the next tick continues on the kept branch.

## Coordination between issues

- An issue has at most one live claim, and the poller skips claimed issues.
- `depends_on` holds an issue until the issues it names are done in Linear.
- Each host keeps its own ledger. To use several hosts, map every repository on exactly one. See [Run a host](setup/running-a-host.md#running-more-than-one-host).

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
