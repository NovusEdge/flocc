package cli

import (
	"errors"
	"fmt"
	"os"

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

var errNoTerminal = errors.New("flocc asks you to choose here; run it in a terminal")

func (p huhPrompt) Repos(a *answers.Answers, found []answers.Repo, defaults func(string) (string, bool)) error {
	if !p.IsTerminal() {
		return errNoTerminal
	}
	return ui.RepoForm(a, found, defaults)
}

func (p huhPrompt) Team(teams []linearapi.Team) (linearapi.Team, error) {
	if !p.IsTerminal() {
		return linearapi.Team{}, errNoTerminal
	}
	opts := make([]huh.Option[int], len(teams))
	for i, t := range teams {
		opts[i] = huh.NewOption(t.Key+"  "+t.Name, i)
	}
	var i int
	err := ui.Form(huh.NewGroup(huh.NewSelect[int]().Title("Linear team for this host").Options(opts...).Value(&i))).Run()
	return teams[i], err
}

func (p huhPrompt) Note(title, body string) {
	if !p.IsTerminal() {
		fmt.Fprintf(os.Stderr, "\n%s\n%s\n", title, body)
		return
	}
	ui.NewHuh().Note(title, body)
}
