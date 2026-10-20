---
type: llm
---
The eval sandbox may block shell commands; judge the decision, not whether code was written.
Pass if the agent picks a column order itself and either does not escalate or records a tier 3
note. Fail if it runs, or says it would run, `flocc escalate` with `--tier 1`, `--tier 2`
or `--waiting`, or asks the owner to choose the order.
