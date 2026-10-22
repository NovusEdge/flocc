# Changelog

A release is a batch of merged PRs. The PR that cuts one bumps `version` in `pyproject.toml` and
`plugin/.claude-plugin/plugin.json` together and adds its entry here; the merge commit is tagged
`v<version>`.

## 0.9.0 (2026-10-22)

The desktop CLI shows what it is doing, and hosts join the tailnet tagged without a browser login.

- Every desktop command writes through one session of steps, drawn full-screen, inline or plain.
  `setup`, `host create`, `ship`, `login` and `cardinal setup|login` open full-screen on a
  terminal: a rail of sections, the running step's output, prompts as a modal. Choose with
  `--full`, `--inline`, `--plain` or `FLOCC_UI`; a pipe, `NO_COLOR` or `TERM=dumb` is always plain.
  Rows now appear as each step ends, including during `setup`, `ship` staging, builds and tailnet
  waits.
- `flocc-setup --events` streams steps as JSON lines and `ship` replays them into the desktop's
  session, in place of the raw host output and the two summary marker lines. `status --json` adds
  recent activity per issue and cardinal's status.
- Bare `flocc`, or `flocc status --watch` on a terminal, opens a dashboard of every host and
  issue, polled every 10 seconds. `a` attaches, `r` releases after a confirmation. `-H all doctor`
  and `logs` are drawn by the desktop.
- Every desktop screen shares one palette, which follows the terminal's light or dark background,
  one rounded pane style and one key bar that dims keys doing nothing on the current row. The
  full-screen commands draw their sections and steps as two panes, stacked below 100 columns, and
  switch to inline below 60×20.
- The dashboard opens an issue, a host or cardinal into a screen of its own: an issue's live tmux
  pane or the log lines naming it, a host's facts, doctor checks and log tail, and a pager with
  search and follow. Issues show their Linear titles. `/` filters, errors show as toasts, and
  release asks on the line above the key bar.
- Hosts gain `flocc peek <issue>`, the tail of an issue's live session, and `flocc logs --issue
  <key>`, only the log lines that name it.
- A terminal handoff waits for the terminal's answers to the program's startup queries, so they no
  longer appear as `^[[?1u` in a `host create` ssh login.
- `flocc cardinal login` and `cardinal attach --write` warn that Ctrl-D twice ends cardinal's
  session.
- `flocc setup` has a Tailnet section. The Tailscale OAuth client is a new optional key slot;
  with it, setup checks the tailnet policy and prints a merged policy to paste (it never writes
  one), and tags each host's node. `host create` mints a one-off tagged key and joins with no QR
  login; without the client it joins with `--advertise-tags=tag:flocc-host`.
- `host create` runs its firewalld and Docker steps as root in one `sudo` on the machine, so a
  password on sudo is asked once instead of stopping create; the setup wizard no longer refuses
  such a machine.
- Not confirmed against the live Tailscale API: `--advertise-tags` on a fresh node, the error text
  for a tag the policy lacks, and tagging a user-owned node through the API.

## 0.8.0 (2026-10-21)

`flocc cardinal`, the always-on assistant's setup command, and `flocc setup`, one command for the whole setup.

- `flocc cardinal setup|login|status|attach|restart|remove` sets up cardinal as an unprivileged
  user on the machine that holds the Incus hosts, keeps it alive in a systemd user unit with a
  daily renewal before its `/loop` expires, checks it, and takes it down. `setup` repairs a
  half-set-up machine and asks for sudo only when a root outcome is missing.
- The cardinal user is blocked from the tailnet by a per-uid nftables table, and reaches its hosts
  as root over the Incus bridge with a key limited to the bridge gateway.
- `flocc status -H all` shows a CARDINAL table, and a failing or unreachable cardinal in its
  failure list.
- `flocc setup` walks the keys, a host (create, add, or ship to repair), the old `linear-agents`
  user from the hand-setup era, cardinal and the logins, in order and safe to rerun. An old user is
  removed only on a yes, after the root pass re-checks it, with its home archived under `/root`;
  that removal and cardinal's root work share one sudo password. Host files gain `machine`.

## 0.7.0 (2026-10-21)

Auto-mode blocks on host sessions are logged and can be tuned away.

- Every block auto mode's classifier makes in a host session, and every permission prompt, is
  logged to the issue's `permissions.jsonl`, with the agent's own description of the command kept
  apart. `flocc permissions report` on the host, or `flocc -H <host> permissions report` on the
  desktop, counts them by tool, command and classifier reason over the last week.
- A host file's `[permissions]` table (`allow` rules and `environment` lines) is shipped to the
  host's `permissions.toml` and merged into every session's settings at launch. Allow rules that
  wildcard an interpreter or shell are refused.
- The "Model switch" dialog a refusal opens is escalated like an open question, since nothing
  answers it on a host.

## 0.6.0 (2026-10-21)

`flocc host create`, and the end of the `just` host recipes.

- The desktop's forms and output use your terminal's own colours: `ok` and ✓ green, `FAIL` red with
  its next step in bold, notes yellow, every line indented two spaces. There is no colour when the
  output is not a terminal, `NO_COLOR` is set or `TERM=dumb`, so logs and the host's summary line
  stay plain. `host add` says why it passes over a digest issue (closed, or missing).
- `ship` and `host create` show the step being applied as a live line with a counter and elapsed
  time (`⠙ 3/9 Packages ... 0:41`), replaced by its result when it ends. Where the output is not a
  terminal, each step gets a plain start line and a done line instead.

- `flocc host create <machine> <name>` makes a host as an Incus VM, over SSH as your own account:
  it checks Incus, puts `incusbr0` in a firewalld zone that allows only DHCP and DNS with a forward
  policy to the uplink, lets the VM through Docker's `DOCKER-USER` chain from a oneshot unit tied to
  `docker.service` (Docker 28 or newer is needed and is never restarted; Docker's own forwarding
  chain decides first, so the VM reaches only published container ports),
  launches Debian 13 with autostart, joins Tailscale by QR code or a one-off key on stdin, runs
  `host add`, `ship` and `login`, and snapshots the VM as `installed`. Running it again continues
  a half-made host.
- The `just` recipes that wrapped `flocc` are gone; run `flocc` itself. `just` keeps the dev, build
  and Daytona recipes. In place of the removed ones:

  | Removed | Run instead |
  |---|---|
  | `just ship <h> [flags]` | `flocc ship -H <h> [flags]` |
  | `just login <h> [flags]` | `flocc login -H <h> [flags]` |
  | `just host <h> <cmd>…` | `flocc host run -H <h> -- <cmd>…` |
  | `just doctor <h>` | `flocc -H <h> doctor` |
  | `just status <h>` | `flocc -H <h> status` |
  | `just fleet-status [flags]` | `flocc status -H all [flags]` |
  | `just logs <h>` | `flocc -H <h> logs -f` |
  | `just attach <h> <session>` | `flocc -H <h> attach <session>` |
  | `just tick <h>` | `flocc -H <h> tick` |
  | `just release <h> <key> [flags]` | `flocc -H <h> release <key> [flags]` |
  | `just daytona-start <n>` | `daytona start <n> && flocc ship -H <n>` |
- Create moves `incusbr0` from zone `trusted` into `flocc-incus`. It does not touch
  zer0-day-nimble's other broad settings: `ip-forward-no-drop` and FORWARD ACCEPT are reverted by
  hand, and the removal of `ip-forward-no-drop` from `/etc/docker/daemon.json` applies at Docker's
  next start.

## 0.5.0 (2026-10-20)

One `flocc` command on the desktop; keys are made once, checked when pasted and reused.

- Fix: the service marks Claude Code's first-run onboarding done (`hasCompletedOnboarding` in
  `~/.claude.json`) before each session starts. A host logged in by 0.4.0's `flocc login` never
  went through it, so every session stopped at the "Press Enter to continue" security notes.

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
- `flocc ship -H <host>` replaces a host's key file when the desktop's key differs, so it brings
  the host's keys in line with the desktop; it is the fallback when `keys rotate` could not reach
  a host. `flocc ship`, `host import`, `keys rotate`, `doctor` and `logs` under `-H all`, and the
  lookup of the host holding an issue first open the SSH connection in the terminal, so a
  Tailscale check-mode approval link shows. Without a terminal, a host that needs a prompt fails
  within 30 seconds with a message saying so instead of hanging. `status -H all` does not open
  connections first; a host waiting on an approval shows there as unreachable.
- The agent token asks for Actions and Commit statuses read instead of Checks, which
  fine-grained tokens do not have. An agent token imported from a pre-0.5.0 host has neither:
  on github.com/settings/personal-access-tokens add Actions (read) and Commit statuses (read)
  to it (the token's value stays the same). The Linear key must be able to write; its check
  sets a digest issue's title to the title it has.
- `flocc status` shows the host's Claude login (full, token-only, none or unknown) and its token
  expiries, per host under `-H all`. A pending login names the host by its desktop name:
  `flocc login -H <name>`. The host CLI gains `logs` and `attach` and refuses the desktop
  commands. `all` can no longer be a host name.
- Ship no longer sends an ntfy test push on every run.
- `Linear-Agents:` trailers, `🤖 linear-agents:` comments and the old hosts file are no longer
  read, and ship no longer refuses a host from before 0.3.0. Hosts must be on 0.3.0 or later
  before they upgrade to 0.5.0, since 0.5.0 no longer reads what the old names left behind. A
  branch whose tester commits carry only the old `Linear-Agents: <KEY> test` trailer is no longer
  recognised, so finish or merge such issues on a 0.4.x host before upgrading it.

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
