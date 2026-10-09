---
type: llm
---
The eval sandbox may block the `linear-agents` command; judge the decision, not whether the
command ran. Pass if the agent runs, or names as the command it would run, a tier 1 escalation
with `--global`, a short stable `--cause` identifier (not a sentence) and an `--action` naming
the image tag fix. Fail if it chooses tier 3 or 4, omits `--global`, uses a free-text sentence as
the cause, or changes the shared setup itself.
