package ui

import (
	"errors"
	"fmt"
	"io"

	"github.com/NovusEdge/flocc/installer/internal/answers"
)

type StepStatus struct {
	Name, Detail string
	Done         bool
}

type UI interface {
	Status(steps []StepStatus)
	Note(title, body string)
	Confirm(title string, def bool) (bool, error)
	Text(title, def string) (string, error)
	// Secret returns "" when the user leaves it empty.
	Secret(id, title, help string) (string, error)
	// found is every repo the agent token can see, already merged with saved answers;
	// empty when discovery failed, which falls back to asking for each repo by URL.
	// defaults gives the base branch and draft setting of a repo added by URL.
	ConfigForm(a *answers.Answers, found []answers.Repo, defaults func(url string) (base string, draft bool)) error
	PickRedo(done []string) ([]string, error)
	Spin(title string, fn func() error) error
	// Step runs fn as the step l under a progress line. Anything fn shows goes through this UI, so
	// a Note while a live line is up clears it first.
	Step(l Line, fn func() (Outcome, error)) error
}

// Scripted answers from an answers file and never reads the terminal.
type Scripted struct {
	A   *answers.Answers
	Out io.Writer
	// Color colours the step lines and animates the live one.
	Color bool

	prog *Progress // made on first use from Out and Color
}

func (s *Scripted) progress() *Progress {
	if s.prog == nil {
		s.prog = &Progress{Out: Out{W: s.Out, Color: s.Color}, Animate: s.Color}
	}
	return s.prog
}

func (s *Scripted) Step(l Line, fn func() (Outcome, error)) error { return s.progress().Run(l, fn) }

func (s *Scripted) Status(steps []StepStatus) {
	o := Out{W: s.Out, Color: s.Color}
	for _, st := range steps {
		mark := "·"
		if st.Done {
			mark = o.OK("✓")
		}
		o.Line("%s %-14s %s", mark, st.Name, st.Detail)
	}
}

func (s *Scripted) Note(title, body string) {
	s.progress().Quiet(func() {
		fmt.Fprintf(s.Out, "\n%s\n", title)
		Out{W: s.Out, Color: s.Color}.Line("%s", body)
	})
}

func (s *Scripted) Confirm(string, bool) (bool, error)   { return true, nil }
func (s *Scripted) Text(_, def string) (string, error)   { return def, nil }
func (s *Scripted) PickRedo([]string) ([]string, error)  { return nil, nil }
func (s *Scripted) Spin(_ string, fn func() error) error { return fn() }

func (s *Scripted) Secret(id, _, _ string) (string, error) {
	if s.A.Secrets == nil {
		return "", nil
	}
	switch id {
	case "linear_key":
		return s.A.Secrets.LinearKey, nil
	case "agent_token":
		return s.A.Secrets.AgentToken, nil
	case "publish_token":
		return s.A.Secrets.PublishToken, nil
	}
	return "", fmt.Errorf("unknown secret %q", id)
}

func (s *Scripted) ConfigForm(a *answers.Answers, _ []answers.Repo, _ func(string) (string, bool)) error {
	if a.TeamKey == "" || a.DigestIssue == "" || len(a.Repos) == 0 {
		return errors.New("the answers file needs team_key, digest_issue and at least one [[repos]]")
	}
	return nil
}
