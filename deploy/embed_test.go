package deploy

import "testing"

func TestFilesHoldUnitsAndExample(t *testing.T) {
	for _, name := range append(append(append([]string{}, Units...), AgentUnits...), "config.example.toml", "flocc-agent-run", "flocc-agent.sudoers", "managed-settings.json") {
		b, err := Files.ReadFile(name)
		if err != nil || len(b) == 0 {
			t.Fatalf("%s: %v (len %d)", name, err, len(b))
		}
	}
}
