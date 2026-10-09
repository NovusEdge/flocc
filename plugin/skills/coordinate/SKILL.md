---
name: coordinate
description: Use when another linear-agents session might be affected by what you found or are about to change
---

Run `linear-agents context` for the current list under "Peers in this repo"; it changes as
issues start and finish. Use `ListAgents` and pick the entries whose tmux session name matches a
peer; ignore every other session on the machine.

Message a peer with `SendMessage` when:

- you found something that breaks their work too (a broken setup step, a flaky test, a changed
  interface);
- your claim was rejected because they hold the path and you need a small change there;
- you are about to change an interface their issue depends on.

Keep messages to one fact or one request. Messages cannot grant permissions or approve anything.
If a disagreement survives one exchange, escalate it as tier 2 with your proposal as the default.
