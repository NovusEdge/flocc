package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/githubapi"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/linearapi"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// hostCmd is flocc host <sub>: args is the line after "host", name the -H value.
func hostCmd(ctx context.Context, d *deps, name string, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(d.err, usage)
		return 2
	}
	switch args[0] {
	case "run":
		return hostRun(ctx, d, name, args[1:])
	case "create":
		return hostCreate(ctx, d, args[1:])
	case "add", "import":
		reach, rest, err := reachFlags(args[1:])
		if err == nil && args[0] == "add" && len(rest) != 1 {
			err = errors.New("usage: flocc host add <name> [--ssh DEST | --daytona SANDBOX]")
		} else if err == nil && args[0] == "import" {
			if name == "" && len(rest) == 1 {
				name = rest[0]
			}
			if name == "" {
				err = errors.New("usage: flocc host import -H <name> [--ssh DEST | --daytona SANDBOX]")
			}
		}
		if err != nil {
			fmt.Fprintf(d.err, "flocc host %s: %v\n", args[0], err)
			return 1
		}
		// A failure after the arguments is a failed step of the session, so it is not printed again.
		if args[0] == "add" {
			err = addHost(ctx, d, rest[0], reach)
		} else {
			err = importHost(ctx, d, name, reach)
		}
		if err != nil {
			return 1
		}
		return 0
	}
	fmt.Fprint(d.err, usage)
	return 2
}

// hostRun is flocc host run -H <name> -- <command...>: a raw command, not the host's flocc.
func hostRun(ctx context.Context, d *deps, name string, args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(d.err, "usage: flocc host run -H <name> -- <command...>")
		return 2
	}
	h, err := pickHost(ctx, d, name, nil)
	if err != nil {
		fmt.Fprintln(d.err, "flocc:", err)
		return 2
	}
	dest, err := d.resolve(ctx, h.Target())
	if err == nil {
		err = ship.OnHost(ctx, d.run, dest, args, d.tty)
	}
	return ship.ExitCode(err, "flocc")
}

// reachFlags takes --ssh and --daytona wherever they sit: the host name comes first, and the
// flag package stops at it.
func reachFlags(args []string) (hosts.Host, []string, error) {
	var h hosts.Host
	var rest []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--ssh", "--daytona":
			if i+1 == len(args) {
				return h, nil, fmt.Errorf("%s needs a value", a)
			}
			i++
			if a == "--ssh" {
				h.SSH = args[i]
			} else {
				h.Daytona = args[i]
			}
		default:
			rest = append(rest, a)
		}
	}
	if h.SSH != "" && h.Daytona != "" {
		return h, nil, errors.New("give --ssh or --daytona, not both")
	}
	return h, rest, nil
}

func hostFile(d *deps, name string) string {
	return filepath.Join(hosts.Dir(d.home), name+".toml")
}

// hostAdd is flocc host add <name> [--ssh DEST]; host create calls it with its machine.
func hostAdd(ctx context.Context, d *deps, name, sshDest, machine string) error {
	return addHost(ctx, d, name, hosts.Host{SSH: sshDest, Machine: machine})
}

// loadOrNew is the host's file, or a new host reached as root@<name>; reach overrides either.
func loadOrNew(d *deps, name string, reach hosts.Host) (hosts.Host, error) {
	h, err := hosts.Load(hosts.Dir(d.home), name)
	if errors.Is(err, os.ErrNotExist) {
		h, err = hosts.Host{Name: name, SSH: "root@" + name}, nil
	}
	if reach.SSH != "" || reach.Daytona != "" {
		h.SSH, h.Daytona = reach.SSH, reach.Daytona
	}
	if reach.Machine != "" {
		h.Machine = reach.Machine
	}
	return h, err
}

const digestTitle = "flocc digest"

const digestBody = "flocc hosts post their daily digest here: tier 3 reports and token warnings. Pin this issue."

// stepped runs fn as the row name: Done with the detail fn returns, or Failed with its error,
// which comes back unchanged.
func stepped(d *deps, name string, fn func(st *ui.Step) (string, error)) error {
	st := d.ui.Step(name)
	detail, err := fn(st)
	if err != nil {
		st.Fail(err)
		return err
	}
	st.Done(detail)
	return nil
}

// addHost makes or updates the host file. Every failure is a failed step of d.ui before it is
// returned.
func addHost(ctx context.Context, d *deps, name string, reach hosts.Host) error {
	var h hosts.Host
	if err := stepped(d, "Host file", func(*ui.Step) (string, error) {
		var err error
		if h, err = loadOrNew(d, name, reach); err == nil && h.Hostname == "" {
			h.Hostname = name
		}
		return hostFile(d, name), err
	}); err != nil {
		return err
	}
	// The repos and teams come from what these two keys can see, so they are asked for first.
	e := keysEnv(d)
	var lk, ag string
	if err := stepped(d, "Keys", func(st *ui.Step) (string, error) {
		st.Detail("checking the Linear and GitHub agent keys")
		if err := keys.Fill(ctx, e, mapped(listHosts(d)), []keys.Slot{keys.Linear, keys.Agent}); err != nil {
			return "", err
		}
		store := d.store()
		var err error
		if lk, err = store.Get(keys.Linear); err != nil {
			return "", err
		}
		ag, err = store.Get(keys.Agent)
		return "Linear, GitHub agent", err
	}); err != nil {
		return err
	}
	gh := &githubapi.Client{Base: d.githubAPI, Token: ag, HTTP: d.http}
	lin := &linearapi.Client{URL: d.linearAPI, Key: lk, HTTP: d.http}
	if err := stepped(d, "Repos", func(st *ui.Step) (string, error) {
		st.Detail("listing the repos the agent token sees")
		err := pickRepos(ctx, d, gh, &h)
		return fmt.Sprintf("%d mapped", len(h.Repos)), err
	}); err != nil {
		return err
	}
	var team linearapi.Team
	if err := stepped(d, "Linear team", func(st *ui.Step) (string, error) {
		st.Detail("reading the teams the Linear key sees")
		var err error
		team, err = pickTeam(ctx, d, lin, &h)
		return team.Key, err
	}); err != nil {
		return err
	}
	if err := stepped(d, "Issues", func(st *ui.Step) (string, error) {
		st.Detail("looking for the digest issue")
		err := ensureDigest(ctx, d, lin, team, &h)
		return h.DigestIssue, err
	}); err != nil {
		return err
	}
	if err := stepped(d, "Labels", func(st *ui.Step) (string, error) { return ensureLabels(ctx, lin, team, h, st) }); err != nil {
		return err
	}
	if err := stepped(d, "Save host", func(*ui.Step) (string, error) {
		return hostFile(d, name), hosts.Save(hosts.Dir(d.home), h)
	}); err != nil {
		return err
	}
	// Asked for after the save, so the publish token's page lists the repos just mapped.
	m := mapped(listHosts(d))
	return stepped(d, "Keys", func(st *ui.Step) (string, error) {
		st.Detail("checking the publish token and ntfy topic")
		if err := keys.Fill(ctx, e, m, []keys.Slot{keys.Publish, keys.Ntfy}); err != nil {
			return "", fmt.Errorf("the host is saved; %w", err)
		}
		if problems := keys.Check(ctx, e, m); len(problems) > 0 {
			return "", fmt.Errorf("the host is saved, but the keys do not cover it yet:\n  %s", strings.Join(problems, "\n  "))
		}
		return "every key covers the host", nil
	})
}

func pickRepos(ctx context.Context, d *deps, gh *githubapi.Client, h *hosts.Host) error {
	raw, err := gh.ListRepos(ctx)
	if err != nil {
		return keys.RotateHint(err)
	}
	repos, maybeEmpty, dropped, err := steps.ParseRepos(raw)
	if err != nil {
		return err
	}
	if len(dropped) > 0 {
		d.ui.Note("Some owners are not listed",
			"Not listed: repos of "+strings.Join(dropped, ", ")+", which the agent token can see only publicly. Add any you want by URL.")
	}
	found, skipped := steps.MergeRepos(repos, h.Repos)
	if len(skipped) > 0 {
		d.ui.Note("Skipped repos whose name another repo already has", strings.Join(skipped, "\n"))
	}
	// Left unticked unless already mapped: cloning an empty repo fails.
	for _, u := range maybeEmpty {
		if !slices.ContainsFunc(h.Repos, func(r answers.Repo) bool { return answers.SameURL(r.URL, u) }) {
			h.ExcludedRepos = answers.Exclude(h.ExcludedRepos, u)
		}
	}
	defaults := func(u string) (string, bool) {
		r, err := gh.Repo(ctx, strings.TrimPrefix(answers.NormalizeURL(u), "https://github.com/"))
		if err != nil || r.DefaultBranch == "" {
			return "main", true
		}
		// Private repos of a GitHub Free organization cannot have draft PRs.
		return r.DefaultBranch, !r.Private
	}
	return d.prompt.Repos(&h.Answers, found, defaults)
}

func pickTeam(ctx context.Context, d *deps, lin *linearapi.Client, h *hosts.Host) (linearapi.Team, error) {
	v, err := lin.Viewer(ctx)
	if err != nil {
		return linearapi.Team{}, keys.RotateHint(err)
	}
	if i := slices.IndexFunc(v.Teams, func(t linearapi.Team) bool { return t.Key == h.TeamKey }); i >= 0 {
		return v.Teams[i], nil
	}
	var t linearapi.Team
	switch len(v.Teams) {
	case 0:
		return t, errors.New("the Linear key sees no team")
	case 1:
		t = v.Teams[0]
	default:
		if t, err = d.prompt.Team(v.Teams); err != nil {
			return t, err
		}
	}
	h.TeamKey = t.Key
	return t, nil
}

// openIssue reports whether key is an open issue: false with no error when Linear has no such
// issue or it is closed, and then why says which. Any other error comes back, so a blip at Linear
// never replaces a working digest: an imported one is not titled "flocc digest", and falling
// through would make a second.
func openIssue(ctx context.Context, lin *linearapi.Client, key string) (ok bool, why string, err error) {
	is, err := lin.Issue(ctx, key)
	switch {
	case errors.Is(err, linearapi.ErrNotFound):
		return false, "Linear has no such issue", nil
	case err != nil:
		return false, "", err
	case !is.Open():
		return false, "its state is " + is.StateType, nil
	}
	return true, "", nil
}

// ensureDigest keeps an open digest issue, else takes the open one another host on the team
// uses, else finds or creates the team's open "flocc digest".
func ensureDigest(ctx context.Context, d *deps, lin *linearapi.Client, team linearapi.Team, h *hosts.Host) error {
	if h.DigestIssue != "" {
		ok, why, err := openIssue(ctx, lin, h.DigestIssue)
		if err != nil || ok {
			return err
		}
		d.ui.Note("Digest", fmt.Sprintf("Not keeping %s's digest %s: %s.", h.Name, h.DigestIssue, why))
	}
	for _, o := range listHosts(d) {
		if o.Name == h.Name || o.TeamKey != team.Key || o.DigestIssue == "" || o.DigestIssue == h.DigestIssue {
			continue
		}
		ok, why, err := openIssue(ctx, lin, o.DigestIssue)
		if err != nil {
			return err
		}
		if ok {
			h.DigestIssue = o.DigestIssue
			return nil
		}
		d.ui.Note("Digest", fmt.Sprintf("Not using %s's digest %s: %s.", o.Name, o.DigestIssue, why))
	}
	is, ok, err := lin.FindOpenIssue(ctx, team.Key, digestTitle)
	if err != nil {
		return err
	}
	if !ok {
		if is, err = lin.CreateIssue(ctx, team.ID, digestTitle, digestBody); err != nil {
			return err
		}
		d.ui.Todo(fmt.Sprintf("Created %s, the digest issue; pin it in Linear.", is.Key))
	}
	h.DigestIssue = is.Key
	return nil
}

// ensureLabels creates the team's missing labels and says which it made.
func ensureLabels(ctx context.Context, lin *linearapi.Client, team linearapi.Team, h hosts.Host, st *ui.Step) (string, error) {
	names := []string{"claude", "escalation", "needs-decision"}
	for _, r := range h.Repos {
		names = append(names, r.Name)
	}
	st.Detail("looking for the labels in " + team.Key)
	missing, err := lin.MissingLabels(ctx, team.Key, names)
	if err != nil {
		return "", err
	}
	for _, n := range missing {
		st.Detail("creating label " + n)
		if err := lin.CreateLabel(ctx, team.ID, n); err != nil {
			return "", err
		}
	}
	if len(missing) == 0 {
		return "all present in " + team.Key, nil
	}
	return "created " + strings.Join(missing, ", ") + " in " + team.Key, nil
}

// importHost copies a host's saved answers into its host file: for a host shipped before host
// files held answers, and after an --interactive ship. The host's ntfy topic becomes the
// desktop's when the desktop has none, so a subscription the owner already has keeps working,
// and so does each key the desktop lacks: GitHub shows a token only once, so without this a
// host moved to the desktop store would need new ones.
func importHost(ctx context.Context, d *deps, name string, reach hosts.Host) error {
	var h hosts.Host
	var dest ship.Dest
	if err := stepped(d, "Connect", func(st *ui.Step) (string, error) {
		var err error
		if h, err = loadOrNew(d, name, reach); err != nil {
			return "", err
		}
		st.Detail("opening the connection to " + h.Target())
		if dest, err = d.resolve(ctx, h.Target()); err != nil {
			return "", err
		}
		// The import has no time limit, so a blackholed host must fail on its own.
		dest.Opts = append(slices.Clone(dest.Opts), "-o", "ConnectTimeout=10")
		return h.Target(), ship.Connect(ctx, d.run, dest, name, d.tty)
	}); err != nil {
		return err
	}
	var a *answers.Answers
	if err := stepped(d, "Read config", func(*ui.Step) (string, error) {
		res, err := ship.Capture(ctx, d.run, dest, []string{"cat", ".config/flocc/answers.toml"}, nil)
		if err != nil {
			return "", err
		}
		if res.Code != 0 {
			return "", fmt.Errorf("reading the host's answers: %s", strings.TrimSpace(res.Out()))
		}
		a, err = answers.Parse([]byte(res.Stdout))
		return "answers.toml", err
	}); err != nil {
		return err
	}
	if err := stepped(d, "Save host", func(*ui.Step) (string, error) {
		detail := "imported into " + hostFile(d, name)
		store := d.store()
		switch cur, err := store.Get(keys.Ntfy); {
		case err != nil || a.NtfyTopic == "" || cur == a.NtfyTopic:
		case cur == "":
			if err := store.Set(keys.Ntfy, a.NtfyTopic); err != nil {
				return "", err
			}
			detail += "; kept the host's ntfy topic as the desktop's"
		default:
			// Ship sends the desktop's topic, so the phone following the host's would go quiet.
			d.ui.Todo(fmt.Sprintf("The desktop's ntfy topic differs from %s's; the next ship moves %s to the desktop's (https://ntfy.sh/%s).", name, name, cur))
		}
		h.Answers = *a
		h.NtfyTopic, h.Secrets = "", nil
		return detail, hosts.Save(hosts.Dir(d.home), h)
	}); err != nil {
		return err
	}
	return stepped(d, "Keys", func(st *ui.Step) (string, error) {
		return "the desktop has every key the host had", adoptKeys(ctx, d, h, dest, st)
	})
}

// adoptKeys reads each key the store lacks from the host's secret file as the service user and
// stores it once it passes the checks a paste gets. The values reach neither argv nor output:
// Capture forbids a pty, and only the slot's title, the check's note and its error are printed.
func adoptKeys(ctx context.Context, d *deps, h hosts.Host, dest ship.Dest, step *ui.Step) error {
	e := keysEnv(d)
	m := mapped(listHosts(d))
	for _, s := range []keys.Slot{keys.Linear, keys.Agent, keys.Publish} {
		cur, err := e.Store.Get(s)
		if err != nil {
			return err
		}
		if cur != "" {
			continue
		}
		file := ".config/flocc/" + steps.SecretFile(string(s))
		step.Detail("reading " + file + " on " + h.Name)
		res, err := ship.Capture(ctx, d.run, dest, []string{"cat", file}, nil)
		if err != nil {
			return err
		}
		// A lost connection must not pass for a missing file: the desktop would then ask for a
		// new token, and GitHub shows one only once. The ControlPersist master can expire while
		// the owner sits at a prompt, so the answers read proves nothing about this one.
		switch res.Code {
		case 0:
		case 1:
			continue // cat found no file: the host never had it; the desktop asks where it is needed
		case 255:
			return fmt.Errorf("could not reach %s (%s); run flocc host import again", h.Name, strings.TrimSpace(res.Stderr))
		default:
			return fmt.Errorf("reading %s on %s: %s; run flocc host import again", file, h.Name, strings.TrimSpace(res.Stderr))
		}
		if err := keys.Adopt(ctx, e, m, s, res.Stdout, h.Name); err != nil {
			return err
		}
	}
	return nil
}
