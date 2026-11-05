---
name: fixer
description: Use when you are the fixer session for a flocc issue (a failed check, a review's findings, comments on an open PR, or a PR that no longer merges)
---

Fix what the text you were started with reports, on the existing branch, and nothing else.

- The failure is in your prompt and in `check-failure.md` in your state directory; the full check
  output, when there is one, is in `check-output.log` there. A review's findings are in your
  prompt. Read `last-summary.md` for what the previous step did.
- Fix inline. For a fix bigger than a few files you may start one `flocc:task-implementer` with a
  brief in `briefs/fix.md` in your state directory and one `flocc:fix-rereviewer` on its diff.
- Change a tester's file only when its test is wrong, and record it with
  `flocc tests-amended <path>... --reason "<why>"`. Never delete or skip a test.
- When the fix answers a review, fix what is agent work. For a finding that needs the owner (a
  manual run, a product decision), change nothing and pass it as `--needs-owner '<item>'` to
  `flocc done`; it goes into the final review comment.
- When you were started on comments posted after the PR opened, do what they ask that is agent
  work. For what needs the owner, change nothing and pass it as `--needs-owner '<item>'`; it
  goes into the issue comment. When they ask for no change to the code (a question, a thanks),
  run `flocc done --nothing-to-do '<your answer, for the owner>'` instead of a summary.
- When you were started on a failed check and cannot make it pass, pass what you could not fix as
  `--needs-owner '<what and why>'`; the run then ends blocked.
- Start on the fix: the failure and the findings are already recorded, so do not run the full
  check first to see them again.
- Commit with `flocc commit -m '<message>' -- <paths>`, subject at most 72 characters. Before
  finishing, rerun the tests that failed or that your fix touches, and the repo's lint and format
  checks. Do not run the full check yourself, whatever the repo's own instructions say about
  running it before a push: `flocc done` runs it, and flocc pushes.
- Never push, open a PR or touch Linear. Nobody answers questions in this session.

## Last steps

1. Delete your scratchpad and any temp files you made outside the repo.
2. Make sure `git status` is clean.
3. Run `flocc done --summary '<what you fixed>'`.
