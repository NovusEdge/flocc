# `flocc setup`: one command from nothing to a working fleet

flocc's setup is four commands today, each with its own arguments: `flocc keys`, `flocc host create
<machine> <name>` (which also adds, ships and logs in the host), `flocc cardinal setup <machine>`
and `flocc cardinal login`. A new user has to know the order, and a machine with an older flocc on
it keeps the old pieces running. On zer0-day-nimble on 2026-10-21 that was the `linear-agents`
service user from the hand-setup era (the first `docs/vm-setup.md`, commit 71ded12), still
lingering with a tmux server two days after the move into a VM.

`flocc setup` walks through all of it in order and is safe to rerun: each step checks first, so a
rerun shows what is done and repairs the rest. It calls the existing functions (`addHost`,
`shipHost`, `loginWith`, `create.Create`, `cardinal.Deps`), not their `Main` entry points, which
read `os.Stdin` and install their own signal handlers. The existing commands stay.

```
$ flocc setup
  ✓ checkout      ~/Projects/Personal/linear-agents at 0.8.0
  ✓ keys          Linear key, agent token
  ✓ flocc-nimble  on zer0-day-nimble, flocc 0.8.0, doctor ok
  ✓ keys          publish token, ntfy topic
  ? old versions  linear-agents user on zer0-day-nimble (lingering since 2026-10-20; tmux running)
                  remove it? its home is archived first (y/N)
  ✓ cardinal      status ok
  … logins        cardinal: login is none; flocc cardinal login opens the browser
```

## Before anything

- **A terminal.** Every step can ask; without one `flocc setup` exits 2.
- **The flocc checkout.** Ship, create and cardinal build from it (`needCheckout`); checked first.

## The steps

1. **Keys, the first half.** `keys.Fill` for the Linear key and the agent token only. The publish
   token and the ntfy topic need a mapped repo and come after the host, as `addHost` already
   orders them.
2. **Host.**
   - With no host file: ask create-on-a-machine or add-existing. Create asks for the machine and
     the name (create takes them as arguments), checks its preconditions first (below), then runs
     `create.Create`. Add asks for the ssh destination or Daytona sandbox, then `addHost` and
     `shipHost`. A create that stopped before `host add` left `~/.cache/flocc/create-<name>.log`;
     the wizard offers to continue that name instead of asking create-or-add again.
   - With host files: read each host's `flocc status --json`, `flocc --version` and `doctor`.
     Each failing check maps to its remedy: a host older than the checkout, or a doctor check
     that ship fixes, gets `ship`; a login check gets step 6; low disk and an unreachable host are
     reported with their fix and not acted on.
   - A host that fails stops steps 4-6 for its machine; the wizard goes on with other machines.
3. **Keys, the second half.** The publish token and the ntfy topic, then `keys.Check`.
4. **Old versions**, for each machine that holds a host. See below.
5. **Cardinal**, for the machine of the first host on one (one cardinal per desktop; see below).
   - A saved install whose `cardinal status` passes: done, nothing runs.
   - A saved install whose status fails: `cardinal setup` as the repair.
   - No saved file but a `cardinal` user on the machine (`id cardinal` over the owner's ssh): the
     hand setup. Ask "Adopt the existing cardinal user on <machine>?"; yes runs setup with
     `--adopt`.
   - Neither: ask "Set up cardinal on <machine>?". A no is a skip, not a failure, and is asked
     again next run; nothing is stored for it.
6. **Logins.** A host's login comes from `claude_login` in its `status --json`; cardinal's from
   `login` in `cardinal-status`. `full` is done. `unknown` is no evidence of a lapse, as the fleet
   view's `loginCell` treats it, and is reported, not acted on. Anything else runs `loginWith` for
   the host or `cardinal login`, asking for the pool account.

The summary has one line per step and host. Exit codes: 0 when every step is done or skipped by
the owner, 1 when any is incomplete or failed, 2 without a terminal.

## Machine preconditions

Checked over the owner's ssh before a machine's first root work, with the remedy printed:

- **`host create`** needs passwordless sudo on the machine (its firewall and Docker steps run
  captured `sudo -n` even to check) and runs Incus as `incus-admin` or through that sudo.
- **`cardinal setup`** needs `incus-admin` membership or passwordless sudo for its Incus pass
  (`create.Env.Probe`), and a terminal for its root pass's password.

A machine whose sudo asks for a password can still hold cardinal and run the old-version cleanup;
it cannot run `host create`, and the wizard says so instead of failing halfway.

## Old versions

A table in the code lists each known old piece with how to find it without sudo and how to remove
it. A later rename adds a row.

| Piece | Found by | Removed by |
|---|---|---|
| `linear-agents` user on the machine | `id linear-agents`, `loginctl show-user linear-agents`, `ps -u linear-agents -o args` | the machine's root pass, as below |

Pieces that need no root are reported with their remedy, not removed: an offline tailnet node
such as la-nimble (Tailscale admin console), and a desktop host file whose host is unreachable and
whose tailnet node is offline (`rm` the file; step 2 never offers ship onto it).

Before asking about the user, the wizard shows what it found: lingering, session start, and each
process. A process whose arguments hold `claude` or a flocc tick (`linear-agents run`, `flocc
run`) gets a warning that the old install may still be polling Linear. The default answer is no.

Removal runs in the root pass, and the root pass checks again for itself before it deletes, so
the desktop's word is not enough:

- the name is in the table;
- its uid is at least `UID_MIN` (from `/etc/login.defs`, default 1000) and is not `SUDO_UID`;
- its home is exactly `/home/<name>` and a real directory, not a symlink;
- it is in no `CardinalPrivileged` group;
- logind lists no session for it other than its user manager's.

Then it archives the home to `/root/flocc-removed-<name>-<date>.tar.gz` (root-only, mode 600) and
runs `CardinalPurge`. The summary names the archive and reminds the owner to revoke the old
install's GitHub tokens and Linear key, which this cannot do.

## One password per machine

The removal and cardinal's root work run in one `ssh -t` to the machine, as two sudo calls on one
line: `sudo <stage>/flocc-setup --phase cardinal-purge --user <old> && sudo <stage>/flocc-setup
--phase cardinal-root --user cardinal`. sudo's ticket for that terminal covers the second call, so
the owner types the password once. Either half runs only when it has work: `rootOutcomes` gains
the pending removals, so an agreed removal on a machine with a healthy cardinal still runs, and a
machine where cardinal was declined runs only the purge. The purge phase keeps its own stage file
list (no `desktop.pub`). `CardinalPurge` reports through the same `flocc-cardinal` line as the other
passes, so its result shows in the summary.

## One cardinal per desktop

The cardinal saved file is keyed by user, so a second machine with the default user would
overwrite the first machine's file and adopt whatever `cardinal` user that machine has. The wizard
offers cardinal on one machine only: the one in the saved file, or, on a first setup, the machine
of the first host. cardinal reaches only Incus VMs on its own machine (Part 2 spec), so hosts on
other machines are not covered by it. `flocc cardinal setup --user <name>` remains for a second
install by hand.

## Which machine a host is on

Host files gain `machine`. `host create` writes it: `Installer.AddHost` takes the machine. For a
file without one, the wizard asks once ("Which machine holds flocc-nimble? none for a host not on
one of your machines") and saves the answer, `none` included, so it is not asked again. `machine`
and `daytona` together are refused when the file loads. Machines are compared by
`saved.HostPart`, so `owner@zer0-day-nimble` and `zer0-day-nimble` are one machine with one root
pass.

## What it does not do

- Install packages on a machine, or change sudo, groups or the tailnet policy.
- Remove anything it did not find and show, or anything without a yes.
- Revoke tokens or keys belonging to an old install.

## Testing

- The step runner with fakes for each existing function: a fresh desktop runs keys (first half),
  host create, keys (second half), cardinal and logins in order; a finished one runs only the
  status reads and exits 0; a declined cardinal still exits 0.
- Step 2: an out-of-date host and a ship-fixable doctor failure offer ship; a login failure goes to
  step 6; low disk and unreachable are reported only; a failed host skips steps 4-6 for its
  machine and not for another.
- Preconditions: a machine whose `sudo -n true` fails refuses create with the remedy, and still
  runs cardinal and cleanup.
- Old versions: a fake machine with `linear-agents` shows it with its processes, warns on a
  `claude` process, defaults to no, and passes the purge only after a yes. None found adds no
  root work.
- The root pass refuses a removal for a uid below `UID_MIN`, the `SUDO_UID`, a symlinked or
  non-`/home/<name>` home, a privileged group and a live session; it archives before `userdel`.
- One `ssh -t` and one sudo prompt per machine whether purge, cardinal or both have work, and none
  when neither does; a healthy cardinal with an agreed removal still runs the purge.
- Host files round-trip `machine`, including `none`; `machine` with `daytona` is refused.
- Live on zer0-day-nimble: the rerun shows `linear-agents`, removes it after a yes with the home
  archived, then adopts the hand cardinal (Part 2's migration), with one password prompt.
