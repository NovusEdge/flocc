# VM setup

One-time steps on the VM, in order. The service and its agents run as a dedicated user, so
sessions cannot read your own account's browser profiles, keys or logins. Create it from your own
account:

```bash
sudo useradd --create-home --shell /bin/bash linear-agents
sudo chmod 700 /home/linear-agents
sudo loginctl enable-linger linear-agents
```

It gets no password, no groups and no sudo. Run every step below as `linear-agents`
(`sudo -iu linear-agents`, or `ssh linear-agents@<vm>` over Tailscale SSH). Check that nothing
outside its home is writable to it, such as a Homebrew prefix your own `PATH` uses:
`find / -xdev -writable -not -path '/proc/*' -not -path '/home/linear-agents/*' 2>/dev/null`.

1. Install `git`, `gh`, `tmux`, `claude` (2.1.293 or later) and this repo; run `./setup`, then
   `uv tool install --editable .` so `linear-agents` is on `PATH` at `~/.local/bin`.
2. Sign Claude Code in with the subscription (`claude`, then `/login`), with `CLAUDE_CONFIG_DIR`
   unset as the sessions have it; set, it moves `.claude.json` and the login and folder trust go
   with it. Do not set `ANTHROPIC_API_KEY` anywhere.
3. In `/config`: turn on "Push when actions required", turn off "Push when Claude decides".
   Leave extra usage off.
4. Create `worktree_root`, then run `cd ~/agents/worktrees && claude` once and accept "Enable
   Remote Control" if asked. A worktree is its own git repo and does not inherit folder trust from
   this directory, so the launcher marks each worktree and its main checkout as trusted in
   `~/.claude.json` before starting a session.
5. Linear: a personal API key limited to the team, saved to `linear_key_file` (mode 600). Create
   the labels `claude`, `escalation` and `needs-decision`, and a pinned digest issue whose key goes
   in `digest_issue`. Create one label per mapped repo, named exactly as its `[repos.<name>]` key
   in `config.toml`: an issue is matched to a repo by that label (or by a project of that name),
   and an issue with neither is skipped.
6. GitHub: a read-only fine-grained token (contents, pull requests, checks, issues: read) in
   `agent_gh_token_file`, and a token with contents and pull requests write on the mapped repos in
   `publish_gh_token_file`. Both mode 600. A session does not start without the read-only one.
7. ntfy: install the app, subscribe to a long random topic, put its URL in `ntfy_url`. Send a
   priority 5 test (`curl -H 'Priority: 5' -d test <url>`) with Do Not Disturb on, to see whether it
   gets through.
8. Copy `deploy/config.example.toml` to `~/.config/linear-agents/config.toml` and edit it. In each
   mapped repo, gitignore everything `./setup` and the check write, or every issue fails at
   prepare. If the
   mapped repos need tools outside `~/.local/bin`, the mise shims and the system directories, add
   them to `Environment=PATH` in `deploy/linear-agents.service`.
9. Install and start the units, and let them run without a login session:

   ```bash
   cp deploy/linear-agents.{service,timer} deploy/linear-agents-tmux.service \
      ~/.config/systemd/user/
   systemctl --user daemon-reload
   systemctl --user enable --now linear-agents-tmux.service
   linear-agents doctor
   systemctl --user enable --now linear-agents.timer
   ```

   Enable the timer only once `linear-agents doctor` reports no FAIL line. It checks the config,
   the Linear key, labels and digest issue, each repo's fetch, both GitHub tokens' expiry and
   access (the agent token must not be able to push), the Claude login and version, and the tmux
   server, and changes nothing. Lingering was enabled when the user was created. Over `sudo -iu`, `systemctl --user` needs
   `XDG_RUNTIME_DIR=/run/user/$(id -u)`.

Watch it with `journalctl --user -u linear-agents -f`, and attach to a session with
`tmux -L linear-agents attach -t NF-123`.

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
   the timer, or with `systemctl --user start linear-agents.service`, systemd kills it when the
   tick ends.
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
10. Check that one failed tick does not stop the timer. Empty the agent token file
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
