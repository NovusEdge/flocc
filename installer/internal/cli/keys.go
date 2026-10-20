package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/githubapi"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
	"github.com/NovusEdge/flocc/installer/internal/keys"
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
	}
	fmt.Fprint(d.err, usage)
	return 2
}
