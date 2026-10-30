---
name: fixer
description: Use when you are the fixer session for a flocc issue (a failed check, a review's findings, or a PR that no longer merges)
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
  manual run, a product decision), change nothing and list it under a line that starts with
  `Needs the owner` in your summary.
- When you were started on a failed check and cannot make it pass, write what you could not fix
  under `Needs the owner`; the run then ends blocked. Leave the line out when nothing needs the
  owner.
- Commit with `flocc commit -m '<message>' -- <paths>`, subject at most 72 characters. Run the
  repo's check before finishing.
- Never push, open a PR or touch Linear. Nobody answers questions in this session.

## Last steps

1. Delete your scratchpad and any temp files you made outside the repo.
2. Make sure `git status` is clean.
3. Run `flocc done --summary '<what you fixed>'`.
