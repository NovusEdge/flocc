# Changelog

A release is a batch of merged PRs. The PR that cuts one bumps `version` in `pyproject.toml` and
`plugin/.claude-plugin/plugin.json` together and adds its entry here; the merge commit is tagged
`v<version>`.

## Unreleased

- Agents ask the owner with `flocc done --needs-owner '<question>'`, one flag per question, and
  cardinal gets a `needs-owner` event with the count. A summary that still uses a "Needs the
  owner" heading is refused. `flocc done --nothing-to-do '<why>'` ends a tester or executor run
  done without a PR, and an executor that finishes with no commits now blocks as `no-changes`
  instead of failing publish three times with "Fix the push or the gh token".
- cardinal's `fix` verb becomes `resume`: it restarts a blocked or timed-out run once, on its
  kept branch and in the step that stopped, with a note. After `needs-owner` the note answers the
  agent's questions. It is refused for `tests-touched`, `branch-moved`, `publish-failed`,
  `rebase-conflict`, `no-tests` and a check at its last fix round. The daily digest counts the
  resumes and how many ended done. `fix` still works as an alias for one release.
- A rerun reads your comments: owner and steering comments posted after the previous run stopped
  go into the restarted session's `context.md` under "Comments since the last run", whether the
  issue went back to Todo by hand or through `flocc release`. Before, only comments posted during
  a run reached it, so "comment, then move to Todo" restarted without the comment.
- A branch's check no longer times out while it waits on a lock, such as nimblefox's machine-wide
  suite lock held by another worktree: time a process of the check spends blocked in
  `/proc/locks` on a lock held outside the check does not count against `check_timeout_s`. systemd allows 4 h of waiting on top,
  and a check it stops while still waiting runs again once instead of failing.
- The tick watches the host: `/tmp` above `tmp_max_pct` (80), memory available below
  `min_available_mem_gb` (1.0 GiB) or a new OOM kill opens an episode and emits `host-pressure`.
  While it lasts nothing starts or relaunches, without spending a fix round or a restart, and
  each running session hears when the host recovers. cardinal repairs it with `host`,
  `cleanup --now` and `stop-tests KEY`, and as a last resort `agent-shell`, which asks you in the
  Claude app before each command. The daily cleanup also removes finished pytest runs. Rerun
  `flocc host cardinal` after `flocc ship` to install cardinal's side.
- A PR title takes its conventional type from the branch's commits when they all share one (a
  docs-only change reads `docs:`); mixed or non-conventional subjects keep the label rule.
- The executor keeps a fact the issue's wording would silently remove, or lists the conflict under
  `Needs the owner`, and both it and the fixer keep commit subjects to 72 characters.
- The supervisor-error event names the exception's type and first line.
- `flocc run-summary` leaves the routine sudo and PAM lines out of its service log, and
  `flocc status` activity drops the same lines.
- A prepare failure outside the repo's `./setup` says it is a flocc error and points to the
  service log, instead of "Fix the repo or ./setup".

## 0.13.0 (2026-10-30)

flocc drops cardinal, A2A and the escalation path built in 0.9 to 0.12. A run that needs you says
so on its Linear issue.

- cardinal is back, rebuilt: `flocc host cardinal -H <host>` makes a small Incus VM beside the
  host with one idle Sonnet session in Remote Control, woken only by a plugin monitor that
  follows the host's events over a forced-command ssh key. cardinal holds new starts and starts
  fixers; it never reruns stopped or finished work. The executor VM gains an OpenSSH server on
  its pinned bridge address for that key alone, and a firewall that lets the cardinal VM reach
  nothing else on it.
- `host create` and `host cardinal` block every VM on `incusbr0` from forwarding out of the
  machine's `tailscale0`, with an nftables table `flocc-no-tailnet` and a boot unit
  `flocc-no-tailnet.service`. Without it a VM reached the tailnet as the machine, and Tailscale
  SSH let the cardinal VM in as root on the executor. On a machine that uses a Tailscale exit
  node the block cuts all VM egress, and the step says so.
- cardinal as built in 0.9 to 0.12 is gone: `flocc cardinal` and its subcommands, cardinal's step
  in `flocc setup`, the dashboard's cardinal pane and `c` key, the CARDINAL table in
  `flocc status -H all` and `~/.cache/flocc/cardinal-<user>.log`. `flocc setup` offers to remove
  the old `cardinal` user.
- A2A is gone: `flocc-a2a.service` (ship stops and deletes it), `flocc a2a serve`, `flocc mcp-a2a` and its tools, the
  `flocc inbox` hook, and the token minting and peer lists in `flocc ship`, which no longer writes
  `~/.config/flocc/a2a.toml`. Remove the server from desktop sessions with
  `claude mcp remove --scope user flocc-a2a`, and the tailnet grant from `tag:flocc-host` to
  itself on `tcp:7443`, which setup now reports.
- Escalation is gone: `flocc escalate`, the escalate skill, the tiers, ntfy pushes and the ntfy
  topic in `flocc keys`. flocc no longer creates the `escalation` and `needs-decision` labels and
  doctor no longer requires them. The service comments on the issue when a run blocks or prepare
  fails. A failed tick, an expiring token and a lapsed Claude login go to the digest issue and the
  journal.
- The subagent gate and its launch budgets are gone, with `flocc gate`, `allow:` grants in the
  agent block and in comments, and `launches N/M` in status and the dashboard. An `allow:` line in
  the agent block is now an unknown key, and the issue is skipped with a comment.
- The permission hook and `flocc permissions report` are gone.
- Path claims are gone: `flocc claim`, the pre-commit hook's claim check and the agent block's
  `paths:` key, which is now refused as unknown. Repo config drops `shared_paths`.
- The coordinate and handoff skills and the restart at 60% context are gone, with
  `flocc handoff-ready` and `context_restart_pct`.
- Open `AskUserQuestion` questions are no longer tracked: they do not extend the wall clock, and
  the skills tell agents not to ask.
- `ntfy_url`, `context_restart_pct` and `shared_paths` in `config.toml` are ignored with a warning
  if still present.
- The ledger gains `events` and `holds` tables and drops the claim's `paths` and
  `escalation_offset` columns on its first open by the new version.
- Sessions run in `bypassPermissions`. Auto mode, `permissions.toml`, the host file's
  `[permissions]` table and its judge keys are gone; a leftover `permissions.toml` is ignored with
  a warning and ship deletes it.
- A PreToolUse guard denies `AskUserQuestion`, `EnterPlanMode`, `ExitPlanMode`, `git push`,
  GitHub writes through `gh` (`gh api` with `-f`/`-F` and no `-X` counts as a POST) and Linear
  tools, and keeps the reviewer to `review.md`. It replaces the pre-commit hook's review check.
  The guard and deny rules for the three dialog tools live in root-owned
  `/etc/claude-code/managed-settings.json`, which the sessions cannot change or turn off.
- Nobody answers a permission dialog in a session, so a `PermissionRequest` hook in the same
  managed settings, `flocc permission-deny`, denies every one and tells the agent to do it another
  way. Without it a plugin hook that answers "ask" (curt does for `cat`, `head` and `tail`) stalls
  the session even in `bypassPermissions`. Doctor checks for the hook.
- A turn that ends without `flocc done` is refused once; ending again marks the session idle and
  the run ends blocked, with a comment on the issue. An API error other than a usage limit does
  the same. The idle and stall notices, `flocc press`, `flocc say`, `idle_escalate_s` and
  `stall_escalate_s` are gone. A crash still gets one restart.
- An issue's `Run:` line picks the archetypes, `executor, reviewer` by default, from `tester`,
  `executor` and `reviewer`. An unknown name or order skips the issue with one comment. Without a
  tester the publisher skips the tester-file guard. Fixers are added by the service as before.
- Four skills, executor, fixer, tester and reviewer, replace implementer, tester and reviewer.
  Every archetype ends with `flocc done`, which replaces `flocc review-done`. A summary with a
  "Needs the owner" heading ends the run blocked, except in a review fix round.
- The service writes events for cardinal: `ended` (a PR is ready, or a run blocked or timed out),
  `cap`, `base-broken`, `bad-sequence`, `service-error` and `daily`. `flocc events --follow`
  prints them in batches and `flocc ack` acknowledges them. New host commands for cardinal:
  `run-summary`, `fix`, `hold`, `cleanup` and `digest-data`. `release` lifts a hold, and
  `release --hold-only` lifts only a hold. `flocc status` prints one table of running and
  finished runs.
- The service no longer posts a daily digest to `digest_issue`; cardinal writes the digest.
  `digest_issue` is optional and unread, and doctor no longer checks it.
- A timed-out run comments on its issue. A failing tick reaches cardinal at once, and Linear
  after three failed ticks.
- Sessions, worktrees, the repo's `./setup` and check, and the Claude login belong to a second
  user, `flocc-agent`, which cannot read the service's keys, ledger or config, nor write
  `/opt/flocc`. The service reaches it only through `sudo` and
  `/usr/local/libexec/flocc-agent-run`. `flocc setup` and `flocc ship` create the user, the
  `flocc-work` group and the sudoers rule, move an existing Claude login to `flocc-agent`, and run
  a new `agent` phase. The service walks into `flocc-agent`'s `710` home with `O_PATH`, which
  needs only search permission, and `context.md` names the session's own state directory.
- With `docker = true`, checks that start containers use rootless Podman as `flocc-agent`
  (`DOCKER_HOST` points at its `podman.socket`; testcontainers' Ryuk is off). Neither user may be
  in the `docker` group, which is root-equivalent: ship takes `flocc` out of it and doctor fails
  if `flocc-agent` is in it. An existing Docker daemon is left installed and untouched. A check
  that runs the `docker` CLI itself needs it installed by hand.
- The publisher fetches the branch from flocc-agent's worktree into the service's clone and pushes
  from there, so the publish token never enters a session. The fetch passes `safe.directory` to
  `upload-pack` itself, because a local fetch drops `-c` from upload-pack's environment.
- When an issue reaches Done or Canceled, or is released without `--keep-worktree`, its worktree,
  session files and scratchpads go; the branch stays on GitHub.
- `worktree_root` and `plugin_dir` in `config.toml` are fixed by the layout and ignored with a
  warning.

## 0.12.0 (2026-10-25)

Fewer runs stop for the owner on problems outside the issue's own code.

- When the check fails, flocc runs the same check on the base branch. If the base fails the same
  way, the repo is held as main-broken, as a failed `./setup` is, and no fix round is spent. Held
  claims don't count against `session_cap`. When the hold lifts, a claim that still fails the same
  way is told to merge the base.
- An open PR that conflicts with its base gets one session to merge the base and resolve the
  conflicts, then a push to the same PR. It goes through admission and the trigger-label check,
  and gets one attempt per base commit.
- A repo setting `amended_label` names a label (nimblefox uses `tests-amended`) that the publish
  token adds to a PR whose tester files were amended. If GitHub rejects the label, a digest line
  records it and the review still starts.
- A rerun syncs its kept branch with GitHub first: it fast-forwards when behind, and resets when
  the branch was rewritten on GitHub and nothing here is unpushed. It stops with a reason when
  local work would be lost. It then finds the tester's commits by trailer, so a rerun after a
  rebase resumes at the implementer instead of starting a new tester.
- `flocc ship` waits for the A2A server to record its head before checking it.

## 0.11.0 (2026-10-24)

Agents on flocc hosts can reach each other, and the owner can reach any of them from a Claude
session.

- Each host serves an A2A endpoint on its tailnet address, port 7443 (`flocc-a2a.service`), with
  an inbox that keeps a message until the issue's agent receives it or the issue closes.
- Host sessions get the `flocc-a2a` tools `agents`, `send`, `notify` and `replies`; desktop
  sessions add them once with `claude mcp add`. Messages reach a session at its next tool call,
  at the end of its turn, or when the issue next starts; an owner message wakes an idle session.
- When a PR opens or merges, the issues Linear lists as blocked by it get a handoff with the PR and
  its files, and a merge also notifies the repo's other live agents. PRs that exist when a host
  first ticks after the upgrade get no "opened" handoff.
- `flocc ship` mints the A2A tokens and refuses a repo mapped on two hosts. Hosts list peers by
  tailnet address and accept tokens by digest. After `flocc host create`, ship the other hosts
  again so they accept the new one.
- The tailnet policy gains a grant from `tag:flocc-host` to itself on tcp:7443. A policy set up
  before 0.11.0 needs `<device>:7443` added to its `tests` deny entry by hand.
- A session whose turn ended while its own background shells run is no longer escalated as idle.

## 0.10.0 (2026-10-23)

Host sessions no longer wait silently on a dialog, and runs stop less often on the host's or
main's problems.

- When `./setup` fails on an untouched checkout of a repo's base branch, the repo's issues are
  held instead of each running setup again every tick, with one comment and push. A new commit
  on the base, 24 hours, or a passing `flocc canary <repo>` lifts the hold.
- A review that asks for changes starts one fix session on the same branch with the review as its
  context. The fix pushes to the open PR, a second review follows, and what needs the owner is
  listed in the issue comment. Skipped over the usage limits.
- PR titles carry a Conventional Commits type, `feat: <title> (NF-…)`, with `fix` for a Bug label
  and `chore` for a Chore label, so a repo's semantic title check passes.
- A killed `flocc canary` no longer leaves `./setup` running; `run_bounded` kills its child's
  process group when interrupted, and the poller removes stray `.canary-*` directories.
- Host file `playwright = "<version>"` installs chromium's system libraries and fonts on apt
  hosts, `docker = true` installs Docker and adds the service user to its group, and `[tools]`
  sets mise tools' global versions for the service user.
- `flocc permission` denies every permission dialog in a host session, AskUserQuestion excepted,
  and tells the agent nobody is there to approve, so it finds another way or asks the owner. A
  hook's "ask" used to hold the session until its wall-clock limit.
- Each launch marks Claude Code's "Teach auto mode about your environment?" dialog as dismissed
  in `~/.claude.json`, as its "Don't show again" does.
- A session whose screen stays the same for `stall_escalate_s` (default 1200) in the middle of a
  turn is escalated: a needs-decision comment and an ntfy push carrying the pane's last lines.
- `flocc press NF-12 Down Enter` answers a dialog and `flocc say NF-12 <message>` types into a
  session. `attach` shows that it is read-only and that Ctrl-b d detaches.
- Bare `flocc` off a terminal prints every host's status instead of the usage.
- A failed check gets `check_fix_rounds` (default 2) fix sessions before the claim blocks; it
  used to get one. The second round sees the first round's failure too.
- An implementer that finds a tester's test wrong fixes it and records it with
  `flocc tests-amended <path> --reason ...` instead of blocking the run. The PR body and the
  review input list each amended file with its reason. An amendment that removes or skips a test,
  and any touched tester file not recorded, still blocks.
- A session with an open question is not stopped at its wall-clock limit.
- A `./setup` timeout no longer holds the repo's other issues; only a non-zero exit or a dirty
  worktree does. `flocc canary --forget` clears every hold, and every `flocc ship` runs it.
- A check that fails the same way in two fix rounds blocks at once instead of spending another.
- A run that hit the wall clock after committing goes back to Todo once on its own, with a
  comment; a second timeout waits for `flocc release` as before.
- `Done when` and `Definition of done` headings count as acceptance criteria.
- The escalate and implementer skills tell sessions to choose on the branch, shrink an
  over-budget plan, finish with leftover findings listed in the summary, and skip the
  re-review of a few-line fix.

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
  create stops before the build when the checkout lacks the commit the desktop flocc was built
  from, and the first ssh to the machine gives up after 10 seconds.
- `flocc keys` and `flocc setup`, run in a flocc checkout, take each key from the shell's
  environment or the checkout's `.env` (`FLOCC_LINEAR_API_KEY`, `FLOCC_GITHUB_AGENT_TOKEN`,
  `FLOCC_GITHUB_PUBLISH_TOKEN`, `FLOCC_NTFY_TOPIC`, `FLOCC_TAILSCALE_OAUTH_SECRET`) and check it
  as a paste. `.env.example` lists them, and setup makes `.env` from it when missing.
- A pasted key shows as dots, then as its kind, last four characters and length. The Tailscale
  client secret alone is enough. Key pages open in the browser without a QR code.
- `flocc cardinal status` connects on the terminal first, so a Tailscale SSH check shows its link
  at once instead of reading as an unreachable machine.
- The docs are a GitBook book in `docs/book/`; `docs/vm-setup.md` points into it.
- Not confirmed against the live Tailscale API: the error text for a tag the policy lacks, and
  tagging a user-owned node through the API.

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
