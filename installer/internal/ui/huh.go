package ui

import (
	"errors"
	"fmt"
	"strconv"

	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"
	"charm.land/lipgloss/v2"

	"github.com/NovusEdge/linear-agents/installer/internal/answers"
)

type Huh struct{}

func NewHuh() *Huh { return &Huh{} }

var (
	doneMark = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render("✓")
	todoMark = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render("•")
	dim      = lipgloss.NewStyle().Faint(true)
	heading  = lipgloss.NewStyle().Bold(true)
)

func (*Huh) Status(steps []StepStatus) {
	for _, s := range steps {
		mark := todoMark
		if s.Done {
			mark = doneMark
		}
		lipgloss.Println(fmt.Sprintf("%s %-14s %s", mark, s.Name, dim.Render(s.Detail)))
	}
}

func (*Huh) Note(title, body string) {
	huh.NewForm(huh.NewGroup(huh.NewNote().Title(title).Description(body))).Run()
}

func (*Huh) Confirm(title string, def bool) (bool, error) {
	v := def
	err := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title(title).Value(&v))).Run()
	return v, err
}

func (*Huh) Text(title, def string) (string, error) {
	v := def
	err := huh.NewForm(huh.NewGroup(huh.NewInput().Title(title).Value(&v))).Run()
	return v, err
}

func (*Huh) Secret(_, title, help string) (string, error) {
	var v string
	err := huh.NewForm(huh.NewGroup(huh.NewInput().Title(title).Description(help).
		EchoMode(huh.EchoModePassword).Value(&v))).Run()
	return v, err
}

func (*Huh) PickRedo(done []string) ([]string, error) {
	if len(done) == 0 {
		return nil, nil
	}
	var picked []string
	err := huh.NewForm(huh.NewGroup(huh.NewMultiSelect[string]().
		Title("Already done. Pick any to redo, or press enter to continue.").
		Options(huh.NewOptions(done...)...).Value(&picked))).Run()
	return picked, err
}

func (*Huh) Spin(title string, fn func() error) error {
	var err error
	if serr := spinner.New().Title(title).Action(func() { err = fn() }).Run(); serr != nil {
		return serr
	}
	return err
}

func (*Huh) ConfigForm(a *answers.Answers) error {
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Linear team key").Value(&a.TeamKey),
		huh.NewInput().Title("Digest issue key").Description("The pinned issue tier 3 reports go to, such as NF-186.").Value(&a.DigestIssue),
		huh.NewInput().Title("ntfy topic").Description("Subscribe to https://ntfy.sh/<topic> in the ntfy app. Keep it long and random.").Value(&a.NtfyTopic),
	)).Run(); err != nil {
		return err
	}
	for i := 0; ; i++ {
		if i == len(a.Repos) {
			more := false
			if err := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Map another repo?").Value(&more))).Run(); err != nil || !more {
				return err
			}
			a.Repos = append(a.Repos, answers.Repo{BaseBranch: "main", Check: "just check", Draft: true})
		}
		r := &a.Repos[i]
		prevURL := r.URL
		if err := huh.NewForm(huh.NewGroup(
			huh.NewNote().Title(heading.Render("Repo "+strconv.Itoa(i+1))),
			huh.NewInput().Title("GitHub URL").Description("https://github.com/<owner>/<repo>").Value(&r.URL).
				Validate(func(s string) error {
					if !answers.ValidGitHubURL(s) {
						return errors.New("use https://github.com/<owner>/<repo>")
					}
					return nil
				}),
		)).Run(); err != nil {
			return err
		}
		// A saved name may be a Linear label that differs from the repo name, so keep it while the URL stands.
		if r.Name == "" || r.URL != prevURL {
			r.Name = answers.NameFromURL(r.URL)
		}
		ok := true
		if err := huh.NewForm(huh.NewGroup(huh.NewConfirm().
			Title(fmt.Sprintf("Use name %s, base %s, check `%s`, draft PRs %s?", r.Name, r.BaseBranch, r.Check, yesNo(r.Draft))).
			Description("The name must equal the repo's Linear label.").Value(&ok))).Run(); err != nil {
			return err
		}
		if ok {
			continue
		}
		if err := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Name").Description("Exactly the Linear label that routes issues to it.").Value(&r.Name),
			huh.NewInput().Title("Base branch").Value(&r.BaseBranch),
			huh.NewInput().Title("Check command").Value(&r.Check),
			huh.NewConfirm().Title("Open draft PRs?").Description("Private repos of a GitHub Free organization cannot have drafts.").Value(&r.Draft),
		)).Run(); err != nil {
			return err
		}
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
