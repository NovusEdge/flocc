package answers

import (
	"crypto/rand"
	"errors"
	"io/fs"
	"math/big"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Repo struct {
	Name       string `toml:"name"`
	URL        string `toml:"url"`
	BaseBranch string `toml:"base_branch"`
	Check      string `toml:"check"`
	Draft      bool   `toml:"draft"`
}

var githubURL = regexp.MustCompile(`^https://github\.com/[^/]+/[^/]+?(\.git)?/?$`)

func ValidGitHubURL(u string) bool { return githubURL.MatchString(u) }

// NameFromURL is the repo segment of a GitHub URL without ".git"; "" if u is not one.
func NameFromURL(u string) string {
	if !ValidGitHubURL(u) {
		return ""
	}
	return strings.TrimSuffix(path.Base(strings.TrimSuffix(u, "/")), ".git")
}

// NormalizeURL is the form every URL comparison uses: lowercase, no ".git", no trailing
// slash. GitHub ignores case, and discovery's html_url, saved answers and typed URLs
// differ in all three. A string that is not a GitHub repo URL is only trimmed.
func NormalizeURL(u string) string {
	u = strings.TrimSpace(u)
	if !ValidGitHubURL(u) {
		return u
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git"))
}

// SameURL reports whether a and b name the same repo.
func SameURL(a, b string) bool { return NormalizeURL(a) == NormalizeURL(b) }

func containsURL(list []string, u string) bool {
	return slices.ContainsFunc(list, func(x string) bool { return SameURL(x, u) })
}

// Preselected is the URLs of found repos that are not excluded.
func Preselected(found []Repo, excluded []string) []string {
	var urls []string
	for _, r := range found {
		if !containsURL(excluded, r.URL) {
			urls = append(urls, r.URL)
		}
	}
	return urls
}

// UpdateExcluded returns the excluded URLs, normalized, after the user ticked chosen among
// found: unticked found repos join, re-ticked ones leave, URLs not offered this time stay.
func UpdateExcluded(excluded []string, found []Repo, chosen []string) []string {
	offered := map[string]bool{}
	var out []string
	for _, r := range found {
		offered[NormalizeURL(r.URL)] = true
		if !containsURL(chosen, r.URL) {
			out = append(out, NormalizeURL(r.URL))
		}
	}
	for _, u := range excluded {
		if !offered[NormalizeURL(u)] {
			out = append(out, NormalizeURL(u))
		}
	}
	return out
}

// Include removes u from excluded.
func Include(excluded []string, u string) []string {
	return slices.DeleteFunc(slices.Clone(excluded), func(x string) bool { return SameURL(x, u) })
}

// Exclude adds u to excluded unless it is already there.
func Exclude(excluded []string, u string) []string {
	if containsURL(excluded, u) {
		return excluded
	}
	return append(slices.Clone(excluded), NormalizeURL(u))
}

type Secrets struct {
	LinearKey    string `toml:"linear_key"`
	AgentToken   string `toml:"agent_token"`
	PublishToken string `toml:"publish_token"`
}

type Answers struct {
	Hostname    string `toml:"hostname,omitempty"`
	TeamKey     string `toml:"team_key,omitempty"`
	DigestIssue string `toml:"digest_issue,omitempty"`
	NtfyTopic   string `toml:"ntfy_topic,omitempty"`
	Repos       []Repo `toml:"repos,omitempty"`
	// URLs of discovered repos the user unticked, so a rerun does not tick them again
	// while repos created since still arrive ticked. A GitHub rename or transfer changes the
	// URL, so the repo comes back ticked.
	ExcludedRepos []string `toml:"excluded_repos,omitempty"`
	// Other hosts handle the repos left unmapped here; renders skip_unmapped.
	SharedTeam bool `toml:"shared_team,omitempty"`
	// Read only from an --answers file, for runs without a terminal; Save drops it.
	Secrets *Secrets `toml:"secrets,omitempty"`
}

func Load(path string) (*Answers, error) {
	a := &Answers{}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	return a, toml.Unmarshal(b, a)
}

func (a *Answers) Save(path string) error {
	c := *a
	c.Secrets = nil
	b, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

var ntfyTopic = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

const ntfyPrefix = "https://ntfy.sh/"

// NormalizeTopic trims whitespace and a pasted https://ntfy.sh/ prefix, then checks ntfy's
// topic rule (docs.ntfy.sh/publish): up to 64 characters of [-_A-Za-z0-9]. An empty
// input stays empty so callers can fall back to a generated topic.
func NormalizeTopic(s string) (string, error) {
	t := strings.TrimPrefix(strings.TrimSpace(s), ntfyPrefix)
	if t == "" || ntfyTopic.MatchString(t) {
		return t, nil
	}
	return "", errors.New("ntfy topic: use 1-64 letters, digits, '-' or '_'; paste the topic only, not the URL")
}

func RandomTopic() string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 24)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[n.Int64()]
	}
	return "la-" + string(b)
}

// Overlay applies o's set fields. A bad ntfy topic fails before anything changes, so an
// --answers file or a saved answers file cannot smuggle one past the form.
func (a *Answers) Overlay(o *Answers) error {
	topic, err := NormalizeTopic(o.NtfyTopic)
	if err != nil {
		return err
	}
	for _, f := range []struct {
		dst *string
		src string
	}{
		{&a.Hostname, o.Hostname}, {&a.TeamKey, o.TeamKey}, {&a.DigestIssue, o.DigestIssue}, {&a.NtfyTopic, topic},
	} {
		if f.src != "" {
			*f.dst = f.src
		}
	}
	if len(o.Repos) > 0 {
		a.Repos = o.Repos
	}
	if len(o.ExcludedRepos) > 0 {
		a.ExcludedRepos = o.ExcludedRepos
	}
	if o.SharedTeam {
		a.SharedTeam = true
	}
	if o.Secrets != nil {
		a.Secrets = o.Secrets
	}
	return nil
}
