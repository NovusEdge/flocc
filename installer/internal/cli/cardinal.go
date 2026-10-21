package cli

import (
	"context"
	"os/exec"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/cardinal"
	"github.com/NovusEdge/flocc/installer/internal/ship"
)

func (d *deps) cardinal(ctx context.Context, args []string) int {
	mux, _ := ship.SSHMux()
	return cardinal.Main(ctx, cardinal.Deps{Out: d.out, Err: d.err, Home: d.home, TTY: d.tty, Open: d.open,
		RepoRoot: d.repoRoot, Tailnet: tailnetIPs, Mux: mux}, args)
}

// tailnetIPs are this desktop's tailnet addresses, which cardinal's user pass must fail to reach.
func tailnetIPs() []string {
	out, err := exec.Command("tailscale", "ip").Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}
