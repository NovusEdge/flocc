import json
import os
import subprocess
import tomllib
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


def test_the_plugin_and_the_package_share_one_version():
    project = tomllib.loads((PLUGIN.parent / "pyproject.toml").read_text())["project"]
    manifest = json.loads((PLUGIN / ".claude-plugin" / "plugin.json").read_text())
    assert manifest["version"] == project["version"]
    changelog = (PLUGIN.parent / "CHANGELOG.md").read_text()
    assert f"## {project['version']} " in changelog


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
    stop = [h["command"] for h in hooks["Stop"][0]["hooks"]]
    assert stop == ["linear-agents gate --answered", "linear-agents idle"]
    # The idle marker means "the last event was a Stop", so any later activity removes it.
    clear = '[ -z "$LINEAR_AGENTS_STATE" ] || rm -f "$LINEAR_AGENTS_STATE/idle"'
    ungated = [e for e in hooks["PreToolUse"] if "matcher" not in e]
    assert [h["command"] for e in ungated for h in e["hooks"]] == [clear]
    assert [h["command"] for e in hooks["UserPromptSubmit"] for h in e["hooks"]] == [clear]
    assert hooks["SubagentStop"][0]["hooks"][0]["command"] == "linear-agents gate --stopped"
    assert hooks["SessionStart"][0]["hooks"][0]["command"] == "linear-agents session-start"


def test_the_idle_clearing_hook_removes_only_the_marker(tmp_path):
    hooks = json.loads((PLUGIN / "hooks" / "hooks.json").read_text())["hooks"]
    command = hooks["UserPromptSubmit"][0]["hooks"][0]["command"]
    (tmp_path / "idle").touch()
    (tmp_path / "idle-escalated").touch()
    env = {"PATH": os.environ["PATH"], "LINEAR_AGENTS_STATE": str(tmp_path)}
    subprocess.run(command, shell=True, env=env, check=True)
    assert not (tmp_path / "idle").exists() and (tmp_path / "idle-escalated").exists()
    subprocess.run(command, shell=True, env={"PATH": env["PATH"]}, check=True)


def test_fix_prompt_points_at_the_fix_section():
    assert "Fix phase" in phase_prompt("fix", "NF-1", "check output")
