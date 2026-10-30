package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/NovusEdge/flocc/installer/internal/create"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
)

const cardinalUsage = "usage: flocc host cardinal -H <host> [--renew-hour H]"

// hostCardinal makes or repairs the cardinal VM on the machine holding host name. The steps of the
// run, and the failure of any of them, are rows of the session.
func hostCardinal(ctx context.Context, d *deps, name string, args []string) int {
	fs := flag.NewFlagSet("flocc host cardinal", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	hour := fs.Int("renew-hour", 4, "local hour of cardinal's daily fresh start")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *hour < 0 || *hour > 23 || name == "" || name == "all" {
		fmt.Fprintln(d.err, "flocc host cardinal:", cardinalUsage)
		return 2
	}
	h, err := hosts.Load(hosts.Dir(d.home), name)
	if err == nil && (h.Machine == "" || h.Machine == hosts.MachineNone) {
		err = fmt.Errorf("%s has no machine in its host file; cardinal runs only beside a host made by flocc host create", name)
	}
	if err != nil {
		d.ui.Step("Host file").Fail(err)
		return 1
	}
	if err := needCheckout(d); err != nil {
		d.ui.Step("Check").Fail(err)
		return 1
	}
	spec := create.Spec{Machine: h.Machine, Name: create.CardinalName, CPUs: 1, MemoryGiB: 2, DiskGiB: 10}
	if err := create.RunCardinal(ctx, spec, create.CardinalSpec{Executor: name, RenewHour: *hour}, d.ui, d.tty, d.repoRoot); err != nil {
		d.ui.Todo("Run the same command again to continue.")
		return 1
	}
	return 0
}
