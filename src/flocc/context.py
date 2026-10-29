from pathlib import Path

from flocc.config import Config
from flocc.ledger import Claim
from flocc.linear import Issue

_COMMANDS = """## Execution only

The issue and the specs it names are the approved design. Do not brainstorm, write a plan for
approval, or ask for design sign-off: nobody is at this session to approve it. Where the spec
leaves a choice open, take its default; what needs the owner goes in your `flocc done` summary
under a line that starts with "Needs the owner".

## Commands

- `flocc claim '<glob>'` before editing files outside your claimed paths.
- `flocc context` to see this text again with the current peer list.
- `flocc done --summary '<one paragraph>'` when this phase is finished.
- `flocc review-done` after writing review.md, in the review phase only.
- `flocc handoff-ready` after writing handoff.md when asked to hand off.
- `flocc commit -m '<message>' -- <paths>` to commit only those paths.
"""


def state_dir(cfg: Config, key: str) -> Path:
    return cfg.state_root / key


def write_context(cfg: Config, issue: Issue, claim: Claim) -> None:
    d = state_dir(cfg, issue.key)
    d.mkdir(parents=True, exist_ok=True)
    paths = ", ".join(claim.paths) if claim.paths else "none yet"
    (d / "context.md").write_text(
        f"# {issue.key}: {issue.title}\n\n{issue.url}\n\n{issue.description}\n\n"
        f"## Claim\n\nBranch `{claim.branch}`. Claimed paths when the issue started: {paths}.\n\n"
        f"Your state directory is {d}; write handoff.md there.\n\n{_COMMANDS}"
    )


def phase_prompt(phase: str, issue_key: str, extra: str = "") -> str:
    if phase == "test":
        body = (
            f"You are the tester for {issue_key}. Follow the tester skill: write tests that"
            " pass only on correct behavior, commit them, write handoff.md for the"
            " implementer, then run flocc done."
        )
    elif phase == "impl":
        body = (
            f"You are the implementer for {issue_key}. Follow the implementer skill: make the"
            " committed tests pass. Fix a test only when it contradicts the acceptance criteria"
            " or the real API, and record it with flocc tests-amended; never delete or skip a"
            " test. Then run flocc done."
        )
    elif phase == "review":
        body = (
            f"You are the reviewer for {issue_key}. Follow the reviewer skill: read"
            " review-input.md in your state directory, write review.md, then run"
            " flocc review-done. Change nothing in the repo."
        )
    else:
        body = (
            f"You are fixing {issue_key} after the service's check failed, the"
            " reviewer asked for changes or the PR conflicted with its base. Follow the"
            " implementer skill's Fix phase section, fix what the text below reports, then run"
            " flocc done."
        )
    return f"{body}\n\n{extra}".strip()
