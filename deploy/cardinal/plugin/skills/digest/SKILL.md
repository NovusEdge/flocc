---
name: digest
description: Use when a daily event arrives from the executor VM, or the owner asks for the digest
---

1. Run `/opt/cardinal/plugin/bin/executor cleanup`. Note what it removed and what it kept, with
   the reasons.
2. Run `/opt/cardinal/plugin/bin/executor digest-data`. Its last line names the `--done N` to
   run once the digest is sent.
3. Write the owner one message of at most 15 lines: PRs ready for review, with links; runs that
   stopped and what each needs from the owner; holds; service problems; events needing
   attention; worktrees cleanup kept, with why. Leave out anything that needs nothing from the
   owner. On a day with nothing, write one line saying so.
4. Run `/opt/cardinal/plugin/bin/executor digest-data --done N` with the N from step 2. Then
   acknowledge the `daily` event and every event listed as needing attention, in one
   `/opt/cardinal/plugin/bin/executor ack ID ...` call.
