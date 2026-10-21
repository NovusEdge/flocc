# `flocc setup`: one command from nothing to a working fleet

flocc's setup is four commands today, each with its own arguments: `flocc keys`, `flocc host create
<machine> <name>` (which also adds, ships and logs in the host), `flocc cardinal setup <machine>`
and `flocc cardinal login`. A new user has to know the order, and a machine with an older flocc on
it keeps the old pieces running. On zer0-day-nimble on 2026-10-21 that was the `linear-agents`
service user, still lingering with a tmux server two days after the move into a VM.

`flocc setup` walks through all of it in order and is safe to rerun: each step checks first, so a
rerun shows what is done and repairs the rest. The existing commands stay for anyone who wants one
piece; the wizard only orders them and asks the questions in between.

## The steps

```
$ flocc setup
  ✓ keys          Linear key, GitHub tokens, ntfy topic
  ✓ flocc-nimble  on zer0-day-nimble, doctor ok
  ? old versions  linear-agents user on zer0-day-nimble, lingering since 2026-10-20: remove? (Y/n)
  ? cardinal      set up on zer0-day-nimble? (Y/n)
  … logins        cardinal: flocc cardinal login opens the browser
```

1. **Keys.** `keys.Fill` for every slot, as `flocc keys` does. Done when every key is stored and
   passes its check.
2. **Host.** With no host file, ask: create one on a machine (`host create`, which asks for the
   machine and name) or add an existing one (`host add`, then `ship`). With host files, show each
   one's `flocc status --json` and doctor result, and offer `ship` only for a host whose doctor
   fails. A Daytona host has no machine, so the next two steps skip it.
3. **Old versions**, for each machine that holds a host. Without sudo, over the owner's ssh, look
   for the pieces older flocc versions left on the machine itself:
   - a `linear-agents` user (`id linear-agents`), with its lingering state, its running processes
     (`ps -u linear-agents`) and how long its session has been up.

   Show each one found and ask before removing it. Removal runs in the machine's root pass,
   through `CardinalPurge`, which already disables lingering, ends the session, waits for the
   processes and runs `userdel -r`. The list of known old pieces lives in one table in the code, so
   a later rename adds a row, not a step.
4. **Cardinal.** For a machine with a saved cardinal install, rerun `flocc cardinal setup` as a
   repair. Otherwise ask "Set up cardinal on <machine>?"; a no skips it for this run and is asked
   again on the next, so nothing new is stored for it.
5. **Logins.** For each host and for cardinal whose Claude login is not full, run `flocc login -H
   <host>` or `flocc cardinal login`. Both open the browser; the wizard asks which pool account.

It ends with the same one-line-per-step summary, and exits non-zero while any step is not done.

## One sudo per machine

Steps 3 and 4 need root on the machine. They share one root pass: the desktop collects what the
machine needs (the old users to remove, cardinal's root outcomes) and runs
`flocc-setup --phase cardinal-root` once, with `--remove-user <name>` for each old user the owner
agreed to remove. The root pass accepts only names in the old-pieces table, so the flag cannot
delete cardinal, the owner or any other account. A machine with nothing to do as root gets no
sudo prompt.

## Which machine a host is on

Host files gain `machine = "<ssh destination>"`. `host create` writes it. For a host file without
one, the wizard asks once ("Which machine holds flocc-nimble? Empty for none") and saves it. With
it, `flocc cardinal setup` needs no argument when one machine holds hosts; with several, it lists
them.

## What it does not do

- It does not install packages on the machine, or change the tailnet policy. The tailnet route
  through cardinal's hosts (see the Part 2 spec) stays the owner's change.
- It does not remove anything it did not find and show, and nothing without a yes.
- It does not run without a terminal: every step can ask a question.

## Testing

- The step runner with fakes for each existing command: a fresh desktop runs keys, host create,
  cardinal and logins in order; a finished one runs nothing and exits 0; a failing doctor offers
  ship only for that host.
- Old versions: a fake machine with `linear-agents` lingering shows it and passes
  `--remove-user linear-agents` only after a yes; none found adds no root work.
- One root pass per machine whether cardinal, removal or both need it, and none when neither does.
- Host files round-trip `machine`; a file without it is asked about once and saved.
- Live: rerun on zer0-day-nimble removes `linear-agents` after a yes, then moves the hand cardinal
  over (the Part 2 spec's migration), with one sudo prompt.
