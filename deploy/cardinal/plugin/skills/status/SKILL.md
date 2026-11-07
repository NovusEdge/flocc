---
name: status
description: Use when the owner asks what is running, what needs them, or how the executor is doing
---

Run `/opt/cardinal/plugin/bin/executor status` on every ask and answer from that run's table
only: never from an earlier table, an earlier event or memory, since PRs merge and runs move
between asks. The table asks GitHub for each finished run's PR, so a merged PR shows `merged`
in the state column and is done, not waiting; a closed one shows `closed`. "What needs me" is
every row that is `blocked` or `timed_out`, every `done` row with a PR, every hold, and the
waiting events. For one issue, add `/opt/cardinal/plugin/bin/executor run-summary KEY`. Answer
in a few lines, with no preamble.
