# Setup installer design

Status: draft for review, 2026-10-10. Replaces the manual steps in `docs/vm-setup.md` with one
program; the smoke test section of that file stays manual.

## Goal

One command from my desktop takes a fresh Linux host to a running linear-agents service, and
the same command run again finishes a setup that stopped halfway. The old VM is gone, so this
is also how the smoke test gets a machine again.

### Success criteria

- `just ship <host>` on a fresh Debian, Ubuntu, Arch, Fedora or RHEL VM, or a Daytona VM
  sandbox, ends with `linear-agents doctor` reporting no FAIL line, with me only logging in to
  Claude and Tailscale, pasting three secrets and answering the config form.
- A run that fails at any step can be re-run and resumes at that step without redoing the
  earlier ones or asking for their answers again.
- The installer never enables the timer while doctor reports a FAIL line.

### Out of scope

- Hosts without systemd as PID 1, including Daytona's default container sandboxes. The service
  relies on the tick unit's cgroup to kill processes a check leaves behind; a no-systemd mode
  would need a second supervisor in the Python service.
- Creating anything in Linear. The NF team already has the `claude`, `escalation`,
  `needs-decision` and repo labels and the digest issue; the installer only checks them, through
  doctor.
- Creating GitHub tokens. Fine-grained tokens can only be made in the browser; the installer
  shows the permissions each one needs.
- Uninstalling, and the smoke test steps.

## Shape

The installer is a Go program in `installer/` with its own `go.mod`. It is a plain `main` that
walks the steps in order: each step that needs input runs a standalone `huh` form, long commands
run under huh's spinner, and interactive logins (`claude`, `tailscale up`) run as ordinary child
processes attached to the terminal, since no Bubble Tea program is running between forms.

Libraries: `charm.land/huh/v2` (forms and spinner) and `charm.land/lipgloss/v2` (the step
list). Both are Charm's v2 line, stable since 2026. Rejected: a full Bubble Tea app with a
persistent checklist and `tea.ExecProcess`, about three times the code for a nicer frame, and
gum with a shell script, which has no state between steps.

The unit files and `config.example.toml` are embedded from `deploy/` with `go:embed` at build
time, so the installer cannot install units that differ from the repo it ships with.

### Shipping

`just ship <host>` runs on my desktop:

1. Builds a static `linux/amd64` binary (`CGO_ENABLED=0`), and `linux/arm64` when the host
   reports `aarch64`.
2. Writes `git bundle create linear-agents.bundle HEAD` of the current checkout. The repo is
   private, and cloning from the bundle means the host needs no GitHub credential for the
   service's own code. Upgrading the service is `just ship` again.
3. Writes the owner's plugin list (see step 6) next to them.
4. `scp`s the three files to `/tmp/la-setup/` on the host and runs
   `ssh -t <host> sudo /tmp/la-setup/la-setup`.

`<host>` is anything `ssh` accepts. The first run on a new host uses whatever SSH the provider
gives (a Daytona SSH token, a cloud console key); step 3 puts the host on the tailnet, and later
runs use its Tailscale name.

## Steps

Each step has a check, which reports whether its outcome already holds, and an apply, which
makes it hold. On start the installer runs every check and shows the list with each step's
state; a multi-select lets me pick finished steps to redo (to replace a secret, say). Then it
applies the steps that are not done, in order.

The first three steps run as root. Step 4 copies the binary, bundle and plugin list into the
service user's home and re-executes the installer as that user (`sudo -iu linear-agents
~/.cache/la-setup/la-setup --phase service`), so nothing after it has root.

| # | Step | As | Check | Apply |
|---|------|----|-------|-------|
| 1 | Preflight | root | `/proc/1/comm` is `systemd`; `/etc/os-release` `ID`/`ID_LIKE` maps to apt, pacman or dnf; arch is amd64 or arm64 | refuses with the reason |
| 2 | Packages | root | `git`, `tmux`, `curl`, `gh`, `node` on `PATH` | installs the missing ones; on Debian and Ubuntu adds GitHub's apt repo for `gh` first |
| 3 | Tailscale | root | `tailscale status --json` shows `BackendState` `Running` and SSH on | installs with `tailscale.com/install.sh`; asks for a hostname; runs `tailscale up --ssh --hostname=<name>` attached to the terminal, so I open the login URL it prints |
| 4 | Service user | root | user exists, home mode 700, `loginctl show-user` reports `Linger=yes`, not in any sudo group | `useradd --create-home --shell /bin/bash`, `chmod 700`, `loginctl enable-linger`; then hands off to the service phase |
| 5 | Service repo | service | `~/agents/linear-agents` is a git checkout at the bundle's commit and `linear-agents` is on `PATH` | clones from (or fetches) the bundle, checks out its commit, runs `./setup` and `uv tool install --editable .` |
| 6 | Claude | service | `claude --version` is 2.1.293 or later, logged in to the subscription, the owner's plugins installed | installs with the native installer; runs `claude` attached to the terminal with a note to `/login`, set the two push settings in `/config`, accept Remote Control in `~/agents/worktrees`, and exit; then `claude plugin marketplace add` and `claude plugin install` for each plugin in the shipped list |
| 7 | Secrets | service | the three files exist, mode 600, one non-empty line each | masked `huh` inputs for the Linear key and both GitHub tokens, with the permissions each token needs shown beside it |
| 8 | Config | service | `~/.config/linear-agents/config.toml` loads (`linear-agents doctor` config line passes) | a form prefilled from the embedded example: team key, digest issue, ntfy topic (a random one generated, shown for me to subscribe to, then a priority 5 test push I confirm arrived), and one or more repos (name, GitHub URL, base branch, draft, check command); clones each repo to `~/src/<name>` with the agent token through a repo-local credential helper |
| 9 | Units | service | the three units match the embedded copies, the tmux service is active | installs them, `daemon-reload`, enables the tmux service, runs `linear-agents doctor` and shows its output; offers to enable the timer only when no line is FAIL |

The plugin list in step 6 is the `enabledPlugins` of my desktop's user-scope Claude settings and
the marketplaces they come from, so agent sessions get the same plugins as my own.

The config form writes only the keys it asks for and keeps everything else from the example, so a
key added to `config.example.toml` reaches new hosts without an installer change.

## Errors

A failing apply stops the run. The installer prints the step, the command, its exit status and
the last 20 lines of its output, and the full output goes to `/tmp/la-setup/setup.log` (root
phase) or `~/.cache/la-setup/setup.log` (service phase). There is no rollback: every apply is
written to be re-run, so the fix is to correct the cause and run `just ship` again.

Answers given in a form are saved to `~/.cache/la-setup/answers.toml` (mode 600, secrets
excluded) as soon as the form is submitted, so a re-run prefills them.

## Testing

- Unit tests for the parts with logic: os-release mapping, version comparison, config rendering
  (the rendered TOML is loaded by the Python config loader in a test, so the two cannot drift),
  plugin-list parsing, and each check against fixture states.
- End-to-end runs in local stoat VMs for Debian, Arch and Fedora. `--answers <file>` supplies
  every form value and `--skip-interactive` skips the Claude and Tailscale logins, so the run
  needs no TTY; the pass condition is that doctor's only FAIL lines are the Claude login and the
  live Linear and GitHub checks the test secrets cannot pass. A second run on the same VM must
  apply nothing.
- `just check` gains `go vet` and `go test` for `installer/`; `mise.toml` pins Go 1.27.1, since
  Bubble Tea v2 needs Go 1.26 or later.

## Open questions

- Whether the Daytona SSH-token login user has root or sudo on a VM sandbox. If not, the first
  run there goes through Daytona's own exec API to install Tailscale, and `just ship` works from
  then on.
- Private plugin marketplaces (docket is in a private repo) need a GitHub credential on the
  host to install. The agent token cannot read `NovusEdge` repos, since fine-grained tokens are
  per owner. Either ship those marketplaces as bundles like the service repo, or skip them with
  a warning.
- Tailscale SSH needs a tailnet policy rule letting my identity reach `linear-agents@<host>`; the
  installer can print the rule but not apply it.
