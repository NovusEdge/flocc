package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/NovusEdge/flocc/installer/internal/answers"
	"github.com/NovusEdge/flocc/installer/internal/hosts"
)

func TestKeysRefusesAKeyOnTheCommandLine(t *testing.T) {
	d, _, errb := testDeps(t)
	for _, arg := range []string{"github_pat_x", "lin_api_x", "ghp_x"} {
		if code := run(context.Background(), d, []string{"keys", arg}); code != 2 || !strings.Contains(errb.String(), "shell history") {
			t.Fatalf("%s: exit %d %q", arg, code, errb.String())
		}
	}
}

func TestReportIndentsEachFailAndPutsTheNextStepUnderIt(t *testing.T) {
	d, _, errb := testDeps(t)
	code := report(d, []string{"Linear key: Linear rejected the key; run flocc keys rotate linear", "ntfy topic: missing; run flocc keys"})
	want := "  FAIL  Linear key: Linear rejected the key\n    run flocc keys rotate linear\n  FAIL  ntfy topic: missing\n    run flocc keys\n"
	if code != 1 || errb.String() != want {
		t.Fatalf("%d %q", code, errb.String())
	}
}

func TestMappedJoinsTheHostsTeamsReposAndDigests(t *testing.T) {
	r := func(u, base string) answers.Repo { return answers.Repo{URL: u, BaseBranch: base} }
	m := mapped([]hosts.Host{
		{Name: "a", Answers: answers.Answers{TeamKey: "NF", DigestIssue: "NF-7", Repos: []answers.Repo{r("https://github.com/O/A.git", "main")}}},
		{Name: "b", Answers: answers.Answers{TeamKey: "NF", DigestIssue: "NF-7", Repos: []answers.Repo{r("https://github.com/o/a", "main"), r("https://github.com/o/b", "trunk")}}},
	})
	if strings.Join(m.Teams, ",") != "NF" || len(m.Repos) != 2 || m.Repos[0].Slug != "o/a" || m.Repos[1].Base != "trunk" ||
		strings.Join(m.Digests, ",") != "NF-7" {
		t.Fatalf("%+v", m)
	}
}
