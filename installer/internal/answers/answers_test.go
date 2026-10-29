package answers

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSharedTeamSurvivesSaveLoadAndOverlay(t *testing.T) {
	p := filepath.Join(t.TempDir(), "answers.toml")
	if err := (&Answers{SharedTeam: true}).Save(p); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	o := &Answers{}
	if err := o.Overlay(saved); err != nil {
		t.Fatal(err)
	}
	if !o.SharedTeam {
		t.Fatal("shared_team lost")
	}
}

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
	if err := o.Overlay(saved); err != nil {
		t.Fatal(err)
	}
	// c was created after the exclusion, so it arrives ticked; b stays excluded
	// although its name differs, because exclusions are keyed by URL.
	later := []Repo{{Name: "a", URL: "u/a"}, {Name: "b2", URL: "u/b"}, {Name: "c", URL: "u/c"}}
	if got := Preselected(later, o.ExcludedRepos); !slices.Equal(got, []string{"u/a", "u/c"}) {
		t.Fatalf("%v", got)
	}
	// Re-ticking b removes it; a URL not offered this time stays excluded.
	if got := UpdateExcluded([]string{"u/b", "gone"}, found, []string{"u/a", "u/b"}); !slices.Equal(got, []string{"gone"}) {
		t.Fatalf("%v", got)
	}
}

func TestNormalizeURLMakesSpellingsOfOneRepoEqual(t *testing.T) {
	for _, u := range []string{"https://github.com/O/R", "https://github.com/o/r.git", "https://github.com/o/r/", "https://github.com/O/R.git/", " https://github.com/o/r "} {
		if got := NormalizeURL(u); got != "https://github.com/o/r" {
			t.Errorf("NormalizeURL(%q) = %q", u, got)
		}
	}
	if SameURL("https://github.com/o/r", "https://github.com/o/r2") {
		t.Error("different repos compared equal")
	}
}

func TestExclusionsMatchAcrossURLSpellings(t *testing.T) {
	found := []Repo{{Name: "R", URL: "https://github.com/O/R"}}
	if got := Preselected(found, []string{"https://github.com/o/r.git"}); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	if got := UpdateExcluded(nil, found, nil); !slices.Equal(got, []string{"https://github.com/o/r"}) {
		t.Fatalf("%v", got)
	}
	if got := Include([]string{"https://github.com/o/r"}, "https://github.com/o/r.git"); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	if got := Exclude([]string{"https://github.com/o/r"}, "https://github.com/O/R/"); len(got) != 1 {
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

// go-toml's own message quotes the offending line, and in an answers document that line can
// be a token.
func TestParseErrorsNeverQuoteTheDocument(t *testing.T) {
	for _, doc := range []string{
		"[secrets]\nlinear_key = \"lin_api_s3cret\n",
		"[secrets]\nagent_token = \"github_pat_s3cret\" junk\n",
		"[secrets]\npublish_token = [\"github_pat_s3cret\"]\n",
	} {
		_, err := Parse([]byte(doc))
		if err == nil || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%q: %v", doc, err)
		}
	}
	if _, err := Parse([]byte("[secrets]\nlinear_key = \"x\n")); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("want the line number, got %v", err)
	}
}

func TestParseReadsSecretsAndValuesSkipsEmptyOnes(t *testing.T) {
	a, err := Parse([]byte("team_key = \"NF\"\n[secrets]\nlinear_key = \"lin_api_x\"\npublish_token = \"github_pat_y\"\n"))
	if err != nil || a.TeamKey != "NF" {
		t.Fatalf("%+v %v", a, err)
	}
	if got := a.Secrets.Values(); !slices.Equal(got, []string{"lin_api_x", "github_pat_y"}) {
		t.Fatalf("%v", got)
	}
	if (*Secrets)(nil).Values() != nil {
		t.Fatal("a document without [secrets] has no values")
	}
	if a, err := Parse(nil); err != nil || a.Secrets != nil {
		t.Fatalf("empty document: %+v %v", a, err)
	}
}

func TestOverlayKeepsUnsetFields(t *testing.T) {
	a := &Answers{TeamKey: "NF", DigestIssue: "NF-186"}
	if err := a.Overlay(&Answers{DigestIssue: "NF-1", Secrets: &Secrets{AgentToken: "t"}}); err != nil {
		t.Fatal(err)
	}
	if a.TeamKey != "NF" || a.DigestIssue != "NF-1" || a.Secrets.AgentToken != "t" {
		t.Fatalf("%+v", a)
	}
}

func TestPermissionsTableParsesAndOverlays(t *testing.T) {
	a, err := Parse([]byte("team_key = \"NF\"\n[permissions]\nallow = [\"Bash(uv sync --frozen)\"]\njudge_model = \"sonnet\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Permissions == nil || a.Permissions.Allow[0] != "Bash(uv sync --frozen)" || a.Permissions.JudgeModel != "sonnet" {
		t.Fatalf("permissions = %+v", a.Permissions)
	}
	b := &Answers{}
	if err := b.Overlay(a); err != nil || b.Permissions != a.Permissions {
		t.Fatalf("overlay lost permissions: %+v, %v", b.Permissions, err)
	}
}

func TestTailscaleAuthKeyIsASecretAndNeverSaved(t *testing.T) {
	a := &Answers{Secrets: &Secrets{TailscaleAuthKey: "tskey-auth-k1-x"}}
	if got := a.Secrets.Values(); !slices.Equal(got, []string{"tskey-auth-k1-x"}) {
		t.Errorf("values %v", got)
	}
	path := filepath.Join(t.TempDir(), "answers.toml")
	if err := a.Save(path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "tskey") {
		t.Errorf("saved:\n%s", b)
	}
	back, err := Parse([]byte("[secrets]\ntailscale_auth_key = \"tskey-auth-k1-x\"\n"))
	if err != nil || back.Secrets.TailscaleAuthKey != "tskey-auth-k1-x" {
		t.Errorf("%v %+v", err, back.Secrets)
	}
}
