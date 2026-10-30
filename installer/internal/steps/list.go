package steps

func RootSteps() []Step {
	return []Step{Packages{}, Browsers{}, Tailscale{}, User{}, AgentAccount{}, AgentAccess{}, ClaudeHome{}, Containers{}, Scratch{}}
}

func ServiceSteps() []Step {
	return []Step{Repo{}, Identity{}, Claude{}, ClaudeLogin{}, Secrets{}, Tools{}, Config{}, Run{}}
}
