# Set up the desktop and a host

A host needs four commands, run on the desktop in this order:

```bash
flocc keys                 # once per desktop
flocc host add my-host
flocc ship -H my-host
flocc login -H my-host
```

`flocc setup` runs these and more in one guided pass. Use the individual commands to repair one part. `flocc keys` first is the usual order, but `host add` and `ship` ask for any key the desktop does not have yet, with the same prompt and checks.

Run `flocc ship` from inside the flocc checkout. Ship sends that checkout's commit and builds the installer from it.

## flocc setup

`flocc setup` walks the whole setup in order. Each step checks first, so a rerun shows what is done and repairs the rest. It asks as it goes and needs a terminal; without one it exits 2.

```bash
flocc setup        # inside the flocc checkout
```

| Step | Checks | Does |
|---|---|---|
| Checkout | The current directory is the flocc checkout | Stops if not |
| Keys | The Linear key and the agent token | Asks for what is missing |
| Tailnet | The Tailscale OAuth client, the tailnet policy, each host's tag | See [Tailnet](../tailnet.md) |
| Hosts | Host files | With none, creates or adds a host. For each host it runs `flocc --version`, `status --json` and `doctor`. A host older than the checkout, or failing a doctor check other than login and disk, is shipped. Low disk and unreachable hosts are reported |
| Keys | The publish token and the ntfy topic | Asks for what is missing, then checks them |
| Old versions | `id`, `loginctl` and `ps`, without sudo | Shows each old user from an earlier install, default no. A yes removes it in the root pass after its own checks, with its home archived to `/root/flocc-removed-<user>-<date-time>.tar.gz` |
| Cardinal | One machine: the saved one, else the first host's | Status ok: nothing. Failing: setup. A `cardinal` user without a saved file: asks to adopt, default no. No cardinal: asks, default yes |
| Logins | Each host's and cardinal's login | `full` is done and `unknown` is shown. Anything else runs `flocc login` or `flocc cardinal login` with the pool account you give |

Host create needs passwordless sudo on the machine. A machine whose sudo asks for a password can still hold cardinal and run the cleanup: the removal and cardinal's root work share one `ssh -t`, so sudo asks once.

Which machine a host is on is `machine` in its host file. Setup asks once for a host that lacks it. `none` is allowed for a host that is on no machine of yours, and `daytona` is refused.

Exit codes: 0 when every step is done or skipped by you, 1 when any is incomplete or failed, 2 without a terminal.

## Add a host

```bash
flocc host add <name> [--ssh DEST | --daytona SANDBOX]
```

This writes `~/.config/flocc/hosts/<name>.toml`, the only place a host's answers live. It records:

- The repositories the agent token can read, offered ticked.
- The Linear team, picked for you when the key sees only one.
- The digest issue: another host's on the same team, else an open issue titled `flocc digest`, else a new one for you to pin.
- The labels `claude`, `escalation`, `needs-decision` and one per repository, created when missing.

It then checks the keys again against every host's repositories and says what a token lacks. The SSH destination defaults to `root@<name>`. `all` cannot be a host name.

`flocc ship -H <name> --interactive` runs the host's own forms and copies its answers back to the host file.

### Import a host that already runs flocc

A host shipped before 0.5.0, or from another desktop, has no answers in a host file, only an SSH destination. `flocc ship` refuses it with a pointer to the import. Import it first, so the desktop takes its answers, its keys and its ntfy topic instead of making new ones:

```bash
flocc host import -H my-host    # its answers, plus each key the desktop lacks
flocc keys                      # asks only for what the host did not have
```

{% hint style="warning" %}
Run the import before `flocc keys`. `flocc keys` first mints a new ntfy topic, and the next ship then moves the host off the topic your phone follows.
{% endhint %}

The import reads each key from the host's secret files and checks it as a paste is checked before storing it. It prints "Took the ..." with a GitHub token's expiry or the Linear key's account and teams, never a value.

- The import opens the SSH connection in the terminal first, so a Tailscale check-mode approval link shows. Without a terminal, a host that needs a prompt fails within 30 seconds with a message saying so.
- If the connection drops partway, it stops with "run flocc host import again" instead of asking for a new token, because GitHub shows a token only once.
- A host token that does not cover a repository is not taken, and the message names the token. On github.com/settings/personal-access-tokens open it, choose Edit, fix the repository access, then run the import again.
- An agent token from a host shipped before 0.5.0 also lacks Actions (read) and Commit statuses (read). Add both to its permissions there. The token's value stays the same.

## Ship

```bash
flocc ship -H <name> [--interactive]
```

Ship sets a host up and is safe to run again. It checks every step, skips the finished ones and resumes at the first that is not. It needs `go`, `gh` logged in on the desktop and, for Daytona, the Daytona CLI logged in with `daytona login`. The SSH user must be root or have passwordless sudo, and `sudo` must be installed: ship checks `sudo -n true` before it stages anything and stops otherwise.

Without `--interactive`, ship runs without a terminal. It sends the host file's answers and the stored keys as one answers document on stdin over one SSH session, and nothing prompts. The SSH session is forced to have no TTY (`ssh -T`), so a `RequestTTY` setting cannot echo the document back. The document must have `team_key`, `digest_issue` and at least one `[[repos]]`; a host file without them is refused with a pointer to `flocc host import -H <name>`.

The document looks like this:

```toml
team_key = "ENG"
digest_issue = "ENG-1"
ntfy_topic = "flocc-a-long-random-topic"
shared_team = false

[[repos]]
name = "backend"
url = "https://github.com/example/backend"
base_branch = "main"
check = "just check"
draft = true

[secrets]
linear_key = "lin_api_…"
agent_token = "github_pat_…"
publish_token = "github_pat_…"
```

With `--interactive`, ship runs over `ssh -t` and asks on the host's terminal instead: the Tailscale login, the three secrets, the config form and the ntfy test push. It sends no answers document.

Answers mode skips the Tailscale login: it only starts `tailscaled`. On a host that is not yet on your tailnet, run `tailscale up --ssh --hostname=<name>` there yourself, and see [Tailnet](../tailnet.md) for tagging. `flocc host create` joins the tailnet for an Incus VM.

### What ship writes on the host

No secret is written on the host except the service's own secret files (mode 600, in `~flocc/.config/flocc/`). None appears in a command line, a `setup.log` or the output. The installer also replaces the secret values of the answers document with `<token>` in its log. Secrets typed into `--interactive` forms are not in that set, but no step logs them.

The host keeps its answers, without secrets, in `~flocc/.config/flocc/answers.toml`. Everything either mode runs is logged to `/tmp/flocc-setup/setup.log` (root steps) and `~flocc/.cache/flocc-setup/setup.log` (the rest). The root phase must run from `/tmp/flocc-setup`, where `flocc ship` puts it; running `flocc-setup` by hand from anywhere else is refused.

### The summary

Ship never logs in to Claude. It ends with a summary: the version on the host, the Claude login (full, token-only or none), the GitHub token expiries, each failed doctor check, the timer and the live sessions.

It exits non-zero when a doctor check fails, except the Claude login while that login is pending. On a fresh host the login is pending, so the tmux unit runs, the timer stays off and ship exits 0. The summary's `claude login` row names the next command, `flocc login -H <name>`.

### Upgrade a host

Run `flocc ship -H <name>` again. It ships the current commit as a git bundle, since the host has no credential for the repository. A session already running keeps the plugin, hooks and settings it was launched with; the upgrade reaches it at its next launch or handoff.

Ship replaces a host's key file whenever the desktop's key differs, so it also brings a host's keys in line with the desktop.

## Claude login

```bash
flocc login -H <name> [--email you@example.com]
```

Sessions need a full claude.ai login. A `claude setup-token` token cannot open Remote Control sessions. `flocc login` runs on the host as `flocc`, over `ssh -t`:

1. It takes the tick lock for the whole login. If a tick is running it waits for it. If sessions are running it says so and asks whether to wait for them or stop, because running sessions rewrite `~/.claude.json` from memory and could drop the Remote Control consent. While it waits for sessions it lets the lock go, so ticks keep supervising them.
2. Without a claude.ai login it runs `claude auth login --claudeai`. The sign-in URL opens in your desktop browser and prints as a QR code. Sign in and paste the code at the prompt in the same terminal. If `claude auth status` still does not show a claude.ai login, it stops there.
3. It marks `~/worktrees` trusted, because sessions trust only their own worktree and `remote-control` would stop at the folder-trust dialog. It then runs `claude remote-control` once under a pty, answers its `Enable Remote Control?` prompt with `y`, waits for its connected line and stops it. If no connected line appears within 60 seconds it fails and shows the output. Run `claude remote-control` as `flocc` by hand to see what it waits for.
4. It checks `claude auth status`, runs doctor, starts the timer and prints the summary.

The host must have been shipped with 0.4.0 or later.

When the login lapses, the next tick sends one tier 1 push. It pages again only after a login has been seen in between. Shipping to a host whose login has lapsed leaves its already-enabled timer on, so ticks keep running and the one page stands until you run `flocc login -H <name>`.

## Daytona

A Daytona container works for a smoke test, at about $0.17 an hour while running:

```bash
daytona create --snapshot daytona-medium --name flocc-smoke --auto-stop 0
flocc host add flocc-smoke --daytona flocc-smoke
flocc ship -H flocc-smoke              # or --interactive
flocc login -H flocc-smoke
# when done
daytona stop flocc-smoke
```

`--auto-stop 0` matters: a running tick loop does not count as activity, so the default 15-minute auto-stop ends it mid-run. A stopped sandbox bills only its disk. If `daytona-medium` has less than 2 vCPU and 4 GiB, pass `--cpu 2 --memory 4096` instead of `--snapshot`.

## What flocc cannot do for you

- **Pin the digest issue in Linear.** `flocc host add` creates the digest issue and the labels `claude`, `escalation`, `needs-decision` and one per mapped repository when they are missing, but `host import` does not. A host file written by import or by hand needs them in the team already. A repository's label is named exactly like its `[repos.<name>]` key; a repository without one is mapped but gets no issues. Doctor fails on a missing `claude`, `escalation` or `needs-decision` label or digest issue and warns about missing repository labels.
- **Make the GitHub tokens.** They are made in the browser. `flocc keys` opens each page with the permissions filled in and prints the repositories to tick.
- **Disable key expiry for an untagged node.** In the Tailscale admin console, an untagged node's key otherwise expires after 180 days. Tagged nodes have expiry off.
- **Gitignore build output.** In each mapped repository, gitignore everything `./setup` and the check write, or every issue fails at prepare. If the mapped repositories need tools outside `~/.local/bin`, the mise shims and the system directories, add them to `Environment=PATH` in `deploy/flocc.service` and to `loopCommand` in `installer/internal/steps/run.go`.
- **Clean up scratchpads on your desktop.** Add the same backstop the installer writes on hosts: `echo 'e /tmp/claude-* - - - 2d' | sudo tee /etc/tmpfiles.d/claude-scratch.conf`. The service deletes a run's recorded scratchpads when its claim stops running; this line catches anything left over. Loop-mode hosts have no tmpfiles timer and rely on the service alone.
- **Choose which plugins agents get.** `deploy/agent-plugins.txt` decides it. A plugin listed there must be enabled on your desktop, or `flocc ship` stops and names it.
