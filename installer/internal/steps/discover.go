package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/NovusEdge/linear-agents/installer/internal/answers"
	"github.com/NovusEdge/linear-agents/installer/internal/host"
)

// ParseRepos reads the output of `gh api --paginate`, which is one JSON array per page,
// concatenated. Archived repos are dropped.
func ParseRepos(b []byte) ([]answers.Repo, error) {
	var repos []answers.Repo
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		var page []struct {
			Name          string `json:"name"`
			HTMLURL       string `json:"html_url"`
			DefaultBranch string `json:"default_branch"`
			Private       bool   `json:"private"`
			Archived      bool   `json:"archived"`
		}
		if err := dec.Decode(&page); errors.Is(err, io.EOF) {
			return repos, nil
		} else if err != nil {
			return nil, err
		}
		for _, r := range page {
			if r.Archived {
				continue
			}
			// Private repos of a GitHub Free organization cannot have draft PRs.
			repos = append(repos, answers.Repo{Name: r.Name, URL: r.HTMLURL, BaseBranch: r.DefaultBranch,
				Check: "just check", Draft: !r.Private})
		}
	}
}

// MergeRepos puts saved answers over discovered repos with the same URL and keeps saved
// repos the token cannot see, such as ones added by URL. Names become config keys and Linear
// labels, so of two repos with one name (different owners) the first stays and the second's
// URL is returned in skipped.
func MergeRepos(found, saved []answers.Repo) (out []answers.Repo, skipped []string) {
	urls, names := map[string]bool{}, map[string]bool{}
	add := func(r answers.Repo) {
		urls[r.URL] = true
		if names[r.Name] {
			skipped = append(skipped, r.URL)
			return
		}
		names[r.Name] = true
		out = append(out, r)
	}
	for _, f := range found {
		for _, s := range saved {
			if s.URL == f.URL {
				f = s
			}
		}
		add(f)
	}
	for _, s := range saved {
		if s.URL != "" && !urls[s.URL] {
			add(s)
		}
	}
	return out, skipped
}

func (e *Env) discoverRepos(ctx context.Context) ([]answers.Repo, error) {
	tok, err := os.ReadFile(e.HomePath(".config/linear-agents/gh-agent.token"))
	if err != nil {
		return nil, err
	}
	// The token goes in the environment: the runner logs argv.
	out, err := e.MustCmd(ctx, host.Cmd{Name: "gh", Env: []string{"GH_TOKEN=" + strings.TrimSpace(string(tok))},
		Args: []string{"api", "--paginate", "/user/repos?per_page=100&affiliation=owner,collaborator,organization_member"}})
	if err != nil {
		return nil, err
	}
	return ParseRepos([]byte(out))
}
