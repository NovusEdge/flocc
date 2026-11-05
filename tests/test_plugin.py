import inspect
import json
import os
import re
import subprocess
import tomllib
from pathlib import Path

from flocc.context import phase_prompt
from tests.test_ledger import make

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
    (clearing,) = hooks["PreToolUse"]
    assert "matcher" not in clearing and [h["command"] for h in clearing["hooks"]] == [clear]
    assert [h["command"] for e in hooks["UserPromptSubmit"] for h in e["hooks"]] == [clear]
    starts = [h["command"] for e in hooks["SessionStart"] for h in e["hooks"]]
    assert starts == ["flocc session-start"]


def test_the_guard_is_in_managed_settings():
    managed = json.loads((PLUGIN.parent / "deploy" / "managed-settings.json").read_text())
    (guarding,) = managed["hooks"]["PreToolUse"]
    assert [h["command"] for h in guarding["hooks"]] == ["/opt/flocc/bin/flocc guard"]
    for tool in ("AskUserQuestion", "EnterPlanMode", "ExitPlanMode", "Bash", "Edit", "Write"):
        assert re.search(guarding["matcher"], tool)
    assert re.search(guarding["matcher"], "mcp__linear__save_issue")
    assert not re.search(guarding["matcher"], "Read")
    assert {"AskUserQuestion", "EnterPlanMode", "ExitPlanMode"} <= set(
        managed["permissions"]["deny"]
    )
    assert "allowManagedHooksOnly" not in managed


def test_the_idle_clearing_hook_removes_only_the_marker(tmp_path):
    hooks = json.loads((PLUGIN / "hooks" / "hooks.json").read_text())["hooks"]
    command = hooks["UserPromptSubmit"][0]["hooks"][0]["command"]
    (tmp_path / "idle").touch()
    env = {"PATH": os.environ["PATH"], "FLOCC_STATE": str(tmp_path)}
    subprocess.run(command, shell=True, env=env, check=True)
    assert not (tmp_path / "idle").exists()
    subprocess.run(command, shell=True, env={"PATH": env["PATH"]}, check=True)


def test_the_four_archetype_skills_end_the_same_way():
    skills = {p.parent.name: p.read_text() for p in (PLUGIN / "skills").glob("*/SKILL.md")}
    assert set(skills) == {"executor", "fixer", "tester", "reviewer"}
    for name, text in skills.items():
        assert frontmatter(PLUGIN / "skills" / name / "SKILL.md")["name"] == name
        assert "AskUserQuestion" not in text and "auto mode" not in text
        assert "flocc done" in text
        if name != "reviewer":
            assert "--needs-owner" in text and "line that starts with `Needs the owner`" not in text
        assert "scratchpad" in text
        assert "## Last steps" in text


def test_no_prompt_asks_for_the_owner_heading():
    from flocc import cli, context, control, guard, supervisor

    texts = [
        context._COMMANDS,
        control._resume_prompt(None, make("NF-1", state="blocked"), "idle", "note"),
        guard.KEEP_MOVING,
        guard.NO_APPROVER,
        cli.STOP_REASON,
        inspect.getsource(supervisor._start_review_fix),
    ]
    for text in texts:
        assert "--needs-owner" in text
        assert 'a line that starts with "Needs the owner"' not in text


def test_fix_prompt_points_at_the_fix_section():
    assert "fixer skill" in phase_prompt("fix", "NF-1", "check output")
