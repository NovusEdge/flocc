package answers

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
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

func (a *Answers) Overlay(o *Answers) {
	for _, f := range []struct {
		dst *string
		src string
	}{
		{&a.Hostname, o.Hostname}, {&a.TeamKey, o.TeamKey}, {&a.DigestIssue, o.DigestIssue}, {&a.NtfyTopic, o.NtfyTopic},
	} {
		if f.src != "" {
			*f.dst = f.src
		}
	}
	if len(o.Repos) > 0 {
		a.Repos = o.Repos
	}
	if o.Secrets != nil {
		a.Secrets = o.Secrets
	}
}
