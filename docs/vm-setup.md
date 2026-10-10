# Host setup

`just ship <target>` sets a host up and is safe to run again: it checks every step, skips the
finished ones and resumes at the first that is not. `<target>` is an SSH destination, such as
the host's Tailscale name, or `daytona:<sandbox>`. It needs `go` (from `mise install`), `gh`
logged in on the desktop, and, for Daytona, the Daytona CLI logged in with `daytona login`.

The first run on a host asks you to log in to Tailscale and to Claude (`/login`, Remote Control;
the two push settings are written to `~/.claude/settings.json` for you), to paste the Linear key
and the two GitHub tokens, to fill in the config form and to confirm an ntfy test push. It ends
by running `linear-agents doctor` and starts ticking only when doctor reports no FAIL line.
Everything it runs is logged to `/tmp/la-setup/setup.log` (root steps) and
`~linear-agents/.cache/la-setup/setup.log` (the rest).

The root phase must run from `/tmp/la-setup`, where `just ship` puts it; running `la-setup` by
hand from anywhere else is refused.

Upgrading the service on a host is `just ship <target>` again: it ships the current commit as
a git bundle, since the repo is private and the host has no credential for it.

## Hosts

Debian, Ubuntu, Arch, Fedora and RHEL VMs run in systemd mode: a timer ticks every 3 minutes
and a user unit keeps the tmux server up. Any host whose PID 1 is not systemd runs in loop mode:
`linear-agents run` in tmux session `la-loop` ticks instead (the installer's `la-loop` session
starts the tmux server), a process a timed-out check daemonized is not killed when its tick
ends, and after the host stops `just ship` restarts tailscaled and the loop.

A Daytona container for a smoke test, about $0.17 an hour while running:

```bash
daytona create --snapshot daytona-medium --name la-smoke --auto-stop 0
just ship daytona:la-smoke
# when done
daytona stop la-smoke
```

`--auto-stop 0` matters: a running tick loop does not count as activity, so the default
15-minute auto-stop ends it mid-run. A stopped sandbox bills only its disk. If
`daytona-medium` has less than 2 vCPU and 4 GiB, pass `--cpu 2 --memory 4096` instead of
`--snapshot`.

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
  system directories, add them to `Environment=PATH` in `deploy/linear-agents.service` and to
  `loopCommand` in `installer/internal/steps/run.go`.

Watch it from the desktop with `just status <target>` (sessions, last log lines, timer),
`just logs <target>` (the journal in systemd mode, `run.log` in loop mode) and
`just attach <target> NF-123` (read-only tmux).

## Upgrading

`digest_issue` is now required. A config without it fails to load, which fails every tick and the
agents' pre-commit hook, so commits in running sessions are refused. Add it before upgrading.

## Smoke test

Run this once by hand before trusting the timer with real issues. It uses your accounts, so no
automated run covers it.

### Plugin evals

Run the escalation evals in `plugin/evals/` (`escalate-irreversible`, `escalate-reversible`,
`escalate-shared-cause`) before the first real issue:

```bash
claude plugin eval plugin --trust-plugin --no-publish --ablation none -j 3 \
  --allow-tools "Bash(linear-agents escalate:*)"
```

If that invocation is rejected, `claude plugin eval --help` shows the current arguments. Pass means
every eval's graders succeed. The sandbox usually cannot run `linear-agents`, so the graders judge
the escalation the agent chose, and step 7 below exercises the real command. Fix the failing skill and rerun before going on.

### Steps

1. Create a scratch GitHub repo with a `justfile` whose `check` runs its tests, map it in the
   config, and create one issue in a test Linear team: labelled `claude` and the repo's label,
   assigned to you, in Todo, with an `## Acceptance criteria` list and an
   `` ```agent `` block containing `wall_clock: 30m`.
2. Run `linear-agents tick` by hand; it takes the same lock as the timer's ticks. Pass: the issue
   moves to In Progress, and `NF-…-test` appears in `tmux -L linear-agents ls` and in the Claude
   app. A tick run from a shell leaves behind any process a timed-out check daemonized; under
   the timer, or with `systemctl --user start linear-agents.service` (systemd mode only), systemd
   kills it when the tick ends.
3. Before anything else, check the tester session's tools. In the app, run `/mcp` in the first
   launched session and list its tools. Pass: no claude.ai connectors, Linear in particular,
   because `--strict-mcp-config` is unverified in interactive sessions. If any connector shows up,
   stop the rollout: `systemctl --user stop linear-agents.timer`, kill the session, and do not
   proceed, since it could write to Linear through the connector. Record the result in the spec's
   "Verified and unverified" section. Add a Linear MCP server to the `linear-agents` user's own
   Claude config only after this passes, and run this step again after each Claude Code upgrade.
4. In the app, confirm the tester session is steerable. Check `ListAgents` from a second session
   and note whether `--name` changed the listed name; record the answer in the spec.
5. Confirm lookup by human key resolves. Pass: the tick in step 2 found and claimed the issue by
   its `NF-…` key without an error in `journalctl --user -u linear-agents`, and
   `linear-agents release NF-…` (step 8) accepts the same key. The Linear query is
   `issue(id: "NF-1")`; a "not found" or schema error means the key lookup in `linear.py` is
   wrong, so fix it and add the real response as a fixture.
6. Keep running `linear-agents tick` until a PR exists and Linear has the PR comment. It is a
   draft unless the repo sets `draft = false`, which a private repo of a GitHub Free
   organization needs.
7. Exercise escalation: in a session, run

   ```bash
   linear-agents escalate --tier 1 --cause smoke --summary test --action ignore
   ```

   then tick. Pass: one ntfy push and the `escalation` label. Repeat the command and tick again.
   Pass: a second comment and no second push.
8. Confirm `issueAddLabel`, `User.isMe` and the `comments_since` filter worked. If any failed, fix
   `linear.py` and add the real response as a fixture.
9. Exercise a rerun: block a second issue (kill its session twice, one tick apart), then run
   `linear-agents release NF-…`. Pass: the issue is back in Todo, and the next tick restarts it on
   the kept branch.
10. Check that one failed tick does not stop the timer (systemd mode). Empty the agent token file
    (`: > <agent_gh_token_file>`, after copying it aside), then run:

    ```bash
    systemctl --user start linear-agents.service
    journalctl --user -u linear-agents -n 20
    systemctl --user list-timers linear-agents.timer
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
