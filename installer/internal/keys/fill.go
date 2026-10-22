package keys

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/githubapi"
	"github.com/NovusEdge/flocc/installer/internal/linearapi"
	"github.com/NovusEdge/flocc/installer/internal/qr"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/tailnet"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

type Prompt interface {
	// Secret reads a paste with echo off; "" when it is left empty.
	// Secret shows facts with the question in every renderer; the full-screen modal covers the rows
	// they came from.
	Secret(title, help string, facts ...string) (string, error)
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
	UI     ui.Session
	Open   func(url string) // the browser
	HTTP   *http.Client
	// The API bases; githubapi.DefaultBase, linearapi.DefaultURL and https://ntfy.sh outside
	// tests. An empty TailscaleAPI is https://api.tailscale.com.
	GitHubAPI, LinearAPI, NtfyBase, TailscaleAPI string
	NewTopic                                     func() string
}

var ErrStopped = errors.New("stopped at an empty paste")

// Fill asks for each key in slots that the store lacks, in fillOrder, and stores each one only
// once it passes its checks. An empty paste for an optional slot skips it instead of stopping.
func Fill(ctx context.Context, e Env, m Mapped, slots []Slot) error {
	for _, s := range fillOrder {
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

func optional(s Slot) bool { return s == Tailscale }

func fillKey(ctx context.Context, e Env, m Mapped, s Slot) error {
	help := "Paste it here. Leave it empty to stop."
	if optional(s) {
		help = "Paste it here. Leave it empty to skip it."
	}
	stored, err := askKey(ctx, e, m, s, help)
	if err == nil && !stored && !optional(s) {
		err = ErrStopped
	}
	return err
}

// askKey opens s's page and asks until a paste passes its checks and is stored. An empty paste
// stores nothing and returns false. A paste whose check got no answer is checked again only if
// the owner says so, and is never stored unchecked.
func askKey(ctx context.Context, e Env, m Mapped, s Slot, help string) (bool, error) {
	v, ok, err := askChecked(ctx, e, m, s, help)
	if err != nil || !ok {
		return false, err
	}
	return true, store(e, s, v)
}

// store sets s as a step of its own; what the check found is already on the check's row.
func store(e Env, s Slot, v string) error {
	st := e.UI.Step("Store " + s.Title())
	if err := e.Store.Set(s, v); err != nil {
		st.Fail(err)
		return err
	}
	st.Done("in " + e.Store.Where())
	return nil
}

// askChecked is askKey up to the store: it returns the checked value, or ok false for an empty
// paste.
func askChecked(ctx context.Context, e Env, m Mapped, s Slot, help string) (v string, ok bool, err error) {
	link, page := Page(s, m)
	e.Open(link)
	raw := ""
	for {
		if raw == "" {
			if raw, err = e.Prompt.Secret(s.Title(), help, page, link); err != nil {
				return "", false, err
			}
			if strings.TrimSpace(raw) == "" {
				return "", false, nil
			}
			e.UI.Note(s.Title(), "pasted "+maskedPaste(s, strings.TrimSpace(raw)))
		}
		// A refused paste ends its row as skipped, not failed: the loop asks again, and a failed
		// row would turn the exit status to 1 even when the next paste passes.
		v, note, st, err := accept(ctx, e, m, s, raw, "Check "+s.Title())
		if ctx.Err() != nil {
			st.Fail(ctx.Err())
			return "", false, ctx.Err()
		}
		var u unchecked
		if errors.As(err, &u) {
			st.Skip("not stored: " + err.Error())
			again, err2 := e.Prompt.Confirm("Check the same paste again?", true)
			if err2 != nil {
				return "", false, err2
			}
			if !again {
				return "", false, fmt.Errorf("the %s is not stored: %w", s.Title(), err)
			}
			continue
		}
		if err != nil {
			st.Skip("not stored: " + err.Error())
			raw = ""
			continue
		}
		st.Done(note)
		return v, true, nil
	}
}

// maskedPaste is Masked for a paste, which for the Tailscale slot may carry the client ID first.
func maskedPaste(s Slot, v string) string {
	if _, secret, ok := SplitClient(v); s == Tailscale && ok {
		return Masked(secret)
	}
	return Masked(v)
}

// Adopt checks a key read from elsewhere, such as a host's secret file, as a paste is checked,
// and stores it once it passes. As with a paste, a check that got no answer runs again only if
// the owner says so, and the key is never stored unchecked. A key left untaken is not an error:
// the row says so and the next step becomes a todo, and only a failed prompt or store comes back.
func Adopt(ctx context.Context, e Env, m Mapped, s Slot, raw, from string) error {
	for {
		v, note, st, err := accept(ctx, e, m, s, raw, "Check "+s.Title()+" from "+from)
		if ctx.Err() != nil {
			st.Fail(ctx.Err())
			return ctx.Err()
		}
		var u unchecked
		if errors.As(err, &u) {
			st.Skip("not taken: " + err.Error())
			again, err := e.Prompt.Confirm("Check it again?", true)
			if err != nil {
				return err
			}
			if again {
				continue
			}
			e.UI.Todo(fmt.Sprintf("run flocc host import to take the %s from %s once the check gets an answer", s.Title(), from))
			return nil
		}
		// Only the host has this value, so there is nothing to paste; editing the token keeps it.
		var rp repoProblems
		if errors.As(err, &rp) {
			st.Skip("not taken: " + rp.Error())
			e.UI.Todo(fmt.Sprintf("on github.com/settings/personal-access-tokens open %q, Edit, and fix the above under Repository access "+
				"and Permissions (the token stays the same), then run flocc host import again", tokenName[s]))
			return nil
		}
		if err != nil {
			st.Skip("not taken: " + err.Error())
			e.UI.Todo("run flocc keys to make a new " + s.Title())
			return nil
		}
		st.Done(note)
		return store(e, s, v)
	}
}

// Take stores the keys in vals that differ from the store, for the slots named, each checked as a
// paste is. A value that fails its check, or whose recheck the owner declines, is skipped and the
// stored key stays. It returns the slots that held another key before, and only those a host file
// carries, so the caller can push them; a store error comes back with what was stored so far.
func Take(ctx context.Context, e Env, m Mapped, vals map[Slot]Taken, slots []Slot) (replaced []Slot, err error) {
	for _, s := range fillOrder {
		t, ok := vals[s]
		if !ok || !slices.Contains(slots, s) {
			continue
		}
		old, err := e.Store.Get(s)
		if err != nil {
			return replaced, err
		}
		if sameKey(s, old, t.Value) {
			continue
		}
		var stored bool
		if s == Ntfy {
			stored, err = takeNtfy(e, t, old != "")
		} else {
			stored, err = takeKey(ctx, e, m, s, t)
		}
		if err != nil {
			return replaced, err
		}
		if stored && old != "" && slices.Contains(hostFiled, s) {
			replaced = append(replaced, s)
		}
	}
	return replaced, nil
}

// hostFiled is the slots that have a secret file on each host.
var hostFiled =[]Slot{Linear, Agent, Publish}

// sameKey compares Tailscale values by their secret, as the id is ignored by the token endpoint.
func sameKey(s Slot, stored, v string) bool {
	if s == Tailscale {
		_, a, okA := SplitClient(stored)
		_, b, okB := SplitClient(v)
		if okA && okB {
			return a == b
		}
	}
	return stored == v
}

func takeKey(ctx context.Context, e Env, m Mapped, s Slot, t Taken) (bool, error) {
	for {
		v, note, st, err := accept(ctx, e, m, s, t.Value, "Check "+s.Title()+" from "+t.From)
		if ctx.Err() != nil {
			st.Fail(ctx.Err())
			return false, ctx.Err()
		}
		var u unchecked
		if errors.As(err, &u) {
			st.Skip(fmt.Sprintf("%s not taken: %v", maskedPaste(s, t.Value), err))
			again, err := e.Prompt.Confirm("Check it again?", true)
			if err != nil {
				return false, err
			}
			if again {
				continue
			}
			return false, nil
		}
		if err != nil {
			st.Skip(fmt.Sprintf("%s not taken: %v", maskedPaste(s, t.Value), err))
			return false, nil
		}
		st.Done(note)
		return true, store(e, s, v)
	}
}

// takeNtfy has no live check to run: the topic is only cleaned. Hosts hold it in their ship, not in
// a secret file, so a topic that replaces another waits for the next ship.
func takeNtfy(e Env, t Taken, replacing bool) (bool, error) {
	topic, err := steps.CleanSecret(t.Value)
	if err != nil {
		e.UI.Step("Check "+Ntfy.Title()+" from "+t.From).Skip(fmt.Sprintf("%s not taken: %v", Masked(t.Value), err))
		return false, nil
	}
	if err := store(e, Ntfy, topic); err != nil {
		return false, err
	}
	if replacing {
		e.UI.Todo("run flocc ship -H <host> for each host to send its pushes to the new ntfy topic, and subscribe to it in the ntfy app")
	}
	return true, nil
}

// Replace asks for a new value of each slot in turn and checks it as Fill does, and stores
// nothing until every slot has been asked, so a Ctrl-C or a failed prompt changes no key. An
// empty paste keeps the current value. It returns the slots it changed, also with an error from
// the store, so the caller can push what was stored.
func Replace(ctx context.Context, e Env, m Mapped, slots []Slot) ([]Slot, error) {
	type pending struct {
		s Slot
		v string
	}
	var asked []pending
	for _, s := range slots {
		v, ok, err := askChecked(ctx, e, m, s, "Paste the new one. Leave it empty to keep the current one.")
		if err != nil {
			return nil, err
		}
		if ok {
			asked = append(asked, pending{s, v})
		}
	}
	var changed []Slot
	for _, p := range asked {
		if err := store(e, p.s, p.v); err != nil {
			return changed, err
		}
		changed = append(changed, p.s)
	}
	return changed, nil
}

// Accept cleans a paste and checks it, by prefix and then live, without storing it.
func Accept(ctx context.Context, e Env, m Mapped, s Slot, raw string) (value, note string, err error) {
	v, note, st, err := accept(ctx, e, m, s, raw, "Check "+s.Title())
	if err != nil {
		st.Fail(err)
	} else {
		st.Done(note)
	}
	return v, note, err
}

// accept is Accept with the check's step left open for the caller to end, as the outcome decides
// whether the row is a failure, a retry or a skip.
func accept(ctx context.Context, e Env, m Mapped, s Slot, raw, name string) (value, note string, st *ui.Step, err error) {
	st = e.UI.Step(name)
	v, err := steps.CleanSecret(raw)
	if err == nil {
		err = CheckPrefix(s, v)
	}
	if err == nil {
		st.Detail(asking[s])
		note, err = checkLive(ctx, e, m, s, v)
	}
	if _, ok := err.(repoProblems); ok {
		err = fmt.Errorf("%w\nChange the token on GitHub (its repositories and permissions can be edited) and paste it again.", err)
	}
	return v, note, st, err
}

// asking is the detail of a check's row while the request is out.
var asking = map[Slot]string{
	Linear:    "asking Linear",
	Agent:     "asking GitHub",
	Publish:   "asking GitHub",
	Tailscale: "asking Tailscale",
	Ntfy:      "sending a test push",
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
	switch s {
	case Linear:
		return checkLinear(ctx, e, m, v)
	case Tailscale:
		return checkTailscale(ctx, e, v)
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

func checkLinear(ctx context.Context, e Env, m Mapped, v string) (string, error) {
	lin := &linearapi.Client{URL: e.LinearAPI, Key: v, HTTP: e.HTTP}
	viewer, err := linearapi.CheckKey(ctx, lin, m.Teams)
	// A team the key cannot see is a plain error and the key's fault. Only an outage leaves
	// the key unchecked: a 400 validation error or a 404 would fail the same way again.
	if errors.Is(err, linearapi.ErrUnavailable) {
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

// ErrTailscaleRefused is the token endpoint turning the client down.
var ErrTailscaleRefused = errors.New("Tailscale refused the client; make a new one")

func checkTailscale(ctx context.Context, e Env, v string) (string, error) {
	id, secret, _ := SplitClient(v)
	base := e.TailscaleAPI
	if base == "" {
		base = "https://api.tailscale.com"
	}
	c := &tailnet.Client{HTTP: e.HTTP, Base: base, ID: id, Secret: secret}
	if _, err := c.Token(ctx); err != nil {
		if errors.Is(err, tailnet.ErrRefused) {
			return "", ErrTailscaleRefused
		}
		return "", unchecked{fmt.Errorf("could not check Tailscale: %w", err)}
	}
	return "token ok", nil
}

// Check runs the live checks on every stored key, one step each, and returns one line per
// problem. A problem's step is already failed, so the caller adds only the next steps.
func Check(ctx context.Context, e Env, m Mapped) []string {
	var problems []string
	slots := []Slot{Linear, Agent, Publish}
	if v, err := e.Store.Get(Tailscale); err == nil && v != "" {
		slots = append(slots, Tailscale)
	}
	for _, s := range slots {
		st := e.UI.Step("Check " + s.Title())
		v, err := e.Store.Get(s)
		if err == nil && v == "" {
			err = errors.New("missing; run flocc keys")
		}
		var note string
		if err == nil {
			st.Detail(asking[s])
			note, err = checkLive(ctx, e, m, s, v)
		}
		if _, ok := err.(repoProblems); ok {
			// Editing a fine-grained token keeps its value, so nothing needs pasting.
			err = fmt.Errorf("%w\nOn github.com/settings/personal-access-tokens open %q, Edit, and fix the above under "+
				"Repository access and Permissions (the token stays the same), then run flocc keys check.", err, tokenName[s])
		}
		if err != nil {
			err = RotateHint(err)
			problems = append(problems, s.Title()+": "+err.Error())
			what, _ := ui.SplitNext(err.Error())
			st.Fail(errors.New(what))
			continue
		}
		st.Done(note)
	}
	st := e.UI.Step("Check ntfy topic")
	if t, err := e.Store.Get(Ntfy); err != nil || t == "" {
		problems = append(problems, "ntfy topic: missing; run flocc keys")
		st.Fail(errors.New("missing"))
	} else {
		st.Done("set")
	}
	return problems
}

// RotateHint adds the next step to an error saying GitHub or Linear refused a stored key: there
// is no prompt to paste a new one into, and rotate is what replaces it on every host.
func RotateHint(err error) error {
	switch {
	case errors.Is(err, githubapi.ErrBadCredentials):
		return fmt.Errorf("%w; run flocc keys rotate github", err)
	case errors.Is(err, linearapi.ErrKeyRejected):
		return fmt.Errorf("%w; run flocc keys rotate linear", err)
	}
	return err
}

// tokenName is the name each GitHub token's page fills in, which the owner finds it by later.
var tokenName = map[Slot]string{Agent: "flocc agent", Publish: "flocc publish"}

// Page is where a key is made and what to do there. GitHub prefills everything but the repos.
func Page(s Slot, m Mapped) (string, string) {
	switch s {
	case Linear:
		// The same path as the installer's help text in steps/secrets.go.
		help := "Linear → Settings → Account → Security & access → New API key. Name it flocc and give it Full access: the service writes comments and moves issues."
		if len(m.Teams) > 0 {
			help += " It must see team " + strings.Join(m.Teams, ", ") + "."
		}
		return "https://linear.app/settings/account/security", help
	case Tailscale:
		return "https://login.tailscale.com/admin/settings/oauth",
			"Settings → OAuth clients → Generate. Scopes: Auth keys (write) and Devices core (write), both with tag " + tailnet.Tag +
				"; Policy file (read). Paste the client secret, which starts with tskey-client-."
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
	body := "Scan this with the ntfy app on Android, or subscribe to " + web + " in the app."
	// qr.Write ends lines in CRLF for a raw-mode terminal; a row is plain text.
	var code bytes.Buffer
	if err := qr.Write(&code, "ntfy://ntfy.sh/"+topic+"?display=flocc"); err == nil {
		body += "\n" + strings.ReplaceAll(code.String(), "\r", "")
	}
	e.UI.Note("ntfy topic", body)
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
			e.UI.Todo("subscribe to " + web + " in the ntfy app before the first ship")
			return nil
		}
	}
}

func push(ctx context.Context, e Env, topic string) (err error) {
	st := e.UI.Step("Check ntfy topic")
	st.Detail(asking[Ntfy])
	defer func() {
		if err != nil {
			st.Fail(err)
			return
		}
		st.Done("test push sent")
	}()
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
