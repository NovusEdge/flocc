from pathlib import Path

from linear_agents.config import Config
from linear_agents.ledger import Claim
from linear_agents.linear import Issue

_COMMANDS = """## Commands

- `linear-agents claim '<glob>'` before editing files outside your claimed paths.
- `linear-agents context` to see this text again with the current peer list.
- `linear-agents escalate ...` when you need the owner; see the escalate skill.
- `linear-agents done --summary '<one paragraph>'` when this phase is finished.
- `linear-agents handoff-ready` after writing handoff.md when asked to hand off.
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
            " implementer, then run linear-agents done."
        )
    elif phase == "impl":
        body = (
            f"You are the implementer for {issue_key}. Follow the implementer skill: make the"
            " committed tests pass without changing them, then run linear-agents done."
        )
    else:
        body = (
            f"You are fixing {issue_key} after the service's check failed. Follow the"
            " implementer skill, fix the failure below, then run linear-agents done."
        )
    return f"{body}\n\n{extra}".strip()
