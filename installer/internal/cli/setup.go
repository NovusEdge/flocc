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
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
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

type wizard struct {
	ctx       context.Context
	d         *deps
	ops       setupOps
	version   [3]int
	pending   bool                    // a row ended pending: setup is not finished
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
	d.ui.Plan([]string{"Checkout", "Keys", "Tailnet", "Hosts", "Old versions", "Cardinal", "Logins"})
	w.run()
	return w.summary()
}

func (w *wizard) run() {
	if !w.checkout() || !w.fillKeys([]keys.Slot{keys.Linear, keys.Agent}, false) {
		return
	}
	if w.tailnet(); w.stopped {
		return
	}
	list := w.hostFiles()
	if w.stopped || len(list) == 0 {
		return
	}
	checked := w.checkHosts(list)
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
	cs := w.cardinal(machines, remove)
	if w.stopped {
		return
	}
	w.removeRest(machines, remove)
	w.logins(checked, cs)
}

// logins signs in each host and cardinal whose login is not full. unknown is a host that could not
// read its login, which is no evidence of a lapse, so it is only shown.
func (w *wizard) logins(checked []checkedHost, cs *saved.Saved) {
	w.d.ui.Group("Logins")
	for _, c := range checked {
		if w.stopped {
			return
		}
		if m := machineOf(c.h); m != "" && w.failed[saved.HostPart(m)] {
			continue
		}
		name := c.h.Name
		w.login(name, c.st.Login, func(email string) error { return w.ops.login(w.ctx, name, email) })
	}
	if cs != nil && !w.stopped {
		w.login("cardinal", &w.cardLogin, func(email string) error { return w.ops.cardLogin(w.ctx, *cs, email) })
	}
}

func (w *wizard) login(name string, state *string, do func(email string) error) {
	switch {
	case state == nil:
		w.row(rowSkipped, "login", name+": the host does not report its login")
		return
	case *state == "full":
		w.row(rowDone, "login", name+": full")
		return
	case *state == "unknown":
		w.row(rowSkipped, "login", name+": unknown, it could not read its login; not acted on")
		return
	}
	email, err := w.ask("claude.ai pool account for "+name+" (login is "+saved.Sanitize(*state)+")", "empty lets the sign-in page choose")
	if err != nil {
		w.fail("login", err)
		return
	}
	// The address goes into a remote command line.
	if email = strings.TrimSpace(email); email != "" && !ship.ValidEmail(email) {
		w.row(rowPending, "login", fmt.Sprintf("%s: %q is not an email address", name, email))
		return
	}
	st := w.d.ui.Step("login")
	st.Detail("signing in " + name)
	if err := do(email); err != nil {
		w.end(st, rowFailed, "login", name+": "+err.Error())
		return
	}
	w.end(st, rowDone, "login", name+": signed in")
}

// oldVersions shows each old user found on a machine and asks whether to remove it, default no.
// The removals it returns run in the machine's root pass.
func (w *wizard) oldVersions(machines []string) map[string][]string {
	w.d.ui.Group("Old versions")
	remove := map[string][]string{}
	for _, m := range machines {
		k := saved.HostPart(m)
		if w.failed[k] {
			w.row(rowPending, "old versions", k+": skipped, a host on it failed")
			continue
		}
		st := w.d.ui.Step("old versions")
		st.Detail("scanning users")
		// The probe is what answers a host-key question or a check-mode approval; the scan runs
		// with BatchMode and cannot.
		if ms := w.probe(m); ms.Err != nil {
			w.end(st, rowFailed, "old versions", k+": could not look: "+ms.Err.Error())
			continue
		}
		found, err := w.ops.oldUsers(w.ctx, m)
		if err != nil {
			w.end(st, rowFailed, "old versions", k+": could not look: "+err.Error())
			continue
		}
		if len(found) == 0 {
			w.end(st, rowDone, "old versions", "none on "+k)
			continue
		}
		w.end(st, rowDone, "old versions", fmt.Sprintf("%d from an earlier flocc on %s", len(found), k))
		for _, u := range found {
			w.showOld(k, u)
			yes, err := w.confirm("Remove the "+u.Name+" user on "+k+"? Its home is archived first", false, u.askFacts()...)
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

// showOld shows what was found. The process lines are the old user's text, so they are sanitized.
func (w *wizard) showOld(machine string, u oldUser) {
	body := strings.Join(u.facts(), ", ")
	for _, p := range u.Procs {
		body += "\n  " + saved.Sanitize(p)
	}
	if slices.ContainsFunc(u.Procs, polls) {
		body += "\nIt runs claude or a flocc tick, so the old install may still be polling Linear."
	}
	w.d.ui.Note(u.Name+" user on "+machine+", from an earlier flocc", body)
}

// facts are the summary of an old user: its uid, process count, lingering and when its user
// manager started.
func (u oldUser) facts() []string {
	var f []string
	if u.UID != "" {
		f = append(f, "uid "+saved.Sanitize(u.UID))
	}
	f = append(f, fmt.Sprintf("%d processes", len(u.Procs)))
	if u.Lingering {
		f = append(f, "lingering")
	}
	if u.Since != "" {
		f = append(f, "user manager since "+saved.Sanitize(u.Since))
	}
	return f
}

// askFacts are shown with a question about the user: facts, then each process it runs.
func (u oldUser) askFacts() []string {
	f := u.facts()
	for _, p := range u.Procs {
		f = append(f, "running "+saved.Sanitize(p))
	}
	return f
}

// polls reports whether a process of an old install may still be taking Linear issues.
func polls(args string) bool {
	return strings.Contains(args, "claude") || strings.Contains(args, steps.OldName+" run") || strings.Contains(args, "flocc run")
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
	w.d.ui.Group("Cardinal")
	st := w.d.ui.Step("cardinal")
	cs, have, err := w.ops.cardinal()
	if err != nil {
		w.end(st, rowFailed, "cardinal", err.Error())
		return nil
	}
	if !have {
		if len(machines) == 0 {
			w.end(st, rowSkipped, "cardinal", "no host is on a machine of yours")
			return nil
		}
		cs = saved.Saved{Machine: machines[0], User: saved.DefaultUser, RenewHour: 4}
	}
	k := saved.HostPart(cs.Machine)
	if w.failed[k] {
		w.end(st, rowPending, "cardinal", "skipped: a host on "+k+" failed")
		return nil
	}
	if have && !cs.Removed {
		st.Detail("reading status")
		status, err := w.ops.cardStatus(w.ctx, cs)
		if err == nil && len(setupFailures(status)) == 0 {
			w.end(st, rowDone, "cardinal", "status ok on "+k)
			w.cardLogin = status.Login
			return &cs
		}
		return w.setupCardinal(st, cs, true, remove)
	}
	st.Detail("looking at " + k)
	ms := w.probe(cs.Machine)
	if ms.Err != nil {
		w.end(st, rowFailed, "cardinal", ms.Err.Error())
		return nil
	}
	var q string
	var facts []string
	var def, adopt bool
	var existing oldUser
	var exists bool
	if !have {
		existing, exists = w.ops.findUser(w.ctx, cs.Machine, cs.User)
	}
	switch {
	case have:
		q, adopt = "Set up cardinal on "+k+" again? flocc cardinal remove took it down", true
	case exists:
		q, adopt = "Adopt the existing "+cs.User+" user on "+k+"? No saved file names it, so it looks like a setup by hand", true
		facts = existing.askFacts()
	default:
		q, def = "Set up cardinal on "+k+"?", true
	}
	st.Detail("waiting for your answer")
	yes, err := w.confirm(q, def, facts...)
	if err != nil {
		w.failAt(st, "cardinal", err)
		return nil
	}
	if !yes {
		w.end(st, rowSkipped, "cardinal", "not set up on "+k+"; asked again next run")
		return nil
	}
	return w.setupCardinal(st, cs, adopt, remove)
}

func (w *wizard) setupCardinal(st *ui.Step, cs saved.Saved, adopt bool, remove map[string][]string) *saved.Saved {
	k := saved.HostPart(cs.Machine)
	st.Detail("looking at " + k)
	ms := w.probe(cs.Machine)
	switch {
	case ms.Err != nil:
		w.end(st, rowFailed, "cardinal", ms.Err.Error())
		return nil
	case ms.Incus != nil:
		w.end(st, rowFailed, "cardinal", ms.Incus.Error())
		return nil
	}
	rm := remove[k]
	delete(remove, k)
	st.Detail("setting up")
	err := w.ops.cardSetup(w.ctx, cs, cardinal.SetupOptions{Adopt: adopt, Remove: rm})
	w.removed(cs.Machine, rm, err)
	if err != nil {
		w.failAt(st, "cardinal", fmt.Errorf("setup: %w", err))
		return nil
	}
	st.Detail("reading status")
	status, err := w.ops.cardStatus(w.ctx, cs)
	switch {
	case err != nil:
		w.end(st, rowFailed, "cardinal", "after setup: "+err.Error())
		return nil
	case len(setupFailures(status)) > 0:
		w.end(st, rowPending, "cardinal", strings.Join(setupFailures(status), "\n"))
	default:
		w.end(st, rowDone, "cardinal", "set up on "+k)
	}
	w.cardLogin = status.Login
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
	st := w.d.ui.Step("old versions")
	st.Detail("checking that " + strings.Join(users, ", ") + " is gone")
	still, scanErr := w.ops.oldUsers(w.ctx, m)
	for i, u := range users {
		if i > 0 {
			st = w.d.ui.Step("old versions")
		}
		if scanErr != nil {
			why := "could not check that it is gone: " + scanErr.Error()
			if err != nil {
				why = err.Error() + "; " + why
			}
			w.end(st, rowFailed, "old versions", u+" on "+k+": "+why)
			continue
		}
		if !slices.ContainsFunc(still, func(o oldUser) bool { return o.Name == u }) {
			w.end(st, rowDone, "old versions", "removed "+u+" on "+k+"; its home is archived under /root there. "+
				"Revoke the old install's GitHub tokens and Linear key yourself")
			continue
		}
		why := "the root pass did not remove it"
		if err != nil {
			why = err.Error()
		}
		w.end(st, rowFailed, "old versions", u+" on "+k+": "+why)
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

const readingHost = "reading flocc --version, status, doctor"

func (w *wizard) checkHost(h hosts.Host) (hostState, bool) {
	row := w.d.ui.Step(h.Name)
	row.Detail(readingHost)
	st := w.ops.readHost(w.ctx, h)
	if st.Err != "" {
		detail := "could not read it: " + st.Err
		if h.SSH != "" && w.ops.offline(w.ctx, saved.HostPart(h.SSH)) {
			detail += "\nits tailnet node is offline; if the host is gone, remove the node at https://login.tailscale.com/admin/machines and rm " + hostFile(w.d, h.Name)
		}
		w.end(row, rowFailed, h.Name, detail)
		return st, false
	}
	if why := w.shipWhy(st); why != "" {
		row.Detail("shipping the checkout: " + why)
		if err := w.ops.ship(w.ctx, h.Name); err != nil {
			w.failAt(row, h.Name, fmt.Errorf("ship: %w", err))
			return st, false
		}
		row.Detail(readingHost)
		if st = w.ops.readHost(w.ctx, h); st.Err != "" {
			w.end(row, rowFailed, h.Name, "after ship, could not read it: "+st.Err)
			return st, false
		}
		if why := w.shipWhy(st); why != "" {
			w.end(row, rowFailed, h.Name, "still, after ship: "+why)
			return st, false
		}
	}
	if st.LowDisk != "" {
		w.end(row, rowPending, h.Name, st.LowDisk+"\nfree space there; flocc does not delete worktrees for you")
		return st, true
	}
	w.end(row, rowDone, h.Name, "flocc "+st.Version+", doctor ok")
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

// row ends a step that had no work to show. A Ctrl-C during an op cancels w.ctx, and the op's
// error need not wrap context.Canceled, so the run stops at the first row after it.
func (w *wizard) row(state rowState, name, detail string) {
	w.end(w.d.ui.Step(name), state, name, detail)
}

// end ends st in state. A pending or failed row is also the owner's to act on, so its text goes to
// the to-do box.
func (w *wizard) end(st *ui.Step, state rowState, name, detail string) {
	switch state {
	case rowDone:
		st.Done(detail)
	case rowSkipped:
		st.Skip(detail)
	case rowPending:
		st.Pending(detail)
		w.pending = true
		w.d.ui.Todo(name + ": " + detail)
	default:
		st.Fail(errors.New(detail))
		w.d.ui.Todo(name + ": " + detail)
	}
	if w.ctx.Err() != nil {
		w.stopped = true
	}
}

// fail records err as name's failure. A cancelled prompt or an empty paste stops the whole run.
func (w *wizard) fail(name string, err error) { w.failAt(w.d.ui.Step(name), name, err) }

func (w *wizard) failAt(st *ui.Step, name string, err error) {
	if errors.Is(err, huh.ErrUserAborted) || errors.Is(err, context.Canceled) || errors.Is(err, keys.ErrStopped) {
		w.stopped = true
	}
	w.end(st, rowFailed, name, err.Error())
}

// summary is the exit code; cli.run closes the session, which draws the record.
func (w *wizard) summary() int {
	if w.stopped {
		w.d.ui.Todo("Stopped. Run flocc setup again to go on.")
		return 1
	}
	if w.pending || w.d.ui.Failed() {
		return 1
	}
	return 0
}

func (w *wizard) confirm(title string, def bool, facts ...string) (bool, error) {
	d := "no"
	if def {
		d = "yes"
	}
	return w.d.ui.Confirm(ui.Question{Title: title, Facts: facts, Default: d})
}

func (w *wizard) ask(title, help string) (string, error) {
	return w.d.ui.Ask(ui.Question{Title: title, Help: help})
}

func (w *wizard) checkout() bool {
	w.d.ui.Group("Checkout")
	st := w.d.ui.Step("checkout")
	root, v, err := w.ops.checkout()
	if err != nil {
		w.end(st, rowFailed, "checkout", err.Error())
		return false
	}
	ver, ok := host.Version(v)
	if !ok {
		w.end(st, rowFailed, "checkout", fmt.Sprintf("%s: version %q is not x.y.z", root, v))
		return false
	}
	w.version = ver
	w.end(st, rowDone, "checkout", root+" at "+v)
	w.ops.seedEnv(root)
	return true
}

// fillKeys asks for the keys in slots the store lacks; with check it then checks every key
// against what the host files map.
func (w *wizard) fillKeys(slots []keys.Slot, check bool) bool {
	w.d.ui.Group("Keys")
	st := w.d.ui.Step("keys")
	st.Detail("looking for the keys the store lacks")
	err := w.ops.envKeys(w.ctx, slots)
	if err == nil {
		err = w.ops.fillKeys(w.ctx, slots)
	}
	if err != nil {
		w.failAt(st, "keys", err)
		return false
	}
	var titles []string
	for _, s := range slots {
		titles = append(titles, s.Title())
	}
	if check {
		st.Detail("checking every key")
		if p := w.ops.checkKeys(w.ctx); len(p) > 0 {
			w.end(st, rowPending, "keys", strings.Join(p, "\n"))
			return true
		}
	}
	w.end(st, rowDone, "keys", strings.Join(titles, ", "))
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
	w.d.ui.Group("Hosts")
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
		again, err := w.confirm("Continue creating "+name+"? A host create for it stopped before the host was added", false)
		if err != nil {
			w.fail("host", err)
			return false
		}
		if again {
			return w.createHost(name)
		}
	}
	i, err := w.d.ui.Choose(ui.Question{Title: "No hosts yet"}, []string{
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
	name, err := w.ask("Host name", "2 to 63 lowercase letters, digits and '-'; also its Incus and Tailscale name")
	if err != nil {
		w.fail("host", err)
		return false
	}
	return w.createHost(strings.TrimSpace(name))
}

func (w *wizard) createHost(name string) bool {
	m, err := w.ask("Machine to make "+name+" on", "the ssh destination of your account there, such as you@box")
	if err != nil {
		w.fail("host", err)
		return false
	}
	spec, err := create.ParseArgs([]string{strings.TrimSpace(m), name})
	if err != nil {
		w.row(rowFailed, "host", err.Error())
		return false
	}
	st := w.d.ui.Step("host")
	st.Detail("looking at " + spec.Machine)
	ms := w.probe(spec.Machine)
	switch {
	case ms.Err != nil:
		w.end(st, rowFailed, "host", ms.Err.Error())
		return false
	case !ms.Sudo:
		w.end(st, rowFailed, "host", fmt.Sprintf("host create needs passwordless sudo on %s: its firewall and Docker steps run sudo -n even to check. "+
			"Add a file in /etc/sudoers.d/ there with visudo, or add a host made another way", spec.Machine))
		return false
	case ms.Incus != nil:
		w.end(st, rowFailed, "host", ms.Incus.Error())
		return false
	}
	st.Detail("creating")
	if err := w.ops.create(w.ctx, spec); err != nil {
		w.failAt(st, "host", fmt.Errorf("create %s: %w", name, err))
		return false
	}
	w.end(st, rowDone, "host", "created "+name+" on "+spec.Machine)
	return true
}

func (w *wizard) addExisting() bool {
	name, err := w.ask("Host name", "what flocc calls it, and its file name")
	if err != nil {
		w.fail("host", err)
		return false
	}
	reach, err := w.ask("How to reach it", "an ssh destination such as root@box, or daytona:<sandbox>")
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
	st := w.d.ui.Step("host")
	st.Detail("adding " + name)
	if err := w.ops.add(w.ctx, name, h); err != nil {
		w.failAt(st, "host", fmt.Errorf("host add %s: %w", name, err))
		return false
	}
	st.Detail("shipping the checkout to " + name)
	if err := w.ops.ship(w.ctx, name); err != nil {
		w.failAt(st, "host", fmt.Errorf("ship %s: %w", name, err))
		return false
	}
	w.end(st, rowDone, "host", "added and shipped "+name)
	return true
}

// placeHost asks once which machine holds an ssh host and saves the answer, none included. An
// empty answer saves nothing, so it is asked again next run.
func (w *wizard) placeHost(h *hosts.Host) {
	if h.Machine != "" || h.Daytona != "" {
		return
	}
	m, err := w.ask("Which machine holds "+h.Name+"?",
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
