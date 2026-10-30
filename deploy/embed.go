// Package deploy embeds the unit files, the agent wrapper, its sudoers rule, Claude Code's
// managed settings and the config template, so the installer ships the copies in this commit
// and no others.
package deploy

import "embed"

//go:embed flocc.service flocc.timer flocc-tmux.service config.example.toml flocc-agent-run flocc-agent.sudoers managed-settings.json
var Files embed.FS

// Units are the service user's; AgentUnits are flocc-agent's.
var (
	Units      = []string{"flocc.service", "flocc.timer"}
	AgentUnits = []string{"flocc-tmux.service"}
)
