package steps

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"regexp"
	"strings"

	"github.com/NovusEdge/linear-agents/deploy"
	"github.com/NovusEdge/linear-agents/installer/internal/answers"
	"github.com/NovusEdge/linear-agents/installer/internal/host"
	"github.com/pelletier/go-toml/v2"
)

// Read on every fetch, so a replaced token takes effect without touching the clone.
const credentialHelper = `!f() { test "$1" = get && printf 'username=x-access-token\npassword=%s\n' "$(cat "$HOME/.config/linear-agents/gh-agent.token")"; }; f`

var (
	repoName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

func RenderConfig(example []byte, a *answers.Answers) ([]byte, error) {
	var m map[string]any
	if err := toml.Unmarshal(example, &m); err != nil {
		return nil, err
	}
	m["team_key"] = a.TeamKey
	m["digest_issue"] = a.DigestIssue
	m["ntfy_url"] = "https://ntfy.sh/" + a.NtfyTopic
	// The example's [repos.backend] and [mcp_servers.sentry] are placeholders.
	delete(m, "mcp_servers")
	repos := map[string]any{}
	for _, r := range a.Repos {
		repos[r.Name] = map[string]any{"path": "~/src/" + r.Name, "base_branch": r.BaseBranch, "check": r.Check, "draft": r.Draft}
	}
	m["repos"] = repos
	return toml.Marshal(m)
}

// servicePath is the PATH the tick unit sets. A sudo -iu login shell may lack ~/.local/bin
// (Debian adds it only if it existed at login, Arch never), and doctor looks tools up on PATH.
func servicePath(home string) string {
	return fmt.Sprintf("PATH=%[1]s/.local/bin:%[1]s/.local/share/mise/shims:/usr/local/bin:/usr/bin:/bin", home)
}

// doctor's ok is false when the command could not run or exited non-zero; that can happen with
// no FAIL line (missing binary, Python traceback), so callers must not read empty fails as healthy.
func doctor(ctx context.Context, e *Env) (out string, fails []string, ok bool) {
	r, err := e.Run.Run(ctx, host.Cmd{Name: e.Real(".local/bin/linear-agents"), Args: []string{"doctor"}, Env: []string{servicePath(e.Home)}})
	for _, line := range strings.Split(r.Stdout, "\n") {
		if strings.HasPrefix(line, "FAIL") {
			fails = append(fails, line)
		}
	}
	ok = err == nil && r.Code == 0
	out = r.Stdout
	if !ok && len(fails) == 0 {
		if err != nil {
			out += err.Error() + "\n"
		}
		out += host.Tail(r.Stderr, 20)
	}
	return out, fails, ok
}

func randomTopic() string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 24)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[n.Int64()]
	}
	return "la-" + string(b)
}

type Config struct{}

func (Config) Name() string { return "Config" }

func (Config) Check(ctx context.Context, e *Env) (bool, string, error) {
	out, fails, _ := doctor(ctx, e)
	if !strings.Contains("\n"+out, "\nok    config:") {
		return false, "no valid config", nil
	}
	for _, f := range fails {
		// A failed fetch is transient; the Run step's doctor reports it.
		if strings.HasPrefix(f, "FAIL  repo ") && strings.Contains(f, "is not a git clone") {
			return false, strings.TrimPrefix(f, "FAIL  "), nil
		}
	}
	return true, "config loads, repos cloned", nil
}

func validate(a *answers.Answers) error {
	if a.TeamKey == "" || a.DigestIssue == "" || len(a.Repos) == 0 {
		return errors.New("team key, digest issue and at least one repo are required")
	}
	for _, r := range a.Repos {
		if !repoName.MatchString(r.Name) {
			return fmt.Errorf("repo name %q: use letters, digits, '.', '_' or '-', matching its Linear label", r.Name)
		}
		if !answers.ValidGitHubURL(r.URL) {
			return fmt.Errorf("repo %s: %q is not an https://github.com/<owner>/<repo> URL", r.Name, r.URL)
		}
	}
	return nil
}

func (Config) Apply(ctx context.Context, e *Env) error {
	a := e.Answers
	if a.TeamKey == "" {
		a.TeamKey = "NF"
	}
	if a.NtfyTopic == "" {
		a.NtfyTopic = randomTopic()
	}
	if len(a.Repos) == 0 {
		a.Repos = []answers.Repo{{BaseBranch: "main", Check: "just check", Draft: true}}
	}
	if err := e.UI.ConfigForm(a); err != nil {
		return err
	}
	// Saved before validating, so a typo does not discard everything typed.
	if err := e.SaveAnswers(); err != nil {
		return err
	}
	if err := validate(a); err != nil {
		return err
	}
	example, _ := deploy.Files.ReadFile("config.example.toml")
	rendered, err := RenderConfig(example, a)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(e.HomePath(".config/linear-agents"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(e.HomePath(".config/linear-agents/config.toml"), rendered, 0o600); err != nil {
		return err
	}
	ntfy := "https://ntfy.sh/" + a.NtfyTopic
	if _, err := e.Must(ctx, "curl", "-fsS", "-H", "Priority: 5", "-H", "Title: linear-agents", "-d", "test push from la-setup", ntfy); err != nil {
		return err
	}
	arrived, err := e.UI.Confirm("Subscribe to "+ntfy+" in the ntfy app. Did the priority 5 test push arrive?", true)
	if err != nil {
		return err
	}
	if !arrived {
		return errors.New("the ntfy test push did not arrive at " + ntfy + "; subscribe to it in the app and run again")
	}
	for _, r := range a.Repos {
		dir := e.Real("src/" + r.Name)
		if _, err := os.Stat(e.HomePath("src/" + r.Name + "/.git")); err != nil {
			if _, err := e.Must(ctx, "git", "-c", "credential.helper=", "-c", "credential.helper="+credentialHelper, "clone", "-q", r.URL, dir); err != nil {
				return err
			}
		}
		if _, err := e.Must(ctx, "git", "-C", dir, "config", "--replace-all", "credential.helper", ""); err != nil {
			return err
		}
		if _, err := e.Must(ctx, "git", "-C", dir, "config", "--add", "credential.helper", credentialHelper); err != nil {
			return err
		}
	}
	return nil
}
