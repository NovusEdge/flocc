package cardinal

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/steps"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// DrawStatus draws a full cardinal-status report and returns the exit code. Every string came
// from cardinal's binary, so each is sanitized.
func DrawStatus(u ui.Session, s steps.CardinalStatus) int {
	t := ui.Table{Title: "cardinal", Headers: []string{"item", "state"}}
	row := func(k, v string) { t.Rows = append(t.Rows, []string{k, saved.Sanitize(v)}) }
	row("unit", fmt.Sprintf("%s, %d restarts", s.Unit, s.Restarts))
	if s.SessionAgeS != nil {
		row("session", ship.Age(*s.SessionAgeS)+" old")
	}
	row("tmux", fmt.Sprintf("session %v, claude %v", s.Tmux, s.Claude))
	row("login", s.Login)
	if s.Linear != "" {
		row("linear", s.Linear)
	}
	if s.LastTurnAgeS != nil {
		row("last turn", ship.Age(*s.LastTurnAgeS)+" ago")
	}
	if s.NextRenew != "" {
		row("next renew", s.NextRenew)
	}
	names := make([]string, 0, len(s.Hosts))
	for h := range s.Hosts {
		names = append(names, h)
	}
	sort.Strings(names)
	for _, h := range names {
		state := "reachable"
		if !s.Hosts[h] {
			state = "unreachable"
		}
		row("host", h+" "+state)
	}
	if s.ClaudeVersion != "" {
		row("claude", s.ClaudeVersion)
	}
	u.Table(t)
	fails := s.Failures()
	for _, f := range fails {
		what, fix, _ := strings.Cut(saved.Sanitize(f), ": ")
		u.Step(what).Fail(errors.New(fix))
	}
	if len(fails) > 0 && s.Journal != "" {
		u.Note("journal", saved.SanitizeBlock(s.Journal, 20))
	}
	if len(fails) > 0 && s.Pane != "" {
		u.Note("pane", saved.SanitizeBlock(s.Pane, 15))
	}
	if len(fails) > 0 {
		return 1
	}
	return 0
}
