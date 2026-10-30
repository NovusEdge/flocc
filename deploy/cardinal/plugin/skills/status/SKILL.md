---
name: status
description: Use when the owner asks what is running, what needs them, or how the executor is doing
---

Run `/opt/cardinal/plugin/bin/executor status` and answer from its table only. "What needs me"
is every row that is `blocked` or `timed_out`, every `done` row with a PR, every hold, and the
waiting events. For one issue, add `/opt/cardinal/plugin/bin/executor run-summary KEY`. Answer
in a few lines, with no preamble.
