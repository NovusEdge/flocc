# Commands

`flocc` on the desktop is a Go command. It has desktop commands, which run on your machine, and it forwards any other command line to a host, where the Python `flocc` service command runs as the service user.

Bare `flocc` on a terminal opens the [dashboard](#the-dashboard). Without a terminal, or with no hosts yet, it prints the usage and exits 2.

## Desktop commands

| Command | Does |
|---|---|
| `flocc setup` | Everything in order: keys, a host, old versions, cardinal, logins. See [Set up](setup/README.md#flocc-setup) |
| `flocc keys` | Make, check and store the Linear key, GitHub tokens, ntfy topic and the optional Tailscale client. See [Keys](keys.md) |
| `flocc keys check` | Check every stored key again |
| `flocc keys rotate github\|linear` | Replace keys and push them to every host |
| `flocc host add <name> [--ssh DEST \| --daytona SANDBOX]` | Write the host file. See [Add a host](setup/README.md#add-a-host) |
| `flocc host import -H <name> [--ssh DEST \| --daytona SANDBOX]` | Build the host file from a host's own answers |
| `flocc host create [--cpus N] [--memory GiB] [--disk GiB] <machine> <name>` | Make an Incus VM and set it up. See [Create a host](setup/host-create.md) |
| `flocc host run -H <name> -- <command...>` | Run a raw command on the host over SSH, not the host's `flocc` |
| `flocc ship -H <name> [--interactive]` | Install or upgrade the service on a host. Run it inside the flocc checkout |
| `flocc login -H <name> [--email ADDR]` | Sign in to Claude on a host |
| `flocc cardinal setup\|login\|status\|attach\|restart\|remove` | The always-on assistant. See [cardinal](cardinal.md) |
| `flocc help`, `-h`, `--help` | Print the usage |

`ship`, `login`, `keys` and `host` are desktop commands. The host's own `flocc` refuses them with "that is a desktop command".

## Host commands

Any other command runs on a host as the service user, with the host's own grammar. Every remedy line a host prints works on the desktop with `-H` added.

```bash
flocc -H my-host doctor
flocc release ENG-12 --keep-worktree   # the host holding ENG-12
flocc -H my-host logs -f
flocc -H my-host attach ENG-12-review
flocc status -H all                    # every host
flocc host run -H my-host -- tmux -L flocc ls
```

| Command | Does |
|---|---|
| `status [--json]` | Claims, last tick, usage, disk, Claude login (full, token-only, none or unknown) and GitHub token expiries |
| `doctor [--json]` | Check config and host setup |
| `logs [-n N] [-f]` | Print the service log: the journal on systemd hosts, `run.log` otherwise |
| `logs --issue <key> [-n N]` | Only the service-log lines that name the issue, with a time on systemd hosts. It does not follow, so `-f` is refused |
| `peek <key> [-n N]` | Print the last N lines (default 40) of the issue's live session. Exits 3 when it has none |
| `attach <session>` | Attach read-only to an agent's tmux session, such as `ENG-12` or `ENG-12-review` |
| `release <key> [--keep-worktree] [--accept-tests]` | Clear a claim and kill its session. The issue goes back to Todo. `--accept-tests` approves the tester's files as they stand on the branch and keeps the worktree |
| `tick` | Run one service tick by hand. It takes the same lock as the timer's ticks |
| `run` | Run ticks in a loop |
| `permissions report [--days N]` | Count the auto-mode blocks and prompts sessions met. See [Run a host](setup/running-a-host.md#auto-mode-blocks) |
| `escalate`, `claim`, `commit`, `done`, `review-done`, `handoff-ready`, `context` | Used by agent sessions, not by you |
| `--version` | Print the host's version |

### Choosing the host

Without `-H`, the host is `$FLOCC_HOST`. Failing that it is the only host. For a command naming an issue key, it is the host whose `flocc status --json` holds that claim.

- `-H` is taken from anywhere before `--`, so an argument that is literally `-H` goes after `--`.
- `-H all` runs only `status`, `doctor` and `logs`, and `logs` without `-f`. `$FLOCC_HOST` cannot be `all`.
- `flocc cardinal` takes no `-H`. Limit its setup with `--only <host>`.

SSH to a host, `ship` and `login` included, shares one connection per host for ten minutes, so a Tailscale check-mode approval covers a whole run.

`ship`, `host import`, `keys rotate`, `doctor` and `logs` under `-H all`, and the lookup of the host holding an issue, open that connection in the terminal first, so the approval link shows. Without a terminal, a host that needs a prompt fails within 30 seconds with a message saying so.

`status -H all` does not open connections first. A host waiting on an approval shows as unreachable there, and `flocc -H <name> status` brings up its link.

## The display

Every desktop command that does work reports through one session of steps, drawn by one of three renderers.

| Mode | Looks like | Default for |
|---|---|---|
| full | Alternate screen: a rail of the command's sections, the current section's steps with their live output, a progress bar. Prompts open as a modal over the dimmed screen | `setup`, `host create`, `ship`, `login`, `cardinal setup`, `cardinal login`, and the dashboard |
| inline | Running steps and any prompt below the scrollback. Each step moves up into scrollback when it ends | Short commands: `keys`, `keys check`, `cardinal restart`, one-shot `status` |
| plain | One line per finished step, numbered or typed prompts, no escape codes | Anything that is not a terminal |

Choose one with `--full`, `--inline` or `--plain`; the last one given wins. With no flag, `FLOCC_UI=full|inline|plain` applies:

```bash
flocc setup --inline
FLOCC_UI=plain flocc ship -H my-host
```

A pipe, `NO_COLOR` or `TERM=dumb` gives plain whatever is set. Commands that hand the terminal to a child (forwarded host commands, `host run`, `cardinal attach`) always run plain. A full-screen run on a terminal smaller than 80 columns by 20 rows switches to inline.

In full-screen:

- `l` shows the whole log of the running step. `esc` closes it, and arrows and page keys scroll.
- `?` lists the keys.
- `q` or Ctrl-C asks whether to stop.

When the run ends, the rows and the "still yours to do" box are printed to the normal screen, so they stay after the program exits. Inline and plain print the rows as they end and only the box at the end.

When a step needs the real terminal (a `sudo` password, a Tailscale login, a browser login), the screen is handed over with one line naming what comes next, and Ctrl-C goes to that program.

## The dashboard

Bare `flocc`, or `flocc status --watch`, on a terminal. It shows:

- A strip per host: version, last tick, usage, disk and capacity.
- One table of every host's issues: phase, state, context use, launches and last update.
- The selected issue's recent activity.

It polls every host's `status --json` every 10 seconds. An unreachable host shows a red dot, and its last known rows are marked stale. A host older than the desktop is shown as needing a ship.

| Key | Does |
|---|---|
| up, down (or k, j) | Select an issue |
| a | `flocc -H <host> attach <issue>` |
| l | `flocc -H <host> logs <issue>` |
| r | Release the issue, after a confirmation |
| c | cardinal, read-only attach |
| ? | List the keys |
| q, Ctrl-C | Quit |

`a`, `l` and `c` hand the terminal to the child and return to the dashboard when it exits.

## The fleet table

`flocc status -H all` prints every host once, in two tables. Plain mode, and `--watch` without a terminal, print the same.

- One row per host: mode, last tick and exit status, running sessions against the cap, 5-hour and weekly usage, free disk, Claude login and token expiry.
- One row per live claim: phase, age, context use, and whether its tmux session is up.

Cells are colored for usage at the configured `five_hour_max_pct` and `weekly_max_pct`, disk under 15% free, a last tick older than 10 minutes, and a non-zero tick exit. LOGIN is red unless it is a full claude.ai login, and yellow `unknown` when the host could not read it. TOKENS is the GitHub token that expires first: yellow under 14 days, red under 3 days or when GitHub rejected it.

Hosts come from `~/.config/flocc/hosts/` (reached over SSH) and from every sandbox in the active Daytona organization, read with the Daytona CLI's login. Without a Daytona login the Daytona hosts are skipped with a note.

An unreachable host gets a row and an error line, and the rest still render. A host file whose `ssh` starts with `-` is skipped with a note, since ssh would read it as an option.

Each host answers `status --json`, which only reads state: it takes no tick lock and calls neither Linear nor GitHub. The `est $/h` column prices started Daytona sandboxes only, from vCPU, memory and disk beyond 5 GiB at Daytona's list rates, so it is an estimate. Stopped sandboxes show `-`.

## Files

| Path | Holds |
|---|---|
| `~/.config/flocc/hosts/<name>.toml` | A host's answers: SSH destination, team, digest issue, repositories |
| `~/.config/flocc/secrets.toml` | Keys, when there is no OS keyring |
| `~/.cache/flocc/tailnet-policy.json` | The policy setup writes for you to paste |
| `~/.cache/flocc/create-<name>.log` | The log of a `host create` run |
| `~/.cache/flocc/cardinal-<user>.log` | The log of a cardinal command |
