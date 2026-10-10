package steps

func RootSteps() []Step    { return []Step{Packages{}, Tailscale{}, User{}} }
func ServiceSteps() []Step { return []Step{Repo{}, Claude{}, Secrets{}, Config{}, Run{}} }
