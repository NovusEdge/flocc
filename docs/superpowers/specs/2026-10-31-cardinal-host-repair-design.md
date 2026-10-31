# Cardinal repairs the executor host

Status: design, approved by the owner on 2026-10-31 subject to an Opus review. This version applies
that review.

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

- Host pressure wakes cardinal within about five minutes: one 3-minute tick, plus up to
  `events.BATCH_S` (120 s) of batching.
- Known problems are repaired without the owner: stale temp files, and a test run that is filling
  `/tmp` or eating memory.
- No session starts or relaunches while the host is under pressure, and the wait does not use up a
  fix round or a restart.
- For a problem nobody planned for, cardinal may run one command at a time as `flocc-agent`. The
  owner approves each command in the Claude app.
- Cardinal gets no root, no shell as `flocc`, and no arbitrary command on its own VM.

## Design

### 1. Measuring the host

A new module, `flocc/host.py`, reads three values as the service user, without root:

| Value | Source | Opens an episode when | Closes when |
|---|---|---|---|
| `/tmp` use | `os.statvfs("/tmp")` | above `tmp_max_pct` (default 80) | below 0.9 × `tmp_max_pct` |
| Memory available | `MemAvailable` in `/proc/meminfo` | below `min_available_mem_gb` (default 1.0) | above 1.1 × `min_available_mem_gb` |
| OOM kills | `oom_kill` in `/proc/vmstat` | the counter rose since the last tick | no new kill this tick |

Both thresholds are new `Config` fields, next to `min_free_disk_gb`. The last `oom_kill` value is
stored in `meta`. The first reading only sets the baseline and emits nothing. A value lower than
the stored one means the VM rebooted, so it resets the baseline.

### 2. The pressure event and the start gate

On every tick, before `supervise`, `host.check(rt)` measures the host.

- **Opening an episode.** When any value crosses its open threshold and no episode is open, it
  emits a `host-pressure` event, a new member of `events.KINDS`, and opens the episode in `meta`.
  The detail names each value over its limit, for example
  `/tmp 93% (2.7/2.9 GiB); MemAvailable 0.6 GiB; 2 OOM kills`.
- **Repeat events.** A new OOM kill during an open episode emits another event, because it means
  the repair did not hold.
- **Closing.** The episode closes only when every value has passed its close threshold. This
  follows `canary.mark`: one event per break.

While an episode is open:

- `admission.admit` refuses new starts, with the reason in the decision, as it does for
  `min_free_disk_gb`. That covers `poller.py:258` and `control.admit_rerun`.
- `supervisor` launches nothing: no fix round, review, crash relaunch or next step. Today those
  paths call no admission (`supervisor.py:205,256,346,384,461,556,782`). Each launch site checks
  `host.under_pressure(rt.con)` and leaves the claim as it is for the next tick. It does not count
  a fix round or a restart, so a run that is waiting keeps its budget.
- When the episode closes, each running session gets one message through the existing `send`
  path: "The host has recovered; you may run the test suite again."

### 3. Repairs without approval

These are new gate verbs. Each one goes through a new `flocc-agent-run` subcommand, because the
service user can neither read nor remove `flocc-agent`'s files. The new subcommands go into the
root-owned allowlist in `deploy/flocc-agent-run:6` and into `agentrun.SUBCOMMANDS` and `HANDLERS`.
A host picks up the new allowlist when `flocc setup` is rerun.

**`host`** (`flocc-agent-run usage`) is a read-only report:

- the three values;
- the ten largest entries under `/tmp` that `flocc-agent` owns, each with the issue that owns it
  when one does: a pytest directory's `.lock` pid, followed to its process's cwd under
  `worktree_root/<KEY>`;
- each claim's process memory, the sum of RSS for processes whose cwd is under its worktree;
- `podman ps` memory per container, listed separately.

tmpfs pages belong to no process, so the `/tmp` list is the measure for `/tmp`. Process RSS does
not count them.

**`cleanup --now`** runs the existing sweep with a shorter scratchpad age, and the sweep gains a
pytest step:

- The scratchpad age drops from `SCRATCH_AGE_S` (a day) to 10 minutes. `max_age_s`, which also
  governs worktree and state removal, stays at 7 days. A scratchpad that a kept claim lists is
  still kept.
- The new step removes `/tmp/pytest-of-flocc-agent/pytest-<N>` directories. It skips symlinks,
  such as pytest's `pytest-current`, and any directory whose `.lock` names a live pid. In the daily
  pass, it removes only directories older than a day.
- The result lists what was removed and the bytes freed.
- `cmd_cleanup` refuses while a tick holds the lock (`cli.py:425`). `--now` instead waits up to
  60 seconds for the lock, and reports that it is busy if it can't get it.

**`stop-tests KEY`** (`flocc-agent-run stop-tests`) stops the issue's test work and tells the
session why:

1. It kills the process trees that the session's Bash tool started. Those trees are the children
   of the session's `claude` process whose argv runs Claude Code's shell snapshot. MCP servers
   are also children of `claude`, and this rule leaves them alone.
2. It removes podman containers whose working directory or labels place them under the issue's
   worktree.
3. It sends the session one message through `send`: "flocc stopped your test run because the host
   is under memory or /tmp pressure. Do not rerun the suite until flocc says the host has
   recovered."

A process that left the tree through `setsid` or a double fork escapes the kill. The `host` report
still shows it, so cardinal can reach it with `shell`. Before relying on the argv rule, the plan
confirms it against a live session.

### 4. The shell, with approval

**`agent-shell CMD`** is a separate binary in cardinal's plugin, `bin/agent-shell`. It sends the
gate verb `shell` over the same SSH link. The gate runs the command as `flocc-agent`, through a
new `flocc-agent-run shell` subcommand:

- the command goes to `bash -c` as a single argument;
- it runs with a 120-second timeout;
- output is capped at 8,000 characters;
- each call is recorded in `meta` with its time, command and exit status, and `digest-data`
  includes the day's calls, so every one appears in the daily digest.

`bin/executor` refuses `shell` as its first argument. So the only path to the gate's `shell` verb
is through `agent-shell`, and no `allow` rule covers that binary. Bash rules match command text,
so an `ask` rule on `executor shell:*` would not be enough: `executor 'shell' …` would match only
the broader allow.

Running as `flocc-agent` gives cardinal no more than an agent session already has, since every
session runs as `flocc-agent` in `bypassPermissions` mode. The approval checks cardinal's
judgment; it is not a security boundary between cardinal and the executor.

**How approval works.** Approval is Claude Code's permission prompt. Remote Control forwards the
prompt to the Claude app and keeps it open until it is answered, and a mobile push fires when
"Push when actions required" is on. `dontAsk` denies every call that would prompt, so cardinal
moves to `default` mode:

```json
{
  "permissions": {
    "defaultMode": "default",
    "allow": ["Bash(/opt/cardinal/plugin/bin/executor:*)", "Skill"],
    "ask": ["Bash(/opt/cardinal/plugin/bin/agent-shell:*)"],
    "deny": ["Read", "Glob", "Grep", "WebFetch", "WebSearch", "Edit", "Write", "NotebookEdit", "mcp__*"],
    "disableBypassPermissionsMode": "disable"
  },
  "hooks": {
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "/opt/cardinal/plugin/bin/only-executor"}]}]
  }
}
```

**The managed `only-executor` hook.** It exits 2 for any Bash command whose first word is not
`/opt/cardinal/plugin/bin/executor` or `/opt/cardinal/plugin/bin/agent-shell`, and for any command
containing a shell control operator (`;`, `&`, `|`, `` ` ``, `$(`, a newline or a redirection). A
blocking PreToolUse hook runs before permission rules, so in `default` mode cardinal still cannot
run an arbitrary command on its own VM, the one that holds the executor key and cardinal's Claude
login. Under `dontAsk` today, such calls were denied too. `cardinal.service` drops
`--permission-mode dontAsk`.

**Risks.** These stay open, and the triage skill is written around them:

- **A prompt the owner should refuse.** Cardinal reads event details and run summaries, which come
  from agent and Linear text, so injected text could ask cardinal to run `agent-shell`. The prompt
  shows the exact command. The owner refuses anything that isn't obviously a host repair.
- **A pending prompt stalls cardinal.** Its one session waits on the prompt, so other events queue
  until the owner answers. `agent-shell` is the last resort for that reason.

### 5. Triage

The triage skill gains a `host-pressure` section:

1. Run `executor host`.
2. If `/tmp` is the problem, run `executor cleanup --now`. If `/tmp` is still over its limit and
   the largest entries belong to a live test run, run `executor stop-tests KEY` for that issue.
3. If memory is the problem, run `executor stop-tests KEY` on the issue with the most process or
   container memory.
4. Run `executor host` again. Report in one line what was wrong, what was done and the values
   after.
5. Use `agent-shell` only when the verbs did not bring the values under their limits. Say in the
   request what the command is for.

Cardinal does not `hold` for host pressure. A hold only blocks new starts, and the pressure gate
already does that.

## What this does not do

- No root verbs. The OOM counter comes from `/proc/vmstat`, so the kernel log is not needed.
- No per-session cgroups. Per-issue memory comes from process cwd and container placement, so a
  process that escapes both is missed. A `systemd-run --user --scope` per session would measure
  and kill by cgroup, including tmpfs pages; that is the next step if this proves too coarse.
- No change to nimblefox's tests. Whether the drift tests stop copying `node_modules` is a
  separate issue in that repo.
- Agent sessions keep `/tmp` as their temp directory. Moving `TMPDIR` to disk becomes optional
  once these repairs exist; it stays a separate task.

## Testing

- **`host.check`.**
  - An episode opens once.
  - It stays open while values sit between their open and close thresholds.
  - It closes past the close thresholds.
  - It emits again on a new OOM kill.
  - The first reading and a counter drop set the baseline without emitting.

  These tests use fake readings.
- **The start gate.**
  - `admission.admit` refuses while an episode is open.
  - Each supervisor launch site defers without counting a fix round or a restart.
  - Closing the episode sends the recovery message to each running session.
- **`cardinalgate.argv_for`.**
  - It accepts `host`, `cleanup --now`, `stop-tests KEY` and `shell CMD`, and refuses malformed
    forms.
  - `shell` is capped at `NOTE_CHARS`.
  - `bin/executor shell …` is refused before ssh runs.
- **`only-executor`.** It passes the two binaries and refuses other first words and every control
  operator.
- **The `agentrun` sweep.**
  - It keeps a pytest directory whose `.lock` holds a live pid, and removes one whose pid is dead.
  - It skips `pytest-current`.
  - `--now` does not touch worktrees younger than 7 days.
- **`stop-tests`.** It kills a Bash-tool shell's tree and leaves the `claude` process and an MCP
  child running. This test uses real processes.
- **On flocc-nimble.**
  - Emit a fake `host-pressure` event and watch cardinal run the triage section end to end.
  - Check that an `agent-shell` call prompts in the Claude app, and that a refused prompt runs
    nothing.
  - Check that `executor 'shell' x` is refused.
