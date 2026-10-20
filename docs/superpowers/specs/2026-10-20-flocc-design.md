# flocc: rename linear-agents, make credentials and login easy, and script host creation

Date: 2026-10-20. Status: draft, revised after two Opus reviews.

linear-agents becomes flocc (thicc-meme spelling, decided 2026-10-20). The repo stays on the
owner's account as `NovusEdge/flocc`. Setting up la-nimble on 2026-10-19 showed that installing a
host takes manual work and several logins nobody else could repeat. This spec has four parts, built
and shipped in this order, one plan each:

1. **Rename and migration** (0.3.0): every name changes, and a new VM replaces la-nimble.
2. **2a, host side of setup** (0.4.0): ship takes every answer on stdin and never prompts; Claude
   login is one command; ship ends with a summary.
3. **2b, desktop CLI and credentials** (0.5.0): one `flocc` command on the desktop; keys and host
   answers are made once, checked on entry and reused.
4. **Host create** (0.6.0): one command makes a VM on a machine, without broad firewall changes.

Out of scope: tracker- or harness-agnostic flocc; the sequencing spec; the spec-to-tickets
pipeline (both separate).

---

# Part 1: rename and migration

## Names

| What | Now | flocc |
|---|---|---|
| GitHub repo | `NovusEdge/linear-agents` | `NovusEdge/flocc` (GitHub redirects) |
| Python distribution, module, host CLI | `linear-agents`, `linear_agents`, `linear-agents` | `flocc`, `flocc`, `flocc` |
| Go module | `github.com/NovusEdge/linear-agents` | `github.com/NovusEdge/flocc` |
| Installer binary, root stage, bundle, desktop temp dir | `la-setup`, `/tmp/la-setup`, `linear-agents.bundle`, `la-ship-*` | `flocc-setup`, `/tmp/flocc-setup`, `flocc.bundle`, `flocc-ship-*` |
| Host answers and stage | `~/.cache/la-setup/` (holds the durable `answers.toml`) | stage `~/.cache/flocc-setup/`; answers `~/.config/flocc/answers.toml` |
| Desktop hosts | `~/.config/la-setup/hosts` (one SSH target per line) | `~/.config/flocc/hosts/<name>.toml` with `ssh = "root@<name>"` or `daytona = "<name>"`; the old file is read once and converted, with a notice |
| Claude Code plugin, `gate.PREFIX` | `linear-agents`, `linear-agents:` | `flocc`, `flocc:` |
| Env vars | `LINEAR_AGENTS_{CONFIG,STATE,PHASE,RUN,LEDGER,LAUNCH_GRANTS}` | `FLOCC_*` |
| Service user, home | `linear-agents`, `/home/linear-agents` | `flocc`, `/home/flocc` |
| Service repo, worktrees, marketplaces | `~/agents/linear-agents`, `~/agents/worktrees`, `~/agents/marketplaces` | `~/flocc`, `~/worktrees`, `~/marketplaces` |
| Config, state | `~/.config/linear-agents/`, `~/.local/state/linear-agents/` | `~/.config/flocc/`, `~/.local/state/flocc/` |
| tmux socket, loop session | `-L linear-agents`, `la-loop` | `-L flocc`, `flocc-loop` |
| systemd user units | `linear-agents.{service,timer}`, tmux unit | `flocc.{service,timer}`, `flocc-tmux.service` |
| Markers and probes | `.la-commit`, `la-commit.lock`, `.claude.json.linear-agents.tmp`, `refs/heads/linear-agents-doctor-probe`, generated ntfy topic prefix `la-` | `.flocc-commit`, `flocc-commit.lock`, `.claude.json.flocc.tmp`, `refs/heads/flocc-doctor-probe`, `flocc-` |
| Default Tailscale hostname | `linear-agents` | `flocc` |
| Commit trailer | `Linear-Agents: NF-1 impl` | `Flocc: NF-1 impl` |
| Linear comment prefix, PR review header, ntfy title | `🤖 linear-agents:`, `🤖 linear-agents review`, `linear-agents` | `🤖 flocc:`, `🤖 flocc review`, `flocc` |

Unchanged: Linear labels, branch names `agent/<key>`, session names, the digest issue, the `just`
recipes (until 2b).

Outside the code, the same change goes into: docket scopes (re-scoped with `docket correct`, never
by hand), `docs/`, the owner's `~/.claude/CLAUDE.md` linear-agents section, the auto-memory files,
and the teammate how-to in NF-215.

## Branches survive a host move

Today a new host has no local `agent/<key>`, cuts a fresh one from main, and its push is rejected
after the whole run. `prepare_worktree` changes:

- With no local branch it runs `git ls-remote origin agent/<key>`; when the branch exists it
  fetches it and starts from it. A missing remote branch is not an error.
- The tester's work is recovered from the branch, not from any one trailer:
  - the fork point is `merge-base origin/<base> origin/agent/<key>`;
  - the tester commits are the first contiguous run of commits after the fork point whose trailer
    is `<KEY> test` (either trailer name); a `test` trailer after any non-test commit is ignored,
    because an implementer can write that trailer itself;
  - `base_sha` is the parent of the run's first commit, `tester_sha` its last commit, written with
    `ledger.remember_tests`, so the guard and `--accept-tests` work on the new host;
  - the existing rule that forgets tests already contained in `origin/<base>` applies first.
- A test acceptance granted on the old host (`accept_tests`) does not travel; the owner grants it
  again if needed.

The publisher names a non-fast-forward rejection as "GitHub's copy of the branch has commits this
one lacks (another host or a person pushed, or a pushed commit was rewritten)" instead of a token
problem. Recovery still finds the tester's commits after the branch merged its base, because they
stay outside the new merge-base.

## Old names still read

Two readers accept the old names, and both are removed in 0.5.0 (a la-nimble branch rerun after
that loses tester recovery):

- branch recovery and the publisher's untrailed check accept `Linear-Agents:` as well as `Flocc:`,
  since a recovered `base_sha` reaches back over la-nimble's commits;
- the poller ignores comments starting with either bot prefix (insurance: the new host reads only
  comments after its own claim started).

## Migration

1. Merge Part 1, tag `v0.3.0`. Ship refuses a target that has a `linear-agents` user or timer, so
   it cannot install a second poller on la-nimble.
2. From here until flocc-nimble is shipped and la-nimble's trigger label is switched, the owner
   adds `claude` to no new issue, so neither host can start one. Check the laptop has memory for
   two VMs and build `flocc-nimble` by the Incus steps Part 1 adds to `docs/vm-setup.md`
   (`images:debian/13`, the bridge and forwarding fixes of 2026-10-19).
3. Create the label `claude-drained` in Linear (doctor requires the trigger label to exist), and set
   la-nimble's `trigger_label` to it: a label that exists but no issue carries. Only flocc-nimble
   will start issues; la-nimble keeps supervising, publishing, reviewing and forwarding comments for
   its claims.
4. Ship flocc-nimble with an answers file whose `[secrets]` block holds la-nimble's three secret
   files (GitHub shows a token only once): `just ship <host> --answers A`. It installs everything
   and stops at the Claude login with no timer; then set `session_cap` to the intended total minus
   la-nimble's running sessions. Then `just login <host>` in the owner's own terminal does the
   Claude login (it needs a TTY). There is no second ship pass.
5. For each blocked or timed-out claim on la-nimble:
   1. commit or discard what its worktree holds;
   2. push `agent/<key>` with the publish token (the agent token is read-only):
      `GH_TOKEN=$(cat ~/.config/linear-agents/gh-publish.token) git -c credential.helper= -c
      'credential.helper=!gh auth git-credential' push origin agent/<key>`;
   3. `linear-agents release --keep-worktree <key>` on la-nimble; flocc-nimble resumes it from
      origin.

   Never move an issue la-nimble holds to Todo by hand during the drain: la-nimble no longer polls,
   so its claim would never clear, and its later `release` would move flocc-nimble's issue back to
   Todo. Drain commands are plain SSH: `ssh root@la-nimble sudo -iu linear-agents linear-agents
   status`. Once flocc-nimble is shipped, the owner may label issues again.
6. When no claim on la-nimble is running: wait for that day's digest, copy
   `~/.local/state/linear-agents` and `~/.claude/projects` to the desktop, stop its timer and the
   VM, remove it from the hosts, and raise flocc-nimble's `session_cap`. Delete the VM 7 days later.

Rollback until the deletion: set la-nimble's trigger label back to `claude`, stop flocc-nimble's
timer. The new host re-posts `no-repo`/`no-criteria` notes once on skipped Todo issues; that is
expected.

## Part 1 testing

- A test fails if a tracked file matches `linear[-_ ]agents` (any case), `\bla-(setup|loop|commit|ship)`,
  `\.la-commit` or `"la-"`, outside `CHANGELOG.md`, `.docket/`, the compatibility readers and dated
  docs under `docs/`.
- Recovery against a real git fixture: only `origin/agent/<key>` exists; tester commits found as the
  first contiguous test run; a forged `test` trailer after an implementer commit ignored; an amended
  tester commit; no remote branch gives a fresh branch from main; the result is remembered.
- The publisher's non-fast-forward message; the untrailed check accepts both trailers.
- Ship refuses a host with a `linear-agents` user.

---

# Part 2a: host side of setup

## Ship never asks

Today ship runs `ssh -t` because sudo may prompt, the root phase hands off to the service phase
through an interactive `sudo -iu`, and answers travel as a file that is left behind when a run
fails.

- Non-interactive ship runs its setup in one SSH session forced to have no TTY (`ssh -T`, so a
  `RequestTTY` setting cannot make a pty echo the secrets). It first checks `sudo -n true` and stops
  with "give <user> passwordless sudo, or use root" otherwise.
- The answers document, secrets included, goes on stdin. The root phase reads stdin to EOF before
  anything else and passes it to `sudo -iu flocc flocc-setup --phase service --answers -` through a
  pipe. No secret is written to a file on the host except the service's own secret files (mode
  600), appears in argv, or reaches `setup.log` or stdout. As a second guard the runner redacts
  the stdin document's secret values from its log; secrets typed into an `--interactive` form are
  not in that set, so the first guard (no step puts a secret in argv, env or output) covers them.
- `--interactive` keeps `-t` and today's forms.
- The Claude step splits. **Claude** installs the CLI, writes push settings, installs marketplaces
  and plugins. **Claude login** probes `claude auth status` for real in every mode and reports
  pending when there is no full login.
- When the only open item is the Claude login, the Run step enables `flocc-tmux.service` but not
  the timer, ship ends with the login command, and exits 0. On an upgraded host whose login has
  lapsed, an already-enabled timer stays on and the tick pages until the login is redone.
- Plugins and marketplaces are installed before the login.

## Login is one command

A full Claude login is required: the long-lived `claude setup-token` token cannot open Remote
Control sessions. `claude auth login` (Claude Code 2.1.41 and later) does the login without the
chat screen, so nothing is scraped:

1. Take the tick lock for the whole login. If sessions are running, say so and wait or abort:
   running sessions rewrite `~/.claude.json` from memory and could drop the Remote Control consent.
2. If `claude auth status` already shows a `claude.ai` login, skip to step 4.
3. The desktop runs `flocc-setup --phase login` on the host over `ssh -t`; holding the tick lock,
   it runs `claude auth login --claudeai [--email <owner>]` as `flocc`. The URL it prints is also
   opened on the desktop and shown as a QR code; the owner signs in and pastes the code straight
   into the remote prompt.
4. Trust `~/worktrees` (the `hasTrustDialogAccepted` write the service already does for
   worktrees), then run `claude remote-control` once under a pty (`script -qfec`), answer its
   `Enable Remote Control? (y/n)` prompt with `y`, wait for its connected line, and stop it. No
   connected line within the timeout is a failure that attaches the owner, never a silent success.
5. `claude auth status` again, then the Run step: doctor, then the timer.

The `just` recipe is `just login <target>` until 2b adds `flocc login -H`.

## The end of a ship

The host ends the service and login phases with one `flocc-summary <json>` line, and the desktop
strips it and prints a summary instead of `Done.`: version on the host, Claude login (full, token-only or
none), GitHub token expiries, each failed doctor check, the timer, live sessions. It exits non-zero
when a check other than the pending login fails.

Token expiry warnings stay as they are (digest line at 14 days, tier 1 at 3 days). New: the tick
sends one tier 1 escalation when the Claude login lapses, since every new session would fail and
running Remote Control sessions cannot recover.

## Part 2a testing

- Against a fake runner: no secret value in any argv, in `setup.log` or on stdout; `sudo -n` failure
  stops early; stdin reaches the service phase intact.
- Fresh host: ship → login pending, tmux unit on, timer off, exit 0; login → timer on.
- Login: an existing `claude.ai` login skips step 3; live sessions block it; the consent prompt is
  answered.
- Summary built from doctor fixtures; exit status.

---

# Part 2b: desktop CLI and credentials

## One command, the host's grammar

The desktop gets a Go binary `flocc` (`installer/cmd/flocc`); the staged installer is its own
`main` (`installer/cmd/flocc-setup`), so a bare `flocc` prints help and never reaches the root
phase. On a host, the Python `flocc` is installed only for the service user; on the owner's
desktop the Python CLI runs as `uv run flocc` in the repo, so `flocc` on a PATH is the Go binary.

`flocc ship` runs inside the flocc checkout and builds `flocc-setup` for the host's architecture
itself; `flocc-setup ship`, `--stage-only` and `--answers FILE` go away in 0.5.0.

`flocc -H <name> <host command line>` runs that line on the host as the service user, each argument
quoted on its own, with `ssh -t` only when stdin is a terminal. The remedy lines the service writes
therefore work on the desktop with `-H` added. Without `-H`: `$FLOCC_HOST`; then the only host;
then, for a command naming an issue key, the host whose `status --json` holds that claim.
`-H all` is allowed for read-only commands (`status`, `doctor`, `logs`). SSH uses
`ControlMaster`/`ControlPersist`, so a Tailscale check-mode approval covers a whole run. `-H` is
taken from anywhere before `--`, so a literal `-H` argument goes after `--`.

Desktop-only verbs: `ship`, `login`, `keys`, `host`. The host CLI answers them with "that is a
desktop command". The `just` recipes become one-line wrappers, then go in 0.6.0.

## Hosts and their answers live on the desktop

A host file is Part 1's `hosts` package with `answers.Answers` embedded. Its destination must pass
`ValidTarget` (no shell metacharacters), and `all` is a reserved name.

`flocc host add <name> [--ssh root@<name>]` writes `~/.config/flocc/hosts/<name>.toml`, the only
source of a host's answers (an `--interactive` ship copies the host's answers back to it). It asks
for any missing key on the spot, then:

- discovers the repos the agent token can read and offers them ticked;
- picks the Linear team, automatically when the key sees one;
- finds or creates the digest issue;
- creates the missing labels: `claude`, `escalation`, `needs-decision` and one per repo.

`flocc host import -H <name>` builds the file from an existing host (flocc-nimble, after Part 1),
and takes each key the desktop lacks from the host, checked as a paste is, so moving a host never
forces new tokens. `flocc ship` also asks for a missing key on the spot rather than stopping.

## Keys: made once, checked on entry, reused everywhere

Secrets live in the OS keyring when there is one (Secret Service, macOS Keychain), otherwise in
`~/.config/flocc/secrets.toml`, mode 600. `flocc keys` fills what is missing, one key at a time,
reading pastes with echo off and never taking a key as a flag:

- It opens the page where the key is made (`xdg-open`/`open`, plus the URL and a terminal QR code).
  GitHub tokens use the fine-grained token page with name, description, owner, permissions and
  `expires_in=366` (the maximum) filled in; the repos to tick, which GitHub cannot prefill, are
  printed beside it from all `hosts/*.toml`. The publish token's page opens only after the agent
  token is accepted. The agent token asks for Contents, Pull requests, Issues, Commit statuses and
  Actions, all read. Fine-grained tokens have no Checks permission and cannot call the Checks API
  (docs.github.com: permissions required for fine-grained personal access tokens; managing your
  personal access tokens).
- A paste is checked by prefix first (`github_pat_`, `lin_api_`) so a key in the wrong slot is
  refused at once, then live: the Linear key reads the viewer and team, and proves it can write by
  setting a mapped digest issue's title to the title it already has (an empty update might not
  check the key's scope); each GitHub token reads every repo in `hosts/*.toml`, and
  the zero-SHA ref probe (`tokens.can_write_contents`) must fail for the agent token and pass for
  the publish token; a `POST /pulls` naming a head that does not exist must return 422, not 403,
  for the publish token. Checks re-run when a host is added. A stored token that misses a repo is
  fixed by editing the token's repository access on GitHub (its value stays the same) and running
  `flocc keys check`; only a failed paste is asked for again.
- The ntfy topic is generated and shown as a QR code (`ntfy://ntfy.sh/<topic>?display=flocc` for
  Android, the https URL beside it), then one test push with "did it arrive?". Ship no longer asks
  this every time.
- `flocc keys rotate github` reopens the page, pushes the new token to every host, lists hosts it
  could not reach, and says not to revoke the old token until running sessions end (they read
  `GH_TOKEN` at start).

`flocc status -H all` adds the Claude login and token expiries per host.

0.5.0 removes the old-name readers, so it is tagged and released only after la-nimble is deleted.

## Part 2b testing

- `-H` quoting with spaces and `$`; host lookup by claim; `-H all` refused for writing commands;
  desktop verbs refused by the host CLI.
- Keys: each validator against recorded responses (good key, wrong team, missing repo, agent token
  that can push, expired token, wrong-slot prefix); nothing in argv or shell history.
- `host add` against a fake Linear and GitHub: labels and digest created once.

---

# Part 3: host create

`flocc host create <machine> <name>` over SSH as the owner's account on `<machine>`, using `sudo`
or `incus` group membership (not root SSH):

1. Checks `incus` is initialised with bridge `incusbr0`, a storage pool with room for the disk, and
   free memory; it stops with the commands to run otherwise.
2. If firewalld runs, puts `incusbr0` in a zone allowing only `dhcp` and `dns`, and forwards it only
   to the default route's interface. Agents in the VM run unreviewed code and must not reach the
   machine's own services.
3. If Docker runs with the iptables backend, adds two `DOCKER-USER` rules scoped to `incusbr0`
   (accept from it; accept related/established to it), installed by a oneshot unit with
   `After=docker.service` and `PartOf=docker.service`. With the nftables backend it stops and says
   so. Docker is never restarted: on 2026-10-20 a restart killed another session's build.
4. Checks `tailscale status` for `<name>`, launches `images:debian/13` as a VM with
   `boot.autostart=true`, and waits for an IPv4 address with outbound HTTPS.
5. Joins Tailscale with `tailscale up --ssh --qr`: the owner scans or clicks. A one-off auth key may
   be pasted instead; it is passed as `--auth-key=file:/dev/stdin`, never stored. Prints the
   key-expiry instruction.
6. `flocc host add`, `flocc ship`, `flocc login`, then Incus snapshot `installed` (replacing an
   older one).

Each step checks before it changes anything, so re-running create continues a half-made host.

The laptop already carries broad settings applied by hand on 2026-10-19/20 (`incusbr0` in zone
`trusted`, `ip-forward-no-drop`, FORWARD ACCEPT). Part 3 replaces them with the scoped ones and
reverts the broad ones, without restarting Docker.

The VM still joins the tailnet as the owner, untagged (d44), so it can reach every tailnet device
and, through the uplink, the LAN. Part 3 states this in `docs/vm-setup.md`. Closing it means tagging
the node with a policy that denies its outbound tailnet traffic, which reopens d44; that is a
separate decision for the owner.

Tailscale SSH check mode can stop a command for a browser re-auth; create prints that URL rather
than hang.

## Part 3 testing

Against a fake runner: each step skips work already done; Docker never restarted; the nftables
backend stops create; the firewalld zone allows only dhcp and dns; an existing Tailscale node of
that name stops create; an auth key never appears in argv or the log; an existing snapshot is
replaced. Manual, recorded in `docs/vm-setup.md`: one create on zer0-day-nimble.

## Later

A GitHub App instead of two tokens: one-click setup by the manifest flow, no expiry, and PRs
authored by the app (so the owner can approve them under branch protection). It needs
installation-token refresh for sessions that outlive an hour. Recorded as the direction if
open-source users ask for it.

## Unconfirmed

- `claude remote-control`'s y/n consent is the same one-time confirmation `--remote-control`
  sessions read, and the wording of its prompt and connected line under a pty (the login's
  matchers are guesses until Part 2a's Daytona trial captures them).
- `claude plugin marketplace add` and `claude plugin install` work before any Claude login.
- A token with Actions and Commit statuses read sees Actions results in `gh pr checks`; otherwise
  agents use `gh run list --branch` and `gh run view --log-failed`.
- The Linear write probe refuses a read-only key and adds no history entry to the digest issue.
- `https://linear.app/settings/account/security` is the API-key page; the menu path (Settings →
  Account → Security & access) is printed as the fallback.
- GitHub checks the caller's permission before validating `head` on `POST /pulls`.
- dockerd does not reset `DOCKER-USER` on `docker network create`.
