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

Commit any work in progress first. Then run `linear-agents handoff-ready` and stop.
