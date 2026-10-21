package answers

import (
	"crypto/rand"
	"errors"
	"fmt"
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
	// A one-off tagged key the desktop minted, for a host that joins the tailnet in this run.
	TailscaleAuthKey string `toml:"tailscale_auth_key,omitempty"`
}

// Values is every secret that is set, for redacting logs and output.
func (s *Secrets) Values() []string {
	if s == nil {
		return nil
	}
	var v []string
	for _, x := range []string{s.LinearKey, s.AgentToken, s.PublishToken, s.TailscaleAuthKey} {
		if x != "" {
			v = append(v, x)
		}
	}
	return v
}

// Permissions is the host's [permissions] table. flocc reads it from permissions.toml beside
// config.toml; the keys match its loader.
type Permissions struct {
	Allow          []string `toml:"allow,omitempty"`
	Environment    []string `toml:"environment,omitempty"`
	JudgeModel     string   `toml:"judge_model,omitempty"`
	JudgeMaxPerRun int      `toml:"judge_max_per_run,omitempty"`
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
	SharedTeam  bool         `toml:"shared_team,omitempty"`
	Permissions *Permissions `toml:"permissions,omitempty"`
	// Read only from an --answers file, for runs without a terminal; Save drops it.
	Secrets *Secrets `toml:"secrets,omitempty"`
}

func Load(path string) (*Answers, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Answers{}, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse reads an answers document. Its errors give only the position: go-toml's own message
// quotes the offending line, which can hold a token.
func Parse(b []byte) (*Answers, error) {
	a := &Answers{}
	err := toml.Unmarshal(b, a)
	if err == nil {
		return a, nil
	}
	var de *toml.DecodeError
	if errors.As(err, &de) {
		row, col := de.Position()
		return nil, fmt.Errorf("answers: invalid TOML at line %d, column %d", row, col)
	}
	return nil, errors.New("answers: a value does not fit its field; check the types against the example in docs/vm-setup.md")
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

var ntfyURLPrefix = regexp.MustCompile(`(?i)^(https?://)?ntfy\.sh/`)

// NormalizeTopic trims whitespace and a pasted ntfy.sh URL prefix (any scheme case, one
// trailing slash), then checks ntfy's topic rule (docs.ntfy.sh/publish): up to 64
// characters of [-_A-Za-z0-9]. Empty input stays empty so callers can fall back to a
// generated topic; a URL with no topic is rejected.
func NormalizeTopic(s string) (string, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return "", nil
	}
	if loc := ntfyURLPrefix.FindStringIndex(t); loc != nil {
		t = strings.TrimSuffix(t[loc[1]:], "/")
	}
	if ntfyTopic.MatchString(t) {
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
	return "flocc-" + string(b)
}

// OverlaySaved overlays a saved answers file. A bad saved topic is dropped and returned as
// dropped, so the form asks for it again instead of locking the owner out of fixing it.
func (a *Answers) OverlaySaved(saved *Answers) (dropped error) {
	if _, err := NormalizeTopic(saved.NtfyTopic); err != nil {
		saved.NtfyTopic, dropped = "", err
	}
	// Cannot fail: the topic is the only error Overlay returns, and it is valid or empty here.
	_ = a.Overlay(saved)
	return dropped
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
	if o.Permissions != nil {
		a.Permissions = o.Permissions
	}
	if o.Secrets != nil {
		a.Secrets = o.Secrets
	}
	return nil
}
