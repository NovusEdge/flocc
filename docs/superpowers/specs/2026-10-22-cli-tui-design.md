# Desktop CLI redesign: one event session, three renderers

Issue: NovusEdge/flocc#49. The hands-off tailnet work, part 4 of the same redesign, has its own spec:
`2026-10-22-tailnet-hands-off-design.md`.

## Problem

The first live `flocc setup` (0.8.0) printed nothing while it read a host, checked keys or scanned
for old versions, and every row waited for the summary at the end. The screen sat blank long
enough to look hung. The same gap runs through the rest of the CLI:

- Silent stretches: ship's staging (`go build`, `git bundle`, `scp`), cardinal's builds and passes,
  `host create`'s tailnet wait and snapshot, the `-H all` fetches, `pickHost`'s scan, and the HTTP
  key checks show nothing while they run.
- Four row formats: setup's end summary, cardinal's uncoloured `fmt` blocks, `Summary.Print`, and
  `ui.Progress` each draw ✓/✗ rows their own way.
- Host output arrives as raw bytes. `flocc-setup` draws its own live line from `FLOCC_COLOR` and
  `COLUMNS` passed over ssh, and two marker lines (`flocc-summary {json}`, `flocc-cardinal {json}`)
  are cut out and redrawn on the desktop.

## Goal

Every desktop command shows what it is doing while it does it, in one visual language, and every
flow works in three renderers: full-screen, inline and plain.

Success: no pause longer than about a second without something moving; each row appears when it
ends; the end of a run lists only what is left for the owner; every flow can be completed without
full-screen.

## Decisions

| | Decision |
|---|---|
| Model | Every command writes through one `ui.Session` of typed events. Three renderers draw it. |
| Default | Full-screen on a terminal for `setup`, `host create`, `ship`, `login`, `cardinal setup`, `cardinal login`; bare `flocc` opens the dashboard full-screen. Commands that finish in a second or two (`keys check`, `cardinal restart`, one-shot `status`, `host run`) stay inline. |
| Opt-out | `--inline` or `ui = "inline"` in the desktop config; `--plain`, a non-terminal, `NO_COLOR` or `TERM=dumb` gives plain. |
| Wizard | Section rail left; current section's rows, live step and command tail right; overall bar at the bottom. |
| Prompts | A centred modal over the dimmed screen in full-screen; in place in inline; numbered or typed in plain. |
| Dashboard | Stacked: host strip, one issue table across hosts, the selected issue's recent activity. |
| Delivery | Four parts, one PR each, in order (below). |

Rejected: a full-screen shell that captures and parses today's output (breaks whenever a line's
wording changes; prompts inside a captured pane); separate Bubble Tea apps for the dashboard and
wizard with `ui.Progress` elsewhere (the full-screen and plain setups would be two codebases that
drift); inline by default (the owner wants the full-screen look).

## Parts

| Part | Delivers | Depends on |
|---|---|---|
| 1 | `ui.Session`, the event types, the plain and inline renderers; every work command moved onto it; `ui.Progress`, cardinal's `fmt` blocks, `Summary.Print` and setup's end-only rows removed | — |
| 2 | Host event protocol: `flocc-setup --events`, desktop replay; `status --json` activity and cardinal fields; `-H all doctor/logs` drawn by the desktop | 1 |
| 3 | Full-screen renderer: wizard layout, modal prompts, the dashboard, full-screen as default | 1, 2 |
| 4 | Hands-off tailnet (own spec) | 1 |

Part 1 alone fixes the blank screen.

## The session

```go
type Session interface {
	Group(title string)                     // section header: "Keys", "Host flocc-nimble", "Root pass"
	Step(name string) *Step                 // a row, live until it ends
	Note(title, body string)
	Todo(text string)                       // collected into the "still yours to do" box
	Confirm(q Question) (bool, error)       // prompts block the caller until answered
	Ask(q Question) (string, error)
	Secret(q Question) (string, error)
	Choose(q Question, options []string) (int, error)
	Form(f *huh.Form) error                 // the repo picker and other multi-field forms
	Handoff(why string, c host.Cmd) error   // the terminal goes to a child
	Table(t Table)                          // status, doctor and fleet tables
	Close() int                             // prints the record; returns the exit code
}

// Safe from any goroutine; a step ends once.
func (s *Step) Detail(text string)          // what it is doing now
func (s *Step) Progress(n, total int)
func (s *Step) Line(text string)            // a command or output line for the tail
func (s *Step) Done(detail string)
func (s *Step) Pending(detail string)       // did what it could; the owner has the rest
func (s *Step) Skip(detail string)
func (s *Step) Fail(err error)
```

`Question` carries the title, the facts the answer rests on, a help line and the default. The
facts are shown with the question in every renderer, because the full-screen modal covers the rows
they came from.

The session replaces `ui.UI`, `ui.Progress`, `cli.prompter`, `keys.Huh` and `huhPrompt`. Every
command takes a `Session` in place of its `Out`, `prompt` and progress fields.

### Data flow

A command calls the session; the session sends events on one channel; the renderer draws them.
Full-screen and inline renderers are one Bubble Tea v2 program each. The plain renderer is a line
writer and starts no Bubble Tea program, so a pipe, `NO_COLOR` or a dumb terminal never gets an
escape code.

A prompt blocks the calling goroutine until the renderer returns the answer: an embedded
`huh.Form` model in Bubble Tea modes (never `Form.Run` inside a running program, which starts a
second one), huh's accessible mode in plain.

### Handoffs

`host.Exec.Interactive` calls `Session.Handoff`, so nothing outside the session writes to the
terminal. Bubble Tea modes run the child with `tea.ExecProcess`, which releases the terminal and
restores it after. Plain runs it directly.

Before the child starts, the renderer prints one line on the normal screen naming what comes next:

```
── root pass: sudo on zer0-day-nimble asks for your password ──
```

There is no keypress to continue. Ctrl-C during a handoff goes to the child, as today. When the
child exits the step's row shows its result.

Handoffs today: ship's `ssh -T` stream and its `--interactive` `ssh -t`; the login `ssh -t` and
`OpenURL`'s QR code; `host create`'s first `Interactive` and its tailnet step; `ship.Connect`'s
attached mode; cardinal's root pass, purge pass, login and attach; `host run` and every forwarded
command.

`flocc cardinal login` and `cardinal attach --write` print, before attaching:

```
Detach with Ctrl-b d. Ctrl-D twice ends cardinal's session; the service starts a fresh one.
```

The read-only attach (the default, and the dashboard's `c`) cannot send Ctrl-D. A double Ctrl-D in
a writable attach on 2026-10-22 exited claude, which moved the conversation into a Claude
background daemon outside `cardinal.service`; the service restarted a fresh session 30s later.

## Renderers

### Plain

Rows as they end, sections as headings, no escape codes, no carriage returns. The glyphs stay
(✓ ✗ • · →). Prompts are numbered or typed. This is today's non-animated `ui.Progress` output,
extended to every row.

```
Old versions
  ✓ zer0-day-nimble    none left
  ✓ la-nimble          none left
Cardinal on zer0-day-nimble
  user cardinal exists (uid 1002), lingering,
  claude running in tmux; no saved file names it
Adopt the existing cardinal user?
Its running session ends; the service starts a fresh one.
  1. Yes, adopt
  2. No, ask next run
Choose [1-2]:
```

### Inline

The live area sits below scrollback: the running steps (several at once for parallel work), an
overall bar where the command knows its total, and a prompt when one is open. Ended rows go above
it with `tea.Println`, so they stay in scrollback, as in Bubble Tea's package-manager example.

### Full-screen

Alternate screen. `tea.Println` prints nothing while it is active, so the record is printed on
exit (below).

Setup wizard:

```
 flocc setup                                              v0.9.0 · 2:14
 ──────────────────────┬─────────────────────────────────────────────────────
  ✓ Checkout           │ Old versions
  ✓ Keys          5/5  │   ✓ zer0-day-nimble   none left
  ✓ Tailnet       1/1  │   ⠹ la-nimble         scanning users            0:04
  ✓ Hosts         1/1  │
  ⠹ Old versions  1/2  │   $ ssh la-nimble getent passwd
  · Cardinal           │   $ ssh la-nimble loginctl list-users
  · Logins             │     linear-agents  lingering
                       │
  • 1 left for you     │
 ──────────────────────┴─────────────────────────────────────────────────────
  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  4/7   l log · ? keys · q quit
```

The rail lists the command's groups; `l` opens the whole log of the selected step. Commands other
than setup use the same frame with their own groups (`host create`: VM, Firewall, Docker, Tailnet,
Ship, Login).

Prompts are a modal composited over the dimmed frame with Lip Gloss v2 layers
(`lipgloss.NewLayer`, `NewCompositor`, in v2.0.6):

```
 ✓ Keys    ╭──────────────────────────────────────────────────╮
 ✓ Tailnet │ Adopt the existing cardinal user?                │
 ✓ Hosts   │ uid 1002, lingering, claude running in tmux.     │
 ✓ Old ver │ Its running session ends.                        │
 ? Cardina │                                                  │
 · Logins  │   ▸ Yes, adopt    No, ask next run               │
           ╰──────────────────────────────────────────────────╯
```

Below 80×20, at start or after a resize, the full-screen renderer hands over to inline for the
rest of the run.

### The record

On exit, full-screen prints to the normal screen a compact copy of every row, then the "still yours
to do" box and the log path. Inline and plain printed the rows as they ended, so they print only
the box. A panic restores the terminal before the record.

## The dashboard

Bare `flocc` on a terminal; `flocc status --watch` opens the same view. `--plain` or a pipe prints
the fleet table once, as `-H all status` does now.

```
 flocc  2 hosts · 3 issues · cardinal ●                       refreshed 3s ago
 ─────────────────────────────────────────────────────────────────────────────
  ● flocc-nimble  v0.8.0  tick 2m ago   5h 12%  wk 31%   disk 82 GiB   cap 2
  ● flocc-2       v0.8.0  tick 40s ago  5h  3%  wk  9%   disk 40 GiB   cap 2
 ─────────────────────────────────────────────────────────────────────────────
   ISSUE    HOST          PHASE     STATE     CTX   LAUNCHES  UPDATED
 ▸ NF-240   flocc-nimble  fix       blocked   7%    0/2       14m
   NF-388   flocc-nimble  review    running   41%   1/2       now
   NF-402   flocc-2       test      running   18%   1/2       1m
 ─────────────────────────────────────────────────────────────────────────────
  NF-240  blocked: waiting on an answer in Linear
  14:02 ran just check: 2 failures in ship_test.go
  14:05 asked in Linear: keep the old summary line?
 ─────────────────────────────────────────────────────────────────────────────
  ↑↓ select · a attach · l logs · r release · c cardinal · ? keys · q quit
```

Every 10s it runs `status --json` on each host in parallel over the existing ssh control
connections. `a`, `l`, `r` and `c` hand off to `attach`, `logs`, `release` and the read-only
`cardinal attach` for the selected row, then return to the dashboard. `r` asks first in a modal,
since a release restarts work.

A host that cannot be reached shows a red dot and its last error in the strip; the table keeps its
last known rows, marked stale.

## Host event protocol (part 2)

`flocc-setup --events` writes every step, row, note and to-do as one line:

```
flocc-event {"v":1,"type":"step","id":"s7","name":"Packages","state":"running","detail":"apt-get install …"}
```

The event types are the session's: `group`, `step` (with `state` running, done, pending, skipped,
failed), `line`, `note`, `todo`. `ship` decodes the lines and replays them into the desktop's
session under a `Host <name>` group. This replaces the raw byte stream, `flocc-summary`,
`flocc-cardinal`, and passing `FLOCC_COLOR` and `COLUMNS`: the host no longer draws. `ui.Scripted`
answers from the answers file as now and writes events in place of lines.

A line that is not an event (apt output, a sudo warning) becomes a `line` of the running step, so it
reaches the tail and the log. If the stream ends inside a step, that step fails with the last 20
such lines.

`ship` builds `flocc-setup` from the desktop's checkout and copies it, so both ends of this stream
are one version. The Python CLI is not: `status --json` gains `"v"`, and the dashboard shows a host
that is behind as `v0.7.0, ship to update` with only the columns it reports.

New in `status --json`: each issue's last five activity lines from its journal entries, and
cardinal's status (the same call `flocc cardinal status` makes). `-H all doctor` and `-H all logs`
are drawn by the desktop as one group per host in place of `== host ==` blocks; `doctor` gets a
`--json` report if it lacks one.

## Commands

| Command | Today | After |
|---|---|---|
| `setup` | rows only in the end summary | rows live, grouped by section; full-screen wizard |
| `ship`, `login` | silent staging; raw host bytes; `Summary.Print` | staging as steps; host events replayed; summary rows are ordinary rows |
| `host create` | `ui.Progress` NameWidth 10; silent tailnet wait | session steps; tailnet wait shows its countdown |
| `host add`, `host import` | silent HTTP and ssh calls | each call a step |
| `keys`, `keys check`, `keys rotate` | silent checks; `FailLine`s | a step per key and per host pushed |
| `cardinal setup/remove/restart` | uncoloured `fmt` blocks; silent builds and passes | groups per pass; builds as steps |
| `cardinal status`, `-H all status/doctor/logs` | `%-12s` rows, lipgloss table, text blocks | session tables |
| forwarded host commands | raw ssh | unchanged: a handoff |

## Testing

- A recording session captures each command's event sequence; command tests assert on it. This
  replaces most of today's ~100 exact-output checks and the pinned column widths and glyphs.
- The plain renderer has golden-text tests.
- The inline and full-screen models have unit tests on `Update` and `View` at fixed sizes,
  including the modal, the resize hand-over and a handoff message.
- The event codec round-trips every type; the replay test feeds a recorded `flocc-setup --events`
  stream with stray lines and a cut-off.
- Before part 3 merges: a manual run of a real `ssh -t` sudo prompt and a browser login through
  `tea.ExecProcess`, which no Charm example covers.

## Open items

- Library versions: the pins are bubbletea v2.0.2, bubbles v2.0.0, lipgloss v2.0.6, huh v2.0.3;
  pkg.go.dev shows bubbletea v2.1.0 and bubbles v2.2.1. Part 1's plan decides whether to upgrade.
- Where an issue's activity lines come from on the host (its journal entries, the session
  transcript, or the escalation log) is settled in part 2's plan by reading what each holds.
