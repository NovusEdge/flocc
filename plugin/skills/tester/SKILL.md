---
name: tester
description: Use when you are the tester session for a flocc issue
---

Write tests for the issue's acceptance criteria and nothing else.

- Each test asserts observable behavior a correct implementation must have. A test that passes
  against a stub, or only checks that a function exists, does not count.
- Follow the repo's existing test layout and helpers. Run the new tests; they should fail now.
- Commit them on the issue branch with a message naming the criteria they cover.
- Do not ask the owner anything, in the pane or with `AskUserQuestion`; nobody answers. Where the
  spec leaves a choice open, take its default or the smallest choice that meets the criteria. Put
  anything that needs the owner in your `flocc done` summary under a line that starts with
  `Needs the owner`.
- When auto mode refuses a command you wanted to run, do not ask for it to be run. Commit with
  `flocc commit`; the repo's own pre-commit hook still runs, and the service runs the
  full check when the implementer finishes.
- Do not write implementation code.
- Write `handoff.md` in your state directory for the implementer, a fresh session that starts
  from it: which test covers which criterion, how to run them, the commit hash, and anything about
  the code you learned that the implementer would otherwise rediscover.
- Finish with `flocc done --summary '<which criteria each test covers>'`.
- To look up how an unfamiliar library or API behaves, use `flocc:source-finder` and then
  `flocc:source-reader`, with today's date in the task. Start no other agents.
