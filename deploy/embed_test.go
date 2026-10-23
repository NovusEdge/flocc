package deploy

import "testing"

func TestFilesHoldUnitsAndExample(t *testing.T) {
	for _, name := range append(Units, A2AUnit, "config.example.toml") {
		b, err := Files.ReadFile(name)
		if err != nil || len(b) == 0 {
			t.Fatalf("%s: %v (len %d)", name, err, len(b))
		}
	}
}
