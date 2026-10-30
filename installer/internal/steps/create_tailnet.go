package steps

import (
	"context"
	"errors"
	"os"
	"strings"
)

const (
	tailnetTable    = "flocc-no-tailnet"
	tailnetFile     = "/etc/flocc/no-tailnet.nft"
	tailnetUnitName = "flocc-no-tailnet.service"
	tailnetUnitPath = "/etc/systemd/system/" + tailnetUnitName
	tailnetDrop     = `iifname "` + IncusBridge + `" oifname "tailscale0" drop`
)

// tailnetRules replaces the table whether or not it exists. Interface names are matched as
// strings, so the table loads before tailscale0 does. A drop in any base chain is final, so
// firewalld's and Docker's accepts do not undo it. Executor VMs run their own tailscaled whose
// WireGuard traffic leaves through the uplink, so they lose nothing, unless the machine uses an
// exit node (or a subnet route covering a peer endpoint): table 52 then sends the default route
// out of tailscale0 and this drop cuts all VM egress.
var tailnetRules = "# Written by flocc host create: VMs on " + IncusBridge + " cannot leave through this machine's tailnet.\n" +
	"table inet " + tailnetTable + "\n" +
	"delete table inet " + tailnetTable + "\n" +
	"table inet " + tailnetTable + " {\n" +
	"  chain forward {\n" +
	"    type filter hook forward priority -10; policy accept;\n" +
	"    " + tailnetDrop + "\n" +
	"  }\n" +
	"}\n"

// No ExecStop: stopping the unit must not reopen the hole. After=nftables.service because that
// unit's flush ruleset would otherwise remove the table after it loaded.
var tailnetUnit = "[Unit]\nDescription=flocc: VMs on " + IncusBridge + " cannot forward into this machine's tailnet\n" +
	"After=nftables.service\nBefore=network-pre.target\nWants=network-pre.target\n\n" +
	"[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=nft -f" + tailnetFile +
	"\n\n[Install]\nWantedBy=multi-user.target\n"

// TailnetBlock keeps VMs on the Incus bridge from reaching the owner's tailnet as this machine.
type TailnetBlock struct{}

func (TailnetBlock) Name() string { return "tailnet block" }

func (TailnetBlock) Check(ctx context.Context, e *Env) (bool, string, error) {
	for path, want := range map[string]string{tailnetFile: tailnetRules, tailnetUnitPath: tailnetUnit} {
		if got, _ := os.ReadFile(e.Path(path)); string(got) != want {
			return false, path + " missing or out of date", nil
		}
	}
	if !unitLive(ctx, e, tailnetUnitName) {
		return false, tailnetUnitName + " not enabled and active", nil
	}
	if !strings.Contains(e.Output(ctx, "nft", "list", "table", "inet", tailnetTable), tailnetDrop) {
		return false, "nft table inet " + tailnetTable + " lacks the drop rule", nil
	}
	detail := IncusBridge + " cannot forward out of tailscale0"
	if strings.Contains(e.Output(ctx, "ip", "route", "get", "1.1.1.1"), "dev tailscale0") {
		detail += "; the machine routes the internet through tailscale0 (an exit node), so VMs have no egress"
	}
	return true, detail, nil
}

func (TailnetBlock) Apply(ctx context.Context, e *Env) error {
	if !e.Ok(ctx, "sh", "-c", "command -v nft") {
		return errors.New("nft is not installed, so a VM can reach the tailnet through this machine; install nftables and run create again")
	}
	if err := writeAtomic(e.Path(tailnetFile), []byte(tailnetRules), 0o644); err != nil {
		return err
	}
	if err := writeAtomic(e.Path(tailnetUnitPath), []byte(tailnetUnit), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", tailnetUnitName}, {"restart", tailnetUnitName}} {
		if _, err := e.Must(ctx, "systemctl", args...); err != nil {
			return err
		}
	}
	return nil
}
