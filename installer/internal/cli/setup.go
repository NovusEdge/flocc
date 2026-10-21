package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"charm.land/huh/v2"

	"github.com/NovusEdge/flocc/installer/internal/cardinal"
	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/create"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/steps"
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
	ctx       context.Context
	d         *deps
	ops       setupOps
	rows      []setupRow
	version   [3]int
	stopped   bool                    // a prompt was cancelled: nothing more is asked or run
	failed    map[string]bool         // by saved.HostPart: machines whose host failed, which steps 4-6 skip
	probed    map[string]machineState // by saved.HostPart
	cardLogin string                  // cardinal's login from its last status
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
	if !w.fillKeys([]keys.Slot{keys.Publish, keys.Ntfy}, true) || w.stopped {
		return
	}
	machines := machinesOf(list)
	remove := w.oldVersions(machines)
	if w.stopped {
		return
	}
	w.cardinal(machines, remove)
	if w.stopped {
		return
	}
	w.removeRest(machines, remove)
}

// oldVersions shows each old user found on a machine and asks whether to remove it, default no.
// The removals it returns run in the machine's root pass.
func (w *wizard) oldVersions(machines []string) map[string][]string {
	remove := map[string][]string{}
	for _, m := range machines {
		k := saved.HostPart(m)
		if w.failed[k] {
			w.row(rowPending, "old versions", k+": skipped, a host on it failed")
			continue
		}
		found := w.ops.oldUsers(w.ctx, m)
		if len(found) == 0 {
			w.row(rowDone, "old versions", "none on "+k)
			continue
		}
		for _, u := range found {
			w.showOld(k, u)
			yes, err := w.d.prompt.Confirm("Remove the "+u.Name+" user on "+k+"? Its home is archived first", false)
			if err != nil {
				w.fail("old versions", err)
				return nil
			}
			if yes {
				remove[k] = append(remove[k], u.Name)
			} else {
				w.row(rowSkipped, "old versions", u.Name+" on "+k+" kept")
			}
		}
	}
	return remove
}

// showOld prints what was found. The process lines are the old user's text, so they are sanitized.
func (w *wizard) showOld(machine string, u oldUser) {
	o := w.d.o()
	facts := []string{fmt.Sprintf("%d processes", len(u.Procs))}
	if u.Lingering {
		facts = append(facts, "lingering")
	}
	if u.Since != "" {
		facts = append(facts, "user manager since "+saved.Sanitize(u.Since))
	}
	o.Line("%s user on %s (%s), from an earlier flocc:", u.Name, machine, strings.Join(facts, ", "))
	for _, p := range u.Procs {
		o.Line("  %s", saved.Sanitize(p))
	}
	if slices.ContainsFunc(u.Procs, polls) {
		o.WarnLine("It runs claude or a flocc tick, so the old install may still be polling Linear.")
	}
}

// polls reports whether a process of an old install may still be taking Linear issues.
func polls(args string) bool {
	return strings.Contains(args, "claude") || strings.Contains(args, "linear-agents run") || strings.Contains(args, "flocc run")
}

// setupFailures are the status failures setup repairs; the login is step 6's.
func setupFailures(st steps.CardinalStatus) []string {
	st.Login = "full"
	return st.Failures()
}

// cardinal sets up or repairs cardinal on its one machine: the saved install's, else the first
// host's. A setup also runs that machine's agreed removals in its root pass, and takes them out of
// remove. It returns the install to log in, or nil.
func (w *wizard) cardinal(machines []string, remove map[string][]string) *saved.Saved {
	cs, have, err := w.ops.cardinal()
	if err != nil {
		w.row(rowFailed, "cardinal", err.Error())
		return nil
	}
	if !have {
		if len(machines) == 0 {
			w.row(rowSkipped, "cardinal", "no host is on a machine of yours")
			return nil
		}
		cs = saved.Saved{Machine: machines[0], User: saved.DefaultUser, RenewHour: 4}
	}
	k := saved.HostPart(cs.Machine)
	if w.failed[k] {
		w.row(rowPending, "cardinal", "skipped: a host on "+k+" failed")
		return nil
	}
	if have && !cs.Removed {
		st, err := w.ops.cardStatus(w.ctx, cs)
		if err == nil && len(setupFailures(st)) == 0 {
			w.row(rowDone, "cardinal", "status ok on "+k)
			w.cardLogin = st.Login
			return &cs
		}
		return w.setupCardinal(cs, true, remove)
	}
	ms := w.probe(cs.Machine)
	if ms.Err != nil {
		w.row(rowFailed, "cardinal", ms.Err.Error())
		return nil
	}
	var q string
	var def, adopt bool
	switch {
	case have:
		q, adopt = "Set up cardinal on "+k+" again? flocc cardinal remove took it down", true
	case w.ops.hasUser(w.ctx, cs.Machine, cs.User):
		q, adopt = "Adopt the existing "+cs.User+" user on "+k+"? No saved file names it, so it looks like a setup by hand", true
	default:
		q, def = "Set up cardinal on "+k+"?", true
	}
	yes, err := w.d.prompt.Confirm(q, def)
	if err != nil {
		w.fail("cardinal", err)
		return nil
	}
	if !yes {
		w.row(rowSkipped, "cardinal", "not set up on "+k+"; asked again next run")
		return nil
	}
	return w.setupCardinal(cs, adopt, remove)
}

func (w *wizard) setupCardinal(cs saved.Saved, adopt bool, remove map[string][]string) *saved.Saved {
	k := saved.HostPart(cs.Machine)
	ms := w.probe(cs.Machine)
	switch {
	case ms.Err != nil:
		w.row(rowFailed, "cardinal", ms.Err.Error())
		return nil
	case ms.Incus != nil:
		w.row(rowFailed, "cardinal", ms.Incus.Error())
		return nil
	}
	rm := remove[k]
	delete(remove, k)
	err := w.ops.cardSetup(w.ctx, cs, cardinal.SetupOptions{Adopt: adopt, Remove: rm})
	w.removed(cs.Machine, rm, err)
	if err != nil {
		w.fail("cardinal", fmt.Errorf("setup: %w", err))
		return nil
	}
	st, err := w.ops.cardStatus(w.ctx, cs)
	switch {
	case err != nil:
		w.row(rowFailed, "cardinal", "after setup: "+err.Error())
		return nil
	case len(setupFailures(st)) > 0:
		w.row(rowPending, "cardinal", strings.Join(setupFailures(st), "\n"))
	default:
		w.row(rowDone, "cardinal", "set up on "+k)
	}
	w.cardLogin = st.Login
	return &cs
}

// removeRest runs the removals no cardinal setup took, one purge-only root pass per machine.
func (w *wizard) removeRest(machines []string, remove map[string][]string) {
	for _, m := range machines {
		if users := remove[saved.HostPart(m)]; len(users) > 0 {
			w.removed(m, users, w.ops.removeOld(w.ctx, m, users))
		}
	}
}

// removed looks for each agreed user again, as it was found, and reports what the root pass did.
func (w *wizard) removed(m string, users []string, err error) {
	if len(users) == 0 {
		return
	}
	k := saved.HostPart(m)
	still := w.ops.oldUsers(w.ctx, m)
	for _, u := range users {
		if !slices.ContainsFunc(still, func(o oldUser) bool { return o.Name == u }) {
			w.row(rowDone, "old versions", "removed "+u+" on "+k+"; its home is archived under /root there. "+
				"Revoke the old install's GitHub tokens and Linear key yourself")
			continue
		}
		why := "the root pass did not remove it"
		if err != nil {
			why = err.Error()
		}
		w.row(rowFailed, "old versions", u+" on "+k+": "+why)
	}
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
