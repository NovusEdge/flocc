# Host setup

`just ship <target>` sets a host up and is safe to run again: it checks every step, skips the
finished ones and resumes at the first that is not. `<target>` is an SSH destination, such as
the host's Tailscale name, or `daytona:<sandbox>`. It needs `go` (from `mise install`), `gh`
logged in on the desktop, and, for Daytona, the Daytona CLI logged in with `daytona login`.

The first run on a host asks you to log in to Tailscale and to Claude (`/login`, Remote Control;
the two push settings are written to `~/.claude/settings.json` for you), to paste the Linear key
and the two GitHub tokens, to fill in the config form and to confirm an ntfy test push. It ends
by running `flocc doctor` and starts ticking only when doctor reports no FAIL line. The service
phase saves the form's answers, without the tokens, in `~flocc/.config/flocc/answers.toml` on the
host, and a rerun starts from them.
Everything it runs is logged to `/tmp/flocc-setup/setup.log` (root steps) and
`~flocc/.cache/flocc-setup/setup.log` (the rest).

The root phase must run from `/tmp/flocc-setup`, where `just ship` puts it; running `flocc-setup` by
hand from anywhere else is refused.

Upgrading the service on a host is `just ship <target>` again: it ships the current commit as
a git bundle, since the repo is private and the host has no credential for it. A session already
running keeps the plugin, hooks and settings it was launched with; the upgrade reaches it at its
next launch or handoff.

## Hosts

Debian, Ubuntu, Arch, Fedora and RHEL VMs run in systemd mode: a timer ticks every 3 minutes
and a user unit keeps the tmux server up. Any host whose PID 1 is not systemd runs in loop mode:
`flocc run` in tmux session `flocc-loop` ticks instead (the installer's `flocc-loop` session
starts the tmux server), a process a timed-out check daemonized is not killed when its tick
ends, and after the host stops `just ship` restarts tailscaled and the loop.

A Daytona container for a smoke test, about $0.17 an hour while running:

```bash
daytona create --snapshot daytona-medium --name flocc-smoke --auto-stop 0
just ship daytona:flocc-smoke
# when done
daytona stop flocc-smoke
```

`--auto-stop 0` matters: a running tick loop does not count as activity, so the default
15-minute auto-stop ends it mid-run. A stopped sandbox bills only its disk. If
`daytona-medium` has less than 2 vCPU and 4 GiB, pass `--cpu 2 --memory 4096` instead of
`--snapshot`.

## A VM on an Incus machine

The owner's host is an Incus VM on the laptop `zer0-day-nimble`. Agents run unreviewed code, so it
is a VM rather than a container. The machine is shared: never restart Docker or Incus on it, since
a Docker restart on 2026-10-20 killed another session's build.

The machine carries the bridge and forwarding settings applied by hand on 2026-10-19. Check them
before building a VM:

```bash
incus network get incusbr0 ipv4.nat                  # true
sudo firewall-cmd --get-zone-of-interface=incusbr0    # trusted
sudo iptables -S FORWARD | head -1                    # -P FORWARD ACCEPT
grep ip-forward-no-drop /etc/docker/daemon.json       # Docker leaves FORWARD alone
```

If any differs, stop and ask the owner; do not restart Docker or Incus.

They are broad: the VM can reach every service on the machine. `flocc host create` (0.6.0)
replaces them with rules scoped to `incusbr0`.

Size a new VM like the existing one and check the machine has room for both:

```bash
incus config show la-nimble --expanded | grep -E 'limits\.(cpu|memory)|size:'
free -g
```

Then, with la-nimble's `limits.cpu`, `limits.memory` and root disk `size` in place of the example
values:

```bash
incus launch images:debian/13 flocc-nimble --vm -c boot.autostart=true \
  -c limits.cpu=4 -c limits.memory=8GiB -d root,size=40GiB
until incus exec flocc-nimble -- true 2>/dev/null; do sleep 2; done   # the Incus agent starts a few seconds after launch
incus exec flocc-nimble -- sh -c 'until ip -4 -o addr show scope global | grep -q inet; do sleep 2; done'
incus exec flocc-nimble -- sh -c 'apt-get update -q && apt-get install -yq curl && curl -fsSI https://pkgs.tailscale.com >/dev/null'
incus exec flocc-nimble -- sh -c 'curl -fsSL https://tailscale.com/install.sh | sh'
incus exec flocc-nimble -- tailscale up --ssh --hostname=flocc-nimble
```

`tailscale up` prints a login URL; open it and log in as the owner. In the Tailscale admin console,
disable key expiry for the node. `just ship root@flocc-nimble` then reaches it over Tailscale SSH.

## What the installer cannot do

- Linear: the labels `claude`, `escalation` and `needs-decision`, and a pinned digest issue,
  must exist in the team. A label per mapped repo (named exactly as its `[repos.<name>]` key)
  is optional: a repo without one is mapped but gets no issues. Doctor fails on the first
  three and warns about missing repo labels.
- GitHub tokens are made in the browser. The installer shows the permissions each needs next to
  its input.
- In the Tailscale admin console, disable key expiry for the host; an untagged node's key
  otherwise expires after 180 days.
- In each mapped repo, gitignore everything `./setup` and the check write, or every issue fails
  at prepare. If the mapped repos need tools outside `~/.local/bin`, the mise shims and the
  system directories, add them to `Environment=PATH` in `deploy/flocc.service` and to
  `loopCommand` in `installer/internal/steps/run.go`.
- On your desktop, add the same scratchpad backstop the installer writes on hosts:
  `echo 'e /tmp/claude-* - - - 2d' | sudo tee /etc/tmpfiles.d/claude-scratch.conf`. The service
  deletes a run's recorded scratchpads when its claim stops running; this line catches anything
  left over. Loop-mode hosts have no tmpfiles timer and rely on the service alone.
- `deploy/agent-plugins.txt` decides which of your plugins agent sessions get. A plugin listed
  there must be enabled on your desktop, or `just ship` stops and names it.

Watch it from the desktop with `just status <target>` (sessions, last log lines, timer),
`just logs <target>` (the journal in systemd mode, `run.log` in loop mode) and
`just attach <target> NF-123` (read-only tmux).

### Fleet view

`just fleet-status` shows every host in two tables: one row per host (mode, last tick and exit
status, running sessions against the cap, 5h and weekly usage, free disk) and one row per live
claim (phase, age, context use, whether its tmux session is up). `just fleet-status --watch
--interval 30s` redraws until Ctrl-C.

The hosts are every sandbox in the active Daytona organization, read with the Daytona CLI's login,
plus one file per ssh host in `~/.config/flocc/hosts/`, named `<name>.toml`:

```toml
ssh = "root@flocc-nimble"
```

A file sets `ssh` or, for a Daytona sandbox, `daytona = "<sandbox>"`; the fleet lists sandboxes
from the Daytona API either way. The first `just fleet-status` after 0.3.0 converts the old
one-destination-per-line hosts file into these files, naming each host by its destination without
`user@` and from the first dot on (`root@box.tail1.ts.net` becomes `box.toml`), renames the old file
to `hosts.converted` and prints what it did.

Each started host answers `flocc status --json`, which only reads state: it takes no tick
lock and calls neither Linear nor GitHub. (On a WAL ledger, opening it read-only still creates
the `-shm` and `-wal` files beside it; the ledger file itself is not changed.) Stopped sandboxes
are listed with their state. The `est $/h` column prices started sandboxes only, from vCPU,
memory and disk beyond 5 GiB at Daytona's list rates, so it is an estimate; stopped and archived
sandboxes show `-` because their rate is not confirmed. A host file whose `ssh` starts with `-`
is skipped with a note, since ssh would read it as an option. An
unreachable host gets a row and an error line, and the rest still render. Cells are colored for usage at
the configured `five_hour_max_pct` and `weekly_max_pct`, disk under 15% free, a last tick older
than 10 minutes, and a non-zero tick exit. Without a Daytona login the Daytona hosts are skipped
with a note.

### Running more than one host

Each host keeps its own ledger, so nothing stops two hosts from claiming the same issue. Split
the repos instead: every repo is mapped on exactly one host, and an issue is picked up by the host
that maps its label or project. That holds only if each issue carries exactly one repo label or
repo project: an issue with two repos' labels matches on both hosts and is worked twice. It also
assumes every host uses the same Linear key, because ready issues are read from the key's
assigned issues; hosts on different keys see different issues.

Set `skip_unmapped = true` on each host. Without it, a host comments "No configured repo matches"
on every ready issue that belongs to the other host's repos. The installer asks "Do other hosts
handle the unticked repos?" when you untick discovered repos in the config form, and writes the key
if you say yes (`shared_team = true` in an answers file does the same). Doctor prints a
`skip_unmapped` line when it is on. The cost is that a typo in a label no longer gets that comment,
so an unmapped issue just sits in Todo.

The hosts share the owner's Claude login and so its 5h and weekly usage limits; each applies
`five_hour_max_pct` and `weekly_max_pct` to the same account, so size the caps with that in mind.
`just fleet-status` shows all hosts together. Tailscale hostnames must differ between hosts.

## Disk use

A worktree with its venv can reach 1.4 GB, and uv's download cache (`~/.cache/uv`) grows with every
repo `./setup`, which can fill a small disk (Daytona's is 10 GB) and break SSH and the installer.
Three things limit it:

- Each worktree's `./setup` runs with `UV_CACHE_DIR` pointing at `<worktree_root>/.uv-cache-<KEY>`
  and `UV_LINK_MODE=hardlink`, and that directory is deleted when setup ends, whether it succeeds,
  fails or times out. Every setup therefore downloads its packages fresh, and the venv keeps its own
  files. The cache is on the worktree's filesystem so uv can still link instead of copy.
- `min_free_disk_gb` (default 2.0) holds new starts while the disk holding `worktree_root` has less
  free space than that. `flocc doctor` warns below twice the value and fails below it, and
  a doctor FAIL also stops a fresh install from starting the timer.
- Each tick removes the worktree (not the branch) of a finished run whose Linear issue is
  completed or canceled, or no longer exists. Running and blocked claims, and issues in any other
  state, keep theirs, so a rerun from Todo still reuses it. A timed-out run with uncommitted
  changes, new files included, keeps its worktree. A removed worktree is recreated from the kept
  branch if the issue is rerun.

On a host with room to spare, `shared_uv_cache = true` replaces the throwaway caches with one
`<worktree_root>/.uv-cache` kept across setups, so only the first setup of a dependency set
downloads it. Nothing prunes that cache; `min_free_disk_gb` still holds new starts when it fills
the disk.

The cache change covers uv inside `./setup` only. Sessions still use `~/.cache/uv`, and the npm,
pnpm, pip and mise caches are not managed.

## Subagents in agent sessions

By default a session may launch only the agents in `plugin/agents/`, and only as often as its
phase allows; grants (below) can add `general-purpose` or a named workflow. The budgets (which
agents, the phase's total launches, how many running at once) are in
`src/flocc/gate.py`; the review phase gets none. A hook refuses any other launch and tells
the agent why. Every launch, allowed or refused, is appended to `<state_dir>/launches.jsonl`, and
`flocc status` shows each running claim's count as `launches N/limit`.

To allow more, put an `allow:` line in the issue's `agent` block, or in a comment of yours on the
issue (a `steer_authors` comment works too). Items are comma-separated:

```
allow: launches 14, agent code-mapper, workflow <name>, fix-rounds 2
```

A comment is applied once the supervisor has delivered it to the live session. A `launches` grant
in the agent block applies to the implementer phase; from a comment it applies to the phase
running when the comment arrives. `fix-rounds` raises the implementer's fix rounds.

The items `agent general-purpose` and `workflow <name>` change the session's launch flags, so
from a comment they take effect at the next handoff; the other items apply within one tick. Until
the handoff the gate refuses them with the rule `next-session`. A granted workflow starts once per
run and counts as one launch. `launches N` is the phase's total launches, workflows included.
Comment grants do not survive a release: a new claim discards them and rewrites only the agent
block's `allow:` line. To keep a grant across a release, put it in the agent block.

The implementer and fix sessions are the only ones connected to Remote Control. When one of them
needs an answer it asks with `AskUserQuestion`, which is meant to arrive in the Claude app with a
push. That is not yet confirmed on a host. A question left unanswered for two hours is escalated
by the service as tier 2 regardless. While a question is open, a reply on the issue is held and
delivered after the question is answered, and the context handoff request waits too.

Implementer and fix sessions still use `flocc escalate`, with a default, for reversible
choices. Tester and reviewer sessions are not on Remote Control and escalate with `--waiting` for
decisions that block them.

## Upgrading

`digest_issue` is now required. A config without it fails to load, which fails every tick and the
agents' pre-commit hook, so commits in running sessions are refused. Add it before upgrading.

## Hosts from before 0.3.0

`just ship` refuses a host set up before 0.3.0: it has the service user of the old name, and a
second poller there would claim the same issues. Build a new host and drain the old one; the steps
used for la-nimble are tasks 12, 14 and 15 of
`docs/superpowers/plans/2026-10-20-flocc-part-1-rename.md`.

A branch the old host pushed continues on the new one: with no local `agent/<key>`, the new host
fetches it, finds the tester's commits on it and resumes at the implementer. An `--accept-tests`
grant made on the old host does not move; grant it again. A push the old host made after the new
one started is reported as "GitHub's copy of the branch has commits this one lacks".

The new host does not read the old host's saved answers; it saves its own in
`~flocc/.config/flocc/answers.toml`.

## Smoke test

Run this once by hand before trusting the timer with real issues. It uses your accounts, so no
automated run covers it.

### Plugin evals

Run the escalation evals in `plugin/evals/` (`escalate-irreversible`, `escalate-reversible`,
`escalate-shared-cause`) before the first real issue:

```bash
claude plugin eval plugin --trust-plugin --no-publish --ablation none -j 3 \
  --allow-tools "Bash(flocc escalate:*)"
```

If that invocation is rejected, `claude plugin eval --help` shows the current arguments. Pass means
every eval's graders succeed. The sandbox usually cannot run `flocc`, so the graders judge
the escalation the agent chose, and step 7 below exercises the real command. Fix the failing skill and rerun before going on.

### Steps

1. Create a scratch GitHub repo with a `justfile` whose `check` runs its tests, map it in the
   config, and create one issue in a test Linear team: labelled `claude` and the repo's label,
   assigned to you, in Todo, with an `## Acceptance criteria` list and an
   `` ```agent `` block containing `wall_clock: 30m`.
2. Run `flocc tick` by hand; it takes the same lock as the timer's ticks. Pass: the issue
   moves to In Progress, and `NF-…-test` appears in `tmux -L flocc ls` and in the Claude
   app. A tick run from a shell leaves behind any process a timed-out check daemonized; under
   the timer, or with `systemctl --user start flocc.service` (systemd mode only), systemd
   kills it when the tick ends.
3. Before anything else, check the tester session's tools. In the app, run `/mcp` in the first
   launched session and list its tools. Pass: no claude.ai connectors, Linear in particular,
   because `--strict-mcp-config` is unverified in interactive sessions. If any connector shows up,
   stop the rollout: `systemctl --user stop flocc.timer`, kill the session, and do not
   proceed, since it could write to Linear through the connector. Record the result in the spec's
   "Verified and unverified" section. Add a Linear MCP server to the `flocc` user's own
   Claude config only after this passes, and run this step again after each Claude Code upgrade.
4. In the app, confirm the tester session is steerable. Check `ListAgents` from a second session
   and note whether `--name` changed the listed name; record the answer in the spec.
5. Confirm lookup by human key resolves. Pass: the tick in step 2 found and claimed the issue by
   its `NF-…` key without an error in `journalctl --user -u flocc`, and
   `flocc release NF-…` (step 8) accepts the same key. The Linear query is
   `issue(id: "NF-1")`; a "not found" or schema error means the key lookup in `linear.py` is
   wrong, so fix it and add the real response as a fixture.
6. Keep running `flocc tick` until a PR exists and Linear has the PR comment. It is a
   draft unless the repo sets `draft = false`, which a private repo of a GitHub Free
   organization needs. Then keep ticking until `NF-…-review` has run (model from `review_model`,
   default `opus`; set it in the config to change it). Pass: the PR has a `🤖 flocc review`
   comment from the publish token's account, and the issue has a `Review: …` comment.
7. Exercise escalation: in a session, run

   ```bash
   flocc escalate --tier 1 --cause smoke --summary test --action ignore
   ```

   then tick. Pass: one ntfy push and the `escalation` label. Repeat the command and tick again.
   Pass: a second comment and no second push.
8. Confirm `issueAddLabel`, `User.isMe` and the `comments_since` filter worked. If any failed, fix
   `linear.py` and add the real response as a fixture.
9. Exercise a rerun: block a second issue (kill its session twice, one tick apart), then run
   `flocc release NF-…`. Pass: the issue is back in Todo, and the next tick restarts it on
   the kept branch. For a run that has finished (review done), timed out or blocked, dragging
   the issue back to Todo in Linear does the same: the next tick releases the old claim, comments
   once, and restarts. A running claim, review phase included, ignores the move; use `release`.
10. Check that one failed tick does not stop the timer (systemd mode). Empty the agent token file
    (`: > <agent_gh_token_file>`, after copying it aside), then run:

    ```bash
    systemctl --user start flocc.service
    journalctl --user -u flocc -n 20
    systemctl --user list-timers flocc.timer
    ```

    Pass: the journal shows the tick failing on the empty token, and `list-timers` still shows a
    NEXT time for the timer. Wait for that time and check the journal for a new tick. Restore the
    token file afterwards.
11. Recapture the status fixture. Copy a live session's `<state_root>/NF-…/status.json` over
    `tests/fixtures/status.json`, then run:

    ```bash
    uv run pytest tests/test_usage.py -q
    ```

    Pass: the tests pass on the real capture. If they fail, the real field paths differ from
    `rate_limits.five_hour/seven_day.used_percentage`, `resets_at` or
    `context_window.used_percentage`: fix `_CONTEXT_PATH` or `latest_usage` in `usage.py`, rerun,
    and commit the fixture with the fix.

When every step passes, remove the scratch issue and repo mapping, then label a real issue.
