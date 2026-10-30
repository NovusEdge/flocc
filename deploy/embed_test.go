package deploy

import "testing"

func TestCardinalFilesAreEmbedded(t *testing.T) {
	for _, p := range []string{
		"cardinal/plugin/.claude-plugin/plugin.json",
		"cardinal/plugin/monitors/monitors.json",
		"cardinal/plugin/bin/follow",
		"cardinal/plugin/skills/triage/SKILL.md",
		"cardinal/cardinal.service",
		"cardinal/managed-settings.json",
	} {
		if _, err := Cardinal.ReadFile(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestFilesHoldUnitsAndExample(t *testing.T) {
	for _, name := range append(append(append([]string{}, Units...), AgentUnits...), "config.example.toml", "flocc-agent-run", "flocc-agent.sudoers", "managed-settings.json") {
		b, err := Files.ReadFile(name)
		if err != nil || len(b) == 0 {
			t.Fatalf("%s: %v (len %d)", name, err, len(b))
		}
	}
}
