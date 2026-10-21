package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/githubapi"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

func keysEnv(d *deps) keys.Env {
	return keys.Env{Store: d.store(), Prompt: d.prompt, UI: d.ui, Open: d.open, HTTP: d.http,
		GitHubAPI: d.githubAPI, LinearAPI: d.linearAPI, NtfyBase: d.ntfyBase, NewTopic: d.newTopic}
}

// mapped is every team, repo and digest issue the host files map, each once.
func mapped(hs []hosts.Host) keys.Mapped {
	var m keys.Mapped
	for _, h := range hs {
		if h.TeamKey != "" && !slices.Contains(m.Teams, h.TeamKey) {
			m.Teams = append(m.Teams, h.TeamKey)
		}
		if h.DigestIssue != "" && !slices.Contains(m.Digests, h.DigestIssue) {
			m.Digests = append(m.Digests, h.DigestIssue)
		}
		for _, r := range h.Repos {
			slug := strings.TrimPrefix(answers.NormalizeURL(r.URL), "https://github.com/")
			if !slices.ContainsFunc(m.Repos, func(t githubapi.Target) bool { return t.Slug == slug }) {
				m.Repos = append(m.Repos, githubapi.Target{Slug: slug, Base: r.BaseBranch})
			}
		}
	}
	return m
}

func looksLikeKey(s string) bool {
	return slices.ContainsFunc([]string{"lin_api_", "github_pat_", "ghp_", "gho_"}, func(p string) bool { return strings.HasPrefix(s, p) })
}

// report turns the next step of each problem keys.Check found into a todo, once, and returns the
// exit status. Check has already failed the problem's row.
func report(d *deps, problems []string) int {
	var seen []string
	for _, p := range problems {
		if _, next := ui.SplitNext(p); next != "" && !slices.Contains(seen, next) {
			seen = append(seen, next)
			d.ui.Todo(next)
		}
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}

func keysCmd(ctx context.Context, d *deps, args []string) int {
	if slices.ContainsFunc(args, looksLikeKey) {
		fmt.Fprintln(d.err, "flocc keys: a key on the command line stays in your shell history; run flocc keys and paste it at the prompt")
		return 2
	}
	m := mapped(listHosts(d))
	switch {
	case len(args) == 0:
		e := keysEnv(d)
		if err := keys.Fill(ctx, e, m, keys.Slots); err != nil {
			d.ui.Step("Fill keys").Fail(err)
			d.ui.Todo("run flocc keys again to go on")
			return 1
		}
		return report(d, keys.Check(ctx, e, m))
	case len(args) == 1 && args[0] == "check":
		e := keysEnv(d)
		// A desktop whose keyring once failed stays on secrets.toml; this shows it.
		d.ui.Step("Keys store").Done(e.Store.Where())
		return report(d, keys.Check(ctx, e, m))
	case len(args) == 2 && args[0] == "rotate":
		return rotate(ctx, d, args[1])
	}
	fmt.Fprint(d.err, usage)
	return 2
}

func rotate(ctx context.Context, d *deps, which string) int {
	var slots []keys.Slot
	switch which {
	case "github":
		slots = []keys.Slot{keys.Agent, keys.Publish}
	case "linear":
		slots = []keys.Slot{keys.Linear}
	default:
		fmt.Fprintln(d.err, "usage: flocc keys rotate github|linear")
		return 2
	}
	all := listHosts(d)
	changed, err := keys.Replace(ctx, keysEnv(d), mapped(all), slots)
	code := 0
	if err != nil {
		d.ui.Step("Rotate keys").Fail(err)
		code = 1
	}
	if len(changed) == 0 {
		if err != nil {
			return code
		}
		// A push that failed on some host leaves the new key in the store and the old one there,
		// and a rerun pastes nothing, so the stored keys are offered to the hosts again.
		var perr error
		if changed, perr = offerRepush(d, all, slots); perr != nil {
			d.ui.Step("Rotate keys").Fail(perr)
			return 1
		}
		if len(changed) == 0 {
			d.ui.Step("Rotate keys").Skip("nothing changed")
			return 0
		}
	}
	failed := false
	pushed := 0
	for _, h := range all {
		st := d.ui.Step("Push keys to " + h.Name)
		// Ctrl-C must not stop a push once a key is stored: the hosts would hold the old key
		// and the desktop the new one.
		if err := pushKeys(context.WithoutCancel(ctx), d, h, d.store(), changed); err != nil {
			st.Fail(err)
			failed = true
			continue
		}
		pushed++
		st.Done("")
	}
	if pushed > 0 && which == "github" {
		d.ui.Todo("revoke the old token once the sessions running now end: each read GH_TOKEN when it started")
		d.ui.Note("Token expiry", "flocc status -H all shows the new expiry after each host's next daily token check.")
	}
	if failed {
		d.ui.Todo(fmt.Sprintf("once the hosts that failed are up, run flocc keys rotate %s again, paste nothing and answer yes to push the stored keys again, or run flocc ship -H <host>", which))
		return 1
	}
	return code
}

// offerRepush asks whether to write the stored keys among slots to every host again, and returns
// the slots to push.
func offerRepush(d *deps, all []hosts.Host, slots []keys.Slot) ([]keys.Slot, error) {
	var stored []keys.Slot
	for _, s := range slots {
		if v, err := d.store().Get(s); err == nil && v != "" {
			stored = append(stored, s)
		}
	}
	if len(all) == 0 || len(stored) == 0 {
		return nil, nil
	}
	var names []string
	for _, h := range all {
		names = append(names, h.Name)
	}
	again, err := d.prompt.Confirm("Push the stored keys to "+strings.Join(names, ", ")+" again?", false)
	if err != nil || !again {
		return nil, err
	}
	return stored, nil
}

// pushKeys writes each changed key into the host's secret file as the service user. The value
// goes on stdin through Capture, which forbids a pty, and the file is written beside the old one
// and renamed over it, mode 600. The time limit leaves out the connection's opening, which may
// wait for the owner to approve it in a browser.
func pushKeys(ctx context.Context, d *deps, h hosts.Host, st keys.Store, slots []keys.Slot) error {
	rctx, cancel := context.WithTimeout(ctx, time.Minute)
	dest, err := d.resolve(rctx, h.Target())
	cancel()
	if err != nil {
		return err
	}
	if err := ship.Connect(ctx, d.run, dest, h.Name, d.tty); err != nil {
		return err
	}
	ctx, cancel = context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for _, s := range slots {
		v, err := st.Get(s)
		if err != nil {
			return err
		}
		file := steps.SecretFile(string(s))
		f := ".config/flocc/" + file
		script := []string{"umask 077 && cat > " + f + ".new && mv " + f + ".new " + f}
		res, err := ship.Capture(ctx, d.run, dest, script, strings.NewReader(v+"\n"))
		if err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		if res.Code != 0 {
			return fmt.Errorf("%s: exit %d: %s", file, res.Code, strings.TrimSpace(res.Out()))
		}
	}
	return nil
}
