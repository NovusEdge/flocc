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
)

func keysEnv(d *deps) keys.Env {
	return keys.Env{Store: d.store(), Prompt: d.prompt, Out: d.out, Open: d.open, QR: d.qr, HTTP: d.http,
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

// report prints the problems keys.Check found and returns the exit status.
func report(d *deps, problems []string) int {
	for _, p := range problems {
		fmt.Fprintln(d.err, "FAIL  "+p)
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
			fmt.Fprintf(d.err, "flocc keys: %v; run flocc keys again to go on\n", err)
			return 1
		}
		return report(d, keys.Check(ctx, e, m))
	case len(args) == 1 && args[0] == "check":
		e := keysEnv(d)
		// A desktop whose keyring once failed stays on secrets.toml; this shows it.
		fmt.Fprintf(d.out, "Keys are in %s.\n", e.Store.Where())
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
		fmt.Fprintln(d.err, "flocc keys rotate:", err)
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
			fmt.Fprintln(d.err, "flocc keys rotate:", perr)
			return 1
		}
		if len(changed) == 0 {
			fmt.Fprintln(d.out, "Nothing changed.")
			return 0
		}
	}
	var failed []string
	pushed := 0
	for _, h := range all {
		// Ctrl-C must not stop a push once a key is stored: the hosts would hold the old key
		// and the desktop the new one.
		err := pushKeys(context.WithoutCancel(ctx), d, h, d.store(), changed)
		if err != nil {
			failed = append(failed, h.Name+": "+err.Error())
			continue
		}
		pushed++
		fmt.Fprintf(d.out, "Pushed to %s.\n", h.Name)
	}
	if pushed > 0 && which == "github" {
		fmt.Fprintln(d.out, "Do not revoke the old token until the sessions running now end: each read GH_TOKEN when it started.")
		fmt.Fprintln(d.out, "flocc status -H all shows the new expiry after each host's next daily token check.")
	}
	if len(failed) > 0 {
		fmt.Fprintf(d.err, "Not pushed; once these are up, run flocc keys rotate %s again, paste nothing and answer yes to push the stored keys again, or run flocc ship -H <host>:\n  %s\n",
			which, strings.Join(failed, "\n  "))
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
