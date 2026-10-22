# cardinal

cardinal is an always-on Claude session that watches your flocc fleet and tells you what needs you. It is optional. flocc runs without it.

It runs as an unprivileged `cardinal` user on the machine that holds your Incus hosts, outside the VMs. Every 20 minutes it checks the fleet with `flocc -H all status` and Linear, and it talks to you through the Claude app over Remote Control. Its brief is `deploy/cardinal/CLAUDE.md`.

It reports a failed or stale tick, a session down for a running claim, usage over 80% of a window, a token expiring within 14 days, low disk, and the Linear issues that wait on you (`needs-decision`, `escalation`, and `claude` issues in Blocked), sorted by priority.

## Set it up

Run these from the flocc checkout:

```bash
flocc cardinal setup my-machine                  # or flocc setup, which asks to adopt a hand setup
flocc cardinal login --email <pool account>      # a pool account, never your own
flocc cardinal attach --write                    # /mcp, linear, Authenticate, paste the localhost URL, Ctrl-b d
flocc cardinal status
flocc status -H all                              # the CARDINAL table
```

`<machine>` is an SSH destination you can reach with your own account, usually the one that holds your Incus hosts. Use a Claude pool account for cardinal, so its usage does not draw on your own.

The `attach --write` step authorizes cardinal's Linear connector. In the session, run `/mcp`, choose linear, choose Authenticate, and paste the localhost URL it prints back into the attach. Detach with Ctrl-b d.

`cardinal login` and `cardinal attach --write` print this before attaching:

```
Detach with Ctrl-b d. Ctrl-D twice ends cardinal's session; the service starts a fresh one.
```

{% hint style="warning" %}
Ctrl-D twice in a writable attach exits Claude, which moves the conversation into a Claude background daemon outside `cardinal.service`. The service starts a fresh session about 30 seconds later. The default attach is read-only and cannot send Ctrl-D, and neither can the dashboard's `c`.
{% endhint %}

## Commands

```
flocc cardinal setup [--user NAME] [--only HOST]... [--key PATH] [--adopt] [<machine>]
flocc cardinal login [--user NAME] [--email ADDR]
flocc cardinal status [--user NAME]
flocc cardinal attach [--user NAME] [--write]
flocc cardinal restart [--user NAME]
flocc cardinal remove [--user NAME] [--purge]
```

| Command | Does |
|---|---|
| `setup` | Installs cardinal on the machine, and repairs a half-set-up one |
| `login` | Signs in the cardinal user to the pool account |
| `status` | Checks cardinal and prints what failed |
| `attach` | Attaches to its tmux session, read-only unless you pass `--write` |
| `restart` | Restarts the service with a fresh session and loop |
| `remove` | Stops cardinal and takes its key off the hosts. `--purge` also deletes the user, its home and its login |

| Flag | Meaning |
|---|---|
| `--user` | The user on the machine. The default is `cardinal` |
| `--only` | Limit setup to this host file. Repeat for more |
| `--key` | The desktop public key that may log in as the user. Without it flocc uses the first of `~/.ssh/id_ed25519.pub`, `id_ecdsa.pub`, `id_rsa.pub` |
| `--adopt` | Take over an existing user that no saved file names |

`--user cardinal-test --only <vm>` gives a test install beside the live one.

cardinal takes no `-H`. It names its machine, not a host. Its log is `~/.cache/flocc/cardinal-<user>.log`.

## How setup works

`setup` is also the repair command. It asks for the machine's sudo password only when a root outcome is missing: the user, lingering, the desktop key or the tailnet block. It never restarts a running cardinal, so a changed brief or unit applies at the next daily renewal (a two-hour window from 04:00) or at `flocc cardinal restart`.

cardinal runs as a systemd user unit, `cardinal.service`, that starts Claude in tmux with an initial prompt that arms a `/loop 20m` check. A renewal timer replaces the session once a day, because a `/loop` expires. The replacement loses replies sent to the old session, so cardinal keeps a state file listing every ask still waiting on you and raises them again in its first check.

## What cardinal can reach

The root pass blocks the cardinal user from the tailnet with a per-user nftables table (`flocc-cardinal-egress@<user>.service`). Tailscale authenticates the machine, not the user, and every node on your tailnet is yours. cardinal reaches its hosts as root over the Incus bridge, with a key limited to the bridge gateway, and reaches Claude and Linear over the internet.

A host whose sshd setup did not install keeps sshd running with its `cardinal.conf` after `remove`.

If cardinal's status shows a Tailscale SSH check, approve it in your browser, then run `status` again.

Not yet confirmed on a live run: that the `/loop` initial prompt arms the loop, that pinning `ipv4.address` on a running VM keeps its lease, and that the egress block holds for connections to the machine's own tailnet address.
