// Package deploy embeds the unit files and config template so the installer ships the
// copies in this commit and no others.
package deploy

import "embed"

//go:embed linear-agents.service linear-agents.timer linear-agents-tmux.service config.example.toml
var Files embed.FS

var Units = []string{"linear-agents.service", "linear-agents.timer", "linear-agents-tmux.service"}
