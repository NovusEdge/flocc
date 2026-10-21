# cardinal Part 2: `flocc cardinal`, the setup command

Part 2 in `2026-10-21-cardinal-design.md` is cardinal's always-on session. That table row still
says "on its own VM"; the owner changed this on 2026-10-21 to an unprivileged `cardinal` user on
the laptop host (ledger d101). This spec covers only how that session is set up, kept alive and
taken down. The owner queue, the retirement of ntfy and the digest, and the judge-reversal metric
are later Part 2 work. The command does not change what cardinal does: that stays in its brief.

## What runs today

cardinal was set up by hand on zer0-day-nimble on 2026-10-21. The live setup, as read that night:

| Piece | Where | State |
|---|---|---|
| User `cardinal` (uid 1002) | zer0-day-nimble | no extra groups, home 700, linger on, the owner's desktop key in `~/.ssh/authorized_keys` |
| Claude Code 2.1.306 | `~cardinal/.local/bin/claude` | official installer; claude.ai login on a pool Max account |
| flocc CLI 0.8.0 | `~cardinal/.local/bin/flocc` | the desktop binary, built on the desktop and copied over |
| Host file | `~cardinal/.config/flocc/hosts/flocc-nimble.toml` | the desktop's, with `ssh = 'root@10.131.47.206'` |
| Brief | `~cardinal/CLAUDE.md` | now `deploy/cardinal/CLAUDE.md`, the same bytes |
| SSH key | `~cardinal/.ssh/id_ed25519` | made on the machine; the public half is in flocc-nimble's `/root/.ssh/authorized_keys` |
| sshd | flocc-nimble | `openssh-server` with `/etc/ssh/sshd_config.d/cardinal.conf`: `ListenAddress 10.131.47.206`, no password or keyboard-interactive login, root by key only |
| Linear | cardinal's user-scope MCP config | `linear`, http, `https://mcp.linear.app/mcp`, authorised by the owner through `/mcp` |
| Session | transient user unit `cardinal-tmux` | `systemd-run --user -p Type=forking tmux new-session -d -s cardinal -x 200 -y 50 -c ~ "claude --name cardinal --remote-control cardinal --permission-mode auto"` |
| Loop | inside the session | `/loop 20m Run your check from CLAUDE.md`, typed in with `tmux send-keys` |

What is wrong with it: a transient unit is gone after a reboot, the loop expires after 7 days, and
nothing reproduces the setup on another machine or for a second user. Two more facts from the hand
setup shape the design:

- The first start stopped at Claude Code's onboarding screens and then at the folder-trust dialog
  for `~`. The first trust answer was lost, because a running session rewrites `~/.claude.json`
  from memory (d81).
- The owner's desktop now fails host-key verification for `root@flocc-nimble` ("REMOTE HOST
  IDENTIFICATION HAS CHANGED"). flocc-nimble's `/etc/ssh/ssh_host_ed25519_key.pub` dates from
  16:25 that day, when `openssh-server` was installed. The likely cause is that Tailscale SSH serves
  the system host keys once they exist. Setup has to deal with this (Host pass, step 4).

## Commands

`cardinal` is a desktop verb, like `ship` and `host`, so the host's `flocc` refuses it.

```
flocc cardinal setup [--user NAME] [--only HOST]... [--key PATH] [<machine>]
flocc cardinal login [--user NAME] [--email ADDR]
flocc cardinal status [--user NAME]
flocc cardinal attach [--user NAME] [--write]
flocc cardinal restart [--user NAME]
flocc cardinal remove [--user NAME] [--purge]
```

- `<machine>` is the SSH destination of the owner's own account on the machine, as for `flocc
  host create`. `setup` saves it, with everything below, in `~/.config/flocc/cardinal/<user>.toml`.
  Later commands, and later runs of `setup`, read it from there.
- `--user` defaults to `cardinal`. Any other name gives a second, independent install on the same
  machine: a test install beside the live one. The tmux session, the Remote Control name and the
  key comment all use the user name. The unit names stay `cardinal*`, inside that user's manager.
- `--only` limits setup to the named host files. By default it uses every desktop host file whose
  name is an Incus instance on the machine (setup step 1). The flag is not `--host`, because
  `splitHost` takes `-H` and `--host` out of every desktop command line.
- `--key` is the desktop public key that may log in as the user. It defaults to the first of
  `~/.ssh/id_ed25519.pub`, `id_ecdsa.pub` and `id_rsa.pub` that exists.
- `attach` is read-only (`tmux attach -r`) unless `--write` is given. Typing is needed only for
  the Linear authorisation.
- `restart` restarts the session, and with it the loop.

The saved file holds `machine`, `user`, `hosts`, cardinal's public key once it is known, and
`renew_at` (default `04:00`). It holds no secret.

## Who does what, where

| Pass | Runs on | As | How |
|---|---|---|---|
| Incus | machine | the owner | `incus` over the owner's SSH (an `incus-admin` member), as `host create` does |
| Root | machine | root | `ssh -t <machine> sudo /tmp/flocc-cardinal/flocc-setup --phase cardinal-root`; one password prompt |
| User | machine | cardinal | handed off by the root pass with `sudo -iu`, or `ssh -T <user>@<machine>` when the root pass is skipped |
| Host | each host in scope | root | the desktop's own route to the host (its host file's `ssh`), as `flocc host run` uses |

The passes reuse `flocc-setup` and its `Step` framework (`Check`, then `Apply`, then `Check`
again, under `RunAll` with the scripted UI). There are three new phases: `cardinal-root`,
`cardinal` and `cardinal-login`. Each pass reads its answers document on stdin and ends with a
summary line that the desktop takes out of the stream, as ship does. The document holds no
secrets: the user, the machine name, the rendered host files, `known_hosts` lines and the desktop
public key. No new dependency.

## `setup`, in order

1. **Incus pass.** `incus list -f json` and `incus network get incusbr0 ipv4.address`. For each
   host in scope, setup takes the VM's IPv4 address on `incusbr0` and pins it: if the instance's
   `eth0` has no `ipv4.address`, setup runs `incus config device override <vm> eth0
   ipv4.address=<addr>`, using `device set` when `eth0` is a local device. The pin keeps the
   address it already has, so sshd's `ListenAddress` survives reboots and lease churn. A desktop
   host that is not an Incus instance on the machine is listed as skipped: cardinal cannot reach
   it yet. Daytona hosts are skipped the same way.
2. **Build and stage.** The desktop builds `flocc` (`./installer/cmd/flocc`) and `flocc-setup` for
   the machine's architecture, from the checkout it runs in, as ship does. It renders one host file
   per host in scope: the desktop's own file with `ssh = "root@<addr>"`.
3. **Root pass, only if needed.** First the desktop checks the root outcomes without sudo: `id -nG
   <user>`, `loginctl show-user <user> -p Linger`, and `ssh -o BatchMode=yes <user>@<machine>
   true`. If any of them fails, it stages to `/tmp/flocc-cardinal` and runs the root pass in the
   terminal. The pass ends by handing off to the user pass. Without a terminal it refuses: "the
   machine's sudo asks for a password; run this in a terminal". A rerun on a finished machine never
   asks for the password.
4. **User pass.** Its last step needs the hosts, which on a first run do not know cardinal's key
   yet, so it ends `pending` and prints the public key in its summary. The desktop saves the key.
5. **Host pass**, for each host in scope, using the saved key.
6. **User pass again**, now with the `known_hosts` lines. On a rerun, when the key was already
   saved, the host pass runs before the first user pass and this second user pass is skipped.
7. The summary has one row per pass and per host, the login state, and the next command: `flocc
   cardinal login` while the login is pending.

### Root pass steps (`cardinal-root`)

1. **Tools.** `tmux`, `curl`, `ssh-keygen` and `script` must be on the machine. If one is missing,
   the pass stops and names the distro's install command. It never installs packages: the machine
   is shared, as for `host create`.
2. **User.** `useradd --create-home --shell /bin/bash <user>`, then home mode 700 and linger on. If
   the user is in `sudo`, `wheel`, `admin`, `incus`, `incus-admin`, `lxd`, `docker` or `libvirt`,
   the pass stops with the `gpasswd -d` command to run. It never removes a group itself.
3. **Desktop key.** The `--key` line goes into `~<user>/.ssh/authorized_keys` (directory 700, file
   600, owned by the user). Every other line is kept.
4. **Hand-off.** `flocc-setup` is copied to `~<user>/.cache/flocc-cardinal/` and the user pass runs
   through `sudo -iu <user>`, as root hands off to the service phase in ship.

If `ssh <user>@<machine>` still fails after this pass, setup stops and says so. The machine's sshd
may have `AllowUsers` set, for example. The desktop does not edit the machine's sshd.

### User pass steps (`cardinal`)

1. **Claude Code**, at least `minClaude`, from the official installer. No plugins and no
   marketplaces.
2. **flocc CLI.** `~/.local/bin/flocc` must be byte-equal to the staged binary.
3. **Host files.** `~/.config/flocc/hosts/` (mode 700) holds exactly the rendered set. A host file
   that is no longer in scope is removed.
4. **Brief.** `~/CLAUDE.md` must equal the embedded `deploy/cardinal/CLAUDE.md`, rendered for the
   machine and user. The repo is the brief's only source, so a local edit is overwritten.
5. **SSH key.** `~/.ssh/id_ed25519`, ed25519 with no passphrase and comment `<user>@<machine>`, is
   made once and never replaced. Its public half goes in the summary. The private half never
   leaves the machine.
6. **known_hosts.** For each host address the file holds exactly the line the desktop sent. Every
   other line is kept.
7. **Claude state.** `~/.claude.json` gets `hasCompletedOnboarding` and `hasTrustDialogAccepted`
   for `~`. `~/.claude/settings.json` gets the push keys: `inputNeededNotifEnabled` and
   `agentPushNotifEnabled` both true, as live. A running session rewrites `~/.claude.json` from
   memory (d81), so this step writes only while `cardinal.service` is inactive. While the service
   is active, the step counts as done.
8. **Linear MCP.** `claude mcp add --scope user --transport http linear
   https://mcp.linear.app/mcp`, unless `claude mcp get linear` already finds it. The authorisation
   is the owner's (Login).
9. **Hosts reachable.** For each host: `ssh -o BatchMode=yes root@<addr> true`, then `flocc -H
   <host> status`. Pending until the host pass has run.
10. **Units.** The three files under Keeping it alive are written to `~/.config/systemd/user/`,
    followed by `daemon-reload`. `cardinal.service` and `cardinal-renew.timer` are enabled. The step
    starts `cardinal.service` when the login is full and the service is not running. It never
    restarts a running cardinal. A changed brief or unit file takes effect at the next renewal or
    at `flocc cardinal restart`, and the summary says so.

### Host pass steps (desktop to the host, as root)

1. **sshd.** `apt-get install -y openssh-server` when it is missing. Hosts come from `host
   create`, which makes Debian VMs; any other distro stops with a message.
2. **Drop-in.** `/etc/ssh/sshd_config.d/cardinal.conf` holds the live file's content with the
   host's address. After `sshd -t`, the step restarts `ssh` only when the file changed, and
   enables `ssh`.
3. **Key line.** `/root/.ssh/authorized_keys` gets `from="<incusbr0 gateway>",no-agent-forwarding,
   no-port-forwarding,no-X11-forwarding <cardinal public key>`. The key's comment,
   `<user>@<machine>`, is the marker: the line replaces any earlier line with that comment, and
   every other line is left alone. `from=` limits the key to connections from the machine over
   the bridge.
4. **Host key.** The step reads `/etc/ssh/ssh_host_ed25519_key.pub` and produces cardinal's
   `known_hosts` line, `<addr> ssh-ed25519 …`, so cardinal never trusts on first use. When step 1
   installed sshd in this run, its new host keys are what Tailscale SSH now presents too. The step
   therefore also replaces the desktop's own `known_hosts` entry for the host's SSH name
   (`ssh-keygen -R`, then appends the line just read). It reads the key over the shared connection
   the pass opened before the install, which was authenticated with the old key. It prints one
   line saying it did this.

## Keeping it alive

`deploy/cardinal/cardinal.service`:

```ini
[Unit]
Description=cardinal, the owner's flocc assistant
StartLimitIntervalSec=1h
StartLimitBurst=5

[Service]
Type=forking
Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin
# The initial prompt arms the loop, so no keys are ever typed into the pane.
ExecStart=/usr/bin/tmux new-session -d -s %u -x 200 -y 50 -c %h "claude --name %u --remote-control %u --permission-mode auto '/loop 20m Run your check from CLAUDE.md.'"
ExecStop=/usr/bin/tmux kill-session -t %u
Restart=always
RestartSec=30

[Install]
WantedBy=default.target
```

`deploy/cardinal/cardinal-renew.service` is a oneshot running `systemctl --user try-restart
cardinal.service`. `deploy/cardinal/cardinal-renew.timer` runs it at `OnCalendar=*-*-*
<renew_at>`.

- **Reboot.** Linger starts the user manager at boot, and the enabled unit starts with it.
- **Claude exits or crashes.** The pane closes, tmux's `exit-empty` ends the server, and that is
  the unit's main process, so `Restart=always` brings it back after 30 s. After five starts in an
  hour the unit stays failed, which keeps a broken login from spinning, and `status` shows why.
- **The 7-day expiry.** Every start is a fresh session whose initial prompt arms a fresh `/loop`.
  The daily renewal restarts the session, so no loop lives longer than a day. `try-restart` leaves
  a cardinal the owner stopped on purpose stopped. Each renewal drops the conversation; the brief
  already keeps what cardinal needs in `~/state.md`.
- tmux 3.4 and later put each pane in its own `tmux-spawn-*.scope`, as seen live. Limits set on
  the unit therefore do not reach claude, and stopping the unit ends claude through tmux's hangup,
  not through the cgroup.
- A user unit cannot order itself after the system's `network-online.target`. A start before the
  network is up relies on Claude Code retrying, and failing that on `Restart=always`.
- The default tmux socket keeps `ssh <user>@<machine> tmux attach -t <user>` working, as now.

Two brief edits land with the implementation. Each check rewrites `~/state.md`, even when nothing
changed, so that its modification time is the heartbeat `status` reads. And the machine and user
names become placeholders that the Brief step renders. Until then the committed copy is
byte-for-byte the live file.

## Login, and what a person still does

`flocc cardinal login` runs `~/.cache/flocc-cardinal/flocc-setup --phase cardinal-login` over
`ssh -t <user>@<machine>`:

1. If `cardinal.service` is active, it asks to stop it, because a running session would rewrite
   `~/.claude.json` over the new login and consent.
2. Without a claude.ai login it runs `claude auth login --claudeai [--email]`. The sign-in URL
   opens in the desktop browser and prints as a QR code (ship's `urlWatch`).
3. It checks the login is full, writes onboarding and the trust for `~`, and answers the Remote
   Control consent with `answerConsent`, run in `~`.
4. It starts `cardinal.service`, waits for the tmux session, and prints the summary and the steps
   below.

Only a person can do these:

- Enter the machine's sudo password, once, on the first setup and on `remove --purge`.
- Sign in with a **pool** account, not the owner's own (d90).
- Authorise Linear, until a way outside the TUI is confirmed: run `flocc cardinal attach --write`,
  type `/mcp`, pick `linear` and choose Authenticate, then approve as the owner for team NF. The
  browser's redirect to `localhost` fails, because that address is on the machine. Paste the whole
  address from the browser into the pane, then detach with Ctrl-b d. Typing `exit` stops cardinal.
- Look after the pool account's claude.ai connectors. They appear in cardinal's session: live it
  has Notion and Claude Docs besides Linear. Manage them in that account's claude.ai settings.

## Rerunning on a half-set-up machine

Every step checks before it changes anything, so `setup` is the repair command. Run it again after
any failure, a new host, a new flocc release or an edited brief. A rerun leaves a finished step
alone and does not prompt for sudo when the root outcomes hold. The parts that cannot simply be
done again are guarded: the SSH key is made once, the conversation is never restarted, group
membership is never removed, and the address pin keeps the current address. If something exists
in a form setup did not make, setup stops and names it, and it does not overwrite. Examples are a
`cardinal.conf` whose `ListenAddress` is not the VM's address, or a user whose home is not 700.

## Status and failures

`flocc cardinal status` runs over `ssh -T <user>@<machine>` and shows:

- the unit's state, how long it has been up, and its restart count (`NRestarts`);
- whether the tmux session and claude are up;
- the Claude login (full, token-only or none);
- Linear (connected or needs authentication, from `claude mcp list`);
- the last check, from `~/state.md`'s modification time;
- the renewal timer's next run;
- each host's reachability;
- cardinal's flocc and Claude versions against the desktop's.

It exits non-zero when the unit is not active, the login is not full, the last check is older than
45 minutes or a host is unreachable. Each failing row prints its remedy: `flocc cardinal login`,
`flocc cardinal restart` or `flocc cardinal setup`. A failed unit also shows the last 20 lines of
`journalctl --user -u cardinal` and the pane's last 15 lines.

Setup failures stop the pass at the failing step, with the step's error and its log:
`/tmp/flocc-cardinal/setup.log` for the root pass, `~<user>/.cache/flocc-cardinal/setup.log` for
the user pass, and `~/.cache/flocc/cardinal-<user>.log` on the desktop for the Incus and host
passes. A failed host does not stop the other hosts. The summary marks it, and setup exits
non-zero.

Nothing tells the owner when cardinal itself is down, beyond its session disappearing from the
Claude app (Open questions).

## Removing it

`flocc cardinal remove [--purge]`, each step idempotent:

1. **User pass.** `systemctl --user disable --now cardinal.service cardinal-renew.timer`, then
   delete the three unit files and `daemon-reload`. The home, the login, the key and `~/state.md`
   stay.
2. **Host pass**, for each saved host. Delete the `<user>@<machine>` key line and `cardinal.conf`,
   then run `systemctl disable --now ssh.service ssh.socket`. `openssh-server` and its host keys
   stay: removing the keys would change the host's Tailscale SSH host key a second time. The
   address pin stays; it is harmless.
3. **`--purge`** adds a root pass: `loginctl disable-linger`, then `pkill -u <user>`, then
   `userdel -r <user>`. The desktop then deletes `cardinal/<user>.toml`. The pool login and the
   Linear authorisation go with the home; the accounts themselves are untouched.

## Testing

Unit tests, with `host.Fake` and a temporary `Env.Root`, as the existing step tests do:

- The three unit files and the brief are embedded and installed byte-for-byte. The `ExecStart`
  line carries `--remote-control <user>`, `--permission-mode auto` and the `/loop` prompt.
- Host-file rendering: `ssh` is rewritten, no `[secrets]` table appears, and stale files are
  removed.
- The `authorized_keys` line: rendered with `from=`, replaces only its own marker, and leaves
  other lines and their order alone. Rerunning changes nothing.
- The sshd drop-in's content, and a restart only on change.
- Merging `known_hosts`, and replacing the desktop's entry after an install.
- Choosing the address and gateway from a recorded `incus list -f json` fixture, and the pin
  command for profile and local devices.
- Skipping the root pass when its outcomes hold, and refusing without a terminal when it is needed.
- The User step refusing each privileged group with the `gpasswd` line.
- The Claude-state step writing nothing while the unit is active.
- The status exit code for each failing row. `cardinal` is refused on the host's `flocc`.

Live run, on zer0-day-nimble with `--user cardinal-test` and a throwaway VM from `flocc host create
zer0-day-nimble flocc-cardtest`, passing `--only flocc-cardtest` so that the live cardinal and
flocc-nimble are not touched. Record the results in `docs/vm-setup.md`.

1. First `setup`. It asks for sudo once, and its summary shows the key installed, the host
   reachable and the login pending. The desktop's `flocc -H flocc-cardtest status` still works
   (the host-key refresh).
2. `setup` again. No sudo prompt, every step done, and no file's mtime changed.
3. Break pieces and rerun: delete the host file, remove the key line on the VM, stop the unit, add
   the user to `docker`. Setup repairs the first three and stops on the group with the `gpasswd`
   line.
4. `login` with a pool account. The session `cardinal-test` appears in the Claude app and runs a
   first check. This proves that the initial-prompt `/loop` arms (Unconfirmed). Ask it to list its
   scheduled tasks.
5. The Linear authorisation through `attach --write`. A check then reads Linear.
6. `pkill -u cardinal-test claude`. The unit is back within a minute and `NRestarts` went up.
7. `sudo systemctl restart user@<uid>`, then a full reboot when the laptop is free. cardinal comes
   back with a fresh loop.
8. `systemctl --user start cardinal-renew.service`. A fresh session and loop. The timer lists its
   next run. After `systemctl --user stop cardinal`, a renewal leaves it stopped.
9. `incus restart flocc-cardtest`. Same address, and sshd listening on it.
10. From the VM itself, and from another VM, `ssh` with cardinal-test's key is refused (`from=`).
    Password login is refused.
11. `status` shows every row. `attach` without `--write` ignores typing.
12. `remove`: units gone, key line and drop-in gone, ssh stopped. `remove --purge`: the user is
    gone. Then destroy flocc-cardtest.

## Rejected

- **A systemd timer that types the check prompt into the pane every 20 minutes.** There is no
  expiry, but it types into whatever has focus, and a permission dialog takes Enter as yes.
- **Re-arming `/loop` with `send-keys` before day 7 in a live session.** The same typing risk, and
  two loops run if the old one has not expired yet.
- **cardinal reaching VMs through `incus exec`.** `incus-admin` is root on the machine (d101).
- **cardinal over Tailscale SSH from the machine.** Tailscale authenticates the machine's node,
  which is the owner's identity, so every local user would get the owner's rights, and check mode
  prompts.
- **Installing packages on the machine.** It is shared; setup stops with the commands, as `host
  create` does.
- **Ansible, cloud-init or a shell script.** The `Step` framework already does checked,
  resumable setup over SSH, with a summary.

## Unconfirmed

- That a slash command passed as the positional prompt (`claude … '/loop 20m …'`) arms the loop.
  Host sessions already take a positional prompt. Fallback: an `ExecStartPost` that waits for the
  prompt box and sends the `/loop` line once. The session is fresh then, and onboarding and trust
  are already set, so no dialog can take the keys.
- That `/loop` tasks expire after 7 days. Daily renewal makes this moot.
- That Tailscale SSH serves `/etc/ssh` host keys once they exist. This is the likely cause of the
  desktop's host-key failure on flocc-nimble. The Host pass's refresh covers both cases.
- That setting `ipv4.address` on a running VM's `eth0` keeps the lease without a NIC restart.
- Whether the Linear MCP can be authorised outside the TUI.
- That connections from the machine reach the VM from the `incusbr0` gateway address, which `from=`
  relies on.

## Open questions

The design above uses the first option in each.

1. **Renewal.** A fresh session daily at 04:00, which bounds context growth and keeps its memory
   in `~/state.md`. The alternative is a weekly restart with `claude --continue`, which keeps the
   owner's conversation thread at the cost of a growing, compacted context.
2. **cardinal's Linear identity.** The owner's own OAuth, as live, with no extra seat. The
   alternative is a separate Linear member, so that cardinal's comments are told apart from the
   owner's.
3. **Who notices cardinal is down.** Nobody beyond the Claude app and `flocc cardinal status`; the
   owner finds ntfy unhelpful. The alternative is a cardinal row in the desktop's `flocc status -H
   all`.
4. **Hosts in scope.** Only Incus VMs on cardinal's machine; flocc-nimble is the only host today.
   The alternative is remote SSH hosts too, with a cardinal key on their sshd over the tailnet.
