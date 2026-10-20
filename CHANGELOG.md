# Changelog

A release is a batch of merged PRs. The PR that cuts one bumps `version` in `pyproject.toml` and
`plugin/.claude-plugin/plugin.json` together and adds its entry here; the merge commit is tagged
`v<version>`.

## 0.5.0 (2026-10-20)

One `flocc` command on the desktop; keys are made once, checked when pasted and reused.

- `flocc` (`go install ./installer/cmd/flocc`) is the desktop command. Host commands run as
  `flocc -H <host> <command>`, with the host picked from `$FLOCC_HOST`, the only host or the issue
  named; `-H all` runs `status`, `doctor` and `logs`. The installer is its own binary,
  `flocc-setup`, built by ship for the host's architecture.
- `flocc keys` opens each key's page with a QR code, prefills GitHub's token page, reads pastes
  with echo off (and refuses a stdin that is not a terminal), checks each key by prefix and
  against the APIs without side effects, and keeps them in the OS keyring (else
  `~/.config/flocc/secrets.toml`, mode 600). A paste whose check got no answer from an API is not
  stored; you are asked whether to check it again. The ntfy topic is shown as a QR code for the
  app and confirmed once. `flocc keys rotate github|linear` checks the new keys before storing
  any, then pushes them to every host; after a partial push, run it again, paste nothing and
  answer yes to push the stored keys again.
- `flocc host add` writes `~/.config/flocc/hosts/<name>.toml` with the host's answers, creating
  the digest issue and the labels when missing; `host add` and `ship` ask for a key the desktop
  lacks on the spot. `flocc host import -H <name>` is the path for a host shipped before 0.5.0
  (its host file has only `ssh`, and `flocc ship` points there): it reads the answers from the
  host and takes each key the desktop lacks from it, checked as a paste is. A dropped
  connection stops the import with "run flocc host import again"; a host token missing a repo is
  fixed by editing its repository access on GitHub, then importing again. `flocc ship -H` and
  `flocc login -H` replace `just ship` and `just login`, whose recipes now run them; run
  `flocc ship` inside the flocc checkout. `flocc-setup ship` is gone, and with it
  `--stage-only DIR` and `--answers FILE`.
- The agent token asks for Actions and Commit statuses read instead of Checks, which
  fine-grained tokens do not have. The Linear key must be able to write; its check sets a
  digest issue's title to the title it has.
- `flocc status` shows the host's Claude login (full, token-only, none or unknown) and its token
  expiries, per host under `-H all`. A pending login names the host by its desktop name:
  `flocc login -H <name>`. The host CLI gains `logs` and `attach` and refuses the desktop
  commands. `all` can no longer be a host name.
- Ship no longer sends an ntfy test push on every run.
- `Linear-Agents:` trailers, `🤖 linear-agents:` comments and the old hosts file are no longer
  read, and ship no longer refuses a host from before 0.3.0. Hosts must be on 0.3.0 or later
  before they upgrade to 0.5.0, since 0.5.0 no longer reads what the old names left behind.

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
