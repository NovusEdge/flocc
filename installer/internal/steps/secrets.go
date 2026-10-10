package steps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type secretSpec struct{ id, file, title, help string }

var secretSpecs = []secretSpec{
	{"linear_key", "linear.key", "Linear API key",
		"Linear → Settings → Security & access → Personal API keys. Limit it to team NF."},
	{"agent_token", "gh-agent.token", "GitHub agent token (read-only)",
		"github.com/settings/personal-access-tokens, fine-grained. Resource owner: the mapped repos' owner; " +
			"repository access: the mapped repos; permissions, all Read-only: Contents, Pull requests, Checks, Issues. " +
			"Agents use it through gh, so it must not be able to push."},
	{"publish_token", "gh-publish.token", "GitHub publish token",
		"Fine-grained, same owner and repos. Contents: Read and write; Pull requests: Read and write. " +
			"Only the service uses it, to push branches and open PRs."},
}

var envPrefix = regexp.MustCompile(`^[A-Z_]+=`)

// cleanSecret strips what a paste adds and refuses what doctor would reject.
func cleanSecret(v string) (string, error) {
	v = strings.TrimSpace(v)
	switch {
	case strings.ContainsAny(v, "\r\n"):
		return "", errors.New("paste a single line")
	case envPrefix.MatchString(v):
		return "", errors.New("paste only the value, not NAME=value")
	case strings.ContainsAny(v, " \t"):
		return "", errors.New("it contains spaces")
	}
	return v, nil
}

func validSecret(path string) (bool, string) {
	st, err := os.Stat(path)
	if err != nil {
		return false, "missing"
	}
	b, _ := os.ReadFile(path)
	if _, err := cleanSecret(string(b)); err != nil {
		return false, err.Error()
	}
	if strings.TrimSpace(string(b)) == "" {
		return false, "empty"
	}
	if p := st.Mode().Perm(); p&0o077 != 0 {
		return false, fmt.Sprintf("mode %o", p)
	}
	return true, ""
}

type Secrets struct{}

func (Secrets) Name() string { return "Secrets" }

func secretPath(e *Env, s secretSpec) string {
	return e.HomePath(filepath.Join(".config/linear-agents", s.file))
}

func (Secrets) Check(_ context.Context, e *Env) (bool, string, error) {
	for _, s := range secretSpecs {
		if ok, why := validSecret(secretPath(e, s)); !ok {
			return false, s.file + ": " + why, nil
		}
	}
	return true, "3 secrets, mode 600", nil
}

func (Secrets) Apply(_ context.Context, e *Env) error {
	if err := os.MkdirAll(e.HomePath(".config/linear-agents"), 0o700); err != nil {
		return err
	}
	for _, s := range secretSpecs {
		path := secretPath(e, s)
		valid, _ := validSecret(path)
		title := s.title
		if valid {
			title += " (leave empty to keep the current one)"
		}
		raw, err := e.UI.Secret(s.id, title, s.help)
		if err != nil {
			return err
		}
		if strings.TrimSpace(raw) == "" {
			if valid {
				continue
			}
			return errors.New(s.title + " is required")
		}
		v, err := cleanSecret(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", s.title, err)
		}
		// WriteFile keeps the mode of an existing file, so a 644 file would briefly hold the token.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.WriteFile(path, []byte(v+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}
