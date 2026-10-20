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
		"Linear → Settings → Account → Security & access → New API key, with Full access. Limit it to team NF."},
	{"agent_token", "gh-agent.token", "GitHub agent token (read-only)",
		"github.com/settings/personal-access-tokens, fine-grained. Resource owner: the mapped repos' owner; " +
			"repository access: the mapped repos; permissions, all Read-only: Contents, Pull requests, Issues, Commit statuses, Actions. " +
			"Agents use it through gh, so it must not be able to push."},
	{"publish_token", "gh-publish.token", "GitHub publish token",
		"Fine-grained, same owner and repos. Contents: Read and write; Pull requests: Read and write. " +
			"Only the service uses it, to push branches and open PRs."},
}

// SecretFile is the file under ~/.config/flocc holding the secret with this answers id; "" for
// an unknown id.
func SecretFile(id string) string {
	for _, s := range secretSpecs {
		if s.id == id {
			return s.file
		}
	}
	return ""
}

var envPrefix = regexp.MustCompile(`^[A-Z_]+=`)

// CleanSecret strips what a paste adds and refuses what doctor would reject.
func CleanSecret(v string) (string, error) {
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
	if _, err := CleanSecret(string(b)); err != nil {
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
	return e.HomePath(filepath.Join(".config/flocc", s.file))
}

// KnownSecrets is every secret this run can hold: the scripted answers' and the values on disk,
// which include what the interactive form just wrote. Output that leaves the host redacts them all.
func (e *Env) KnownSecrets() []string {
	var v []string
	if e.Answers != nil {
		v = e.Answers.Secrets.Values()
	}
	for _, s := range secretSpecs {
		if b, err := os.ReadFile(secretPath(e, s)); err == nil {
			if x := strings.TrimSpace(string(b)); x != "" {
				v = append(v, x)
			}
		}
	}
	return v
}

func scriptedSecret(e *Env, id string) string {
	if e.Answers == nil || e.Answers.Secrets == nil {
		return ""
	}
	switch id {
	case "linear_key":
		return e.Answers.Secrets.LinearKey
	case "agent_token":
		return e.Answers.Secrets.AgentToken
	case "publish_token":
		return e.Answers.Secrets.PublishToken
	}
	return ""
}

// Check fails on a scripted value that differs from the file: a ship never asks PickRedo, so
// passing here would drop the desktop's keys (a rotate's "run flocc ship" fallback relies on it).
func (Secrets) Check(_ context.Context, e *Env) (bool, string, error) {
	for _, s := range secretSpecs {
		path := secretPath(e, s)
		if ok, why := validSecret(path); !ok {
			return false, s.file + ": " + why, nil
		}
		if x := strings.TrimSpace(scriptedSecret(e, s.id)); x != "" {
			b, _ := os.ReadFile(path)
			if v, err := CleanSecret(x); err != nil || v != strings.TrimSpace(string(b)) {
				return false, s.file + ": differs from the answers", nil
			}
		}
	}
	return true, "3 secrets, mode 600", nil
}

// writeSecret replaces path by rename, so the service, which reads the file on every tick, never
// sees it missing or half written. CreateTemp makes the file 0600.
func writeSecret(path, v string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(v + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (Secrets) Apply(_ context.Context, e *Env) error {
	if err := os.MkdirAll(e.HomePath(".config/flocc"), 0o700); err != nil {
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
		v, err := CleanSecret(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", s.title, err)
		}
		if err := writeSecret(path, v); err != nil {
			return err
		}
	}
	return nil
}
