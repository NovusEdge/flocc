# Changelog

A release is a batch of merged PRs. The PR that cuts one bumps `version` in `pyproject.toml` and
`plugin/.claude-plugin/plugin.json` together and adds its entry here; the merge commit is tagged
`v<version>`.

## 0.4.0 (2026-10-20)

The host side of setup: ship never asks, and the Claude login is one command.

- `just ship <target> --answers FILE` runs its setup in one SSH session without a terminal. The answers
  document, secrets included, goes on stdin; no secret lands in a file other than the service's own
  secret files, in a command line, in `setup.log` or in the output. Ship checks for passwordless
  sudo first. `--interactive` keeps the terminal forms; `--skip-interactive` is gone.
- Ship no longer logs in to Claude. `just login <target>` does a full claude.ai login with
  `claude auth login --claudeai`, answers the Remote Control consent before it reports success and
  starts the timer, holding the tick lock throughout. It knows remote-control is up by its
  `Connected ·` status line (Claude Code 2.1.304); verified on flocc-nimble.
- Ship ends with a summary (version, Claude login, GitHub token expiries, failed doctor checks,
  timer, live sessions) and exits non-zero only when a check other than a pending login fails. The
  login row names the next command with the host you gave ship: `just login <target>`.
- The tick pages once, tier 1, when the Claude login lapses. `flocc status --json` names the tick
  lock. A transient `claude auth status` error (JSON without `loggedIn`) is not read as a lapse and
  does not page.
- The service phase removes an answers file a 0.3.0 run left in the host's stage, secrets included.

## 0.3.0 (2026-10-20)

linear-agents is now flocc. A host set up before 0.3.0 is replaced by a new one, not upgraded.

- Every name changed: package, CLI and plugin `flocc`, env vars `FLOCC_*`, service user `flocc`
  with its repo in `~/flocc` and worktrees in `~/worktrees`, units `flocc.{service,timer}` and
  `flocc-tmux.service`, installer `flocc-setup`, commit trailer `Flocc:`, comments starting
  `🤖 flocc`. The full table is in `docs/superpowers/specs/2026-10-20-flocc-design.md`.
- `just ship` refuses a host that has a `linear-agents` user.
- A host with no local `agent/<key>` continues the branch from GitHub and recovers the tester's
  commits from it, so a rerun on another host resumes at the implementer. `--accept-tests`
  grants do not move with it.
- A push rejected because the branch moved on GitHub blocks at once and says so, instead of three
  retries that point at the token.
- `Linear-Agents:` trailers and `🤖 linear-agents:` comments are still read until 0.5.0.
- The desktop's hosts are `~/.config/flocc/hosts/<name>.toml`; `just fleet-status` converts
  `~/.config/la-setup/hosts` once. The service phase saves its answers in
  `~flocc/.config/flocc/answers.toml`.

## 0.2.0 (2026-10-20)

The agent loop, and fixes from its first day on la-nimble.

- Sessions delegate only to seven catalogue subagents with fixed models and per-phase launch
  budgets, enforced by a fail-closed `PreToolUse` gate. The owner grants exceptions with `allow`
  lines in issue comments (#31).
- Implementer and fix sessions ask blocking questions with `AskUserQuestion`; an unanswered
  question escalates after two hours. A session compacts once, then hands off (#31).
- Agent sessions load only the plugins in `deploy/agent-plugins.txt`. Remote Control is on for
  implementer and fix sessions only. Finished runs' scratchpads are deleted (#31).
- `linear-agents release --accept-tests` accepts an owner-approved change to the tester's files
  (#32).
- A session idle for 30 minutes without finishing escalates with its last message. Claims are
  checked within one repo, are refused for whole trees, and skip `shared_paths`. Sessions are told
  the issue is the approved design (#33).
- A failed check's comment shows its failing lines, and the fix session gets the full output (#34).
- `linear-agents --version`, and the version in `linear-agents status`.

## 0.1.0 (2026-10-19)

Everything through #30: the poller, tester, implementer, reviewer and fix phases, the publisher,
escalations and digest, steering by comment, the Go installer and `just ship`.
