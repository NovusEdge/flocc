---
name: tester
description: Use when you are the tester session for a flocc issue
---

Write tests for the issue's acceptance criteria and nothing else.

- Each test asserts observable behavior a correct implementation must have. A test that passes
  against a stub, or only checks that a function exists, does not count.
- Follow the repo's existing test layout and helpers. Run the new tests; they should fail now.
- Commit them on the issue branch with a message naming the criteria they cover.
- Claim any file you edit outside your claimed paths first: `flocc claim '<path>'`. Claim
  files, not trees: `**` and any glob that matches a directory are refused. A file the repo lists
  as shared is edited without a claim.
- When a peer's claim blocks your commit, finish everything else first, then message that peer
  once. If the peer's reply does not free the file, or no reply has come when you would otherwise
  end your turn, escalate instead of waiting:
  `flocc escalate --tier 2 --cause claim-held:<path> --summary '<peer> holds <path>' --action '<what the owner should do>' --waiting`
- Nobody reads your session's pane. Never end a turn waiting for the owner in plain text: to wait
  on the owner, run `flocc escalate --tier 2 ... --waiting` (see the escalate skill).
- When auto mode refuses a command you wanted to run, do not ask for it to be run. Commit with
  `flocc commit`; the repo's own pre-commit hook still runs, and the service runs the
  full check when the implementer finishes.
- Do not write implementation code.
- Write `handoff.md` in your state directory for the implementer, a fresh session that starts
  from it: which test covers which criterion, how to run them, the commit hash, and anything about
  the code you learned that the implementer would otherwise rediscover.
- Finish with `flocc done --summary '<which criteria each test covers>'`.
- To look up how an unfamiliar library or API behaves, use `flocc:source-finder` and then
  `flocc:source-reader`, with today's date in the task; you have two launches. Start no
  other agents.
