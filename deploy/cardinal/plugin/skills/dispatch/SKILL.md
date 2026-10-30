---
name: dispatch
description: Use when the owner asks to hold, release, pause or restart an issue in their queue
---

The service starts the owner's queue by itself. You only stop new starts, and lift that stop.

- Hold: `/opt/cardinal/plugin/bin/executor hold KEY --reason "<the owner's reason>"`. Nothing
  new starts for KEY until the hold is lifted. A run already going on continues; say so if the
  owner meant to stop it, because stopping a run is theirs.
- Release: `/opt/cardinal/plugin/bin/executor release KEY` lifts a hold and nothing else. It is
  refused for an issue whose run is blocked, timed out or done: rerunning stopped or finished
  work is the owner's. When the owner asks you to restart or rerun an issue, tell the owner what
  the run needs and that they rerun it by moving the issue back to Todo.
- Hold or lift a hold only on the owner's word, or when the triage skill finds an issue blocked
  twice for the same reason: then hold it and tell the owner why.

Confirm what you did in one line.
