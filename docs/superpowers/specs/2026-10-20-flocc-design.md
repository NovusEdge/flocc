# flocc: rename linear-agents, and polish installing a host

Date: 2026-10-20. Status: draft for review.

## Why

linear-agents is to become flocc (thicc-meme spelling, decided 2026-10-20), ahead of open-sourcing
it. The name is spelled out 1,352 times in 115 files, and on each host in the service user, its
paths, its units and its tmux socket, so the rename changes host layout and lands with a host
migration. Setting up la-nimble on 2026-10-19 also showed that installing a host takes manual work
nobody else could repeat. This spec covers both. It does not make flocc tracker- or
harness-agnostic, and it does not change sequencing (separate spec).

## Names

| What | Now | flocc |
|---|---|---|
| GitHub repo | `NovusEdge/linear-agents` | `NovusEdge/flocc` (GitHub redirects the old URL); org `flocc-sh` claimed now, used when public |
| Python distribution, module | `linear-agents`, `linear_agents` | `flocc`, `flocc` |
| Host CLI (agents and the owner on a host) | `linear-agents` | `flocc` |
| Desktop CLI (Go, today `la-setup` behind `just`) | `just ship`, `just host`, ... | `flocc` (see Desktop CLI) |
| Staged installer binary on a host | `la-setup` | `flocc-setup` |
| Go module | `github.com/NovusEdge/linear-agents` | `github.com/NovusEdge/flocc` |
| Claude Code plugin | `linear-agents` (`linear-agents:implementer`, `linear-agents:source-finder`) | `flocc` (`flocc:implementer`, `flocc:source-finder`) |
| Env vars | `LINEAR_AGENTS_{CONFIG,STATE,PHASE,RUN,LEDGER,LAUNCH_GRANTS}` | `FLOCC_*`, same suffixes |
| Service user, home | `linear-agents`, `/home/linear-agents` | `flocc`, `/home/flocc` |
| Service repo, worktrees, marketplaces | `~/agents/linear-agents`, `~/agents/worktrees`, `~/agents/marketplaces` | `~/flocc`, `~/worktrees`, `~/marketplaces` |
| Config, state | `~/.config/linear-agents/`, `~/.local/state/linear-agents/` | `~/.config/flocc/`, `~/.local/state/flocc/` |
| tmux socket, loop session | `-L linear-agents`, `la-loop` | `-L flocc`, `flocc-loop` |
| systemd user units | `linear-agents.service`, `.timer`, tmux unit | `flocc.service`, `flocc.timer`, `flocc-tmux.service` |
| Commit trailer | `Linear-Agents: NF-123` | `Flocc: NF-123` |
| Linear comment prefix | `🤖 linear-agents:` | `🤖 flocc:` |
| PR review header | `🤖 linear-agents review` | `🤖 flocc review` |
| ntfy title | `linear-agents` | `flocc` |

Unchanged: the Linear labels (`claude`, `escalation`, `needs-decision`, repo labels), branch names
`agent/<key>`, session names (`NF-211`, `NF-211-test`), the digest issue, the docket ledger.

### Old names still read

Linear comments, commits and PRs written before the rename stay. For one minor version (0.3.x):

- The poller ignores comments starting with either prefix, so the service never treats its own
  old comments as owner replies.
- The commit-msg hook writes `Flocc:`; the publisher's untrailed check accepts either trailer, so a
  rerun on a branch with old commits is not reported.

0.4.0 drops both. Nothing else reads an old name: env vars, paths, units and the ledger belong to
one host, and the migration below builds a new one.

## Desktop CLI

Today the desktop side is `just <recipe> <target> [flags]`, and `la-setup ship` takes the target
before its flags, so `just ship --skip-interactive root@la-nimble` fails. flocc gets one Go binary
on the desktop, `flocc`, built from `installer/` and installed with `go install` or `just install`:

| Command | Does | Replaces |
|---|---|---|
| `flocc host create <machine> <name>` | Creates an Incus VM on a machine (see Host create) | manual VM setup |
| `flocc ship <host>` | Installs or upgrades flocc on a host; never needs a TTY | `just ship <t> --skip-interactive` |
| `flocc login <host>` | The one interactive step: Claude login, trust, Remote Control consent | the login inside `ship` |
| `flocc status [<host>...]` | Runs `flocc status` on each host over SSH | `just status`, `just fleet-status` |
| `flocc attach <host> <session>`, `flocc logs <host>`, `flocc tick <host>` | as today | the same recipes |
| `flocc release <host> <key> [--keep-worktree] [--accept-tests]` | as today | `just release` |
| `flocc doctor <host>` | as today | `just doctor` |

Flags parse anywhere on the line (Go's `flag` stops at the first positional; the CLI moves flags
first before parsing). `<host>` is an SSH target; a bare name means `root@<name>`. The `just`
recipes stay as one-line wrappers for one minor version, then go.

On a host, `flocc` is the Python CLI. The two never share a PATH: the desktop has no host install,
and a host has no Go binary. On the owner's desktop the Python CLI runs as `uv run flocc` in the
repo, for development only.

### Ship never asks

`flocc ship` runs the installer with `--skip-interactive` always and reads answers from the saved
answers file or `--answers`. A missing answer fails with the key to set, not a prompt. When Claude is
not logged in on the host, ship finishes every other step and ends with "run `flocc login <host>`".
Today's Huh forms remain for a first install run as `flocc ship <host> --interactive`.

### The end of a ship

Ship prints a summary instead of `Done.`: the version now on the host, whether Claude is logged in
and with which login (full or token), each doctor check that failed, the timer state, and the live
sessions. Exit status is non-zero when any doctor check failed.

## Host create

`flocc host create <machine> <name> [--cpus 4] [--memory 16GiB] [--disk 100GiB]`, over SSH as root
on `<machine>`, does what took an evening on 2026-10-19:

1. Checks `incus` is installed and initialised, with bridge `incusbr0`. If not, it stops with the
   commands to run; it does not install Incus.
2. If firewalld runs, puts `incusbr0` in zone `trusted` (permanent and runtime).
3. If Docker runs and the FORWARD policy is DROP: adds `"ip-forward-no-drop": true` to
   `/etc/docker/daemon.json` (backup first) and sets the policy to ACCEPT now. Restarting Docker is
   needed for the setting to persist; before restarting, it lists running builds and containers
   (`docker ps`, `pgrep -a 'buildkit|docker build'`) and asks, or skips the restart with
   `--no-restart` and says the policy reverts on reboot until Docker restarts.
4. Launches `images:debian/13` as a VM (not a cloud variant: those boot-looped) and waits for its
   agent and an IPv4 address with outbound HTTPS.
5. Installs Tailscale in the VM and runs `tailscale up --ssh`, printing the login URL; it waits up
   to 10 minutes for the node to join. Joining stays interactive (decision d44: untagged, no auth
   key).
6. Runs `flocc ship <name>`, then takes Incus snapshot `installed`.

It ends with the next step: `flocc login <name>`. Every step checks before it changes anything, so
re-running create on a half-made host continues it.

Only Incus is supported. Daytona keeps its `just daytona-*` recipes.

## Login

A full Claude login is required: the long-lived token from `claude setup-token` cannot open Remote
Control sessions (Remote Control docs, troubleshooting), and folder trust and the Remote Control
consent have no documented non-interactive form. So login stays interactive, as one command:

`flocc login <host>` opens an SSH TTY as the service user and runs `claude` in `~/worktrees` with
the steps printed first (today's `loginNote`): `/login`, accept Remote Control, `/exit`. Afterwards
it checks `claude auth status` (or the existing `loggedIn` probe) and prints the result.

`flocc status` on a host shows the login: logged in, logged out, or token-only. When the login has
lapsed, the tick sends one tier 1 escalation (cause `claude-login`), since every new session would
fail and running Remote Control sessions stop and cannot recover until a new login.

## Migration

The rename moves every host path, so la-nimble is not renamed in place. A new VM takes over:

1. Merge the rename, tag `v0.3.0`.
2. `flocc host create zer0-day-nimble flocc-nimble`, then `flocc login flocc-nimble`. Copy the
   config from la-nimble with paths rewritten (`flocc ship` writes it from the same answers).
3. On la-nimble, set `session_cap = 0`. It starts nothing new and keeps supervising, publishing
   and reviewing its running claims. flocc-nimble starts new issues. Two hosts never start the same
   issue: only Todo issues are started, and a started issue leaves Todo.
4. When `linear-agents status` on la-nimble shows no live claims, stop its timer and the VM. Keep
   the VM stopped for 7 days, then delete it.

Lost by moving: remembered tester commits and acceptances in la-nimble's ledger, so an issue rerun
after the move starts again at the tester on its kept branch. Rerun issues before step 4 if that
matters.

## Version

The rename is 0.3.0: hosts, CLI names and env vars change incompatibly.

## Testing

- A test fails if `linear-agents`, `linear_agents`, `LINEAR_AGENTS` or `la-setup` appears in a
  tracked file outside `CHANGELOG.md`, the compatibility code above, and dated docs.
- Poller: a comment with either prefix is ignored. Publisher: either trailer counts as trailered.
- Desktop CLI: flags before and after positionals parse the same; a bare host name becomes
  `root@<name>`; `ship` never calls an interactive runner unless `--interactive`.
- Host create, against a fake runner: each step's check skips work already done; Docker is never
  restarted without consent or with `--no-restart`; a FORWARD DROP policy is reported.
- Ship summary: built from doctor output in a fixture, exit non-zero on a failed check.
- Manual, recorded in `docs/vm-setup.md`: create, login and ship of flocc-nimble; one issue run end
  to end on it; la-nimble drained.

## Unconfirmed

- `claude auth status` exists and reports login kind on 2.1.30x; otherwise the existing probe stays.
- Folder trust for `~/worktrees` covers every worktree under it, as it does today for
  `~/agents/worktrees` (claim c9).
- The org `flocc-sh` is still free when claimed.
