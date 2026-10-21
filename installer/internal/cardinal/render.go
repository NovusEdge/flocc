package cardinal

import (
	"fmt"
	"sort"
	"strings"

	"github.com/NovusEdge/flocc/installer/internal/cardinal/saved"
	"github.com/NovusEdge/flocc/installer/internal/ship"
	"github.com/NovusEdge/flocc/installer/internal/steps"
)

// RenderStatus prints a full cardinal-status report and returns the exit code. Every string came
// from cardinal's binary, so each is sanitized.
func RenderStatus(s steps.CardinalStatus) (string, int) {
	var b strings.Builder
	row := func(k, v string) { fmt.Fprintf(&b, "%-12s %s\n", k, saved.Sanitize(v)) }
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
	fails := s.Failures()
	for _, f := range fails {
		fmt.Fprintf(&b, "FAIL  %s\n", saved.Sanitize(f))
	}
	if len(fails) > 0 && s.Journal != "" {
		fmt.Fprintf(&b, "\njournal:\n%s\n", saved.SanitizeBlock(s.Journal, 20))
	}
	if len(fails) > 0 && s.Pane != "" {
		fmt.Fprintf(&b, "\npane:\n%s\n", saved.SanitizeBlock(s.Pane, 15))
	}
	if len(fails) > 0 {
		return b.String(), 1
	}
	return b.String(), 0
}
