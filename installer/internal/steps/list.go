package steps

func RootSteps() []Step { return []Step{Packages{}, Tailscale{}, User{}, Scratch{}} }
func ServiceSteps() []Step {
	return []Step{Repo{}, Identity{}, Claude{}, ClaudeLogin{}, Secrets{}, Config{}, Permissions{}, Run{}}
}
