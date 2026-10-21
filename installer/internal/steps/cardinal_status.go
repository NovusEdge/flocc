package steps

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/NovusEdge/flocc/installer/internal/hosts"
)

const (
	renewAge     = 20 * time.Hour
	renewQuiet   = 5 * time.Minute
	staleTurn    = 45 * time.Minute
	oldSession   = 6 * 24 * time.Hour
	statusSchema = 1
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

func secs(d time.Duration) *float64 { s := d.Seconds(); return &s }

func dur(s *float64) time.Duration { return time.Duration(*s * float64(time.Second)) }

// Failures are the rows that make status exit non-zero, each with the command that fixes it.
func (s CardinalStatus) Failures() []string {
	var out []string
	if s.Unit != "active" {
		out = append(out, fmt.Sprintf("cardinal.service is %s: flocc cardinal restart", s.Unit))
	}
	if s.Login != "full" {
		out = append(out, fmt.Sprintf("login is %s: flocc cardinal login", s.Login))
	}
	switch {
	case s.LastTurnAgeS != nil && dur(s.LastTurnAgeS) > staleTurn:
		out = append(out, fmt.Sprintf("no turn for %s: flocc cardinal restart", dur(s.LastTurnAgeS).Round(time.Minute)))
	case s.LastTurnAgeS == nil && s.SessionAgeS != nil && dur(s.SessionAgeS) > staleTurn:
		out = append(out, "no turn since the session started: flocc cardinal restart")
	}
	if s.SessionAgeS != nil && dur(s.SessionAgeS) > oldSession {
		out = append(out, fmt.Sprintf("the session is %dd old and its loop expires at 7: flocc cardinal restart", int(dur(s.SessionAgeS).Hours()/24)))
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

func CollectCardinalStatus(ctx context.Context, e *Env, user string, quick bool, now time.Time) CardinalStatus {
	s := CardinalStatus{Schema: statusSchema, User: user, Quick: quick, Login: claudeLogin(ctx, e)}
	s.Unit = userShow(ctx, e, "cardinal.service", "ActiveState")
	if s.Unit == "" {
		s.Unit = "unknown"
	}
	s.Restarts, _ = strconv.Atoi(userShow(ctx, e, "cardinal.service", "NRestarts"))
	if age, ok := sessionAge(ctx, e, now); ok && s.Unit == "active" {
		s.SessionAgeS = secs(age)
	}
	if t, ok := lastTurn(e); ok {
		s.LastTurnAgeS = secs(now.Sub(t))
	}
	s.Tmux = e.Ok(ctx, "tmux", "-L", user, "has-session", "-t", "="+user)
	s.Claude = strings.Contains(e.Output(ctx, "tmux", "-L", user, "list-panes", "-t", "="+user, "-F", "#{pane_current_command}"), "claude")
	s.NextRenew = userShow(ctx, e, "cardinal-renew.timer", "NextElapseUSecRealtime")
	_, s.ClaudeVersion = claudeVersion(ctx, e)
	if quick {
		return s
	}
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
	return e.Output(ctx, "systemctl", "--user", "status", "cardinal", "--no-pager", "-n", "20")
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
