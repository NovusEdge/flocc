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
}

type huhPrompt struct{ keys.Huh }

func (huhPrompt) Repos(a *answers.Answers, found []answers.Repo, defaults func(string) (string, bool)) error {
	return ui.RepoForm(a, found, defaults)
}

func (huhPrompt) Team(teams []linearapi.Team) (linearapi.Team, error) {
	opts := make([]huh.Option[int], len(teams))
	for i, t := range teams {
		opts[i] = huh.NewOption(t.Key+"  "+t.Name, i)
	}
	var i int
	err := huh.NewForm(huh.NewGroup(huh.NewSelect[int]().Title("Linear team for this host").Options(opts...).Value(&i))).Run()
	return teams[i], err
}

func (huhPrompt) Note(title, body string) { ui.NewHuh().Note(title, body) }
