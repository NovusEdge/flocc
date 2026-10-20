package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
)

// ParseRepos reads the output of `gh api --paginate`, which is one JSON array per page,
// concatenated. Archived and disabled repos and any without a default branch are dropped:
// none can be cloned and checked out. Repos with size 0 are kept, since the API also reports
// 0 for small or new repos, but their URLs come back in maybeEmpty so the caller can warn.
//
// GitHub lists every public repo the user owns or belongs to for any token, so a token scoped
// to one org still sees hundreds of unrelated public repos. Private repos appear only if the
// token was granted them, so only owners with a private repo in the result are kept. With no
// private repo visible, everything is kept. The owners left out come back sorted in dropped.
func ParseRepos(b []byte) (repos []answers.Repo, maybeEmpty, dropped []string, err error) {
	var all []apiRepo
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		var page []apiRepo
		if err := dec.Decode(&page); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, nil, nil, err
		}
		all = append(all, page...)
	}
	// Counted before the archived/disabled filter: an archived private repo still shows the grant.
	granted, anyPrivate := map[string]bool{}, false
	for _, r := range all {
		if r.Private {
			granted[r.owner()] = true
			anyPrivate = true
		}
	}
	for _, r := range all {
		if r.Archived || r.Disabled || r.DefaultBranch == "" {
			continue
		}
		if anyPrivate && !granted[r.owner()] {
			if !slices.Contains(dropped, r.owner()) {
				dropped = append(dropped, r.owner())
			}
			continue
		}
		repos = append(repos, r.repo())
		if r.Size == 0 {
			maybeEmpty = append(maybeEmpty, r.HTMLURL)
		}
	}
	slices.Sort(dropped)
	return repos, maybeEmpty, dropped, nil
}

type apiRepo struct {
	Name          string `json:"name"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	Disabled      bool   `json:"disabled"`
	Size          int    `json:"size"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func (r apiRepo) owner() string {
	if r.Owner.Login != "" {
		return strings.ToLower(r.Owner.Login)
	}
	return strings.ToLower(strings.SplitN(strings.TrimPrefix(r.HTMLURL, "https://github.com/"), "/", 2)[0])
}

func (r apiRepo) repo() answers.Repo {
	// Private repos of a GitHub Free organization cannot have draft PRs.
	return answers.Repo{Name: r.Name, URL: r.HTMLURL, BaseBranch: r.DefaultBranch, Check: "just check", Draft: !r.Private}
}

// ParseRepoDefaults reads one `gh api repos/<owner>/<repo>` object. A repo with no default
// branch yields main, which is also the fallback when the lookup fails.
func ParseRepoDefaults(b []byte) (base string, draft bool) {
	var r apiRepo
	if json.Unmarshal(b, &r) != nil || r.DefaultBranch == "" {
		return "main", true
	}
	return r.DefaultBranch, !r.Private
}

// MergeRepos puts saved answers over discovered repos with the same URL and keeps saved
// repos the token cannot see, such as ones added by URL. Names become config keys and Linear
// labels, so of two repos with one name (different owners) the first stays and the second's
// URL is returned in skipped.
func MergeRepos(found, saved []answers.Repo) (out []answers.Repo, skipped []string) {
	urls, names := map[string]bool{}, map[string]bool{}
	add := func(r answers.Repo) {
		urls[answers.NormalizeURL(r.URL)] = true
		if names[r.Name] {
			skipped = append(skipped, r.URL)
			return
		}
		names[r.Name] = true
		out = append(out, r)
	}
	for _, f := range found {
		for _, s := range saved {
			if answers.SameURL(s.URL, f.URL) {
				f = s
			}
		}
		add(f)
	}
	for _, s := range saved {
		if s.URL != "" && !urls[answers.NormalizeURL(s.URL)] {
			add(s)
		}
	}
	return out, skipped
}

func (e *Env) ghAPI(ctx context.Context, args ...string) (string, error) {
	tok, err := os.ReadFile(e.HomePath(".config/flocc/gh-agent.token"))
	if err != nil {
		return "", err
	}
	// The token goes in the environment: the runner logs argv.
	return e.MustCmd(ctx, host.Cmd{Name: "gh", Env: []string{"GH_TOKEN=" + strings.TrimSpace(string(tok))},
		Args: append([]string{"api"}, args...)})
}

func (e *Env) discoverRepos(ctx context.Context) ([]answers.Repo, []string, []string, error) {
	out, err := e.ghAPI(ctx, "--paginate", "/user/repos?per_page=100&affiliation=owner,collaborator,organization_member")
	if err != nil {
		return nil, nil, nil, err
	}
	return ParseRepos([]byte(out))
}

// repoDefaults is the base branch and draft setting of a repo added by URL.
func (e *Env) repoDefaults(ctx context.Context, url string) (string, bool) {
	slug := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSuffix(url, "/"), "https://github.com/"), ".git")
	out, err := e.ghAPI(ctx, "repos/"+slug)
	if err != nil {
		return "main", true
	}
	return ParseRepoDefaults([]byte(out))
}
