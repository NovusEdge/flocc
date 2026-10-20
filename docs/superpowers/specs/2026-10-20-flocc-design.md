# flocc: rename linear-agents, give it a desktop CLI with easy credentials, and script host creation

Date: 2026-10-20. Status: draft, revised after an Opus review.

linear-agents becomes flocc (thicc-meme spelling, decided 2026-10-20). The repo stays on the
owner's account as `NovusEdge/flocc`. Setting up la-nimble on 2026-10-19 showed that installing a
host takes manual work and several logins nobody else could repeat. This spec has three parts,
built and shipped in this order, one plan each:

1. **Rename and migration** (0.3.0): every name changes, and a new VM replaces la-nimble.
2. **Desktop CLI and credentials** (0.4.0): one `flocc` command on the desktop; credentials are
   entered once, checked on entry and reused for every host; `ship` never prompts; `login` is one
   step.
3. **Host create** (0.5.0): one command makes a VM on a machine without broad firewall changes.

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
| Desktop hosts file | `~/.config/la-setup/hosts` | `~/.config/flocc/hosts` (old path read once, with a notice) |
| Claude Code plugin, `gate.PREFIX` | `linear-agents`, `linear-agents:` | `flocc`, `flocc:` |
| Env vars | `LINEAR_AGENTS_{CONFIG,STATE,PHASE,RUN,LEDGER,LAUNCH_GRANTS}` | `FLOCC_*` |
| Service user, home | `linear-agents`, `/home/linear-agents` | `flocc`, `/home/flocc` |
| Service repo, worktrees, marketplaces | `~/agents/linear-agents`, `~/agents/worktrees`, `~/agents/marketplaces` | `~/flocc`, `~/worktrees`, `~/marketplaces` |
| Config, state | `~/.config/linear-agents/`, `~/.local/state/linear-agents/` | `~/.config/flocc/`, `~/.local/state/flocc/` |
| tmux socket, loop session | `-L linear-agents`, `la-loop` | `-L flocc`, `flocc-loop` |
| systemd user units | `linear-agents.{service,timer}`, tmux unit | `flocc.{service,timer}`, `flocc-tmux.service` |
| Markers and probes | `.la-commit`, `la-commit.lock`, `.claude.json.linear-agents.tmp`, `refs/heads/linear-agents-doctor-probe` | `.flocc-commit`, `flocc-commit.lock`, `.claude.json.flocc.tmp`, `refs/heads/flocc-doctor-probe` |
| Default Tailscale hostname | `linear-agents` | `flocc` |
| Commit trailer | `Linear-Agents: NF-1 impl` | `Flocc: NF-1 impl` |
| Linear comment prefix, PR review header, ntfy title | `🤖 linear-agents:`, `🤖 linear-agents review`, `linear-agents` | `🤖 flocc:`, `🤖 flocc review`, `flocc` |

Unchanged: Linear labels, branch names `agent/<key>`, session names, the digest issue, the `just`
recipes (until Part 2).

Outside the code, the same change goes into: docket scopes (re-scoped with `docket correct`, never
by hand), `docs/`, the owner's `~/.claude/CLAUDE.md` linear-agents section, the auto-memory files,
and the teammate how-to in NF-215.

## Branches survive a host move

Today a new host has no local `agent/<key>`, cuts a fresh one from main, and its push is rejected
after the whole run. `prepare_worktree` changes: with no local branch, it fetches
`origin/agent/<key>` and starts from it when it exists. The tester commit is recovered from the
branch: the last commit whose trailer is `Flocc: <KEY> test` or `Linear-Agents: <KEY> test`. That
replaces the remembered-tests row a new host lacks, so the rerun resumes at the implementer with the
tests-touched guard intact. The publisher names a non-fast-forward rejection as "the branch moved on
GitHub (another host or a person pushed)" instead of a token problem.

Reading the old trailer is needed only here; the comment-prefix check accepts the old prefix as
insurance. Both go in 0.5.0.

## Migration

1. Merge Part 1, tag `v0.3.0`. Ship refuses a target that has a `linear-agents` user or timer, so
   it cannot install a second poller on la-nimble.
2. Check the laptop has memory for two VMs. Build `flocc-nimble` by hand as in `docs/vm-setup.md`
   (Incus `images:debian/13`), then the interactive `just ship root@flocc-nimble`. Give it
   `session_cap` = the intended total minus la-nimble's running sessions.
3. Drain la-nimble: set its `trigger_label` to `claude-drained`, a label no issue has. It stops
   polling Todo, so only flocc-nimble starts issues, while la-nimble keeps supervising, publishing,
   reviewing and forwarding comments for its claims. Drain commands for the old host are plain SSH:
   `ssh root@la-nimble sudo -iu linear-agents linear-agents status` (and `release`).
4. When no claim on la-nimble is live: push every `agent/*` branch, wait for that day's digest,
   copy `~/.local/state/linear-agents` to the desktop, stop its timer and the VM, and remove it from
   the hosts file. Delete the VM 7 days later.

Rollback until step 4's deletion: set la-nimble's trigger label back to `claude` and stop
flocc-nimble's timer.

## Part 1 testing

- A test fails if a tracked file matches `linear[-_ ]agents` (any case), `\bla-(setup|loop|commit|ship)`
  or `\.la-commit`, outside `CHANGELOG.md`, `.docket/`, the two compatibility readers and dated
  docs under `docs/`.
- `prepare_worktree` with only `origin/agent/<key>` starts from it; the tester commit is recovered
  from either trailer; with neither, a fresh branch from main as today.
- The publisher's non-fast-forward message.
- Ship refuses a host with a `linear-agents` user.

---

# Part 2: desktop CLI and credentials

## One command, the host's grammar

The desktop gets a Go binary `flocc` (`installer/cmd/flocc`); the staged installer becomes its own
`main` (`installer/cmd/flocc-setup`), so a bare `flocc` prints help and never reaches the root phase.

Every host command is the host's own command line with a host selector:
`flocc -H flocc-nimble release --keep-worktree NF-12` runs `flocc release --keep-worktree NF-12`
over SSH as the service user. The remedy lines the service writes therefore work on the desktop
with `-H` added. `-H` defaults to `$FLOCC_HOST`, then to the only host in `~/.config/flocc/hosts`.
`-H all` fans out (today's `fleet-status`).

Desktop-only verbs: `ship`, `login`, `keys`, `host` (Part 3). The host CLI answers them with "that
is a desktop command"; the desktop binary refuses to run where `~/.config/flocc/config.toml`
exists, so the two never act for each other. The `just` recipes become one-line wrappers, then go
in 0.5.0.

## Credentials: entered once, checked on entry, reused everywhere

Today every new host asks for the Linear key, two GitHub tokens and an ntfy topic, in a TTY, with
help text to find each page, and checks them only later in doctor. They are kept nowhere on the
desktop, so the next host asks again.

**The desktop keeps them.** `~/.config/flocc/secrets.toml`, mode 600, holds `linear_key`,
`agent_token`, `publish_token`, `ntfy_topic`, and later `tailscale_auth_key`. Non-secret answers per
host live in `~/.config/flocc/hosts/<name>.toml`. `ship` sends both to the host on stdin, never in
argv or a file left behind.

**`flocc keys` fills what is missing, one key at a time:**

- It opens the page where that key is made (`xdg-open`/`open`, with the URL and a terminal QR code
  printed too, for a phone or another machine). For the GitHub tokens it uses the fine-grained token
  page with name, description, expiry, resource owner and permissions filled in by URL parameters,
  so the owner only picks the repos and clicks Generate.
- It waits for one paste, then checks the key live before saving it: the Linear key reads the
  viewer and the team; each GitHub token reads every mapped repo, and the publish token can push a
  probe ref and open a PR. A failure names what is missing ("agent token cannot read
  nimble-fox-ai/nimblefox-web: add it under Repository access").
- The ntfy topic is generated (random, 24 characters) and shown as a QR code to subscribe from the
  phone. Nothing to type.
- `flocc keys import -H la-nimble` copies an existing host's keys into the desktop file, for the
  migration and for anyone who already has a host.
- `flocc keys rotate github` reopens the page and pushes the new token to every host.

**Expiry is watched.** `flocc status` shows each GitHub token's expiry (GitHub returns it in the
`github-authentication-token-expiration` header) and the Claude login. The tick escalates once at 7
days before a token expires, and at once when the Claude login lapses, since every new session
would fail.

## Login is one link and one paste

A full Claude login is required: the long-lived `claude setup-token` token cannot open Remote
Control sessions (Remote Control docs), and the Remote Control consent has no non-interactive form.
Folder trust is already written by the service before each launch (d33), so login does not need it.

`flocc login -H <host>` drives the Claude UI for the owner instead of handing over a terminal:

1. On the host it starts `claude` in a tmux pane on the service's socket, in `~/worktrees`, and
   sends `/login`.
2. It reads the login URL from the pane, opens it on the desktop and prints it with a QR code.
3. The owner signs in and pastes the code into flocc's prompt; flocc types it into the pane.
4. It answers the "Enable Remote Control" consent, sets the push settings, and exits Claude.
5. It checks `claude auth status` (`authMethod` `claude.ai`) and then runs the Run step: doctor, and
   the timer is enabled.

If a screen it expects does not appear within 30 seconds, it attaches the owner to the pane with
the remaining steps printed, so login never dead-ends.

## Ship never asks

`flocc ship <host>` reads the desktop secrets and the host's answers file; a missing value fails at
once naming `flocc keys` or the answers key. `loggedIn` probes for real in every mode. When the only
open item is the Claude login, ship leaves the timer off, ends with `flocc login -H <host>`, and
exits 0. `--interactive` keeps today's forms for anyone who wants them.

Ship prints a summary instead of `Done.`: version on the host, Claude login (full, token-only or
none), token expiries, each failed doctor check, the timer, live sessions. It exits non-zero when a
check other than the pending login fails.

## Part 2 testing

- `-H` passes the line through unchanged; `-H all` fans out; desktop verbs on the host CLI and host
  use of the desktop binary are refused.
- Keys: each validator against recorded responses (good key, wrong team, a repo missing from a
  token, an expired token); secrets never appear in argv of any runner call.
- Fresh host against a fake runner: ship → login pending, timer off, exit 0; login → timer on.
- Login driver against a scripted fake pane: URL extracted, code typed, consent answered; a missing
  screen falls back to attach.

---

# Part 3: host create

`flocc host create <machine> <name>` over SSH as the owner's account on `<machine>`, using `sudo`
or `incus` group membership (not root SSH):

1. Checks `incus` is initialised with bridge `incusbr0`, a storage pool with room for the disk, and
   free memory; it stops with the commands to run otherwise.
2. If firewalld runs, puts `incusbr0` in a zone allowing only `dhcp` and `dns`, with a policy that
   forwards `incusbr0` to the uplink. Agents in the VM run unreviewed code and must not reach the
   machine's own services.
3. If Docker runs, adds two `DOCKER-USER` rules scoped to `incusbr0` (accept from it; accept
   related/established to it), installed by a oneshot unit with `After=docker.service` and
   `PartOf=docker.service`. Docker is never restarted: on 2026-10-20 a restart killed another
   session's build.
4. Checks `tailscale status` for `<name>` (a stale node would make ship target the wrong machine),
   launches `images:debian/13` as a VM with `boot.autostart=true`, and waits for an IPv4 address
   with outbound HTTPS.
5. Joins Tailscale: with `tailscale_auth_key` in the desktop secrets, no click; otherwise it prints
   the login URL with a QR code, waits up to 10 minutes, and prints the key-expiry instruction.
6. `flocc ship <name>`, `flocc login -H <name>`, then Incus snapshot `installed` (replacing an older
   one).

Each step checks before it changes anything, so re-running create continues a half-made host.

The laptop already carries the broad settings applied by hand on 2026-10-19/20 (`incusbr0` in zone
`trusted`, `ip-forward-no-drop` and FORWARD ACCEPT). Part 3 replaces them with the scoped ones and
reverts the broad ones, without restarting Docker.

Tailscale SSH under the default policy can stop for a browser re-auth (check mode); create and ship
print that URL when it happens rather than hang.

## Part 3 testing

Against a fake runner: each step skips work already done; Docker is never restarted; the firewalld
zone allows only dhcp and dns; an existing Tailscale node of that name stops create; an existing
snapshot is replaced. Manual, recorded in `docs/vm-setup.md`: one create on zer0-day-nimble.

## Unconfirmed

- GitHub's fine-grained token page accepts name, description, expiry, owner and permission URL
  parameters; if not, flocc opens the plain page with the values printed to copy.
- The Claude UI screens `flocc login` drives (login URL line, code prompt, Remote Control consent)
  can be recognised from pane text on 2.1.30x; the attach fallback covers a change.
- dockerd does not reset `DOCKER-USER` on `docker network create`.
