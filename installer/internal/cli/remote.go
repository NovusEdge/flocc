package cli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/ship"
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

// listHosts is every host file that loads; each one that does not is printed as a note, so one
// bad file hides no other host.
func listHosts(d *deps) []hosts.Host {
	all, err := hosts.List(hosts.Dir(d.home))
	if err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			fmt.Fprintln(d.err, "hosts: "+line)
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
	all := listHosts(d)
	if name == "" {
		name = d.getenv("FLOCC_HOST")
	}
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
	var holders []hosts.Host
	var unreachable []string
	for j, row := range ship.Statuses(ctx, d.run, all) {
		if row.Report == nil {
			why := all[j].Name
			if row.Err != "" {
				why += ": " + row.Err
			}
			unreachable = append(unreachable, why)
			continue
		}
		if slices.ContainsFunc(row.Report.Claims, func(c ship.Claim) bool { return c.Issue == key }) {
			holders = append(holders, all[j])
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

func onHost(ctx context.Context, d *deps, name string, args []string) int {
	h, err := pickHost(ctx, d, name, args)
	if err != nil {
		fmt.Fprintln(d.err, "flocc:", err)
		return 2
	}
	dest, err := d.resolve(ctx, h.Target())
	if err == nil {
		err = ship.OnHost(ctx, d.run, dest, ship.FloccLine(args), d.tty)
	}
	return ship.ExitCode(err, "flocc")
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
		return d.fleet(args[1:])
	}
	all := listHosts(d)
	if len(all) == 0 {
		fmt.Fprintln(d.err, "flocc: no hosts yet; flocc host add <name>")
		return 2
	}
	results := make([]host.Result, len(all))
	var wg sync.WaitGroup
	for i, h := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dest, err := d.resolve(ctx, h.Target())
			if err == nil {
				// Results print together after wg.Wait, so one blackholed host must time out fast.
				dest.Opts = append(slices.Clone(dest.Opts), "-o", "ConnectTimeout=10")
				results[i], err = ship.Capture(ctx, d.run, dest, ship.FloccLine(args), nil)
			}
			if err != nil {
				results[i] = host.Result{Stderr: err.Error(), Code: 255}
			}
		}()
	}
	wg.Wait()
	code := 0
	for i, h := range all {
		out := results[i].Out()
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		fmt.Fprintf(d.out, "== %s ==\n%s", h.Name, out)
		if results[i].Code != 0 {
			fmt.Fprintf(d.out, "(exit %d)\n", results[i].Code)
			code = 1
		}
	}
	return code
}
