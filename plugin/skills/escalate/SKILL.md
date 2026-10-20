---
name: escalate
description: Use when you are unsure whether to involve the owner, or need a decision you cannot make alone
---

Most uncertainty is yours to resolve. Escalate only by this table.

| Tier | When | Command |
|---|---|---|
| 1 | Irreversible harm is about to happen (data loss, a secret about to be committed), or something blocks every issue (auth, ./setup broken for all) | `flocc escalate --tier 1 --cause <stable-id> --summary '...' --action '<one thing to do>'` (add `--global` when it is not specific to this issue) |
| 2 | A decision blocks you and you cannot reverse it cheaply later, or a peer conflict you cannot settle by messaging | Reversible with a sensible default: `--tier 2 ... --default '<what you will do>' --deadline-min 120`, then keep working on the default. Irreversible, tester sessions: `--tier 2 ... --waiting`, then stop and wait for a reply. Irreversible, implementer and fix sessions: ask with `AskUserQuestion` (see "Asking the owner directly" below). A held claim (`--cause claim-held:<path>`) uses `--waiting` in every session type. |
| 3 | Something the owner should read later: a reversible choice you made, a limitation | `--tier 3 --cause <id> --summary '...'` |
| 4 | Anything else | Do not escalate |

## Asking the owner directly

For a decision you cannot go on without (what tier 2 `--waiting` was for), ask with
`AskUserQuestion`: the owner gets a push in the Claude app and answers there. Only implementer and
fix sessions are connected to the app; a tester escalates with `--tier 2 --waiting` instead. First record it:
`flocc escalate --tier 3 --cause asked:<topic> --summary '<the question>'`. Give two to
four options with your recommendation first. If nobody answers within two hours the service
escalates it again. For a reversible choice, do not ask: escalate with `--default` and carry on.

Rules:

- `--cause` is a stable identifier for the underlying problem (`setup-missing-redis`, not a
  sentence), so several sessions hitting the same problem produce one alert.
- `--action` names one concrete thing the owner can do.
- Do not escalate a question you can answer by reading the code, the issue or the repo docs.
- Do not escalate the same cause twice.
