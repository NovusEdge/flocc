import json
from pathlib import Path

from linear_agents.context import phase_prompt
from linear_agents.gate import CATALOGUE

PLUGIN = Path(__file__).parent.parent / "plugin"
SPEC = {
    "source-finder": ("haiku", "low"),
    "source-reader": ("sonnet", "medium"),
    "code-mapper": ("sonnet", "medium"),
    "plan-reviewer": ("opus", "high"),
    "task-implementer": ("sonnet", "medium"),
    "wave-reviewer": ("opus", "high"),
    "fix-rereviewer": ("sonnet", "medium"),
}


def frontmatter(path: Path) -> dict[str, str]:
    head = path.read_text().split("---")[1]
    pairs = (line.partition(":") for line in head.splitlines())
    return {k.strip(): v.strip() for k, _, v in pairs if k.strip()}


def test_every_catalogue_agent_has_a_file_with_its_model_and_effort():
    assert set(CATALOGUE) == SPEC.keys()
    for name, (model, effort) in SPEC.items():
        fm = frontmatter(PLUGIN / "agents" / f"{name}.md")
        assert (fm["name"], fm["model"], fm["effort"]) == (name, model, effort)


def test_reviewers_and_mappers_cannot_edit_or_run_shell():
    for name in ("code-mapper", "plan-reviewer", "wave-reviewer", "fix-rereviewer"):
        tools = {
            t.strip() for t in frontmatter(PLUGIN / "agents" / f"{name}.md")["tools"].split(",")
        }
        assert not {"Edit", "Write", "Bash"} & tools


def test_hooks():
    hooks = json.loads((PLUGIN / "hooks" / "hooks.json").read_text())["hooks"]
    pre = hooks["PreToolUse"][0]
    assert pre["matcher"] == "Agent|Task|Workflow|AskUserQuestion"
    assert pre["hooks"][0]["command"] == "linear-agents gate"
    assert pre["hooks"][0]["onFailure"] == "block"
    assert hooks["PostToolUse"][0]["matcher"] == "AskUserQuestion"
    assert hooks["PostToolUse"][0]["hooks"][0]["command"] == "linear-agents gate --answered"
    # A question cut short by an error or an interrupt never reaches PostToolUse, and an
    # `asking` left behind would hold Linear replies and the handoff for the whole session.
    assert hooks["PostToolUseFailure"][0]["matcher"] == "AskUserQuestion"
    assert hooks["PostToolUseFailure"][0]["hooks"][0]["command"] == "linear-agents gate --answered"
    assert hooks["Stop"][0]["hooks"][0]["command"] == "linear-agents gate --answered"
    assert hooks["SubagentStop"][0]["hooks"][0]["command"] == "linear-agents gate --stopped"
    assert hooks["SessionStart"][0]["hooks"][0]["command"] == "linear-agents session-start"


def test_fix_prompt_points_at_the_fix_section():
    assert "Fix phase" in phase_prompt("fix", "NF-1", "check output")
