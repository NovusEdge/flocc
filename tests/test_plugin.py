import json
import os
import re
import subprocess
import tomllib
from pathlib import Path

from flocc.context import phase_prompt

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
    assert {p.stem for p in (PLUGIN / "agents").glob("*.md")} == SPEC.keys()
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
    assert set(hooks) == {"SessionStart", "UserPromptSubmit", "PreToolUse", "Stop", "StopFailure"}
    assert [h["command"] for h in hooks["StopFailure"][0]["hooks"]] == ["flocc stop --failure"]
    assert [h["command"] for h in hooks["Stop"][0]["hooks"]] == ["flocc stop"]
    # The idle marker means "the last event was a Stop", so any later activity removes it.
    clear = '[ -z "$FLOCC_STATE" ] || rm -f "$FLOCC_STATE/idle"'
    (clearing, guarding) = hooks["PreToolUse"]
    assert "matcher" not in clearing and [h["command"] for h in clearing["hooks"]] == [clear]
    assert [h["command"] for h in guarding["hooks"]] == ["flocc guard"]
    for tool in ("AskUserQuestion", "EnterPlanMode", "ExitPlanMode", "Bash", "Edit", "Write"):
        assert re.search(guarding["matcher"], tool)
    assert re.search(guarding["matcher"], "mcp__linear__save_issue")
    assert not re.search(guarding["matcher"], "Read")
    assert [h["command"] for e in hooks["UserPromptSubmit"] for h in e["hooks"]] == [clear]
    starts = [h["command"] for e in hooks["SessionStart"] for h in e["hooks"]]
    assert starts == ["flocc session-start"]


def test_the_idle_clearing_hook_removes_only_the_marker(tmp_path):
    hooks = json.loads((PLUGIN / "hooks" / "hooks.json").read_text())["hooks"]
    command = hooks["UserPromptSubmit"][0]["hooks"][0]["command"]
    (tmp_path / "idle").touch()
    (tmp_path / "idle-escalated").touch()
    env = {"PATH": os.environ["PATH"], "FLOCC_STATE": str(tmp_path)}
    subprocess.run(command, shell=True, env=env, check=True)
    assert not (tmp_path / "idle").exists() and (tmp_path / "idle-escalated").exists()
    subprocess.run(command, shell=True, env={"PATH": env["PATH"]}, check=True)


def test_fix_prompt_points_at_the_fix_section():
    assert "fixer skill" in phase_prompt("fix", "NF-1", "check output")
