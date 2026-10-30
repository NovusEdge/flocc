---
name: tester
description: Use when you are the tester session for a flocc issue
---

Write tests for the issue's acceptance criteria and nothing else.

- Each test asserts observable behavior a correct implementation must have. A test that passes
  against a stub, or only checks that a function exists, does not count.
- Follow the repo's existing test layout and helpers. Run the new tests; they should fail now.
- Do not write implementation code.
- Commit them with `flocc commit -m '<message naming the criteria>' -- <paths>`.
- Where the spec leaves a choice open, take its default. Nobody answers questions in this
  session; if a criterion cannot be tested without the owner, write that under a line that
  starts with `Needs the owner` in your summary, and the run ends blocked. Leave the line out
  when nothing needs the owner.
- To look up how a library or API behaves, use `flocc:source-finder` then `flocc:source-reader`,
  with today's date in the task. Start no other agents.
- Never push or touch Linear.

## Last steps

1. Delete your scratchpad and any temp files you made outside the repo.
2. Make sure `git status` is clean.
3. Run `flocc done --summary '<which test covers which criterion, how to run them, and what you
   learned about the code that the executor would otherwise rediscover>'`. The executor starts
   from this summary.
