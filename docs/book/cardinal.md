# cardinal

cardinal is a Claude Code session on its own small Incus VM, `flocc-cardinal`, on the machine
that holds your executor host. It sits idle, spending nothing, until the executor has something
for it. Then it triages the run, resumes it once with a note when the note can carry it on (an
answer to the agent's questions, or what the next attempt does differently), holds new starts,
and tells you, in the Claude app, only what needs you. It never starts a fresh run or touches a
finished one. Once a day it runs the cleanup pass and writes
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
every fix round), `needs-owner` (an agent stopped with questions), `base-broken` (once per break), `bad-sequence` (an unusable `Run:` line),
`service-error` (a failing tick, Linear failing three ticks in a row, a token or the Claude login
expiring or rejected), `daily` (the digest hour) and `host-pressure` (`/tmp` above
`tmp_max_pct`, memory available below `min_available_mem_gb`, or a new OOM kill; once per
episode, and again for each new kill). Routine starts, fixers under the cap, reviews and rebases
emit nothing.
A stopped run's event also carries the stop's `cause` and its `type`: `agent` (cardinal may
resume it), `owner`, `transient` (the service retries it once) or `repo` (the repo's base is
broken). Other events carry null in both.

## What it can do

cardinal's key runs only `flocc-cardinal-gate` on the executor, and only from cardinal's address.
The gate splits the request without a shell and allows `events`, `ack`, `status`, `run-summary`,
`resume`, `hold`, `release`, `cleanup`,
`cleanup --now`, `digest-data`, `host`, `stop-tests`, `shell`, `issue`, `pr`, `comment`, `pr-comment`, `search` and `file`, with every issue key
matching `^[A-Z]+-[0-9]+$`. The six Linear and GitHub verbs run as the service user with its
Linear key and publish token, so neither reaches `flocc-agent`; `file` only drafts unless
`cardinal_file_on` is set, and never adds the trigger label. Its `release` is
`release --hold-only`: it lifts a hold and nothing else, and is refused for a blocked, timed-out
or done run. Its `host` runs `flocc host-report`.

Inside the VM, cardinal runs in `default` mode, in an empty directory, under root-owned managed
settings that allow its executor wrapper, `agent-shell` and its skills, and deny the read, edit,
web and MCP tools. A managed PreToolUse hook,
`only-executor`, refuses any Bash command that is not one call to `executor` or `agent-shell`, or
that holds a shell operator, so cardinal cannot read its own key or Claude login. `executor`
refuses `shell`: the only way to the gate's `shell` verb is `agent-shell`, which runs one command
as `flocc-agent` with a 540-second timeout and lists it in the next digest. It does not ask you
first: `flocc-agent` is the user every agent session already runs as in `bypassPermissions` mode.
Triage uses it to read a run's worktree, git history, check output and logs, and for host repairs.

## Daily fresh start

`cardinal-renew.timer` restarts the session at the renewal hour (default 04:00, local time).
Events wait in the executor's ledger meanwhile, so nothing is lost. The new Remote Control
session sends one phone notification.

## The link

cardinal reaches the executor only over the Incus bridge, by ssh, as `flocc`. When the link step
of `flocc host cardinal` fails, run the command it prints on the machine. Usual causes: the
executor VM is stopped, or the machine's firewall drops VM-to-VM traffic on the bridge (see
"What create changes on the machine" in [Create a host](setup/host-create.md)).
