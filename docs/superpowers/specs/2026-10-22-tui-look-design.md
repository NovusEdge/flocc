# One look for the desktop TUI, and a dashboard worth opening

Follows `2026-10-22-cli-tui-design.md` (NovusEdge/flocc#49), whose event stream and three renderers
stay as they are. This spec changes what the full-screen and inline renderers draw, and rebuilds
the dashboard on top of a shared theme. Stoat's TUI (`~/Projects/stoat/internal/tui`) is the
reference for the look, not for the layout.

## Problem

Bare `flocc` at 140×40 fills ten rows: one line per host, an issue table, and a box that for most
issues says "no activity reported". Nothing opens; the only actions hand the terminal to another
process. The colours are the terminal's named ones, so the dashboard, the setup wizard and inline
output each look different, and none look finished.

## Goal

Every desktop screen shares one palette, one pane style and one key bar. The dashboard becomes a
place to watch and act on the fleet: an issue or a host opens into a detail screen with live output,
and logs scroll inside the dashboard.

## Decisions

| | Decision |
|---|---|
| Layout | Full screen, htop style: panes fill the terminal less a margin of 1 row top and bottom and 2 columns each side. No centred column. |
| Theme | One package, `internal/ui/theme`, used by the dashboard, the full-screen renderer and the inline renderer. Plain output stays uncoloured. |
| Palette | Six semantic colours (accent, up, down, warn, err, dim), with dark and light values; the light ones meet 4.5:1 contrast. Stoat's values are the starting point. |
| Light or dark | The full-screen programs ask the terminal's background (`tea.RequestBackgroundColor`) and redraw when it answers; dark until then. Inline asks once before its first draw. |
| Panes | One helper: rounded border in accent, padding 0×1, an optional bold title set into the top border, and a right-aligned dim hint in it. |
| Glyphs | `❯` selection, `●` up, `○` down, `✗` failed, `✓` done, `⠋` MiniDot spinner for running work, `·` separator. |
| Key bar | Built from `key.Binding` values with bubbles `help`; keys that do nothing on the current row are dimmed; `?` shows every key. It wraps, never truncates. |
| Small terminals | Below 60×20 the screen says the terminal is too small and how big it is. Side-by-side panes stack below 100 columns. |
| Messages | Errors from an action show as a toast at the top right for 4 s; each toast carries a generation so an older timer cannot clear a newer toast. Prompts and confirmations use a fixed status line above the key bar (`release NF-240? y/N`), so nothing below it moves. |

Rejected: a centred 72-column frame like stoat's (the owner wants the screen used); a tab bar
(three screens are reached by drilling in, which needs no tabs); a mouse (no request for it).

## Dashboard

### Fleet screen

```
  ╭─ hosts ─────────────────────────────────────╮ ╭─ cardinal ───────────────╮
  │ ❯ ● flocc-nimble  behind · ship to update   │ │ ● active · up 10h        │
  │     tick 2m · 0/2 · 5h 0% · wk 2% · 83 GiB  │ │   last turn 8m · login ok│
  ╰─────────────────────────────────────────────╯ ╰──────────────────────────╯
  ╭─ issues ─────────────────────────────────────────────── 1 of 2 · / filter ─╮
  │ ❯ ✗ NF-240  Fix the poller tick         fix · blocked 24h  ctx 7%  nimble  │
  │   ⠋ NF-241  Retry a failed publish      review · 12m       ctx 41% nimble  │
  │                                                                            │
  ╰────────────────────────────────────────────────────────────────────────────╯
  ╭─ activity · NF-240 ────────────────────────────────────────────────────────╮
  │ 08:12 review requested changes                                             │
  ╰────────────────────────────────────────────────────────────────────────────╯
  status line
  ↑↓ select · tab hosts/issues · → open · a attach · r release · / filter · ? keys
```

- The hosts and cardinal panes size to their rows; the issues pane takes the rest; the activity
  pane shows the selected issue's last activity lines and shrinks first when height runs short.
- `tab` moves focus between the hosts and issues panes; `→`/`enter` opens the focused row.
- `/` filters issues by key, title, host or state; `esc` clears it. An empty fleet says "no hosts
  yet, run flocc setup"; an empty filter says "no issue matches, esc clears".
- The header line shows a spinner while a poll is out and "refreshed 3s ago" otherwise. A host
  whose poll failed keeps its last report, drawn dim with the error on its row.

### Issue screen

```
  ╭─ NF-240 · Fix the poller tick ─────────────────────────── o open in Linear ─╮
  │ phase    test ✓ → impl ✓ → review ✓ → fix ✗                                 │
  │ state    blocked 24h                                                        │
  │ session  none · context 7% · launches 0/2                                   │
  │ host     flocc-nimble · started 3d ago · updated 24h ago                    │
  ╰─────────────────────────────────────────────────────────────────────────────╯
  ╭─ session ───────────────────────────────────────────────── L full screen ──╮
  │ last lines of the live session, or of the service log naming the issue     │
  ╰─────────────────────────────────────────────────────────────────────────────╯
  a attach · r release · o open · L pager · esc back
```

- The phase line lists the host's phases in order (`test`, `impl`, `review`, `fix`, read from
  `src/flocc`), marks the earlier ones done and the current one with its state's glyph.
- The output pane shows the live tmux session's pane while one is alive, else the service log
  lines that name the issue. It refreshes every 2 s while the screen is open, with ticks that
  carry a generation so a stale answer is dropped. `L` opens it in the pager.

### Host screen

Opened from the hosts pane. One pane of facts (version, mode, last tick, usage, disk, Claude
login, token expiry, session cap), one pane with the host's `doctor --json` results as rows, and
the output pane showing the service log tail. `d` runs doctor again, `s` ships to the host (dimmed,
with the reason in the key bar, when the dashboard was not started inside the flocc checkout), `l`
opens the log in the pager.

### Cardinal screen

Opened from the cardinal pane: the status table `flocc cardinal status` draws, and `c` attaches
read-only (the handoff and its Ctrl-D warning stay as they are).

### Pager

Full screen inside the dashboard: `j`/`k`, `pgup`/`pgdn`, `g`/`G`, `/` search, `f` follows new
lines, `esc` back. Lines are sanitised as every host string is today.

### Data

| Field | Source |
|---|---|
| Issue titles | The desktop's stored Linear key: one query per poll for the claimed keys, cached by key. Without the key or on error the screens show keys alone and one note says why. |
| Session output | New host command `flocc peek <issue> [-n N]`: the session's tmux pane, last N lines, sanitised, no escape codes. Exits 3 when no session is alive. |
| Issue log | New `--issue <key>` on the host's `logs`: only lines that name the issue. |
| Doctor | The existing `doctor --json`. |
| Everything else | The existing `status --json`; no schema change beyond what peek and logs add. |

Desktop code that calls `peek` or `logs --issue` on a host without them (a host not shipped yet)
shows "ship to see session output" in the pane instead of an error.

## Setup and other full-screen commands

The section rail and the live pane become two panes side by side (rail 24 columns, live pane the
rest), stacked below 100 columns. Rows use the shared glyphs; the running row's spinner is
MiniDot; details are dim. The prompt modal is a pane in accent over the frame, as now, with the
key bar inside it. The key bar at the bottom replaces the current hint text.

## Inline output

Rows keep their shape and take the theme's glyphs and colours: `✓` up, `✗` err, `⠋` accent while
running, details dim, todos in warn. Nothing else about inline changes.

## Testing

- The theme: both palettes, contrast of each light colour against white, and that `NO_COLOR`
  and plain mode draw no escape codes.
- Frames at 59×19 (too small), 80×24 (stacked) and 140×40 (side by side), compared as text with
  escapes stripped, for the fleet, issue and host screens and for setup.
- The dashboard's keys: tab focus, open and back, filter, release's confirm line, pager follow,
  and a dimmed key doing nothing.
- Ticks and toasts: a stale tick or an older toast timer changes nothing.
- `peek` and `logs --issue` on the host, with a fake tmux and a fixture log.
- The desktop against a host without `peek`: the pane says to ship.

## Out of scope

Mouse support, themes beyond light and dark, and any change to plain output or the event stream.
