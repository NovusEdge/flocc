# Commands

`flocc` on the desktop is a Go command. It has desktop commands, which run on your machine, and it forwards any other command line to a host, where the Python `flocc` service command runs as the service user.

Bare `flocc` on a terminal opens the [dashboard](#the-dashboard). Without a terminal, or with no hosts yet, it prints the usage and exits 2.

## Desktop commands

| Command | Does |
|---|---|
| `flocc setup` | Everything in order: keys, a host, old versions, logins. See [Set up](setup/README.md#flocc-setup) |
| `flocc keys` | Make, check and store the Linear key, GitHub tokens and the optional Tailscale client. See [Keys](keys.md) |
| `flocc keys check` | Check every stored key again |
| `flocc keys rotate github\|linear` | Replace keys and push them to every host |
| `flocc host add <name> [--ssh DEST \| --daytona SANDBOX]` | Write the host file. See [Add a host](setup/README.md#add-a-host) |
| `flocc host import -H <name> [--ssh DEST \| --daytona SANDBOX]` | Build the host file from a host's own answers |
| `flocc host create [--cpus N] [--memory GiB] [--disk GiB] <machine> <name>` | Make an Incus VM and set it up. See [Create a host](setup/host-create.md) |
| `flocc host cardinal -H <name> [--renew-hour H]` | Make or repair the cardinal VM beside that host. See [cardinal](cardinal.md) |
| `flocc host run -H <name> -- <command...>` | Run a raw command on the host over SSH, not the host's `flocc` |
| `flocc ship -H <name> [--interactive]` | Install or upgrade the service on a host. Run it inside the flocc checkout |
| `flocc login -H <name> [--email ADDR]` | Sign in to Claude on a host |
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
| `status [--json]` | Running and finished runs (step, state, fix rounds, age, context, session, PR), cardinal's holds, events waiting for cardinal and those needing attention, last tick, usage, disk, Claude login and GitHub token expiries |
| `doctor [--json]` | Check config and host setup |
| `logs [-n N] [-f]` | Print the service log: the journal on systemd hosts, `run.log` otherwise |
| `logs --issue <key> [-n N]` | Only the service-log lines that name the issue, with a time on systemd hosts. It does not follow, so `-f` is refused |
| `peek <key> [-n N]` | Print the last N lines (default 40) of the issue's live session. Exits 3 when it has none |
| `attach <session>` | Attach read-only to an agent's tmux session, such as `ENG-12` or `ENG-12-review` |
| `release <key> [--keep-worktree] [--accept-tests] [--hold-only]` | Clear a claim and kill its session, and lift a hold on the issue. The issue goes back to Todo. `--accept-tests` approves the tester's files as they stand on the branch and keeps the worktree. With no claim, it only lifts the hold. `--hold-only`, cardinal's release, lifts a hold and nothing else, and is refused for a blocked, timed-out or done run |
| `hold <key> [--reason TEXT]` | Start nothing for the issue until `release` lifts the hold. A run in progress goes on |
| `resume <key> --note TEXT` | Restart a blocked or timed-out run once, on its branch, with the note: the answer to a `needs-owner` stop, or direction after a stop typed `agent` (`check-failed` with rounds left, `no-changes`, `idle`, `crash` or a timeout). Refused for other stops |
| `run-summary <key>` | The run's step, its last summary, why it stopped, a check excerpt and log lines, at most 6000 characters |
| `digest-data [--done N]` | What the daily digest covers. `--done N` clears the digest lines through N once the digest is sent |
| `cleanup [--now]` | Remove worktrees, state, scratchpads and finished pytest runs under `/tmp/pytest-of-flocc-agent` that no claim owns. A worktree with uncommitted changes, commits not on GitHub, or a change in the last 7 days stays and is listed. `--now`, cardinal's repair for a full `/tmp`, takes scratchpads idle 10 minutes and pytest runs of any age that no live process holds, and waits up to 60 seconds for a running tick |
| `host-report` | `/tmp` use, memory available and OOM kills against their limits, the open host episode, the ten largest `/tmp` entries of `flocc-agent`'s, process memory per issue and container memory |
| `stop-tests <key>` | Kill the processes the issue's session started through its Bash tool and remove the issue's containers, then tell the session not to rerun its tests until the host recovers |
| `shell --command CMD` | Run one command as `flocc-agent` (cardinal's `agent-shell`). 540-second timeout, output capped at 24,000 characters, listed in the digest |
| `issue <key>` | Print an issue's title, state, labels, description, relations and comments, at most 12,000 characters (cardinal's `issue`) |
| `pr <key>` | Print the state, mergeability, review decision, diff size, checks, reviews and comments of the issue's PR, with the publish token |
| `comment <key> --text TEXT` | Comment on the issue as cardinal, through the service's Linear key. Listed in the digest |
| `pr-comment <key> --text TEXT` | Comment on the issue's PR as cardinal, with the publish token. Listed in the digest |
| `search --query TEXT` | The team's open issues whose title or description holds the text, at most 20 |
| `file <key> --title TEXT --text TEXT` | With `cardinal_file_on`, create a Backlog issue labelled `cardinal-draft` and related to the key; otherwise record the draft for the digest. At most 2 per source issue and 5 per 24 hours, drafts included |
| `config list` | The [settable keys](#settable-config-keys), each with its value and whether `config.toml` sets it or the default applies |
| `config get <key>` | Print one settable key's effective value |
| `config set <key> <value>` | Write a top-level key of `config.toml`, such as `config set session_cap 3`. A key that is not settable, or a value outside its range, exits 2 with one line. The edited file must also load, or the original stays and the loader's error prints. The old file is kept as `config.toml.bak`. It applies from the next tick; a running tick keeps the config it loaded |
| `config unset <key>` | Remove the key's line so its default applies |
| `events [--follow] [--hello]` | Print unacknowledged events as JSON lines of at most 50. A stopped run's event carries its `cause` and `type`; other events carry null. `--follow` keeps printing: a batch closes 2 minutes after its first event, and an unacknowledged event is printed again every 30 minutes, at most 3 times per connection, and on every new connection |
| `ack <id>...` | Acknowledge events. Acking twice is harmless |
| `tick` | Run one service tick by hand. It takes the same lock as the timer's ticks |
| `run` | Run ticks in a loop |
| `done`, `tests-amended`, `commit`, `context` | Used by agent sessions, not by you. Every archetype ends its run with `flocc done`, which carries its summary |
| `guard` | PreToolUse hook, run from the managed settings: denies `AskUserQuestion`, `EnterPlanMode`, `ExitPlanMode`, `git push`, GitHub writes through `gh` and Linear tools, and keeps the reviewer to `review.md` |
| `stop [--failure]` | Hook for agent sessions at the end of a turn. A turn that ends without `flocc done` is refused once; ending again marks the session idle and the run ends blocked. With `--failure`, an API error other than a usage limit marks it idle at once |
| `--version` | Print the host's version |

The five verbs that take a key (`issue`, `pr`, `comment`, `pr-comment`, `file`) refuse one outside `team_key`.

### Settable config keys

`config set` accepts only these keys, within these ranges: `session_cap` 1 to 6; `check_timeout_s` and `setup_timeout_s` 60 to 7200; `check_fix_rounds` 0 to 3; `default_wall_clock_s` and `review_wall_clock_s` 600 to 21600; `min_available_mem_gb` 0 to 64; `min_free_disk_gb` 0 to 1000; `tmp_max_pct` 1 to 100; `digest_hour` 0 to 23; `shared_uv_cache`, `skip_unmapped` and `cardinal_file_on`, `true` or `false`. Models, efforts, usage limits, labels, tokens and repos are not settable with it.

### The `Run:` line

An issue picks its archetypes with a `Run:` line in its description, such as `Run: tester, executor, reviewer`. The default is `executor, reviewer`. Names are `tester`, `executor` and `reviewer`, case-insensitive, in that order, with `executor` exactly once; the service adds fixers itself. An unknown name or a wrong order skips the issue with one comment. Without a tester, the publisher skips the tester-file guard.

### Choosing the host

Without `-H`, the host is `$FLOCC_HOST`. Failing that it is the only host. For a command naming an issue key, it is the host whose `flocc status --json` holds that claim.

- `-H` is taken from anywhere before `--`, so an argument that is literally `-H` goes after `--`.
- `-H all` runs only `status`, `doctor` and `logs`, and `logs` without `-f`. `$FLOCC_HOST` cannot be `all`.

SSH to a host, `ship` and `login` included, shares one connection per host for ten minutes, so a Tailscale check-mode approval covers a whole run.

`ship`, `host import`, `keys rotate`, `doctor` and `logs` under `-H all`, and the lookup of the host holding an issue, open that connection in the terminal first, so the approval link shows. Without a terminal, a host that needs a prompt fails within 30 seconds with a message saying so.

`status -H all` does not open connections first. A host waiting on an approval shows as unreachable there, and `flocc -H <name> status` brings up its link.

## The display

Every desktop command that does work reports through one session of steps, drawn by one of three renderers.

| Mode | Looks like | Default for |
|---|---|---|
| full | Alternate screen: a rail of the command's sections, the current section's steps with their live output, a progress bar. Prompts open as a modal over the dimmed screen | `setup`, `host create`, `ship`, `login`, and the dashboard |
| inline | Running steps and any prompt below the scrollback. Each step moves up into scrollback when it ends | Short commands: `keys`, `keys check`, one-shot `status` |
| plain | One line per finished step, numbered or typed prompts, no escape codes | Anything that is not a terminal |

Choose one with `--full`, `--inline` or `--plain`; the last one given wins. With no flag, `FLOCC_UI=full|inline|plain` applies:

```bash
flocc setup --inline
FLOCC_UI=plain flocc ship -H my-host
```

A pipe, `NO_COLOR` or `TERM=dumb` gives plain whatever is set. Commands that hand the terminal to a child (forwarded host commands and `host run`) always run plain. A full-screen run on a terminal smaller than 60 columns by 20 rows switches to inline. Below 100 columns the rail of sections sits above the steps instead of beside them.

In full-screen:

- `l` shows the whole log of the running step. `esc` closes it, and arrows and page keys scroll.
- `?` lists the keys.
- `q` or Ctrl-C asks whether to stop.

When the run ends, the rows and the "still yours to do" box are printed to the normal screen, so they stay after the program exits. Inline and plain print the rows as they end and only the box at the end.

When a step needs the real terminal (a `sudo` password, a Tailscale login, a browser login), the screen is handed over with one line naming what comes next, and Ctrl-C goes to that program.

## The dashboard

Bare `flocc`, or `flocc status --watch`, on a terminal. The fleet screen has three panes:

- hosts: per host, its status version, last tick, live sessions against the cap, usage and free disk.
- issues: every claimed issue with its Linear title, phase, state, context use and host. Titles come from the Linear key stored by `flocc keys`; without it the keys show alone, and the line above the key bar says why.
- activity: the selected issue's recent service-log lines.

It polls every host's `status --json` every 10 seconds; the top line shows a spinner while a poll is out. A host whose poll failed keeps its last report, drawn dim with the error on its row. A host older than the desktop is shown as needing a ship.

Each row opens into a screen of its own:

- An issue: its phases, state, session and host, and the live tmux pane of its session, refreshed every 2 seconds. With no live session the pane shows the service-log lines that name the issue.
- A host: its facts, its `doctor --json` checks and the tail of its service log.

A host shipped before `peek` and `logs --issue` existed shows "ship to see session output" in place of the output.

| Key | Where | Does |
|---|---|---|
| up, down (or k, j) | any list | Move the selection |
| tab | fleet | Move between the hosts and issues panes |
| right, enter, l | fleet | Open the selected row |
| / | fleet | Filter issues by key, title, host or state; `esc` clears it |
| a | issues pane, issue screen | `flocc -H <host> attach <issue>` |
| r | issues pane, issue screen | Release the issue, after `y` on the line above the key bar |
| o | issue screen | Open the issue in Linear |
| L | issue screen | The output in the pager |
| d | host screen | Run doctor again |
| s | host screen | `flocc ship -H <host>`; dimmed when the dashboard did not start inside a git checkout |
| l | host screen | The service log in the pager |
| esc, left | any screen | Back |
| ? | anywhere | List the keys |
| q | fleet | Quit; Ctrl-C quits anywhere |

The pager scrolls with `j`, `k`, page up and down, `g` and `G`, searches with `/` and `n`, and follows new lines with `f`.

Keys that do nothing on the current row stay in the key bar, dimmed. An action that fails shows its error at the top right for 4 seconds. Below 60 columns by 20 rows the dashboard says the terminal is too small and only `q` and Ctrl-C work. Panes that sit side by side stack below 100 columns. The colours follow the terminal's background, light or dark.

`a` and `s` hand the terminal to the child, and so does `r` after its `y`. Each returns to the dashboard when the child exits.

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