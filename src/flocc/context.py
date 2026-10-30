from pathlib import Path

from flocc.config import Config
from flocc.ledger import Claim
from flocc.linear import Issue
from flocc.sequence import ARCHETYPE

_COMMANDS = """## Execution only

The issue and the specs it names are the approved design. Do not brainstorm, write a plan for
approval, or ask for design sign-off: nobody is at this session to approve it. Where the spec
leaves a choice open, take its default; what needs the owner goes in your `flocc done` summary
under a line that starts with "Needs the owner".

## Commands

- `flocc context` to see this text again.
- `flocc done --summary '<one paragraph>'` when this step is finished; the reviewer runs
  `flocc done` after writing review.md.
- `flocc commit -m '<message>' -- <paths>` to commit only those paths.
"""


def state_dir(cfg: Config, key: str) -> Path:
    return cfg.state_root / key


def agent_dir(cfg: Config, key: str) -> Path:
    """Where the session for key keeps its files: flocc-agent's, read through flocc.agentfs."""
    return cfg.agent_state_root / key


def write_context(cfg: Config, issue: Issue, claim: Claim) -> None:
    d = state_dir(cfg, issue.key)
    d.mkdir(parents=True, exist_ok=True)
    (d / "context.md").write_text(
        f"# {issue.key}: {issue.title}\n\n{issue.url}\n\n{issue.description}\n\n"
        f"## Claim\n\nBranch `{claim.branch}`.\n\n"
        f"Your state directory is {d}.\n\n{_COMMANDS}"
    )


def phase_prompt(phase: str, issue_key: str, extra: str = "") -> str:
    skill = ARCHETYPE[phase]
    body = (
        f"You are the {skill} for {issue_key}. Follow the flocc {skill} skill and end the way it"
        " says: delete your scratchpad and the temp files you made, write your summary, run"
        " flocc done."
    )
    if phase == "review":
        body += (
            " Change nothing in the repo; read review-input.md and write review.md in your state"
            " directory."
        )
    return f"{body}\n\n{extra}".strip()
