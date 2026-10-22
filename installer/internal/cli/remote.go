package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// readOnly are the host commands -H all may run: they change nothing on any host.
var readOnly = []string{"status", "doctor", "logs"}

var issueKey = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]+$`)

// splitHost takes -H and --host out of args. Everything after "--" is left alone, so a raw
// command for host run keeps its own flags.
func splitHost(args []string) (name string, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return name, append(rest, args[i:]...), nil
		}
		var v string
		switch {
		case a == "-H" || a == "--host":
			if i+1 == len(args) {
				return "", nil, fmt.Errorf("%s needs a host name", a)
			}
			i++
			v = args[i]
		case strings.HasPrefix(a, "-H="), strings.HasPrefix(a, "--host="):
			_, v, _ = strings.Cut(a, "=")
		default:
			rest = append(rest, a)
			continue
		}
		if name != "" {
			return "", nil, errors.New("-H given twice")
		}
		name = v
	}
	return name, rest, nil
}

// listHosts is every host file that loads; each one that does not is shown as a note, once per
// run, so one bad file hides no other host.
func listHosts(d *deps) []hosts.Host {
	all, err := hosts.List(hosts.Dir(d.home))
	if err != nil {
		switch {
		case d.ui == nil:
			for _, line := range strings.Split(err.Error(), "\n") {
				fmt.Fprintln(d.err, "hosts: "+line)
			}
		case d.hostsNote != err.Error():
			d.hostsNote = err.Error()
			d.ui.Note("hosts", d.hostsNote)
		}
	}
	return all
}

func names(hs []hosts.Host) []string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Name)
	}
	return out
}

// and joins names the way the messages read: "a", "a and b", "a, b and c".
func and(ns []string) string {
	if len(ns) < 2 {
		return strings.Join(ns, "")
	}
	return strings.Join(ns[:len(ns)-1], ", ") + " and " + ns[len(ns)-1]
}

// pickHost: -H, then $FLOCC_HOST, then the only host, then the host holding the issue named.
func pickHost(ctx context.Context, d *deps, name string, args []string) (hosts.Host, error) {
	if name == "all" {
		return hosts.Host{}, fmt.Errorf("-H all runs only %s; name one host", strings.Join(readOnly, ", "))
	}
	if name == "" {
		name = d.getenv("FLOCC_HOST")
		if name == "all" {
			return hosts.Host{}, errors.New("FLOCC_HOST cannot be all; use -H all for status, doctor or logs")
		}
	}
	all := listHosts(d)
	if name != "" {
		if i := slices.IndexFunc(all, func(h hosts.Host) bool { return h.Name == name }); i >= 0 {
			return all[i], nil
		}
		return hosts.Host{}, fmt.Errorf("no host named %s; flocc host add %s", name, name)
	}
	switch len(all) {
	case 0:
		return hosts.Host{}, errors.New("no hosts yet; flocc host add <name>")
	case 1:
		return all[0], nil
	}
	i := slices.IndexFunc(args, issueKey.MatchString)
	if i < 0 {
		return hosts.Host{}, fmt.Errorf("name a host with -H: %s", strings.Join(names(all), ", "))
	}
	key := args[i]
	var holders, reached []hosts.Host
	var unreachable []string
	// One at a time, as for -H all: Statuses fetches in parallel and keeps ssh's stderr.
	for _, h := range all {
		if err := connectFirst(ctx, d, h); err != nil {
			unreachable = append(unreachable, err.Error())
			continue
		}
		reached = append(reached, h)
	}
	for j, row := range ship.Statuses(ctx, d.ui, d.run, reached, key) {
		if row.Report == nil {
			why := reached[j].Name
			if row.Err != "" {
				why += ": " + row.Err
			}
			unreachable = append(unreachable, why)
			continue
		}
		if slices.ContainsFunc(row.Report.Claims, func(c ship.Claim) bool { return c.Issue == key }) {
			holders = append(holders, reached[j])
		}
	}
	switch len(holders) {
	case 1:
		return holders[0], nil
	case 0:
		msg := fmt.Sprintf("no reachable host holds %s", key)
		if len(unreachable) > 0 {
			msg += " (unreachable: " + strings.Join(unreachable, "; ") + ")"
		}
		return hosts.Host{}, fmt.Errorf("%s; name one with -H: %s", msg, strings.Join(names(all), ", "))
	}
	return hosts.Host{}, fmt.Errorf("%s is claimed on %s; name one with -H", key, and(names(holders)))
}

// connectFirst opens h's shared connection before Statuses reads it. A Daytona host is left to
// Statuses: it mints its own token, and the connection is keyed by the token, so one opened here
// would not be shared.
func connectFirst(ctx context.Context, d *deps, h hosts.Host) error {
	if h.Daytona != "" {
		return nil
	}
	dest, err := d.resolve(ctx, h.Target())
	if err != nil {
		return err
	}
	dest.Opts = append(slices.Clone(dest.Opts), "-o", "ConnectTimeout=10")
	return ship.Connect(ctx, d.run, dest, h.Name, d.tty)
}

func onHost(ctx context.Context, d *deps, name string, args []string) int {
	h, err := pickHost(ctx, d, name, args)
	if err != nil {
		fmt.Fprintln(d.err, "flocc:", err)
		return 2
	}
	dest, err := d.resolve(ctx, h.Target())
	if err == nil {
		err = ship.OnHostWhy(ctx, d.run, dest, ship.FloccLine(args), d.tty, "-")
	}
	return ship.ExitCode(err, "flocc", d.showErr)
}

// follows reports whether logs flags ask to follow. The host parser is argparse, which accepts
// abbreviations of --follow and clusters of short flags, so exact matches are not enough. Any
// short cluster with an f is refused, even -n5foo where the f is a value: a false refusal is
// cheap and a follow under Capture hangs until Ctrl-C.
func follows(flags []string) bool {
	for _, a := range flags {
		switch {
		case a == "--":
			return false
		case strings.HasPrefix(a, "--"):
			if len(a) >= 4 && strings.HasPrefix("--follow", a) {
				return true
			}
		case strings.HasPrefix(a, "-") && strings.Contains(a, "f"):
			return true
		}
	}
	return false
}

// onAll runs a read-only command on every host at once and prints each host's output whole.
func onAll(ctx context.Context, d *deps, args []string) int {
	switch {
	case !slices.Contains(readOnly, args[0]):
		fmt.Fprintf(d.err, "flocc: -H all runs only %s; name one host for %s\n", strings.Join(readOnly, ", "), args[0])
		return 2
	case args[0] == "logs" && follows(args[1:]):
		fmt.Fprintln(d.err, "flocc: logs -f follows one host; name it with -H")
		return 2
	case args[0] == "status":
		return d.fleet(ctx, d.ui, args[1:])
	}
	all := listHosts(d)
	if len(all) == 0 {
		fmt.Fprintln(d.err, "flocc: no hosts yet; flocc host add <name>")
		return 2
	}
	outs := make([]hostOut, len(all))
	dests := make([]*ship.Dest, len(all))
	steps := make([]*ui.Step, len(all))
	// One at a time: two hosts' approval prompts on one terminal would interleave.
	for i, h := range all {
		steps[i] = d.ui.Step(h.Name)
		steps[i].Detail("connect")
		dest, err := d.resolve(ctx, h.Target())
		if err == nil {
			// Connect goes host by host and the output draws together after wg.Wait, so one
			// blackholed host must time out fast.
			dest.Opts = append(slices.Clone(dest.Opts), "-o", "ConnectTimeout=10")
			err = ship.Connect(ctx, d.run, dest, h.Name, d.tty)
		}
		if err != nil {
			outs[i].code = 1
			steps[i].Fail(err)
			continue
		}
		dests[i] = &dest
	}
	var wg sync.WaitGroup
	for i, dest := range dests {
		if dest == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			steps[i].Detail(args[0])
			if args[0] == "doctor" {
				outs[i] = doctorOf(ctx, d, all[i], *dest, steps[i])
			} else {
				outs[i] = logsOf(ctx, d, *dest, args, steps[i])
			}
		}()
	}
	wg.Wait()
	code := 0
	for i, h := range all {
		o := outs[i]
		code = max(code, o.code)
		if dests[i] == nil {
			continue
		}
		d.ui.Group(h.Name)
		ui.Rows(d.ui, o.rows)
		if o.text != "" {
			d.ui.Note("", o.text)
		}
		if o.todo != "" {
			d.ui.Todo(o.todo)
		}
	}
	return code
}

// hostOut is what one host contributed to -H all: check rows, or its text as it printed it, and a
// to-do when its flocc is too old to report checks.
type hostOut struct {
	rows []ui.Event
	text string
	todo string
	code int
}

// doctorReport is flocc doctor --json; v is 0 or missing from a host that predates it.
type doctorReport struct {
	V      int `json:"v"`
	Checks []struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Warn   bool   `json:"warn"`
		Detail string `json:"detail"`
	} `json:"checks"`
}

func doctorOf(ctx context.Context, d *deps, h hosts.Host, dest ship.Dest, st *ui.Step) hostOut {
	res, err := ship.Capture(ctx, d.run, dest, ship.FloccLine([]string{"doctor", "--json"}), nil)
	if err != nil {
		st.Fail(err)
		return hostOut{code: 1}
	}
	var rep doctorReport
	if json.Unmarshal([]byte(res.Stdout), &rep) != nil || rep.V == 0 {
		// An older flocc refuses the flag or ignores it; either way its text report is the answer.
		return doctorText(ctx, d, h, dest, st)
	}
	o := hostOut{}
	failed := 0
	for _, c := range rep.Checks {
		e := ui.Event{Kind: ui.KStep, Name: sanitize(c.Name), State: ui.Done, Detail: sanitize(c.Detail)}
		switch {
		case !c.OK:
			e.State, e.Err = ui.Failed, e.Detail
			failed++
		case c.Warn:
			e.State = ui.Pending
		}
		o.rows = append(o.rows, e)
	}
	if failed > 0 || res.Code != 0 {
		o.code = 1
	}
	st.Done(fmt.Sprintf("%d checks, %d failed", len(rep.Checks), failed))
	return o
}

// doctorText is the fallback for a host without doctor --json.
func doctorText(ctx context.Context, d *deps, h hosts.Host, dest ship.Dest, st *ui.Step) hostOut {
	o := logsOf(ctx, d, dest, []string{"doctor"}, st)
	o.todo = "flocc ship -H " + h.Name + " to update it"
	return o
}

func logsOf(ctx context.Context, d *deps, dest ship.Dest, args []string, st *ui.Step) hostOut {
	res, err := ship.Capture(ctx, d.run, dest, ship.FloccLine(args), nil)
	if err != nil {
		st.Fail(err)
		return hostOut{code: 1}
	}
	o := hostOut{text: sanitize(strings.TrimRight(res.Out(), "\n"))}
	if res.Code != 0 {
		o.code = 1
		st.Fail(fmt.Errorf("exit %d", res.Code))
	} else {
		st.Done("")
	}
	return o
}

// sanitize drops the control characters of text from a host, so none of its escape sequences reach
// the terminal; newlines and tabs stay, and unlike saved.Sanitize nothing is cut short.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
