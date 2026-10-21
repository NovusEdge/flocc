package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/hosts"
)

const (
	renewAge   = 20 * time.Hour
	renewQuiet = 5 * time.Minute
	staleTurn  = 45 * time.Minute
	oldSession = 6 * 24 * time.Hour
	// CardinalStatusSchema changes when CardinalStatus does; a desktop refuses another.
	CardinalStatusSchema = 1
)

// RenewAge is how old a session must be before the daily renewal replaces it.
const RenewAge = renewAge

// ShouldRenew holds when a restart cuts nothing: the owner did not stop cardinal, the session is
// at least minAge old, and no transcript changed lately, so no check is mid-turn.
func ShouldRenew(active bool, sessionAge, sinceTurn, minAge time.Duration) bool {
	return active && sessionAge >= minAge && sinceTurn >= renewQuiet
}

// CardinalStatus is cardinal-status's JSON. Quick leaves out what costs time or network (Linear
// and the hosts), for the fleet view's redraws.
type CardinalStatus struct {
	Schema        int             `json:"schema"`
	User          string          `json:"user"`
	Unit          string          `json:"unit"`
	Restarts      int             `json:"restarts"`
	SessionAgeS   *float64        `json:"session_age_s"`
	LastTurnAgeS  *float64        `json:"last_turn_age_s"`
	Tmux          bool            `json:"tmux"`
	Claude        bool            `json:"claude"`
	Login         string          `json:"login"`
	Linear        string          `json:"linear,omitempty"`
	Hosts         map[string]bool `json:"hosts,omitempty"`
	NextRenew     string          `json:"next_renew,omitempty"`
	ClaudeVersion string          `json:"claude_version,omitempty"`
	Quick         bool            `json:"quick"`
	Journal       string          `json:"journal,omitempty"` // full status of a unit that is not active
	Pane          string          `json:"pane,omitempty"`    // full status only
}

// Failures are the rows that make status exit non-zero, each with the command that fixes it.
func (s CardinalStatus) Failures() []string {
	var out []string
	if s.Unit != "active" {
		out = append(out, fmt.Sprintf("cardinal.service is %s: flocc cardinal restart", s.Unit))
	}
	if s.Login != "full" {
		out = append(out, fmt.Sprintf("login is %s: flocc cardinal login", s.Login))
	}
	turn, session := s.LastTurnAgeS, s.SessionAgeS
	switch {
	case turn != nil && *turn > staleTurn.Seconds():
		out = append(out, fmt.Sprintf("no turn for %s: flocc cardinal restart", (time.Duration(*turn)*time.Second).Round(time.Minute)))
	case turn == nil && session != nil && *session > staleTurn.Seconds():
		out = append(out, "no turn since the session started: flocc cardinal restart")
	}
	if session != nil && *session > oldSession.Seconds() {
		out = append(out, fmt.Sprintf("the session is %dd old and its loop expires at 7: flocc cardinal restart", int(*session/86400)))
	}
	for h, ok := range s.Hosts {
		if !ok {
			out = append(out, h+" unreachable: flocc cardinal setup")
		}
	}
	return out
}

func userShow(ctx context.Context, e *Env, unit, prop string) string {
	r, err := e.Run.Run(ctx, e.userctl("show", unit, "-p", prop, "--value", "--timestamp=unix"))
	if err != nil || r.Code != 0 {
		return ""
	}
	return strings.TrimSpace(r.Stdout)
}

// sessionAge is how long cardinal.service has been active; ok is false while it is not.
func sessionAge(ctx context.Context, e *Env, now time.Time) (time.Duration, bool) {
	at, err := strconv.ParseInt(strings.TrimPrefix(userShow(ctx, e, "cardinal.service", "ActiveEnterTimestamp"), "@"), 10, 64)
	if err != nil || at == 0 {
		return 0, false
	}
	return now.Sub(time.Unix(at, 0)), true
}

// lastTurn is the newest transcript's modification time: Claude Code writes the transcript on
// every turn, so the heartbeat needs nothing from the brief.
func lastTurn(e *Env) (time.Time, bool) {
	var newest time.Time
	filepath.WalkDir(e.HomePath(".claude/projects"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest, !newest.IsZero()
}

// CardinalRenew restarts cardinal.service when ShouldRenew holds. minAge is RenewAge except in the
// live test.
func CardinalRenew(ctx context.Context, e *Env, minAge time.Duration, now time.Time) (bool, error) {
	age, _ := sessionAge(ctx, e, now)
	since := renewQuiet
	if t, ok := lastTurn(e); ok {
		since = now.Sub(t)
	}
	if !ShouldRenew(unitActive(ctx, e, "cardinal.service"), age, since, minAge) {
		return false, nil
	}
	_, err := e.MustCmd(ctx, e.userctl("restart", "cardinal.service"))
	return err == nil, err
}

// linearState reads `claude mcp list`, which health-checks every server, so only full status runs it.
func linearState(ctx context.Context, e *Env) string {
	for _, l := range strings.Split(e.Output(ctx, e.claude(), "mcp", "list"), "\n") {
		if !strings.HasPrefix(l, "linear:") {
			continue
		}
		switch {
		case strings.Contains(l, "Connected"):
			return "connected"
		case strings.Contains(l, "auth"):
			return "needs authentication"
		}
		return "failing"
	}
	return "not configured"
}

// credentialsLogin reads the login from Claude Code's credentials file instead of starting claude,
// which the fleet view's redraws would do for every install: "full" when it holds a claude.ai OAuth
// login, "none" otherwise.
func credentialsLogin(e *Env) string {
	var c struct {
		OAuth json.RawMessage `json:"claudeAiOauth"`
	}
	b, err := os.ReadFile(e.HomePath(".claude/.credentials.json"))
	if err != nil || json.Unmarshal(b, &c) != nil || len(c.OAuth) == 0 || string(c.OAuth) == "null" {
		return "none"
	}
	return "full"
}

func CollectCardinalStatus(ctx context.Context, e *Env, user string, quick bool, now time.Time) CardinalStatus {
	s := CardinalStatus{Schema: CardinalStatusSchema, User: user, Quick: quick}
	s.Unit = userShow(ctx, e, "cardinal.service", "ActiveState")
	if s.Unit == "" {
		s.Unit = "unknown"
	}
	s.Restarts, _ = strconv.Atoi(userShow(ctx, e, "cardinal.service", "NRestarts"))
	if age, ok := sessionAge(ctx, e, now); ok && s.Unit == "active" {
		s.SessionAgeS = new(age.Seconds())
	}
	if t, ok := lastTurn(e); ok {
		s.LastTurnAgeS = new(now.Sub(t).Seconds())
	}
	s.Tmux = e.Ok(ctx, "tmux", "-L", user, "has-session", "-t", "="+user)
	s.Claude = strings.Contains(e.Output(ctx, "tmux", "-L", user, "list-panes", "-t", "="+user, "-F", "#{pane_current_command}"), "claude")
	s.NextRenew = userShow(ctx, e, "cardinal-renew.timer", "NextElapseUSecRealtime")
	if quick {
		s.Login = credentialsLogin(e)
		return s
	}
	s.Login = claudeLogin(ctx, e)
	_, s.ClaudeVersion = claudeVersion(ctx, e)
	s.Linear = linearState(ctx, e)
	s.Hosts = map[string]bool{}
	list, _ := hosts.List(e.HomePath(cardinalHosts))
	for _, h := range list {
		s.Hosts[h.Name] = e.Ok(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", h.SSH, "true")
	}
	return s
}

// CardinalJournal is the unit's last journal lines. The journal can refuse a user that is not in
// systemd-journal, so systemctl status stands in then.
func CardinalJournal(ctx context.Context, e *Env) string {
	if out := e.Output(ctx, "journalctl", "--user", "-u", "cardinal", "-n", "20", "--no-pager"); out != "" && !strings.Contains(out, "No journal files") {
		return out
	}
	r, _ := e.Run.Run(ctx, e.userctl("status", "cardinal", "--no-pager", "-n", "20"))
	// systemctl status exits 3 for an inactive unit, which is when it is asked for.
	return strings.TrimSpace(r.Stdout)
}

// CardinalPane is the pane's last lines, less any holding code=: the Linear authorisation is
// pasted there and carries the OAuth code.
func CardinalPane(ctx context.Context, e *Env, user string) string {
	var keep []string
	for _, l := range strings.Split(e.Output(ctx, "tmux", "-L", user, "capture-pane", "-p", "-t", "="+user, "-S", "-15"), "\n") {
		if !strings.Contains(l, "code=") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}
