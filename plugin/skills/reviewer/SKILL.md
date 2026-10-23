---
name: reviewer
description: Use when you are the review session for a flocc issue whose PR is already open
---

Review the branch the implementer pushed. You change nothing: do not edit files, commit or push.

- Read `review-input.md` in your state directory. It holds the PR link, the issue description with
  its acceptance criteria, and the path of `pr.diff`, the full `git diff <base>...HEAD`.
- Check the diff against each acceptance criterion, one by one. A criterion with no code or test
  behind it is a finding.
- If `review-input.md` has an "Amended tests" section, judge each amendment and its reason against
  the acceptance criteria. An amendment that weakens what the test asserts is a finding, and the
  verdict is changes requested. The service catches only removed or skipped test names; read the
  amendment's diff yourself for dropped subtests, parametrize cases and tests commented out.
- Read the repo's `AGENTS.md` and `CLAUDE.md` if present, and check the diff against their
  conventions. Open the surrounding code when the diff alone does not show whether a change is safe.
- If `review-input.md` has an "Your earlier review" section, a fix round followed it. Judge the new diff against
  that review's findings: say which are fixed and which remain. The fixer was told to leave
  findings that need the owner alone, so those are not failures; list them once under `## Minor`.
- Run the repo's check only if you doubt it passes; the service already ran it before the push.
- Findings go only in `review.md`. Do not run `flocc escalate`; the PR is already open and
  the owner reads the review.
- Do not raise style points a linter would catch or changes outside the diff.

Write `review.md` in your state directory, with these headings, omitting an empty one:

- `## Critical`: wrong behavior, data loss, security, an unmet acceptance criterion.
- `## Important`: likely bugs, missing tests for stated behavior, broken repo conventions.
- `## Minor`: small improvements the author may skip.

Give each finding as `path:line` followed by what is wrong and what to do. Quote nothing the diff
does not contain. Close the file with exactly one line, `Verdict: approve` or
`Verdict: changes requested`. Any Critical finding means changes requested.

Then run `flocc review-done`. The service posts the file as a PR comment; the owner decides
whether to merge.
