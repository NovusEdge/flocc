package steps

func RootSteps() []Step {
	return []Step{Packages{}, Browsers{}, Tailscale{}, User{}, AgentAccount{}, AgentAccess{}, ClaudeHome{}, Containers{}, Scratch{}, DiskTmp{}}
}

// AgentSteps run as flocc-agent, before the service phase, whose doctor reads their outcome
// through flocc-agent-run.
func AgentSteps() []Step {
	return []Step{Identity{}, Mise{}, Claude{}, Tools{}, AgentTmux{}, PodmanSocket{}}
}

func ServiceSteps() []Step {
	return []Step{Repo{}, ClaudeLogin{}, Secrets{}, Config{}, OldWorktrees{}, Run{}}
}
