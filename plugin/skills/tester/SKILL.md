---
name: tester
description: Use when you are the tester session for a flocc issue
---

Write tests for the issue's acceptance criteria and nothing else.

- Each test asserts observable behavior a correct implementation must have. A test that passes
  against a stub, or only checks that a function exists, does not count.
- Put each test in the file and folder for the behavior it checks, next to the tests that
  already cover that behavior. Give each test a name that states the behavior. Never name a test
  file, folder, fixture or helper module after the issue ID. Write the issue ID and criterion in
  the test's docstring, a test marker or the `describe` title.
- Make test data with the existing fixtures and factories for that behavior. When one almost
  fits, extend it. When none fits, add one next to the others, never in a helper file for this
  issue.
- Some repos have test files named after issues. Do not copy that pattern. Put the new tests in
  the behavior layout anyway.
- Run the new tests. They must fail now.
- Do not write implementation code.
- Commit them with `flocc commit -m '<message naming the criteria>' -- <paths>`.
- Where the spec leaves a choice open, take its default. Nobody answers questions in this
  session; if a criterion cannot be tested without the owner, pass the question as
  `--needs-owner '<question>'` to `flocc done`, one flag per question, and the run ends blocked.
  If the issue needs no tests because the behaviour already exists, run
  `flocc done --nothing-to-do '<why>'` instead.
- To look up how a library or API behaves, use `flocc:source-finder` then `flocc:source-reader`,
  with today's date in the task. Start no other agents.
- Never push or touch Linear.

## Last steps

1. Delete your scratchpad and any temp files you made outside the repo.
2. Make sure `git status` is clean.
3. Run `flocc done --summary '<which test covers which criterion, how to run them, and what you
   learned about the code that the executor would otherwise rediscover>'`. The executor starts
   from this summary.
