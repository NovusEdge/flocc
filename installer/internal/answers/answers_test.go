package answers

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExclusionsRoundTripAndDriveThePreselection(t *testing.T) {
	found := []Repo{{Name: "a", URL: "u/a"}, {Name: "b", URL: "u/b"}}
	excluded := UpdateExcluded(nil, found, []string{"u/a"})
	if !slices.Equal(excluded, []string{"u/b"}) {
		t.Fatalf("%v", excluded)
	}
	p := filepath.Join(t.TempDir(), "answers.toml")
	if err := (&Answers{ExcludedRepos: excluded}).Save(p); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	o := &Answers{}
	o.Overlay(saved)
	// c was created after the exclusion, so it arrives ticked.
	// Renaming b does not un-exclude it: the key is the URL.
	later := []Repo{{Name: "a", URL: "u/a"}, {Name: "b2", URL: "u/b"}, {Name: "c", URL: "u/c"}}
	if got := Preselected(later, o.ExcludedRepos); !slices.Equal(got, []string{"u/a", "u/c"}) {
		t.Fatalf("%v", got)
	}
	// Re-ticking b removes it; a URL not offered this time stays excluded.
	if got := UpdateExcluded([]string{"u/b", "gone"}, found, []string{"u/a", "u/b"}); !slices.Equal(got, []string{"gone"}) {
		t.Fatalf("%v", got)
	}
}

func TestNameFromURL(t *testing.T) {
	for u, want := range map[string]string{
		"https://github.com/o/r":      "r",
		"https://github.com/o/r.git":  "r",
		"https://github.com/o/r/":     "r",
		"https://github.com/o/r.git/": "r",
		"https://github.com/o":        "",
		"http://github.com/o/r":       "",
		"":                            "",
	} {
		if got := NameFromURL(u); got != want {
			t.Errorf("NameFromURL(%q) = %q, want %q", u, got, want)
		}
	}
}

func TestSaveNeverWritesSecretsAndIsMode600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "answers.toml")
	a := &Answers{TeamKey: "NF", Repos: []Repo{{Name: "r", URL: "https://github.com/o/r", BaseBranch: "main", Draft: false, Check: "just check"}},
		Secrets: &Secrets{LinearKey: "lin_api_x"}}
	if err := a.Save(p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "lin_api_x") || strings.Contains(string(b), "secrets") {
		t.Fatalf("secrets written: %s", b)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	back, err := Load(p)
	if err != nil || back.TeamKey != "NF" || back.Repos[0].Draft || back.Secrets != nil {
		t.Fatalf("round trip: %+v %v", back, err)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	a, err := Load(filepath.Join(t.TempDir(), "none.toml"))
	if err != nil || a.TeamKey != "" {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestOverlayKeepsUnsetFields(t *testing.T) {
	a := &Answers{TeamKey: "NF", DigestIssue: "NF-186"}
	a.Overlay(&Answers{DigestIssue: "NF-1", Secrets: &Secrets{AgentToken: "t"}})
	if a.TeamKey != "NF" || a.DigestIssue != "NF-1" || a.Secrets.AgentToken != "t" {
		t.Fatalf("%+v", a)
	}
}
