# cardinal

cardinal is a Claude Code session on its own small Incus VM, `flocc-cardinal`, on the machine
that holds your executor host. It sits idle, spending nothing, until the executor has something
for it. Then it triages the run, starts one fixer when a note can fix it, holds new starts, and
tells you, in the Claude app, only what needs you. It never reruns a blocked, timed-out or
finished run; it tells you what the run needs. Once a day it runs the cleanup pass and writes
the digest.

## Make it

From the flocc checkout, after the executor host runs this flocc version (`flocc ship`):

    flocc host cardinal -H flocc-nimble [--renew-hour 4]

The command makes the VM (1 vCPU, 2 GiB, 10 GiB disk) on the host file's machine, installs
Claude Code for a `cardinal` user, pins both VMs' bridge addresses, gives the executor an
OpenSSH server on its bridge address and a firewall that lets cardinal reach only that sshd,
puts cardinal's key in `flocc`'s `authorized_keys` with a forced command, and starts the
session. It hands you the terminal twice: once to sign cardinal in to Claude, and once for the
Remote Control consent, where you answer `y`, wait for the connected line, then press Ctrl-C.
Run it again to repair anything. Each step checks first.

## How it wakes

cardinal's plugin has one monitor, `when: "always"`, that runs
`ssh executor events --follow` and reconnects whenever the connection ends. The executor prints
every unacknowledged event when cardinal connects, then one JSON line per batch, at most 50
events a line. A batch closes 2 minutes after its first event. An event cardinal has not
acknowledged is printed again every 30 minutes, at most 3 times per connection; after that it
waits for the next connection, and `flocc status` and the digest list it as needing attention.
After 5 failed connections in a row the loop prints one `link-down` line, and one `link-up` line
when a connection holds again.

Events: `ended` (a PR is ready, or a run blocked or timed out), `cap` (the check still fails after
every fix round), `base-broken` (once per break), `bad-sequence` (an unusable `Run:` line),
`service-error` (a failing tick, Linear failing three ticks in a row, a token or the Claude login
expiring or rejected) and `daily` (the digest hour). Routine starts, fixers under the cap,
reviews and rebases emit nothing.

## What it can do

cardinal's key runs only `flocc-cardinal-gate` on the executor, and only from cardinal's address.
The gate splits the request without a shell and allows `events`, `ack`, `status`, `run-summary`,
`fix`, `hold`, `release`, `cleanup` and `digest-data`, with every issue key matching
`^[A-Z]+-[0-9]+$`. Its `release` is `release --hold-only`: it lifts a hold and nothing else, and
is refused for a blocked, timed-out or done run. Inside the VM, cardinal runs in `dontAsk` mode,
in an empty directory, under root-owned managed settings that allow only its executor wrapper and
its skills and deny the read and web tools, so it cannot read its own key or Claude login.

## Daily fresh start

`cardinal-renew.timer` restarts the session at the renewal hour (default 04:00, local time).
Events wait in the executor's ledger meanwhile, so nothing is lost. The new Remote Control
session sends one phone notification.

## The link

cardinal reaches the executor only over the Incus bridge, by ssh, as `flocc`. When the link step
of `flocc host cardinal` fails, run the command it prints on the machine. Usual causes: the
executor VM is stopped, or the machine's firewall drops VM-to-VM traffic on the bridge (see
"What create changes on the machine" in [Create a host](setup/host-create.md)).
