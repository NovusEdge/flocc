# Cardinal repairs the executor host

Status: design, approved in conversation 2026-10-31; this file awaits the owner's review.

## What the owner asked for

> "make it so that the cardinal can also handle like, you know, OOMs, so so that like if there's
> like memory issues or whatever, if like temp is getting like too cluttered or some shit like
> that, then the Cardinal can kind of like step in and see what it can do"

> "i dont wanna have to come in every fucking time to fix or approve tbh that's WHY i have
> cardinal"

Asked for fixed repair verbs or a general shell, the owner answered "both".

## What happened on 2026-10-31

flocc-nimble's `/tmp` is a 2.9 GiB tmpfs. nimblefox's contracts drift tests copy `contracts/`
(about 121 MiB) into a pytest `tmp_path` for each test, and the base directories outlived their
runs: `/tmp/pytest-of-flocc-agent` reached 2.8 GiB. NF-574's fixer failed both fix rounds on
`OSError: [Errno 28] No space left on device`. The tmpfs held 3.1 GiB of the VM's 5.8 GiB RAM, and
NF-576's session died twice. Nothing reported it: no event kind covers the host, so cardinal
never woke, and the owner found it by hand from a Linear comment.

## Outcome

- Host pressure wakes cardinal within one tick.
- Known problems are repaired without the owner: stale temp files, and a test run that is eating
  the host's memory.
- No new issue starts while the host is under pressure.
- For an unforeseen problem, cardinal may run one command at a time as `flocc-agent`, each one
  approved by the owner in the Claude app.
- Cardinal gets no root and no shell as `flocc`.

## Design

### 1. Measuring the host

A new module, `flocc/host.py`, reads three values without root, as the service user:

| Value | Source | Pressure when |
|---|---|---|
| `/tmp` use | `os.statvfs("/tmp")` | above `tmp_max_pct` (default 80) |
| Memory available | `MemAvailable` in `/proc/meminfo` | below `min_available_mem_gb` (default 1.0) |
| OOM kills | `oom_kill` in `/proc/vmstat` | the counter rose since the last tick |

Both thresholds are new `Config` fields, next to `min_free_disk_gb`.

### 2. The pressure event and the start gate

On every tick, before `poll`, `host.check(rt)` measures the host. When any value is under
pressure and no episode is open, it emits a `host-pressure` event, a new member of
`events.KINDS`, and opens the episode in `meta`. The event's detail names each value that is over
its limit, for example `/tmp 93% (2.7/2.9 GiB); MemAvailable 0.6 GiB; 2 OOM kills`. The episode
closes when every value is back under 90% of its limit (`/tmp` below 72%, memory above 1.1
GiB), so a value that hovers near its limit does not emit one event per tick. A new OOM kill
during an open episode emits another event, because it means the repair did not hold. The pattern
is `canary.mark`'s: one event per break.

`admission.admit` refuses a start while an episode is open, with the reason in the decision,
exactly as it does for `min_free_disk_gb`. A rerun from `control.admit_rerun` waits too.

### 3. Repairs without approval

These are new gate verbs. Each one goes through a new `flocc-agent-run` subcommand, because the
service user cannot read or remove `flocc-agent`'s files.

**`host`** is a read-only report: the three values, the ten largest entries under `/tmp` that
`flocc-agent` owns, and the resident memory of each claim's processes. The agent side groups
processes by a cwd under `worktree_root/<KEY>`. The `flocc-agent-run` subcommand is `usage`.

**`cleanup --now`** runs the existing `cleanup` sweep with short ages, and the sweep now also
covers pytest base directories:

- The sweep's scratchpad age drops from `SCRATCH_AGE_S` (a day) to 10 minutes. A scratchpad that
  a kept claim lists is still kept.
- A new step removes `/tmp/pytest-of-flocc-agent/pytest-*` directories. It skips any directory
  whose `.lock` names a live pid: pytest writes that lock while the run uses the directory. In the
  daily pass, this step removes only directories older than a day.
- The result lists what it removed and how many bytes it freed.

**`stop-tests KEY`** kills the process trees that the session's Bash tool started in that issue's
worktree. Those trees are the children of the session's `claude` process whose argv runs Claude
Code's shell snapshot. MCP servers are also children of `claude`, and this rule leaves them
alone. The session sees its command fail and carries on. The `flocc-agent-run` subcommand is
`stop-tests`. The plan confirms the argv rule against a live session before relying on it.

Cardinal may also `hold` the issue that uses the most memory, so it cannot start again until the
host recovers. `hold` already exists.

### 4. The shell, with approval

**`shell CMD`** is a gate verb that runs one command as `flocc-agent`, through a new
`flocc-agent-run shell` subcommand.

- The command goes to `bash -c` as one argument.
- It runs with a 120-second timeout, and its output is capped at 8,000 characters.
- Each call is recorded in `meta`, with the time, the command and the exit status. `digest-data`
  includes the day's calls, so each one shows up in the daily digest.

Running as `flocc-agent` gives cardinal no more than an agent session already has, since every
session runs as `flocc-agent` in `bypassPermissions` mode. The approval checks cardinal's
judgment; it is not a security boundary.

Approval comes from Claude Code's permission prompt, which Remote Control forwards to the Claude
app and keeps open until it is answered. A mobile push fires when "Push when actions required" is
on. `dontAsk` denies every call that would prompt, so cardinal's managed settings change:

```json
{
  "permissions": {
    "defaultMode": "default",
    "allow": ["Bash(/opt/cardinal/plugin/bin/executor:*)", "Skill"],
    "ask": ["Bash(/opt/cardinal/plugin/bin/executor shell:*)"],
    "deny": ["Read", "Glob", "Grep", "WebFetch", "WebSearch", "Edit", "Write"],
    "disableBypassPermissionsMode": "disable"
  }
}
```

`cardinal.service` drops `--permission-mode dontAsk`. In `default` mode, a call that matches no
rule prompts instead of being denied. Cardinal's skills already restrict it to `executor`, so a
stray call reaches the owner as a prompt, not as an action.

### 5. Triage

The triage skill gains a `host-pressure` section:

1. Run `executor host`.
2. If `/tmp` is the problem, run `executor cleanup --now`.
3. If memory is the problem, run `executor stop-tests KEY` on the claim using the most memory,
   then `executor hold KEY --reason "host memory"`.
4. Run `executor host` again. Report in one line what was wrong, what was done and the values
   after.
5. Use `executor shell` only when the verbs did not bring the values under their limits, and
   name in the request what the command is for.

A hold placed in step 3 is lifted by cardinal with `release` once the episode closes, which a
later `host` shows.

## What this does not do

- No root verbs. The OOM counter comes from `/proc/vmstat`, so the kernel log is not needed.
- No change to nimblefox's tests. Whether the drift tests stop copying `node_modules` is a
  separate issue in that repo.
- Agent sessions keep `/tmp` as their temp directory. Moving `TMPDIR` to disk becomes optional
  once these repairs exist; it stays a separate task.

## Testing

- `host.check`: an episode opens once, stays open while the values hover near their limits,
  closes below 90%, and emits again on a new OOM kill. Use fake readings.
- `admission.admit` refuses while an episode is open.
- `cardinalgate.argv_for` accepts `host`, `cleanup --now`, `stop-tests KEY` and `shell CMD`, and
  refuses malformed forms. `shell` is capped at `NOTE_CHARS`.
- `agentrun` sweep: keeps a pytest directory whose `.lock` holds a live pid, and removes one whose
  pid is dead.
- `stop-tests`: kills a Bash-tool shell's tree and leaves the `claude` process and an MCP child
  running. Use real processes in a test.
- On flocc-nimble: emit a fake `host-pressure` event and watch cardinal run the triage section
  end to end. Check that an `executor shell` call prompts in the Claude app and that a refused
  prompt runs nothing.
