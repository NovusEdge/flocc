---
name: tester
description: Use when you are the tester session for a linear-agents issue
---

Write tests for the issue's acceptance criteria and nothing else.

- Each test asserts observable behavior a correct implementation must have. A test that passes
  against a stub, or only checks that a function exists, does not count.
- Follow the repo's existing test layout and helpers. Run the new tests; they should fail now.
- Commit them on the issue branch with a message naming the criteria they cover.
- Claim any file you edit outside your claimed paths first: `linear-agents claim '<path>'`. Claim
  files, not trees: `**` is refused. A file the repo lists as shared is edited without a claim.
- When a peer's claim blocks your commit, finish everything else first, then message that peer
  once. If the file is still held after the peer's next turn, escalate (tier 2,
  `--cause claim-held:<path>`) instead of waiting.
- Nobody reads your session's pane. Never end a turn waiting for the owner in plain text: to wait
  on the owner, run `linear-agents escalate --tier 2 ... --waiting` (see the escalate skill).
- When auto mode refuses a command you wanted to run, do not ask for it to be run. Commit with
  `linear-agents commit`, whose git hook runs the repo's lint on the commit; the service runs the
  full check when the implementer finishes.
- Do not write implementation code.
- Write `handoff.md` in your state directory for the implementer, a fresh session that starts
  from it: which test covers which criterion, how to run them, the commit hash, and anything about
  the code you learned that the implementer would otherwise rediscover.
- Finish with `linear-agents done --summary '<which criteria each test covers>'`.
- To look up how an unfamiliar library or API behaves, use `linear-agents:source-finder` and then
  `linear-agents:source-reader`, with today's date in the task; you have two launches. Start no
  other agents.
