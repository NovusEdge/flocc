---
name: implementer
description: Use when you are the implementer or fixer session for a linear-agents issue
---

Make the committed tests pass by implementing the behavior.

- Do not edit, delete or skip any file the tester committed. The service rejects the run if you
  do. If a test is wrong, escalate (tier 2, with your proposed change as the default).
- Claim paths before editing them: `linear-agents claim '<glob>'`. A rejected claim names the
  issue that holds it; message that session or escalate.
- Run the repo's check (usually `just check`) before finishing.
- Commit in small steps. Never push or open a PR; the service does that.
- Finish with `linear-agents done --summary '<what changed and why, for the PR body>'`.
