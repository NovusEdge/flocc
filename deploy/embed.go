// Package deploy embeds the unit files and config template so the installer ships the
// copies in this commit and no others.
package deploy

import "embed"

//go:embed flocc.service flocc.timer flocc-tmux.service flocc-a2a.service config.example.toml
var Files embed.FS

var Units = []string{"flocc.service", "flocc.timer", "flocc-tmux.service"}

// A2AUnit is installed by the host's A2A step, not with Units: it runs only once ship has written
// a2a.toml.
const A2AUnit = "flocc-a2a.service"

//go:embed cardinal
var Cardinal embed.FS

var CardinalUnits = []string{"cardinal.service", "cardinal-renew.service", "cardinal-renew.timer"}
