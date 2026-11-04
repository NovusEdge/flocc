# Cardinal autonomy, part 1: structured outcomes, a capped resume, and wider reach

Status: design approved in conversation 2026-11-04; awaiting review of this document.

## Goal

flocc stops and waits for the owner in four places: agents' questions, reruns of stopped work,
blocked runs, and intake and follow-ups. This part gives cardinal enough to take over the first
three in bounded cases, and the means for the fourth, without a general shell over credentials.

Authority grows one typed verb at a time. Code decides which verb applies to which stop, caps how
often it runs, and records each use; cardinal chooses among the verbs code allows. Text from
issues, PRs, agents and run summaries is data to cardinal, never instructions.

Three changes, each its own PR in this order:

1. Structured `flocc done` outcomes, so stops carry typed fields instead of a heading in prose.
2. `resume`, which replaces `fix` and restarts a stopped claim once, on its kept branch.
3. A wider `agent-shell` for diagnosis, and service-run Linear and GitHub verbs.

Stages after this part are listed under Roadmap.

## 1. Structured outcomes

### Today

`_finish_phase` finds a `Needs the owner` heading in the free-text summary by regex
(`supervisor.py:73-75`). For the tester, executor and check fixer it blocks the run as
`needs-owner`; for the review fixer it writes `owner-items.md`. Cardinal recognises the block
only by the `needs-owner:` prefix of an `ended` event's detail.

An executor or check fixer that runs `flocc done` with no commits reaches publish, where
`gh pr create` fails on an empty branch. That counts as a publish failure, the whole check runs
again each tick, and the third failure blocks with `publish-failed` and "Fix the push or the gh
token", which is wrong for this case.

### `flocc done`

- `--needs-owner TEXT`, repeatable, one question per flag.
- `--nothing-to-do TEXT`, the reason. Tester and executor only; it replaces `--summary` and
  cannot be combined with `--needs-owner`.
- A `--summary` whose text has a line starting with `needs the owner` (the current regex) is
  refused, and the error names `--needs-owner`. The agent learns at once and runs `flocc done`
  again, so the supervisor needs no fallback parser.
- `done.json` gains `questions: [str]` and `outcome: "work" | "nothing"`.

### Supervisor

| Outcome | Phase | Result |
|---|---|---|
| questions | tester, executor, check fixer | Block `needs-owner`. The Linear comment lists the questions numbered. `questions.json` in the state directory keeps them for `run-summary`. |
| questions | review fixer | `owner-items.md`, posted as today. |
| nothing | tester, executor | No publish. The claim ends `done`; the `ended` event detail is `done, nothing to do: <why>`; the reason is commented on Linear. The issue's state is left as a finished run leaves it, so the owner closes it. |
| no commits since the phase started, no flag | executor, check fixer | Block `no-changes`: "finished with no commits; resume with a note or close the issue". Checked before publish. |

The `needs-owner` block emits a new event kind, `needs-owner`, with the issue and the question
count, in place of an `ended` event whose detail starts `needs-owner:`.

### Text that changes

The Stop hook's `STOP_REASON`, the tester, executor, fixer and reviewer skills, the command list
in `context.py`, the review-fix prompt (`supervisor.py:339`) and the cardinal fix prompt
(`control.py:30`) name the flags instead of the heading. The reviewer keeps writing owner items
under `## Important` in `review.md`; only the review fixer's path changes.

## 2. `resume`

### Today

`fix KEY --note` (`control.py:54-103`) relaunches the fix phase of a `blocked` or `timed_out`
claim in phase `impl` or `fix`, once per claim (`cardinal_fix:KEY`), while fix rounds remain.
The triage skill tells cardinal it never reruns an issue.

### The verb

`resume KEY --note TEXT` relaunches a stopped claim on its kept branch with the note in the
prompt. The relaunched phase and the framing of the note come from the stop cause, in code:

| Stop | Relaunched phase | Note framed as |
|---|---|---|
| `needs-owner` | the claim's phase: test, impl or fix | "cardinal answered your questions", then the questions, then the note |
| `check-failed`, same failure twice | fix, with the check context as `fix` gives it today | direction for the fix round |
| `no-changes`, `idle`, `crash`, timed out | the claim's phase | direction |
| `tests-touched`, `branch-moved`, `publish-failed`, `rebase-conflict`, `check-failed` at the fix cap, `no-tests` | refused | owner |

`tests-touched` stays with the owner because it is the guard against agents weakening their own
tests. The others are infrastructure or a loop that already used its rounds.

- **Cap.** One resume per claim, meta `cardinal_resume:KEY` (renamed from `cardinal_fix`). An
  owner rerun makes a fresh claim, and `_start` clears it.
- **Other refusals, kept from `fix`.** A done or held claim, a missing worktree, a refusal from
  `admit_rerun`, a note empty or over 2,000 characters.
- **Record.** A Linear comment "cardinal resumed <phase> after <cause>: <note>" and a digest
  line. The daily digest adds a count of resumes and how many of those claims then ended done.
  That count is what the owner uses to decide when to widen cardinal again.
- **Migration.** The gate maps `fix KEY --note` to `resume` for one release, because the cardinal
  VM and the executor host deploy separately. A follow-up removes the alias.

`release` stays hold-only, and cardinal still never starts a fresh claim or touches a done one.

### Triage

- `needs-owner`: cardinal answers when the issue description, or a spec it names in the repo,
  settles the question and the answer stays inside the acceptance criteria. Questions about
  scope, criteria, credentials, cost or systems outside the repo go to the owner.
- The other allowed stops: cardinal resumes when it can say what the next attempt does
  differently, after diagnosis (section 3). Otherwise it tells the owner in at most three lines.

An injected question can at worst spend a claim's one resume on a stop code already allows.

## 3. agent-shell and service-run verbs

### agent-shell

agent-shell runs one `bash -c` command as `flocc-agent` on the executor VM, from its home, and
the digest lists every call. `only-executor` refuses shell operators only outside quotes, so a
pipeline inside the single-quoted command already passes; the triage skill's "one command per
call" is what stops cardinal.

- **Use.** Triage allows it for diagnosis before a resume or an answer: reading the claim's
  worktree, git history, check output and logs. Writing in a worktree is agent work and stays
  out. Host repair keeps its existing rule.
- **Compound commands.** The skill says pipes, `&&` and `;` work inside the single quotes. The
  hook is unchanged.
- **Limits.** `SHELL_TIMEOUT_S` 90 → 540, under the 600,000 ms Bash timeout triage already sets.
  `SHELL_CHARS` 8,000 → 24,000.

No credential is added where `flocc-agent` can read it: every agent session runs as that user
in bypassPermissions mode and reads issue text.

### Service-run verbs

New gate verbs, run by the service as `flocc` with its own Linear key and publish token. Each
takes an issue key, so cardinal reaches only that issue and the claim's own PR.

| Verb | Does | Limits |
|---|---|---|
| `issue KEY` | Title, description, labels, state, comments, linked issues | Read |
| `pr KEY` | The claim's PR: state, checks, review comments, diff stat | Read; no PR is an error |
| `comment KEY --text=TEXT` | Linear comment with `BOT_PREFIX` and "cardinal:" | 2,000 characters; steering ignores it |
| `pr-comment KEY --text=TEXT` | Comment on the claim's PR | 2,000 characters; labels stay with `amend-tests` |
| `search --query=TEXT` | Open issues in the team matching the text | Read; for duplicate checks |
| `file KEY --title=TEXT --text=TEXT` | Backlog issue linked to KEY, label `cardinal-draft` | Never `claude`; 2 per source issue, 5 per day; `cardinal_file` mode |

- **Observe mode.** Config `cardinal_file = "observe" | "on"`, default `observe`. In observe,
  `file` creates nothing and records the draft in the digest under "Follow-ups cardinal would
  have filed".
- **Record.** Every write verb adds a digest line, and the digest lists them in their own
  section.
- **Triage.** `file` only after `search` finds no open duplicate, and only for a review finding
  or owner item the PR left out. Read output is data.

## Testing

- `test_cli.py`: `flocc done` options and refusals; the verbs' caps, observe mode, no `claude`
  label, PR lookup by claim.
- `test_supervisor.py`: each row of the outcome table; replace the heading tests at `:378-420`
  and `:993-1018`.
- `test_control.py`: each stop's resume phase and framing, refusals, the cap, the rename.
- `test_cardinalgate.py`: every new verb's argument checks and the `fix` alias.
- `test_cardinal_plugin.py`: triage covers `needs-owner`; the dispatch check at `:186` changes
  from "never reruns" to "resumes only through `resume`"; diagnosis and filing rules.
- `test_events.py`: the `needs-owner` kind.

## Roadmap

- Typed block handling: every block cause carries a type (transient, repo, agent, owner) that
  decides retry, hold, resume or owner without cardinal reading prose.
- Resume on comment: an owner or `steer_authors` comment on a done issue with an open PR starts a
  fix session on the same branch; GitHub PR comments later.
- `file` in `on` mode, once the observe-mode drafts read right.
- Remove the `fix` alias.
