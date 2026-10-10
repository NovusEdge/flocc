# Setup installer design

Status: draft for review, 2026-10-10. Replaces the manual steps in `docs/vm-setup.md` with one
program; the smoke test section of that file stays manual.

## Goal

One command from my desktop takes a fresh Linux host to a running linear-agents service, and
the same command run again finishes a setup that stopped halfway. The old VM is gone, so this
is also how the smoke test gets a machine again: a Daytona sandbox, run for the smoke test and
then stopped.

### Success criteria

- `just ship <target>` on a fresh Debian, Ubuntu, Arch, Fedora or RHEL VM, or a Daytona
  sandbox, ends with `linear-agents doctor` reporting no FAIL line, with me only logging in to
  Claude and Tailscale, pasting three secrets and answering the config form.
- A run that fails at any step can be re-run and resumes at that step without redoing the
  earlier ones or asking for their answers again.
- The installer never starts ticking while doctor reports a FAIL line.

### Out of scope

- Creating anything in Linear. The NF team already has the `claude`, `escalation`,
  `needs-decision` and repo labels and the digest issue; the installer only checks them, through
  doctor.
- Creating GitHub tokens. Fine-grained tokens can only be made in the browser; the installer
  shows the permissions each one needs.
- Creating or stopping Daytona sandboxes; `docs/vm-setup.md` gives the commands.
- Uninstalling, and the smoke test steps.

## Runtime modes

The installer picks the mode from PID 1 and shows it in the step list.

| | systemd mode | loop mode |
|---|---|---|
| When | `/proc/1/comm` is `systemd` | anything else, such as a Daytona container |
| Ticks | `linear-agents.timer` every 3 min | `linear-agents run` in tmux session `la-loop` |
| tmux server | `linear-agents-tmux.service` | started by the `la-loop` session |
| Processes a timed-out check daemonized | killed with the tick unit's cgroup | survive until the host stops |
| After a reboot or sandbox stop | lingering units restart | `just ship` again restarts tailscaled and the loop |

`linear-agents run` is a new Python command: it runs `linear-agents tick` as a subprocess every
180 seconds, appends each tick's output to `<state_root>/run.log`, and keeps looping when a tick
fails, as the timer does. The subprocess keeps one failing tick from ending the loop. Ticks
already use `tmux -N` and look sessions up by name, so `la-loop` on the same socket is invisible
to them.

Loop mode exists for short runs on Daytona, where VM sandboxes are not available on either of my
organizations. The surviving-daemon gap is accepted there: a check daemonizing out of its process
group is rare, and the sandbox is stopped after the smoke test.

## Shape

The installer is one Go program, `la-setup`, in `installer/`. The `go.mod` sits at the repo root
because `go:embed` cannot reach a parent directory: a small `deploy` package next to the unit
files embeds them. On the host
it is a plain `main` that walks the steps in order: each step that needs input runs a standalone
`huh` form, long commands run under huh's spinner, and interactive logins (`claude`,
`tailscale up`) run as ordinary child processes attached to the terminal, since no Bubble Tea
program is running between forms. On the desktop, `la-setup ship <target>` does the shipping.

Libraries: `charm.land/huh/v2` (forms and spinner) and `charm.land/lipgloss/v2` (the step
list), Charm's v2 line. Rejected: a full Bubble Tea app with a persistent checklist and
`tea.ExecProcess`, about three times the code for a nicer frame, and gum with a shell script,
which has no state between steps.

The unit files and `config.example.toml` are embedded from `deploy/` with `go:embed` at build
time, so the installer cannot install units that differ from the repo it ships with.

### Shipping

`just ship <target>` builds `la-setup` for the desktop and for `linux/amd64` and `linux/arm64`
(`CGO_ENABLED=0`), then runs `la-setup ship <target>`, which:

1. Resolves the target. `daytona:<sandbox>` asks the Daytona API for a 60-minute SSH token
   (`POST /api/sandbox/<id>/ssh-access`, with the access token and organization from the
   Daytona CLI's `~/.config/daytona/config.json`) and connects as `<token>@ssh.app.daytona.io`.
   Anything else is passed to `ssh` as given, such as a Tailscale name.
2. Reads `uname -m` on the host and picks the matching binary.
3. Writes `git bundle create` of HEAD for this repo. The repo is private, and cloning from the
   bundle means the host needs no GitHub credential for the service's own code. Upgrading the
   service is `just ship` again.
4. Writes the plugin list: the `enabledPlugins` of my user-scope Claude settings and the
   marketplaces they come from. Each marketplace whose GitHub repo is private
   (`gh repo view --json visibility`) is bundled from its local clone under
   `~/.claude/plugins/marketplaces/` the same way.
5. `scp`s the binary, bundles and plugin list to `/tmp/la-setup/` and runs
   `ssh -t <target> sudo /tmp/la-setup/la-setup`.

## Steps

Each step has a check, which reports whether its outcome already holds, and an apply, which
makes it hold. On start the installer runs every check and shows the list with each step's
state; a multi-select lets me pick finished steps to redo (to replace a secret, say). Then it
applies the steps that are not done, in order.

The first four steps run as root. Step 4 copies the binary, bundles and plugin list into the
service user's home and re-executes the installer as that user (`sudo -iu linear-agents
~/.cache/la-setup/la-setup --phase service`), so nothing after it has root.

| # | Step | As | Check | Apply |
|---|------|----|-------|-------|
| 1 | Preflight | root | `/etc/os-release` `ID`/`ID_LIKE` maps to apt, pacman or dnf; arch is amd64 or arm64; picks the runtime mode | refuses with the reason; in loop mode, shows the table row above and asks to continue |
| 2 | Packages | root | `git`, `tmux`, `curl`, `gh`, `node` on `PATH` | installs the missing ones; on Debian and Ubuntu adds GitHub's apt repo for `gh`, on RHEL GitHub's rpm repo |
| 3 | Tailscale | root | `tailscale status --json` shows `BackendState` `Running` | installs with `tailscale.com/install.sh`, or `pacman -S tailscale` on Arch; starts tailscaled (`systemctl enable --now tailscaled`, or `nohup tailscaled` in loop mode); if logged out, asks for a hostname and runs `tailscale up --hostname=<name>` attached to the terminal so I open the URL it prints; then `tailscale set --ssh`; reminds me to disable key expiry for the node in the admin console |
| 4 | Service user | root | user exists, home mode 700, in no sudo group; in systemd mode `loginctl show-user` reports `Linger=yes` | `useradd --create-home --shell /bin/bash`, `chmod 700`, `loginctl enable-linger` in systemd mode; then hands off to the service phase |
| 5 | Service repo | service | `~/agents/linear-agents` is a git checkout at the bundle's commit and `linear-agents` is on `PATH` | clones from (or fetches) the bundle, checks out its commit, runs `./setup` and `uv tool install --editable .` |
| 6 | Claude | service | `claude --version` is 2.1.293 or later, `claude auth status` reports a claude.ai login, every plugin in the shipped list installed | installs with the native installer; runs `claude` attached to the terminal with a note to `/login`, set the two push settings in `/config`, accept Remote Control in `~/agents/worktrees`, and exit; then clones each bundled marketplace to `~/agents/marketplaces/<name>`, adds it with `claude plugin marketplace add <dir>`, adds public ones by their GitHub source, and runs `claude plugin install <plugin>@<marketplace>` for each |
| 7 | Secrets | service | the three files exist, mode 600, one non-empty line each | masked `huh` inputs for the Linear key and both GitHub tokens, with the permissions each token needs shown beside it |
| 8 | Config | service | `linear-agents doctor` prints `ok    config:` | a form prefilled from the embedded example: team key, digest issue, ntfy topic (a random one generated, shown for me to subscribe to, then a priority 5 test push I confirm arrived), and one or more repos (name, GitHub URL, base branch, draft, check command); clones each repo to `~/src/<name>` with the agent token through a repo-local credential helper |
| 9 | Run | service | systemd mode: the three units match the embedded copies, the tmux service is active and the timer enabled; loop mode: session `la-loop` runs `linear-agents run` | systemd mode: installs the units, `daemon-reload`, enables the tmux service; loop mode: starts session `la-loop` idle, so the tmux server is up for doctor's tmux check. Both: runs `linear-agents doctor` and shows its output, then offers to start ticking (enable the timer, or start `la-loop`) only when no line is FAIL |

The config form writes only the keys it asks for and keeps everything else from the example, so
a key added to `config.example.toml` reaches new hosts without an installer change.

Tailscale SSH needs no policy change: the default tailnet policy lets me reach my own devices as
root or any non-root user in check mode, which asks for a browser re-auth every 12 hours. That
holds only while the node is logged in as me and untagged, which is why step 3 logs in
interactively and does not use an auth key or tags.

## Errors

A failing apply stops the run. The installer prints the step, the command, its exit status and
the last 20 lines of its output, and the full output goes to `/tmp/la-setup/setup.log` (root
phase) or `~/.cache/la-setup/setup.log` (service phase). There is no rollback: every apply is
written to be re-run, so the fix is to correct the cause and run `just ship` again.

Answers given in a form are saved to `~/.cache/la-setup/answers.toml` (mode 600, secrets
excluded) as soon as the form is submitted, so a re-run prefills them.

## Testing

- Unit tests for the parts with logic: os-release mapping, mode choice, version comparison,
  config rendering (the rendered TOML is a golden file that a Python test loads with
  `config.load`, so the two cannot drift), plugin-list parsing, and each check against fixture
  states.
- A Python test for `linear-agents run`: a failing tick is logged and the loop continues.
- End-to-end runs in local stoat VMs for Debian, Arch and Fedora (systemd mode), from a stage
  directory written by `la-setup ship --stage-only <dir>`. `--answers <file>` supplies every form
  value and `--skip-interactive` skips the Claude and Tailscale logins, so the run needs no TTY;
  the pass condition is that doctor's only FAIL lines are the Claude login and the live Linear
  and GitHub checks the test secrets cannot pass. A second run on the same host must apply
  nothing before the Run step, which stops on those FAIL lines by design.
- Loop mode is exercised by bringing up the Daytona smoke host itself, interactively; a
  separate throwaway container run would spend credits for the same coverage.
- `just check` gains `go vet` and `go test` for `installer/`; `mise.toml` pins Go 1.27.1, since
  Bubble Tea v2 needs Go 1.26 or later.

## Verified on 2026-10-10

A Daytona `daytona-small` container in the Engrammic organization, probed and deleted:

- VM snapshots (`daytona-vm-small`, `daytona-vm`, `daytona-vm-medium`) are listed but creating
  one fails with "not available in region eu" on both the Engrammic and Personal organizations.
- The SSH login user is `daytona` with passwordless sudo; the image is Debian 13; PID 1 is
  Daytona's own init, not systemd.
- An SSH token from `POST /api/sandbox/<id>/ssh-access` works for `scp` and for `ssh` with a
  command.
- `/dev/net/tun` exists; tailscaled starts on the kernel TUN device and `tailscale up` prints a
  login URL.
- Outbound HTTPS reaches tailscale.com, api.linear.app, api.github.com, ntfy.sh and the Claude
  installer. claude.ai answered `curl` with 403, likely bot protection; the Claude login in
  step 6 is the real test.

Claude Code accepts a local directory as a marketplace source and loads its plugins in place
(code.claude.com plugin docs), which is what step 6 relies on for private marketplaces.
