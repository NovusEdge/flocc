---
name: tester
description: Use when you are the tester session for a linear-agents issue
---

Write tests for the issue's acceptance criteria and nothing else.

- Each test asserts observable behavior a correct implementation must have. A test that passes
  against a stub, or only checks that a function exists, does not count.
- Follow the repo's existing test layout and helpers. Run the new tests; they should fail now.
- Commit them on the issue branch with a message naming the criteria they cover.
- Claim any path you edit outside your claimed paths first: `linear-agents claim '<glob>'`.
- Do not write implementation code.
- Write `handoff.md` in your state directory for the implementer, a fresh session that starts
  from it: which test covers which criterion, how to run them, the commit hash, and anything about
  the code you learned that the implementer would otherwise rediscover.
- Finish with `linear-agents done --summary '<which criteria each test covers>'`.
- To look up how an unfamiliar library or API behaves, use `linear-agents:source-finder` and then
  `linear-agents:source-reader`, with today's date in the task; you have two launches. Start no
  other agents.
