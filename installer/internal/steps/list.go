package steps

func RootSteps() []Step { return []Step{Packages{}, Tailscale{}, User{}, Scratch{}} }

// Permissions goes before Config: flocc doctor loads permissions.toml, so a bad table already on
// the host fails Config's check, and the fixed table must land before that check runs.
func ServiceSteps() []Step {
	return []Step{Repo{}, Identity{}, Claude{}, ClaudeLogin{}, Secrets{}, Permissions{}, Config{}, Run{}}
}
