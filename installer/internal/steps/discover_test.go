package steps

import (
	"slices"
	"testing"

	"github.com/NovusEdge/linear-agents/installer/internal/answers"
)

func TestParseReposReadsConcatenatedPagesAndSkipsArchived(t *testing.T) {
	out := `[{"name":"a","html_url":"https://github.com/o/a","default_branch":"trunk","private":false,"size":5},
{"name":"old","html_url":"https://github.com/o/old","default_branch":"main","size":5,"archived":true},
{"name":"off","html_url":"https://github.com/o/off","default_branch":"main","size":5,"disabled":true},
{"name":"empty","html_url":"https://github.com/o/empty","default_branch":"main","size":0},
{"name":"nobranch","html_url":"https://github.com/o/nobranch","default_branch":null,"size":5},
{"name":"blank","html_url":"https://github.com/o/blank","default_branch":"","size":5}]
[{"name":"b","html_url":"https://github.com/o/b","default_branch":"main","private":true,"size":1}]`
	got, err := ParseRepos([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []answers.Repo{
		{Name: "a", URL: "https://github.com/o/a", BaseBranch: "trunk", Check: "just check", Draft: true},
		{Name: "b", URL: "https://github.com/o/b", BaseBranch: "main", Check: "just check", Draft: false},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%+v", got)
	}
	if _, err := ParseRepos([]byte(`{"message":"Bad credentials"}`)); err == nil {
		t.Fatal("an error object is not a repo list")
	}
}

func TestValidateRejectsDotNames(t *testing.T) {
	for _, n := range []string{".", ".."} {
		a := &answers.Answers{TeamKey: "NF", DigestIssue: "NF-1",
			Repos: []answers.Repo{{Name: n, URL: "https://github.com/o/r"}}}
		if validate(a) == nil {
			t.Errorf("name %q passed", n)
		}
	}
}

func TestParseRepoDefaultsFallsBackToMainAndDrafts(t *testing.T) {
	if b, d := ParseRepoDefaults([]byte(`{"default_branch":"trunk","private":true}`)); b != "trunk" || d {
		t.Fatalf("%q %v", b, d)
	}
	for _, in := range []string{`{"message":"Not Found"}`, `not json`, ``} {
		if b, d := ParseRepoDefaults([]byte(in)); b != "main" || !d {
			t.Fatalf("%q: %q %v", in, b, d)
		}
	}
}

func TestMergeReposKeepsSavedValuesAndUnseenSavedRepos(t *testing.T) {
	found := []answers.Repo{{Name: "a", URL: "u/a", BaseBranch: "main"}, {Name: "b", URL: "u/b", BaseBranch: "main"}}
	saved := []answers.Repo{{Name: "label-a", URL: "u/a", BaseBranch: "dev"}, {Name: "c", URL: "u/c"}, {}}
	got, skipped := MergeRepos(found, saved)
	if len(got) != 3 || got[0].Name != "label-a" || got[1].Name != "b" || got[2].Name != "c" || len(skipped) != 0 {
		t.Fatalf("%+v %v", got, skipped)
	}
}

func TestMergeReposKeepsTheFirstOfTwoReposWithOneName(t *testing.T) {
	found := []answers.Repo{{Name: "api", URL: "https://github.com/o1/api"}, {Name: "api", URL: "https://github.com/o2/api"}}
	got, skipped := MergeRepos(found, nil)
	if len(got) != 1 || got[0].URL != "https://github.com/o1/api" || !slices.Equal(skipped, []string{"https://github.com/o2/api"}) {
		t.Fatalf("%+v %v", got, skipped)
	}
}
