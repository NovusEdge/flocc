package ui

import (
	"errors"
	"fmt"
	"slices"
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

func (*Huh) ConfigForm(a *answers.Answers, found []answers.Repo, defaults func(url string) (base string, draft bool)) error {
	suggested := answers.RandomTopic()
	if err := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Linear team key").Value(&a.TeamKey),
		huh.NewInput().Title("Digest issue key").Description("The pinned issue tier 3 reports go to, such as NF-186.").Value(&a.DigestIssue),
		// A placeholder, not a value: pasting an existing topic into a prefilled field splices the two.
		huh.NewInput().Title("ntfy topic").
			Description("Subscribe to https://ntfy.sh/<topic> in the ntfy app. Leave empty to use the suggestion, or paste an existing topic.").
			Placeholder(suggested).
			Validate(func(s string) error { _, err := answers.NormalizeTopic(s); return err }).
			Value(&a.NtfyTopic),
	)).Run(); err != nil {
		return err
	}
	topic, err := answers.NormalizeTopic(a.NtfyTopic)
	if err != nil {
		return err
	}
	if a.NtfyTopic = topic; topic == "" {
		a.NtfyTopic = suggested
	}
	start := 0
	if len(found) > 0 {
		byURL := map[string]answers.Repo{}
		opts := make([]huh.Option[string], len(found))
		urls := answers.Preselected(found, a.ExcludedRepos)
		for i, r := range found {
			byURL[r.URL] = r
			opts[i] = huh.NewOption(r.Name, r.URL).Selected(slices.Contains(urls, r.URL))
		}
		for {
			if err := huh.NewForm(huh.NewGroup(huh.NewMultiSelect[string]().
				Title("Repos agents may work on").Description("Untick to exclude.").
				Validate(func(s []string) error {
					if len(s) == 0 {
						return errors.New("pick at least one repo")
					}
					return nil
				}).
				Options(opts...).Value(&urls))).Run(); err != nil {
				return err
			}
			if len(urls) <= maxRepos {
				break
			}
			ok := false
			if err := huh.NewForm(huh.NewGroup(huh.NewConfirm().
				Title(fmt.Sprintf("%d repos selected; each is cloned now and checked on every run. Continue?", len(urls))).
				Value(&ok))).Run(); err != nil {
				return err
			}
			if ok {
				break
			}
			for i, r := range found {
				opts[i] = opts[i].Selected(slices.Contains(urls, r.URL))
			}
		}
		a.ExcludedRepos = answers.UpdateExcluded(a.ExcludedRepos, found, urls)
		if len(urls) < len(found) {
			if err := huh.NewForm(huh.NewGroup(huh.NewConfirm().
				Title("Do other hosts handle the unticked repos?").
				Description("If yes, issues for unmapped repos get no comment from this host.").
				Value(&a.SharedTeam))).Run(); err != nil {
				return err
			}
		} else {
			a.SharedTeam = false
		}
		a.Repos = nil
		for _, u := range urls {
			a.Repos = append(a.Repos, byURL[u])
		}
		start = len(a.Repos)
	}
	for i := start; ; i++ {
		if i == len(a.Repos) {
			more := false
			title := "Map another repo?"
			if len(found) > 0 {
				title = "Add another repo by URL?"
			}
			if err := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title(title).Value(&more))).Run(); err != nil || !more {
				return err
			}
			a.Repos = append(a.Repos, answers.Repo{BaseBranch: "main", Check: "just check", Draft: true})
		}
		if err := editRepo(a.Repos, i, defaults); err != nil {
			return err
		}
		a.ExcludedRepos = answers.Include(a.ExcludedRepos, a.Repos[i].URL)
	}
}

// maxRepos is where the form stops to ask: every mapped repo is cloned in Apply and
// ls-remoted by doctor on each run.
const maxRepos = 30

func editRepo(repos []answers.Repo, i int, defaults func(url string) (string, bool)) error {
	r := &repos[i]
	prevURL := r.URL
	if err := huh.NewForm(huh.NewGroup(
		huh.NewNote().Title(heading.Render("Repo "+strconv.Itoa(i+1))),
		huh.NewInput().Title("GitHub URL").Description("https://github.com/<owner>/<repo>").Value(&r.URL).
			Validate(func(s string) error {
				if !answers.ValidGitHubURL(s) {
					return errors.New("use https://github.com/<owner>/<repo>")
				}
				for j, o := range repos {
					if j != i && answers.SameURL(o.URL, s) {
						return errors.New("already mapped as " + o.Name)
					}
				}
				return nil
			}),
	)).Run(); err != nil {
		return err
	}
	// A saved name may be a Linear label that differs from the repo name, so keep it while the URL stands.
	if r.Name == "" || r.URL != prevURL {
		// Taken before normalizing, which lowercases.
		r.Name = answers.NameFromURL(r.URL)
		r.URL = answers.NormalizeURL(r.URL)
	}
	if prevURL == "" {
		r.BaseBranch, r.Draft = defaults(r.URL)
	}
	ok := true
	if err := huh.NewForm(huh.NewGroup(huh.NewConfirm().
		Title(fmt.Sprintf("Use name %s, base %s, check `%s`, draft PRs %s?", r.Name, r.BaseBranch, r.Check, yesNo(r.Draft))).
		Description("The name must equal the repo's Linear label.").Value(&ok))).Run(); err != nil || ok {
		return err
	}
	return huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Name").Description("Exactly the Linear label that routes issues to it.").Value(&r.Name),
		huh.NewInput().Title("Base branch").Value(&r.BaseBranch),
		huh.NewInput().Title("Check command").Value(&r.Check),
		huh.NewConfirm().Title("Open draft PRs?").Description("Private repos of a GitHub Free organization cannot have drafts.").Value(&r.Draft),
	)).Run()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
