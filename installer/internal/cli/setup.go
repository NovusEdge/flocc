package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"charm.land/huh/v2"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/create"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
)

type rowState int

const (
	rowDone rowState = iota
	// rowSkipped is a step left as it is with nothing wrong: the owner said no, or there is nothing
	// the wizard could act on. It does not fail the run.
	rowSkipped
	rowPending // left to do; the detail says what
	rowFailed
)

type setupRow struct {
	state        rowState
	name, detail string
}

type wizard struct {
	ctx     context.Context
	d       *deps
	ops     setupOps
	rows    []setupRow
	version [3]int
	stopped bool                    // a prompt was cancelled: nothing more is asked or run
	failed  map[string]bool         // by saved.HostPart: machines whose host failed, which steps 4-6 skip
	probed  map[string]machineState // by saved.HostPart
}

func setupCmd(ctx context.Context, d *deps, args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(d.err, "usage: flocc setup")
		return 2
	}
	if !d.tty {
		fmt.Fprintln(d.err, "flocc setup asks as it goes; run it in a terminal")
		return 2
	}
	return runSetup(ctx, d, newSetupOps(d))
}

func runSetup(ctx context.Context, d *deps, ops setupOps) int {
	w := &wizard{ctx: ctx, d: d, ops: ops, failed: map[string]bool{}, probed: map[string]machineState{}}
	w.run()
	return w.summary()
}

func (w *wizard) run() {
	if !w.checkout() || !w.fillKeys([]keys.Slot{keys.Linear, keys.Agent}, false) {
		return
	}
	list := w.hostFiles()
	if w.stopped || len(list) == 0 {
		return
	}
	w.checkHosts(list)
	if w.stopped {
		return
	}
	// The publish token's page lists the mapped repos, so it comes after the host.
	w.fillKeys([]keys.Slot{keys.Publish, keys.Ntfy}, true)
}

type checkedHost struct {
	h  hosts.Host
	st hostState
}

// checkHosts reads every host, ships the ones ship can repair and marks each machine whose host
// failed, which steps 4-6 then skip. It returns the hosts that did not fail.
func (w *wizard) checkHosts(list []hosts.Host) []checkedHost {
	var ok []checkedHost
	for _, h := range list {
		if w.stopped {
			break
		}
		if st, good := w.checkHost(h); good {
			ok = append(ok, checkedHost{h, st})
		} else if m := machineOf(h); m != "" {
			w.failed[saved.HostPart(m)] = true
		}
	}
	return ok
}

func (w *wizard) checkHost(h hosts.Host) (hostState, bool) {
	st := w.ops.readHost(w.ctx, h)
	if st.Err != "" {
		detail := "could not read it: " + st.Err
		if h.SSH != "" && w.ops.offline(w.ctx, saved.HostPart(h.SSH)) {
			detail += "\nits tailnet node is offline; if the host is gone, remove the node at https://login.tailscale.com/admin/machines and rm " + hostFile(w.d, h.Name)
		}
		w.row(rowFailed, h.Name, detail)
		return st, false
	}
	if why := w.shipWhy(st); why != "" {
		w.d.o().Line("%s: %s; shipping the checkout", h.Name, why)
		if err := w.ops.ship(w.ctx, h.Name); err != nil {
			w.fail(h.Name, fmt.Errorf("ship: %w", err))
			return st, false
		}
		if st = w.ops.readHost(w.ctx, h); st.Err != "" {
			w.row(rowFailed, h.Name, "after ship, could not read it: "+st.Err)
			return st, false
		}
		if why := w.shipWhy(st); why != "" {
			w.row(rowFailed, h.Name, "still, after ship: "+why)
			return st, false
		}
	}
	if st.LowDisk != "" {
		w.row(rowPending, h.Name, st.LowDisk+"\nfree space there; flocc does not delete worktrees for you")
		return st, true
	}
	w.row(rowDone, h.Name, "flocc "+st.Version+", doctor ok")
	return st, true
}

// shipWhy is why ship should run on a host, "" when it need not.
func (w *wizard) shipWhy(st hostState) string {
	switch {
	case st.Version == "":
		return "flocc is not installed there"
	case !host.AtLeast(st.Version, w.version):
		return "flocc " + st.Version + " is older than the checkout"
	case len(st.Doctor) > 0:
		return "doctor fails " + strings.Join(st.Doctor, "; ")
	}
	return ""
}

func (w *wizard) row(state rowState, name, detail string) {
	w.rows = append(w.rows, setupRow{state, name, detail})
}

// fail records err as name's failure. A cancelled prompt or an empty paste stops the whole run.
func (w *wizard) fail(name string, err error) {
	if errors.Is(err, huh.ErrUserAborted) || errors.Is(err, context.Canceled) || errors.Is(err, keys.ErrStopped) {
		w.stopped = true
	}
	w.row(rowFailed, name, err.Error())
}

func (w *wizard) summary() int {
	o := w.d.o()
	marks := map[rowState]string{rowDone: o.OK("✓"), rowSkipped: "·", rowPending: o.Warn("…"), rowFailed: o.Fail("✗")}
	fmt.Fprintln(w.d.out, "\nflocc setup")
	code := 0
	for _, r := range w.rows {
		o.Line("%s %-13s %s", marks[r.state], r.name, r.detail)
		if r.state >= rowPending {
			code = 1
		}
	}
	if w.stopped {
		o.Line("Stopped. Run flocc setup again to go on.")
		code = 1
	}
	return code
}

func (w *wizard) checkout() bool {
	root, v, err := w.ops.checkout()
	if err != nil {
		w.row(rowFailed, "checkout", err.Error())
		return false
	}
	ver, ok := host.Version(v)
	if !ok {
		w.row(rowFailed, "checkout", fmt.Sprintf("%s: version %q is not x.y.z", root, v))
		return false
	}
	w.version = ver
	w.row(rowDone, "checkout", root+" at "+v)
	return true
}

// fillKeys asks for the keys in slots the store lacks; with check it then checks every key
// against what the host files map.
func (w *wizard) fillKeys(slots []keys.Slot, check bool) bool {
	if err := w.ops.fillKeys(w.ctx, slots); err != nil {
		w.fail("keys", err)
		return false
	}
	var titles []string
	for _, s := range slots {
		titles = append(titles, s.Title())
	}
	if check {
		if p := w.ops.checkKeys(w.ctx); len(p) > 0 {
			w.row(rowPending, "keys", strings.Join(p, "\n"))
			return true
		}
	}
	w.row(rowDone, "keys", strings.Join(titles, ", "))
	return true
}

// probe reads a machine once per run.
func (w *wizard) probe(machine string) machineState {
	k := saved.HostPart(machine)
	if s, ok := w.probed[k]; ok {
		return s
	}
	s := w.ops.machine(w.ctx, machine)
	w.probed[k] = s
	return s
}

// hostFiles is every host file, after making the first host when there is none and asking each
// ssh host without a machine which one holds it.
func (w *wizard) hostFiles() []hosts.Host {
	list := w.ops.hosts()
	if len(list) == 0 {
		if !w.firstHost() {
			return nil
		}
		list = w.ops.hosts()
	}
	for i := range list {
		if w.stopped {
			return nil
		}
		w.placeHost(&list[i])
	}
	return list
}

func (w *wizard) firstHost() bool {
	// A create that stopped before host add left its log and no host file.
	for _, name := range w.ops.createLogs() {
		again, err := w.d.prompt.Confirm("Continue creating "+name+"? A host create for it stopped before the host was added", false)
		if err != nil {
			w.fail("host", err)
			return false
		}
		if again {
			return w.createHost(name)
		}
	}
	i, err := w.d.prompt.Choose("No hosts yet", []string{
		"Create one as an Incus VM on a machine of yours",
		"Add one that already runs flocc, or will after ship (ssh or Daytona)",
	})
	if err != nil {
		w.fail("host", err)
		return false
	}
	if i == 1 {
		return w.addExisting()
	}
	name, err := w.d.prompt.Ask("Host name", "2 to 63 lowercase letters, digits and '-'; also its Incus and Tailscale name")
	if err != nil {
		w.fail("host", err)
		return false
	}
	return w.createHost(strings.TrimSpace(name))
}

func (w *wizard) createHost(name string) bool {
	m, err := w.d.prompt.Ask("Machine to make "+name+" on", "the ssh destination of your account there, such as you@box")
	if err != nil {
		w.fail("host", err)
		return false
	}
	spec, err := create.ParseArgs([]string{strings.TrimSpace(m), name})
	if err != nil {
		w.row(rowFailed, "host", err.Error())
		return false
	}
	ms := w.probe(spec.Machine)
	switch {
	case ms.Err != nil:
		w.row(rowFailed, "host", ms.Err.Error())
		return false
	case !ms.Sudo:
		w.row(rowFailed, "host", fmt.Sprintf("host create needs passwordless sudo on %s: its firewall and Docker steps run sudo -n even to check. "+
			"Add a file in /etc/sudoers.d/ there with visudo, or add a host made another way", spec.Machine))
		return false
	case ms.Incus != nil:
		w.row(rowFailed, "host", ms.Incus.Error())
		return false
	}
	if err := w.ops.create(w.ctx, spec); err != nil {
		w.fail("host", fmt.Errorf("create %s: %w", name, err))
		return false
	}
	w.row(rowDone, "host", "created "+name+" on "+spec.Machine)
	return true
}

func (w *wizard) addExisting() bool {
	name, err := w.d.prompt.Ask("Host name", "what flocc calls it, and its file name")
	if err != nil {
		w.fail("host", err)
		return false
	}
	reach, err := w.d.prompt.Ask("How to reach it", "an ssh destination such as root@box, or daytona:<sandbox>")
	if err != nil {
		w.fail("host", err)
		return false
	}
	name, reach = strings.TrimSpace(name), strings.TrimSpace(reach)
	var h hosts.Host
	if sb, ok := strings.CutPrefix(reach, "daytona:"); ok {
		h.Daytona = sb
	} else {
		h.SSH = reach
	}
	if err := w.ops.add(w.ctx, name, h); err != nil {
		w.fail("host", fmt.Errorf("host add %s: %w", name, err))
		return false
	}
	if err := w.ops.ship(w.ctx, name); err != nil {
		w.fail("host", fmt.Errorf("ship %s: %w", name, err))
		return false
	}
	w.row(rowDone, "host", "added and shipped "+name)
	return true
}

// placeHost asks once which machine holds an ssh host and saves the answer, none included. An
// empty answer saves nothing, so it is asked again next run.
func (w *wizard) placeHost(h *hosts.Host) {
	if h.Machine != "" || h.Daytona != "" {
		return
	}
	m, err := w.d.prompt.Ask("Which machine holds "+h.Name+"?",
		"the ssh destination of your account there, such as you@box; none for a host not on one of your machines")
	if err != nil {
		w.fail(h.Name, err)
		return
	}
	m = strings.TrimSpace(m)
	switch {
	case m == "":
		return
	case m != hosts.MachineNone && !hosts.ValidTarget(m):
		w.row(rowPending, h.Name, fmt.Sprintf("%q is not an ssh destination; asked again next run", m))
		return
	}
	h.Machine = m
	if err := w.ops.saveHost(*h); err != nil {
		w.row(rowFailed, h.Name, "saving its machine: "+err.Error())
	}
}

// machineOf is the machine holding h, or "" when it is on none of the owner's or not known yet.
func machineOf(h hosts.Host) string {
	if h.Machine == hosts.MachineNone {
		return ""
	}
	return h.Machine
}

// machinesOf is each machine holding a host, in host order, once: owner@box and box are one.
func machinesOf(list []hosts.Host) []string {
	var out []string
	for _, h := range list {
		m := machineOf(h)
		if m != "" && !slices.ContainsFunc(out, func(o string) bool { return saved.HostPart(o) == saved.HostPart(m) }) {
			out = append(out, m)
		}
	}
	return out
}
