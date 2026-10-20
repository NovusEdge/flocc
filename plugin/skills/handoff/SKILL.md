---
name: handoff
description: Use when asked to hand off, or before your context runs out
---

Write `handoff.md` in your state directory (shown in your context) so a fresh session can continue
without you. Include:

- the phase and what is done, with commit hashes;
- what you were doing when you stopped and the next concrete step;
- decisions you made and why, and anything you ruled out;
- open questions and escalations you raised.

If you started agents, wait until they have all finished before handing off; a handoff kills the
session and every agent it is running. In `handoff.md`, also record `plan.md`'s path, the waves
done, and the wave in progress.

Commit any work in progress with `flocc commit -m '<message>' -- <paths>`. Then run
`flocc handoff-ready` and stop.
