package steps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/deploy"
	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/host"
	"github.com/pelletier/go-toml/v2"
)

// Read on every fetch, so a replaced token takes effect without touching the clone.
const credentialHelper = `!f() { test "$1" = get && printf 'username=x-access-token\npassword=%s\n' "$(cat "$HOME/.config/flocc/gh-agent.token")"; }; f`

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
	if a.SharedTeam {
		m["skip_unmapped"] = true
	}
	return toml.Marshal(m)
}

// ServicePath is the PATH=... assignment the tick unit sets. A sudo -iu login shell may lack ~/.local/bin
// (Debian adds it only if it existed at login, Arch never), and doctor looks tools up on PATH.
func ServicePath(home string) string {
	return fmt.Sprintf("PATH=%[1]s/.local/bin:%[1]s/.local/share/mise/shims:/usr/local/bin:/usr/bin:/bin", home)
}

// doctor's ok is false when the command could not run or exited non-zero; that can happen with
// no FAIL line (missing binary, Python traceback), so callers must not read empty fails as healthy.
func doctor(ctx context.Context, e *Env) (out string, fails []string, ok bool) {
	r, err := e.Run.Run(ctx, host.Cmd{Name: e.Real(".local/bin/flocc"), Args: []string{"doctor"}, Env: []string{ServicePath(e.Home)}})
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
	names, urls := map[string]bool{}, map[string]bool{}
	for _, r := range a.Repos {
		if !repoName.MatchString(r.Name) || r.Name == "." || r.Name == ".." {
			return fmt.Errorf("repo name %q: use letters, digits, '.', '_' or '-', matching its Linear label", r.Name)
		}
		if !answers.ValidGitHubURL(r.URL) {
			return fmt.Errorf("repo %s: %q is not an https://github.com/<owner>/<repo> URL", r.Name, r.URL)
		}
		// Names are config keys and clone directories, so a repeat would silently replace the first.
		u := answers.NormalizeURL(r.URL)
		if names[r.Name] || urls[u] {
			return fmt.Errorf("repo %s (%s) is mapped twice", r.Name, r.URL)
		}
		names[r.Name], urls[u] = true, true
	}
	return nil
}

func (Config) Apply(ctx context.Context, e *Env) error {
	a := e.Answers
	if a.TeamKey == "" {
		a.TeamKey = "NF"
	}
	if len(a.Repos) == 0 {
		a.Repos = []answers.Repo{{BaseBranch: "main", Check: "just check", Draft: true}}
	}
	var found []answers.Repo
	if !e.SkipInteractive {
		repos, maybeEmpty, dropped, err := e.discoverRepos(ctx)
		if err != nil || len(repos) == 0 {
			why := "The token sees no usable repos."
			if err != nil {
				why = err.Error()
			}
			e.UI.Note("Could not list the repos the agent token can see", "Enter each repo by URL instead.\n\n"+why)
		} else {
			var skipped []string
			if len(dropped) > 0 {
				e.UI.Note("Some owners are not listed",
					"Not listed: repos of "+strings.Join(dropped, ", ")+", which the token can see only publicly. Add any you want by URL.")
			}
			found, skipped = MergeRepos(repos, a.Repos)
			if len(skipped) > 0 {
				e.UI.Note("Skipped repos whose name another repo already has",
					strings.Join(skipped, "\n")+"\nAdd one by URL and rename it if you want it mapped.")
			}
			// Left unticked unless already mapped; ticking one in the form removes the exclusion.
			var empty []string
			for _, u := range maybeEmpty {
				if !slices.ContainsFunc(a.Repos, func(r answers.Repo) bool { return answers.SameURL(r.URL, u) }) {
					a.ExcludedRepos = answers.Exclude(a.ExcludedRepos, u)
					empty = append(empty, u)
				}
			}
			if len(empty) > 0 {
				e.UI.Note("These repos may be empty; the clone fails if so", strings.Join(empty, "\n"))
			}
		}
	}
	defaults := func(url string) (string, bool) { return e.repoDefaults(ctx, url) }
	if err := e.UI.ConfigForm(a, found, defaults); err != nil {
		return err
	}
	// The form shows a suggestion as a placeholder; scripted runs have none.
	if a.NtfyTopic == "" {
		a.NtfyTopic = answers.RandomTopic()
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
	if err := os.MkdirAll(e.HomePath(".config/flocc"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(e.HomePath(".config/flocc/config.toml"), rendered, 0o600); err != nil {
		return err
	}
	ntfy := "https://ntfy.sh/" + a.NtfyTopic
	if _, err := e.Must(ctx, "curl", "-fsS", "-H", "Priority: 5", "-H", "Title: flocc", "-d", "test push from flocc-setup", ntfy); err != nil {
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
			err := e.UI.Spin("Cloning "+r.Name, func() error {
				_, err := e.Must(ctx, "git", "-c", "credential.helper=", "-c", "credential.helper="+credentialHelper, "clone", "-q", r.URL, dir)
				return err
			})
			if err != nil {
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
