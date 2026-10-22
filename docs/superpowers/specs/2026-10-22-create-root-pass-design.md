# Host create: one sudo root pass for the firewall and Docker steps

## Problem

`flocc host create` runs every root command on the machine as its own `ssh -T <machine> sudo -n ...`
(`create.Env.root`, `needRoot`, `firewalld.go`, `docker.go`). With a password on sudo there is no
terminal to type it on, so the Firewalld step stops at once:

> Firewalld: Reading firewalld on zer0-day-nimble needs root, and sudo there asks for a password,
> which create cannot type.

A narrow sudoers rule is no fix: the commands include `cat` and `tee` of system files as root, which
amount to root.

## Design

Create does what `flocc cardinal setup` does with its root pass: build `flocc-setup` for the
machine, stage it in a directory the owner's `mktemp` made, and run it once as root over
`ssh -t <machine> sudo <stage>/flocc-setup --phase create-root`. The owner types the sudo password
once. With passwordless sudo the same command runs without a prompt, so there is one code path.

### Machine side

- The Firewalld and Docker steps move from `installer/internal/create` to
  `installer/internal/steps` (`steps.Firewalld`, `steps.Docker`), written against `steps.Env`.
  They run as root, so commands run without `sudo`. Files are read and written with
  `os.ReadFile`/`os.WriteFile` on `e.Path(...)` (`firewalld.conf`, `daemon.json`, the
  `flocc-docker-user.service` unit) instead of `cat`/`tee`.
- Checks, fixes and messages stay as they are, with one change: the machine-side code does not
  know the owner's ssh destination, so "on <machine>" becomes "on this machine". The desktop's
  error for a failed pass names the machine.
- `docker.service` is still never restarted; only `flocc-docker-user.service` is.
- `steps.CreateRootSteps()` returns `[Firewalld{}, Docker{}]`.
- `steps.CreateRootIdle(ctx, e) (idle bool, detail string)` reports, with no root, whether
  neither step has work: firewalld not active, and Docker neither active, enabled nor
  socket-activated. The steps' Checks use it for their first branch, and the desktop uses it
  through its ssh runner to skip the pass.
- `flocc-setup --phase create-root` is a staged root phase in `cmd/flocc-setup/cardinal.go`,
  next to `cardinal-root` and `cardinal-purge`. It takes no `--user`. It checks the stage as
  those do (owned by `SUDO_UID`, mode 700, no symlinks), runs `steps.RunAll` over
  `CreateRootSteps()`, and prints one `flocc-cardinal` report line with `ReportSteps`.
- The stage check accepts `/tmp/flocc-create.XXXXXX` as well as `/tmp/flocc-cardinal.XXXXXX`.
  `ValidCardinalStage` and `CheckCardinalStage` become `ValidRootStage` and `CheckRootStage`.
- The bridge name moves to `steps.IncusBridge`; `create.Bridge` is defined as it.

### Desktop side

- A new package `installer/internal/rootpass` holds the pieces cardinal and create share, moved
  out of `cardinal/passes.go`: `SSH` (was `sshArgs`), `Build` (was `Deps.build`, minus the
  `Group` call and the RepoRoot lookup), `Pass` (the body of `Deps.rootPass`: mktemp, stage
  check, scp, the one `ssh -t` line of sudo calls, the report tap, the error naming the log),
  `Events` (was `reportEvents`) and `Tap` (was `reportTap`). Cardinal calls them; its behaviour
  and tests do not change. `create` cannot import `cardinal` (cardinal imports create), hence the
  new package.
- In `create.Steps()`, `Firewalld{}, Docker{}` become one `Root{}` step named "Root pass":
  - Check: once the pass ran in this run, done with its detail. Otherwise `CreateRootIdle` over
    `e.M`; idle is done with its detail ("firewalld not running; ...; Docker not running").
    Else not done: "firewalld or Docker runs on <machine>; checking them needs root".
  - Apply: refuse when `!e.TTY && !e.sudo` ("sudo on <machine> asks for a password; run create
    in a terminal"). Read `uname -m`, `ship.ArchFor`, build `flocc-setup` from the checkout
    (`Env.Checkout`, set from the new `Installer.RepoRoot()`), run `rootpass.Pass` with stage
    prefix `/tmp/flocc-create` and the one call `--phase create-root`. Draw the report's steps
    as rows with `rootpass.Events`. Fail on a failed pass (the error names the report's error,
    the machine and the stage's log) or on any report step not done, naming it.
- `needRoot` and the desktop-side `e.root` calls for firewall-cmd, cat, tee, systemctl and
  iptables are removed. `e.root` stays for incus through `sudo -n`, with its current message.
- `create.Preconditions` returns only the incus error. The setup wizard's "host create needs
  passwordless sudo" refusal and `machineState.Sudo` are removed.

### Rows and the terminal

The pass is a terminal handoff, as cardinal's is. sudo's prompt has no newline, so a line-by-line
replay into the session would hold it back unseen. During the handoff the owner sees the prompt
and flocc-setup's plain step lines; the Firewalld and Docker rows are drawn from the report after
sudo finishes.

### Secrets

The pass carries no secret: no answers document, no auth key. The handoff banner is
`root pass: sudo on <machine> may ask for your password`, never the argv.

### Ctrl-C

In the handoff, ssh holds the terminal in raw mode, so Ctrl-C reaches the remote sudo and
flocc-setup as SIGINT. flocc-setup's context ends, the rebind wait returns, and the report
carries the error. The desktop fails the Root pass row and create stops; the next create run
starts the pass again. A Ctrl-C at the password prompt ends sudo before any report; the error
says the pass ended without a report.

### Idempotence

A second run changes nothing: every fix comes from a check. On a machine where firewalld or Docker
runs, the pass runs on every create, so sudo asks for the password each time unless it is
passwordless or its ticket is still valid.

## Rulings

- Ruling: rows after the handoff, as cardinal — a live replay would hide sudo's prompt, and
  drawing in the session while the terminal is handed off is not possible — cost if wrong: the
  owner reads plain lines for the seconds the pass takes.
- Ruling: shared code goes in a new `rootpass` package — create cannot import cardinal — cost if
  wrong: one more small package.
- Ruling: machine messages say "this machine" and the desktop's error names the machine — no new
  flag on the phase — cost if wrong: wording only.
- Ruling: the desktop fails a pass whose report has a step not done, even with no error —
  `RunAll` checks every step before applying any, so a later step's pre-check can be stale —
  cost if wrong: a spurious failure that a re-run clears.
- Ruling: skip the pass when `CreateRootIdle` — no password prompt on a machine without
  firewalld or Docker — cost if wrong: none; the steps' own Checks start with the same test.

## Out of scope

Incus through `sudo -n`, ship's `sudo -n true` check, and anything in `internal/ui` or
`internal/dash`.
