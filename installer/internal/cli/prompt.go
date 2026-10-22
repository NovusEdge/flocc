package cli

import (
	"charm.land/huh/v2"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/keys"
	"github.com/NovusEdge/flocc/installer/internal/linearapi"
	"github.com/NovusEdge/flocc/installer/internal/ui"
)

// prompter is everything the desktop asks the owner.
type prompter interface {
	keys.Prompt
	Repos(a *answers.Answers, found []answers.Repo, defaults func(url string) (base string, draft bool)) error
	Team(teams []linearapi.Team) (linearapi.Team, error)
	Note(title, body string)
	// Ask reads one line with echo on; "" when it is left empty.
	Ask(title, help string) (string, error)
	// Choose returns the index of the option picked.
	Choose(title string, options []string) (int, error)
}

// sessionPrompt asks through the ui session, whose renderer answers ui.ErrNoTerminal on a piped
// stdin instead of letting huh pick a default.
type sessionPrompt struct{ ui.Prompts }

func (p sessionPrompt) Repos(a *answers.Answers, found []answers.Repo, defaults func(string) (string, bool)) error {
	return ui.RepoForm(p.S.Form, a, found, defaults)
}

func (p sessionPrompt) Team(teams []linearapi.Team) (linearapi.Team, error) {
	opts := make([]huh.Option[int], len(teams))
	for i, t := range teams {
		opts[i] = huh.NewOption(t.Key+"  "+t.Name, i)
	}
	var i int
	if err := p.S.Form(ui.Form(huh.NewGroup(huh.NewSelect[int]().Title("Linear team for this host").Options(opts...).Value(&i)))); err != nil {
		return linearapi.Team{}, err
	}
	return teams[i], nil
}
