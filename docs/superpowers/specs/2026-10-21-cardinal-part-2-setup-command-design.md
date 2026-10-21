# cardinal Part 2: `flocc cardinal`, the setup command

Part 2 in `2026-10-21-cardinal-design.md` is cardinal's always-on session. That table row still
says "on its own VM"; the owner changed this on 2026-10-21 to an unprivileged `cardinal` user on
the laptop host (ledger d101). This spec covers only how that session is set up, kept alive and
taken down. The owner queue, the retirement of ntfy and the digest, and the judge-reversal metric
are later Part 2 work. The command does not change what cardinal does: that stays in its brief.

## What runs today

cardinal was set up by hand on zer0-day-nimble on 2026-10-21. Setup has to take this over (Moving
the hand setup over). The live setup, as read that night:

| Piece | Where | State |
|---|---|---|
| User `cardinal` (uid 1002) | zer0-day-nimble | no extra groups, home 700, linger on, the owner's desktop key in `~/.ssh/authorized_keys` |
| Claude Code 2.1.306 | `~cardinal/.local/bin/claude` | official installer; claude.ai login on a pool Max account |
| flocc CLI 0.8.0 | `~cardinal/.local/bin/flocc` | the desktop binary, built on the desktop and copied over |
| Host file | `~cardinal/.config/flocc/hosts/flocc-nimble.toml` | the desktop's, with `ssh = 'root@10.131.47.206'` |
| Brief | `~cardinal/CLAUDE.md` | now `deploy/cardinal/CLAUDE.md`, the same bytes |
| SSH key | `~cardinal/.ssh/id_ed25519` | made on the machine; the public half is in flocc-nimble's `/root/.ssh/authorized_keys`, with no `from=` |
| sshd | flocc-nimble | `openssh-server`, installed by hand, with `/etc/ssh/sshd_config.d/cardinal.conf`: `ListenAddress 10.131.47.206`, no password or keyboard-interactive login, root by key only |
| Linear | cardinal's user-scope MCP config | `linear`, http, `https://mcp.linear.app/mcp`, authorised by the owner through `/mcp` |
| Session | transient user unit `cardinal-tmux` | `systemd-run --user -p Type=forking tmux new-session -d -s cardinal -x 200 -y 50 -c ~ "claude --name cardinal --remote-control cardinal --permission-mode auto"`, on tmux's default socket |
| Loop | inside the session | `/loop 20m Run your check from CLAUDE.md`, typed in with `tmux send-keys` |

What is wrong with it: a transient unit is gone after a reboot, the loop expires after 7 days, and
nothing reproduces the setup on another machine or for a second user. Two more facts from the hand
setup shape the design:

- The first start stopped at Claude Code's onboarding screens and then at the folder-trust dialog
  for `~`. The first trust answer was lost (User pass, step 7, says why).
- The owner's desktop now fails host-key verification for `root@flocc-nimble` ("REMOTE HOST
  IDENTIFICATION HAS CHANGED"). flocc-nimble's `/etc/ssh/ssh_host_ed25519_key.pub` dates from
  16:25 that day, when `openssh-server` was installed. The likely cause is that Tailscale SSH serves
  the system host keys once they exist (c21). Setup has to deal with this (Host pass, step 1).

## Commands

`cardinal` is a desktop verb, like `ship` and `host`.

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
  machine: a test install beside the live one. The tmux socket and session, the Remote Control name
  and the key comment all use the user name. The unit names stay `cardinal*`, inside that user's
  manager.
- `--only` limits setup to the named host files. By default it uses every desktop host file whose
  name is an Incus instance on the machine (setup step 1). The flag is not `--host`, because
  `splitHost` takes `-H` and `--host` out of every desktop command line. `cardinal` refuses a
  command line from which `splitHost` took a host, instead of ignoring it.
- `--key` is the desktop public key that may log in as the user. It defaults to the first of
  `~/.ssh/id_ed25519.pub`, `id_ecdsa.pub` and `id_rsa.pub` that exists.
- `attach` is read-only (`tmux attach -r`) unless `--write` is given. Typing is needed only for
  the Linear authorisation.
- `restart` restarts the unit, and with it the session and the loop.

The saved file holds `machine`, `user`, `hosts`, cardinal's public key once it is known, and
`renew_hour` (default `4`). It holds no secret.

cardinal's own `~/.local/bin/flocc` is the desktop binary, because `flocc -H <host> status` is
routed on the desktop side. It can therefore run `ship`, `keys`, `host create` and `cardinal`, but
only with what cardinal holds: no SSH into the owner's account, no desktop secrets and no
checkout. cardinal already has root SSH to its hosts, so refusing those verbs in its copy would
not be a boundary. The brief and the owner's yes are.

## Who does what, where

| Pass | Runs on | As | How |
|---|---|---|---|
| Incus | machine | the owner | `host create`'s incus runner over the owner's SSH, including its `sudo -n incus` fallback when the owner is not in `incus-admin` |
| Root | machine | root | one `ssh -t <machine>` that runs `sudo <stage>/flocc-setup --phase cardinal-root --user <user>`: the run's only sudo, so one password prompt |
| User | machine | the user | `ssh -T <user>@<machine>`, with its answers document on stdin |
| Host | each host in scope | root | the desktop's own route to the host (its host file's `ssh`), as `flocc host run` uses |

**Root never writes inside the user's home.** A process running as the user can plant a symlink
there and have root write through it, which is why `handoff` in `installer/cmd/flocc-setup/main.go`
keeps the same rule. Root changes only the mode of the home directory itself. Every write inside
the home happens as the user: in the root pass through `runuser -u <user> --`, everywhere else over
the user's own SSH.

**The root pass's stage.** The owner's SSH makes it with `mktemp -d /tmp/flocc-cardinal.XXXXXX`, so
its name cannot be claimed first, and it is the owner's, mode 700. `scp` puts `flocc-setup` and
`desktop.pub` (the `--key` line) in it. Before it acts, the root pass checks the stage with
`lstat`: a directory, not a symlink, owned by `SUDO_UID`, mode 700, holding those two regular
files owned by `SUDO_UID`. Anything else, and it refuses. The pass takes its few inputs as the flag
and the staged key, never on stdin, so the terminal stays free for sudo. The same `ssh -t` command
removes the stage after a success and keeps it, with the pass's `setup.log`, after a failure. ship's
fixed `/tmp/flocc-setup` and `checkStage` are not used.

**The user pass's stage.** The desktop copies `flocc` and `flocc-setup` as the user into
`~/.cache/flocc-cardinal/stage/` and runs the staged `flocc-setup --phase cardinal --answers -`.

The passes reuse `flocc-setup` and its `Step` framework (`Check`, then `Apply`, then `Check`
again, under `RunAll` with the scripted UI). The new phases are `cardinal-root`, `cardinal` and
`cardinal-login`, and two that the units run: `cardinal-prestart` and `cardinal-renew`. Each pass
ends with a summary line that the desktop takes out of the stream, as ship does. The user pass's
document holds no secrets: the user, the machine name, the rendered host files and the
`known_hosts` lines. No new dependency.

## `setup`, in order

1. **Incus pass.** `incus list -f json` and `incus network get incusbr0 ipv4.address`. For each
   host in scope, setup takes the VM's IPv4 address on `incusbr0` and pins it: if the instance's
   `eth0` has no `ipv4.address`, setup runs `incus config device override <vm> eth0
   ipv4.address=<addr>`, using `device set` when `eth0` is a local device. The pin keeps the
   address it already has, so sshd's `ListenAddress` survives reboots and lease churn. A desktop
   host that is not an Incus instance on the machine is listed as skipped: cardinal cannot reach
   it yet. Daytona hosts are skipped the same way.
2. **Build.** The desktop builds `flocc` (`./installer/cmd/flocc`) and `flocc-setup` for the
   machine's architecture, from the checkout it runs in, as ship does. It renders one host file
   per host in scope: the desktop's own file with `ssh = "root@<addr>"`.
3. **Root pass, only if needed.** First the desktop checks the root outcomes without sudo: `id -nG
   <user>`, `loginctl show-user <user> -p Linger`, and `ssh -o BatchMode=yes <user>@<machine>
   true`. If any of them fails, it runs the root pass in the terminal. Without a terminal it
   refuses: "the machine's sudo asks for a password; run this in a terminal". A rerun on a finished
   machine never asks for the password.
4. **Host pass**, for each host in scope, with the saved key when there is one. It returns each
   host's `known_hosts` line.
5. **User pass**, with those lines. It always reports cardinal's public key.
6. **Key check.** If the reported key differs from the saved one (the first run, or a key made
   anew after the home was restored or the key deleted), the desktop saves it, prints one line
   saying so, and runs the host pass and then the user pass again. The host pass then replaces the
   old key's lines.
7. The summary has one row per pass and per host, the login state, and the next command: `flocc
   cardinal login` while the login is pending.

### Root pass steps (`cardinal-root`)

1. **Tools.** `tmux`, `curl`, `ssh-keygen` and `script` must be on the machine. If one is missing,
   the pass stops and names the distro's install command. It never installs packages: the machine
   is shared, as for `host create`.
2. **User.** `useradd --create-home --shell /bin/bash <user>`, then home mode 700 and linger on.
   The pass stops with the `gpasswd -d` command to run if the user is in `sudo`, `wheel`, `admin`,
   `adm`, `systemd-journal`, `disk`, `incus`, `incus-admin`, `lxd`, `docker` or `libvirt`, and
   names the sudoers file to fix if `sudo -l -U <user>` lists any rule. It never removes a group or
   a rule itself.
3. **Desktop key.** As the user, through `runuser`, with the key line passed as an argument and
   never put into a shell string: `~/.ssh` 700, `authorized_keys` 600, the line added once. Every
   other line is kept.

If `ssh <user>@<machine>` still fails after this pass, setup stops and says so. The machine's sshd
may have `AllowUsers` set, for example. The desktop does not edit the machine's sshd.

### User pass steps (`cardinal`)

1. **Claude Code**, at least `minClaude`, from the official installer. No plugins and no
   marketplaces.
2. **Binaries.** `~/.local/bin/flocc` and `~/.local/lib/flocc-cardinal/flocc-setup` must be
   byte-equal to the staged ones. The units run the second.
3. **Host files.** `~/.config/flocc/hosts/` (mode 700) holds exactly the rendered set. A host file
   that is no longer in scope is removed.
4. **Brief.** `~/CLAUDE.md` must equal the embedded `deploy/cardinal/CLAUDE.md`, rendered for the
   machine and user. The repo is the brief's only source, so a local edit is overwritten.
5. **SSH key.** `~/.ssh/id_ed25519`, ed25519 with no passphrase and comment `<user>@<machine>`, is
   made when missing and never replaced. Its public half goes in the summary. The private half
   never leaves the machine.
6. **known_hosts.** For each host address the file holds exactly the line the desktop sent. Every
   other line is kept.
7. **Claude state.** Onboarding (`hasCompletedOnboarding`), the folder trust for `~` and the
   user-scope `linear` MCP server all live in `~/.claude.json`. A running session rewrites that
   file from memory and drops keys written behind its back (d81), so a `claude mcp add` during a
   session passes its check and is then lost. So the unit writes them: `cardinal-prestart`, its
   `ExecStartPre`, adds whatever is missing before every start, while no session runs, as the
   launcher does for host sessions. The Linear entry is `claude mcp add --scope user --transport
   http linear https://mcp.linear.app/mcp`, unless `claude mcp get linear` finds it. While the unit
   is inactive this step runs the prestart once. While it is active the step only reads, and
   reports a missing key as "applies at the next start". `~/.claude/settings.json` gets the push
   keys `inputNeededNotifEnabled` and `agentPushNotifEnabled`, both true, as live. A session does
   not rewrite that file.
8. **Hosts reachable.** For each host: `ssh -o BatchMode=yes root@<addr> true`, then `flocc -H
   <host> status`. Pending until the host pass has the current key.
9. **Units.** The three files under Keeping it alive are written to `~/.config/systemd/user/`,
   followed by `daemon-reload`. `cardinal.service` and `cardinal-renew.timer` are enabled. The step
   starts `cardinal.service` when the login is full and the service is not running. It never
   restarts a running cardinal. A changed brief or unit file takes effect at the next renewal or
   at `flocc cardinal restart`, and the summary says so. The one exception is the hand setup's
   session (Moving the hand setup over).

### Host pass steps (desktop to the host, as root)

1. **sshd and host key.** One remote command: `apt-get install -y openssh-server` when it is
   missing, then `cat /etc/ssh/ssh_host_ed25519_key.pub`. Hosts come from `host create`, which
   makes Debian VMs; any other distro stops with a message. The key comes back over the connection
   opened before the install, so a slow install that outlasts `ControlPersist` cannot strand the
   pass behind a changed host key. The pass turns it into cardinal's `known_hosts` line, `<addr>
   ssh-ed25519 …`, so cardinal never trusts on first use. When the install ran in this command, its
   new host keys are what Tailscale SSH now presents too. The desktop then replaces its own
   `known_hosts` entry for the host's SSH name (`ssh-keygen -R`, then appends the line just read)
   before it opens another connection, prints one line saying so, and writes
   `/var/lib/flocc-cardinal/ssh-installed` on the host.
2. **Listen address.** `/etc/ssh/sshd_config.d/cardinal.conf` holds the live file's content with
   the host's address. `/etc/sysctl.d/60-flocc-cardinal.conf` sets `net.ipv4.ip_nonlocal_bind=1`,
   applied with `sysctl -p` when it changes. Debian's `ssh.service` is ordered only after
   `network.target`, so at boot sshd can start before DHCP brings the address. It then exits 255,
   and the unit's `RestartPreventExitStatus=255` never retries it. The sysctl lets sshd bind the
   address before the lease arrives. After `sshd -t`, the step restarts `ssh` only when the
   drop-in changed, and enables `ssh`.
3. **Key line.** `/root/.ssh/authorized_keys` gets `from="<incusbr0 gateway>",no-agent-forwarding,
   no-port-forwarding,no-X11-forwarding <cardinal public key>`. Lines are matched by the key
   itself (type and base64), never by the comment. Any line carrying cardinal's key, or the saved
   key it replaced, becomes the rendered line, and every other line is left alone, in its order.
   `from=` limits the key to connections from the machine over the bridge.
   `/var/lib/flocc-cardinal/installs/<user>@<machine>` records that this install uses the host.

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
# A private socket: a tmux session anyone else starts can neither join this server at start nor
# keep it alive after claude exits, which would hide the exit from Restart=always.
ExecStartPre=-/usr/bin/tmux -L %u kill-server
ExecStartPre=%h/.local/lib/flocc-cardinal/flocc-setup --phase cardinal-prestart
# With more than one argument tmux runs claude without a shell, so systemd's quoting is the only
# quoting. The initial prompt arms the loop, so no keys are ever typed into the pane.
ExecStart=/usr/bin/tmux -L %u new-session -d -s %u -x 200 -y 50 -c %h %h/.local/bin/claude --name %u --remote-control %u --permission-mode auto "/loop 20m Run your check from CLAUDE.md."
ExecStop=/usr/bin/tmux -L %u kill-server
Restart=always
RestartSec=30

[Install]
WantedBy=default.target
```

`deploy/cardinal/cardinal-renew.service`:

```ini
[Unit]
Description=Renew cardinal's session before its /loop expires

[Service]
Type=oneshot
ExecStart=%h/.local/lib/flocc-cardinal/flocc-setup --phase cardinal-renew
```

`deploy/cardinal/cardinal-renew.timer`, with the hours rendered from `renew_hour`:

```ini
[Unit]
Description=cardinal's daily renewal window

[Timer]
OnCalendar=*-*-* 04,05:00/15
Persistent=true

[Install]
WantedBy=timers.target
```

- **Reboot.** Linger starts the user manager at boot, and the enabled unit starts with it.
- **Claude exits or crashes.** The pane closes, tmux's `exit-empty` ends the server, and that is
  the unit's main process, so `Restart=always` brings it back after 30 s. After five starts in an
  hour the unit stays failed, which keeps a broken login from spinning, and `status` shows why.
- **The 7-day expiry.** Claude Code documents that recurring `/loop` tasks live only in their
  session and expire 7 days after they are made. Every start is a fresh session whose initial
  prompt arms a fresh loop. Loop tasks fire only while the session is idle, so a dialog left open
  or a long conversation with the owner pauses the checks until it ends.
- **Renewal.** Every 15 minutes in a two-hour window from `renew_hour`, `cardinal-renew` restarts
  `cardinal.service` when all of these hold: the unit is active, so a cardinal the owner stopped
  stays stopped; the session is at least 20 hours old; and the newest transcript under
  `~/.claude/projects/` has not changed for 5 minutes, so no check is cut halfway. Otherwise it
  exits 0 and the next tick tries again. `Persistent=true` runs a window the laptop slept or was
  off through at the next chance. `status` fails when the session is more than 6 days old, before
  the loop can expire.
- **What the owner sees.** Each renewal ends the `cardinal` Remote Control session and starts a
  new one under the same name. The old one shows as ended in the Claude app, and a reply sent to it
  reaches nothing. The conversation does not carry over. cardinal keeps each ask it is waiting on
  in `~/state.md` with its exact command, and the first check of a new session raises them again,
  so the owner answers there.
- On this machine's tmux build, which has systemd support, each pane runs in its own
  `tmux-spawn-*.scope`, as seen live. Limits set on the unit therefore do not reach claude, and
  stopping the unit ends claude through tmux's hangup, not through the cgroup.
- A user unit cannot order itself after the system's `network-online.target`. A start before the
  network is up relies on Claude Code retrying, and failing that on `Restart=always`.
- `flocc cardinal attach` runs `ssh -t <user>@<machine> tmux -L <user> attach -t <user>`.

Two brief edits land with the implementation. cardinal records each pending ask in `~/state.md`
with its exact command, drops it when answered, and raises the open ones in the first check of a
session. And the machine and user names become placeholders that the Brief step renders. Until
then the committed copy is byte-for-byte the live file.

## Login, and what a person still does

`flocc cardinal login` runs `~/.local/lib/flocc-cardinal/flocc-setup --phase cardinal-login` over
`ssh -t <user>@<machine>`:

1. If `cardinal.service` is active, it asks to stop it, because a running session would rewrite
   the new login and consent out of `~/.claude.json` (User pass, step 7).
2. Without a claude.ai login it runs `claude auth login --claudeai [--email]`. The sign-in URL
   opens in the desktop browser and prints as a QR code (ship's `urlWatch`).
3. It checks the login is full, runs the prestart, and answers the Remote Control consent with
   `answerConsent`, run in `~`.
4. It starts `cardinal.service`, waits for the tmux session, and prints the summary and the steps
   below.

Only a person can do these:

- Enter the machine's sudo password, once, on the first setup, on `remove --purge`, and on any
  rerun that finds a root outcome missing.
- Sign in with a **pool** account, not the owner's own (d90).
- Authorise Linear, until a way outside the TUI is confirmed: run `flocc cardinal attach --write`,
  type `/mcp`, pick `linear` and choose Authenticate, then approve as the owner for team NF. The
  browser's redirect to `localhost` fails, because that address is on the machine. Paste the whole
  address from the browser into the pane, then detach with Ctrl-b d. That address carries the
  OAuth code, so `attach --write` clears the pane's history when you detach. Typing `exit` ends
  the conversation, and the unit starts a fresh one 30 s later.
- Look after the pool account's claude.ai connectors. They appear in cardinal's session: live it
  has Notion and Claude Docs besides Linear. Manage them in that account's claude.ai settings.

## Rerunning on a half-set-up machine

Every step checks before it changes anything, so `setup` is the repair command. Run it again after
any failure, a new host, a new flocc release or an edited brief. A rerun leaves a finished step
alone and does not prompt for sudo when the root outcomes hold. The parts that cannot simply be
done again are guarded: the SSH key is made only when missing, the conversation is never
restarted, group membership is never removed, and the address pin keeps the current address. If
something exists in a form setup did not make, setup stops and names it, and it does not
overwrite. Examples are a `cardinal.conf` whose `ListenAddress` is not the VM's address, or a user
whose home is not 700.

### Moving the hand setup over

The first `flocc cardinal setup zer0-day-nimble` runs over the live install:

- The desktop's `known_hosts` entry for flocc-nimble is already stale (c21), and the host pass
  cannot fix it over a connection that fails verification. The pass stops and names the fix:
  `ssh-keygen -R` for the host, and the new fingerprint read with `incus exec flocc-nimble --
  ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` to compare on the next connect.
- The user pass keeps cardinal's key. The host pass finds flocc-nimble's hand-made line by its key
  and replaces it with the restricted one, whatever its comment.
- The Units step stops the transient `cardinal-tmux` unit, kills a `cardinal` session left on
  tmux's default socket, and starts `cardinal.service`. This ends the hand session's conversation
  once, and the summary says so.
- flocc-nimble's sshd was installed by hand, so it has no `ssh-installed` marker. `remove` leaves
  its sshd running and says so.

## Status and failures

`flocc cardinal status` runs over `ssh -T <user>@<machine>` and shows:

- the unit's state, the session's age, and its restart count (`NRestarts`);
- whether the tmux session on `-L <user>` and claude are up;
- the Claude login (full, token-only or none);
- Linear (connected or needs authentication, from `claude mcp list`);
- the last turn: the modification time of the newest transcript under `~/.claude/projects/`,
  which Claude Code writes on every turn, so the heartbeat needs nothing from the brief;
- the renewal timer's next run;
- each host's reachability;
- cardinal's flocc and Claude versions against the desktop's.

It exits non-zero when the unit is not active, the login is not full, the last turn is older than
45 minutes, the session is more than 6 days old or a host is unreachable. Each failing row prints
its remedy: `flocc cardinal login`, `flocc cardinal restart` or `flocc cardinal setup`. A failed
unit also shows the last 20 lines of `journalctl --user -u cardinal`, or `systemctl --user status
cardinal` when the journal refuses the user, and the pane's last 15 lines, leaving out any line
that holds `code=`.

Setup failures stop the pass at the failing step, with the step's error and its log: `setup.log`
in the kept stage for the root pass, `~<user>/.cache/flocc-cardinal/setup.log` for the user pass,
and `~/.cache/flocc/cardinal-<user>.log` on the desktop for the Incus and host passes. A failed host
does not stop the other hosts. The summary marks it, and setup exits non-zero.

Nothing tells the owner when cardinal itself is down, beyond its session disappearing from the
Claude app (Open questions).

## Removing it

`flocc cardinal remove [--purge]`, each step idempotent:

1. **User pass.** `systemctl --user disable --now cardinal.service cardinal-renew.timer`, then
   delete the three unit files and `daemon-reload`. The home, the login, the key and `~/state.md`
   stay.
2. **Host pass**, for each saved host. Delete the lines carrying this install's key and its
   `installs/<user>@<machine>` record. Only when no install record is left on the host, also delete
   `cardinal.conf` and the sysctl file, and, if `ssh-installed` is there, run `systemctl disable
   --now ssh.service ssh.socket`. Another install on the same host keeps its access.
   `openssh-server` and its host keys stay: removing the keys would change the host's Tailscale SSH
   host key a second time. The address pin stays; it is harmless.
3. **`--purge`** adds a root pass, under the same stage rules: `loginctl disable-linger`, then
   `pkill -u <user>`, then `userdel -r <user>`. The desktop then deletes `cardinal/<user>.toml`.
   The pool login and the Linear authorisation go with the home; the accounts themselves are
   untouched.

## Testing

Unit tests, with `host.Fake` and a temporary `Env.Root`, as the existing step tests do:

- The three rendered units pass `systemd-analyze --user verify`. Where `systemd-analyze` is
  missing, the test skips with an open issue in its reason.
- `ExecStart`, split by systemd's quoting rules, gives tmux the argv `-L <user> new-session … <home>/.local/bin/claude --name <user> --remote-control <user> --permission-mode auto` and the
  `/loop` prompt as one argument. The timer's `OnCalendar` follows `renew_hour`.
- The renewal: no restart when the unit is inactive, the session is under 20 hours old or a
  transcript changed in the last 5 minutes; a restart otherwise.
- Host-file rendering: `ssh` is rewritten, no `[secrets]` table appears, and stale files are
  removed.
- The `authorized_keys` line: rendered with `from=`, matched by key whatever its comment, so a
  hand-made line without `from=` and the replaced key's line both become the rendered line. Other
  lines and their order are left alone, and rerunning changes nothing.
- Removal with two install records on one host: the first `remove` leaves the drop-in, the sysctl
  file and ssh; the second removes them, and disables ssh only when `ssh-installed` exists.
- The sshd drop-in and sysctl content, and a restart only when the drop-in changed.
- Merging `known_hosts`, and replacing the desktop's entry from the key read in the install's own
  command.
- Choosing the address and gateway from a recorded `incus list -f json` fixture, and the pin
  command for profile and local devices.
- The root pass: skipped when its outcomes hold; refused without a terminal; refusing a stage that
  is a symlink, is not owned by `SUDO_UID` or is not mode 700; no command run as root names a path
  under the user's home.
- The User step refusing each privileged group with the `gpasswd` line, and a sudoers rule.
- A reported key that differs from the saved one is saved, and the host and user passes run again.
- The Claude-state step writing nothing while the unit is active.
- The status exit code for each failing row; no pane line holding `code=` is printed; the
  `systemctl status` fallback when the journal refuses.
- `flocc cardinal -H x status` is refused.

Live run, on zer0-day-nimble with `--user cardinal-test` and a throwaway VM from `flocc host create
zer0-day-nimble flocc-cardtest`, passing `--only flocc-cardtest` so that the live cardinal and
flocc-nimble are not touched until the last step. Record the results in `docs/vm-setup.md`.

1. First `setup`. It asks for sudo once, and its summary shows the key installed, the host
   reachable and the login pending. The desktop's `flocc -H flocc-cardtest status` still works
   (the host-key refresh).
2. `setup` again. No sudo prompt, every step done, and no file's mtime changed.
3. Break pieces and rerun: delete the host file, remove the key line on the VM, stop the unit, add
   the user to `docker`. Setup repairs the first three and stops on the group with the `gpasswd`
   line. Then delete cardinal-test's key and rerun: setup says the key changed, and the VM holds
   one line, with the new key.
4. `login` with a pool account. The session `cardinal-test` appears in the Claude app and runs a
   first check. This proves that the initial-prompt `/loop` arms (Unconfirmed). Ask it to list its
   scheduled tasks.
5. The Linear authorisation through `attach --write`. A check then reads Linear, and the pane's
   history holds no `code=` after the detach.
6. `pkill -u cardinal-test claude`. The unit is back within a minute and `NRestarts` went up. Start
   a `tmux` session on the default socket as cardinal-test first; it changes nothing.
7. `sudo systemctl restart user@<uid>`, then a full reboot when the laptop is free. cardinal comes
   back with a fresh loop.
8. `cardinal-renew` with the age limit set to zero (a test flag): a fresh session and loop. Run
   during a check, it does nothing. After `systemctl --user stop cardinal`, it leaves it stopped.
   `systemctl --user list-timers` shows the next window.
9. `incus stop flocc-cardtest`, then `incus start`, three times. Each time the address is the
   same, `systemctl is-active ssh` says active, and sshd listens on the address.
10. On flocc-cardtest, add a throwaway key made on the VM to root's `authorized_keys`, with the
    same options as cardinal's line. `ssh` with it from the VM to its own address is refused
    (`from=`), and so is password login. cardinal-test's key never leaves the machine.
11. A second install, `--user cardinal-test2 --only flocc-cardtest`, then `remove --purge` for it.
    cardinal-test still reaches the host, and ssh is still active.
12. `status` shows every row. `attach` without `--write` ignores typing.
13. `remove`: units gone, key line, install record, drop-in and sysctl file gone, ssh stopped.
    `remove --purge`: the user is gone. Then destroy flocc-cardtest.
14. `flocc cardinal setup zer0-day-nimble` over the live cardinal (Moving the hand setup over).
    The transient unit is gone, `cardinal.service` runs on `-L cardinal`, flocc-nimble holds one
    cardinal line, with `from=`, and the desktop still reaches flocc-nimble.

## Rejected

- **A systemd timer that types the check prompt into the pane every 20 minutes.** There is no
  expiry, but it types into whatever has focus, and a permission dialog takes Enter as yes.
- **Re-arming `/loop` with `send-keys` before day 7 in a live session.** The same typing risk, and
  two loops run if the old one has not expired yet.
- **cardinal reaching VMs through `incus exec`.** `incus-admin` is root on the machine (d101).
- **cardinal over Tailscale SSH from the machine.** Tailscale authenticates the machine's node,
  which is the owner's identity, so every local user would get the owner's rights, and check mode
  prompts.
- **Ordering `ssh.service` after `network-online.target`.** Whether that target waits for the
  lease depends on the image's network stack; the sysctl works with any.
- **Ansible, cloud-init or a shell script.** The `Step` framework already does checked,
  resumable setup over SSH, with a summary.

## Unconfirmed

- That a slash command passed as the positional prompt (`claude … '/loop 20m …'`) arms the loop.
  Fallback: an `ExecStartPost` that waits for the prompt box and sends the `/loop` line once. The
  session is fresh then, and onboarding and trust are already set, so no dialog can take the keys.
- That no turn of a check goes 5 minutes without writing its transcript. A longer tool call would
  let the renewal cut that check.
- That Tailscale SSH serves `/etc/ssh` host keys once they exist. This is the likely cause of the
  desktop's host-key failure on flocc-nimble. The Host pass's refresh covers both cases.
- That setting `ipv4.address` on a running VM's `eth0` keeps the lease without a NIC restart.
- Whether the Linear MCP can be authorised outside the TUI.
- That connections from the machine reach the VM from the `incusbr0` gateway address, which `from=`
  relies on.

## Open questions

The design above uses the first option in each.

1. **Renewal.** A fresh session daily, in a window from 04:00, which bounds context growth and
   carries pending asks in `~/state.md`. The alternative is a weekly restart with `claude
   --continue`, which keeps the owner's conversation thread at the cost of a growing, compacted
   context.
2. **cardinal's Linear identity.** The owner's own OAuth, as live, with no extra seat. The
   alternative is a separate Linear member, so that cardinal's comments are told apart from the
   owner's.
3. **Who notices cardinal is down.** Nobody beyond the Claude app and `flocc cardinal status`; the
   owner finds ntfy unhelpful. The alternative is a cardinal row in the desktop's `flocc status -H
   all`.
4. **Hosts in scope.** Only Incus VMs on cardinal's machine; flocc-nimble is the only host today.
   The alternative is remote SSH hosts too, with a cardinal key on their sshd over the tailnet.
