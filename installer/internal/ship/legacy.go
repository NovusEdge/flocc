package ship

import (
	"context"
	"fmt"

	"github.com/NovusEdge/flocc/installer/internal/host"
)

// The service user before the rename. Its timer is a user unit, so the user alone tells.
const legacyUser = "linear-agents"

// refuseLegacy stops a ship to a host still running the service under its old name: the new
// service would install beside it, and both pollers would claim the same issues.
func refuseLegacy(ctx context.Context, r host.Runner, dest Dest) error {
	res, err := r.Run(ctx, sshCmd(dest, false, "id -u "+legacyUser))
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return nil
	}
	return fmt.Errorf("the host has a %s user, so it runs the service from before 0.3.0, and a second poller there would claim the same issues; build a new host instead (docs/vm-setup.md, Hosts from before 0.3.0)", legacyUser)
}
