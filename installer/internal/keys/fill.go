package keys

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/githubapi"
	"github.com/NovusEdge/flocc/installer/internal/linearapi"
	"github.com/NovusEdge/flocc/installer/internal/steps"
)

type Prompt interface {
	// Secret reads a paste with echo off; "" when it is left empty.
	Secret(title, help string) (string, error)
	Confirm(title string, def bool) (bool, error)
}

// Mapped is what the host files map, which the keys are checked against. The Linear key's write
// probe runs on the first digest issue Linear still has.
type Mapped struct {
	Teams   []string
	Repos   []githubapi.Target
	Digests []string
}

type Env struct {
	Store  Store
	Prompt Prompt
	Out    io.Writer
	Open   func(url string) // the browser, the URL and a QR code
	QR     func(text string)
	HTTP   *http.Client
	// The API bases; githubapi.DefaultBase, linearapi.DefaultURL and https://ntfy.sh outside tests.
	GitHubAPI, LinearAPI, NtfyBase string
	NewTopic                       func() string
}

var ErrStopped = errors.New("stopped at an empty paste")

// Fill asks for each key in slots that the store lacks, in Slots order, and stores each one only
// once it passes its checks.
func Fill(ctx context.Context, e Env, m Mapped, slots []Slot) error {
	for _, s := range Slots {
		if !slices.Contains(slots, s) {
			continue
		}
		v, err := e.Store.Get(s)
		if err != nil {
			return err
		}
		if v != "" {
			continue
		}
		if s == Ntfy {
			err = fillNtfy(ctx, e)
		} else {
			err = fillKey(ctx, e, m, s)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func fillKey(ctx context.Context, e Env, m Mapped, s Slot) error {
	stored, err := askKey(ctx, e, m, s, "Paste it here; nothing shows while you paste. Leave it empty to stop.")
	if err == nil && !stored {
		err = ErrStopped
	}
	return err
}

// askKey opens s's page and asks until a paste passes its checks and is stored. An empty paste
// stores nothing and returns false. A paste whose check got no answer is checked again only if
// the owner says so, and is never stored unchecked.
func askKey(ctx context.Context, e Env, m Mapped, s Slot, help string) (bool, error) {
	link, page := Page(s, m)
	fmt.Fprintf(e.Out, "\n%s\n%s\n%s\n", s.Title(), page, link)
	e.Open(link)
	raw := ""
	for {
		if raw == "" {
			var err error
			if raw, err = e.Prompt.Secret(s.Title(), help); err != nil {
				return false, err
			}
			if strings.TrimSpace(raw) == "" {
				return false, nil
			}
		}
		v, note, err := Accept(ctx, e, m, s, raw)
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		var u unchecked
		if errors.As(err, &u) {
			fmt.Fprintf(e.Out, "Not stored: %v\n", err)
			again, err2 := e.Prompt.Confirm("Check the same paste again?", true)
			if err2 != nil {
				return false, err2
			}
			if !again {
				return false, fmt.Errorf("the %s is not stored: %w", s.Title(), err)
			}
			continue
		}
		if err != nil {
			fmt.Fprintf(e.Out, "Not stored: %v\n", err)
			raw = ""
			continue
		}
		if err := e.Store.Set(s, v); err != nil {
			return false, err
		}
		fmt.Fprintf(e.Out, "Stored in %s: %s\n", e.Store.Where(), note)
		return true, nil
	}
}

// Accept cleans a paste and checks it, by prefix and then live, without storing it.
func Accept(ctx context.Context, e Env, m Mapped, s Slot, raw string) (value, note string, err error) {
	v, err := steps.CleanSecret(raw)
	if err == nil {
		err = CheckPrefix(s, v)
	}
	if err == nil {
		note, err = checkLive(ctx, e, m, s, v)
	}
	if _, ok := err.(repoProblems); ok {
		err = fmt.Errorf("%w\nChange the token on GitHub (its repositories and permissions can be edited) and paste it again.", err)
	}
	return v, note, err
}

// repoProblems are the repos a working token cannot serve, one line each. The fix differs: a
// paste is asked for again, a stored token is edited on GitHub.
type repoProblems []string

func (p repoProblems) Error() string { return strings.Join(p, "\n") }

// unchecked is a check that got no answer about the key: no reply, a 5xx, a rate limit. Asking
// for another paste would send the owner to remake a key that may be good.
type unchecked struct{ error }

func (u unchecked) Unwrap() error { return u.error }

func checkLive(ctx context.Context, e Env, m Mapped, s Slot, v string) (string, error) {
	if s == Linear {
		lin := &linearapi.Client{URL: e.LinearAPI, Key: v, HTTP: e.HTTP}
		viewer, err := linearapi.CheckKey(ctx, lin, m.Teams)
		// A team the key cannot see is a plain error and the key's fault; a failed call is the
		// key's fault only when Linear rejected it.
		var le *linearapi.Error
		if errors.As(err, &le) && !errors.Is(err, linearapi.ErrKeyRejected) {
			return "", unchecked{err}
		}
		if err != nil {
			return "", err
		}
		// One write proves the key's scope. A digest Linear no longer has is skipped: that is the
		// host file's problem, which host add fixes. With no host yet there is no digest to write
		// to, and host add's own check runs the probe later.
		for _, key := range m.Digests {
			err := linearapi.CanWrite(ctx, lin, key)
			if errors.Is(err, linearapi.ErrNotFound) {
				continue
			}
			if errors.Is(err, linearapi.ErrKeyRejected) {
				return "", err
			}
			// Besides an outage, CanWrite fails when Linear answers the issue read with no
			// issue, which is not the key's fault either.
			if err != nil {
				return "", unchecked{err}
			}
			break
		}
		var teams []string
		for _, t := range viewer.Teams {
			teams = append(teams, t.Key)
		}
		return fmt.Sprintf("%s, teams %s", viewer.Name, strings.Join(teams, ", ")), nil
	}
	check := githubapi.CheckAgent
	if s == Publish {
		check = githubapi.CheckPublish
	}
	res, err := check(ctx, &githubapi.Client{Base: e.GitHubAPI, Token: v, HTTP: e.HTTP}, m.Repos)
	if err != nil && !errors.Is(err, githubapi.ErrBadCredentials) {
		return "", unchecked{fmt.Errorf("could not check GitHub: %w", err)}
	}
	if err != nil {
		return "", err
	}
	if len(res.Problems) > 0 {
		return "", repoProblems(res.Problems)
	}
	if res.Expires.IsZero() {
		return "never expires", nil
	}
	return "expires " + res.Expires.Format("2006-01-02"), nil
}

// Check runs the live checks on every stored key. It prints an ok line per good key and returns
// one line per problem.
func Check(ctx context.Context, e Env, m Mapped) []string {
	var problems []string
	for _, s := range []Slot{Linear, Agent, Publish} {
		v, err := e.Store.Get(s)
		if err == nil && v == "" {
			err = errors.New("missing; run flocc keys")
		}
		var note string
		if err == nil {
			note, err = checkLive(ctx, e, m, s, v)
		}
		if _, ok := err.(repoProblems); ok {
			// Editing a fine-grained token keeps its value, so nothing needs pasting.
			err = fmt.Errorf("%w\nOn github.com/settings/personal-access-tokens open %q, Edit, and fix the above under "+
				"Repository access and Permissions (the token stays the same), then run flocc keys check.", err, tokenName[s])
		}
		if err != nil {
			problems = append(problems, s.Title()+": "+err.Error())
			continue
		}
		fmt.Fprintf(e.Out, "ok    %s: %s\n", s.Title(), note)
	}
	if t, err := e.Store.Get(Ntfy); err != nil || t == "" {
		problems = append(problems, "ntfy topic: missing; run flocc keys")
	}
	return problems
}

// tokenName is the name each GitHub token's page fills in, which the owner finds it by later.
var tokenName = map[Slot]string{Agent: "flocc agent", Publish: "flocc publish"}

// Page is where a key is made and what to do there. GitHub prefills everything but the repos.
func Page(s Slot, m Mapped) (string, string) {
	if s == Linear {
		// The same path as the installer's help text in steps/secrets.go.
		help := "Linear → Settings → Account → Security & access → New API key. Name it flocc and give it Full access: the service writes comments and moves issues."
		if len(m.Teams) > 0 {
			help += " It must see team " + strings.Join(m.Teams, ", ") + "."
		}
		return "https://linear.app/settings/account/security", help
	}
	q := url.Values{"expires_in": {"366"}, "name": {tokenName[s]}}
	if s == Agent {
		q.Set("description", "Read-only. flocc's agents run gh with it.")
		// No checks: fine-grained tokens have no such permission. Actions results are read
		// through actions (runs and logs) and statuses.
		for _, p := range []string{"contents", "pull_requests", "issues", "statuses", "actions"} {
			q.Set(p, "read")
		}
	} else {
		q.Set("description", "flocc's service pushes branches and opens pull requests with it.")
		q.Set("contents", "write")
		q.Set("pull_requests", "write")
	}
	var owners, slugs []string
	for _, r := range m.Repos {
		slugs = append(slugs, r.Slug)
		if o, _, _ := strings.Cut(r.Slug, "/"); !slices.Contains(owners, o) {
			owners = append(owners, o)
		}
	}
	help := "Repository access: Only select repositories, then tick the repos you will map; flocc host add checks them again."
	if len(slugs) > 0 {
		help = "Repository access: Only select repositories, then tick " + strings.Join(slugs, ", ") + "."
	}
	switch len(owners) {
	case 1:
		q.Set("target_name", owners[0])
	case 0:
	default:
		help += " These repos belong to " + strings.Join(owners[:len(owners)-1], ", ") + " and " + owners[len(owners)-1] +
			", but a fine-grained token serves one owner: pick the owner as Resource owner, and map only that owner's repos."
	}
	return "https://github.com/settings/personal-access-tokens/new?" + q.Encode(), help
}

func fillNtfy(ctx context.Context, e Env) error {
	topic := e.NewTopic()
	if err := e.Store.Set(Ntfy, topic); err != nil {
		return err
	}
	web := "https://ntfy.sh/" + topic
	fmt.Fprintf(e.Out, "\nntfy topic\nScan this with the ntfy app on Android, or subscribe to %s in the app.\n", web)
	e.QR("ntfy://ntfy.sh/" + topic + "?display=flocc")
	for {
		if err := push(ctx, e, topic); err != nil {
			return err
		}
		arrived, err := e.Prompt.Confirm("A test push went to "+web+". Did it arrive?", true)
		if err != nil || arrived {
			return err
		}
		again, err := e.Prompt.Confirm("Subscribe to "+web+" in the ntfy app. Send another test push?", true)
		if err != nil {
			return err
		}
		if !again {
			fmt.Fprintf(e.Out, "Kept the topic; subscribe to %s before the first ship.\n", web)
			return nil
		}
	}
}

func push(ctx context.Context, e Env, topic string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", e.NtfyBase+"/"+topic, strings.NewReader("test push from flocc keys"))
	if err != nil {
		return err
	}
	req.Header.Set("Title", "flocc")
	req.Header.Set("Priority", "5")
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy answered the test push with %s", resp.Status)
	}
	return nil
}
